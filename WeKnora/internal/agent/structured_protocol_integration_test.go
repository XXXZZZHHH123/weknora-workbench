package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const typedFieldManifest = `{"tools":[{"name":"confirm_fields","description":"Confirm a real user reply","inputSchema":{"type":"object","additionalProperties":false,"required":["resolution_id"],"properties":{"resolution_id":{"type":"string"},"selections":{"anyOf":[{"type":"object"},{"type":"null"}]},"note":{"type":"string"}}}},{"name":"resolve_fields","description":"Resolve the current actual task","inputSchema":{"type":"object","additionalProperties":false,"required":["question"],"properties":{"question":{"type":"string"},"previous_resolution_id":{"type":"string"}}}},{"name":"list_datasets","description":"List actual authorized sources","inputSchema":{"type":"object","additionalProperties":false,"properties":{}}}]}`

func TestStructuredTypedConfirmationUsesOriginalPendingScopeAndPreservesModelCall(t *testing.T) {
	var calls atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		identityRaw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(identityRaw, &identity))
		require.Equal(t, "kb-b", identity["knowledge_base_id"], "confirmation belongs to the actual pending plan, not the first authorized library")
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "confirm_fields", input["operation"])
		arguments := input["arguments"].(map[string]any)
		require.Nil(t, arguments["selections"], "outer-model guesses cannot become human selections")
		require.Equal(t, "使用我补充的字段说明", arguments["note"])
		_, _ = io.WriteString(w, `{"kind":"structured_field_resolution","status":"resolved","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`)
	})
	baseTool, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	baseTool.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 7}})
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(typedFieldManifest), &manifest))
	installed, err := registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, installed.Success)
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = registry
	caller := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	metadata := types.ToolCallMetadata{"opaque": json.RawMessage(`{"value":"unchanged"}`)}
	request := types.LLMToolCall{ID: "attempt", ProviderMetadata: metadata, Function: types.FunctionCall{Name: "structured_confirm_fields", Arguments: `{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{"v1":"guessed"},"note":"model note"}`}}
	blocked := engine.runToolCall(withFieldConfirmationTurn(caller, "查询一下", nil), request, 0, 0, 1, "session", "turn")
	require.False(t, blocked.Result.Success)
	require.Contains(t, blocked.Result.Error, "WAIT_FOR_USER")
	require.Zero(t, calls.Load())
	history := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "pending", Function: chat.FunctionCall{Name: "structured_resolve_fields", Arguments: `{"knowledge_base_id":"kb-b","question":"原问题"}`}}}},
		{Role: "tool", Name: "structured_resolve_fields", ToolCallID: "pending", Content: `{"kind":"structured_field_resolution","status":"needs_clarification","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`},
	}
	ctx := withFieldConfirmationTurn(caller, "使用我补充的字段说明", history)
	completed := engine.runToolCall(ctx, request, 0, 0, 1, "session", "turn")
	require.True(t, completed.Result.Success, completed.Result.Error)
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, request.Function.Name, completed.Name)
	require.Equal(t, metadata, completed.ProviderMetadata)
	require.Equal(t, "model note", completed.Args["note"], "history keeps the original provider call")
	require.Equal(t, "guessed", completed.Args["selections"].(map[string]any)["v1"])
	require.Equal(t, "structured_data", completed.Target.ServiceName)
	require.Equal(t, "confirm_fields", completed.Target.ToolName)
	require.Equal(t, "kb-b", completed.ExecutionArgs()["knowledge_base_id"])
	require.Nil(t, completed.ExecutionArgs()["selections"])
	require.Equal(t, "使用我补充的字段说明", completed.ExecutionArgs()["note"])
	repeated := engine.runToolCall(ctx, request, 0, 0, 1, "session", "turn")
	require.False(t, repeated.Result.Success)
	require.Equal(t, "confirmation_already_attempted", repeated.Result.Data["code"])
	require.Equal(t, int64(1), calls.Load(), "one human clarification cannot be sent twice by the model")
	request.Function.Arguments = `{"knowledge_base_id":"kb-a","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","selections":{"v1":"guessed"}}`
	wrongScope := engine.runToolCall(ctx, request, 0, 0, 1, "session", "turn")
	require.False(t, wrongScope.Result.Success)
	require.Equal(t, int64(1), calls.Load())
}

func TestStructuredConfirmationNewDirectHumanTurnRetainsClarificationAndRefusal(t *testing.T) {
	var calls atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "confirm_fields", input["operation"], "direct human turns do not need protocol or source discovery")
		arguments := input["arguments"].(map[string]any)
		switch calls.Add(1) {
		case 1:
			require.Equal(t, "这是我选择的字段", arguments["note"])
			require.Equal(t, "original.series", arguments["selections"].(map[string]any)["v1"])
		case 2:
			require.Equal(t, "不用这个字段，请取消", arguments["note"])
			require.Empty(t, arguments["selections"], "a later human may reject the same unresolved plan")
		default:
			t.Error("a submission without an actual previous pending result reached transport")
		}
		_, _ = io.WriteString(w, `{"kind":"structured_field_resolution","status":"needs_clarification","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`)
	})
	base, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}})
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	history := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "pending", Function: chat.FunctionCall{Name: "structured_resolve_fields", Arguments: `{"knowledge_base_id":"kb-a","question":"原问题"}`}}}},
		{Role: "tool", Name: "structured_resolve_fields", ToolCallID: "pending", Content: `{"kind":"structured_field_resolution","status":"needs_clarification","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`},
	}
	first := fieldConfirmationPrefix + `{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","source_tool":"structured_resolve_fields","selections":{"v1":"original.series"},"note":"这是我选择的字段"}`
	state, err := engine.Execute(ctx, "session", "first-human-turn", first, history)
	require.NoError(t, err)
	require.Len(t, state.RoundSteps, 1)
	require.True(t, state.RoundSteps[0].ToolCalls[0].Result.Success, state.FinalAnswer)
	later := fieldConfirmationPrefix + `{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","source_tool":"structured_resolve_fields","selections":{},"note":"不用这个字段，请取消"}`
	state, err = engine.Execute(ctx, "session", "later-human-turn", later, history)
	require.NoError(t, err)
	require.Len(t, state.RoundSteps, 1)
	require.True(t, state.RoundSteps[0].ToolCalls[0].Result.Success, state.FinalAnswer)
	require.Equal(t, int64(2), calls.Load(), "reusing an engine must not consume a later actual human reply")
	state, err = engine.Execute(ctx, "session", "without-pending", later, nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, int64(2), calls.Load(), "a new consumption boundary still requires the ordinary pending-result guard")
}

func TestStructuredCapabilitiesBootstrapPrecedesPlannerWithoutDataIOForNoSourceTask(t *testing.T) {
	var calls atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "describe_tools", request["operation"], "a no-source task performs no business lookup")
		_, _ = io.WriteString(w, typedFieldManifest)
	})
	plan, err := json.Marshal(taskEvidencePlan{Goals: []string{"回复已收到"}, Evidence: []string{}, ToolNames: []string{}, CompletionCriteria: []string{"给出文字回复"}, SourceDiscovery: &taskSourceDiscovery{Mode: "none"}, DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Criteria: "回复已收到"}}})
	require.NoError(t, err)
	model := &taskPlannerChat{mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "已收到", Done: true, FinishReason: "stop"}}}}}, plan: &types.ChatResponse{Content: string(plan), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "turn", "回复已收到", nil)
	require.NoError(t, err)
	require.Equal(t, "已收到", state.FinalAnswer)
	require.Equal(t, int64(1), calls.Load(), "only protocol schemas are read before planning; a no-source plan performs no source discovery")
	require.NotEmpty(t, model.requests)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
	capabilities := input["available_tools"].([]any)
	names := []string{}
	for _, capability := range capabilities {
		names = append(names, capability.(map[string]any)["name"].(string))
	}
	require.Contains(t, names, "structured_list_datasets")
	require.Contains(t, names, "structured_confirm_fields")
	require.Contains(t, names, agenttools.ToolStructuredCapabilities)
	require.NotContains(t, names, agenttools.ToolStructuredData)
	require.Empty(t, input["discovery_results"], "tool definitions are not source observations")
}

func TestStructuredManifestTimeoutCannotConsumeOrdinaryDataTimeout(t *testing.T) {
	release := make(chan struct{})
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		require.Equal(t, "describe_tools", request["operation"])
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state := &types.AgentState{}
	started := time.Now()
	messages := engine.prepareWorkbenchCapabilities(ctx, state, nil, "普通任务", "session", "turn")
	require.Less(t, time.Since(started), 4*time.Second, "schema negotiation must not inherit the 30-second data timeout")
	require.Len(t, state.RoundSteps, 1)
	require.False(t, state.RoundSteps[0].ToolCalls[0].Result.Success)
	require.NotEmpty(t, messages)
	names := listToolNames(engine.buildToolsForLLM())
	require.Contains(t, names, agenttools.ToolStructuredCapabilities)
	require.NotContains(t, names, agenttools.ToolStructuredData)
	require.NotContains(t, strings.Join(names, ","), "structured_confirm_fields")
}
