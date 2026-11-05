package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Generate the service fixture from the entire native protocol inventory. The
// assertion below inspects actual registrations, not a second hand-copied list
// of the few operations that happened to have codec policies in the past.
func structuredCodecManifest() map[string]any {
	entries := []any{}
	for _, operation := range agenttools.StructuredOperationNames() {
		entries = append(entries, map[string]any{"name": operation, "description": "Synthetic protocol operation", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}})
	}
	return map[string]any{"protocol_version": 2, "manifest_sha256": strings.Repeat("c", 64), "tools": entries}
}

func TestEveryRegisteredNativeSelectorHasCurrentSourceCodecContract(t *testing.T) {
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { t.Error("codec coverage must not access data") })
	base, _ := registry.GetTool(agenttools.ToolStructuredData)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-current", TenantID: 7}})
	installed, err := registry.InstallStructuredManifest(structuredCodecManifest())
	require.NoError(t, err)
	require.True(t, installed.Success)
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.syncStructuredToolSourceContracts()
	handle := engine.modelContext.RegisterKnowledgeBase("kb-current")
	count := 0
	for _, definition := range registry.GetFunctionDefinitions() {
		descriptor, _, native := registry.StructuredInvocation(definition.Name, json.RawMessage(`{}`))
		if !native || descriptor.Operation == "describe_tools" {
			continue
		}
		count++
		t.Run(definition.Name, func(t *testing.T) {
			original := `{"knowledge_base_id":"` + handle + `","sql":"SELECT '` + handle + `'","data":{"knowledge_base_id":"` + handle + `"}}`
			calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: definition.Name, Arguments: original}}}
			engine.modelContext.DecodeToolCalls(calls)
			require.JSONEq(t, `{"knowledge_base_id":"kb-current","sql":"SELECT '`+handle+`'","data":{"knowledge_base_id":"`+handle+`"}}`, calls[0].Function.Arguments)
			require.Equal(t, original, calls[0].ModelArguments)
		})
	}
	require.Equal(t, len(agenttools.StructuredOperationNames()), count, "every current native operation was really registered and checked")
}

type externalStructuredCodecTool struct{ agenttools.BaseTool }

func (t *externalStructuredCodecTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true}, nil
}

func TestStructuredSourceCodecRefreshUsesActualRegistrationAndRejectsNameCollisions(t *testing.T) {
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { t.Error("no HTTP expected") })
	base, _ := registry.GetTool(agenttools.ToolStructuredData)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-current", TenantID: 7}})
	for _, name := range []string{"structured_list_pdf_tables", "structured_query_series"} {
		registry.RegisterTool(&externalStructuredCodecTool{BaseTool: agenttools.NewBaseTool(name, "External identity contract", json.RawMessage(`{"type":"object","properties":{"knowledge_base_id":{"type":"string"}}}`))})
	}
	_, err := registry.InstallStructuredManifest(structuredCodecManifest())
	require.NoError(t, err)
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.syncStructuredToolSourceContracts()
	handle := engine.modelContext.RegisterKnowledgeBase("kb-current")
	for _, name := range []string{"structured_list_pdf_tables", "structured_query_series"} {
		calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: name, Arguments: `{"knowledge_base_id":"` + handle + `"}`}}}
		engine.modelContext.DecodeToolCalls(calls)
		require.NotContains(t, calls[0].Function.Arguments, `"knowledge_base_id":"kb-current"`, "same name/schema on a first-wins external registration does not own the source field")
	}
	// A subsequent real manifest removes an old native tool. Its prior codec
	// snapshot must not remain active merely because the name was seen before.
	manifest := structuredCodecManifest()
	for _, entry := range manifest["tools"].([]any) {
		if entry.(map[string]any)["name"] == "list_datasets" {
			manifest["tools"] = []any{entry}
			break
		}
	}
	manifest["manifest_sha256"] = strings.Repeat("d", 64)
	_, err = registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	engine.syncStructuredToolSourceContracts()
	removed := []types.LLMToolCall{{Function: types.FunctionCall{Name: "structured_read_pdf_table", Arguments: `{"knowledge_base_id":"` + handle + `"}`}}}
	engine.modelContext.DecodeToolCalls(removed)
	require.NotContains(t, removed[0].Function.Arguments, `"knowledge_base_id":"kb-current"`)
}

func TestModelHandleFlowsThroughNativeSelectorScopeAndSignedHTTP(t *testing.T) {
	var requests atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var request, identity map[string]any
		require.NoError(t, json.Unmarshal(body, &request))
		encoded := r.Header.Get("X-Research-Identity")
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &identity))
		require.Equal(t, "list_pdf_tables", request["operation"])
		require.Equal(t, "kb-shared", identity["knowledge_base_id"])
		require.Equal(t, float64(29), identity["knowledge_base_tenant_id"])
		require.Equal(t, float64(7), identity["tenant_id"])
		require.NotContains(t, request["arguments"], "knowledge_base_id")
		bodyHash := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(strings.Repeat("k", 48)))
		_, _ = mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", encoded, hex.EncodeToString(bodyHash[:])}, "\n")))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
		require.Equal(t, strings.Repeat("c", 64), r.Header.Get("X-Structured-Manifest-SHA256"))
		w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat("c", 64))
		_, _ = io.WriteString(w, `{"status":"ok","tables":[]}`)
	})
	base, _ := registry.GetTool(agenttools.ToolStructuredData)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-shared", TenantID: 29}})
	_, err := registry.InstallStructuredManifest(structuredCodecManifest())
	require.NoError(t, err)
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	handle := engine.modelContext.RegisterKnowledgeBase("kb-shared")
	original := ` { "knowledge_base_id" : "` + handle + `" } `
	provider := types.ToolCallMetadata{"opaque": json.RawMessage(`{"keep":"bytes"}`)}
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeToolCall, ToolCalls: []types.LLMToolCall{{ID: "native-call", ProviderMetadata: provider, Function: types.FunctionCall{Name: "structured_list_pdf_tables", Arguments: original}}}, Done: true, FinishReason: "tool_calls"}}}}}
	engine.chatModel = model
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified-user", Role: types.TenantRoleViewer})
	response, err := engine.streamLLMToEventBus(ctx, []chat.Message{{Role: "user", Content: "Read authorized metadata"}}, &chat.ChatOptions{}, func(*types.StreamResponse, string) {})
	require.NoError(t, err)
	require.Len(t, response.ToolCalls, 1)
	call := response.ToolCalls[0]
	require.JSONEq(t, `{"knowledge_base_id":"kb-shared"}`, call.Function.Arguments)
	require.Equal(t, original, call.ModelArguments)
	require.Equal(t, provider, call.ProviderMetadata)
	result := engine.runToolCall(ctx, call, 0, 0, 0, "session", "turn")
	require.True(t, result.Result.Success, result.Result.Error)
	require.Equal(t, int64(1), requests.Load(), "one normal execution, no automatic replay")
	require.Equal(t, provider, result.ProviderMetadata)
	unknown := []types.LLMToolCall{{ID: "unknown", Function: types.FunctionCall{Name: call.Function.Name, Arguments: `{"knowledge_base_id":"kb://999"}`}}}
	engine.modelContext.DecodeToolCalls(unknown)
	blocked := engine.runToolCall(ctx, unknown[0], 0, 0, 0, "session", "turn")
	require.False(t, blocked.Result.Success)
	require.Equal(t, int64(1), requests.Load(), "an unresolved handle cannot reach transport")
}
