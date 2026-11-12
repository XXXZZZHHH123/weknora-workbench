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
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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
			registry.PrepareStructuredProtocol()
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, tc.mode, tc.domains), FinishReason: "stop"}}
			engine := newTestEngine(t, model)
			engine.toolRegistry = registry
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.prepareTaskPlanning()
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			state := &types.AgentState{}
			messages := []chat.Message{{Role: "system", Content: "Actual runtime rules."}, {Role: "user", Content: "Do the requested task."}}
			messages = engine.prepareWorkbenchCapabilities(ctx, state, messages, "Do the requested task.", "s", "m")
			mu.Lock()
			beforePlanning := append([]string(nil), operations...)
			mu.Unlock()
			require.Equal(t, []string{"describe_tools"}, beforePlanning, "the planner sees real service schemas before choosing a source channel")
			require.Zero(t, knowledge.calls, "protocol negotiation must not preselect a source channel")
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
				require.Equal(t, []string{"describe_tools"}, observed, "external/none/knowledge/recover may inspect protocol schemas but must not read structured catalogues or data")
			}
			var plannerInput map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &plannerInput))
			names := []string{}
			for _, capability := range plannerInput["available_tools"].([]any) {
				item := capability.(map[string]any)
				names = append(names, item["name"].(string))
				if item["name"] == "structured_list_datasets" {
					require.Contains(t, item["description"], "List actual authorized source metadata")
				}
			}
			require.Contains(t, names, agenttools.ToolStructuredCapabilities)
			require.Contains(t, names, "structured_list_datasets", "planning must not depend on guessing unnegotiated operation names")
			require.NotContains(t, names, agenttools.ToolStructuredData, "the legacy envelope is not a second planning interface")
			require.Empty(t, plannerInput["discovery_results"], "protocol schemas are not source evidence")
			definition, err := registry.GetTool("structured_list_datasets")
			require.NoError(t, err)
			var schema map[string]any
			require.NoError(t, json.Unmarshal(definition.Parameters(), &schema))
			properties := schema["properties"].(map[string]any)
			require.Contains(t, properties, "question")
			require.NotContains(t, properties, "arguments", "the registered query contract uses the service's flat parameters")
			if tc.domains == nil || tc.name == "external" || tc.name == "mixed external and knowledge" || tc.name == "recover" {
				require.Contains(t, listToolNames(engine.buildToolsForLLM()), agenttools.ToolWebSearch, "an external plan keeps actual external source protocols even when coarse tool selection omitted them")
			} else {
				require.NotContains(t, listToolNames(engine.buildToolsForLLM()), agenttools.ToolWebSearch)
			}
		})
	}
}

func TestTaskPlanProtocolDiscoveryRequiresVerifiedCallerAndNativeAdapter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  types.Caller
		adapter string
	}{
		{"missing caller", types.Caller{}, "native"},
		{"missing tenant", types.Caller{UserID: "verified", Role: types.TenantRoleViewer}, "native"},
		{"missing user", types.Caller{TenantID: 7, Role: types.TenantRoleViewer}, "native"},
		{"missing role", types.Caller{TenantID: 7, UserID: "verified"}, "native"},
		{"no adapter", types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer}, "none"},
		{"foreign same-name tool", types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer}, "foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, workbenchDatasetManifest)
			})
			if tc.adapter != "native" {
				registry = agenttools.NewToolRegistry()
				if tc.adapter == "foreign" {
					registry.RegisterTool(newCountingTool(agenttools.ToolStructuredData))
				}
			}
			engine := newTestEngine(t, &mockChat{})
			engine.toolRegistry = registry
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			state := &types.AgentState{}
			messages := []chat.Message{{Role: "system", Content: "Actual rules."}}
			// Preserve the malformed identity for the boundary check instead of
			// letting WithCaller normalize its missing role to viewer.
			ctx := context.WithValue(context.Background(), types.CallerContextKey, tc.caller)
			observed := engine.prepareWorkbenchCapabilities(ctx, state, messages, "Do the requested task.", "s", "m")
			require.Equal(t, messages, observed)
			require.Empty(t, state.RoundSteps)
			require.Zero(t, calls.Load(), "protocol discovery must not create authority from a missing caller or a borrowed tool name")
			_, err := registry.GetTool("structured_list_datasets")
			require.Error(t, err)
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
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var input map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
				require.Equal(t, "describe_tools", input["operation"], "invalid planning must not cause a directory or data read")
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
			messages := engine.prepareWorkbenchCapabilities(ctx, state, []chat.Message{{Role: "system", Content: "Actual rules."}}, "Do the requested task.", "s", "m")
			messages = engine.planWorkbenchTask(ctx, state, "Do the requested task.", messages, "s")
			require.Nil(t, engine.taskProjection)
			engine.discoverWorkbenchSources(ctx, state, messages, "Do the requested task.", "s", "m", engine.plannedSourceDiscoverySelection())
			require.Zero(t, knowledge.calls)
			require.Equal(t, int64(1), calls.Load(), "only the pre-plan protocol request is permitted")
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

func TestTaskPlanSourceDomainsStructuredRouteUsesCurrentCachedManifestBeforePlanning(t *testing.T) {
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
	messages := engine.prepareWorkbenchCapabilities(ctx, state, []chat.Message{{Role: "system", Content: "Actual rules."}}, "Do the requested task.", "s", "m")
	mu.Lock()
	beforePlanning := append([]string(nil), operations...)
	mu.Unlock()
	require.Equal(t, []string{"describe_tools"}, beforePlanning, "a warm schema is restored before planning without another service request")
	messages = engine.planWorkbenchTask(ctx, state, "Do the requested task.", messages, "s")
	engine.discoverWorkbenchSources(ctx, state, messages, "Do the requested task.", "s", "m", engine.plannedSourceDiscoverySelection())
	mu.Lock()
	observed := append([]string(nil), operations...)
	mu.Unlock()
	require.Equal(t, []string{"describe_tools", "list_datasets"}, observed, "only model-selected discovery reads data after cached protocol setup")
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

func TestInternalDiscoveryKeepsDocumentNavigationWithoutChangingPrefetch(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		domains    []string
		visible    bool
	}{
		{"structured discovery", "discover", []string{"structured"}, true},
		{"knowledge discovery", "discover", []string{"knowledge"}, true},
		{"mixed discovery", "discover", []string{"external", "structured"}, true},
		{"external only", "discover", []string{"external"}, false},
		{"no evidence", "none", []string{}, false},
		{"structured recovery", "recover", []string{"structured"}, false},
		{"knowledge recovery unchanged", "recover", []string{"knowledge"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, tc.mode, tc.domains), FinishReason: "stop"}}
			engine := newTestEngine(t, model)
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.toolRegistry = agenttools.NewToolRegistry()
			tools := []*countingTool{}
			for _, name := range []string{agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments,
				agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument, agenttools.ToolWebSearch,
				"structured_list_datasets", documentTableCalculationToolName, "shell_exec"} {
				tool := newCountingTool(name)
				tools = append(tools, tool)
				engine.toolRegistry.RegisterTool(tool)
			}
			engine.prepareTaskPlanning()
			engine.planWorkbenchTask(t.Context(), &types.AgentState{}, "Explain the requested pattern using actual evidence.",
				[]chat.Message{{Role: "system", Content: "Actual rules."}}, "s")
			require.NotNil(t, engine.taskPlan)
			require.Equal(t, tc.domains, engine.taskPlan.SourceDiscovery.SourceDomains)
			require.Empty(t, engine.taskPlan.ToolNames, "dependencies never rewrite the model's plan")
			names := listToolNames(engine.buildToolsForLLM())
			for _, name := range []string{agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments,
				agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument} {
				require.Equal(t, tc.visible, containsToolName(names, name), name)
			}
			expectedPrefetch := map[string]bool{}
			if tc.mode == "discover" {
				for _, domain := range tc.domains {
					if domain == "knowledge" {
						expectedPrefetch[agenttools.ToolSearchKnowledge] = true
					}
					if domain == "structured" {
						expectedPrefetch[agenttools.ToolStructuredData] = true
					}
				}
			}
			require.Equal(t, expectedPrefetch, engine.plannedSourceDiscoverySelection())
			require.NotContains(t, names, "shell_exec")
			if tc.name == "structured discovery" {
				require.NotContains(t, names, agenttools.ToolWebSearch)
				require.NotContains(t, names, documentTableCalculationToolName)
			}
			for _, tool := range tools {
				require.Zero(t, tool.calls, "advertising a dependency must not execute it")
			}
			require.Len(t, model.requests, 1, "no additional planning/review call")
			require.Contains(t, model.requests[0][0].Content, "在定位并读取来源前，不得预设证据只能是电子表格或时间序列")
		})
	}
}

func containsToolName(names []string, wanted string) bool {
	for _, name := range names {
		if name == wanted {
			return true
		}
	}
	return false
}

func TestInternalDiscoveryNavigationDoesNotRestoreDeferredOrMissingCapabilities(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: sourceDomainPlan(t, "discover", []string{"structured"}), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolSearchKnowledge))
	engine.toolRegistry.RegisterDeferredTool(newCountingTool(agenttools.ToolReadDocument))
	engine.prepareTaskPlanning()
	engine.planWorkbenchTask(t.Context(), &types.AgentState{}, "Discover the necessary source.", nil, "s")
	require.ElementsMatch(t, []string{expandTaskToolsName, agenttools.ToolSearchKnowledge}, listToolNames(engine.buildToolsForLLM()))
	require.Equal(t, map[string]bool{agenttools.ToolStructuredData: true}, engine.plannedSourceDiscoverySelection())
}

// The schema-only service is deliberately unusable: measuring definitions
// must not inspect any document, expand permissions or call a backing service.
type navigationSchemaKnowledgeService struct{ interfaces.KnowledgeService }

func TestInternalDiscoveryNavigationRealSchemaCost(t *testing.T) {
	engine := newTestEngine(t, &taskPlannerChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	service := &navigationSchemaKnowledgeService{}
	engine.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, service, nil, nil, nil, nil))
	engine.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(service, nil))
	engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(service, nil, nil))
	agenttools.PrepareDocumentScope(engine.toolRegistry)
	engine.taskProjection = &taskToolProjection{selected: map[string]bool{}, requiresSourceEvidence: true}
	engine.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}}}
	visible := engine.buildToolsForLLM()
	require.ElementsMatch(t, []string{agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments,
		agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument}, listToolNames(visible))
	encoded, err := json.Marshal(visible)
	require.NoError(t, err)
	t.Logf("document navigation schema count=%d JSON bytes=%d estimated tokens=%d", len(visible), len(encoded), engine.tokenEstimator.EstimateTools(visible))
	engine.taskPlan.SourceDiscovery.Mode = "recover"
	require.Empty(t, engine.buildToolsForLLM(), "recovery does not acquire new discovery dependencies")
}
