package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Negotiate capabilities before planning, independently of source discovery.
// The fixed service has a short protocol budget; its failure cannot make an
// unrelated task wait on the ordinary 30-second data-request timeout.
func (e *AgentEngine) prepareWorkbenchCapabilities(ctx context.Context, state *types.AgentState, messages []chat.Message, query, sessionID, messageID string) []chat.Message {
	if e.toolRegistry == nil || state == nil {
		return messages
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.ID == fmt.Sprintf("source-capabilities-%s", messageID) && call.Name == agenttools.ToolStructuredCapabilities || call.ID == fmt.Sprintf("source-schemas-%s", messageID) && call.Name == agenttools.ToolStructuredData && call.Args["operation"] == "describe_tools" {
				return messages
			}
		}
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() || !e.toolRegistry.PrepareStructuredProtocol() {
		return messages
	}
	baseTool, _ := e.toolRegistry.GetTool(agenttools.ToolStructuredData)
	base := baseTool.(*agenttools.StructuredDataTool)
	base.BindSourceConversation(query, messages)
	step := types.AgentStep{Iteration: -1, Timestamp: time.Now()}
	if cached, ok := e.toolRegistry.BeginStructuredProtocolTurn(ctx); ok {
		step.ToolCalls = []types.ToolCall{{ID: fmt.Sprintf("source-capabilities-%s", messageID), Name: agenttools.ToolStructuredCapabilities, Args: map[string]any{"refresh": false}, Result: cached}}
	} else {
		protocolCtx, cancel := context.WithTimeout(ctx, agenttools.StructuredManifestTimeout)
		call := e.runToolCall(protocolCtx, types.LLMToolCall{ID: fmt.Sprintf("source-capabilities-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredCapabilities, Arguments: `{"refresh":true}`}}, 0, -1, 0, sessionID, messageID)
		cancel()
		step.ToolCalls = []types.ToolCall{call}
	}
	state.RoundSteps = append(state.RoundSteps, step)
	return e.appendToolResults(messages, step)
}

// Discover files before the first reasoning call. This is authorized source
// metadata, never an inferred answer or an unrestricted database connection.
// The normal execution boundary records the actual call and its failure too;
// an unavailable source must not look like an empty catalogue.
func (e *AgentEngine) discoverWorkbenchSources(ctx context.Context, state *types.AgentState, messages []chat.Message, query, sessionID, messageID string, planned ...map[string]bool) []chat.Message {
	if !e.config.WorkbenchDefault || e.toolRegistry == nil {
		return messages
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" {
		return messages
	}
	messages = e.prepareWorkbenchCapabilities(ctx, state, messages, query, sessionID, messageID)
	// The real QA model plans sources from the complete conversation and
	// authorized metadata; code validates the explicit plan before content reads.
	agenttools.PrepareDocumentScope(e.toolRegistry)
	selection := map[string]bool(nil)
	if len(planned) > 0 {
		selection = planned[0]
	}
	discoveryQuery := e.plannedSourceDiscoveryQuery(query)
	requests := []types.LLMToolCall{}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			base.BindSourceConversation(query, messages)
		}
		if shouldPrefetchStructuredSources(selection) {
			catalogArgs, _ := json.Marshal(map[string]string{"question": discoveryQuery})
			// The operation comes from a current registered native descriptor,
			// never from a constructed name or an unnegotiated legacy wrapper.
			for _, definition := range e.toolRegistry.GetModelFunctionDefinitions() {
				descriptor, _, native := e.toolRegistry.StructuredInvocation(definition.Name, catalogArgs)
				if native && descriptor.Operation == "list_datasets" && descriptor.ToolName == definition.Name && descriptor.Generation > 0 && descriptor.SchemaSHA256 != "" {
					requests = append(requests, types.LLMToolCall{ID: fmt.Sprintf("source-catalog-%s", messageID), Function: types.FunctionCall{Name: descriptor.ToolName, Arguments: string(catalogArgs)}})
					break
				}
			}
		}
	}
	if _, err := e.toolRegistry.GetTool(agenttools.ToolSearchKnowledge); err == nil && shouldPrefetchKnowledgeSources(selection) {
		args, _ := json.Marshal(map[string]string{"query": discoveryQuery})
		requests = append(requests, types.LLMToolCall{ID: fmt.Sprintf("source-knowledge-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolSearchKnowledge, Arguments: string(args)}})
	}
	if len(requests) == 0 {
		return messages
	}
	e.emitProgress(ctx, "sources", -1, sessionID)
	step := types.AgentStep{Iteration: -1, Timestamp: time.Now()}
	response := &types.ChatResponse{ToolCalls: requests}
	// These calls only discover authorized sources and schema; their independent
	// providers can run concurrently without executing a numeric query or write.
	e.executeToolCallsParallel(ctx, response, &step, -1, sessionID, messageID)
	if len(state.RoundSteps) > 0 && state.RoundSteps[len(state.RoundSteps)-1].Iteration == -1 {
		state.RoundSteps[len(state.RoundSteps)-1].ToolCalls = append(state.RoundSteps[len(state.RoundSteps)-1].ToolCalls, step.ToolCalls...)
	} else {
		state.RoundSteps = append(state.RoundSteps, step)
	}
	return e.appendToolResults(messages, compactWorkbenchDiscovery(step, discoveryQuery, nil))
}

func shouldPrefetchKnowledgeSources(selected map[string]bool) bool {
	if selected == nil {
		return true
	}
	return selected[agenttools.ToolSearchKnowledge]
}

func shouldPrefetchStructuredSources(selected map[string]bool) bool {
	if selected == nil {
		return true
	}
	if selected[agenttools.ToolStructuredData] {
		return true
	}
	for name := range selected {
		if strings.HasPrefix(name, "structured_") {
			return true
		}
	}
	return false
}

// Keep the actual authorized calls and their complete evidence in RoundSteps.
// Only duplicate schemas already present in the function definitions are
// omitted from the model-facing discovery view. Query results, literal
// field candidates, source context and all coverage evidence remain untouched.
func compactWorkbenchDiscovery(step types.AgentStep, query string, registered map[string]string) types.AgentStep {
	modelStep := step
	modelStep.ToolCalls = append([]types.ToolCall(nil), step.ToolCalls...)
	for i, call := range modelStep.ToolCalls {
		if call.Result == nil || !call.Result.Success || call.Result.Data == nil {
			continue
		}
		operation, structured := agenttools.StructuredOperationFromCall(call)
		if !structured {
			continue
		}
		var compact map[string]any
		switch operation {
		case "list_datasets":
			compact = compactDiscoveryCatalogue(call.Result.Data, query)
		case "describe_tools":
			compact = compactDiscoverySchemas(call.Result.Data, registered)
		}
		if compact == nil {
			continue
		}
		output, err := json.Marshal(compact)
		if err != nil {
			continue
		}
		result := *call.Result
		result.Data, result.Output = compact, string(output)
		modelStep.ToolCalls[i].Result = &result
	}
	return modelStep
}

func copyDiscoveryMap(original map[string]any) map[string]any {
	copy := make(map[string]any, len(original)+2)
	for key, value := range original {
		copy[key] = value
	}
	return copy
}

func compactDiscoveryCatalogue(data map[string]any, _ string) map[string]any {
	// Every actual source/coverage remains visible regardless of question
	// wording. Literal matches can add evidence, never remove source metadata.
	for _, key := range []string{"literal_fields", "literal_field_matches"} {
		if fields, ok := data[key].([]any); ok && len(fields) > 0 {
			compact := copyDiscoveryMap(data)
			compact["literal_fields_query_hint"] = "这些是实际目录的召回证据，包含真实ID、表头和公式来源。模型根据完整问题与上下文选择来源并生成计划；候选截断时继续补齐，不能将字面命中视为已明确口径。"
			return compact
		}
	}
	return nil
}

func compactDiscoverySchemas(data map[string]any, registered map[string]string) map[string]any {
	directory, ok := data["tools"].([]any)
	if !ok || len(registered) == 0 {
		return nil
	}
	rows := make([]any, 0, len(directory))
	changed := false
	for _, value := range directory {
		entry, ok := value.(map[string]any)
		if !ok {
			rows = append(rows, value)
			continue
		}
		name, _ := entry["name"].(string)
		toolName, found := registered[name]
		if !found {
			rows = append(rows, value)
			continue
		}
		row := copyDiscoveryMap(entry)
		delete(row, "inputSchema")
		delete(row, "description")
		row["typed_tool"] = toolName
		row["schema_in_function_definition"] = true
		rows = append(rows, row)
		changed = true
	}
	if !changed {
		return nil
	}
	compact := copyDiscoveryMap(data)
	compact["tools"] = rows
	compact["schema_notice"] = "与原生工具定义一致的参数schema与说明不再重复；未注册或与保留定义不同的操作，其本次原schema保持完整。"
	return compact
}
