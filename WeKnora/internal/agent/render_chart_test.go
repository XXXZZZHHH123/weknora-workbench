package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type chartWriterFixture struct {
	requests []chart.SaveRequest
	fail     bool
}

func (w *chartWriterFixture) SaveChart(_ context.Context, request chart.SaveRequest) (*chart.Delivery, error) {
	w.requests = append(w.requests, request)
	if w.fail {
		return nil, errors.New("private storage error")
	}
	return &chart.Delivery{Handle: "resource://abcdefghijklmnopqrstuv", Index: 2, FileName: "chart-saved.png"}, nil
}
func chartContext(t *testing.T) context.Context {
	t.Helper()
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "alice")
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: "alice"})
	return agenttools.WithToolExecContext(ctx, &agenttools.ToolExecContext{SessionID: "session", AssistantMessageID: "message", ToolCallID: "chart-call"})
}
func queryChartFixture() types.ToolCall {
	return types.ToolCall{ID: "real-query", Name: "structured_query_series", Result: &types.ToolResult{Success: true, Data: map[string]any{"status": "ok", "rows_are": "observations", "source": map[string]any{"file": "actual.xlsx", "sha256": strings.Repeat("ab", 32)}, "series": map[string]any{"unit": "kg"}, "truncated": false, "rows": []any{map[string]any{"date": "2025-01-01", "value": 10.0, "source_cell": "B2"}, map[string]any{"date": "2025-01-02", "value": nil, "source_cell": "B3"}, map[string]any{"date": "2025-01-03", "value": 15.0, "source_cell": "B4"}}}}}
}
func chartRun(t *testing.T, tool *renderChartTool, ctx context.Context, args any) *types.ToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	result, err := tool.Execute(ctx, raw)
	require.NoError(t, err)
	return result
}
func TestRenderChartInspectsActualRowsAndSavesTheSelectedServerEvidence(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	inspect := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	require.True(t, inspect.Success)
	tables, _ := chartSources(steps)
	require.Len(t, tables, 1)
	source := tables[0]
	x, y := -1, -1
	for _, col := range source.Columns {
		if col.Name == "date" {
			x = col.Index
		}
		if col.Name == "value" {
			y = col.Index
			require.Equal(t, "kg", col.Unit)
		}
	}
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": source.ID, "x_column": x, "y_columns": []int{y}, "chart_type": "line", "x_type": "time"})
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, "resource://abcdefghijklmnopqrstuv")
	require.Len(t, writer.requests, 1)
	actual := writer.requests[0]
	require.Equal(t, "session", actual.SessionID)
	require.Equal(t, "message", actual.MessageID)
	require.Equal(t, "chart-call", actual.ToolCallID)
	require.Equal(t, source.ID, actual.SourceID)
	_, err := png.Decode(bytes.NewReader(actual.PNG))
	require.NoError(t, err)
	require.Equal(t, "chart.png", actual.FileName)
	require.Equal(t, "![图表](resource://abcdefghijklmnopqrstuv)", result.Output)
	require.Equal(t, "chart_only", result.Data["delivery_mode"])
	require.Equal(t, source.Provenance, result.Data["provenance"], "source audit remains available outside the picture")
	// Changing a successful source invalidates an old content-bound handle.
	steps[0].ToolCalls[0].Result.Data["rows"] = []any{map[string]any{"date": "2025-01-01", "value": 999.0}}
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": source.ID, "x_column": x, "y_columns": []int{y}, "chart_type": "line"})
	require.False(t, result.Success)
	require.Len(t, writer.requests, 1)
}
func TestRenderChartRejectsMetadataFailedQueriesAndArbitraryNumericArguments(t *testing.T) {
	failed := queryChartFixture()
	failed.Result.Success = false
	directory := queryChartFixture()
	directory.Name = "structured_list_series"
	metadata := queryChartFixture()
	metadata.Result.Data["rows_are"] = "source_examples_not_all_inputs"
	unknown := queryChartFixture()
	unknown.Name = "some_untrusted_tool"
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{failed, directory, metadata, unknown}}}
	tables, _ := chartSources(steps)
	require.Empty(t, tables)
	ctx := chartContext(t)
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": "made-up", "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "values": []int{101, 102}})
	require.False(t, result.Success)
	require.Empty(t, writer.requests)
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": "made-up", "x_column": 0, "y_columns": []int{1}, "chart_type": "line"})
	require.False(t, result.Success)
	require.Empty(t, writer.requests)
}
func TestRenderChartDocumentSourcesAreActualFetchedTablesOnly(t *testing.T) {
	steps := analysisSteps("Unit: kg\n| Label | Actual |\n| --- | --- |\n| first | 2 |\n| second | 9 |\n")
	tables, _ := chartSources(steps)
	require.Len(t, tables, 1)
	require.Equal(t, [][]string{{"first", "2"}, {"second", "9"}}, tables[0].Rows)
	require.Contains(t, tables[0].Provenance, "actual-chunk")
	steps[0].ToolCalls[0].Result.Success = false
	tables, _ = chartSources(steps)
	require.Empty(t, tables)
}
func TestRenderChartRetainsPartialFlagsAndSQLResultColumns(t *testing.T) {
	call := queryChartFixture()
	call.Name = "structured_analyze_table"
	call.Result.Data = map[string]any{"kind": "structured_analysis", "status": "ok", "columns": []string{"label", "value"}, "rows": [][]any{{"a", 2}, {"b", 3}}, "output_truncated": true, "source": map[string]any{"file": "actual.xlsx", "sha256": strings.Repeat("cd", 32)}}
	tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
	require.Len(t, tables, 1)
	require.True(t, tables[0].Truncated)
	require.Equal(t, "label", tables[0].Columns[0].Name)
	out, err := chart.Render(tables[0], chart.Spec{Type: "bar", X: 0, Y: []int{1}})
	require.NoError(t, err)
	_, err = png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
}

func TestRenderChartDeliveryModeRetainsExplicitAnalysisAndRejectsUnknownModes(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	sources, _ := chartSources(steps)
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	args := map[string]any{"operation": "render", "source_id": sources[0].ID, "x_column": 0, "y_columns": []int{2}, "chart_type": "line", "delivery_mode": "chart_with_analysis"}
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "chart_with_analysis", result.Data["delivery_mode"])
	require.Len(t, writer.requests, 1)
	args["delivery_mode"] = "unknown"
	result = chartRun(t, tool, ctx, args)
	require.False(t, result.Success)
	require.Len(t, writer.requests, 1, "invalid mode must not save an attachment")
}
func TestRenderChartEnforcesTurnAndCallerBoundaries(t *testing.T) {
	ctx := chartContext(t)
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return nil }, &chartWriterFixture{}, "session", "message")
	for name, foreign := range map[string]context.Context{"no_execution": t.Context(), "other_session": agenttools.WithToolExecContext(ctx, &agenttools.ToolExecContext{SessionID: "other", AssistantMessageID: "message", ToolCallID: "call"}), "other_caller": types.WithCaller(ctx, types.Caller{TenantID: 2, UserID: "alice"})} {
		t.Run(name, func(t *testing.T) {
			result := chartRun(t, tool, foreign, map[string]any{"operation": "inspect"})
			require.False(t, result.Success)
		})
	}
	shared := types.WithExecutionTenant(types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "alice"}), 2)
	result := chartRun(t, tool, shared, map[string]any{"operation": "inspect"})
	require.True(t, result.Success, "authorized execution scope does not change caller-owned source/attachment scope")
}

func TestRenderChartRegistrationIsAvailableInWorkbenchCapabilityDirectory(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "Ready.", Done: true, FinishReason: "stop"}}}}}
	e := newTestEngine(t, model)
	e.toolRegistry = agenttools.NewToolRegistry()
	e.config.WorkbenchDefault = true
	e.SetChartWriter(&chartWriterFixture{})
	_, err := e.Execute(chartContext(t), "session", "message", "Describe your capabilities", nil)
	require.NoError(t, err)
	registered, err := e.toolRegistry.GetTool(agenttools.ToolRenderChart)
	require.NoError(t, err)
	require.IsType(t, &renderChartTool{}, registered)
	require.True(t, isTaskSourceProtocolTool(renderChartToolName))
	require.False(t, agenttools.CanRunConcurrently(renderChartToolName))
	require.Contains(t, registered.Description(), "确定性绘图")
}
func TestRenderChartStorageFailureDoesNotClaimDelivery(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	sources, _ := chartSources(steps)
	writer := &chartWriterFixture{fail: true}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": sources[0].ID, "x_column": 0, "y_columns": []int{2}, "chart_type": "line"})
	require.False(t, result.Success)
	require.Contains(t, result.Error, "未交付")
	require.NotContains(t, result.Error, "private storage error")
}

func largeAuditChartFixture() types.ToolCall {
	call := queryChartFixture()
	call.Name = "structured_analyze_table"
	call.Result.Data = map[string]any{
		"kind": "structured_analysis", "status": "ok", "dataset_id": "real-dataset", "result_id": "actual-analysis-result",
		"source":  map[string]any{"file": "measurements.xlsx", "sha256": strings.Repeat("cd", 32)},
		"columns": []string{"date", "measurement"}, "rows": [][]any{{"2025-01-01", 2.5}, {"2025-01-02", nil}, {"2025-01-03", 3.75}},
		"sql": "SELECT date, value AS measurement FROM data ORDER BY date", "filters": map[string]any{"region": "actual-region"},
		"requested_range": map[string]any{"start_date": "2025-01-01", "end_date": "2025-01-03"},
		"input_complete":  true, "output_truncated": false,
		"inputs": map[string]any{"actual-series": map[string]any{
			"series":      map[string]any{"series_id": "actual-series", "sheet": "Measurements", "metric": "Observed quantity", "unit": "kg", "unit_status": "explicit"},
			"input_count": 3, "valid_count": 2, "actual_first_date": "2025-01-01", "actual_last_date": "2025-01-03",
			"observation_coverage": map[string]any{"detail": strings.Repeat("audit evidence ", 1300)},
		}},
	}
	return call
}

func TestRenderChartProjectsOversizedInputAuditWithoutChangingRowsOrCoreMetadata(t *testing.T) {
	call := largeAuditChartFixture()
	original, err := json.Marshal(call.Result.Data)
	require.NoError(t, err)
	require.Greater(t, len(original), 18*1024)
	tables, omitted := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
	require.Zero(t, omitted)
	require.Len(t, tables, 1)
	source := tables[0]
	require.LessOrEqual(t, len(source.Provenance), 16*1024)
	require.Equal(t, [][]string{{"2025-01-01", "2.5"}, {"2025-01-02", ""}, {"2025-01-03", "3.75"}}, source.Rows)
	require.False(t, source.Truncated, "audit projection must not rewrite actual row/input completeness")
	var audit map[string]any
	require.NoError(t, json.Unmarshal([]byte(source.Provenance), &audit))
	for _, field := range []string{"source", "dataset_id", "sql", "result_id", "filters", "requested_range", "input_complete", "output_truncated"} {
		expected, _ := json.Marshal(call.Result.Data[field])
		actual, _ := json.Marshal(audit[field])
		require.JSONEq(t, string(expected), string(actual), field)
	}
	projection := audit["audit_projection"].(map[string]any)
	require.Equal(t, true, projection["projection_partial"])
	inputs, _ := json.Marshal(call.Result.Data["inputs"])
	digest := sha256.Sum256(inputs)
	require.Equal(t, hex.EncodeToString(digest[:]), projection["inputs_sha256"])
	require.EqualValues(t, len(inputs), projection["inputs_original_bytes"])
	input := audit["inputs"].(map[string]any)["actual-series"].(map[string]any)
	require.Equal(t, "kg", input["series"].(map[string]any)["unit"])
	require.EqualValues(t, 3, input["input_count"])
	require.NotContains(t, input, "observation_coverage")
	unchanged, _ := json.Marshal(call.Result.Data)
	require.Equal(t, original, unchanged, "projection must never mutate the true canonical tool result")
}

func TestRenderChartLargeAuditDigestAndRowsAndSourceRemainBoundToHandle(t *testing.T) {
	for _, change := range []string{"omitted-audit", "row", "source"} {
		t.Run(change, func(t *testing.T) {
			ctx := chartContext(t)
			call := largeAuditChartFixture()
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
			sources, _ := chartSources(steps)
			require.Len(t, sources, 1)
			previous := sources[0]
			switch change {
			case "omitted-audit":
				call.Result.Data["inputs"].(map[string]any)["actual-series"].(map[string]any)["observation_coverage"].(map[string]any)["detail"] = strings.Repeat("different audit ", 1300)
			case "row":
				call.Result.Data["rows"] = [][]any{{"2025-01-01", 99}, {"2025-01-03", 100}}
			case "source":
				call.Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("ef", 32)
			}
			updated, _ := chartSources(steps)
			require.Len(t, updated, 1)
			require.NotEqual(t, previous.ID, updated[0].ID)
			writer := &chartWriterFixture{}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
			result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": previous.ID, "x_column": 0, "y_columns": []int{1}, "chart_type": "line"})
			require.False(t, result.Success)
			require.Empty(t, writer.requests)
		})
	}
}

func TestRenderChartReportsRejectedSourceAndDoesNotDropOversizedCoreAudit(t *testing.T) {
	for _, field := range []string{"source", "sql"} {
		t.Run(field, func(t *testing.T) {
			call := largeAuditChartFixture()
			if field == "source" {
				call.Result.Data["source"].(map[string]any)["file"] = strings.Repeat("x", 4097)
			} else {
				call.Result.Data["sql"] = strings.Repeat("SELECT source_data ", 1300)
			}
			ctx := chartContext(t)
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
			result := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
			require.True(t, result.Success)
			require.Empty(t, result.Data["sources"])
			require.Equal(t, 1, result.Data["omitted_source_count"])
			require.Equal(t, 1, result.Data["rejected_source_count"])
			rejections := result.Data["source_rejections"].([]map[string]any)
			require.Len(t, rejections, 1)
			require.Equal(t, call.ID, rejections[0]["tool_call_id"])
			require.Contains(t, rejections[0]["reason"], "over_budget")
		})
	}
}

func TestRenderChartRequiresChartHandleAndCanRenderLargeAuditAnalysis(t *testing.T) {
	ctx := chartContext(t)
	call := largeAuditChartFixture()
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	inspected := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	sourceID := inspected.Data["sources"].([]any)[0].(map[string]any)["source_id"]
	args := map[string]any{"operation": "render", "source_id": call.Result.Data["result_id"], "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": "time"}
	wrong := chartRun(t, tool, ctx, args)
	require.False(t, wrong.Success)
	require.Contains(t, wrong.Data["available_source_ids"], sourceID)
	require.Empty(t, writer.requests)
	args["source_id"] = sourceID
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	require.Len(t, writer.requests, 1)
	_, err := png.Decode(bytes.NewReader(writer.requests[0].PNG))
	require.NoError(t, err)
}

func TestRenderChartRejectionDiagnosticsStayBoundedAndKeepTotalCount(t *testing.T) {
	ctx := chartContext(t)
	call := largeAuditChartFixture()
	call.Result.Data["source"].(map[string]any)["file"] = strings.Repeat("x", 4097)
	calls := []types.ToolCall{}
	for i := 0; i < chart.MaxSources+3; i++ {
		call.ID = strings.Repeat("long-tool-call-id", 40) + strings.Repeat("a", i+1)
		calls = append(calls, call)
	}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return []types.AgentStep{{ToolCalls: calls}} }, &chartWriterFixture{}, "session", "message")
	result := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	require.Equal(t, chart.MaxSources+3, result.Data["rejected_source_count"])
	require.Equal(t, true, result.Data["source_rejections_truncated"])
	rejections := result.Data["source_rejections"].([]map[string]any)
	require.Len(t, rejections, chart.MaxSources)
	require.Less(t, len(rejections[0]["tool_call_id"].(string)), 80)
	require.LessOrEqual(t, len(result.Output), 64*1024)
}
