package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Discover files before the first reasoning call. This is authorized source
// metadata, never an inferred answer or an unrestricted database connection.
// The normal execution boundary records the actual call and its failure too;
// an unavailable source must not look like an empty catalogue.
func (e *AgentEngine) discoverWorkbenchSources(ctx context.Context, state *types.AgentState, messages []chat.Message, query, sessionID, messageID string) []chat.Message {
	if !e.config.WorkbenchDefault || e.toolRegistry == nil {
		return messages
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" {
		return messages
	}
	requests := []types.LLMToolCall{}
	if _, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		requests = append(requests,
			types.LLMToolCall{ID: fmt.Sprintf("source-catalog-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: `{"operation":"list_datasets","arguments":{}}`}},
			types.LLMToolCall{ID: fmt.Sprintf("source-schemas-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: `{"operation":"describe_tools","arguments":{"operations":["list_series","query_series","analyze_table","inspect_source","get_result_evidence"]}}`}},
		)
	}
	if _, err := e.toolRegistry.GetTool(agenttools.ToolSearchKnowledge); err == nil {
		args, _ := json.Marshal(map[string]string{"query": query})
		requests = append(requests, types.LLMToolCall{ID: fmt.Sprintf("source-knowledge-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolSearchKnowledge, Arguments: string(args)}})
	}
	if len(requests) == 0 {
		return messages
	}
	step := types.AgentStep{Iteration: -1, Timestamp: time.Now()}
	response := &types.ChatResponse{ToolCalls: requests}
	// These calls only discover authorized sources and schema; their independent
	// providers can run concurrently without executing a numeric query or write.
	e.executeToolCallsParallel(ctx, response, &step, -1, sessionID, messageID)
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			for _, call := range step.ToolCalls {
				if call.Args["operation"] == "describe_tools" && call.Result != nil && call.Result.Success {
					for _, operation := range agenttools.NewStructuredReadOperationTools(base, call.Result.Data) {
						e.toolRegistry.RegisterTool(operation)
					}
				}
			}
		}
	}
	state.RoundSteps = append(state.RoundSteps, step)
	return e.appendToolResults(messages, step)
}
