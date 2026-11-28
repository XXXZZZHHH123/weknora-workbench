package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Archived Af2 traces (1-based SSE ordinals). R1 keeps #15 (bound
// page), #21 (workbook observation) and #28 (same cell via direct SQL).
// R3 keeps every source observation (#6 and #7), including decoys.
// The original SQLite Steps and SSE hashes are stored with each projection.
// SSE and SQLite omit page text. R1 restores only the two cited texts,
// keeping the exact Af1 read's JSON pointers and checking its source hash,
// title chunk, OCR chunk, object ID and native OCR text against the archive.
// Reconstruct the relevant plan fields from the truncated planner logs
// (135334 and 139391) and the request/terminal gap, without adding receipts.
type af3KB28Replay struct {
	Question  string
	Steps     []types.AgentStep
	Document  *types.Knowledge
	Chunks    []*types.Chunk
	Final     string
	Notice    string
	SSEHash   string            `json:"sse_sha256"`
	StepsHash string            `json:"steps_sha256"`
	TextHash  map[string]string `json:"restored_text_sha256"`
}

const af3KB28Library = "0d463b67-5640-4b94-84d1-8bbb426c3bcd"

func loadAf3KB28Replay(t *testing.T, run int) af3KB28Replay {
	t.Helper()
	file, err := os.ReadFile(fmt.Sprintf("testdata/qa4/replay/af3-kb28-r%d.json", run))
	require.NoError(t, err)
	var trace af3KB28Replay
	decoder := json.NewDecoder(bytes.NewReader(file))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&trace))
	if run == 1 {
		require.Equal(t, "fdd3829cb4d5b4df1424c31025c1c99ab86e371ed2adc47fc6f4a82625c89cf3", trace.SSEHash)
		require.Equal(t, "9ac39bc53328230479c4ded401fd0b79671398e583a5df16cf0a59e7d78794dd", trace.StepsHash)
		page := trace.Steps[0].ToolCalls[0].Result.Data
		title := sourceNavigationString(sourceNavigationRows(page["chunks"])[0], "content")
		ocr := sourceNavigationString(sourceNavigationRows(page[types.DocumentImageTextEvidenceKey])[1], "text")
		require.Equal(t, trace.TextHash["title"], candidateTextDigest(title))
		require.Equal(t, trace.TextHash["ocr"], candidateTextDigest(ocr))
	} else {
		require.Equal(t, "74d1589dcebde6346ba03c27e89471f11231a218f39fb6430279c15fa7a4db43", trace.SSEHash)
		require.Equal(t, "603ae11d7d8e2c206f55d25c76938c75dd6194cb767b82dd4ceedf90982a3528", trace.StepsHash)
	}
	return trace
}

func newAf3KB28ReplayEngine(t *testing.T, trace af3KB28Replay, model *mockChat, documentReplay ...af3KB28Replay) (*AgentEngine, *types.AgentState, context.Context, []chat.Message) {
	t.Helper()
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
	ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "qa4-replay-reader", Role: types.TenantRoleViewer}))
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: af3KB28Library, TenantID: 1}}
	e := newTestEngine(t, model, withCitationsEnabled(true), withMaxIterations(20))
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled, e.config.EvidenceReferencesEnabled = true, true, true
	e.config.AnswerReviewMode, e.config.SearchTargets = "standard", targets
	e.toolRegistry = agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool().WithSearchTargets(targets)
	e.toolRegistry.RegisterTool(base)
	e.toolRegistry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, raw := range manifest["tools"].([]any) {
		entry := raw.(map[string]any)
		if entry["name"] == "query_series" {
			schema := entry["inputSchema"].(map[string]any)
			fields := schema["properties"].(map[string]any)
			for _, name := range []string{"dataset_id", "series_id", "start_date", "end_date", "operation", "source_binding"} {
				fields[name] = map[string]any{}
			}
		}
	}
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, manifest) {
		e.toolRegistry.RegisterTool(tool)
	}
	store := &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}}
	var chunks *qa4Stage2CChunks
	if len(documentReplay) > 0 {
		native := documentReplay[0]
		store.byID[native.Document.ID] = native.Document
		chunks = &qa4Stage2CChunks{repo: &qa4Stage2CChunkRepository{rows: native.Chunks}}
	}
	e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(store, chunks, targets))
	e.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, store, nil, targets, nil, nil))
	agenttools.PrepareDocumentScope(e.toolRegistry, trace.Question)
	if trace.Document != nil {
		doc := trace.Document
		store.byID[doc.ID] = doc
		binder, err := e.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
		require.NoError(t, err)
		args, err := json.Marshal(agenttools.DocumentScopePlan{Reason: "Restore the exact document selected by the archived bound read.",
			Sources: []agenttools.DocumentSourceSelection{{KnowledgeID: doc.ID, FileName: doc.FileName, Title: doc.Title}}})
		require.NoError(t, err)
		result, err := binder.Execute(ctx, args)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
	}
	goal := &StructuredTaskGoal{SourceMode: "open", SourceTime: "observed_period", VersionTask: "compare_sources",
		ComparisonSources: []string{"3月31日周报", "棉花数据库"}, Table: "中国棉花平衡表", Metric: "工商业期末库存",
		Period: map[string]any{"from": "2023-10-01", "to": "2023-10-31", "grain": "month"}, Required: []string{"scoped_value"}}
	if trace.Document == nil {
		// The actual missing reading names this combined metric (SSE #18).
		goal.Metric = "中国棉花平衡表期末库存"
		goal.Table, goal.VersionTask, goal.ComparisonSources = "", "", nil
	}
	e.taskPlan = &taskEvidencePlan{Goals: []string{goal.Metric, "Compare verified source values"}, Evidence: []string{trace.Question},
		SourceDiscovery:      &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured", "knowledge"}},
		DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "prose"}},
		CoverageTargets:      []taskCoverageTarget{{RequestBasis: trace.Question, StructuredTaskGoal: goal}}}
	state := &types.AgentState{RoundSteps: trace.Steps}
	if len(trace.Steps) > 0 {
		state.CurrentRound = max(0, trace.Steps[len(trace.Steps)-1].Iteration+1)
	}
	messages := []chat.Message{{Role: "user", Content: trace.Question}}
	e.reviewConversation, e.reviewConversationReady = messages, true
	e.turnReview.Query, e.turnReview.Conversation = trace.Question, messages
	e.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
	return e, state, ctx, messages
}

func TestQA4Af3HistoricalWeakComparison(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
	trace := loadAf3KB28Replay(t, 1)
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprint(reversed), func(t *testing.T) {
			trace.Steps = slices.Clone(trace.Steps)
			if reversed {
				slices.Reverse(trace.Steps)
			}
			e, state, ctx, _ := newAf3KB28ReplayEngine(t, trace, &mockChat{})
			e.commitTerminalResult(ctx, state, "run", "", "", "failed", false, terminalTableEvidence)
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				return nil
			})
			e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			require.Equal(t, trace.Final, complete.FinalAnswer)
			require.Contains(t, complete.FinalAnswer, "298.93291576470585")
			require.NotContains(t, complete.FinalAnswer, "32.27")
			t.Logf("legacy terminal comparison: %s", complete.FinalAnswer)
		})
	}
}

func TestQA4Af3HistoricalSourceTimeout(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
	trace := loadAf3KB28Replay(t, 3)
	// The three private provider streams were omitted from SQLite/SSE.
	// Reproduce their observed disposition with a catalog-only model double.
	const draft = "本轮看到了文档和工作簿目录，未读取两份材料中的期末库存。"
	model := &mockChat{responses: []mockResponse{chatAnswer(draft), chatAnswer(draft), chatAnswer(draft)}}
	e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model)
	require.True(t, e.standardAnswerBudgetEligible(ctx, state))
	record, _ := e.terminalCoverageRecord(ctx, state)
	require.NotEmpty(t, record.Evidence)
	for _, item := range record.Evidence {
		require.Contains(t, []string{"directory", "navigation"}, item.Kind)
	}
	done, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "run", "turn", &messages, &loopGuards{}, "model_call_time")
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, string(terminalAnswerEvidence), e.turnReview.Last.ReasonCode)
	var complete event.AgentCompleteData
	e.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
		complete = item.Data.(event.AgentCompleteData)
		return nil
	})
	e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
	require.Empty(t, complete.FinalAnswer)
	require.NotNil(t, complete.Incomplete)
	require.Contains(t, e.turnReview.TerminalReceipt.notice, "尚缺 中国棉花平衡表期末库存")
	require.Contains(t, trace.Notice, "尚缺 中国棉花平衡表期末库存")
	t.Logf("legacy source budget result: %s", e.turnReview.TerminalReceipt.notice)
}

func TestQA4Af3VerifiedComparisonPublication(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	trace := loadAf3KB28Replay(t, 1)
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprint(reversed), func(t *testing.T) {
			view := trace
			view.Steps = slices.Clone(trace.Steps)
			if reversed {
				slices.Reverse(view.Steps)
			}
			e, state, ctx, _ := newAf3KB28ReplayEngine(t, view, &mockChat{})
			e.commitTerminalResult(ctx, state, "run", "", "", "failed", false, terminalTableEvidence)
			e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			require.True(t, strings.HasPrefix(state.FinalAnswer, "两处来源给出的数字不同"))
			require.Contains(t, state.FinalAnswer, "棉花周报20240331.pptx 高于 棉花数据库.xlsx")
			require.Contains(t, state.FinalAnswer, "32.27")
			require.Contains(t, state.FinalAnswer, "331.2")
			require.Contains(t, state.FinalAnswer, "约 298.93")
			require.Contains(t, state.FinalAnswer, "第 6 页")
			require.Contains(t, state.FinalAnswer, "G87")
			require.NotContains(t, state.FinalAnswer, "298.93291576470585")
			require.NotContains(t, state.FinalAnswer, "<kb")
		})
	}
}

func TestQA4Af3VerifiedSourceTimeoutWithNoReading(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "false")
	for _, mode := range []string{"planned", "no plan candidate"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			trace := loadAf3KB28Replay(t, 3)
			model := &mockChat{responses: []mockResponse{chatAnswer(trace.Notice), chatAnswer(trace.Notice), chatAnswer(trace.Notice)}}
			e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model)
			if mode == "no plan candidate" {
				e.taskPlan = nil
				e.evidenceCompletion = newEvidenceCompletionTurn(0)
				decision := e.checkCandidate(ctx, trace.Question, trace.Notice, "", state, messages, false)
				require.Equal(t, candidateRevise, decision.Disposition)
				e.commitTerminalResult(ctx, state, "run", "", "", "failed", false, terminalBudget)
			} else {
				done, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "run", "turn", &messages, &loopGuards{}, "model_call_time")
				require.NoError(t, err)
				require.True(t, done)
				require.LessOrEqual(t, len(model.calls), 3, "existing synthesis attempts bound collection")
			}
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				complete = item.Data.(event.AgentCompleteData)
				return nil
			})
			e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			require.True(t, complete.HasPartialAnswer())
			require.Equal(t, state.FinalAnswer, complete.FinalAnswer)
			if mode == "planned" {
				require.NotContains(t, complete.FinalAnswer, trace.Question)
			} else {
				require.Contains(t, complete.FinalAnswer, trace.Question)
			}
			require.NotContains(t, complete.FinalAnswer, "已生成的草稿")
			require.NotContains(t, complete.FinalAnswer, "尚缺")
			require.NotContains(t, complete.FinalAnswer, "331.2")
			require.NotContains(t, complete.FinalAnswer, "298.93")
			require.NotContains(t, complete.FinalAnswer, "20240310")
		})
	}
}

func TestQA4Af3ReservedSourceAttemptActuallyReadsBothSources(t *testing.T) {
	var answers []string
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprint(reversed), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			trace := loadAf3KB28Replay(t, 3)
			native := loadAf3KB28Replay(t, 1)
			if reversed {
				slices.Reverse(trace.Steps[0].ToolCalls)
				for _, call := range trace.Steps[0].ToolCalls {
					for _, key := range []string{"documents", "datasets", "field_candidates", "literal_fields"} {
						if rows, ok := call.Result.Data[key].([]any); ok {
							slices.Reverse(rows)
						}
					}
				}
			}
			const endpoint = "https://af3-structured.invalid"
			t.Setenv("STRUCTURED_DATA_URL", endpoint)
			key := t.TempDir() + "/signing-key"
			require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0600))
			t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
			workbook := native.Steps[1].ToolCalls[0]
			model := &mockChat{}
			e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model, native)
			e.syncStructuredToolSourceContracts()
			arguments := map[string]any{}
			for k, v := range workbook.Args {
				arguments[k] = v
			}
			arguments["knowledge_base_id"] = e.modelContext.RegisterKnowledgeBase(af3KB28Library)
			workbookArguments, err := json.Marshal(arguments)
			require.NoError(t, err)
			// This boundary case refines the degraded original plan in an
			// existing attempted read use, without supplying any business values.
			original := native.Steps[0].ToolCalls[0]
			basis := map[string]any{"goal_index": 0, "request_basis": trace.Question, "source_mode": "open",
				"table": "中国棉花平衡表", "metric": "工商业期末库存", "version_task": "compare_sources",
				"source_time": "observed_period", "comparison_sources": []string{"3月31日周报", "棉花数据库"},
				"required": []string{"scoped_value"},
				"period":   map[string]any{"grain": "month", "from": "2023-10-01", "to": "2023-10-31"}}
			documentArguments, err := json.Marshal(map[string]any{"id": e.modelContext.RegisterDocument(native.Document.ID),
				"page": original.Args["page"], "task_coverage": map[string]any{"goal_index": 0, "goal": basis}})
			require.NoError(t, err)
			response := chatToolCall("recovery-page", "read_document", string(documentArguments))
			book := chatToolCall("recovery-cell", workbook.Name, string(workbookArguments))
			bookCalls := book.chunks[0].ToolCalls
			response.chunks[0].ToolCalls = append(response.chunks[0].ToolCalls, bookCalls...)
			if reversed {
				slices.Reverse(response.chunks[0].ToolCalls)
			}
			model.responses = []mockResponse{response, chatAnswer("两处来源的期末库存尚未核到。"), chatAnswer("现有目录无法判断两处的期末库存。")}
			transportCalls := 0
			defaultTransport := http.DefaultTransport
			http.DefaultTransport = qa4Stage2Transport(func(request *http.Request) (*http.Response, error) {
				transportCalls++
				require.Equal(t, endpoint+"/api/tools/read", request.URL.String())
				var wire struct {
					Operation string
					Arguments map[string]any
				}
				require.NoError(t, json.NewDecoder(request.Body).Decode(&wire))
				require.Equal(t, "query_series", wire.Operation)
				require.Equal(t, workbook.Args["series_id"], wire.Arguments["series_id"])
				require.Equal(t, workbook.Args["dataset_id"], wire.Arguments["dataset_id"])
				require.Equal(t, []any{}, wire.Arguments["source_binding"], "do not permit an independent field-model call")
				payload, err := json.Marshal(workbook.Result.Data)
				require.NoError(t, err)
				header := http.Header{}
				header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(payload))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = defaultTransport })
			done, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "run", "turn", &messages, &loopGuards{}, "model_call_time")
			require.NoError(t, err)
			require.True(t, done)
			require.Equal(t, 1, transportCalls)
			require.LessOrEqual(t, len(model.calls), 3)
			require.Len(t, state.RoundSteps, len(trace.Steps)+1)
			require.Len(t, state.RoundSteps[1].ToolCalls, 2)
			for _, call := range state.RoundSteps[1].ToolCalls {
				require.True(t, call.Result.Success, call.Result.Error)
				if call.Name == "read_document" {
					require.NotEmpty(t, call.Result.Data["table_reads"])
				}
			}
			e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
			require.Contains(t, state.FinalAnswer, "两处来源给出的数字不同")
			require.Contains(t, state.FinalAnswer, "331.2")
			require.Contains(t, state.FinalAnswer, "约 298.93")
			require.Empty(t, e.turnReview.TerminalReceipt.notice)
			require.NotContains(t, state.FinalAnswer, "尚缺")
			require.NotEmpty(t, state.KnowledgeRefs)
			require.NotEmpty(t, state.EvidenceRefs)
			answers = append(answers, state.FinalAnswer)
			t.Logf("checked answer: %s", state.FinalAnswer)
		})
	}
	require.Len(t, answers, 2)
	require.Equal(t, answers[0], answers[1])
}

func TestQA4Af3ComparisonVerifiesScopeAndPreservesOriginalPrecision(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	trace := loadAf3KB28Replay(t, 1)
	e, state, ctx, _ := newAf3KB28ReplayEngine(t, trace, &mockChat{})
	original, _ := e.terminalCoverageRecord(ctx, state)
	target := e.evidenceCompletionTargets()[0]
	wantFacts, aligned := e.tableReceiptReadings(target, original, "")
	require.True(t, aligned)
	require.Len(t, wantFacts, 2)
	book := wantFacts[1].Evidence
	for _, invalid := range []string{"other table", "other period", "other row", "invalid source binding"} {
		t.Run(invalid, func(t *testing.T) {
			record := original
			record.Evidence = slices.Clone(original.Evidence)
			for i := range record.Evidence {
				item := &record.Evidence[i]
				switch invalid {
				case "other table":
					if item.TableGrid != nil {
						item.Sheet = "Annual ledger"
					}
				case "other period":
					if item.Kind == "cell" && item.Cell == book.Cell {
						item.Date = "2023-09-30"
					}
				case "other row":
					if item.Kind == "cell" && item.Cell == book.Cell {
						item.DateCell = "A86"
					}
				}
			}
			if invalid == "invalid source binding" {
				record.Bindings = []coverageBinding{{Role: "primary", SourceRef: target.ComparisonSources[1],
					SourceID: book.SourceID, PhysicalSourceID: comparisonPhysicalSource(book)[1],
					KnowledgeBaseID: book.KnowledgeBaseID, Metric: "Opening balance"}}
			}
			_, aligned := e.tableReceiptReadings(target, record, "")
			require.False(t, aligned)
		})
	}
	t.Run("same original numbers from independent sources", func(t *testing.T) {
		facts := slices.Clone(wantFacts)
		facts[1].Evidence.Raw = json.Number("331.2")
		d := terminalCoverageDelivery{record: original, chinese: true}
		d.addComparison(facts, true)
		require.Equal(t, []string{"两处来源给出的数字相同。双方单位尚未核实为一致，无法据此确认量值相同。"}, d.conclusions)
	})
	t.Run("original display and cached precision are independent", func(t *testing.T) {
		record := original
		format := book
		format.Operation, format.OriginalNumberFormat = "inspect_source", "0.0"
		record.Evidence = append(slices.Clone(original.Evidence), format)
		d := terminalCoverageDelivery{record: record, chinese: true}
		d.addComparison(wantFacts, true)
		require.Contains(t, d.observedValue(book), "298.9（缓存值约 298.93）")
		record.Evidence[len(record.Evidence)-1].SnapshotHash = "different snapshot"
		mismatched := terminalCoverageDelivery{record: record, chinese: true}
		mismatched.addComparison(wantFacts, true)
		require.NotContains(t, mismatched.observedValue(book), "298.9（")
		require.Contains(t, mismatched.observedValue(book), "约 298.93")
	})
	t.Run("equal-looking display cannot erase a small difference", func(t *testing.T) {
		facts := slices.Clone(wantFacts)
		near := &facts[1].Evidence
		near.Raw, near.OriginalNumberFormat = json.Number("331.200000001"), "0.0"
		d := terminalCoverageDelivery{record: original, chinese: true}
		d.addComparison(facts, true)
		require.Contains(t, d.observedValue(*near), "331.2（底层数值 331.200000001）")
		require.Contains(t, d.conclusions[0], "棉花数据库.xlsx 高于 棉花周报20240331.pptx")
		require.NotContains(t, d.conclusions[0], "相差约 0.00")
	})
}

func TestQA4Af3ComparisonKeepsAnIndependentRequestedMetric(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	trace := loadAf3KB28Replay(t, 1)
	e, state, ctx, _ := newAf3KB28ReplayEngine(t, trace, &mockChat{})
	period := e.taskPlan.CoverageTargets[0].Period
	e.taskPlan.CoverageTargets = append(e.taskPlan.CoverageTargets,
		taskCoverageTarget{GoalIndex: 1, RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{
			SourceMode: "open", SourceTime: "observed_period", Metric: "Independent metric", Period: period, Required: []string{"scoped_value"},
		}},
	)
	delivery := e.terminalCoverageDelivery(ctx, state)
	require.NotEmpty(t, delivery.conclusions)
	require.Contains(t, strings.Join(delivery.missing, "\n"), "Independent metric")
}

type af3RecoveringContextOverflowModel struct {
	mockChat
	requests []*chat.ChatOptions
}

func (m *af3RecoveringContextOverflowModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	m.requests = append(m.requests, opts)
	if len(m.requests) == 1 {
		return nil, fmt.Errorf("maximum context length exceeded")
	}
	return m.mockChat.ChatStream(ctx, messages, opts)
}

func TestQA4Af3ReservedSourceAttemptFitsActualToolAndNoticeTokens(t *testing.T) {
	for _, mode := range []string{"tool schema does not fit", "provider overflow"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			trace := loadAf3KB28Replay(t, 3)
			model := &mockChat{responses: []mockResponse{chatAnswer("当前的来源目录没有提供所问读数。")}}
			e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model)
			e.config.ModelMaxOutputTokens = 512
			var overflow *af3RecoveringContextOverflowModel
			if mode == "tool schema does not fit" {
				e.config.MaxContextTokens = e.finalAnswerInputTokens(messages, standardAnswerBudgetNotice+e.evidenceCompletionFinalNotice()) +
					e.getCompletionTokenBudget() + contextSafetyTokens + 600
			} else {
				overflow = &af3RecoveringContextOverflowModel{mockChat: mockChat{responses: model.responses}}
				e.chatModel = overflow
			}
			done, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "run", "turn", &messages, &loopGuards{}, "model_call_time")
			require.NoError(t, err)
			require.True(t, done)
			require.Len(t, state.RoundSteps, len(trace.Steps))
			if overflow == nil {
				require.Len(t, model.opts, 1)
				require.Empty(t, model.opts[0].Tools)
			} else {
				require.Len(t, overflow.requests, 2, "overflow cannot add another recovery or compaction call")
				require.NotEmpty(t, overflow.requests[0].Tools)
				require.Empty(t, overflow.requests[1].Tools)
			}
		})
	}
}

// An archived final sentence without the model's goal/source association
// cannot establish task scope. This replay is a failed acceptance witness,
// separate from the positive real-read variants with an authorized goal.
func TestQA4Af3ArchivedComparisonWithoutGoal(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	trace := loadAf3KB28Replay(t, 1)
	model := &mockChat{responses: []mockResponse{discoveryReply("keep", "")}}
	e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model)
	e.taskPlan = nil
	e.evidenceCompletion = newEvidenceCompletionTurn(0)
	decision := e.checkCandidate(ctx, trace.Question, trace.Final, "", state, messages, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	e.commitTerminalResult(ctx, state, "run", decision.Candidate, "", "answer", false)
	e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
	require.Equal(t, trace.Final, state.FinalAnswer)
	require.Contains(t, state.FinalAnswer, "298.93291576470585")
	require.NotContains(t, state.FinalAnswer, "32.27")
	t.Logf("unresolved acceptance: archived candidate contains attributed original reads without a verified comparison goal")
}

func TestQA4Af3ArchivedComparisonWithGoalThroughOrdinaryCommit(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	trace := loadAf3KB28Replay(t, 1)
	model := &mockChat{responses: []mockResponse{discoveryReply("keep", "")}}
	e, state, ctx, messages := newAf3KB28ReplayEngine(t, trace, model)
	decision := e.checkCandidate(ctx, trace.Question, trace.Final, "", state, messages, false)
	if decision.Disposition == candidateAccepted || decision.Disposition == candidateEvidencePartial {
		e.commitTerminalResult(ctx, state, "run", decision.Candidate, "", "answer", false)
	} else {
		// The recorded Af2 turn had already spent its candidate repairs.
		// Its original rejected candidate and live source records reach
		// the normal budget outlet rather than forcing table fallback.
		e.commitTerminalResult(ctx, state, "run", "", "", "failed", false, terminalBudget)
	}
	e.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
	require.Contains(t, state.FinalAnswer, "两处来源给出的数字不同")
	require.Contains(t, state.FinalAnswer, "32.27")
	require.Contains(t, state.FinalAnswer, "331.2")
	require.Contains(t, state.FinalAnswer, "约 298.93")
	require.NotContains(t, state.FinalAnswer, "298.93291576470585")
	require.Empty(t, e.turnReview.TerminalReceipt.notice)
}
