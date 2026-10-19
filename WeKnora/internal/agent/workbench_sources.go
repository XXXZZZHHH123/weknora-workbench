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
	// The real QA model plans sources from the complete conversation and
	// authorized metadata; code validates the explicit plan before content reads.
	agenttools.PrepareDocumentScope(e.toolRegistry)
	selection := map[string]bool(nil)
	if len(planned) > 0 {
		selection = planned[0]
	}
	requests := []types.LLMToolCall{}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			base.BindSourceConversation(query, messages)
		}
		if shouldPrefetchStructuredSources(selection) {
			catalogArgs, _ := json.Marshal(map[string]any{"operation": "list_datasets", "arguments": map[string]string{"question": query}})
			requests = append(requests,
				types.LLMToolCall{ID: fmt.Sprintf("source-catalog-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: string(catalogArgs)}},
				types.LLMToolCall{ID: fmt.Sprintf("source-schemas-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: `{"operation":"describe_tools","arguments":{"operations":["list_series","query_series","analyze_table","inspect_source","get_result_evidence"]}}`}},
			)
		}
	}
	if _, err := e.toolRegistry.GetTool(agenttools.ToolSearchKnowledge); err == nil && shouldPrefetchKnowledgeSources(selection) {
		args, _ := json.Marshal(map[string]string{"query": query})
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
	registered := map[string]string{}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			for _, call := range step.ToolCalls {
				if call.Args["operation"] == "describe_tools" && call.Result != nil && call.Result.Success {
					for _, operation := range agenttools.NewStructuredReadOperationTools(base, call.Result.Data) {
						e.toolRegistry.RegisterTool(operation)
						// Registration is first-wins. A retained older contract
						// must not make a changed service schema disappear.
						current, err := e.toolRegistry.GetTool(operation.Name())
						if err == nil && string(current.Parameters()) == string(operation.Parameters()) && current.Description() == operation.Description() {
							registered[strings.TrimPrefix(operation.Name(), "structured_")] = operation.Name()
						}
					}
				}
			}
		}
	}
	state.RoundSteps = append(state.RoundSteps, step)
	return e.appendToolResults(messages, compactWorkbenchDiscovery(step, query, registered))
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
		if call.Name != agenttools.ToolStructuredData || call.Result == nil || !call.Result.Success || call.Result.Data == nil {
			continue
		}
		var compact map[string]any
		switch call.Args["operation"] {
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
