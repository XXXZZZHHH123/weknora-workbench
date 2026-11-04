package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChartReviewReceivesOnlyMechanicallyEligibleAxisTypes(t *testing.T) {
	for _, test := range []struct {
		name, candidate, required string
		values                    []string
		eligible                  []string
	}{
		{"number-distance", "category", "numeric", []string{"2", "8", "11"}, []string{"category", "numeric"}},
		{"numeric-category-comparison", "numeric", "category", []string{"2032", "2037", "2041"}, []string{"category", "numeric"}},
		{"true-date-interval", "category", "time", []string{"2032-02-03", "2032-02-06", "2032-03-01"}, []string{"category", "time"}},
		{"actual-categories", "category", "category", []string{"north", "south", "east"}, []string{"category"}},
		{"mixed-values-still-allow-category", "category", "category", []string{"2", "unavailable", "11"}, []string{"category"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, test.required, "The actual dimension and source readings satisfy the full task.", "")}
			e := newTestEngine(t, model)
			table := chartAxisReviewTable(t)
			for i, value := range test.values {
				table.Rows[i][0] = value
			}
			require.NoError(t, table.Seal("synthetic-axis-eligibility"))
			spec := chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: test.candidate}
			verdict, err := e.chartPlanReviewer("Use the dimensions appropriate to the complete source-bound task.", &types.AgentState{})(chartContext(t), table, spec)
			if test.required != test.candidate {
				require.ErrorContains(t, err, "独立核验要求"+test.required)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.eligible, verdict.EligibleXTypes)
			}
			var input struct {
				Eligible []string       `json:"eligible_x_types"`
				Spec     map[string]any `json:"chart_spec"`
				Rows     [][]string     `json:"selected_rows"`
			}
			require.NoError(t, json.Unmarshal([]byte(model.seen[0][1].Content), &input))
			require.Equal(t, test.eligible, input.Eligible)
			require.NotContains(t, input.Spec, "x_type")
			require.Equal(t, table.Rows, input.Rows)
			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(model.options[0].Format, &schema))
			require.Equal(t, test.eligible, schema.Properties["required_x_type"].Enum)
			require.Contains(t, model.seen[0][0].Content, "所有合法类型均无法满足任务，accepted=false")
		})
	}
}

func TestChartIneligibleReviewAxisIsProtocolFailureAndDoesNotChangeSourceOrSave(t *testing.T) {
	ctx := chartContext(t)
	call := chartRecoverySQLFixture()
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	tables, _ := chartSources(steps)
	mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": tables[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1}})
	require.True(t, mapped.Success, mapped.Error)
	id := mapped.Data["source_id"].(string)
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "time", "The task requires distance between actual observations.", "")}
	e := newTestEngine(t, model)
	tool.reviewPlan = e.chartPlanReviewer("Keep the actual source positions and readings.", &types.AgentState{})
	args := map[string]any{"operation": "render", "source_id": id, "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": "category"}
	failed := chartRun(t, tool, ctx, args)
	require.False(t, failed.Success)
	require.Contains(t, failed.Error, "chart_review_axis_type_not_eligible")
	require.Contains(t, failed.Error, "全部绘图参数不变重试")
	require.NotContains(t, failed.Error, "请修正真实绘图参数")
	diagnostic := chartRecoveryModelDiagnostic(t, modelcontext.NewRegistry(true), failed)
	require.Equal(t, id, diagnostic["source_id"])
	require.Empty(t, writer.requests)
	require.Empty(t, tool.receipts)
	require.Equal(t, mapped.Data["rows"], tool.mapped[id].Table.Rows)
	// A retry retains the same selected source and candidate encoding. The
	// independent model can accept a real category comparison without the
	// program promoting number-like labels to numeric business semantics.
	model.response = chartAxisReviewResponse(t, "category", "The source labels are discrete observation categories for this request.", "")
	recovered := chartRun(t, tool, ctx, args)
	require.True(t, recovered.Success, recovered.Error)
	require.Len(t, writer.requests, 1)
	require.Equal(t, model.seen[0], model.seen[1], "an invalid reviewer type must not change the source or independent review input")
	require.Equal(t, []string{"category", "numeric"}, recovered.Data["semantic_validation"].(*chartSemanticValidation).EligibleXTypes)
}

func TestEligibleAxesCannotAuthorizeSemanticallyWrongData(t *testing.T) {
	model := &chartAxisReviewModel{response: &types.ChatResponse{Content: `{"accepted":false,"complete_delivery":false,"required_x_type":"category","assessment":"The selected source dimension does not answer the requested comparison.","feedback":"Choose the actual requested dimension from the authorized source."}`, FinishReason: "stop"}}
	e := newTestEngine(t, model)
	verdict, err := e.chartPlanReviewer("Compare the requested original source dimension.", &types.AgentState{})(chartContext(t), chartAxisReviewTable(t), chart.Spec{Type: "bar", X: 0, Y: []int{1}, XType: "category"})
	require.Nil(t, verdict)
	require.ErrorContains(t, err, "Choose the actual requested dimension")
}

func TestChartSemanticRejectionPreservesSourceFeedbackAcrossEligibleAxisMismatch(t *testing.T) {
	for _, candidate := range []string{"category", "numeric"} {
		t.Run(candidate, func(t *testing.T) {
			ctx := chartContext(t)
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{chartRecoverySQLFixture()}}}
			writer := &chartWriterFixture{}
			tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
			tables, _ := chartSources(steps)
			mapped := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": tables[0].ID, "mapping": map[string]any{"orientation": "rows", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{2, 3, 4}, "label_index": 1}})
			require.True(t, mapped.Success, mapped.Error)
			required := "numeric"
			if candidate == "numeric" {
				required = "category"
			}
			feedback := "Select the requested source measure; changing the axis cannot repair this evidence mismatch."
			payload, err := json.Marshal(map[string]any{"accepted": false, "complete_delivery": false, "required_x_type": required, "assessment": "Both encodings are representable, but the selected measure does not satisfy the task.", "feedback": feedback})
			require.NoError(t, err)
			model := &chartAxisReviewModel{response: &types.ChatResponse{Content: string(payload), FinishReason: "stop"}}
			e := newTestEngine(t, model)
			tool.reviewPlan = e.chartPlanReviewer("Chart the measure requested from the authorized original source.", &types.AgentState{})
			id := mapped.Data["source_id"].(string)
			result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": candidate})
			require.False(t, result.Success)
			require.Contains(t, result.Error, feedback)
			require.NotContains(t, result.Error, "轴类型与完整任务不符")
			require.Equal(t, mapped.Data["rows"], tool.mapped[id].Table.Rows)
			require.Empty(t, writer.requests)
			require.Empty(t, tool.receipts)
		})
	}
}

func TestChartReviewEligibilityUsesSelectedAxisInsteadOfOtherSourceColumns(t *testing.T) {
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "numeric", "The selected positions have real numeric distances; the other source column is unrelated to this axis.", "")}
	e := newTestEngine(t, model)
	table := chart.Table{Columns: []chart.Column{{Name: "Recorded date"}, {Name: "Position"}, {Name: "Reading", Unit: "kg"}}, Rows: [][]string{{"2032-02-03", "4", "2"}, {"2032-03-01", "9", "5"}}, Provenance: "isolated actual source"}
	require.NoError(t, table.Seal("synthetic-selected-axis"))
	verdict, err := e.chartPlanReviewer("Compare readings over their actual measured positions.", &types.AgentState{})(chartContext(t), table, chart.Spec{Type: "line", X: 1, Y: []int{2}, XType: "numeric"})
	require.NoError(t, err)
	require.Equal(t, []string{"category", "numeric"}, verdict.EligibleXTypes)
	var input struct {
		Rows [][]string `json:"selected_rows"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.seen[0][1].Content), &input))
	require.Equal(t, [][]string{{"4", "2"}, {"9", "5"}}, input.Rows)
}
