package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredReviewPriorityKeepsComputedCoverageWithinExistingBudget(t *testing.T) {
	const endpointFirst = "2001-01"
	const endpointLast = "2001-04"
	const hiddenTail = "hidden-interpretation-tail"

	makeData := func(n int) map[string]any {
		segments := make([]any, 4)
		for i := range segments {
			segments[i] = map[string]any{
				"start_date":   fmt.Sprintf("2001-%02d", i+1),
				"end_date":     fmt.Sprintf("2001-%02d", i+1),
				"record_count": 1, "valid_count": 1, "has_value": true,
				"duplicate_date_boundary": false, "undated_boundary": false,
				"value_status": "valid",
			}
		}
		input := map[string]any{
			"actual_first_date": endpointFirst, "actual_last_date": endpointLast,
			"input_count": 4, "valid_count": 4,
			"valid_first_date": endpointFirst, "valid_last_date": endpointLast,
			"observation_coverage_scope": "loaded_synthetic_test_input",
			"example_cells":              []any{"A2", "A5"},
			"observation_coverage": map[string]any{
				"scope_start_date": endpointFirst, "scope_end_date": endpointLast,
				"scope_record_count": 4, "scope_valid_count": 4,
				"interpretation": strings.Repeat("Synthetic scope explanation. ", 180) + hiddenTail,
				"max_segments":   64, "segments": segments, "total_segment_count": 4, "truncated": false,
				"value_changes": map[string]any{
					"scope":                     map[string]any{"record_count": 4, "valid_value_count": 4, "eligible_record_count": 4, "eligible_start_date": endpointFirst, "eligible_end_date": endpointLast, "complete_filtered_input": true},
					"excluded_boundary_records": map[string]any{"null_date_count": 0, "missing_value_count": 0, "duplicate_date_record_count": 0},
					"interpretation":            strings.Repeat("Synthetic adjacency interpretation. ", 120),
					"first":                     map[string]any{"date": endpointFirst, "value": json.Number(fmt.Sprintf("%d.5", n+1))},
					"last":                      map[string]any{"date": endpointLast, "value": json.Number(fmt.Sprintf("%d.5", n+4))},
					"minimum":                   map[string]any{"date": "2001-02", "value": json.Number(fmt.Sprintf("%d.5", n))},
					"maximum":                   map[string]any{"date": "2001-03", "value": json.Number(fmt.Sprintf("%d.5", n+5))},
					"adjacent_comparison_count": 3,
					"increase_count":            2, "decrease_count": 1, "unchanged_count": 0,
				},
			},
		}
		return map[string]any{
			"kind": "structured_analysis", "status": "ok",
			"dataset_id": fmt.Sprintf("synthetic-dataset-%d", n), "result_id": fmt.Sprintf("synthetic-result-%d", n),
			"columns":     []any{"period", "amount"},
			"rows":        []any{[]any{endpointFirst, json.Number("1.5")}},
			"inputs":      map[string]any{"synthetic-input": input},
			"input_count": 1, "input_complete": true,
			"returned_row_count": 1, "output_truncated": false,
			"requested_range": map[string]any{"start_date": endpointFirst, "end_date": endpointLast},
			"notice":          strings.Repeat("Synthetic top-level note. ", 400),
		}
	}

	state := &types.AgentState{RoundSteps: []types.AgentStep{{}}}
	messages := make([]chat.Message, 0, 2)
	for i := 0; i < 2; i++ {
		callID := fmt.Sprintf("synthetic-analysis-%d", i)
		call, message := structuredQueryFixture(t, "structured_analyze_table", callID,
			map[string]any{"operation": "analyze_table"}, makeData(i), true)
		state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, call)
		messages = append(messages, message)
	}

	view := structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget)
	require.NotNil(t, view)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 12*1024, "the metadata priority adjustment must stay within the existing companion budget")
	require.False(t, view["projection_complete"].(bool), "preserving selected statistics must not claim full metadata visibility")

	entries, ok := view["entries"].([]structuredQueryRowCompanionEntry)
	require.True(t, ok)
	require.Len(t, entries, 2)
	reader := newReviewEvidenceReader(messages)
	input := map[string]any{"structured_query_row_companion": view}
	for i, entry := range entries {
		require.False(t, entry.MetadataProjectionComplete)
		require.True(t, entry.ProjectionComplete, "all rows are retained independently of partial metadata")
		require.Contains(t, entry.MetadataOmittedPointers, "/data/notice")

		inputs := entry.Metadata["inputs"].(map[string]any)
		syntheticInput := inputs["synthetic-input"].(map[string]any)
		coverage := syntheticInput["observation_coverage"].(map[string]any)
		require.Equal(t, endpointFirst, coverage["scope_start_date"])
		require.Equal(t, endpointLast, coverage["scope_end_date"])
		require.Equal(t, json.Number("4"), coverage["scope_record_count"])
		require.Equal(t, json.Number("4"), coverage["scope_valid_count"])

		changes := coverage["value_changes"].(map[string]any)
		scope := changes["scope"].(map[string]any)
		require.Equal(t, json.Number("4"), scope["record_count"])
		require.Equal(t, endpointFirst, scope["eligible_start_date"])
		require.Equal(t, endpointLast, scope["eligible_end_date"])
		require.Equal(t, true, scope["complete_filtered_input"])
		require.Equal(t, json.Number("3"), changes["adjacent_comparison_count"])
		require.Equal(t, json.Number("2"), changes["increase_count"])
		require.Equal(t, json.Number("1"), changes["decrease_count"])
		require.Equal(t, json.Number("0"), changes["unchanged_count"])
		for _, endpoint := range []struct {
			name, date string
			value      string
		}{{"first", endpointFirst, fmt.Sprintf("%d.5", i+1)}, {"last", endpointLast, fmt.Sprintf("%d.5", i+4)}, {"minimum", "2001-02", fmt.Sprintf("%d.5", i)}, {"maximum", "2001-03", fmt.Sprintf("%d.5", i+5)}} {
			point := changes[endpoint.name].(map[string]any)
			require.Equal(t, endpoint.date, point["date"])
			require.Equal(t, json.Number(endpoint.value), point["value"])
			witness := reviewSourceWitness{
				EvidenceID:  entry.EvidenceID,
				JSONPointer: "/data/inputs/synthetic-input/observation_coverage/value_changes/" + endpoint.name + "/value",
				Quote:       endpoint.value, Kind: "source",
			}
			require.NoError(t, reviewWitnessVisible(input, reader, witness), "retained exact summary values remain verifiable against the immutable source")
		}

		hidden := reviewSourceWitness{
			EvidenceID:  entry.EvidenceID,
			JSONPointer: "/data/inputs/synthetic-input/observation_coverage/interpretation",
			Quote:       hiddenTail, Kind: "source",
		}
		require.Error(t, reviewWitnessVisible(input, reader, hidden), "the unprojected interpretation suffix must not be treated as visible evidence")
	}
}
