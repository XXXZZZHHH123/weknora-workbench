package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchRuntimeEnablesAdditionsSelectionForSmartAndQuick(t *testing.T) {
	for _, id := range []string{types.BuiltinQuickAnswerID, types.BuiltinSmartReasoningID, "custom-agent"} {
		t.Run(id, func(t *testing.T) {
			workbench := id != "custom-agent"
			svc := &sessionService{cfg: &config.Config{}, webSearchProviderRepo: &sharedAgentWebSearchRepo{}}
			req := &types.QARequest{Session: &types.Session{ID: "s", TenantID: 1}, CustomAgent: &types.CustomAgent{ID: id, TenantID: 1, WorkbenchDefault: workbench, Config: types.CustomAgentConfig{MaxIterations: 5}}}
			cfg, err := svc.buildAgentConfig(t.Context(), req, &types.Tenant{ID: 1}, 1)
			require.NoError(t, err)
			require.Equal(t, workbench, cfg.CandidateAdditionsEnabled)
		})
	}
}
