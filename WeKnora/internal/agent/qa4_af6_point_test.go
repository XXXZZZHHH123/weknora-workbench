package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
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

// These are the first and third Af5 KB-31 stops transcribed from the public tool
// events and their validated plans from the backend's review input. The
// separate Af4 fixture supplies authorized directory and document stores.
func loadAf6PointStops(t *testing.T) []af5SupplementTrace {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/qa4_af6_point.json.gz")
	require.NoError(t, err)
	defer file.Close()
	body, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	var traces []af5SupplementTrace
	require.NoError(t, decoder.Decode(&traces))
	require.Len(t, traces, 2)
	return traces
}

func af6PointSetup(t *testing.T, data af5SupplementFixture, trace af5SupplementTrace, reversed bool) (*AgentEngine, *types.AgentState, *af5SupplementIndex, *mockChat, context.Context) {
	t.Helper()
	model := &mockChat{}
	engine, index := af5SupplementEngine(t, data, trace, model, reversed)
	ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "fixture", Role: types.TenantRoleViewer}))
	ctx = context.WithValue(ctx, types.LanguageContextKey, "zh-CN")
	state := &types.AgentState{RoundSteps: trace.Steps}
	for i := range state.RoundSteps {
		step := &state.RoundSteps[i]
		state.CurrentRound = max(state.CurrentRound, step.Iteration)
		for j := range step.ToolCalls {
			call := &step.ToolCalls[j]
			args, err := json.Marshal(call.Args)
			require.NoError(t, err)
			if descriptor, _, native := engine.toolRegistry.StructuredInvocation(call.Name, args); native {
				// Register the same native operation in this isolated engine.
				call.Result.Data["_structured_execution"] = descriptor
			}
			if call.Name == agenttools.ToolBindDocumentScope || call.Name == agenttools.ToolReadDocument && call.Result.Success {
				tool, err := engine.toolRegistry.GetTool(call.Name)
				require.NoError(t, err)
				result, err := tool.Execute(ctx, args)
				require.NoError(t, err)
				require.True(t, result.Success, result.Error)
				call.Result = result
			}
		}
	}
	if reversed {
		for i := range state.RoundSteps {
			slices.Reverse(state.RoundSteps[i].ToolCalls)
		}
	}
	messages := []chat.Message{{Role: "user", Content: trace.Question}}
	for _, step := range state.RoundSteps {
		messages = engine.appendToolResults(messages, step)
	}
	engine.turnReview.Query, engine.turnReview.Conversation = trace.Question, messages
	engine.reviewConversation = conversationThroughLastHuman(messages)
	engine.reviewConversationReady = true
	engine.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
	return engine, state, index, model, ctx
}

func af6PointAccept(t *testing.T, engine *AgentEngine, state *types.AgentState, ctx context.Context, candidate string) {
	t.Helper()
	engine.turnReview.Last = candidateDecision{
		Candidate: candidate, CandidateHash: candidateTextDigest(candidate),
		Disposition: candidateAccepted, StandardReview: engine.standardAnswerReview(),
		StructuredResultReview: engine.structuredResultReviewEnabled(),
		TaskHash:               engine.candidateTaskDigest(engine.turnReview.Query, engine.turnReview.Conversation),
		EvidenceHash:           reviewDigest(engine.sourceConsistencyMessages(state, engine.turnReview.Conversation)),
		PublicationEvidence:    engine.terminalEvidenceRevision(ctx, state),
	}
	require.True(t, engine.candidateApprovalCurrent(candidate, state))
}

func af6PointCommit(t *testing.T, engine *AgentEngine, state *types.AgentState, ctx context.Context, candidate string) ([]types.ToolCall, string) {
	t.Helper()
	stream := &terminalAnswerRecorder{}
	stream.attach(engine.eventBus)
	var complete event.AgentCompleteData
	engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		complete = evt.Data.(event.AgentCompleteData)
		return nil
	})
	start := len(state.RoundSteps)
	engine.commitTerminalResult(ctx, state, "run", candidate, "", "answer", false)
	engine.emitCompletionEvent(ctx, state, "run", "turn", time.Now())
	closed, answer := stream.snapshot()
	require.Equal(t, 1, closed)
	require.Equal(t, complete.FinalAnswer, answer)
	require.Equal(t, state.FinalAnswer, answer)
	require.NotEmpty(t, answer)
	require.NotRegexp(t, `</?(?:kb|evidence|task_coverage)`, answer)
	require.NotContains(t, answer, "这些结果未给出可对应所问范围的读数")
	var added []types.ToolCall
	for _, step := range state.RoundSteps[start:] {
		added = append(added, step.ToolCalls...)
	}
	return added, answer
}

func TestQA4Af6PointGapRecordedStops(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", enabled)
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", enabled)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "false")
			data := loadAf5SupplementFixture(t)
			for i, trace := range loadAf6PointStops(t) {
				t.Run(trace.ID, func(t *testing.T) {
					require.Equal(t, []string{"structured"}, trace.Plan.SourceDiscovery.SourceDomains)
					require.Equal(t, "named_first", trace.Plan.CoverageTargets[0].SourceMode)
					require.NotContains(t, trace.Answer, "60.6")
					engine, state, index, model, ctx := af6PointSetup(t, data, trace, i == 1)
					require.True(t, engine.standardAnswerBudgetEligible(ctx, state))
					require.False(t, engine.evidenceCompletionEnabled(), "the accepted draft is in shadow mode")
					added, answer := af6PointNaturalStop(t, engine, state, model, ctx, trace)
					require.Contains(t, strings.ReplaceAll(answer, ",", ""), "5160")
					require.Contains(t, answer, "B3340")
					if enabled == "false" {
						require.Empty(t, added)
						require.Zero(t, index.calls)
						require.NotContains(t, answer, "60.6")
						require.Equal(t, trace.Answer, answer)
						return
					}
					require.Equal(t, 1, index.calls)
					require.Len(t, added, finalCoverageReadLimit)
					require.Equal(t, agenttools.ToolSearchKnowledge, added[0].Name)
					require.Equal(t, agenttools.ToolBindDocumentScope, added[1].Name)
					for _, read := range added[2:] {
						require.Equal(t, agenttools.ToolReadDocument, read.Name)
						require.True(t, read.Result.Success, read.Result.Error)
					}
					require.Contains(t, answer, "2024-03-10")
					require.Contains(t, answer, "棉花周报20240310.pptx")
					require.Contains(t, answer, "60.6万吨")
					require.Contains(t, answer, "棉花周报20240310-冲突测试版.pptx")
					require.Contains(t, answer, "62.3万吨")
				})
			}
		})
	}
}

func af6PointNaturalStop(t *testing.T, engine *AgentEngine, state *types.AgentState, model *mockChat, ctx context.Context, trace af5SupplementTrace) ([]types.ToolCall, string) {
	t.Helper()
	_, binding := af3CollaborationReplayStore(t)
	engine.SetCollaboration(binding)
	model.responses = []mockResponse{chatAnswer(trace.Answer)}
	messages := slices.Clone(engine.turnReview.Conversation)
	start := len(state.RoundSteps)
	stream := &terminalAnswerRecorder{}
	stream.attach(engine.eventBus)
	var complete event.AgentCompleteData
	engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		complete = evt.Data.(event.AgentCompleteData)
		return nil
	})
	outcome, err := engine.runReActIteration(ctx, state, &messages, engine.buildToolsForLLM(),
		binding.Scope.SessionID, binding.Scope.ID, trace.Question, &loopGuards{})
	require.NoError(t, err)
	require.Equal(t, iterOutcomeBreak, outcome)
	require.Equal(t, candidateAccepted, engine.turnReview.Last.Disposition)
	require.Equal(t, 1, model.callCount)
	group, err := binding.Runtime.Store.Read(ctx, binding.Scope)
	require.NoError(t, err)
	require.Equal(t, "completed", group.Status)
	engine.emitCompletionEvent(ctx, state, binding.Scope.SessionID, binding.Scope.ID, time.Now())
	closed, answer := stream.snapshot()
	require.Equal(t, 1, closed)
	require.Equal(t, state.FinalAnswer, answer)
	require.Equal(t, complete.FinalAnswer, answer)
	require.NotEmpty(t, answer)
	require.NotRegexp(t, `</?(?:kb|evidence|task_coverage)`, answer)
	require.NotContains(t, answer, "这些结果未给出可对应所问范围的读数")
	var added []types.ToolCall
	for _, step := range state.RoundSteps[start:] {
		added = append(added, step.ToolCalls...)
	}
	return added, answer
}

func TestQA4Af6PointGapSourceScopeAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		calls      []string
		reading    string
		readFailed bool
	}{
		{name: "degraded declaration", mode: "degraded", calls: []string{"search_knowledge", "bind_document_scope", "read_document", "read_document"}, reading: "60.6万吨"},
		{name: "verified restriction", mode: "only"},
		{name: "pinned document", mode: "pinned"},
		{name: "empty recall", mode: "empty", calls: []string{"search_knowledge"}},
		{name: "prior model binding", mode: "prebound", calls: []string{"search_knowledge", "bind_document_scope", "read_document", "read_document"}, reading: "60.6万吨"},
		{name: "unrelated same-day binding", mode: "same-day", calls: []string{"search_knowledge", "bind_document_scope", "read_document", "read_document"}, reading: "60.6万吨"},
		{name: "unpaged note", mode: "note", calls: []string{"search_knowledge", "bind_document_scope", "read_document"}, reading: "19 pallets"},
		{name: "unrelated passage", mode: "no-reading", calls: []string{"search_knowledge", "bind_document_scope", "read_document"}},
		{name: "missing page read", mode: "missing-page", calls: []string{"search_knowledge", "bind_document_scope", "read_document"}, readFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "true")
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "false")
			trace := loadAf6PointStops(t)[0]
			data := loadAf5SupplementFixture(t)
			var oldBinding *types.Knowledge
			if test.mode == "empty" {
				data.Hits = nil
			}
			if test.mode == "prebound" {
				for _, doc := range data.Documents {
					if doc.FileName == "棉花数据库.xlsx" {
						oldBinding = doc
						break
					}
				}
				require.NotNil(t, oldBinding)
			}
			if test.mode == "same-day" {
				oldBinding = &types.Knowledge{ID: "fixture-unrelated", TenantID: 1, KnowledgeBaseID: trace.Libraries[0],
					Title: "Window inspection 20240310", FileHash: "124b7ce705", Type: "file", FileType: "txt"}
				data.Documents = append(data.Documents, oldBinding)
			}
			if slices.Contains([]string{"note", "no-reading", "missing-page"}, test.mode) {
				note := &types.Knowledge{ID: "fixture-log", TenantID: 1, KnowledgeBaseID: trace.Libraries[0],
					Title: "Shift log", FileName: "Shift log", FileHash: "123f7ce705", Type: "file", FileType: "txt"}
				content := "郑棉仓单：2024-03-10 19 pallets"
				if test.mode == "no-reading" {
					content = "郑棉仓单：2024-03-09 19 pallets"
				}
				chunk := &types.Chunk{ID: "fixture-log-chunk", TenantID: 1, KnowledgeID: note.ID,
					KnowledgeBaseID: note.KnowledgeBaseID, Content: content,
					ChunkType: types.ChunkTypeText, IsEnabled: true, EndAt: len([]rune(content))}
				hit := &types.SearchResult{ID: chunk.ID, KnowledgeID: note.ID, KnowledgeBaseID: note.KnowledgeBaseID,
					Content: content, Score: 1, ChunkType: string(types.ChunkTypeText)}
				if test.mode == "missing-page" {
					// A stale physical locator can still yield a candidate.
					// The actual reader sees the same source without that page.
					hit.SourceLocators = []types.SourceLocator{{Type: types.SourceLocatorPDF, Mapping: "exact", Page: 13, SourceHash: note.FileHash}}
				}
				data.Documents, data.Chunks, data.Hits = []*types.Knowledge{note}, []*types.Chunk{chunk}, []*types.SearchResult{hit}
			}
			engine, state, index, model, ctx := af6PointSetup(t, data, trace, false)
			switch test.mode {
			case "degraded", "only":
				// The earlier native metadata selection is already verified.
				// Keep it and change only the effective model declaration.
				record, _ := engine.terminalCoverageRecord(ctx, state)
				declared := engine.taskPlan.CoverageTargets[0]
				goal := *declared.StructuredTaskGoal
				declared.StructuredTaskGoal = &goal
				declared.SourceMode = "only"
				declared.PlanIssues = []string{"unverified_restriction_basis"}
				if test.mode == "only" {
					basis := "只查棉花数据库。"
					declared.RestrictionBasis = basis
					declared.PlanIssues = nil
					engine.turnReview.Query += basis
					engine.turnReview.Conversation[0].Content += basis
				}
				engine.evidenceCompletion.declared = []taskCoverageTarget{declared}
				for _, gap := range record.Evidence {
					if gap.Kind == "gap" {
						engine.evidenceCompletion.nativeSelections = map[int][]coverageEvidence{0: {gap}}
						break
					}
				}
				// Even a model-supplied matching tool ID cannot borrow the
				// private invocation context from the directed executor.
				input := agenttools.SearchKnowledgeInput{Query: data.Query, KnowledgeBaseIDs: trace.Libraries, Mode: agenttools.SearchModeKeyword}
				call := finalSupplementCall(&declared.GoalIndex, "discover", agenttools.ToolSearchKnowledge, input)
				var args map[string]any
				require.NoError(t, json.Unmarshal([]byte(call.Function.Arguments), &args))
				require.False(t, engine.coverageSourceCallAllowed(ctx, call, args))
			case "pinned":
				engine.config.KnowledgeIDs = []string{"96c5719b-5dd5-444b-bfbb-59b30fbf3f53"}
			case "prebound", "same-day":
				plan := agenttools.DocumentScopePlan{Reason: "Previously selected source", Sources: []agenttools.DocumentSourceSelection{
					{KnowledgeID: oldBinding.ID, FileName: oldBinding.FileName, Title: oldBinding.Title},
				}}
				if test.mode == "same-day" {
					plan.Sources[0].Page = 13
				}
				args, err := json.Marshal(plan)
				require.NoError(t, err)
				binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
				require.NoError(t, err)
				result, err := binder.Execute(ctx, args)
				require.NoError(t, err)
				require.True(t, result.Success, result.Error)
				var normalized map[string]any
				require.NoError(t, json.Unmarshal(args, &normalized))
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: state.CurrentRound,
					ToolCalls: []types.ToolCall{{ID: "prior-bind", Name: agenttools.ToolBindDocumentScope, Args: normalized, Result: result}}})
				if test.mode == "same-day" {
					state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: state.CurrentRound,
						ToolCalls: []types.ToolCall{{ID: "prior-recall", Name: agenttools.ToolSearchKnowledge,
							Args: map[string]any{"query": "Window inspection"}, Result: result}}})
				}
			}
			var added []types.ToolCall
			var answer string
			if slices.Contains([]string{"empty", "missing-page", "no-reading", "prebound", "same-day"}, test.mode) {
				added, answer = af6PointNaturalStop(t, engine, state, model, ctx, trace)
			} else {
				af6PointAccept(t, engine, state, ctx, trace.Answer)
				added, answer = af6PointCommit(t, engine, state, ctx, trace.Answer)
				require.Zero(t, model.callCount)
			}
			require.Len(t, added, len(test.calls))
			if len(test.calls) != 0 {
				require.Equal(t, 1, index.calls)
			} else {
				require.Zero(t, index.calls)
			}
			for i, call := range added {
				require.Equal(t, test.calls[i], call.Name)
				require.Equal(t, !test.readFailed || call.Name != agenttools.ToolReadDocument, call.Result.Success, call.Result.Error)
			}
			require.Contains(t, strings.ReplaceAll(answer, ",", ""), "5160")
			require.Contains(t, answer, "B3340")
			if test.reading == "" {
				require.NotContains(t, answer, "60.6")
				require.NotContains(t, answer, "19 pallets")
			} else {
				require.Contains(t, answer, test.reading)
			}
			if slices.Contains([]string{"empty", "missing-page", "no-reading"}, test.mode) {
				require.Equal(t, trace.Answer, answer)
			}
			if oldBinding != nil {
				require.Contains(t, agenttools.CurrentDocumentScopeIDs(engine.toolRegistry), oldBinding.ID)
			}
		})
	}
}

func TestQA4Af6PointGapPreservesAlreadyReadAf4Result(t *testing.T) {
	for _, test := range []struct{ name, scoped, recall, substantive string }{
		{name: "Af6", scoped: "true", recall: "true", substantive: "false"},
		{name: "Af7", scoped: "true", recall: "false", substantive: "true"},
		{name: "Af5", scoped: "false", recall: "true", substantive: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", test.scoped)
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", test.recall)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", test.substantive)
			data := loadAf5SupplementFixture(t)
			trace := data.Traces[1] // Af4-2 contains the real binding and both document reads.
			engine, state, index, model, ctx := af6PointSetup(t, data, trace, true)
			af6PointAccept(t, engine, state, ctx, trace.Answer)
			added, answer := af6PointCommit(t, engine, state, ctx, trace.Answer)
			require.Empty(t, added)
			require.Zero(t, index.calls)
			require.Zero(t, model.callCount)
			require.Contains(t, strings.ReplaceAll(answer, ",", ""), "5160")
			require.Contains(t, answer, "60.6")
			require.Contains(t, answer, "62.3")
		})
	}
}
