package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSynthesisCriticGetsWiderProjectionDirectKeepsCompact(t *testing.T) {
	for _, tc := range []struct {
		depth string
		scale int
	}{{"synthesis", sourceReviewSynthesisScale}, {"direct", 1}, {"", 1}} {
		engine := sourceConsistencyEngine(t, &mockChat{})
		engine.config.SourceReviewReasoningEffort = "auto"
		engine.taskPlan = &taskEvidencePlan{ReasoningDepth: tc.depth}
		snapshot := engine.buildSourceReviewSnapshot(t.Context(), "A candidate.", sourceConsistencyState(), nil)
		scope := snapshot.input["source_review_packet_scope"].(map[string]any)
		require.Equal(t, sourceReviewSharedProjectionBudget*tc.scale, scope["budget_target_bytes"], tc.depth)
	}
	require.Greater(t, sourceReviewCitationBudgetForBaseScaled(map[string]any{}, sourceReviewSynthesisScale), sourceReviewCitationBudgetForBase(map[string]any{}))
}
