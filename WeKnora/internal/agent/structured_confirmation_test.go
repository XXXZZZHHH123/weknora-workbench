package agent

import (
	"context"
	"encoding/json"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	internalmcp "github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	sdkmcp "github.com/mark3labs/mcp-go/mcp"
	sdkserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMCPStructuredConfirmationPreservesObjectsAndNotes(t *testing.T) {
	input := fieldConfirmationPrefix + `{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","source_tool":"mcp__resolve_fields_abc","selections":{"v1":"original.series"},"note":"只查收盘价","summary":"显示用"}`
	submission, err := parseStructuredConfirmation(input)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"v1": "original.series"}, submission.Selections)
	require.Equal(t, "只查收盘价", submission.Note)
	require.Equal(t, "mcp__resolve_fields_abc", submission.SourceTool)
}

func TestMCPStructuredConfirmationExecutesOnceWithTypedArgsAndLiveResult(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	var calls atomic.Int32
	server := sdkserver.NewMCPServer("fields", "1", sdkserver.WithToolCapabilities(false))
	schema := json.RawMessage(`{"type":"object","properties":{"resolution_id":{"type":"string"},"selections":{"anyOf":[{"type":"object","additionalProperties":{"type":"string"}},{"type":"null"}]},"note":{"type":"string"}},"required":["resolution_id"]}`)
	server.AddTool(sdkmcp.Tool{Name: "confirm_fields", RawInputSchema: schema}, func(_ context.Context, request sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)
		require.Equal(t, map[string]any{"v1": "original.series"}, request.GetArguments()["selections"])
		require.Equal(t, "只查收盘价", request.GetArguments()["note"])
		return sdkmcp.NewToolResultText(`{"kind":"structured_field_resolution","status":"resolved","answer":{"sources":{}}}`), nil
	})
	upstream := httptest.NewServer(sdkserver.NewStreamableHTTPServer(server, sdkserver.WithStateLess(true)))
	defer upstream.Close()
	manager := internalmcp.NewMCPManager(nil)
	defer manager.Shutdown()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	registry := agenttools.NewToolRegistry()
	_, err := agenttools.RegisterMCPTools(ctx, registry, []*types.MCPService{{ID: "fields", TenantID: 7, Name: "Fields", Enabled: true, URL: &upstream.URL, TransportType: types.MCPTransportHTTPStreamable}}, manager, nil, 0, nil, nil)
	require.NoError(t, err)
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	var results []event.AgentToolResultData
	engine.eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
		results = append(results, evt.Data.(event.AgentToolResultData))
		return nil
	})
	input := fieldConfirmationPrefix + `{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{"v1":"original.series"},"note":"只查收盘价"}`
	state, err := engine.Execute(ctx, "session", "message", input, nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	if len(state.RoundSteps) == 0 {
		t.Fatalf("no confirmation call: %s", state.FinalAnswer)
	}
	require.True(t, state.RoundSteps[0].ToolCalls[0].Result.Success, state.RoundSteps[0].ToolCalls[0].Result.Error)
	require.EqualValues(t, 1, calls.Load())
	require.Len(t, state.RoundSteps, 1)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)
	require.Contains(t, results[0].Output, "structured_field_resolution")
	otherTenant := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(8))
	denied, err := engine.Execute(otherTenant, "session", "message", input, nil)
	require.NoError(t, err)
	require.Len(t, denied.RoundSteps, 0)
	require.EqualValues(t, 1, calls.Load(), "Cross-tenant confirmation must not reach the service")
}

func TestMCPStructuredConfirmationRejectsMalformedSubmissions(t *testing.T) {
	for _, body := range []string{
		`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":"{\"v1\":\"x\"}","note":""}`,
		`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{},"note":""}`,
		`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{"v9":"x"},"note":""}`,
		`{"resolution_id":"../secret","selections":{},"note":"x"}`,
		`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{},"note":"x","arguments":{}}`,
		`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{},"note":"x"}{}`,
	} {
		_, err := parseStructuredConfirmation(fieldConfirmationPrefix + body)
		require.Error(t, err)
	}
	submission, err := parseStructuredConfirmation("一般用户问题")
	require.NoError(t, err)
	require.Nil(t, submission)
}
