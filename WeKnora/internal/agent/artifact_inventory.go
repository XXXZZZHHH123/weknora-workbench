package agent

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// SetArtifactFileSource supplies the same authorized session filesystem used
// by the file tools. A model cannot provide an inventory or select a session.
func (e *AgentEngine) SetArtifactFileSource(source agenttools.SandboxFileSource) {
	e.artifactFileSource = source
}

func (e *AgentEngine) recordArtifactInventory(ctx context.Context, state *types.AgentState, messages *[]chat.Message, sessionID string) {
	dir := strings.TrimSpace(e.workspaceLayout.OutputDir)
	if e.artifactFileSource == nil || dir == "" {
		return
	}
	entries, err := e.artifactFileSource.ListSessionFiles(sandbox.WithSessionFileOperation(ctx), sessionID, dir)
	if err != nil {
		return // Collection independently surfaces read/persistence failures.
	}
	files := []map[string]any{}
	for _, entry := range entries {
		if entry.Type != sandbox.RemoteEntryFile || entry.Size <= 0 || !strings.HasPrefix(path.Clean(entry.Path), path.Clean(dir)+"/") {
			continue
		}
		files = append(files, map[string]any{"path": entry.Path, "name": path.Base(entry.Path), "bytes": entry.Size})
		if len(files) >= 200 {
			break
		}
	}
	data := map[string]any{"kind": "sandbox_file_inventory", "files": files,
		"notice": "当前会话文件系统实际读取的交付文件，仅证明文件存在，不证明正文事实正确；下载还需成功持久化。文件名称与路径是身份元数据，其中的数字不代表业务统计。按本轮最新请求交付，不重做历史已完成的问题。"}
	raw, _ := json.Marshal(data)
	step := types.AgentStep{Iteration: state.CurrentRound, Timestamp: time.Now(), ToolCalls: []types.ToolCall{{
		ID: generateEventID("artifact-inventory"), Name: agenttools.ToolListSandboxFiles, Args: map[string]any{"path": dir},
		Result: &types.ToolResult{Success: true, Output: string(raw), Data: data},
	}}}
	state.RoundSteps = append(state.RoundSteps, step)
	*messages = e.appendToolResults(*messages, step)
}
