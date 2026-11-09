package agent

import (
	"encoding/json"
	"errors"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
)

func (e *AgentEngine) normalizeRegisteredToolArguments(name, raw string) (string, bool, error) {
	if e.toolRegistry == nil || !modelcontext.SupportsArgumentNormalization(name) {
		return raw, false, nil
	}
	tool, err := e.toolRegistry.GetTool(name)
	if err != nil {
		return raw, false, nil
	}
	switch tool.(type) {
	case *agenttools.MCPTool, *agenttools.MCPRegisteredTool:
		return raw, false, nil // Foreign schemas/identities remain opaque.
	}
	if len(tool.Parameters()) == 0 {
		return raw, false, nil
	}
	normalized, err := modelcontext.NormalizeToolArguments(raw, tool.Parameters())
	return normalized, normalized != raw, err
}

func toolArgumentFailure(tc types.LLMToolCall, err error) types.ToolCall {
	data := map[string]any{
		"kind": "tool_arguments", "status": "error", "reason_code": "invalid_tool_arguments",
		"notice": "工具未执行。请按本轮真实工具schema修正具体字段后重试；缺少的参数须依据完整任务和真实证据提供，不能猜测来源、ID或数值。",
	}
	var validation *modelcontext.ToolArgumentError
	if errors.As(err, &validation) {
		data["reason_code"], data["field_path"], data["validation"] = validation.Code, validation.Path, validation
	}
	data["error"] = err.Error()
	encoded, _ := json.Marshal(data)
	return types.ToolCall{
		ID: tc.ID, Name: tc.Function.Name, Args: map[string]any{"_raw": tc.ModelArguments}, ProviderMetadata: tc.ProviderMetadata,
		Result: &types.ToolResult{Success: false, Error: err.Error(), Output: string(encoded), Data: data},
	}
}
