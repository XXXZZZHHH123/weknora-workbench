package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTaskPlanContextualDiscoveryKeepsHumanRequestAndAuthorizedTargets(t *testing.T) {
	const question = "把刚才的数据画出来"
	const contextualQuery = "已讨论的设备周度负荷记录：查找观测日期和负荷值的原始表"
	var mu sync.Mutex
	var requests []map[string]any
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(raw, &identity))
		require.Equal(t, float64(7), identity["tenant_id"])
		require.Equal(t, "verified", identity["user_id"])
		mu.Lock()
		requests = append(requests, input)
		mu.Unlock()
		if input["operation"] == "describe_tools" {
			_, _ = io.WriteString(w, workbenchDatasetManifest)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	})
	service := &sourceDiscoveryKnowledgeService{}
	registry.RegisterTool(agenttools.NewSearchKnowledgeTool(service, nil, nil, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "granted-base", TenantID: 7,
			KnowledgeIDs: []string{"selected-document"}, TagIDs: []string{"physical-tag"}, ScopeTagIDs: []string{"user-selected-tag"}},
	}, nil, nil))
	plan, err := json.Marshal(taskEvidencePlan{
		Goals:                []string{"按原始观测记录交付图表。"},
		Evidence:             []string{"先定位原始来源并核验日期与指标，不信任旧助手数字。"},
		SourceDiscovery:      &taskSourceDiscovery{Mode: "discover", Query: contextualQuery},
		ToolNames:            []string{agenttools.ToolStructuredData},
		CompletionCriteria:   []string{"图表观测值有真实来源。"},
		DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: "以真实观测日期为横轴、设备负荷为纵轴。"}},
	})
	require.NoError(t, err)
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: string(plan), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	engine.prepareTaskPlanning()
	state := &types.AgentState{}
	conversation := []chat.Message{{Role: "system", Content: "Actual runtime rules."},
		{Role: "user", Content: "请查看设备周度负荷的原始记录。"},
		{Role: "assistant", Content: "此前说明尚未核验，不能作为原始观测。"}, {Role: "user", Content: question}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	messages := engine.planWorkbenchTask(ctx, state, question, conversation, "session")
	require.NotNil(t, engine.taskPlan)
	require.Len(t, model.requests, 1)
	var plannerInput map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &plannerInput))
	require.Equal(t, question, plannerInput["current_user_question"])
	require.Contains(t, model.requests[0][1].Content, "设备周度负荷")
	engine.discoverWorkbenchSources(ctx, state, messages, question, "session", "message", engine.plannedSourceDiscoverySelection())
	mu.Lock()
	discovered := append([]map[string]any(nil), requests...)
	mu.Unlock()
	require.Len(t, discovered, 2)
	for _, request := range discovered {
		if request["operation"] == "list_datasets" {
			require.Equal(t, contextualQuery, request["arguments"].(map[string]any)["question"], "directory recall uses model-resolved context")
		}
	}
	service.mu.Lock()
	searches := append([]sourceDiscoverySearch(nil), service.searches...)
	service.mu.Unlock()
	require.Len(t, searches, 1)
	require.Equal(t, contextualQuery, searches[0].params.QueryText)
	require.Equal(t, []string{"selected-document"}, searches[0].params.KnowledgeIDs)
	require.Equal(t, []string{"physical-tag"}, searches[0].params.TagIDs)
	require.Equal(t, []string{"user-selected-tag"}, searches[0].params.ScopeTagIDs)
	require.Empty(t, searches[0].params.KnowledgeBaseIDs, "contextual retrieval cannot expand selected-document scope")
	require.False(t, hasStructuredEvidence(state.RoundSteps), "contextual plans/catalogues do not turn previous assistant data into evidence")

	// The retrieval hint must not replace the original task used by later
	// semantic source planning, even when a model supplies another source_question.
	tool, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"resolve_fields","arguments":{"question":"field lookup","source_question":"forged task"}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	var bound map[string]any
	require.NoError(t, json.Unmarshal([]byte(last["arguments"].(map[string]any)["source_question"].(string)), &bound))
	require.Equal(t, question, bound["current_user_question"])
	require.NotEqual(t, contextualQuery, bound["current_user_question"])
}

func TestTaskPlanSourceRecoverySkipsBroadSearchAndKeepsRealQueryContracts(t *testing.T) {
	var operations []string
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		operations = append(operations, input.Operation)
		_, _ = io.WriteString(w, `{"tools":[{"name":"read_pdf_table","description":"Read current source rows","inputSchema":{"type":"object","properties":{"table_id":{"type":"string"}}}},{"name":"query_pdf_table","description":"Query the real source table","inputSchema":{"type":"object","properties":{"table_id":{"type":"string"},"sql":{"type":"string"}}}}]}`)
	})
	knowledge := newCountingTool(agenttools.ToolSearchKnowledge)
	registry.RegisterTool(knowledge)
	registry.RegisterTool(newCountingTool(agenttools.ToolBindDocumentScope))
	registry.RegisterTool(newCountingTool(agenttools.ToolReadDocument))
	require.True(t, registry.PrepareStructuredProtocol(), "the normal execution entry advertises the local bootstrap before planning")
	plan, err := json.Marshal(taskEvidencePlan{
		Goals:              []string{"交付上一轮所讨论来源的图表。"},
		Evidence:           []string{"重新绑定近期已定位的文件并读取原表，按当前权限和版本恢复证据。"},
		SourceDiscovery:    &taskSourceDiscovery{Mode: "recover"},
		ToolNames:          []string{agenttools.ToolReadDocument},
		CompletionCriteria: []string{"从当前真实来源得到可核验图表。"},
	})
	require.NoError(t, err)
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: string(plan), FinishReason: "stop"}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	engine.prepareTaskPlanning()
	state := &types.AgentState{}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	messages := engine.planWorkbenchTask(ctx, state, "请画出刚才的记录", []chat.Message{{Role: "system", Content: "Actual runtime rules."}}, "session")
	require.NotNil(t, engine.taskPlan)
	selection := engine.plannedSourceDiscoverySelection()
	require.NotNil(t, selection)
	engine.discoverWorkbenchSources(ctx, state, messages, "请画出刚才的记录", "session", "message", selection)
	require.Empty(t, operations, "recovery does not negotiate or search unrelated directories before the execution model chooses a capability")
	require.Zero(t, knowledge.calls)
	require.False(t, hasStructuredEvidence(state.RoundSteps), "recovery instructions and schemas never stand in for newly read source evidence")
	definitions := listToolNames(engine.buildToolsForLLM())
	require.Contains(t, definitions, agenttools.ToolStructuredCapabilities)
	require.NotContains(t, definitions, "structured_query_pdf_table", "an unnegotiated operation must not be advertised as available")
	capability := engine.runToolCall(ctx, types.LLMToolCall{ID: "recover-capabilities", Function: types.FunctionCall{
		Name: agenttools.ToolStructuredCapabilities, Arguments: `{"refresh":true}`,
	}}, 0, 0, 0, "session", "message")
	require.NotNil(t, capability.Result)
	require.True(t, capability.Result.Success, capability.Result.Error)
	require.Equal(t, []string{"describe_tools"}, operations, "the first explicit capability request negotiates real query contracts without reading source data")
	require.Zero(t, knowledge.calls)
	definitions = listToolNames(engine.buildToolsForLLM())
	for _, name := range []string{"structured_read_pdf_table", "structured_query_pdf_table", agenttools.ToolStructuredCapabilities, agenttools.ToolReadDocument, agenttools.ToolBindDocumentScope, agenttools.ToolSearchKnowledge} {
		require.Contains(t, definitions, name, "the execution model can recover sources or explicitly discover a missing source")
	}
	require.NotContains(t, definitions, agenttools.ToolStructuredData, "the historical nested entry remains executable but is not a second model protocol")
}

func TestTaskPlanRejectsAmbiguousSourceAndDeliveryContracts(t *testing.T) {
	definitions := []types.FunctionDefinition{{Name: agenttools.ToolReadDocument}}
	base := taskEvidencePlan{Goals: []string{"Deliver the requested output."}, Evidence: []string{"Read the current source."},
		ToolNames: []string{agenttools.ToolReadDocument}, CompletionCriteria: []string{"Meet the requested scope."}}
	for _, test := range []struct {
		name       string
		acquire    *taskSourceDiscovery
		noEvidence bool
		delivery   []taskDeliveryRequirement
	}{
		{name: "missing contextual query", acquire: &taskSourceDiscovery{Mode: "discover"}},
		{name: "whitespace contextual query", acquire: &taskSourceDiscovery{Mode: "discover", Query: "  "}},
		{name: "oversized contextual query", acquire: &taskSourceDiscovery{Mode: "discover", Query: strings.Repeat("文", 1025)}},
		{name: "query without evidence", acquire: &taskSourceDiscovery{Mode: "discover", Query: "source topic"}, noEvidence: true},
		{name: "recovery without evidence", acquire: &taskSourceDiscovery{Mode: "recover"}, noEvidence: true},
		{name: "recovery with query", acquire: &taskSourceDiscovery{Mode: "recover", Query: "source topic"}},
		{name: "none with evidence", acquire: &taskSourceDiscovery{Mode: "none"}},
		{name: "none with query", acquire: &taskSourceDiscovery{Mode: "none", Query: "source topic"}, noEvidence: true},
		{name: "unknown acquisition", acquire: &taskSourceDiscovery{Mode: "guess"}},
		{name: "unknown delivery kind", delivery: []taskDeliveryRequirement{{Kind: "execute", Criteria: "Run a tool."}}},
		{name: "missing delivery meaning", delivery: []taskDeliveryRequirement{{Kind: "chart", Criteria: " "}}},
		{name: "oversized delivery meaning", delivery: []taskDeliveryRequirement{{Kind: "chart", Criteria: strings.Repeat("维", 513)}}},
		{name: "too many delivery requirements", delivery: make([]taskDeliveryRequirement, 9)},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := base
			plan.SourceDiscovery, plan.DeliveryRequirements = test.acquire, test.delivery
			if test.noEvidence {
				plan.Evidence = []string{}
			}
			encoded, err := json.Marshal(plan)
			require.NoError(t, err)
			_, err = decodeTaskEvidencePlan(string(encoded), definitions, 0)
			require.Error(t, err, "invalid advice must fall back instead of silently skipping necessary sources or requirements")
		})
	}
	for _, mode := range []string{"discover", "recover", "none", "legacy"} {
		plan := base
		switch mode {
		case "discover":
			plan.SourceDiscovery = &taskSourceDiscovery{Mode: mode, Query: "Context-resolved source topic and scope"}
		case "recover":
			plan.SourceDiscovery = &taskSourceDiscovery{Mode: mode}
		case "none":
			plan.SourceDiscovery, plan.Evidence = &taskSourceDiscovery{Mode: mode}, []string{}
		}
		plan.DeliveryRequirements = []taskDeliveryRequirement{{Kind: "chart", Criteria: "Show the source-backed observation dates and requested measure."}}
		encoded, err := json.Marshal(plan)
		require.NoError(t, err)
		decoded, err := decodeTaskEvidencePlan(string(encoded), definitions, 0)
		require.NoError(t, err)
		engine := &AgentEngine{taskPlan: decoded}
		require.Equal(t, plan.DeliveryRequirements, engine.taskReviewPlan()["delivery_requirements"], "completion review retains the explicit delivery contract independently of source acquisition")
	}
}
