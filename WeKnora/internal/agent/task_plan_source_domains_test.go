package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceDomainPlan(t *testing.T, mode string, domains []string) string {
	t.Helper()
	evidence := []string{"Obtain the evidence required by the current request."}
	query := "The contextual source topic."
	if mode != "discover" {
		query = ""
	}
	if mode == "none" {
		evidence = []string{}
	}
	value := map[string]any{
		"goals": []string{"Deliver the requested result."}, "evidence": evidence, "tool_names": []string{},
		"completion_criteria":   []string{"Deliver only what the user requested, with actual evidence when necessary."},
		"source_discovery":      map[string]any{"mode": mode, "query": query},
		"delivery_requirements": []map[string]any{{"kind": "text", "criteria": "Answer the current request."}},
	}
	if domains != nil {
		value["source_discovery"].(map[string]any)["source_domains"] = domains
	}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestTaskPlanSourceDomainsChooseOnlyRequestedDirectories(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		domains    []string
		knowledge  int
		structured bool
	}{
		{"knowledge", "discover", []string{"knowledge"}, 1, false},
		{"structured", "discover", []string{"structured"}, 0, true},
		{"external", "discover", []string{"external"}, 0, false},
		{"mixed external and knowledge", "discover", []string{"external", "knowledge"}, 1, false},
		{"mixed internal", "discover", []string{"knowledge", "structured"}, 1, true},
		{"none", "none", []string{}, 0, false},
		{"recover", "recover", []string{"structured", "external"}, 0, false},
		{"legacy omitted domains", "discover", nil, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			operations := []string{}
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, request *http.Request) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
				operation, _ := input["operation"].(string)
				mu.Lock()
				operations = append(operations, operation)
				mu.Unlock()
				if operation == "describe_tools" {
					_, _ = io.WriteString(w, workbenchDatasetManifest)
					return
				}
				require.Equal(t, "list_datasets", operation)
				require.Equal(t, "The contextual source topic.", input["arguments"].(map[string]any)["question"])
				_, _ = io.WriteString(w, `{"datasets":[]}`)
			})
			knowledge := newCountingTool(agenttools.ToolSearchKnowledge)
			registry.RegisterTool(knowledge)
			registry.RegisterTool(newCountingTool(agenttools.ToolWebSearch))
			registry.RegisterTool(newCountingTool(agenttools.ToolWebFetch))
			registry.PrepareStructuredProtocol() // local bootstrap only
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, tc.mode, tc.domains), FinishReason: "stop"}}
			engine := newTestEngine(t, model)
			engine.toolRegistry = registry
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.prepareTaskPlanning()
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			state := &types.AgentState{}
			messages := []chat.Message{{Role: "system", Content: "Actual runtime rules."}, {Role: "user", Content: "Do the requested task."}}
			engine.prepareWorkbenchCapabilities(ctx, state, messages, "Do the requested task.", "s", "m")
			mu.Lock()
			beforePlanning := append([]string(nil), operations...)
			mu.Unlock()
			require.Empty(t, beforePlanning, "intent planning sees local bootstrap without remote protocol setup")
			messages = engine.planWorkbenchTask(ctx, state, "Do the requested task.", messages, "s")
			require.NotNil(t, engine.taskPlan)
			require.Equal(t, tc.domains, engine.taskPlan.SourceDiscovery.SourceDomains)
			engine.discoverWorkbenchSources(ctx, state, messages, "Do the requested task.", "s", "m", engine.plannedSourceDiscoverySelection())
			require.Equal(t, tc.knowledge, knowledge.calls)
			mu.Lock()
			observed := append([]string(nil), operations...)
			mu.Unlock()
			if tc.structured {
				require.ElementsMatch(t, []string{"describe_tools", "list_datasets"}, observed)
			} else {
				require.Empty(t, observed, "external/none/knowledge/recover must not touch internal structured setup or catalogues")
			}
			var plannerInput map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &plannerInput))
			names := []string{}
			for _, capability := range plannerInput["available_tools"].([]any) {
				names = append(names, capability.(map[string]any)["name"].(string))
			}
			require.Contains(t, names, agenttools.ToolStructuredCapabilities, "deferred protocol must not look like an absent service")
			if tc.domains == nil || tc.name == "external" || tc.name == "mixed external and knowledge" || tc.name == "recover" {
				require.Contains(t, listToolNames(engine.buildToolsForLLM()), agenttools.ToolWebSearch, "an external plan keeps actual external source protocols even when coarse tool selection omitted them")
			} else {
				require.NotContains(t, listToolNames(engine.buildToolsForLLM()), agenttools.ToolWebSearch)
			}
		})
	}
}

func TestTaskPlanSourceDomainsInvalidOrFailedPlanningDoesNotSearchByDefault(t *testing.T) {
	for _, content := range []string{
		`{"incomplete":`,
		"model_error",
		sourceDomainPlan(t, "discover", []string{"unknown"}),
		sourceDomainPlan(t, "discover", []string{"knowledge", "knowledge"}),
		sourceDomainPlan(t, "discover", []string{}),
		sourceDomainPlan(t, "none", []string{"external"}),
		strings.Replace(sourceDomainPlan(t, "discover", []string{"external"}), `"source_domains":["external"]`, `"source_domains":null`, 1),
	} {
		t.Run(content, func(t *testing.T) {
			var calls atomic.Int64
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, workbenchDatasetManifest)
			})
			knowledge := newCountingTool(agenttools.ToolSearchKnowledge)
			registry.RegisterTool(knowledge)
			registry.RegisterTool(newCountingTool("actual_connected_reader"))
			registry.PrepareStructuredProtocol()
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: content, FinishReason: "stop"}}
			if content == "model_error" {
				model.err = errors.New("fixture planning unavailable")
			}
			engine := newTestEngine(t, model)
			engine.toolRegistry = registry
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.prepareTaskPlanning()
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			state := &types.AgentState{}
			messages := engine.planWorkbenchTask(ctx, state, "Do the requested task.", []chat.Message{{Role: "system", Content: "Actual rules."}}, "s")
			require.Nil(t, engine.taskProjection)
			engine.discoverWorkbenchSources(ctx, state, messages, "Do the requested task.", "s", "m", engine.plannedSourceDiscoverySelection())
			require.Zero(t, knowledge.calls)
			require.Zero(t, calls.Load())
			require.Contains(t, listToolNames(engine.buildToolsForLLM()), "actual_connected_reader", "failed planning keeps the execution model's real tools")
		})
	}
}

func TestTaskPlanSourceDomainsExternalCapabilityCanExpandWithoutGrantingMissingTools(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, "discover", []string{"external"}), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(newCountingTool("actual_connected_reader"))
	engine.prepareTaskPlanning()
	engine.planWorkbenchTask(t.Context(), &types.AgentState{}, "Do the requested task.", []chat.Message{{Role: "system", Content: "Actual rules."}}, "s")
	require.NotNil(t, engine.taskProjection)
	require.True(t, engine.taskProjection.requiresSourceEvidence, "external factual work keeps the ordinary source-review requirement")
	require.NotContains(t, listToolNames(engine.buildToolsForLLM()), "actual_connected_reader")
	expand, err := engine.toolRegistry.GetTool(expandTaskToolsName)
	require.NoError(t, err)
	result, err := expand.Execute(t.Context(), json.RawMessage(`{"names":["actual_connected_reader"],"reason":"The actual task needs this authorized reader."}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, listToolNames(engine.buildToolsForLLM()), "actual_connected_reader")
	result, err = expand.Execute(t.Context(), json.RawMessage(`{"names":["web_search"],"reason":"Need a missing capability."}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotContains(t, listToolNames(engine.buildToolsForLLM()), agenttools.ToolWebSearch, "source routing does not enable an unavailable network tool")
}

func TestTaskPlanSourceDomainsStructuredRouteUsesCurrentCachedManifestAfterPlanning(t *testing.T) {
	var mu sync.Mutex
	operations := []string{}
	manifestHash := strings.Repeat("a", 64)
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, request *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		operation, _ := input["operation"].(string)
		mu.Lock()
		operations = append(operations, operation)
		mu.Unlock()
		w.Header().Set("X-Structured-Manifest-SHA256", manifestHash)
		if operation == "describe_tools" {
			var manifest map[string]any
			require.NoError(t, json.Unmarshal([]byte(workbenchDatasetManifest), &manifest))
			manifest["manifest_sha256"], manifest["protocol_version"] = manifestHash, 2
			require.NoError(t, json.NewEncoder(w).Encode(manifest))
		} else {
			require.Equal(t, manifestHash, request.Header.Get("X-Structured-Manifest-SHA256"))
			_, _ = io.WriteString(w, `{"datasets":[]}`)
		}
	})
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-library", TenantID: 7}}
	base, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(targets)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	warm := newTestEngine(t, &mockChat{})
	warm.toolRegistry = registry
	warm.prepareWorkbenchCapabilities(ctx, &types.AgentState{}, nil, "Earlier task.", "s", "warm")
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, "discover", []string{"structured"}), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(targets))
	engine.toolRegistry.PrepareStructuredProtocol()
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	engine.prepareTaskPlanning()
	state := &types.AgentState{}
	messages := engine.planWorkbenchTask(ctx, state, "Do the requested task.", []chat.Message{{Role: "system", Content: "Actual rules."}}, "s")
	engine.discoverWorkbenchSources(ctx, state, messages, "Do the requested task.", "s", "m", engine.plannedSourceDiscoverySelection())
	mu.Lock()
	observed := append([]string(nil), operations...)
	mu.Unlock()
	require.Equal(t, []string{"describe_tools", "list_datasets"}, observed, "post-plan negotiation reuses only the current authenticated service manifest cache")
	require.Contains(t, listToolNames(engine.buildToolsForLLM()), "structured_list_datasets")
}

func TestTaskPlanSourceDomainsProjectSchemasAndKeepOtherDomainsExpandable(t *testing.T) {
	fixture, err := os.ReadFile("tools/testdata/structured_read_operations.json")
	require.NoError(t, err)
	for _, domain := range []string{"knowledge", "external"} {
		t.Run(domain, func(t *testing.T) {
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(fixture)
			})
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, "discover", []string{domain}), FinishReason: "stop"}}
			engine := newTestEngine(t, model)
			engine.toolRegistry = registry
			engine.prepareWorkbenchCapabilities(ctx, &types.AgentState{}, nil, "Fixture setup.", "s", "setup")
			for _, name := range []string{agenttools.ToolSearchKnowledge, agenttools.ToolReadDocument, agenttools.ToolListDocuments,
				agenttools.ToolBindDocumentScope, agenttools.ToolWebSearch, agenttools.ToolWebFetch} {
				registry.RegisterTool(newCountingTool(name))
			}
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.prepareTaskPlanning()
			before := engine.buildToolsForLLM()
			beforeJSON, err := json.Marshal(before)
			require.NoError(t, err)
			engine.planWorkbenchTask(ctx, &types.AgentState{}, "Do the requested task.", []chat.Message{{Role: "system", Content: "Actual rules."}}, "s")
			after := engine.buildToolsForLLM()
			afterJSON, err := json.Marshal(after)
			require.NoError(t, err)
			require.Less(t, len(after), len(before))
			require.Less(t, len(afterJSON), len(beforeJSON))
			require.Less(t, engine.tokenEstimator.EstimateTools(after), engine.tokenEstimator.EstimateTools(before))
			t.Logf("schema_projection_fixture domain=%s before_tools=%d after_tools=%d before_json_bytes=%d after_json_bytes=%d before_estimated_tokens=%d after_estimated_tokens=%d",
				domain, len(before), len(after), len(beforeJSON), len(afterJSON), engine.tokenEstimator.EstimateTools(before), engine.tokenEstimator.EstimateTools(after))
			visible := listToolNames(after)
			require.NotContains(t, visible, "structured_list_series")
			require.Contains(t, visible, expandTaskToolsName)
			expand, err := registry.GetTool(expandTaskToolsName)
			require.NoError(t, err)
			result, err := expand.Execute(ctx, json.RawMessage(`{"names":["structured_list_series"],"reason":"A newly found source gap needs this actual registered capability."}`))
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Contains(t, listToolNames(engine.buildToolsForLLM()), "structured_list_series")
		})
	}
}
