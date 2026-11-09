package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRegisterCommodityResearchRequiresRealCallerScopeAndPreservesExplicitToolChoice(t *testing.T) {
	for _, mode := range []string{"ordinary", "scoped", "v2", "wrong caller", "missing authorization", "explicitly disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
			if mode != "ordinary" {
				scope := types.ResearchWorkbenchScope{ID: "00000000-0000-4000-8000-000000000007", TenantID: 7, UserID: "reader", KnowledgeBaseID: "library", KnowledgeBaseTenantID: 13}
				if mode == "wrong caller" {
					scope.UserID = "another-reader"
				}
				if mode == "v2" {
					scope.ScopeVersion, scope.SourcesRevision = 2, 3
					scope.KnowledgeBaseID = ""
				}
				ctx = types.WithResearchWorkbenchScope(ctx, scope)
				if mode != "missing authorization" {
					ctx = types.WithResearchAuthorization(ctx, "Bearer synthetic-authorization")
				}
			}
			allowed := []string{tools.ToolCommodityResearch}
			if mode == "explicitly disabled" {
				allowed = []string{}
			}
			cfg := &types.AgentConfig{AllowedTools: allowed}
			registry := tools.NewToolRegistry()
			svc := newToolSurfaceService(&types.KnowledgeBase{ID: "library"})
			require.NoError(t, svc.registerTools(ctx, registry, cfg, nil, nil, "session"))
			_, err := registry.GetTool(tools.ToolCommodityResearch)
			if mode == "scoped" || mode == "v2" {
				require.NoError(t, err, "a valid research session must retain its selected capability")
			} else {
				require.Error(t, err, "a saved tool name cannot grant a missing source scope or override explicit disablement")
			}
			require.Equal(t, allowed, cfg.AllowedTools, "runtime prerequisite filtering must not rewrite saved configuration")
		})
	}
}
