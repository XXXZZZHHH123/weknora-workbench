package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func chartLongFixture() types.ToolCall {
	return types.ToolCall{ID: "real-long-query", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"kind": "structured_analysis", "status": "ok", "columns": []string{"date", "metric", "unit", "value"},
		"rows":   [][]any{{"2025-01-01", "原字段甲", "kg", 2}, {"2025-01-01", "原字段乙", "kg", 3}, {"2025-01-02", "原字段乙", "kg", 5}},
		"source": map[string]any{"file": "fixture.xlsx", "sha256": strings.Repeat("ab", 32)}, "output_truncated": false,
	}}}
}

func TestChartLongRelationUsesExactRealLabelsUnitsAndMissingCells(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{chartLongFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	sources, _ := chartSources(steps)
	require.Len(t, sources, 1)
	parentID := sources[0].ID
	reviewed := false
	tool.reviewPlan = func(_ context.Context, source chart.Table, spec chart.Spec) (*chartSemanticValidation, error) {
		reviewed = true
		require.Equal(t, []string{"原字段甲", "原字段乙"}, []string{source.Columns[1].Label(), source.Columns[2].Label()})
		require.Equal(t, "kg", source.Columns[1].Unit)
		require.Equal(t, "kg", source.Columns[2].Unit)
		require.Equal(t, []int{0}, source.Columns[1].UnitMetadata.Evidence[0]["source_row_indexes"])
		require.Equal(t, []int{1, 2}, source.Columns[2].UnitMetadata.Evidence[0]["source_row_indexes"])
		require.Equal(t, [][]string{{"2025-01-01", "2", "3"}, {"2025-01-02", "", "5"}}, source.Rows)
		require.Equal(t, []int{1, 2}, spec.Y)
		return &chartSemanticValidation{EvidenceHash: reviewDigest([]any{source, source.Rows})}, nil
	}
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": parentID, "x_column": 0, "y_columns": []int{3}, "series_column": 1, "unit_column": 2, "chart_type": "line", "x_type": "time"})
	require.True(t, result.Success, result.Error)
	require.True(t, reviewed)
	require.Len(t, writer.requests, 1)
	id := result.Data["source_id"].(string)
	require.NotEqual(t, parentID, id)
	require.Equal(t, parentID, tool.mapped[id].ParentID)
	coordinates, ok := chartOriginalProjectionCoordinates(id, result.Data["provenance"].(string), parentID, []chartProjectionCoordinate{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}}, 0)
	require.True(t, ok)
	require.Equal(t, []chartProjectionCoordinate{{0, 0}, {0, 3}, {1, 3}, {2, 0}, {2, 3}}, coordinates)
	// The ordinary mapping entry point must produce the same exact projection.
	mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": parentID, "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{3}, "value_indexes": []int{0, 1, 2}, "series_column": 1, "unit_column": 2}})
	require.True(t, mapped.Success, mapped.Error)
	require.Equal(t, id, mapped.Data["source_id"])
	require.Equal(t, parentID, mapped.Data["parent_source_id"])
	// A dimension projection cannot outlive the actual query snapshot.
	steps[0].ToolCalls[0].Result.Data["rows"].([][]any)[0][3] = 91
	stale := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "x_column": 0, "y_columns": []int{1, 2}, "chart_type": "line", "x_type": "time"})
	require.False(t, stale.Success)
	require.Len(t, writer.requests, 1)
}

func TestChartSeriesRejectsMixedAxisUnitsBeforeReviewAndPersistence(t *testing.T) {
	call := chartLongFixture()
	call.Result.Data["rows"].([][]any)[0][2] = "亿元"
	call.Result.Data["rows"].([][]any)[1][2] = "万件"
	call.Result.Data["rows"].([][]any)[2][2] = "万件"
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	tool.reviewPlan = func(context.Context, chart.Table, chart.Spec) (*chartSemanticValidation, error) {
		t.Fatal("mixed explicit units must fail before semantic review")
		return nil, nil
	}
	sources, _ := chartSources(steps)
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": sources[0].ID, "x_column": 0, "y_columns": []int{3}, "series_column": 1, "unit_column": 2, "chart_type": "line", "x_type": "time"})
	require.False(t, result.Success)
	require.Contains(t, result.Error, "显式单位不同")
	require.Contains(t, result.Error, "拆图")
	require.Empty(t, writer.requests)
}

func TestChartSeriesBoundsDuplicatesAndTechnicalLabelsRemainGuarded(t *testing.T) {
	zero, one, two := 0, 1, 2
	base := chart.Table{Columns: []chart.Column{{Name: "x"}, {Name: "dimension"}, {Name: "measure"}}, Rows: [][]string{{"A", "label", "1"}}}
	require.NoError(t, base.Seal("real-source"))
	mapping := chartSourceMapping{Orientation: "columns", XIndex: &zero, YIndexes: []int{two}, SeriesColumn: &one, ValueIndexes: []int{0}}
	for _, test := range []struct{ name, role, kind string }{{"technical", "source_row_identifier", ""}, {"derived-technical", "", "source_metadata"}, {"constant", "", "constant_label"}} {
		t.Run(test.name, func(t *testing.T) {
			source := base
			source.Columns = append([]chart.Column(nil), base.Columns...)
			source.Columns[1].Role, source.Columns[1].DependencyKind = test.role, test.kind
			_, err := mapChartSource(source, mapping)
			require.ErrorContains(t, err, "技术来源ID")
		})
	}
	base.Rows = [][]string{{"A", "label", "1"}, {"A", "label", "2"}}
	mapping.ValueIndexes = []int{0, 1}
	_, err := mapChartSource(base, mapping)
	require.ErrorContains(t, err, "多行")
	base.Rows = nil
	mapping.ValueIndexes = nil
	for i := 0; i < 7; i++ {
		base.Rows = append(base.Rows, []string{"A", fmt.Sprintf("real-%d", i), "1"})
		mapping.ValueIndexes = append(mapping.ValueIndexes, i)
	}
	_, err = mapChartSource(base, mapping)
	require.ErrorContains(t, err, "超过6条")
}

func TestChartUnknownUnitsAllowRawMultiSeriesWithoutUnitClaims(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	sources, _ := chartSources(steps)
	source := sources[0]
	unknown := &structuredresult.Unit{Status: "unknown", EvidenceLevel: "none", BindingScope: "result_column", Reason: "Actual source has no unit declaration"}
	source.Columns = []chart.Column{{Name: "x"}, {Name: "first", UnitMetadata: unknown}, {Name: "second", Unit: "kg", UnitMetadata: &structuredresult.Unit{Status: "declared", Symbol: "kg"}}}
	source.Rows = [][]string{{"A", "1", "2"}, {"B", "3", "4"}}
	require.NoError(t, source.Seal("unknown-presentation"))
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	tool.mapped = map[string]mappedChartSource{source.ID: {Table: source, ParentID: sources[0].ID}}
	tool.reviewPlan = func(_ context.Context, actual chart.Table, _ chart.Spec) (*chartSemanticValidation, error) {
		require.Equal(t, "unknown", actual.Columns[1].UnitMetadata.Status)
		require.Equal(t, "kg", actual.Columns[2].UnitMetadata.Symbol)
		return nil, nil
	}
	spec := chart.Spec{Type: "line", X: 0, Y: []int{1, 2}, XType: "category"}
	presentation := chartUnitPresentation(source, spec)
	require.Empty(t, presentation.Columns[1].Unit)
	require.Empty(t, presentation.Columns[2].Unit, "an unknown Y precludes a shared asserted unit")
	require.Equal(t, "unknown", source.Columns[1].UnitMetadata.Status)
	result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": source.ID, "x_column": 0, "y_columns": []int{1, 2}, "chart_type": "line", "x_type": "category"})
	require.True(t, result.Success, result.Error)
	columns := result.Data["selected_columns"].([]chart.Column)
	require.Equal(t, "unknown", columns[1].UnitMetadata.Status)
}

func TestChartMixedUnitCannotBeErasedByTranspositionOrSeriesUnitSelection(t *testing.T) {
	zero, one, two, three := 0, 1, 2, 3
	mixed := &structuredresult.Unit{Status: "mixed", EvidenceLevel: "none", BindingScope: "result_column", Reason: "Known conflicting source units", Evidence: []map[string]any{{"source_id": "actual"}}}
	source := chart.Table{Columns: []chart.Column{{Name: "label"}, {Name: "first", UnitMetadata: mixed}, {Name: "second"}}, Rows: [][]string{{"dimension", "A", "B"}, {"measure", "2", "3"}}}
	require.NoError(t, source.Seal("real-mixed"))
	mapped, err := mapChartSource(source, chartSourceMapping{Orientation: "rows", XIndex: &zero, YIndexes: []int{1}, ValueIndexes: []int{1, 2}, LabelIndex: &zero})
	require.NoError(t, err)
	require.Equal(t, "mixed", mapped.Columns[1].UnitMetadata.Status)
	require.Equal(t, mixed.Evidence, mapped.Columns[1].UnitMetadata.Evidence)
	require.ErrorContains(t, validateChartUnits(mapped, chart.Spec{X: 0, Y: []int{1}}), "混合单位")
	source = chart.Table{Columns: []chart.Column{{Name: "x"}, {Name: "metric"}, {Name: "unit"}, {Name: "value", UnitMetadata: mixed}}, Rows: [][]string{{"A", "label", "kg", "2"}}}
	require.NoError(t, source.Seal("real-long-mixed"))
	mapped, err = mapChartSource(source, chartSourceMapping{Orientation: "columns", XIndex: &zero, YIndexes: []int{three}, ValueIndexes: []int{0}, SeriesColumn: &one, UnitColumn: &two})
	require.NoError(t, err)
	require.ErrorContains(t, validateChartUnits(mapped, chart.Spec{X: 0, Y: []int{1}}), "Known conflicting")
}

func TestRenderChartFailureCapStopsFurtherRendersAndPreservesRealReason(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	sources, _ := chartSources(steps)
	args := map[string]any{"operation": "render", "source_id": sources[0].ID, "x_column": 0, "y_columns": []int{99}, "chart_type": "line"}
	for i := 1; i <= maxChartRenderFailures; i++ {
		result := chartRun(t, tool, ctx, args)
		require.False(t, result.Success)
		if i < maxChartRenderFailures {
			require.Nil(t, result.Data["final_answer_required"])
		} else {
			require.Equal(t, true, result.Data["final_answer_required"])
			require.Equal(t, "chart_render_failure_limit", result.Data["terminal_reason"])
			require.Contains(t, result.Error, "真实数据完成文字回答")
			require.Contains(t, result.Data["last_render_failure"], "真实列")
		}
	}
	args["y_columns"] = []int{2}
	result := chartRun(t, tool, ctx, args)
	require.False(t, result.Success)
	require.Equal(t, maxChartRenderFailures, tool.renderFailures)
	require.Empty(t, writer.requests)
	require.True(t, chartRun(t, tool, ctx, map[string]any{"operation": "inspect"}).Success)
}

func TestRenderChartInvalidArgumentsCannotBypassFailureCap(t *testing.T) {
	ctx := chartContext(t)
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return nil }, &chartWriterFixture{}, "session", "message")
	for i := 1; i <= maxChartRenderFailures; i++ {
		result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "unrecognized_argument": true})
		require.False(t, result.Success)
		if i == maxChartRenderFailures {
			require.Equal(t, true, result.Data["final_answer_required"])
			require.Contains(t, result.Data["last_render_failure"], "合法对象")
		}
	}
	require.Equal(t, maxChartRenderFailures, tool.renderFailures)
}
