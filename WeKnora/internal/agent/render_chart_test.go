package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	return &chart.Delivery{Handle: "resource://saved-chart", Index: 2, FileName: "chart-saved.html"}, nil
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
	require.Contains(t, result.Output, "resource://saved-chart")
	require.Len(t, writer.requests, 1)
	actual := writer.requests[0]
	require.Equal(t, "session", actual.SessionID)
	require.Equal(t, "message", actual.MessageID)
	require.Equal(t, "chart-call", actual.ToolCallID)
	require.Equal(t, source.ID, actual.SourceID)
	require.Contains(t, string(actual.HTML), "B3")
	require.Contains(t, string(actual.HTML), "15")
	require.NotContains(t, string(actual.HTML), "<script")
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
	require.Contains(t, string(out), "来源结果已截断")
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
