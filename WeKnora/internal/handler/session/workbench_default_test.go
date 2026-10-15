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
	require.Equal(t, "all", active.Config.SkillsSelectionMode)
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

func TestWorkbenchThreeEntriesShareScopedDataToolsWhileKeepingModelSettings(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	t.Setenv("COMMODITY_RESEARCH_URL", "http://research:8765")
	for _, id := range []string{types.BuiltinQuickAnswerID, types.BuiltinSmartReasoningID, "managed-renamed-agent"} {
		t.Run(id, func(t *testing.T) {
			thinking := true
			original := &types.CustomAgent{
				ID: id, Name: "随意改名", IsBuiltin: id != "managed-renamed-agent", TenantID: 7, CreatedBy: "real-user",
				Description: "表格服务 [managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421]",
				Config: types.CustomAgentConfig{ModelID: "chosen-model", Temperature: 0.25, Thinking: &thinking, ReasoningEffort: "high", ThinkingBudgetTokens: 4096, MaxCompletionTokens: 8192, MaxIterations: 40,
					AllowedTools: []string{"thinking", "web_search"}, MCPSelectionMode: "selected", MCPServices: []string{"managed", "unrelated"}, SkillsSelectionMode: "selected", SelectedSkills: []string{"chosen-skill"}, SandboxConfigID: "chosen-sandbox",
					KBSelectionMode: "none", KnowledgeBases: []string{"old-config"}, IntentPrompts: map[string]string{"lookup": "saved"}},
			}
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "real-user", Role: types.TenantRoleViewer})
			rc := &qaRequestContext{ctx: ctx, session: &types.Session{TenantID: 7}, customAgent: original}
			(&Handler{}).prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: id})
			active := rc.customAgent
			require.True(t, active.WorkbenchDefault)
			require.True(t, active.IsAgentMode())
			require.Equal(t, workbenchDefaultPrompt, active.Config.SystemPrompt)
			for _, tool := range []string{tools.ToolStructuredData, tools.ToolCommodityResearch, tools.ToolSearchKnowledge, tools.ToolReadDocument, tools.ToolListDocuments} {
				require.Contains(t, active.Config.AllowedTools, tool)
			}
			require.Equal(t, "chosen-model", active.Config.ModelID)
			require.Equal(t, 0.25, active.Config.Temperature)
			require.Equal(t, "high", active.Config.ReasoningEffort)
			require.Equal(t, 4096, active.Config.ThinkingBudgetTokens)
			require.Equal(t, 8192, active.Config.MaxCompletionTokens)
			require.Equal(t, 40, active.Config.MaxIterations)
			require.Equal(t, "all", active.Config.KBSelectionMode)
			require.Equal(t, "chosen-sandbox", active.Config.SandboxConfigID)
			require.Equal(t, "selected", active.Config.SkillsSelectionMode)
			require.Equal(t, []string{"chosen-skill"}, active.Config.SelectedSkills)
			if id != types.BuiltinQuickAnswerID {
				require.Equal(t, []string{"managed", "unrelated"}, active.Config.MCPServices)
				require.Equal(t, "selected", active.Config.MCPSelectionMode)
				require.Equal(t, "chosen-sandbox", active.Config.SandboxConfigID)
				require.Equal(t, []string{"chosen-skill"}, active.Config.SelectedSkills)
				require.Contains(t, active.Config.AllowedTools, "web_search")
			}
			*active.Config.Thinking = false
			active.Config.IntentPrompts["lookup"] = "changed"
			active.Config.KnowledgeBases[0] = "changed"
			if len(active.Config.MCPServices) > 0 {
				active.Config.MCPServices[0] = "changed"
				active.Config.SelectedSkills[0] = "changed"
			}
			require.True(t, *original.Config.Thinking)
			require.Equal(t, "saved", original.Config.IntentPrompts["lookup"])
			require.Equal(t, []string{"old-config"}, original.Config.KnowledgeBases)
			require.Equal(t, []string{"managed", "unrelated"}, original.Config.MCPServices)
			require.Equal(t, []string{"chosen-skill"}, original.Config.SelectedSkills)
		})
	}
}

func TestWorkbenchNeverAdaptsSharedForeignUnownedOrSameNameAgents(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	marker := "[managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421]"
	for _, test := range []struct {
		name                            string
		agent                           *types.CustomAgent
		shared                          bool
		source, execution, callerTenant uint64
	}{
		{"same name", &types.CustomAgent{ID: "other", Name: "表格数据分析（结构化）", TenantID: 7, CreatedBy: "real-user"}, false, 0, 0, 7},
		{"another creator", &types.CustomAgent{ID: "managed", Description: marker, TenantID: 7, CreatedBy: "other-user"}, false, 0, 0, 7},
		{"missing creator", &types.CustomAgent{ID: "managed", Description: marker, TenantID: 7}, false, 0, 0, 7},
		{"foreign managed", &types.CustomAgent{ID: "managed", Description: marker, TenantID: 8, CreatedBy: "real-user"}, false, 0, 0, 7},
		{"shared without source", &types.CustomAgent{ID: types.BuiltinSmartReasoningID, IsBuiltin: true, TenantID: 7}, true, 0, 0, 7},
		{"explicit shared", &types.CustomAgent{ID: types.BuiltinSmartReasoningID, IsBuiltin: true, TenantID: 7}, false, 8, 0, 7},
		{"foreign execution", &types.CustomAgent{ID: types.BuiltinSmartReasoningID, IsBuiltin: true, TenantID: 7}, false, 0, 8, 7},
		{"caller mismatch", &types.CustomAgent{ID: types.BuiltinSmartReasoningID, IsBuiltin: true, TenantID: 7}, false, 0, 0, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: test.callerTenant, UserID: "real-user"})
			rc := &qaRequestContext{ctx: ctx, session: &types.Session{TenantID: 7}, customAgent: test.agent, sharedAgentReadOnly: test.shared, effectiveTenantID: test.execution}
			(&Handler{}).prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: test.agent.ID, AgentSourceTenantID: test.source})
			require.Same(t, test.agent, rc.customAgent)
			require.False(t, rc.customAgent.WorkbenchDefault)
		})
	}
}

func TestWorkbenchExplicitDocumentAndTagSelectionsDoNotAddWorkspaceKBDefaults(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "real-user"})
	for _, id := range []string{types.BuiltinQuickAnswerID, types.BuiltinSmartReasoningID} {
		for _, targets := range []struct {
			kb, doc []string
			tags    []types.TagScope
		}{
			{kb: []string{"kb-selected"}}, {doc: []string{"doc-selected"}}, {tags: []types.TagScope{{KnowledgeBaseID: "tag-kb", TagIDs: []string{"tag-only"}}}},
		} {
			original := &types.CustomAgent{ID: id, IsBuiltin: true, TenantID: 7, Config: types.CustomAgentConfig{KnowledgeBases: []string{"saved-other-kb"}}}
			rc := &qaRequestContext{ctx: ctx, session: &types.Session{TenantID: 7}, customAgent: original, knowledgeBaseIDs: targets.kb, knowledgeIDs: targets.doc, tagScopes: targets.tags}
			(&Handler{}).prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: id})
			require.Equal(t, "selected", rc.customAgent.Config.KBSelectionMode)
			require.Equal(t, targets.kb, rc.customAgent.Config.KnowledgeBases)
			require.Equal(t, targets.doc, rc.knowledgeIDs)
			require.Equal(t, targets.tags, rc.tagScopes)
			require.Equal(t, []string{"saved-other-kb"}, original.Config.KnowledgeBases)
		}
	}
}

func TestWorkbenchDoesNotReplaceUnresolvedExplicitAgentWithDefault(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured:18082")
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "real-user"})
	for _, id := range []string{"missing-custom-agent", types.BuiltinSmartReasoningID} {
		rc := &qaRequestContext{ctx: ctx, session: &types.Session{TenantID: 7}}
		(&Handler{}).prepareWorkbenchDefault(rc, &CreateKnowledgeQARequest{AgentID: id})
		require.Nil(t, rc.customAgent)
	}
}
