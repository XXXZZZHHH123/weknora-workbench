package service

import (
	"context"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchReplacesOnlyManagedLegacyConnectorWhenNativeToolAvailable(t *testing.T) {
	marked := &types.MCPService{ID: "managed", Description: "Excel 服务 [managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421]"}
	other := &types.MCPService{ID: "other", Name: "结构化表格数据", Description: "用户自定义数据服务"}
	for _, mode := range []string{"all", "selected", ""} {
		cfg := &types.AgentConfig{WorkbenchDefault: true, AllowedTools: []string{tools.ToolStructuredData}, MCPSelectionMode: mode, MCPServices: []string{"managed", "other"}}
		require.True(t, workbenchReplacesMCP(cfg, marked))
		require.False(t, workbenchReplacesMCP(cfg, other))
		cfg.WorkbenchDefault = false
		require.False(t, workbenchReplacesMCP(cfg, marked))
		cfg.WorkbenchDefault = true
		cfg.AllowedTools = []string{tools.ToolCommodityResearch}
		require.False(t, workbenchReplacesMCP(cfg, marked), "no native structured service must not remove legacy table connector")
	}
}

func TestWorkbenchKeepsSandboxForConfiguredSkillsAndSandbox(t *testing.T) {
	require.False(t, workbenchUsesSandbox(&types.AgentConfig{WorkbenchDefault: true}))
	require.True(t, workbenchUsesSandbox(&types.AgentConfig{WorkbenchDefault: true, SkillsEnabled: true}))
	require.True(t, workbenchUsesSandbox(&types.AgentConfig{WorkbenchDefault: true, SandboxConfigID: "user-sandbox"}))
	require.True(t, workbenchUsesSandbox(&types.AgentConfig{}))
}

func TestWorkbenchEnginePreservesConfiguredSandboxCapabilities(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "")
	t.Setenv("COMMODITY_RESEARCH_URL", "")
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "real-user", Role: types.TenantRoleViewer})
	ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(7))
	for _, test := range []struct {
		name      string
		skills    bool
		sandboxID string
	}{
		{"configured sandbox with skills disabled", false, "chosen-sandbox"},
		{"configured skills and sandbox", true, "chosen-sandbox"},
		{"quick default without sandbox", false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &fakeAgentChatModel{}
			_, db := newResourceCatalogForTest(t)
			svc := &agentService{db: db, sandboxResolver: stubSandboxResolver{mgr: &capableManager{
				typ: sandbox.SandboxTypeCube, files: stubSessionFileStore{}, shell: &stubShellExecutor{},
			}}}
			config := &types.AgentConfig{WorkbenchDefault: true, SandboxConfigID: test.sandboxID, SkillsEnabled: test.skills, WebSearchEnabled: true, AllowedTools: []string{tools.ToolThinking, tools.ToolShellExec, tools.ToolWebFetch}}
			engine, err := svc.CreateAgentEngine(ctx, config, model, nil, nil, "workbench-session", "workbench-message")
			require.NoError(t, err)
			_, err = engine.Execute(ctx, "workbench-session", "workbench-message", "hello", nil)
			require.NoError(t, err)
			for _, tool := range []string{tools.ToolReadFile, tools.ToolWriteSandboxFile, tools.ToolEditSandboxFile} {
				require.Equal(t, test.sandboxID != "", toolOffered(model.lastToolNames, tool), "sandbox tool %s", tool)
			}
			require.Equal(t, test.skills, toolOffered(model.lastToolNames, tools.ToolShellExec), "remote shell keeps the original skills gate")
			if test.sandboxID != "" {
				require.Contains(t, model.lastToolDescriptions[tools.ToolReadFile], "web://", "web snapshots remain readable alongside sandbox files")
			}
		})
	}
}
