package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChartReviewSeesDimensionsHiddenBySingleTotalLegend(t *testing.T) {
	for _, test := range []struct {
		name, dimension, firstUnit, secondUnit, issue string
	}{
		{"different-measures", "metric", "亿元", "万件", "Different measures and source units have been combined on one Y axis; split the real measures and units."},
		{"missing-categories", "segment", "kg", "kg", "The requested categories were lost; the single total legend cannot identify the actual source dimensions."},
	} {
		t.Run(test.name, func(t *testing.T) {
			table := chart.Table{
				Columns: []chart.Column{{Name: "date"}, {Name: test.dimension}, {Name: "unit"}, {Name: "total", UnitMetadata: &structuredresult.Unit{Status: "unknown", Reason: "No column-wide unit was established."}}},
				Rows: [][]string{
					{"2024-01-01", "observed label A", test.firstUnit, "10"},
					{"2024-01-01", "observed label B", test.secondUnit, "15"},
					{"2024-02-01", "observed label A", test.firstUnit, "12"},
					{"2024-02-01", "observed label B", test.secondUnit, "18"},
				},
				Provenance: "Isolated actual long-form observation fixture; no dimension has been aggregated.",
			}
			require.NoError(t, table.Seal("isolated-long-form"))
			model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "time", "Checked actual source rows and labels against the requested comparison.", test.issue)}
			e := newTestEngine(t, model)
			verdict, err := e.chartPlanReviewer("Compare the actual labeled observations over time and explain the findings.", &types.AgentState{})(chartContext(t), table, chart.Spec{Type: "line", X: 0, Y: []int{3}, XType: "time"})
			require.Nil(t, verdict)
			require.ErrorContains(t, err, test.issue)
			require.Len(t, model.seen, 1)
			var input struct {
				SelectedColumns []chart.Column    `json:"selected_columns"`
				OtherColumns    []chart.Column    `json:"other_source_columns"`
				OtherRows       [][]string        `json:"other_source_rows"`
				UnitContract    map[string]string `json:"unit_state_contract"`
			}
			require.NoError(t, json.Unmarshal([]byte(model.seen[0][1].Content), &input))
			require.Equal(t, "total", input.SelectedColumns[1].Name)
			require.Equal(t, "unknown", input.SelectedColumns[1].UnitMetadata.Status)
			require.Equal(t, test.dimension, input.OtherColumns[0].Name)
			require.Equal(t, "unit", input.OtherColumns[1].Name)
			require.Equal(t, [][]string{{"observed label A", test.firstUnit}, {"observed label B", test.secondUnit}, {"observed label A", test.firstUnit}, {"observed label B", test.secondUnit}}, input.OtherRows)
			require.Contains(t, input.UnitContract["mixed"], "conflicting units")
			require.Contains(t, input.UnitContract["unknown"], "has not established a unit")
			require.Contains(t, model.seen[0][0].Content, "这类问题写入chart_issues")
		})
	}
}

func TestChartReviewAllowsSourceBackedTotalLegend(t *testing.T) {
	// No literal legend blacklist: the independent model evaluates the complete
	// task and real source shape, so a legitimate aggregate remains available.
	table := chart.Table{Columns: []chart.Column{{Name: "date"}, {Name: "total", Unit: "kg"}}, Rows: [][]string{{"2024-01-01", "10"}, {"2024-02-01", "12"}}, Provenance: "Isolated executed source aggregate, declared kg."}
	require.NoError(t, table.Seal("isolated-aggregate"))
	model := &chartAxisReviewModel{response: chartAxisReviewResponse(t, "time", "The requested aggregate matches the actual executed aggregate and unit.", "")}
	e := newTestEngine(t, model)
	verdict, err := e.chartPlanReviewer("Plot the actual aggregate observations.", &types.AgentState{})(chartContext(t), table, chart.Spec{Type: "line", X: 0, Y: []int{1}, XType: "time"})
	require.NoError(t, err)
	require.True(t, verdict.CompleteDelivery)
}
