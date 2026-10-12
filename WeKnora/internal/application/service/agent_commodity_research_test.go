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
	require.NoError(t, svc.registerTools(t.Context(), registry, &types.AgentConfig{
		AllowedTools: []string{tools.ToolCommodityResearch},
	}, nil, nil, "research-session"))
	require.Contains(t, registry.ListTools(), tools.ToolCommodityResearch)
	for _, name := range tools.DefaultAllowedTools() {
		require.NotEqual(t, tools.ToolCommodityResearch, name)
	}
}
