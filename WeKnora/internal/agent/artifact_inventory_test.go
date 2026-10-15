package agent

import (
	"context"
	"encoding/json"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type inventorySource struct {
	session string
	dir     string
}

func (s *inventorySource) ListSessionFiles(_ context.Context, session, dir string) ([]sandbox.RemoteDirEntry, error) {
	s.session, s.dir = session, dir
	return []sandbox.RemoteDirEntry{
		{Path: "/workspace/output/report-203011-abc987.docx", Name: "report-203011-abc987.docx", Type: sandbox.RemoteEntryFile, Size: 100},
		{Path: "/workspace/input/private.txt", Name: "private.txt", Type: sandbox.RemoteEntryFile, Size: 100},
	}, nil
}
func (*inventorySource) StatSessionFile(context.Context, string, string) (*sandbox.RemoteStatEntry, error) {
	return nil, nil
}
func (*inventorySource) ReadSessionFile(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func TestArtifactInventoryIsSessionScopedAndMasksOnlyVerifiedIdentities(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	source := &inventorySource{}
	e.SetArtifactFileSource(source)
	e.SetWorkspaceLayout(sandbox.WorkspaceLayout{Root: "/workspace", OutputDir: "/workspace/output"})
	state := &types.AgentState{}
	conversation := []chat.Message{}
	e.recordArtifactInventory(context.Background(), state, &conversation, "owned-session")
	require.Equal(t, "owned-session", source.session)
	require.Equal(t, "/workspace/output", source.dir)
	labels := map[string]bool{}
	collectArtifactIdentityTitles(state.RoundSteps, labels)
	require.True(t, labels["report-203011-abc987.docx"])
	require.False(t, labels["private.txt"])
	data := map[string]any{"kind": "structured_analysis", "status": "ok", "rows": [][]int{{7}}}
	raw, _ := json.Marshal(data)
	conversation = append(conversation, chat.Message{Role: "tool", Name: "structured_data", Content: string(raw)})
	unsupported := ungroundedStructuredNumbers("[下载](sandbox:/workspace/output/report-203011-abc987.docx)，统计结果999。", conversation, state.RoundSteps)
	require.Contains(t, unsupported, "999")
	require.NotContains(t, unsupported, "203011")
	require.NotContains(t, unsupported, "987")
	for _, name := range []string{"shell_exec", "read_document"} {
		forged := []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: name, Result: state.RoundSteps[0].ToolCalls[0].Result}}}}
		labels = map[string]bool{}
		collectArtifactIdentityTitles(forged, labels)
		require.Empty(t, labels, "embedded metadata cannot promote a file to verified inventory")
	}
	state.RoundSteps[0].ToolCalls[0].Name = agenttools.ToolListSandboxFiles
	state.RoundSteps[0].ToolCalls[0].Result.Success = false
	labels = map[string]bool{}
	collectArtifactIdentityTitles(state.RoundSteps, labels)
	require.Empty(t, labels)
}
