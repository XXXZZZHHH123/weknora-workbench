package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTerminalEvidenceCompletionFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		alter      func(*AgentEngine, *types.AgentState)
		submission string
		want       []string
		omit       []string
	}{
		{name: "native facts without declarations", want: []string{
			"Elapsed time 在已核查范围 2021-07-02", "Elapsed time 在已核查范围 2021-01-01",
			"Equipment.xlsx", "Operating hours", "2021-06-03", "47.25", "A15", "B15", "单位未确认", "上述缺数范围仍缺可核实的观测值",
		}, omit: []string{"其余要求"}},
		{name: "normal delivery", submission: "Readings in scope were reviewed.", want: []string{"Readings in scope were reviewed."}, omit: []string{"B15", "47.25"}},
		{name: "scope narrowed", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "different-library"}}
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "new human task", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Use a different file for this question."})
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "source snapshot expires", alter: func(_ *AgentEngine, state *types.AgentState) {
			catalogue := terminalLatestSeriesCall()
			catalogue.ID = "newer-read"
			catalogue.Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
			catalogue.Result.Data["rows"].([]any)[0].(map[string]any)["value"] = 48.5
			state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, catalogue)
		}, want: []string{"48.5"}, omit: []string{"47.25"}},
		{name: "only excludes unrelated workbook", alter: func(e *AgentEngine, state *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only"}}}}
			e.evidenceCompletion = newEvidenceCompletionTurn(0)
			e.refreshEvidenceCompletion(t.Context(), e.turnReview.Query, state, e.reviewConversation)
			e.evidenceCompletion.record.Bindings = []coverageBinding{{Role: "primary", SourceID: "other-register", KnowledgeBaseID: "allowed-library"}}
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "current directory only", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[:1]
		}, want: []string{"Equipment.xlsx", "Operating hours", "目录范围", "仍待核实"}, omit: []string{"B15", "47.25", "没有有效观测值"}},
		{name: "verified gap and another actual cell", alter: func(_ *AgentEngine, state *types.AgentState) {
			call := state.RoundSteps[0].ToolCalls[1]
			call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", "2021-06-01", "2021-06-30"
			filters := call.Result.Data["filters"].(map[string]any)
			filters["operation"], filters["start_date"], filters["end_date"] = "rows", "2021-06-01", "2021-06-30"
			call.Result.Data["rows_are"] = "observations"
			rows := call.Result.Data["rows"].([]any)
			for i := 16; i <= 20; i++ {
				cell := strconv.Itoa(i)
				rows = append(rows, map[string]any{"date": "2021-06-03", "date_cell": "A" + cell, "source_cell": "B" + cell, "value": i, "value_status": "valid"})
			}
			call.Result.Data["rows"] = rows
		}, want: []string{"没有有效观测值", "2021-07-02", "47.25", "B15", "B19", "所列原格来自本轮实读"}, omit: []string{"最近有效值", "B20"}},
		{name: "only rejected source calls", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[1:2]
			state.RoundSteps[0].ToolCalls[0].Result = &types.ToolResult{PreflightRejected: true, Error: "ineligible optional selection", Data: map[string]any{"execution_started": false}}
		}, want: []string{"读取开始前被拦下", "仍待核实"}, omit: []string{"ineligible optional selection", "47.25"}},
		{name: "legacy switch", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.evidenceCompletion.enabled = false
		}, want: []string{answerReviewUnavailableFallback}, omit: []string{"47.25"}},
		{name: "exact format", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "exact"}}}
		}, want: []string{answerReviewUnavailableFallback}, omit: []string{"47.25"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = agenttools.NewToolRegistry()
			base := agenttools.NewStructuredDataTool()
			e.toolRegistry.RegisterTool(base)
			for _, tool := range agenttools.NewStructuredReadOperationTools(base, structuredCodecManifest()) {
				e.toolRegistry.RegisterTool(tool)
			}
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
			e.turnReview.Query = "What was recorded for the requested day?"
			e.reviewConversationReady = true
			e.reviewConversation = []chat.Message{{Role: "user", Content: e.turnReview.Query}}
			directory := types.ToolCall{ID: "catalogue", Name: "structured_list_datasets", Args: map[string]any{},
				Result: &types.ToolResult{Success: true, Data: map[string]any{
					"_structured_execution": map[string]any{"canonical_operation": "list_datasets", "effect_class": "read", "scope_mode": "directory"},
					"source_context":        map[string]any{"knowledge_base_id": "allowed-library"},
					"datasets":              []any{map[string]any{"dataset_id": "register", "source_file": "Equipment.xlsx", "sheets": []any{"Operating hours"}}},
					"datasets_truncated":    false,
				}}}
			latest, gap := terminalLatestSeriesCall(), terminalLatestSeriesCall()
			gap.ID = "exact-gap"
			gap.Args["start_date"], gap.Args["end_date"], gap.Args["operation"] = "2021-07-02", "2021-07-02", "rows"
			filters := gap.Result.Data["filters"].(map[string]any)
			filters["start_date"], filters["end_date"], filters["operation"] = "2021-07-02", "2021-07-02", "rows"
			gap.Result.Data["status"], gap.Result.Data["rows"], gap.Result.Data["valid_count"] = "no_data", []any{}, 0
			wider, unrelated := terminalLatestSeriesCall(), terminalLatestSeriesCall()
			for _, call := range []types.ToolCall{wider, unrelated} {
				call.Result.Data["status"], call.Result.Data["rows"], call.Result.Data["valid_count"] = "no_data", []any{}, 0
				call.Args["start_date"], call.Args["end_date"], call.Args["operation"] = "2021-01-01", "2021-12-31", "rows"
				filters := call.Result.Data["filters"].(map[string]any)
				filters["start_date"], filters["end_date"], filters["operation"] = call.Args["start_date"], call.Args["end_date"], "rows"
			}
			wider.ID, unrelated.ID = "wide-gap", "other-gap"
			unrelated.Args["series_id"] = "unrelated"
			unrelated.Result.Data["filters"].(map[string]any)["series_id"] = "unrelated"
			unrelated.Result.Data["series"].(map[string]any)["series_id"] = "unrelated"
			unrelated.Result.Data["series"].(map[string]any)["metric"] = "Unrelated field"
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{directory, latest, gap, wider, unrelated}}}}
			e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, e.reviewConversation)
			require.NotEmpty(t, e.evidenceCompletion.record.Evidence)
			require.Empty(t, e.coverageHandoffFacts())
			if test.alter != nil {
				test.alter(e, state)
			}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			submission, outcome := test.submission, "answer"
			if submission == "" {
				submission, outcome = answerReviewUnavailableFallback, "failed"
			}
			e.commitTerminalResult(ctx, state, "terminal", submission, "", outcome, false)
			e.commitTerminalResult(ctx, state, "terminal", "Duplicate candidate", "", "answer", false)
			require.True(t, state.IsComplete)
			done, streamed := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, streamed)
			for _, value := range test.want {
				require.Contains(t, state.FinalAnswer, value)
			}
			for _, value := range test.omit {
				require.NotContains(t, state.FinalAnswer, value)
			}
		})
	}
}

func terminalLatestSeriesCall() types.ToolCall {
	return types.ToolCall{ID: "latest-read", Name: "structured_query_series",
		Args: map[string]any{"dataset_id": "register", "series_id": "elapsed-time", "operation": "latest"},
		Result: &types.ToolResult{Success: true, Data: map[string]any{
			"_structured_execution": map[string]any{"canonical_operation": "query_series", "effect_class": "read", "scope_mode": "knowledge_base"},
			"status":                "ok", "source_context": map[string]any{"knowledge_base_id": "allowed-library"},
			"source":     map[string]any{"file": "Equipment.xlsx", "sheet": "Operating hours", "sha256": strings.Repeat("a", 64)},
			"dataset_id": "register", "truncated": false, "rows_are": "latest_valid_observation", "valid_last_date": "2021-06-03",
			"series":  map[string]any{"series_id": "elapsed-time", "metric": "Elapsed time", "column": "B", "unit_status": "needs_review", "last_valid_date": "2021-06-03"},
			"filters": map[string]any{"series_id": "elapsed-time", "operation": "latest", "value_filters": []any{}},
			"rows":    []any{map[string]any{"date": "2021-06-03", "date_cell": "A15", "source_cell": "B15", "value": 47.25, "value_status": "valid"}},
		}}}
}

func TestTerminalEvidenceCompletionDocumentReadStatus(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	document := &types.Knowledge{ID: "actual-document", TenantID: 7, KnowledgeBaseID: "granted-base", Title: "Quarterly inspection",
		FileName: "quarterly.pptx", FileType: "pptx", ParseStatus: types.ParseStatusCompleted}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: document.TenantID}}
	e := newTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode, e.config.WorkbenchDefault, e.config.SearchTargets = "standard", true, targets
	e.toolRegistry = agenttools.NewToolRegistry()
	read := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &plannedDocumentChunks{repo: &plannedDocumentRepo{}}, targets)
	e.toolRegistry.RegisterTool(read)
	const query = "Which labels were reported on the selected page?"
	agenttools.PrepareDocumentScope(e.toolRegistry, query)
	ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
	ctx = types.WithCaller(ctx, types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	args := `{"id":"actual-document","page":4,"source_scope":{"selection_reason":"Read the selected inspection page.","sources":[{"knowledge_id":"actual-document","expected_file_name":"quarterly.pptx","page":4}]}}`
	result, err := read.Execute(ctx, []byte(args))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.True(t, agenttools.IsCurrentDocumentRead(e.toolRegistry, result))
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "scoped-page", Name: agenttools.ToolReadDocument, Args: map[string]any{"id": document.ID, "page": 4}, Result: result,
	}}}}}
	e.turnReview.Query = query
	e.reviewConversation, e.reviewConversationReady = []chat.Message{{Role: "user", Content: query}}, true
	e.commitTerminalResult(ctx, state, "terminal", answerReviewUnavailableFallback, "", "failed", false)
	require.Contains(t, state.FinalAnswer, "本轮已读取 Quarterly inspection 第 4 页返回的正文")
	require.Contains(t, state.FinalAnswer, "以上是本轮核实到的来源和范围")
	require.NotContains(t, state.FinalAnswer, "本轮未取得")
	require.NotContains(t, state.FinalAnswer, "类型甲")
}
