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

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const workbenchDatasetManifest = `{"tools":[{"name":"list_datasets","description":"List actual authorized source metadata","inputSchema":{"type":"object","additionalProperties":false,"properties":{"question":{"type":"string"}}}}]}`

func withWorkbenchDatasetDefinition(t *testing.T, raw []byte) []byte {
	t.Helper()
	var directory, catalog map[string]any
	require.NoError(t, json.Unmarshal(raw, &directory))
	require.NoError(t, json.Unmarshal([]byte(workbenchDatasetManifest), &catalog))
	directory["tools"] = append(directory["tools"].([]any), catalog["tools"].([]any)...)
	result, err := json.Marshal(directory)
	require.NoError(t, err)
	return result
}

func TestWorkbenchTypedPrefetchPreservesLegacyHistoryAndPinnedExecution(t *testing.T) {
	const question = "Locate the current authorized source records."
	manifestHash := strings.Repeat("a", 64)
	var reads, negotiations atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.NotEmpty(t, r.Header.Get("X-Research-Signature"))
		w.Header().Set("X-Structured-Manifest-SHA256", manifestHash)
		if input["operation"] == "describe_tools" {
			negotiations.Add(1)
			var manifest map[string]any
			require.NoError(t, json.Unmarshal([]byte(workbenchDatasetManifest), &manifest))
			manifest["manifest_sha256"], manifest["protocol_version"] = manifestHash, 2
			require.NoError(t, json.NewEncoder(w).Encode(manifest))
			return
		}
		reads.Add(1)
		require.Equal(t, "list_datasets", input["operation"])
		require.Equal(t, question, input["arguments"].(map[string]any)["question"])
		require.Equal(t, manifestHash, r.Header.Get("X-Structured-Manifest-SHA256"), "the typed adapter must pin the actual service generation")
		rawIdentity, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(rawIdentity, &identity))
		require.Equal(t, "granted-library", identity["knowledge_base_id"])
		require.Equal(t, float64(7), identity["knowledge_base_tenant_id"])
		_, _ = io.WriteString(w, `{"datasets":[],"literal_fields":[{"actual_field":"source column"}]}`)
	})
	base, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-library", TenantID: 7}})
	engine := newTestEngine(t, nil)
	engine.toolRegistry, engine.config.WorkbenchDefault = registry, true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	oldArgs := `{"operation":"list_datasets","arguments":{"question":"previous request"}}`
	history := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "legacy-pair", Type: "function", Function: chat.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: oldArgs}}}},
		{Role: "tool", ToolCallID: "legacy-pair", Name: agenttools.ToolStructuredData, Content: `{"datasets":[]}`},
	}
	before, err := json.Marshal(history)
	require.NoError(t, err)
	state := &types.AgentState{}
	messages := engine.discoverWorkbenchSources(ctx, state, history, question, "session", "current")
	after, err := json.Marshal(history)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "new prefetch must not mutate old names, nested args or call/result pairing")
	require.Equal(t, history, messages[:len(history)])
	require.Equal(t, int64(1), negotiations.Load())
	require.Equal(t, int64(1), reads.Load())
	require.Len(t, state.RoundSteps, 1)
	require.Len(t, state.RoundSteps[0].ToolCalls, 2)
	capability, catalog := state.RoundSteps[0].ToolCalls[0], state.RoundSteps[0].ToolCalls[1]
	require.Equal(t, agenttools.ToolStructuredCapabilities, capability.Name)
	require.NotNil(t, capability.Target)
	require.Equal(t, "describe_tools", capability.Target.ToolName)
	require.Equal(t, "structured_list_datasets", catalog.Name)
	require.Equal(t, map[string]any{"question": question}, catalog.Args)
	require.NotNil(t, catalog.Target)
	require.Equal(t, catalog.Name, catalog.Target.Name)
	require.Equal(t, "structured_data", catalog.Target.ServiceName)
	require.Equal(t, "list_datasets", catalog.Target.ToolName)
	require.Equal(t, catalog.Args, catalog.Target.Args)
	require.True(t, catalog.Result.Success, catalog.Result.Error)
	descriptor, ok := catalog.Result.Data["_structured_execution"].(agenttools.StructuredOperationDescriptor)
	require.True(t, ok)
	require.Equal(t, catalog.Name, descriptor.ToolName)
	require.Equal(t, "list_datasets", descriptor.Operation)
	require.Equal(t, manifestHash, descriptor.ManifestSHA256)
	require.NotEmpty(t, descriptor.SchemaSHA256)
	require.Positive(t, descriptor.Generation)
	require.Equal(t, "directory", descriptor.ScopeMode)
	require.Equal(t, "read", descriptor.EffectClass)
	foundCatalog := false
	for _, message := range messages[len(history):] {
		require.NotEqual(t, agenttools.ToolStructuredData, message.Name)
		for _, call := range message.ToolCalls {
			require.NotEqual(t, agenttools.ToolStructuredData, call.Function.Name, "current prefetch must not teach a new legacy call")
		}
		if message.Role == "tool" && message.Name == catalog.Name {
			foundCatalog = true
			require.Equal(t, catalog.Result.Output, message.Content, "typed prefetch preserves the tool's bounded directory view")
			require.Contains(t, message.Content, "source column", "actual recalled source metadata remains visible")
		}
	}
	require.True(t, foundCatalog)
	require.False(t, hasStructuredEvidence(state.RoundSteps), "a typed directory is still metadata, not numerical evidence")
}

func TestWorkbenchPrefetchDoesNotInventTypedCatalogWithoutManifestOrTrustNameCollision(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_manifest", true: "unrelated_same_name"}[collision], func(t *testing.T) {
			var negotiations, dataReads atomic.Int64
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				if input["operation"] != "describe_tools" {
					dataReads.Add(1)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				negotiations.Add(1)
				if collision {
					_, _ = io.WriteString(w, workbenchDatasetManifest)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			})
			unrelated := newCountingTool("structured_list_datasets")
			if collision {
				registry.RegisterTool(unrelated)
			}
			engine := newTestEngine(t, nil)
			engine.toolRegistry, engine.config.WorkbenchDefault = registry, true
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			state := &types.AgentState{}
			messages := engine.discoverWorkbenchSources(ctx, state, nil, "Locate source records.", "session", "current")
			require.Equal(t, int64(1), negotiations.Load())
			require.Zero(t, dataReads.Load())
			require.Zero(t, unrelated.calls, "a matching string is not a native descriptor")
			require.Len(t, state.RoundSteps, 1)
			require.Len(t, state.RoundSteps[0].ToolCalls, 1)
			require.Equal(t, agenttools.ToolStructuredCapabilities, state.RoundSteps[0].ToolCalls[0].Name)
			names := listToolNames(engine.buildToolsForLLM())
			require.Contains(t, names, agenttools.ToolStructuredCapabilities)
			require.NotContains(t, names, agenttools.ToolStructuredData)
			if !collision {
				require.NotContains(t, names, "structured_list_datasets")
				require.Contains(t, messages[len(messages)-1].Content, "manifest_unavailable")
			}
		})
	}
}
