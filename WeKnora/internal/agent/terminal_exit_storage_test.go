package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTerminalEngineExitsKeepVisibleCompletionAndStoredAnswerTogether(t *testing.T) {
	const sourceDraft = "Equipment.xlsx reports 47.25 on 2021-06-03, from B15."
	for _, flag := range []string{"true", "false"} {
		for _, outlet := range []string{"collaboration parent", "parent citation", "iteration budget", "verification failure", "authorization revoked"} {
			t.Run(flag+"/"+outlet, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", flag)
				ctx := withAgentExecutionStart(t.Context())
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				require.NoError(t, err)
				conn, err := db.DB()
				require.NoError(t, err)
				conn.SetMaxOpenConns(1)
				t.Cleanup(func() { require.NoError(t, conn.Close()) })
				require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
				repo := repository.NewMessageRepository(db)
				message, err := repo.CreateMessage(ctx, &types.Message{SessionID: "s", Role: "assistant"})
				require.NoError(t, err)
				model := &mockChat{}
				engine := newTestEngine(t, model, withCitationsEnabled(true))
				engine.config.WorkbenchDefault = true
				engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "allowed-library", TenantID: 7}}
				engine.toolRegistry = qa4ReplayRegistry()
				observed := terminalLatestSeriesCall()
				if outlet == "verification failure" {
					observed.Result.Data["actual_first_date"] = "2021-06-03"
					original, err := json.Marshal(observed.Result.Data)
					require.NoError(t, err)
					observed.Result.Output = string(original)
				}
				state := &types.AgentState{RoundSteps: []types.AgentStep{{Iteration: -1, ToolCalls: []types.ToolCall{observed}}}}
				question := "What elapsed time did the equipment register report?"
				if outlet == "parent citation" {
					engine, state, _ = directCitationReadFixture(t, model, true)
					engine.config.AnswerReviewMode = "standard"
					engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "contract-library", TenantID: 7}}
					engine.toolRegistry = agenttools.NewToolRegistry()
					engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(nil, nil, engine.config.SearchTargets))
					question = "What does the indicator measure?"
				}
				messages := []chat.Message{{Role: "system", Content: "Report verified findings."}, {Role: "user", Content: question}}
				messages = engine.appendToolResults(messages, state.RoundSteps[0])
				var events []event.Event
				engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
					events = append(events, item)
					return nil
				})
				engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
					events = append(events, item)
					return nil
				})
				switch outlet {
				case "collaboration parent", "parent citation":
					runtime, seed := engineCollaboration(t)
					binding, err := runtime.AcquireParent(ctx, seed.Scope)
					require.NoError(t, err)
					engine.SetCollaboration(binding)
					if outlet == "parent citation" {
						handle := engine.modelContext.CitableChunkHandle("contract-supporting")
						require.NotEmpty(t, handle)
						model.responses = []mockResponse{chatAnswer(`The indicator measures covered area.<ref id="` + handle + `"/>`)}
					} else {
						model.responses = []mockResponse{chatAnswer(answerReviewUnavailableFallback)}
					}
					_, err = engine.executeLoop(ctx, state, question, messages, nil, message.SessionID, message.ID)
					require.NoError(t, err)
					group, err := runtime.Store.Read(ctx, binding.Scope)
					require.NoError(t, err)
					require.Equal(t, "completed", group.Status)
				case "iteration budget":
					state.CurrentRound = engine.config.MaxIterations
					_, err = engine.executeLoop(ctx, state, question, messages, nil, message.SessionID, message.ID)
					require.ErrorContains(t, err, "failed to synthesize final answer")
					require.Equal(t, terminalBudget, engine.turnReview.TerminalReceipt.failure)
				case "verification failure":
					engine.config.AnswerReviewMode, engine.config.TaskPlanningEnabled = "strict", true
					model.responses = []mockResponse{chatAnswer(sourceDraft), chatAnswer("invalid evaluator result"), chatAnswer("invalid evaluator retry")}
					_, err = engine.executeLoop(ctx, state, question, messages, nil, message.SessionID, message.ID)
					require.NoError(t, err)
					require.Equal(t, candidateProtocolFailure, engine.turnReview.Last.Disposition)
					require.ErrorContains(t, engine.turnReview.Last.Err, "answer review unavailable")
				case "authorization revoked":
					engine.turnReview.Query = question
					engine.turnReview.TerminalSensitive, engine.turnReview.DeferTerminalClose = true, true
					const revokedDraft = "Equipment.xlsx reports 47.25 on 2021-06-03."
					state.RoundSteps = append(state.RoundSteps, types.AgentStep{Thought: revokedDraft})
					engine.commitTerminalResult(ctx, state, message.SessionID, revokedDraft, "", "answer", false)
					require.Empty(t, events)
					engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "other-library", TenantID: 7}}
					engine.emitCompletionEvent(ctx, state, message.SessionID, message.ID, time.Now())
				}
				require.True(t, state.IsComplete)
				require.Nil(t, state.Suspended)
				require.GreaterOrEqual(t, len(events), 3)
				lastAnswer := events[len(events)-2]
				require.Equal(t, event.EventAgentFinalAnswer, lastAnswer.Type)
				require.True(t, lastAnswer.Data.(event.AgentFinalAnswerData).Done)
				last := events[len(events)-1]
				require.Equal(t, event.EventAgentComplete, last.Type)
				complete := last.Data.(event.AgentCompleteData)
				require.Nil(t, complete.Suspended)
				require.Equal(t, message.ID, complete.MessageID)
				var visible strings.Builder
				var first string
				var closes int
				for _, item := range events[:len(events)-1] {
					require.Equal(t, event.EventAgentFinalAnswer, item.Type)
					require.Equal(t, lastAnswer.ID, item.ID)
					payload := item.Data.(event.AgentFinalAnswerData)
					if payload.Done {
						closes++
						continue
					}
					if first == "" && payload.Content != "" {
						first = payload.Content
					}
					if payload.Replace {
						visible.Reset()
					}
					visible.WriteString(payload.Content)
				}
				require.Equal(t, 1, closes)
				require.Equal(t, state.FinalAnswer, complete.FinalAnswer)
				require.Equal(t, complete.FinalAnswer, visible.String())
				require.Equal(t, visible.String(), first)
				require.False(t, terminalCandidateFallback(first))
				require.NotContains(t, first, "invalid evaluator")
				switch outlet {
				case "authorization revoked":
					require.NotContains(t, first, "47.25")
					require.NotContains(t, first, "Equipment.xlsx")
				case "parent citation":
					require.Equal(t, "The indicator measures covered area.", documentAnswerCitation.ReplaceAllString(first, ""))
					require.Equal(t, []string{"contract-supporting"}, uniqueCandidateCitationIDs(first))
				default:
					for _, fact := range []string{"Equipment.xlsx", "47.25", "2021-06-03", "B15"} {
						require.Contains(t, first, fact)
					}
				}

				message.Content, message.IsCompleted = complete.FinalAnswer, state.IsComplete
				message.EvidenceReferences = types.EvidenceReferencesFromInternal(complete.EvidenceRefs)
				for _, item := range complete.KnowledgeRefs {
					ref, ok := item.(*types.SearchResult)
					require.True(t, ok)
					message.KnowledgeReferences = append(message.KnowledgeReferences, ref)
				}
				steps, ok := complete.AgentSteps.([]types.AgentStep)
				require.True(t, ok)
				message.AgentSteps = agenttools.SanitizeAgentStepsForStorage(steps)
				require.NoError(t, repo.UpdateMessage(ctx, message))
				stored, err := repo.GetMessage(ctx, message.SessionID, message.ID)
				require.NoError(t, err)
				require.True(t, stored.IsCompleted)
				require.Empty(t, stored.RunState)
				require.Equal(t, []byte(complete.FinalAnswer), []byte(stored.Content))
				if outlet == "parent citation" {
					require.Len(t, complete.KnowledgeRefs, 1)
					require.Len(t, stored.KnowledgeReferences, 1)
					require.Equal(t, "contract-supporting", stored.KnowledgeReferences[0].ID)
				} else {
					require.Empty(t, stored.KnowledgeReferences)
				}
				require.Empty(t, stored.EvidenceReferences)
				if outlet == "authorization revoked" {
					serializedSteps, err := json.Marshal(stored.AgentSteps)
					require.NoError(t, err)
					require.NotContains(t, string(serializedSteps), "47.25")
					require.NotContains(t, string(serializedSteps), "Equipment.xlsx")
				}
			})
		}
	}
}
