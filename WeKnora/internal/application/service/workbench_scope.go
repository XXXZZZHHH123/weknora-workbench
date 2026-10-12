package service

import (
	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"slices"
)

// Only the marked legacy table connector is replaced. Unrelated services and
// every non-workbench agent retain their original MCP configuration.
func workbenchReplacesMCP(config *types.AgentConfig, service *types.MCPService) bool {
	return config != nil && config.WorkbenchDefault && service != nil &&
		slices.Contains(config.AllowedTools, tools.ToolStructuredData) &&
		types.IsManagedStructuredIntegration(service.Description)
}

// Default chat can avoid provisioning a sandbox, but smart reasoning keeps
// configured skills and sandbox execution after the shared data adaptation.
func workbenchUsesSandbox(config *types.AgentConfig) bool {
	return !config.WorkbenchDefault || config.SkillsEnabled || config.SandboxConfigID != ""
}
