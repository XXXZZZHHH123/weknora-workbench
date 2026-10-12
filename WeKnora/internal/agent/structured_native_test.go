package agent

import (
	"context"
	"encoding/json"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeStructuredRegistry(t *testing.T, handler http.HandlerFunc) *agenttools.ToolRegistry {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("k", 48)), 0600))
	t.Setenv("STRUCTURED_DATA_URL", server.URL)
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewStructuredDataTool())
	return registry
}
func TestNativeStructuredConfirmationIsWrappedAndExecutedOnce(t *testing.T) {
	calls := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "confirm_fields", input["operation"])
		args := input["arguments"].(map[string]any)
		require.Equal(t, "original.series", args["selections"].(map[string]any)["v1"])
		_, _ = io.WriteString(w, `{"kind":"structured_field_resolution","status":"resolved","answer":{"summary":"原表确认完成"}}`)
	})
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actual", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", fieldConfirmationPrefix+`{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","source_tool":"structured_data","selections":{"v1":"original.series"},"note":"收盘价"}`, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.True(t, state.IsComplete)
	require.Equal(t, "原表确认完成", state.FinalAnswer)
}
func TestNativeStructuredModelCannotConfirmFieldsInSameTurn(t *testing.T) {
	calls := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, `{}`) })
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actual", Role: types.TenantRoleViewer})
	ctx = withFieldConfirmationTurn(ctx, "计算价格", nil)
	call := engine.runToolCall(ctx, types.LLMToolCall{ID: "forged", Function: types.FunctionCall{Name: "structured_data", Arguments: `{"operation":"confirm_fields","arguments":{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{"v1":"fake"}}}`}}, 0, 0, 1, "session", "message")
	require.False(t, call.Result.Success)
	require.Contains(t, call.Result.Error, "WAIT_FOR_USER")
	require.Zero(t, calls)
}
func TestNativeStructuredAvailableDoesNotForceAQueryForGreeting(t *testing.T) {
	calls := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, `{}`) })
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "你好。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	state, err := engine.Execute(context.Background(), "session", "message", "你好", []chat.Message{})
	require.NoError(t, err)
	require.Equal(t, "你好。", state.FinalAnswer)
	require.Zero(t, calls)
}
