package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

// An agent explicitly configured to use the field protocol runs that protocol
// directly. Outer chat models cannot skip file evidence, manufacture numbers,
// or bypass a terminal failure with hand-selected raw-series queries.
func (e *AgentEngine) executeStructuredQuery(ctx context.Context, state *types.AgentState, query, sessionID, messageID string) bool {
	prompt := e.config.SystemPrompt
	if !e.config.UseCustomSystemPrompt || !strings.Contains(prompt, "resolve_fields") || !strings.Contains(prompt, "confirm_fields") {
		return false
	}
	started := time.Now()
	turn, _ := ctx.Value(fieldConfirmationTurnKey{}).(*fieldConfirmationTurn)
	if turn == nil {
		turn = &fieldConfirmationTurn{question: query}
	}
	name, err := agenttools.StructuredResolveTool(ctx, e.toolRegistry)
	args := turn.resolveArgs()
	// Forward the exact message plus trusted previous task identity. The task
	// planner decides continuation/correction/new topic; keywords cannot do so.
	state.FinalAnswer = "结构化查询未完成，尚未取得可核验的结果。"
	if err == nil {
		body, _ := json.Marshal(args)
		call := e.runToolCall(ctx, types.LLMToolCall{ID: generateEventID("field-query"), Type: "function", Function: types.FunctionCall{Name: name, Arguments: string(body)}}, 0, 0, 1, sessionID, messageID)
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: 0, Timestamp: started, ToolCalls: []types.ToolCall{call}})
		e.emitToolOutcome(ctx, call, 0, sessionID)
		if call.Result != nil {
			data := structuredOutcome(call.Result.Output)
			if message, ok := data["message"].(string); ok {
				state.FinalAnswer = "结构化查询未完成：" + message
			}
			if question, ok := data["question"].(string); ok && data["status"] == "needs_clarification" {
				state.FinalAnswer = question
			}
			if answer, ok := data["answer"].(map[string]any); ok {
				if summary, ok := answer["summary"].(string); ok && summary != "" {
					state.FinalAnswer = summary
				} else if notice, ok := answer["notice"].(string); ok {
					state.FinalAnswer = notice
				} else {
					state.FinalAnswer = "请查看原表查询结果与来源。"
				}
			}
			if !call.Result.Success {
				state.FinalAnswer = "结构化查询未完成：" + call.Result.Error
			}
		}
	} else {
		state.FinalAnswer = "结构化查询未完成：" + err.Error()
	}
	state.IsComplete = true
	e.eventBus.Emit(ctx, event.Event{ID: generateEventID("answer"), Type: event.EventAgentFinalAnswer, SessionID: sessionID, Data: event.AgentFinalAnswerData{Content: state.FinalAnswer, Done: true}})
	e.emitCompletionEvent(ctx, state, sessionID, messageID, started)
	return true
}
