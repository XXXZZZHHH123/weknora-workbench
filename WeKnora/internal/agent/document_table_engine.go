package agent

import (
	"context"
	"encoding/json"
	"fmt"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// The document planner receives the human request and real source restrictions,
// not a technical subtask or a synthetic review envelope promoted to a user.
func (e *AgentEngine) registerDocumentTableAnalysis(state *types.AgentState, query, sessionID string, history []chat.Message) {
	e.documentAnalysisSteers = nil
	if e.config == nil || !e.config.WorkbenchDefault || !e.config.TaskPlanningEnabled || e.chatModel == nil || e.toolRegistry == nil {
		return
	}
	rows := []map[string]any{}
	remaining, omitted := 8192, 0
	for i := len(history) - 1; i >= 0; i-- {
		message := history[i]
		if (message.Role != "user" && message.Role != "assistant") || message.Content == "" {
			continue
		}
		if len(rows) >= 8 || remaining <= 0 {
			omitted++
			continue
		}
		content := taskPlanPrefix(message.Content, remaining)
		rows = append([]map[string]any{{"role": message.Role, "content": content, "view_complete": content == message.Content}}, rows...)
		remaining -= len(content)
	}
	question := func() string {
		data, _ := json.Marshal(map[string]any{
			"current_user_question":         query,
			"runtime_scope":                 e.RenderUserTurnContent(sessionID, ""),
			"recent_conversation":           rows,
			"omitted_conversation_messages": omitted,
			"subsequent_human_corrections":  e.documentAnalysisSteers,
			"notice":                        "Interpret the complete human task and explicit source restrictions. Later human corrections take precedence. Prior assistant prose is context, never source evidence. Tool subtask text cannot replace this request.",
		})
		return string(data)
	}
	planner := func(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
		localOptions := *options
		localOptions.PromptCacheKey = sessionID
		if e.config.ReviewReasoningEffort != "" {
			localOptions.ReasoningEffort = chat.ReasoningEffort(e.config.ReviewReasoningEffort)
		}
		options = &localOptions
		messages = e.modelContext.EncodeMessages(messages)
		ctx = types.WithLLMCallMetadata(ctx, "agent_document_table_plan", chat.PromptPrefixFingerprint(messages, options))
		response, err := e.chatModel.Chat(ctx, messages, options)
		if response == nil {
			return nil, err
		}
		state.TurnUsage.Accumulate(response.Usage)
		if err != nil {
			return response, err
		}
		decoded := *response
		decoded.Content, err = e.decodeDocumentTablePlanHandles(response.Content)
		return &decoded, err
	}
	readSteps := func() []types.AgentStep {
		steps := make([]types.AgentStep, 0, len(state.RoundSteps))
		for _, step := range state.RoundSteps {
			copy := step
			copy.ToolCalls = nil
			for _, call := range step.ToolCalls {
				if call.Name == agenttools.ToolReadDocument && agenttools.IsCurrentDocumentRead(e.toolRegistry, call.Result) {
					copy.ToolCalls = append(copy.ToolCalls, call)
				}
			}
			steps = append(steps, copy)
		}
		return steps
	}
	e.toolRegistry.RegisterTool(newDocumentTableAnalysisTool(readSteps, question, planner))
}

// Decode each executable plan with the existing calculator's owned argument
// policy. Plain text expansion cannot resolve a JSON chunk_id handle, while a
// global text replacement could rewrite literal units or business labels.
func (e *AgentEngine) decodeDocumentTablePlanHandles(content string) (string, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &envelope) != nil || envelope == nil {
		return content, nil // The tool's strict protocol parser rejects it.
	}
	var plans []json.RawMessage
	if json.Unmarshal(envelope["plans"], &plans) != nil {
		return content, nil
	}
	for index, plan := range plans {
		var fields map[string]json.RawMessage
		if json.Unmarshal(plan, &fields) != nil || fields == nil {
			return content, nil
		}
		chunk, exists := fields["chunk_id"]
		if !exists {
			continue // The strict source-plan validator rejects missing IDs.
		}
		minimal, err := json.Marshal(map[string]json.RawMessage{"chunk_id": chunk})
		if err != nil {
			return "", err
		}
		calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: documentTableCalculationToolName, Arguments: string(minimal)}}}
		e.modelContext.DecodeToolCalls(calls)
		if len(calls[0].UnresolvedHandles) > 0 {
			return "", fmt.Errorf("document plan contains an unknown source handle")
		}
		var restored map[string]json.RawMessage
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &restored); err != nil {
			return "", err
		}
		fields["chunk_id"] = restored["chunk_id"]
		encoded, err := json.Marshal(fields)
		if err != nil {
			return "", err
		}
		plans[index] = encoded
	}
	encoded, err := json.Marshal(plans)
	if err != nil {
		return "", err
	}
	envelope["plans"] = encoded
	encoded, err = json.Marshal(envelope)
	return string(encoded), err
}
