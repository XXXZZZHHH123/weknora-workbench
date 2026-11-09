package session

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchAnswerReviewDefaultIsRuntimeOnlyAndPreservesExplicitMode(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://fixture-structured")
	for _, mode := range []string{"", "standard", "strict"} {
		for _, id := range []string{types.BuiltinQuickAnswerID, types.BuiltinSmartReasoningID, "managed-fixture"} {
			original := &types.CustomAgent{ID: id, IsBuiltin: id != "managed-fixture", TenantID: 7,
				Description: "[managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421]", Config: types.CustomAgentConfig{
					AnswerReviewMode: mode, ModelID: "selected-model", ReasoningEffort: "high"}}
			active := workbenchDefaultAgent(original, 7)
			want := mode
			if want == "" {
				want = "standard"
			}
			require.Equal(t, want, active.Config.AnswerReviewMode)
			require.Equal(t, mode, original.Config.AnswerReviewMode, "the saved agent remains unchanged")
			require.Equal(t, original.Config.ModelID, active.Config.ModelID)
			require.Equal(t, original.Config.ReasoningEffort, active.Config.ReasoningEffort)
		}
	}
	custom := &types.CustomAgent{ID: "unrelated-custom", TenantID: 7}
	require.Same(t, custom, workbenchDefaultAgent(custom, 7))
	require.Empty(t, custom.Config.AnswerReviewMode, "ordinary legacy agents retain core strict compatibility")
	t.Setenv("STRUCTURED_DATA_URL", "")
	t.Setenv("COMMODITY_RESEARCH_URL", "")
	unconfigured := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, TenantID: 7}
	require.Same(t, unconfigured, workbenchDefaultAgent(unconfigured, 7))
	require.Empty(t, unconfigured.Config.AnswerReviewMode, "an installation without workbench services keeps its compatibility default")
}
