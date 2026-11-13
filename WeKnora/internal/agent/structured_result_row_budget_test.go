package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStructuredResultReviewUsesRemainingInputBudgetForAllRows(t *testing.T) {
	e, state, model := lightStructuredFixture(t, lightReviewReply("keep", ""))
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	for i, row := range data["rows"].([]any) {
		row.(map[string]any)["provenance_detail"] = strings.Repeat(string(rune('a'+i)), 5000)
	}
	rehashLightFixture(t, data)
	selected := currentStructuredResultReviewState(state)
	initial := structuredQueryRowReviewEvidence(selected, e.sourceConsistencyMessages(selected, nil), structuredQueryRowReviewBudget)
	require.False(t, structuredResultReviewRowsComplete(initial))
	before := reviewDigest(state.RoundSteps)
	decision := e.checkCandidate(t.Context(), "Report the final observation.", "The final observation is 12.500 kg.", "", state, nil, false)
	require.Equal(t, "kept", decision.StructuredResultReviewStatus)
	require.Len(t, model.calls, 1)
	input := model.calls[0][1].Content
	require.LessOrEqual(t, len(input), structuredResultReviewInputBytes)
	var view map[string]any
	require.NoError(t, json.Unmarshal([]byte(input), &view))
	projection := view["actual_structured_results"].(map[string]any)
	require.Greater(t, projection["budget_bytes"].(float64), float64(structuredQueryRowReviewBudget))
	for _, raw := range projection["entries"].([]any) {
		entry := raw.(map[string]any)
		require.Equal(t, true, entry["projection_complete"])
		require.Equal(t, entry["source_row_count"], entry["retained_row_count"])
	}
	for _, row := range data["rows"].([]any) {
		require.Contains(t, input, row.(map[string]any)["provenance_detail"])
	}
	require.Equal(t, before, reviewDigest(state.RoundSteps), "no source data or result contract was rewritten")
}

func TestStructuredResultReviewNeverBorrowsBeyondWholeInputLimit(t *testing.T) {
	e, state, model := lightStructuredFixture(t, lightReviewReply("keep", ""))
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	for i, row := range data["rows"].([]any) {
		row.(map[string]any)["provenance_detail"] = strings.Repeat(string(rune('a'+i)), 15000)
	}
	rehashLightFixture(t, data)
	decision := e.checkCandidate(t.Context(), "Report the final observation.", "The final observation is 12.500 kg.", "", state, nil, false)
	require.Equal(t, "not_checked_row_omission", decision.StructuredResultReviewStatus)
	require.Empty(t, model.calls, "an incomplete row projection never reaches the editor")
}
