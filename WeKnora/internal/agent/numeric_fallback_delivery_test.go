package agent

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNumericFailureFallbackDeliversEarlierGroundedCandidate(t *testing.T) {
	for _, fallback := range []string{numericFailureFallback, finalNumericFailureFallback, incompleteAnswerFallback,
		answerReviewUnavailableFallback, sourceProgressStalledFallback, stalledAnswerFallback, truncatedAnswerFallback} {
		engine := sourceConsistencyEngine(t, &mockChat{})
		state := sourceConsistencyState()
		// An earlier candidate passed numeric grounding and reached review; a
		// later rewrite failed grounding. The earlier one is delivered.
		rememberUnverifiedCitationFixture(t, engine, state, "Grounded draft.")
		engine.rememberUnverifiedCandidate("Later draft with 999.", candidateDecision{Disposition: candidateNumericFailure, Unsupported: []string{"999"}})
		engine.commitTerminalResult(t.Context(), state, "session", fallback, "", "failed", false)
		require.True(t, strings.HasPrefix(state.FinalAnswer, "Grounded draft."))
		require.Contains(t, state.FinalAnswer, "未能完成自动核验")
		require.NotContains(t, state.FinalAnswer, "999")

		engine = sourceConsistencyEngine(t, &mockChat{})
		state = sourceConsistencyState()
		engine.commitTerminalResult(t.Context(), state, "session", fallback, "", "failed", false)
		require.False(t, terminalCandidateFallback(state.FinalAnswer), "a fixed runtime message is not the published result")
		require.Contains(t, state.FinalAnswer, "本轮没有取得可用于回答的已核实内容")
		require.NotContains(t, state.FinalAnswer, "999")
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
