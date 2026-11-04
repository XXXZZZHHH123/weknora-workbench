package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func pdfCellChartFixture() types.ToolCall {
	return types.ToolCall{ID: "pdf-read", Name: "structured_read_pdf_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_source_inspection", "status": "ok", "source_kind": "pdf_table", "table_id": "actual-table", "record_sha256": strings.Repeat("ab", 32),
		"source":   map[string]any{"file_name": "isolated-table.pdf", "sha256": strings.Repeat("cd", 32)},
		"columns":  []map[string]any{{"name": "label"}, {"name": "first", "unit": "kg"}, {"name": "second", "unit": "kg"}, {"name": "third", "unit": "kg"}, {"name": "fourth", "unit": "kg"}},
		"rows":     []any{map[string]any{"values": map[string]any{"label": "Period", "first": "2024-02-03", "second": "2024-02-06", "third": "2024-02-10", "fourth": "2024-03-01"}}, map[string]any{"values": map[string]any{"label": "Harvest", "first": "8.25", "second": nil, "third": "11.5", "fourth": "9"}}},
		"coverage": map[string]any{"table_input_complete": true}, "page_context": "Harvest, unit kg; dates are original measurement dates.", "output_truncated": false, "offset": 0, "total_row_count": 2,
	}}}
}

func TestChartPDFCellMappingPreservesAllCoordinatesUnitsAndMissingValues(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{pdfCellChartFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	inputs := pdfChartInputSources(steps)
	require.Len(t, inputs, 1)
	result := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	require.True(t, result.Success)
	require.Empty(t, result.Data["sources"])
	require.Len(t, result.Data["input_sources"], 1)
	view := result.Data["input_sources"].([]any)[0].(map[string]any)
	require.Equal(t, map[string]int{"rows": 2, "columns": 5}, view["matrix_shape"])
	result = chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": inputs[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1, 2, 3, 4}, "label_index": 0, "unit_index": 1}})
	require.True(t, result.Success, result.Error)
	id := result.Data["source_id"].(string)
	mapped := tool.mapped[id].Table
	require.Equal(t, [][]string{{"2024-02-03", "8.25"}, {"2024-02-06", ""}, {"2024-02-10", "11.5"}, {"2024-03-01", "9"}}, mapped.Rows)
	require.Equal(t, "Harvest", mapped.Columns[1].Name)
	require.Equal(t, "kg", mapped.Columns[1].Unit)
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{1}})
	require.True(t, result.Success, result.Error)
	require.Len(t, writer.requests, 1)
	require.Equal(t, mapped.Rows, result.Data["selected_rows"])
	// A source result changing invalidates the mapped content-bound handle.
	steps[0].ToolCalls[0].Result.Data["source"] = map[string]any{"file_name": "changed.pdf", "sha256": strings.Repeat("ef", 32)}
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_column": 0, "y_columns": []int{1}})
	require.False(t, result.Success)
	require.Len(t, writer.requests, 1)
}

// A current SELECT * result includes a technical identity alongside the real
// horizontal cells. These are synthetic values, unrelated to an acceptance
// question; the runtime must project coordinates rather than inventing rows.
func pdfSQLHorizontalChartFixture() types.ToolCall {
	columns := []string{"_row_id", "label", "first", "second", "third"}
	dependencies := make([]map[string]any, len(columns))
	for index, name := range columns {
		dependencies[index] = map[string]any{"column_index": index, "column": name, "kind": "source_value", "source_columns": []string{name}, "source_row_dependency": true, "technical_metadata": false}
	}
	dependencies[0]["kind"] = "source_metadata"
	dependencies[0]["technical_metadata"] = true
	return types.ToolCall{ID: "pdf-sql-current", Name: "structured_query_pdf_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_analysis", "status": "ok", "source_kind": "pdf_table", "table_id": "actual-query-table",
		"source":  map[string]any{"file": "isolated-query.pdf", "sha256": strings.Repeat("ed", 32)},
		"columns": columns, "rows": [][]any{{"pdfr_axis", "Period", "2031", "2034", "2040"}, {"pdfr_measure", "Reading", "8.25", nil, "11.5"}},
		"sql": "SELECT * FROM data", "input_complete": false, "output_truncated": false,
		"sql_source_dependency_verified": true, "sql_source_dependency_method": "duckdb_ast_output_expression_dependencies_v1", "sql_column_dependencies": dependencies,
	}}}
}

func TestChartPDFSQLCatalogSourceMapsHorizontalCellsAndKeepsDependencies(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{pdfSQLHorizontalChartFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	inspect := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	require.True(t, inspect.Success)
	require.Empty(t, inspect.Data["input_sources"])
	require.Len(t, inspect.Data["sources"], 1)
	view := inspect.Data["sources"].([]any)[0].(map[string]any)
	id := view["source_id"].(string)
	require.Equal(t, map[string]int{"rows": 2, "columns": 5}, view["matrix_shape"])
	require.Equal(t, true, view["mapping_supported"])
	require.Equal(t, true, view["render_supported"])
	require.Equal(t, true, view["source_truncated"])
	mapping := map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1}
	invalid := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": "actual-query-table", "mapping": mapping})
	require.False(t, invalid.Success)
	require.Equal(t, "chart_mapping_source_not_current", invalid.Data["reason_code"])
	require.Equal(t, []string{id}, invalid.Data["available_mapping_source_ids"])
	result := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": id, "mapping": mapping})
	require.True(t, result.Success, result.Error)
	mappedID := result.Data["source_id"].(string)
	mapped := tool.mapped[mappedID].Table
	require.Equal(t, id, tool.mapped[mappedID].ParentID)
	require.Equal(t, [][]string{{"2031", "8.25"}, {"2034", ""}, {"2040", "11.5"}}, mapped.Rows)
	require.Equal(t, []chart.Column{{Name: "Period", Numeric: true, DependencyKind: "source_value"}, {Index: 1, Name: "Reading", Numeric: true, DependencyKind: "source_value"}}, mapped.Columns)
	require.True(t, mapped.Truncated)
	var proof struct {
		SourceProvenance  string `json:"source_provenance"`
		ProjectionColumns []struct {
			OutputIndex         int            `json:"output_index"`
			SourceColumnIndexes []int          `json:"source_column_indexes"`
			SourceRowIndexes    []int          `json:"source_row_indexes"`
			SourceColumns       []chart.Column `json:"source_columns"`
		} `json:"projection_columns"`
	}
	require.NoError(t, json.Unmarshal([]byte(mapped.Provenance), &proof))
	require.Contains(t, proof.SourceProvenance, `"sql_source_dependency_verified":true`)
	require.Contains(t, proof.SourceProvenance, `"technical_metadata":true`)
	require.Len(t, proof.ProjectionColumns, 2)
	for index, column := range proof.ProjectionColumns {
		require.Equal(t, index, column.OutputIndex)
		require.Equal(t, []int{2, 3, 4}, column.SourceColumnIndexes)
		require.Equal(t, []int{index}, column.SourceRowIndexes)
		require.Len(t, column.SourceColumns, 3)
		for originalIndex, original := range column.SourceColumns {
			require.Equal(t, []string{"first", "second", "third"}[originalIndex], original.Name)
			require.Equal(t, "source_value", original.DependencyKind)
		}
	}
	rendered := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": mappedID, "chart_type": "line", "x_type": "numeric", "x_column": 0, "y_columns": []int{1}})
	require.True(t, rendered.Success, rendered.Error)
	require.Equal(t, mapped.Rows, rendered.Data["selected_rows"])
	require.Len(t, writer.requests, 1)
	// Remapping an already verified projection keeps its original fresh anchor.
	remapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": mappedID, "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{0, 1, 2}}})
	require.True(t, remapped.Success, remapped.Error)
	remappedID := remapped.Data["source_id"].(string)
	require.Equal(t, id, tool.mapped[remappedID].ParentID)
	require.Equal(t, "source_value", tool.mapped[remappedID].Table.Columns[1].DependencyKind)
	steps[0].ToolCalls[0].Result.Data["source"] = map[string]any{"file": "isolated-query.pdf", "sha256": strings.Repeat("fe", 32)}
	for _, stale := range []string{id, mappedID, remappedID} {
		rejected := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": stale, "chart_type": "line", "x_type": "numeric", "x_column": 0, "y_columns": []int{1}})
		require.False(t, rejected.Success)
	}
	require.Len(t, writer.requests, 1)
	steps = nil
	rejected := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": mappedID, "mapping": mapping})
	require.False(t, rejected.Success)
	require.Empty(t, rejected.Data["available_mapping_source_ids"])
}

func TestChartPDFSQLMappingCannotLaunderTechnicalOrLiteralColumns(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      string
		technical bool
	}{
		{name: "literal", kind: "constant_label"},
		{name: "mixed", kind: "mixed_constant_values"},
		{name: "renamed-metadata", kind: "source_metadata", technical: true},
		{name: "technical-expression", kind: "source_value", technical: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := chartContext(t)
			call := pdfSQLHorizontalChartFixture()
			dependency := call.Result.Data["sql_column_dependencies"].([]map[string]any)[2]
			dependency["kind"], dependency["technical_metadata"] = test.kind, test.technical
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
			writer := &chartWriterFixture{}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
			inspect := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
			require.Len(t, inspect.Data["sources"], 1)
			id := inspect.Data["sources"].([]any)[0].(map[string]any)["source_id"].(string)
			for _, mapping := range []map[string]any{
				{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1},
				{"orientation": "columns", "x_index": 3, "y_indexes": []int{2}, "value_indexes": []int{0, 1}},
			} {
				mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": id, "mapping": mapping})
				require.False(t, mapped.Success)
				require.Empty(t, tool.mapped)
			}
			// Direct rendering remains guarded as well; failed maps cannot
			// manufacture a replacement handle or save an artifact.
			rendered := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_column": 3, "y_columns": []int{2}})
			require.False(t, rendered.Success)
			require.Empty(t, writer.requests)
		})
	}
	t.Run("technical-identity-as-values-or-label", func(t *testing.T) {
		call := pdfSQLHorizontalChartFixture()
		tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
		require.Len(t, tables, 1)
		zero, one := 0, 1
		for _, mapping := range []chartSourceMapping{
			{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{0, 2, 3}, LabelIndex: &one},
			{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{2, 3, 4}, LabelIndex: &zero},
			{Orientation: "columns", XIndex: &zero, YIndexes: []int{2}, ValueIndexes: []int{0, 1}},
		} {
			_, err := mapChartSource(tables[0], mapping)
			require.Error(t, err)
		}
	})
}

func TestChartMappingRejectsFakeValuesDuplicateCoordinatesAndNonReadSources(t *testing.T) {
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{pdfCellChartFixture()}}}
	input := pdfChartInputSources(steps)[0]
	zero := 0
	for _, tc := range []struct {
		mapping chartSourceMapping
		message string
	}{
		{chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{1}}, "需要label_index"},
		{chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{1, 1}, LabelIndex: &zero}, "不重复的真实观测坐标"},
		{chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{0}, ValueIndexes: []int{1}, LabelIndex: &zero}, "不同的真实行或列"},
		{chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{88}, LabelIndex: &zero}, "真实观测坐标"},
		{chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{0}, LabelIndex: &zero}, "不能包含标签坐标"},
	} {
		_, err := mapChartSource(input, tc.mapping)
		require.ErrorContains(t, err, tc.message)
	}
	ctx := chartContext(t)
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	result := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": input.ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1}, "values": []int{777}}})
	require.False(t, result.Success)
	steps[0].ToolCalls[0].Name = "structured_list_pdf_tables"
	require.Empty(t, pdfChartInputSources(steps))
}

func TestChartPDFLastPageAndUnknownReadCoverageRemainPartial(t *testing.T) {
	for _, total := range []any{nil, 4} {
		call := pdfCellChartFixture()
		call.Result.Data["offset"] = 2
		call.Result.Data["total_row_count"] = total
		inputs := pdfChartInputSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
		require.Len(t, inputs, 1)
		require.True(t, inputs[0].Truncated)
		require.Contains(t, inputs[0].Provenance, `"offset":2`)
	}
}

func TestChartRowMappingCannotHideMixedColumnUnits(t *testing.T) {
	call := pdfCellChartFixture()
	columns := call.Result.Data["columns"].([]map[string]any)
	columns[2]["unit"] = "tonnes"
	source := pdfChartInputSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})[0]
	zero, one := 0, 1
	_, err := mapChartSource(source, chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{1, 2}, LabelIndex: &zero, UnitIndex: &one})
	require.ErrorContains(t, err, "单位不一致")
	// Unit declarations are retained for the semantic checker even when the
	// model elects not to select a unit for the projected observation.
	require.Contains(t, source.Provenance, "tonnes")
}

func TestChartColumnDependenciesRejectMetadataAndLiteralMeasures(t *testing.T) {
	for _, column := range []chart.Column{{Name: "_row_id"}, {Name: "renamed", Role: "source_row_identifier"}, {Name: "literal", DependencyKind: "constant_label"}, {Name: "mixed", DependencyKind: "mixed_constant_values"}} {
		table := chart.Table{Columns: []chart.Column{{Name: "x"}, column}, Rows: [][]string{{"a", "1"}}}
		require.Error(t, validateChartSourceColumns(table, chart.Spec{X: 0, Y: []int{1}}))
	}
	table := chart.Table{Columns: []chart.Column{{Name: "label", DependencyKind: "constant_label"}, {Name: "count", DependencyKind: "source_cardinality"}}, Rows: [][]string{{"all", "4"}}}
	require.NoError(t, validateChartSourceColumns(table, chart.Spec{X: 0, Y: []int{1}}))
}

type chartReviewModel struct {
	mockChat
	response string
	err      error
	seen     []chat.Message
}

func (m *chartReviewModel) Chat(_ context.Context, messages []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.seen = messages
	return &types.ChatResponse{Content: m.response, FinishReason: "stop"}, m.err
}

func TestChartPlanReviewBeforeSaveAndPrivateReceiptBinding(t *testing.T) {
	ctx := chartContext(t)
	model := &chartReviewModel{response: `{"accepted":true,"complete_delivery":true,"required_x_type":"time","assessment":"The actual dates and measured values match the requested source and scope.","feedback":""}`}
	e := newTestEngine(t, model)
	e.toolRegistry = agenttools.NewToolRegistry()
	e.config.WorkbenchDefault = true
	e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: "Draw the measured readings over their actual dates."}}}
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Draw the measured readings over their actual dates."}}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, writer, "session", "message")
	tool.reviewPlan = e.chartPlanReviewer("Draw the measured readings over their actual dates.", state)
	e.toolRegistry.RegisterTool(tool)
	tables, _ := chartSources(state.RoundSteps)
	source := tables[0]
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": source.ID, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{2}})
	require.True(t, result.Success, result.Error)
	require.Len(t, writer.requests, 1)
	call := types.ToolCall{ID: "chart-call", Name: renderChartToolName, Result: result}
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	require.True(t, e.isVerifiedChartDelivery("Draw the measured readings over their actual dates.", result.Output, state, nil))
	require.False(t, e.isVerifiedChartDelivery("Another task", result.Output, state, nil))
	require.False(t, e.isVerifiedChartDelivery("Draw the measured readings over their actual dates.", result.Output+"Extra conclusion.", state, nil))
	result.Data["row_count"] = 999
	require.False(t, e.isVerifiedChartDelivery("Draw the measured readings over their actual dates.", result.Output, state, nil))
	request, _ := json.Marshal(model.seen)
	require.Contains(t, string(request), "selected_rows")
	require.Contains(t, string(request), "2025-01-03")
}

func TestChartRejectedSemanticPlanDoesNotSaveOrCreateReceipt(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	tool.reviewPlan = func(context.Context, chart.Table, chart.Spec) (*chartSemanticValidation, error) {
		return nil, errors.New("selected dimensions do not match the requested variable")
	}
	tables, _ := chartSources(steps)
	r := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": tables[0].ID, "chart_type": "bar", "x_column": 0, "y_columns": []int{2}})
	require.False(t, r.Success)
	require.Empty(t, writer.requests)
	require.Empty(t, tool.receipts)
}

func TestChartReviewOmittedImagesCannotAuthorizeFastDelivery(t *testing.T) {
	model := &chartReviewModel{response: `{"accepted":true,"complete_delivery":true,"required_x_type":"time","assessment":"The text task matches this selected data.","feedback":""}`}
	e := newTestEngine(t, model)
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Draw using this reference.", Images: []string{"https://example.invalid/reference.png"}}}
	tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}})
	verdict, err := e.chartPlanReviewer("Draw using this reference.", &types.AgentState{})(chartContext(t), tables[0], chart.Spec{X: 0, Y: []int{2}, Type: "line", XType: "time"})
	require.NoError(t, err)
	require.False(t, verdict.FullTaskView)
}

func TestChartSemanticApprovalCannotOverrideRequiredAxisType(t *testing.T) {
	model := &chartReviewModel{response: `{"accepted":true,"complete_delivery":true,"required_x_type":"time","assessment":"The user requires actual time intervals.","feedback":""}`}
	e := newTestEngine(t, model)
	tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}})
	_, err := e.chartPlanReviewer("Preserve actual date intervals.", &types.AgentState{})(chartContext(t), tables[0], chart.Spec{Type: "line", X: 0, Y: []int{2}, XType: "category"})
	require.ErrorContains(t, err, "轴类型与完整任务不符")
}

func TestChartPDFSQLRequiresVerifiedOutputDependencyContract(t *testing.T) {
	call := queryChartFixture()
	call.Name = "structured_query_pdf_table"
	call.Result.Data = map[string]any{"kind": "structured_analysis", "status": "ok", "source_kind": "pdf_table", "source": map[string]any{"file": "isolated.pdf", "sha256": strings.Repeat("ab", 32)}, "columns": []string{"period", "value"}, "rows": [][]any{{"A", 4}, {"B", 5}}, "output_truncated": false}
	_, err := checkedStructuredChartTable(call)
	require.ErrorContains(t, err, "dependency_not_verified")
	call.Result.Data["sql_source_dependency_verified"] = true
	call.Result.Data["sql_column_dependencies"] = []map[string]any{{"column_index": 0, "column": "period", "kind": "constant_label"}, {"column_index": 1, "column": "value", "kind": "source_value"}}
	table, err := checkedStructuredChartTable(call)
	require.NoError(t, err)
	require.Equal(t, "source_value", table.Columns[1].DependencyKind)
	call.Result.Data["sql_column_dependencies"] = []map[string]any{{"column_index": 0, "column": "period", "kind": "source_metadata", "technical_metadata": true}, {"column_index": 1, "column": "value", "kind": "source_value"}}
	table, err = checkedStructuredChartTable(call)
	require.NoError(t, err)
	require.Error(t, validateChartSourceColumns(table, chart.Spec{X: 0, Y: []int{1}}))
}
