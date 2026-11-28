package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Af2 r2 #8, #9, #16 and #17. Preserve the actual native tool identity and
// scope from the stored steps; both catalogs and the report locator have no
// observations. Trim unrelated metadata and duplicate textual output.
func af3Round2DirectoryReplay(t *testing.T) af3KB28Replay {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/af3-kb28-r2.json")
	require.NoError(t, err)
	defer file.Close()
	var trace af3KB28Replay
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&trace))
	require.Equal(t, "e3c848fd732ed0d08b1e88dbecdba6b4a901d38e277f0ed03f87765ea9ea5890", trace.SSEHash)
	require.Equal(t, "e497bd03714c5203cb4f5173428ee5706817139683ef930bb1d7c6a2dc3abd11", trace.StepsHash)
	require.Empty(t, trace.Final)
	return trace
}

type af3ModelBudgetReplay struct {
	*mockChat
	beforeBudget func()
	beforeFinal  func()
	finalErr     error
	calls        int
}

func (m *af3ModelBudgetReplay) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	m.calls++
	if m.calls == 1 {
		m.beforeBudget()
		return nil, fmt.Errorf("create chat completion stream: %w", errStandardAnswerModelBudget)
	}
	if m.beforeFinal != nil {
		m.beforeFinal()
	}
	if m.finalErr != nil {
		return nil, m.finalErr
	}
	return m.mockChat.ChatStream(ctx, messages, opts)
}

func af3CollaborationReplayStore(t *testing.T) (*gorm.DB, *collaboration.Binding) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "collaboration.db")), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sql.Close()) })
	require.NoError(t, db.AutoMigrate(&collaboration.Record{}))
	runtime := collaboration.New(collaboration.NewStore(db), nil)
	binding, err := runtime.AcquireParent(t.Context(), collaboration.Scope{TenantID: 1, UserID: "owner", SessionID: "s", ID: "m"})
	require.NoError(t, err)
	return db, binding
}

func af3ProjectStaleLease(t *testing.T, db *gorm.DB, remaining *atomic.Int32) {
	t.Helper()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("af3:test_stale_lease", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*collaboration.Record)
		if !ok || row.ID == "" {
			return
		}
		count := remaining.Load()
		if count <= 0 || !remaining.CompareAndSwap(count, count-1) {
			return
		}
		var snapshot collaboration.Group
		require.NoError(t, json.Unmarshal([]byte(row.Data), &snapshot))
		snapshot.ParentLease = time.Now().Add(-time.Millisecond)
		body, err := json.Marshal(&snapshot)
		require.NoError(t, err)
		row.Data, row.Version = string(body), row.Version-1
	}))
}

func af3ConflictOnCompletion(t *testing.T, db *gorm.DB, rejected int32, loseAcknowledgment bool) (*atomic.Int32, *atomic.Int32) {
	t.Helper()
	var attempts, locked atomic.Int32
	isCompletion := func(tx *gorm.DB) bool {
		values, ok := tx.Statement.Dest.(map[string]any)
		return ok && values["status"] == "completed"
	}
	isOptimistic := func(tx *gorm.DB) bool {
		where, _ := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
		for _, condition := range where.Exprs {
			if sql, ok := condition.(clause.Expr); ok && strings.Contains(sql.SQL, "version = ?") {
				return true
			}
		}
		return false
	}
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("af3:conflicting_completion", func(tx *gorm.DB) {
		if !isCompletion(tx) {
			return
		}
		if !isOptimistic(tx) {
			locked.Add(1)
			return
		}
		if attempts.Add(1) <= rejected {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
		}
	}))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("af3:completion_acknowledgment", func(tx *gorm.DB) {
		if loseAcknowledgment && isCompletion(tx) && isOptimistic(tx) && tx.RowsAffected == 1 {
			tx.RowsAffected = 0
		}
	}))
	return &attempts, &locked
}

// Replay the first observable budget boundary in Af2 r2, including the
// short-lived same-owner stale lease interleaving. The database/log have no
// earlier row revisions, so the precise historical interleaving is unknown.
func TestAf3Af2Round2CollaborationLeaseRefresh(t *testing.T) {
	for _, mode := range []string{"legacy", "strict terminal", "relaxed terminal", "Af5 default", "substantive terminal"} {
		t.Run(mode, func(t *testing.T) {
			substantive := mode == "substantive terminal"
			af5 := mode == "Af5 default" || substantive
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", strconv.FormatBool(mode != "legacy"))
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", strconv.FormatBool(mode == "relaxed terminal" || af5))
			finalRead := "false"
			if af5 {
				finalRead = ""
			}
			t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", finalRead)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", strconv.FormatBool(substantive))
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "false")
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
			require.Equal(t, af5, finalReadAnswerEnabled())
			trace := af3Round2DirectoryReplay(t)
			db, binding := af3CollaborationReplayStore(t)
			var staleReads atomic.Int32
			af3ProjectStaleLease(t, db, &staleReads)
			var updates, locked *atomic.Int32
			if af5 {
				updates, locked = af3ConflictOnCompletion(t, db, 3, false)
			}
			model := &af3ModelBudgetReplay{
				mockChat:     &mockChat{},
				beforeBudget: func() { staleReads.Store(1) },
				finalErr:     fmt.Errorf("reserved synthesis produced no candidate for the directory-only scope"),
			}
			engine, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model.mockChat)
			engine.chatModel = model
			engine.SetCollaboration(binding)
			record, _ := engine.terminalCoverageRecord(ctx, state)
			require.NotEmpty(t, record.Evidence)
			for _, item := range record.Evidence {
				require.Contains(t, []string{"directory", "navigation"}, item.Kind)
			}
			var complete event.AgentCompleteData
			var completions int
			var streamed string
			engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				completions++
				return nil
			})
			if af5 {
				engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
					answer := item.Data.(event.AgentFinalAnswerData)
					if answer.Replace {
						streamed = ""
					}
					streamed += answer.Content
					return nil
				})
			}
			_, err := engine.executeLoop(ctx, state, trace.Question, messages, engine.buildToolsForLLM(), "s", "m")
			require.Zero(t, staleReads.Load(), "the budget handoff must see the projected stale lease")
			if mode == "legacy" {
				require.ErrorIs(t, err, collaboration.ErrConflict)
				require.Empty(t, complete.FinalAnswer, "matches Af2 r2 #22")
				require.Equal(t, 1, model.calls, "the old execution aborts before budgeted synthesis")
				return
			}
			require.NoError(t, err)
			require.Equal(t, 2, model.calls, "lease refresh must not trigger an additional model round")
			require.NotNil(t, complete.Incomplete)
			if af5 {
				require.Equal(t, 1, completions)
				require.EqualValues(t, 3, updates.Load(), "the optimistic SQLite completion writes all hit the conflict")
				require.EqualValues(t, 1, locked.Load(), "the current owner completed under the locked finish")
				require.NotEmpty(t, complete.FinalAnswer)
				require.True(t, complete.HasPartialAnswer())
				require.Equal(t, string(terminalCollaborationContention), complete.Incomplete.Reason)
				require.Equal(t, complete.FinalAnswer, complete.Incomplete.Notice)
				require.Equal(t, complete.FinalAnswer, streamed)
				if substantive {
					require.Contains(t, complete.FinalAnswer, "中国棉花平衡表期末库存 2023-10-01 – 2023-10-31")
				} else {
					require.Contains(t, complete.FinalAnswer, trace.Question)
				}
				require.Contains(t, complete.FinalAnswer, "来源目录查询已执行")
				for _, absent := range []string{"331.2", "298.93", "32.27", "高于", "低于", "已生成的草稿未覆盖", "尚缺", "<evidence", "<kb"} {
					require.NotContains(t, complete.FinalAnswer, absent)
				}
				group, err := binding.Runtime.Store.Read(ctx, binding.Scope)
				require.NoError(t, err)
				require.Equal(t, "completed", group.Status)
				require.Equal(t, binding.ParentOwner, group.ParentOwner)
				return
			}
			if mode == "relaxed terminal" {
				require.Empty(t, complete.FinalAnswer)
				require.False(t, complete.HasPartialAnswer(), "navigation alone does not establish a comparison")
				require.NotEmpty(t, complete.Incomplete.Notice)
				return
			}
			require.NotEmpty(t, complete.FinalAnswer)
			require.True(t, complete.HasPartialAnswer(), "navigation-only progress is visible without claiming the reading was completed")
			require.Contains(t, complete.FinalAnswer, "棉花数据库.xlsx")
			require.NotContains(t, complete.FinalAnswer, "331.2")
			require.NotContains(t, complete.FinalAnswer, "298.93")
		})
	}
}

func TestAf3CollaborationVerifiedReceiptsAfterBudgetConflict(t *testing.T) {
	for _, scenario := range []struct {
		name               string
		reject             int32
		loseAcknowledgment bool
		locked             int32
	}{
		{"commit acknowledgment lost", 2, true, 0},
		{"all optimistic writes rejected", 3, false, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "true")
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
			require.True(t, finalReadAnswerEnabled())
			trace := loadAf3KB28Replay(t, 1)
			db, binding := af3CollaborationReplayStore(t)
			var staleReads atomic.Int32
			af3ProjectStaleLease(t, db, &staleReads)
			updates, locked := af3ConflictOnCompletion(t, db, scenario.reject, scenario.loseAcknowledgment)
			model := &af3ModelBudgetReplay{mockChat: &mockChat{}, beforeBudget: func() { staleReads.Store(2) },
				finalErr: fmt.Errorf("reserved final synthesis returned no candidate")}
			engine, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model.mockChat)
			engine.chatModel = model
			engine.SetCollaboration(binding)
			var complete event.AgentCompleteData
			var completions int
			engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				completions++
				return nil
			})
			_, err := engine.executeLoop(ctx, state, trace.Question, messages, engine.buildToolsForLLM(), "s", "m")
			require.NoError(t, err)
			require.Equal(t, 2, model.calls, "one decision and its already reserved final call, with no conflict-triggered model calls")
			require.EqualValues(t, 3, updates.Load())
			require.Equal(t, scenario.locked, locked.Load())
			require.Equal(t, 1, completions)
			require.Nil(t, complete.Incomplete)
			require.Contains(t, complete.FinalAnswer, "331.2")
			require.Contains(t, complete.FinalAnswer, "298.93")
			require.Contains(t, complete.FinalAnswer, "G87")
			require.Contains(t, complete.FinalAnswer, "棉花周报20240331.pptx")
			require.Contains(t, complete.FinalAnswer, "棉花数据库.xlsx")
			group, err := binding.Runtime.Store.Read(ctx, binding.Scope)
			require.NoError(t, err)
			require.Equal(t, "completed", group.Status)
			require.Equal(t, binding.ParentOwner, group.ParentOwner)
		})
	}
}

func TestAf3CollaborationFinishFencesNewInputAndRevokedPublication(t *testing.T) {
	for _, fault := range []string{"stale owner", "new message", "expired lease", "cancelled", "late steer", "steer at locked finish"} {
		t.Run(fault, func(t *testing.T) {
			db, binding := af3CollaborationReplayStore(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var updates, locked *atomic.Int32
			var expected = collaboration.ErrConflict
			switch fault {
			case "stale owner", "new message", "expired lease":
				require.NoError(t, binding.Runtime.Store.Change(ctx, binding.Scope, func(g *collaboration.Group) error {
					switch fault {
					case "stale owner":
						g.ParentOwner = "replacement"
					case "new message":
						g.ParentMessageID = "superseding-turn"
					case "expired lease":
						g.ParentLease = time.Now().Add(-time.Second)
					}
					return nil
				}))
				recovered, err := binding.CheckPublicationLease(ctx, "m")
				require.ErrorIs(t, err, collaboration.ErrConflict)
				require.False(t, recovered)
			case "cancelled":
				cancel()
				expected = context.Canceled
			case "late steer", "steer at locked finish":
				updates, locked = af3ConflictOnCompletion(t, db, 3, false)
				threshold := int32(3)
				if fault == "late steer" {
					threshold = 1
				}
				binding.HasInput = func(context.Context) (bool, error) { return updates.Load() >= threshold, nil }
				expected = collaboration.ErrInput
			}
			done, recovered, err := binding.FinishPublication(ctx, "m")
			require.ErrorIs(t, err, expected)
			require.False(t, done)
			require.False(t, recovered)
			if fault == "steer at locked finish" {
				require.EqualValues(t, 3, updates.Load())
				require.Zero(t, locked.Load(), "the transaction checks steering before the terminal write")
			} else if fault == "late steer" {
				require.EqualValues(t, 1, updates.Load(), "a new human correction wins before the next CAS")
			}
			group, err := binding.Runtime.Store.Read(t.Context(), binding.Scope)
			require.NoError(t, err)
			require.NotEqual(t, "completed", group.Status)
		})
	}
}

func TestAf3CollaborationRecoveryCannotPublishAfterCancellationOrScopeLoss(t *testing.T) {
	for _, fault := range []string{"cancellation", "owner changed", "source revoked"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			trace := loadAf3KB28Replay(t, 1)
			db, binding := af3CollaborationReplayStore(t)
			var staleReads atomic.Int32
			af3ProjectStaleLease(t, db, &staleReads)
			model := &af3ModelBudgetReplay{mockChat: &mockChat{}, beforeBudget: func() { staleReads.Store(1) },
				finalErr: fmt.Errorf("reserved final synthesis returned no candidate")}
			engine, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model.mockChat)
			engine.chatModel = model
			engine.SetCollaboration(binding)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			switch fault {
			case "cancellation":
				model.beforeFinal = cancel
			case "owner changed":
				model.beforeFinal = func() {
					require.NoError(t, binding.Runtime.Store.Change(ctx, binding.Scope, func(g *collaboration.Group) error {
						g.ParentOwner = "replacement"
						g.ParentMessageID = "replacement-message"
						return nil
					}))
				}
			case "source revoked":
				ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"other-library"}})
			}
			var complete event.AgentCompleteData
			var completions int
			engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				completions++
				return nil
			})
			_, err := engine.executeLoop(ctx, state, trace.Question, messages, engine.buildToolsForLLM(), "s", "m")
			require.Equal(t, 2, model.calls, "the loss occurs after the regular reserved model stage is entered")
			switch fault {
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
				require.Empty(t, complete.FinalAnswer)
			case "owner changed":
				require.ErrorIs(t, err, collaboration.ErrConflict)
				require.Zero(t, completions, "publication belongs to the replacement owner")
			case "source revoked":
				require.NoError(t, err)
				require.NotContains(t, complete.FinalAnswer, "331.2")
				require.NotContains(t, complete.FinalAnswer, "298.93")
				require.NotContains(t, complete.FinalAnswer, "G87")
				require.Empty(t, complete.EvidenceRefs)
				require.Empty(t, complete.KnowledgeRefs)
			}
		})
	}
}
