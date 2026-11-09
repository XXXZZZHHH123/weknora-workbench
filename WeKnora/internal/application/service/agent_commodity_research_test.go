package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRegisterToolsCommodityResearchIsOptInAndSurvivesNoKnowledgeBase(t *testing.T) {
	svc := newToolSurfaceService(&types.KnowledgeBase{ID: "kb"})
	registry := tools.NewToolRegistry()
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	ctx = types.WithResearchWorkbenchScope(ctx, types.ResearchWorkbenchScope{
		ID: "00000000-0000-4000-8000-000000000007", TenantID: 7, UserID: "reader", ScopeVersion: 2, SourcesRevision: 1,
	})
	ctx = types.WithResearchAuthorization(ctx, "Bearer synthetic-authorization")
	require.NoError(t, svc.registerTools(ctx, registry, &types.AgentConfig{
		AllowedTools: []string{tools.ToolCommodityResearch},
	}, nil, nil, "research-session"))
	require.Contains(t, registry.ListTools(), tools.ToolCommodityResearch)
	for _, name := range tools.DefaultAllowedTools() {
		require.NotEqual(t, tools.ToolCommodityResearch, name)
	}
}
