package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func chartRecoverySQLFixture() types.ToolCall {
	call := pdfSQLHorizontalChartFixture()
	call.Args = map[string]any{"knowledge_base_id": "source-kb", "table_id": "actual-query-table"}
	call.Result.Data["source"].(map[string]any)["knowledge_base_id"] = "source-kb"
	call.Result.Data["source_context"] = map[string]any{"knowledge_base_id": "source-kb"}
	return call
}

func chartRecoveryModelDiagnostic(t *testing.T, registry *modelcontext.Registry, result *types.ToolResult) map[string]any {
	t.Helper()
	require.False(t, result.Success)
	text := registry.ModelToolResultForTool(renderChartToolName, result)
	require.Contains(t, text, result.Error, "the model must retain the actual failure alongside recovery data")
	var body map[string]any
	require.NoError(t, json.NewDecoder(strings.NewReader(text)).Decode(&body))
	require.Equal(t, result.Error, body["error"])
	return body["source_diagnostics"].(map[string]any)
}

func TestChartColumnMappingLabelColumnDoesNotExcludeObservationRowZero(t *testing.T) {
	source := chart.Table{Columns: []chart.Column{{Name: "Position"}, {Name: "Reading", Unit: "kg"}}, Rows: [][]string{{"A", "3"}, {"B", "7"}, {"C", "4"}}}
	require.NoError(t, source.Seal("synthetic-source"))
	zero, one := 0, 1
	mapped, err := mapChartSource(source, chartSourceMapping{Orientation: "columns", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{0, 1, 2}, LabelIndex: &zero, UnitIndex: &one})
	require.NoError(t, err)
	require.Equal(t, source.Rows, mapped.Rows)
	require.Equal(t, "Position", mapped.Columns[0].Name)
	require.Equal(t, "kg", mapped.Columns[1].Unit)
	_, err = mapChartSource(source, chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{0, 1}, LabelIndex: &zero})
	require.ErrorContains(t, err, "不能包含标签坐标", "rows mode still excludes its actual label column")
}

func TestChartMappingRecoveryReachesModelAndOnlyLocatesItsCurrentPDFSource(t *testing.T) {
	for _, test := range []struct {
		name     string
		registry *modelcontext.Registry
	}{{"citations", modelcontext.NewRegistry(true)}, {"without-citations", modelcontext.NewRegistry(false)}, {name: "nil-facade"}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := chartContext(t)
			call := chartRecoverySQLFixture()
			other := chartRecoverySQLFixture()
			other.ID, other.Args["table_id"] = "other-execution", "other-table"
			other.Result.Data["table_id"] = "other-table"
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call, other}}}
			writer := &chartWriterFixture{}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
			tables, _ := chartSources(steps)
			require.Len(t, tables, 2)
			failed := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": tables[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 2}, "label_index": 1}})
			diagnostic := chartRecoveryModelDiagnostic(t, test.registry, failed)
			require.Equal(t, tables[0].ID, diagnostic["source_id"])
			require.Equal(t, map[string]any{"rows": float64(2), "columns": float64(5)}, diagnostic["matrix_shape"])
			require.Contains(t, diagnostic["mapping_coordinates"].(map[string]any)["columns"], "第0行")
			menu := diagnostic["raw_source_reads"].([]any)
			require.Len(t, menu, 1)
			read := menu[0].(map[string]any)
			require.Equal(t, "structured_read_pdf_table", read["tool"])
			require.Equal(t, map[string]any{"knowledge_base_id": "source-kb", "table_id": "actual-query-table"}, read["arguments"])
			require.Equal(t, call.ID, read["source_tool_call_id"])
			require.NotContains(t, failed.Output, "other-table")
			require.NotContains(t, failed.Output, "SELECT", "navigation must not generate replacement SQL")
			require.Empty(t, writer.requests)
			require.Empty(t, tool.mapped)
			require.Empty(t, tool.receipts)
			require.Less(t, len(failed.Output), 4096)
		})
	}
}

func TestChartSemanticFailureRecoveryTracksExactRootThroughRemapping(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{chartRecoverySQLFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	tables, _ := chartSources(steps)
	mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": tables[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1}})
	require.True(t, mapped.Success, mapped.Error)
	remapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": mapped.Data["source_id"], "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{0, 1, 2}, "label_index": 0}})
	require.True(t, remapped.Success, remapped.Error)
	id := remapped.Data["source_id"].(string)
	var reviewed chart.Spec
	tool.reviewPlan = func(_ context.Context, _ chart.Table, spec chart.Spec) (*chartSemanticValidation, error) {
		reviewed = spec
		return nil, errors.New("Selected axes do not match the original cells")
	}
	render := map[string]any{"operation": "render", "source_id": id, "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": "numeric"}
	failed := chartRun(t, tool, ctx, render)
	diagnostic := chartRecoveryModelDiagnostic(t, modelcontext.NewRegistry(true), failed)
	require.Equal(t, id, diagnostic["source_id"])
	require.Equal(t, map[string]any{"rows": float64(3), "columns": float64(2)}, diagnostic["matrix_shape"])
	read := diagnostic["raw_source_reads"].([]any)[0].(map[string]any)
	require.Equal(t, "pdf-sql-current", read["source_tool_call_id"])
	require.Equal(t, "actual-query-table", read["arguments"].(map[string]any)["table_id"])
	require.Equal(t, "numeric", reviewed.XType)
	require.Empty(t, writer.requests, "rejected semantic review must not save an image")
	require.Empty(t, tool.receipts)
	tool.reviewPlan = func(_ context.Context, _ chart.Table, spec chart.Spec) (*chartSemanticValidation, error) {
		require.Equal(t, reviewed, spec)
		return nil, incompleteChartReview("chart_review_invalid_protocol")
	}
	temporary := chartRun(t, tool, ctx, render)
	diagnostic = chartRecoveryModelDiagnostic(t, nil, temporary)
	require.Contains(t, temporary.Error, "全部绘图参数不变重试")
	require.Contains(t, diagnostic["notice"], "仅明确的语义不匹配")
	require.Empty(t, writer.requests)
	// A changed original record invalidates every projection and its navigation.
	steps[0].ToolCalls[0].Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("bc", 32)
	for _, stale := range []string{tables[0].ID, mapped.Data["source_id"].(string), id, "chart-source-guessed"} {
		for _, operation := range []string{"map", "render"} {
			args := map[string]any{"operation": operation, "source_id": stale, "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": "numeric", "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1}}
			rejected := chartRun(t, tool, ctx, args)
			require.False(t, rejected.Success)
			require.NotContains(t, rejected.Data, "source_diagnostics")
			require.NotContains(t, rejected.Output, "raw_source_reads")
		}
	}
	unauthorized := agenttools.WithToolExecContext(ctx, &agenttools.ToolExecContext{SessionID: "other-session", AssistantMessageID: "message", ToolCallID: "chart-call"})
	rejected := chartRun(t, tool, unauthorized, render)
	require.False(t, rejected.Success)
	require.Contains(t, rejected.Error, "已认证会话")
	require.NotContains(t, rejected.Data, "source_diagnostics")
}

func TestChartRawRecoveryRequiresExactSuccessfulRecordAndConsistentIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*types.ToolCall)
	}{
		{"failed-record", func(c *types.ToolCall) { c.Result.Success = false }},
		{"different-handle", func(c *types.ToolCall) { c.ID = "other-call" }},
		{"table-argument-conflict", func(c *types.ToolCall) { c.Args["table_id"] = "other-table" }},
		{"kb-argument-conflict", func(c *types.ToolCall) { c.Args["knowledge_base_id"] = "other-kb" }},
		{"kb-context-conflict", func(c *types.ToolCall) {
			c.Result.Data["source_context"] = map[string]any{"knowledge_base_id": "other-kb"}
		}},
		{"missing-table-only-nested", func(c *types.ToolCall) {
			delete(c.Result.Data, "table_id")
			c.Result.Data["provenance"] = map[string]any{"table_id": "nested-table"}
		}},
		{"missing-kb", func(c *types.ToolCall) { delete(c.Result.Data["source"].(map[string]any), "knowledge_base_id") }},
		{"oversized-table", func(c *types.ToolCall) { c.Result.Data["table_id"] = strings.Repeat("x", 257) }},
		{"invalid-sql-contract", func(c *types.ToolCall) { c.Result.Data["sql_source_dependency_verified"] = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := chartRecoverySQLFixture()
			tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
			require.Len(t, tables, 1)
			test.change(&call)
			require.Empty(t, chartRawSourceReads([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}}, tables[0].ID))
		})
	}
	call := chartRecoverySQLFixture()
	call.Name = "model-alias"
	call.Target = &types.ToolCallTarget{Name: "structured_data", Args: map[string]any{"operation": "query_pdf_table", "knowledge_base_id": "source-kb", "arguments": map[string]any{"table_id": "actual-query-table"}}}
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	tables, _ := chartSources(steps)
	require.Len(t, tables, 1)
	require.Len(t, chartRawSourceReads(steps, tables[0].ID), 1, "resolved wrapper arguments must bind the same actual source")
	inner := call.Target.Args["arguments"].(map[string]any)
	inner["knowledge_base_id"] = "source-kb"
	require.Empty(t, chartRawSourceReads(steps, tables[0].ID), "even a matching inner KB is not a valid gateway scope location")
	delete(inner, "knowledge_base_id")
	call.Target.Args["knowledge_base_id"] = "other-kb"
	require.Empty(t, chartRawSourceReads(steps, tables[0].ID), "the outer gateway scope must match the actual result")
	call.Target.Args["knowledge_base_id"] = "source-kb"
	for i := 0; i < chartRecoveryCallBudget; i++ {
		steps[0].ToolCalls = append(steps[0].ToolCalls, types.ToolCall{})
	}
	require.Empty(t, chartRawSourceReads(steps, tables[0].ID), "the bounded navigation scan must fail closed")
}

func TestChartRawReadMappingFailureOffersOnlyAuthenticatedReadLocator(t *testing.T) {
	ctx := chartContext(t)
	call := pdfCellChartFixture()
	call.Args = map[string]any{"knowledge_base_id": "raw-kb", "table_id": "actual-table"}
	call.Result.Data["source"].(map[string]any)["knowledge_base_id"] = "raw-kb"
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	input := pdfChartInputSources(steps)[0]
	failed := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": input.ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1, 2}}})
	diagnostic := chartRecoveryModelDiagnostic(t, nil, failed)
	read := diagnostic["raw_source_reads"].([]any)[0].(map[string]any)
	require.Equal(t, map[string]any{"knowledge_base_id": "raw-kb", "table_id": "actual-table"}, read["arguments"])
	require.NotContains(t, read["arguments"], "sql")
	require.Empty(t, tool.mapped)
	call.Target = &types.ToolCallTarget{Name: "structured_data", Args: map[string]any{"operation": "read_pdf_table", "knowledge_base_id": "raw-kb", "arguments": map[string]any{"table_id": "actual-table"}}}
	steps[0].ToolCalls[0] = call
	failed = chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": input.ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1, 2}}})
	diagnostic = chartRecoveryModelDiagnostic(t, nil, failed)
	read = diagnostic["raw_source_reads"].([]any)[0].(map[string]any)
	require.Equal(t, map[string]any{"knowledge_base_id": "raw-kb", "table_id": "actual-table"}, read["arguments"])
}

func TestChartRepeatedMappingFeedbackExplainsProjectionBoundary(t *testing.T) {
	guards := &loopGuards{}
	guards.recordFailedToolCall(feedbackOperation(renderChartToolName, "map", 1, false))
	name, attempts, nudge := guards.recordFailedToolCall(feedbackOperation(renderChartToolName, "map", 2, false))
	require.True(t, nudge)
	guidance := guards.failedToolLoopGuidance(name, attempts)
	require.Contains(t, guidance, "cannot repair values already paired incorrectly by SQL")
	require.Contains(t, guidance, "raw_source_reads")
	require.Contains(t, guidance, "not a business-axis decision")
}
