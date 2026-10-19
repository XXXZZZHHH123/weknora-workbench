package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type taskPlannerChat struct {
	mockChat
	plan     *types.ChatResponse
	err      error
	wait     bool
	requests [][]chat.Message
	options  []*chat.ChatOptions
}

func (model *taskPlannerChat) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	model.requests = append(model.requests, append([]chat.Message(nil), messages...))
	model.options = append(model.options, options)
	if model.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return model.plan, model.err
}

func taskPlanFixture(t *testing.T, model *taskPlannerChat) (*AgentEngine, *types.AgentState, []chat.Message) {
	t.Helper()
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	engine.toolRegistry = agenttools.NewToolRegistry()
	for _, name := range []string{"structured_list_series", "read_document", "bind_document_scope", "read_file", "shell_exec", "calculate_document_table", "calculate_source_values", documentTableAnalysisToolName} {
		engine.toolRegistry.RegisterTool(newCountingTool(name))
	}
	engine.prepareTaskPlanning()
	state := &types.AgentState{RoundSteps: []types.AgentStep{{Iteration: -1, ToolCalls: []types.ToolCall{{
		ID: "discovered-files", Name: "structured_data", Args: map[string]any{"operation": "list_datasets"},
		Result: &types.ToolResult{Success: true, Output: `{"datasets":[{"dataset_id":"actual-dataset","source_file":"source.xlsx","sheets":["records"],"is_latest_for_source":true}]}`},
	}}}}}
	messages := []chat.Message{{Role: "system", Content: "Actual runtime rules."}, {Role: "user", Content: "Earlier request with an explicit scope."}, {Role: "assistant", Content: "Earlier unverified explanation."}, {Role: "user", Content: "Current complete request."}}
	return engine, state, messages
}

func validTaskPlan(t *testing.T, tools ...string) string {
	t.Helper()
	encoded, err := json.Marshal(taskEvidencePlan{Goals: []string{"Deliver the requested structure."}, Evidence: []string{"Actual source field metadata."}, ToolNames: tools, SourceChoices: []int{0}, CompletionCriteria: []string{"Answer all requested fields with actual source attribution."}})
	require.NoError(t, err)
	return string(encoded)
}

func TestTaskPlanProjectsOnlyRealToolsAndKeepsExecutionAuthorization(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "structured_list_series"), FinishReason: "stop", Usage: types.TokenUsage{TotalTokens: 17}}}
	engine, state, messages := taskPlanFixture(t, model)
	before := len(state.RoundSteps)
	planned := engine.planWorkbenchTask(t.Context(), state, "Current complete request.", messages, "session")
	require.ElementsMatch(t, []string{"structured_list_series", "bind_document_scope", "read_document", documentTableCalculationToolName, documentTableAnalysisToolName, expandTaskToolsName}, listToolNames(engine.buildToolsForLLM()))
	require.Len(t, state.RoundSteps, before, "planning is not an executed source result")
	require.Equal(t, 17, state.TurnUsage.TotalTokens)
	require.Contains(t, planned[0].Content, "advisory_task_plan")
	require.NotContains(t, messages[0].Content, "advisory_task_plan", "do not mutate the input transcript")
	require.Equal(t, 0.7, engine.config.Temperature)
	require.Zero(t, model.options[0].Temperature)
	require.Equal(t, 768, model.options[0].MaxCompletionTokens)
	require.Empty(t, model.options[0].Tools)
	require.Equal(t, "none", model.options[0].ToolChoice)
	require.NotNil(t, model.options[0].Thinking)
	require.False(t, *model.options[0].Thinking)
	actual, err := engine.toolRegistry.ExecuteTool(t.Context(), "shell_exec", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, actual.Success, "projection never removes existing executor permission checks or registry entries")
	require.False(t, agenttools.CanRunConcurrently(expandTaskToolsName), "plan expansion is an execution barrier")
}

func TestTaskPlanRetainsQuestionOriginPinnedSourcesAndBoundedDiscovery(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "read_document"), FinishReason: "stop"}}
	engine, state, messages := taskPlanFixture(t, model)
	engine.questionOrigin = &QuestionOriginInfo{KnowledgeBaseID: "real-kb", KnowledgeBaseName: "Selected library", Document: &SelectedDocumentInfo{KnowledgeID: "real-doc", FileName: "selected-report.pdf"}}
	engine.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "real-doc", FileName: "selected-report.pdf"}}
	engine.pinnedSkills = []*PinnedSkillInfo{{Name: "document-generator"}}
	engine.knowledgeBasesInfo = []*KnowledgeBaseInfo{{ID: "real-kb", Name: "Selected library"}}
	state.RoundSteps[0].ToolCalls[0].Result.Output = `{"dataset_id":"actual-dataset","truncated":true,"rows":["` + strings.Repeat("actual source text ", 10000) + `"]}`
	messages = append(messages, chat.Message{Role: "user", Content: engine.RenderUserTurnContent("session", "Current complete request.")})
	engine.planWorkbenchTask(t.Context(), state, "Current complete request.", messages, "session")
	require.Len(t, model.requests, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
	require.Equal(t, "Current complete request.", input["current_user_question"])
	require.Contains(t, model.requests[0][1].Content, "selected-report.pdf")
	require.Contains(t, model.requests[0][1].Content, "document-generator")
	require.Contains(t, model.requests[0][1].Content, "Selected library")
	require.NotContains(t, model.requests[0][1].Content, "real-doc", "origins use the same source codec as ordinary model context")
	scope := input["discovery_scope"].(map[string]any)
	require.Equal(t, false, scope["view_complete"])
	require.Contains(t, model.requests[0][1].Content, "omitted_paths")
	require.Contains(t, model.requests[0][1].Content, "calculate_document_table")
	require.Contains(t, model.requests[0][1].Content, "calculate_source_values")
}

func TestTaskPlanInvalidUnavailableOrIncompletePlansFallBackToFullTools(t *testing.T) {
	for _, response := range []*types.ChatResponse{
		{Content: `{}`},
		{Content: `{"goals":["incomplete"`},
		{Content: strings.Replace(validTaskPlan(t, "read_document"), `"tool_names":["read_document"]`, `"tool_names":null`, 1)},
		{Content: strings.Replace(validTaskPlan(t, "read_document"), `"tool_names":["read_document"],`, ``, 1)},
		{Content: strings.Replace(validTaskPlan(t, "read_document"), `"evidence":["Actual source field metadata."]`, `"evidence":null`, 1)},
		{Content: validTaskPlan(t, "unauthorized_tool")},
		{Content: strings.Replace(validTaskPlan(t, "read_document"), `"source_choices":[0]`, `"source_choices":[9]`, 1)},
		{Content: validTaskPlan(t, "read_document"), FinishReason: "length"},
		{Content: validTaskPlan(t, "read_document"), ToolCalls: []types.LLMToolCall{{Function: types.FunctionCall{Name: "read_document"}}}},
	} {
		t.Run(response.Content, func(t *testing.T) {
			model := &taskPlannerChat{plan: response}
			engine, state, messages := taskPlanFixture(t, model)
			before := listToolNames(engine.buildToolsForLLM())
			after := engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s")
			require.Equal(t, messages, after)
			require.Nil(t, engine.taskProjection)
			require.Equal(t, before, listToolNames(engine.buildToolsForLLM()))
		})
	}
	for _, model := range []*taskPlannerChat{{err: errors.New("provider unavailable")}, {wait: true}} {
		engine, state, messages := taskPlanFixture(t, model)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		after := engine.planWorkbenchTask(ctx, state, "Current request", messages, "s")
		cancel()
		require.Equal(t, messages, after)
		require.Nil(t, engine.taskProjection)
	}
}

func TestTaskPlanExplicitEmptyToolChoiceRetainsSourceAndExpansionProtocols(t *testing.T) {
	content := strings.Replace(validTaskPlan(t, "read_document"), `"tool_names":["read_document"]`, `"tool_names":[]`, 1)
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: content, FinishReason: "stop"}}
	engine, state, messages := taskPlanFixture(t, model)
	planned := engine.planWorkbenchTask(t.Context(), state, "Use the supplied material to complete the task.", messages, "s")
	require.NotNil(t, engine.taskProjection, "an explicit empty choice is a valid model plan")
	require.ElementsMatch(t, []string{"structured_list_series", "bind_document_scope", "read_document", documentTableCalculationToolName, documentTableAnalysisToolName, expandTaskToolsName}, listToolNames(engine.buildToolsForLLM()))
	require.Contains(t, planned[0].Content, "advisory_task_plan")
}

func TestEvidencePlanDiscoversBothAuthorizedSourceDirectories(t *testing.T) {
	operations := make(chan string, 2)
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		operation, _ := input["operation"].(string)
		operations <- operation
		_, _ = io.WriteString(w, `{}`)
	})
	knowledge := newCountingTool(agenttools.ToolSearchKnowledge)
	registry.RegisterTool(knowledge)
	reader := newCountingTool(agenttools.ToolReadDocument)
	registry.RegisterTool(reader)
	plan, err := json.Marshal(taskEvidencePlan{
		Goals:              []string{"Answer the request from a real source."},
		Evidence:           []string{"The relevant source material."},
		ToolNames:          []string{"read_document"},
		CompletionCriteria: []string{"Answer from a verified source."},
	})
	require.NoError(t, err)
	model := &taskPlannerChat{
		plan: &types.ChatResponse{Content: string(plan), FinishReason: "stop"},
		mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
			ResponseType: types.ResponseTypeAnswer, Content: "依据来源回答。", Done: true, FinishReason: "stop",
		}}}}},
	}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", "请依据实际来源回答。", nil)
	require.NoError(t, err)
	require.Equal(t, "依据来源回答。", state.FinalAnswer)
	require.NotNil(t, engine.taskProjection, "the plan must have decoded successfully")
	require.True(t, engine.taskProjection.requiresSourceEvidence)
	require.True(t, engine.taskProjection.selected[agenttools.ToolReadDocument], "the actual valid plan selected document reading")
	require.Nil(t, engine.plannedSourceDiscoverySelection(), "a source-backed plan must request conservative discovery")
	require.Equal(t, 1, knowledge.calls, "document-only tool selection must not hide knowledge source discovery")
	observedOperations := make([]string, 0, 2)
	for range 2 {
		select {
		case operation := <-operations:
			observedOperations = append(observedOperations, operation)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for structured source discovery operations")
		}
	}
	require.ElementsMatch(t, []string{"list_datasets", "describe_tools"}, observedOperations, "a source-backed plan must also discover the structured-data directory")
	require.Len(t, model.requests, 1, "reuse the existing single task-planning call")
}

func TestTaskPlanAllowsNoExternalEvidenceAndDiscoveryUsesThatChoice(t *testing.T) {
	operations := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		operations++
		_, _ = io.WriteString(w, `{}`)
	})
	plan, err := json.Marshal(taskEvidencePlan{
		Goals:              []string{"Explain the requested concepts."},
		Evidence:           []string{},
		ToolNames:          []string{},
		CompletionCriteria: []string{"Answer the requested comparison clearly."},
	})
	require.NoError(t, err)
	model := &taskPlannerChat{
		plan:     &types.ChatResponse{Content: string(plan), FinishReason: "stop"},
		mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "概念解释。", Done: true, FinishReason: "stop"}}}}},
	}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", "解释仓单和持仓的概念差异。", nil)
	require.NoError(t, err)
	require.Equal(t, "概念解释。", state.FinalAnswer)
	require.Zero(t, operations, "a valid model plan with no external evidence skips eager catalog/schema discovery")
	require.NotNil(t, engine.taskProjection)
	require.NotNil(t, engine.plannedSourceDiscoverySelection(), "explicit no-evidence plan returns an empty selection and skips both directories")
	require.Empty(t, engine.plannedSourceDiscoverySelection())
	require.Len(t, model.requests, 1, "reuse the existing single task-planning call")
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
	require.Empty(t, input["discovery_results"], "planning happens before discovery")
	require.Len(t, model.calls, 1, "normal reasoning still runs after the advisory plan")
}

func TestPlannedSourceDiscoverySelectionAndLegacyFallback(t *testing.T) {
	require.True(t, shouldPrefetchKnowledgeSources(nil), "unavailable planning keeps conservative legacy discovery")
	require.True(t, shouldPrefetchStructuredSources(nil))
	require.False(t, shouldPrefetchKnowledgeSources(map[string]bool{}), "an explicit empty model plan skips eager retrieval")
	require.False(t, shouldPrefetchStructuredSources(map[string]bool{}))
	require.True(t, shouldPrefetchKnowledgeSources(map[string]bool{agenttools.ToolSearchKnowledge: true}))
	require.True(t, shouldPrefetchStructuredSources(map[string]bool{"structured_query_series": true}))
	require.False(t, shouldPrefetchStructuredSources(map[string]bool{agenttools.ToolReadDocument: true}), "document protocols remain available without running unrelated data catalog calls")
}

func TestTaskPlanAndExpansionReuseMCPPresentationHandlesWithoutChangingRegistry(t *testing.T) {
	const serverID = "8f7a5b68-a7ab-4565-b6f3-1cae2578f040"
	const toolRef = "mcpt_actual_registered_ref"
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "discover_mcp_tools")}}
	engine, state, messages := taskPlanFixture(t, model)
	discovery := newCountingTool("discover_mcp_tools")
	discovery.BaseTool = agenttools.NewBaseTool("discover_mcp_tools", "Sources:\n"+`{"server_id":"`+serverID+`","name":"Actual service"}`, json.RawMessage(`{"type":"object","properties":{"server_id":{"type":"string","enum":["`+serverID+`"]}}}`))
	proxy := newCountingTool("call_mcp_tool")
	proxy.BaseTool = agenttools.NewBaseTool("call_mcp_tool", "Call an actually discovered tool.", json.RawMessage(`{"type":"object","properties":{"tool_ref":{"type":"string","enum":["`+toolRef+`"]}}}`))
	engine.toolRegistry.RegisterTool(discovery)
	engine.toolRegistry.RegisterTool(proxy)
	engine.planWorkbenchTask(t.Context(), state, "Use the selected service.", messages, "s")
	require.NotContains(t, model.requests[0][1].Content, serverID)
	require.Contains(t, model.requests[0][1].Content, `ms1`)
	expansion, err := engine.toolRegistry.GetTool(expandTaskToolsName)
	require.NoError(t, err)
	result, err := expansion.Execute(t.Context(), json.RawMessage(`{"names":["discover_mcp_tools","call_mcp_tool"],"reason":"Need the actual registered service capabilities."}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotContains(t, result.Output, serverID)
	require.NotContains(t, result.Output, toolRef)
	require.Contains(t, result.Output, `ms1`)
	require.Contains(t, result.Output, `mt1`)
	require.Contains(t, string(discovery.Parameters()), serverID)
	require.Contains(t, string(proxy.Parameters()), toolRef)
	calls := []types.LLMToolCall{
		{Function: types.FunctionCall{Name: "discover_mcp_tools", Arguments: `{"server_id":"ms1"}`}},
		{Function: types.FunctionCall{Name: "call_mcp_tool", Arguments: `{"tool_ref":"mt1","arguments":{}}`}},
	}
	engine.modelContext.DecodeToolCalls(calls)
	require.Contains(t, calls[0].Function.Arguments, serverID)
	require.Contains(t, calls[1].Function.Arguments, toolRef)
	require.Empty(t, calls[0].UnresolvedHandles)
	require.Empty(t, calls[1].UnresolvedHandles)
}

func TestTaskPlanExpansionAllowsDocumentDeliveryAndNewlyDiscoveredTools(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "structured_list_series")}}
	engine, state, messages := taskPlanFixture(t, model)
	engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s")
	engine.toolRegistry.RegisterTool(newCountingTool("new_authorized_delivery"))
	tool, err := engine.toolRegistry.GetTool(expandTaskToolsName)
	require.NoError(t, err)
	listing, err := tool.Execute(t.Context(), json.RawMessage(`{"names":[],"reason":"Need the actual current capability directory."}`))
	require.NoError(t, err)
	require.True(t, listing.Success)
	require.Contains(t, listing.Output, "new_authorized_delivery")
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"names":["read_file","shell_exec","new_authorized_delivery"],"reason":"The requested downloadable document needs generation and delivery."}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, listToolNames(engine.buildToolsForLLM()), "shell_exec")
	require.Contains(t, listToolNames(engine.buildToolsForLLM()), "new_authorized_delivery")
	before := listToolNames(engine.buildToolsForLLM())
	denied, err := tool.Execute(t.Context(), json.RawMessage(`{"names":["bind_document_scope","not_authorized"],"reason":"Expand evidence."}`))
	require.NoError(t, err)
	require.False(t, denied.Success)
	require.Equal(t, before, listToolNames(engine.buildToolsForLLM()), "validate the entire choice before changing projection")
	engine.toolRegistry.RegisterDeferredTool(newCountingTool("not_yet_discoverable"))
	denied, err = tool.Execute(t.Context(), json.RawMessage(`{"names":["not_yet_discoverable"],"reason":"Cannot skip ordinary discovery."}`))
	require.NoError(t, err)
	require.False(t, denied.Success, "projection does not bypass deferred capability discovery")
}

func TestTaskPlanSteeringRestoresFullToolsBeforeTheNextModelCall(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "structured_list_series")}}
	model.responses = []mockResponse{{chunks: []types.StreamResponse{{Content: "Completed according to the latest request.", Done: true, FinishReason: "stop"}}}}
	engine, state, messages := taskPlanFixture(t, model)
	messages = engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s")
	state.RoundSteps = nil // Isolate steering mechanics from source review.
	engine.SetSteerSink(&fakeSteerSink{queued: []map[string]interface{}{steerEntry("new-message", "Also generate a downloadable document.")}})
	_, err := engine.executeLoop(t.Context(), state, "Current request", messages, engine.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Nil(t, engine.taskProjection)
	require.Empty(t, engine.taskPlanContext)
	require.Contains(t, listToolNames(model.opts[0].Tools), "shell_exec")
	for _, message := range model.calls[0] {
		require.NotContains(t, message.Content, "advisory_task_plan")
	}
}

func TestTaskPlanCannotBecomeNumericEvidenceAndDefaultEnginesSkipIt(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: strings.Replace(validTaskPlan(t, "read_document"), "Deliver the requested structure.", "Unverified value 987654.", 1)}}
	engine, state, messages := taskPlanFixture(t, model)
	planned := engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s")
	withActualEvidence := append(append([]chat.Message(nil), planned...), chat.Message{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":3}`})
	require.Contains(t, ungroundedStructuredNumbers("The result is 987654.", withActualEvidence, state.RoundSteps), "987654")
	require.False(t, hasStructuredEvidence(state.RoundSteps))
	engine.config.TaskPlanningEnabled = false
	engine.prepareTaskPlanning()
	before := len(model.requests)
	require.Equal(t, messages, engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s"))
	require.Len(t, model.requests, before)
	require.Nil(t, engine.taskProjection)
}

func TestTaskPlanKeepsActualSourceProtocolsWithoutRestoringDeferredCapabilities(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "calculate_document_table")}}
	engine, state, messages := taskPlanFixture(t, model)
	for _, name := range []string{agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments, agenttools.ToolStructuredData} {
		engine.toolRegistry.RegisterTool(newCountingTool(name))
	}
	engine.toolRegistry.RegisterDeferredTool(newCountingTool("not_discovered"))
	engine.planWorkbenchTask(t.Context(), state, "Use the selected actual source.", messages, "s")
	names := listToolNames(engine.buildToolsForLLM())
	require.ElementsMatch(t, []string{expandTaskToolsName, documentTableAnalysisToolName, "calculate_document_table", "structured_list_series", agenttools.ToolSearchKnowledge,
		agenttools.ToolReadDocument, agenttools.ToolListDocuments, agenttools.ToolBindDocumentScope, agenttools.ToolStructuredData}, names)
	require.NotContains(t, names, "shell_exec")
	require.NotContains(t, names, "not_discovered")
}

func TestTaskPlanSourceViewExcludesDuplicateCapabilitySchemas(t *testing.T) {
	model := &taskPlannerChat{plan: &types.ChatResponse{Content: validTaskPlan(t, "read_document")}}
	engine, state, messages := taskPlanFixture(t, model)
	state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls,
		types.ToolCall{ID: "schemas", Name: agenttools.ToolStructuredData, Args: map[string]any{"operation": "describe_tools"},
			Result: &types.ToolResult{Success: true, Output: `{"tools":[{"description":"` + strings.Repeat("schema detail ", 10000) + `"}]}`}},
		types.ToolCall{ID: "actual-document", Name: agenttools.ToolSearchKnowledge,
			Result: &types.ToolResult{Success: true, Output: `{"documents":[{"file_name":"actual-selected-source.pdf"}]}`}})
	engine.planWorkbenchTask(t.Context(), state, "Current request", messages, "s")
	require.Len(t, model.requests, 1)
	var input struct {
		Sources []reviewToolEvidence `json:"discovery_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
	require.Len(t, input.Sources, 2)
	require.Contains(t, model.requests[0][1].Content, "actual-selected-source.pdf")
	require.NotContains(t, model.requests[0][1].Content, "schema detail")
}
