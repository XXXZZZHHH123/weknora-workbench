package session

import (
	"context"
	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWorkbenchDefaultEntersToolLoopWithoutEnablingExternalMCP(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_URL", "http://research:8765")
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	original := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, TenantID: 7, Config: types.CustomAgentConfig{AgentMode: types.AgentModeQuickAnswer, ModelID: "chosen-model", KBSelectionMode: "none", MCPSelectionMode: "all", SkillsSelectionMode: "all"}}
	active := workbenchDefaultAgent(original, 7)
	require.True(t, active.IsAgentMode())
	require.True(t, active.WorkbenchDefault)
	require.Equal(t, "chosen-model", active.Config.ModelID)
	require.Equal(t, "all", active.Config.KBSelectionMode)
	require.Equal(t, "none", active.Config.MCPSelectionMode)
	require.Equal(t, "none", active.Config.SkillsSelectionMode)
	require.Contains(t, active.Config.AllowedTools, tools.ToolStructuredData)
	require.Contains(t, active.Config.AllowedTools, tools.ToolCommodityResearch)
	require.Contains(t, active.Config.AllowedTools, tools.ToolSearchKnowledge)
	require.NotContains(t, active.Config.AllowedTools, tools.ToolCallMCPTool)
	require.Equal(t, types.AgentModeQuickAnswer, original.Config.AgentMode)
	require.Equal(t, "none", original.Config.KBSelectionMode)
}
func TestWorkbenchDefaultPreservesCustomQuickAnswerAndUnconfiguredInstall(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_URL", "")
	t.Setenv("STRUCTURED_DATA_URL", "")
	original := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true}
	require.Same(t, original, workbenchDefaultAgent(original, 7))
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	custom := &types.CustomAgent{ID: "custom-quick", Config: types.CustomAgentConfig{AgentMode: types.AgentModeQuickAnswer}}
	require.Same(t, custom, workbenchDefaultAgent(custom, 7))
	require.True(t, workbenchDefaultAgent(nil, 7).IsAgentMode())
}

func TestWorkbenchDefaultExplicitRequestAndSharedAgentRemainNarrow(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	original := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, TenantID: 7, Config: types.CustomAgentConfig{KnowledgeBases: []string{"old"}, IntentPrompts: map[string]string{"lookup": "old prompt"}}}
	h := &Handler{}
	rc := &qaRequestContext{ctx: context.Background(), session: &types.Session{TenantID: 7}, customAgent: original, knowledgeBaseIDs: []string{"selected-kb"}, knowledgeIDs: []string{"selected-doc"}}
	h.prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: types.BuiltinQuickAnswerID})
	require.Equal(t, "selected", rc.customAgent.Config.KBSelectionMode)
	require.Equal(t, []string{"selected-kb"}, rc.customAgent.Config.KnowledgeBases)
	rc.customAgent.Config.IntentPrompts["lookup"] = "runtime change"
	require.Equal(t, "old prompt", original.Config.IntentPrompts["lookup"])
	require.Equal(t, []string{"old"}, original.Config.KnowledgeBases)
	rc.customAgent = original
	h.prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: types.BuiltinQuickAnswerID, AgentSourceTenantID: 99})
	require.Same(t, original, rc.customAgent)
}
