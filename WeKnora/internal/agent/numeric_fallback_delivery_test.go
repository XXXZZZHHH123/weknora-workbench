package agent

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNumericFailureFallbackDeliversEarlierGroundedCandidate(t *testing.T) {
	for _, fallback := range []string{numericFailureFallback, finalNumericFailureFallback} {
		engine := sourceConsistencyEngine(t, &mockChat{})
		state := sourceConsistencyState()
		// An earlier candidate passed numeric grounding and reached review; a
		// later rewrite failed grounding. The earlier one is delivered.
		engine.rememberUnverifiedCandidate("Grounded draft.", candidateDecision{Disposition: candidateRevise})
		engine.rememberUnverifiedCandidate("Later draft with 999.", candidateDecision{Disposition: candidateNumericFailure, Unsupported: []string{"999"}})
		engine.commitTerminalResult(t.Context(), state, "session", fallback, "", "failed", false)
		require.True(t, strings.HasPrefix(state.FinalAnswer, "Grounded draft."))
		require.Contains(t, state.FinalAnswer, "未能完成自动核验")
		require.NotContains(t, state.FinalAnswer, "999")

		engine = sourceConsistencyEngine(t, &mockChat{})
		state = sourceConsistencyState()
		engine.commitTerminalResult(t.Context(), state, "session", fallback, "", "failed", false)
		require.Equal(t, fallback, state.FinalAnswer, "without a grounded candidate the fallback remains")
	}
}

// Synthetic chart observation; values are fixture data, not acceptance facts.
func citedImageStep(geometry []any) []types.AgentStep {
	index := 0
	return []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "read", Name: "read_document", Result: &types.ToolResult{
		Success:           true,
		ImageDescriptions: []string{"Rightmost value of the red series is about 470 (between 300 and 600)."},
		Data: map[string]any{
			"image_evidence":          []any{map[string]any{"chunk_id": "chunk-a", "reference": "resource://img", "read_success": true, "returned_image_index": index}},
			"image_geometry_evidence": geometry,
		},
	}}}}}
}

func TestCitedImageObservationNumbersAreTraceable(t *testing.T) {
	cited := `约470 <kb doc="r.pptx" chunk_id="chunk-a" kb_id="kb" />`
	require.Contains(t, scopedCitedDocumentNumbers(cited, citedImageStep(nil)), 470.0)

	uncited := `约470 <kb doc="r.pptx" chunk_id="chunk-b" kb_id="kb" />`
	require.NotContains(t, scopedCitedDocumentNumbers(uncited, citedImageStep(nil)), 470.0, "an observation bound to another chunk lends no support")

	measured := citedImageStep([]any{map[string]any{"reference": "resource://img", "chunk_id": "chunk-a"}})
	require.NotContains(t, scopedCitedDocumentNumbers(cited, measured), 470.0, "verified geometry, not vision text, is the measurement for that image")

	failed := citedImageStep(nil)
	failed[0].ToolCalls[0].Result.Data["image_evidence"] = []any{map[string]any{"chunk_id": "chunk-a", "reference": "resource://img", "read_success": false, "returned_image_index": 0}}
	require.NotContains(t, scopedCitedDocumentNumbers(cited, failed), 470.0)
}
