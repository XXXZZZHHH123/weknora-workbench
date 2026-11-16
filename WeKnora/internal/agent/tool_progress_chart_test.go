package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func progressChartCall(operation string, data map[string]any) types.ToolCall {
	return types.ToolCall{Name: "render_chart", Args: map[string]any{"operation": operation, "source_id": "real-source"},
		Result: &types.ToolResult{Success: true, Data: data}}
}

func TestToolProgressDeduplicatesChartInspectAndMapping(t *testing.T) {
	for _, call := range []types.ToolCall{
		progressChartCall("inspect", map[string]any{"kind": "chart_source_catalog", "sources": []any{map[string]any{"source_id": "real-source", "row_count": 12}}}),
		progressChartCall("map", map[string]any{"kind": "chart_source_mapping", "source_id": "mapped-source", "parent_source_id": "real-source", "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}}}),
	} {
		var guard toolProgressGuard
		require.Equal(t, 1, guard.observe([]types.ToolCall{call}).NewEvidence)
		for round := 1; round <= toolProgressStopRounds; round++ {
			call.ID = fmt.Sprintf("changed-call-%d", round)
			call.Result.Data["notice"] = fmt.Sprintf("navigation %d", round)
			if call.ExecutionArgs()["operation"] == "map" {
				call.Result.Data["source_id"] = fmt.Sprintf("transport-map-%d", round)
			}
			observation := guard.observe([]types.ToolCall{call})
			require.Zero(t, observation.NewEvidence)
			require.Equal(t, round, observation.StaleRounds)
			require.Equal(t, round == toolProgressReplanRounds, observation.Replan)
			require.Equal(t, round == toolProgressStopRounds, observation.Stop)
		}
	}
}

func TestToolProgressDeduplicatesStructuredResultsAcrossSQLAndExecutionIDs(t *testing.T) {
	call := types.ToolCall{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_analysis", "status": "ok", "sql": "SELECT value FROM data", "result_id": "execution-a",
		"columns": []any{"value"}, "rows": []any{[]any{5}}, "source": map[string]any{"sha256": "actual-file-hash"},
		"result_contract": map[string]any{"result_id": "a", "result_hash": "hash-a", "execution": map[string]any{"sql_sha256": "a"}},
	}}}
	var guard toolProgressGuard
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).NewEvidence)
	call.Result.Data["sql"], call.Result.Data["result_id"] = "SELECT value FROM data WHERE TRUE", "execution-b"
	call.Result.Data["result_contract"] = map[string]any{"result_id": "b", "result_hash": "hash-b", "execution": map[string]any{"sql_sha256": "b"}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).StaleRounds)
	call.Result.Data["rows"] = []any{[]any{6}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).NewEvidence)
}

func TestToolProgressRepeatedStructuredCapabilityRefreshIsStale(t *testing.T) {
	call := types.ToolCall{Name: agenttools.ToolStructuredCapabilities, Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_capabilities", "generation": 1, "observed_manifest_sha256": "manifest-a", "ttl_seconds": 30,
		"tools": []any{map[string]any{"tool_name": "structured_query_series", "schema_sha256": "schema-a", "generation": 1, "manifest_sha256": "manifest-a"}},
	}}}
	var guard toolProgressGuard
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).NewEvidence)
	call.Result.Data["generation"], call.Result.Data["ttl_seconds"], call.Result.Data["observed_manifest_sha256"] = 2, 29, "manifest-b"
	call.Result.Data["tools"] = []any{map[string]any{"tool_name": "structured_query_series", "schema_sha256": "schema-a", "generation": 2, "manifest_sha256": "manifest-b"}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).StaleRounds)
	call.Result.Data["tools"] = []any{map[string]any{"tool_name": "structured_query_series", "schema_sha256": "schema-b", "generation": 3}}
	require.Equal(t, 1, guard.observe([]types.ToolCall{call}).NewEvidence, "a changed actual schema can support replanning")
}

func TestToolProgressChartCatalogIgnoresReexecutedSourceHandlesAndOrder(t *testing.T) {
	entry := func(id, sql string) map[string]any {
		provenance, err := json.Marshal(map[string]any{"source": map[string]any{"sha256": "real-source-version", "file": "fixture.xlsx"}, "sql": sql,
			"result_id": id, "result_contract": map[string]any{"result_hash": id, "execution": map[string]any{"sql_sha256": id}}})
		require.NoError(t, err)
		return map[string]any{"source_id": id, "columns": []any{map[string]any{"name": "observed", "unit": "declared-unit"}},
			"row_count": 1, "sample_rows": []any{[]any{5}}, "provenance": string(provenance)}
	}
	var guard toolProgressGuard
	first := progressChartCall("inspect", map[string]any{"kind": "chart_source_catalog", "sources": []any{entry("call-a-source", "SELECT observed FROM data")}})
	require.Equal(t, 1, guard.observe([]types.ToolCall{first}).NewEvidence)
	second := progressChartCall("inspect", map[string]any{"kind": "chart_source_catalog", "sources": []any{
		entry("call-b-source", "SELECT observed FROM data WHERE TRUE"), entry("call-a-source", "SELECT observed FROM data"),
	}})
	require.Equal(t, 1, guard.observe([]types.ToolCall{second}).StaleRounds, "an expanded catalog of equivalent executions is still the same directory evidence")
	second.Result.Data["sources"] = []any{entry("call-c-source", "SELECT observed FROM data /* same result */")}
	require.Equal(t, 2, guard.observe([]types.ToolCall{second}).StaleRounds)
}

func TestToolProgressNewSavedChartResetsStallButNotFailureAllowance(t *testing.T) {
	failed := progressChartCall("render", nil)
	failed.Result = &types.ToolResult{Error: "source column is out of range"}
	var guard toolProgressGuard
	for round := 1; round <= toolProgressReplanRounds; round++ {
		observation := guard.observe([]types.ToolCall{failed})
		require.Equal(t, round, observation.StaleRounds)
		require.Equal(t, round == toolProgressReplanRounds, observation.Replan)
	}
	saved := progressChartCall("render", map[string]any{"kind": "data_chart", "status": "ok", "artifact": map[string]any{"handle": "resource://abcdefghijklmnopqrstuv"}})
	observation := guard.observe([]types.ToolCall{saved})
	require.Equal(t, 1, observation.NewEvidence)
	require.Zero(t, guard.stale)
	require.Equal(t, 1, guard.observe([]types.ToolCall{saved}).StaleRounds, "same saved attachment is not another delivery")
	saved.Result.Data["artifact"] = map[string]any{"handle": "resource://bcdefghijklmnopqrstuvw"}
	require.Equal(t, 1, guard.observe([]types.ToolCall{saved}).NewEvidence)
	observation = guard.observe([]types.ToolCall{failed})
	require.True(t, observation.RenderLimit, "successful charts cannot replenish the turn's cumulative failure allowance")
}

func TestToolProgressChartAndStructuredFailuresReachStop(t *testing.T) {
	var guard toolProgressGuard
	for round := 1; round <= toolProgressStopRounds; round++ {
		name := "render_chart"
		if round%2 == 0 {
			name = "structured_query_pdf_table"
		}
		observation := guard.observe([]types.ToolCall{{Name: name, Args: map[string]any{"operation": "map"}, Result: &types.ToolResult{Error: "invalid actual coordinates"}}})
		require.Equal(t, round, observation.StaleRounds)
		require.Equal(t, round == toolProgressStopRounds, observation.Stop)
	}
}

type failedProgressChartTool struct {
	agenttools.BaseTool
	calls int
}

func (tool *failedProgressChartTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return &types.ToolResult{Error: "The actual selected columns have different declared units."}, nil
}

func TestChartRenderLimitEntersCheckedTextSynthesisForEveryEntry(t *testing.T) {
	for _, workbench := range []bool{true, false} {
		t.Run(fmt.Sprint(workbench), func(t *testing.T) {
			model := &mockChat{}
			for round := 0; round < maxChartRenderFailures; round++ {
				model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{
					ID: fmt.Sprintf("chart-%d", round), Type: "function", Function: types.FunctionCall{Name: "render_chart", Arguments: `{"operation":"render"}`},
				}}}}})
			}
			const answer = "The available source describes covered area. The chart could not be generated because the selected columns have different declared units."
			verdict := deliveryVerdict("blocked")
			if !workbench {
				verdict = sourceConsistencyVerdict(`{"complete":true,"delivery_check":{"status":"blocked","assessment":"The candidate states the actual chart failure and offers supported text without claiming a saved image."}}`)
			}
			model.responses = append(model.responses, chatAnswer(answer), verdict)
			e := sourceConsistencyEngine(t, model)
			e.config.WorkbenchDefault, e.config.MaxIterations = workbench, 50
			e.taskPlan = chartDeliveryPlan()
			e.taskPlan.DeliveryRequirements = append(e.taskPlan.DeliveryRequirements, taskDeliveryRequirement{Kind: "text", Format: "prose", Criteria: "Explain the actual observed measure and limitations."})
			tool := &failedProgressChartTool{BaseTool: agenttools.NewBaseTool("render_chart", "synthetic chart", json.RawMessage(`{"type":"object"}`))}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(tool)
			state := sourceConsistencyState()
			messages := e.appendToolResults(emptyMessages(), state.RoundSteps[0])
			_, err := e.executeLoop(t.Context(), state, "Analyze the actual source.", messages, nil, "session", "message")
			require.NoError(t, err)
			require.Equal(t, maxChartRenderFailures, tool.calls)
			require.Equal(t, answer, state.FinalAnswer)
			require.Equal(t, "none", model.opts[maxChartRenderFailures].ToolChoice)
			require.Empty(t, model.opts[maxChartRenderFailures].Tools)
			require.Contains(t, model.opts[maxChartRenderFailures].RuntimeNotice, "chart render failure limit")
			require.Equal(t, 1, e.turnReview.Checks, "final prose still goes through the real candidate validator")
			require.True(t, e.candidateApprovalCurrent(answer, state))
		})
	}
}

// A provider is allowed to close the channel asynchronously. The engine must
// still respect the final phase deadline without waiting for that close.
type openFinalStreamChat struct{ mockChat }

func (model *openFinalStreamChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	return make(chan types.StreamResponse), nil
}

func TestToolStopSynthesisDeadlineCommitsBodyOnLiveParent(t *testing.T) {
	e := newTestEngine(t, &openFinalStreamChat{})
	state := &types.AgentState{}
	ctx := context.WithValue(t.Context(), toolStopSynthesisDeadlineKey{}, time.Now().Add(40*time.Millisecond))
	err := e.finishToolStoppedAnswer(ctx, "Summarize successful observations.", state, "session", emptyMessages(), "Tools stopped after repeated failures.")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, ctx.Err())
	require.True(t, state.IsComplete)
	require.NotEmpty(t, state.FinalAnswer)
	require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer)
}

type repeatedSourceProgressTool struct {
	agenttools.BaseTool
	data  map[string]any
	calls int
}

func (tool *repeatedSourceProgressTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	body, _ := json.Marshal(tool.data)
	return &types.ToolResult{Success: true, Data: tool.data, Output: string(body)}, nil
}

func TestNonWorkbenchChartAndStructuredStallsSynthesizeText(t *testing.T) {
	for _, operation := range []string{"inspect", "map", "structured"} {
		t.Run(operation, func(t *testing.T) {
			name, args := renderChartToolName, `{"operation":"`+operation+`"}`
			data := map[string]any{"kind": "chart_source_catalog", "sources": []any{map[string]any{"source_id": "same-source", "row_count": 1}}}
			if operation == "map" {
				data = map[string]any{"kind": "chart_source_mapping", "source_id": "mapped-source", "parent_source_id": "same-parent", "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}}}
			} else if operation == "structured" {
				name, args = "structured_analyze_table", `{}`
				data = chartLongFixture().Result.Data
			}
			model := &mockChat{}
			for i := 0; i < 1+toolProgressStopRounds; i++ {
				model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: fmt.Sprintf("call-%d", i), Type: "function", Function: types.FunctionCall{Name: name, Arguments: args}}}}}})
			}
			const answer = "The available observations cover the returned source only; the remaining work is unresolved."
			model.responses = append(model.responses, chatAnswer(answer), completenessVerdict(true))
			e := newTestEngine(t, model, withMaxIterations(50))
			e.toolRegistry = agenttools.NewToolRegistry()
			tool := &repeatedSourceProgressTool{BaseTool: agenttools.NewBaseTool(name, "synthetic actual source", json.RawMessage(`{"type":"object"}`)), data: data}
			e.toolRegistry.RegisterTool(tool)
			state, err := e.executeLoop(t.Context(), &types.AgentState{}, "Summarize the returned source.", emptyMessages(), nil, "session", "message")
			require.NoError(t, err)
			require.False(t, e.config.WorkbenchDefault)
			require.Equal(t, 1+toolProgressStopRounds, tool.calls)
			require.Equal(t, answer, state.FinalAnswer)
			require.Equal(t, "none", model.opts[tool.calls].ToolChoice)
		})
	}
}

func TestToolProgressMappedEquivalentExecutionsAndBusinessRecordFields(t *testing.T) {
	makeMap := func(id, sql string) types.ToolCall {
		provenance, err := json.Marshal(map[string]any{"kind": "source_cell_projection", "source_id": id,
			"source_provenance": `{"source":{"sha256":"real-version"},"sql":"` + sql + `","result_id":"` + id + `"}`})
		require.NoError(t, err)
		return progressChartCall("map", map[string]any{"kind": "chart_source_mapping", "parent_source_id": id, "source_id": id,
			"mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}},
			"columns": []any{"x", "measure"}, "rows": []any{[]any{"A", 5}}, "provenance": string(provenance)})
	}
	var guard toolProgressGuard
	require.Equal(t, 1, guard.observe([]types.ToolCall{makeMap("chart-source-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "SELECT measure FROM data")}).NewEvidence)
	require.Equal(t, 1, guard.observe([]types.ToolCall{makeMap("chart-source-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "SELECT measure FROM data WHERE TRUE")}).StaleRounds)
	for _, field := range []string{"query", "question", "sql", "notice", "created_at"} {
		var records toolProgressGuard
		call := types.ToolCall{Name: "structured_query_series", Result: &types.ToolResult{Success: true, Data: map[string]any{"rows": []any{map[string]any{field: "first observed value", "value": 5}}}}}
		require.Equal(t, 1, records.observe([]types.ToolCall{call}).NewEvidence)
		call.Result.Data["rows"] = []any{map[string]any{field: "changed observed value", "value": 5}}
		require.Equal(t, 1, records.observe([]types.ToolCall{call}).NewEvidence, "business field %s is source data", field)
	}
}

type continuousFinalStreamChat struct {
	mockChat
	stop chan struct{}
}

func (model *continuousFinalStreamChat) ChatStream(_ context.Context, _ []chat.Message, _ *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	stream := make(chan types.StreamResponse, 256)
	go func() {
		defer close(stream)
		for {
			select {
			case stream <- types.StreamResponse{ResponseType: types.ResponseTypeThinking, Content: "tick"}:
			case <-model.stop:
				return
			}
		}
	}()
	return stream, nil // Intentionally ignores context until test cleanup.
}

func TestFinalSynthesisDeadlineDoesNotDrainUnboundedProviderOutput(t *testing.T) {
	model := &continuousFinalStreamChat{stop: make(chan struct{})}
	t.Cleanup(func() { close(model.stop) })
	e := newTestEngine(t, model)
	state := &types.AgentState{}
	ctx := context.WithValue(t.Context(), toolStopSynthesisDeadlineKey{}, time.Now().Add(20*time.Millisecond))
	start := time.Now()
	err := e.finishToolStoppedAnswer(ctx, "Deliver the actual findings.", state, "session", emptyMessages(), "The tool budget was exhausted.")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	require.NotEmpty(t, state.FinalAnswer)
}
