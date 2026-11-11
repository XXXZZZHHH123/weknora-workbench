package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestAgentConfigCarriesAnswerReviewModeWithoutDefaultingOrMutatingSource(t *testing.T) {
	for _, mode := range []string{"", "standard", "strict"} {
		for _, workbench := range []bool{false, true} {
			svc := &sessionService{cfg: &config.Config{}, webSearchProviderRepo: &sharedAgentWebSearchRepo{}}
			original := types.CustomAgentConfig{MaxIterations: 5, ModelID: "selected-model", AnswerReviewMode: mode,
				KBSelectionMode: "selected", KnowledgeBases: []string{"selected-library"}, RetrieveKBOnlyWhenMentioned: true, AllowedTools: []string{"read_document"}}
			req := &types.QARequest{Session: &types.Session{ID: "session", TenantID: 1},
				CustomAgent: &types.CustomAgent{TenantID: 1, WorkbenchDefault: workbench, Config: original}}
			got, err := svc.buildAgentConfig(t.Context(), req, &types.Tenant{ID: 1}, 1)
			require.NoError(t, err)
			require.Equal(t, mode, got.AnswerReviewMode, "only the workbench adapter chooses the empty-field default")
			require.Equal(t, workbench && mode == "standard", got.StructuredResultReviewEnabled,
				"the workbench explicitly enables only the bounded standard structured-result check")
			require.Equal(t, original, req.CustomAgent.Config)
		}
	}
}
