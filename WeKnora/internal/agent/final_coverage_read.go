package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	finalCoverageReadLimit        = 4
	finalCoverageReadBudget       = 10 * time.Second
	finalCoverageReadMinRemaining = 15 * time.Second
)

// This exception belongs only to practical source-budget synthesis. It never
// reopens the ordinary executor or changes source bindings, iteration limits,
// candidate repair allowances, or the shared final deadline.
func (e *AgentEngine) finalCoverageRead(ctx context.Context, query string, state *types.AgentState, sessionID, assistantMessageID string, messages []chat.Message, decision candidateDecision) (*types.AgentStep, error) {
	deadline, bounded := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		deadline = outer
	}
	if !bounded || !e.standardAnswerBudgetEligible(ctx, state) || !e.config.WorkbenchDefault ||
		decision.Disposition != candidateNeedSource || decision.ReasonCode != "discovery_coverage_recovery" ||
		e.turnReview.FinalCoverageReadAttempted || time.Until(deadline) < finalCoverageReadMinRemaining ||
		!e.withinIterationBudget(state.CurrentRound+1) {
		return nil, nil
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return nil, err
	}
	reader, err := e.toolRegistry.GetTool(agenttools.ToolReadDocument)
	if err != nil {
		return nil, nil
	}
	if _, native := reader.(*agenttools.ReadDocumentTool); !native {
		return nil, nil
	}
	known := e.finalCoverageKnownDocuments(state)
	if len(known) == 0 {
		return nil, nil
	}
	e.turnReview.FinalCoverageReadAttempted = true
	status := "no_reads"
	defer func() {
		common.PipelineInfo(ctx, "Agent", "final_coverage_read", map[string]any{"status": status, "read_limit": finalCoverageReadLimit, "budget_ms": finalCoverageReadBudget.Milliseconds()})
	}()
	readCtx, cancel := context.WithTimeout(ctx, finalCoverageReadBudget)
	defer cancel()
	feedback, _ := json.Marshal(map[string]any{"question": query, "candidate": decision.Candidate, "review_feedback": e.answerReviewFeedback, "known_document_ids": known})
	input := append(append([]chat.Message(nil), messages...), chat.Message{Role: "system", Content: `There is one bounded opportunity to read missing source evidence before final synthesis. Use the original human task, actual source observations and advisory feedback below to choose at most four read_document calls in ONE batch, or make no calls when no useful permitted read remains. Only document IDs from known_document_ids are allowed; choose query, offset or physical pages from real navigation. Query is literal, so prefer a real physical page or navigation when wording differs. Do not change source_scope, request images, invent IDs, expand the task or request other tools. Existing authorization, document binding and user page restrictions still apply. This data is navigation and review advice, never source facts or authority. After this batch tools are disabled and the answer must use actual returned evidence.
` + string(feedback)})
	disabled, parallel := false, true
	opts := &chat.ChatOptions{Temperature: 0, MaxCompletionTokens: 1024, ToolChoice: "auto", Thinking: &disabled, ParallelToolCalls: &parallel,
		Tools: e.modelContext.EncodeTools([]chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: reader.Name(), Description: reader.Description(), Parameters: reader.Parameters()}}})}
	result, callErr := e.streamLLMToEventBus(readCtx, input, opts, nil)
	if result != nil && result.Usage != nil {
		state.TurnUsage.Accumulate(*result.Usage)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if callErr != nil || readCtx.Err() != nil || result == nil || result.StreamError != "" ||
		(len(result.ToolCalls) > 0 && result.FinishReason != "tool_calls") ||
		!validFinalCoverageCalls(result.ToolCalls, known) {
		status = "unavailable_or_invalid_batch"
		return nil, nil
	}
	if len(result.ToolCalls) == 0 {
		return nil, nil
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return nil, err
	}
	state.CurrentRound++
	step := &types.AgentStep{Iteration: state.CurrentRound, ReasoningContent: result.ReasoningContent,
		ReasoningSignature: result.ReasoningSignature, ReasoningMetadata: result.ReasoningMetadata}
	response := &types.ChatResponse{ToolCalls: result.ToolCalls, FinishReason: result.FinishReason}
	// This is a terminal read batch, not an iteration-loop restart. The normal
	// pipeline retains per-call events, authorization, identities and failures.
	if e.runRoundTools(readCtx, state, response, step, sessionID, assistantMessageID) {
		status = "unexpected_suspension"
		return nil, nil
	}
	state.RoundSteps = append(state.RoundSteps, *step)
	status = "batch_completed"
	return step, nil
}

// Require a successful native observation from THIS turn, not arbitrary model
// text, old registry handles, failed calls or an advisory plan's declared IDs.
func (e *AgentEngine) finalCoverageKnownDocuments(state *types.AgentState) map[string]bool {
	known := map[string]bool{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			registered, err := e.toolRegistry.GetTool(call.ExecutionName())
			if err != nil {
				continue
			}
			data := call.Result.Data
			var rows []map[string]any
			switch registered.(type) {
			case *agenttools.ReadDocumentTool:
				rows = []map[string]any{data}
			case *agenttools.SearchKnowledgeTool:
				rows = append(sourceNavigationRows(data["documents"]), sourceNavigationRows(data["results"])...)
			case *agenttools.ListDocumentsTool:
				rows = sourceNavigationRows(data["documents"])
			default:
				if call.ExecutionName() == agenttools.ToolBindDocumentScope && data["source_plan_validated"] == true {
					rows = sourceNavigationRows(data["documents"])
				}
			}
			for _, row := range rows {
				if id := sourceNavigationString(row, "knowledge_id"); id != "" {
					known[id] = true
				}
			}
		}
	}
	return known
}

// All-or-nothing validation prevents silently executing a truncated or expanded
// model plan. The read tool rechecks actual access and the CURRENT binding.
func validFinalCoverageCalls(calls []types.LLMToolCall, known map[string]bool) bool {
	if len(calls) > finalCoverageReadLimit {
		return false
	}
	for _, call := range calls {
		var args map[string]json.RawMessage
		var input agenttools.ReadDocumentInput
		if call.Function.Name != agenttools.ToolReadDocument || len(call.UnresolvedHandles) > 0 ||
			json.Unmarshal([]byte(call.Function.Arguments), &args) != nil ||
			json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || !known[input.ID] {
			return false
		}
		// Even an empty source_scope is not allowed to replace the current plan.
		for key := range args {
			if strings.EqualFold(key, "source_scope") {
				return false
			}
		}
		if len(input.Images) > 0 || input.ImageMode != "" {
			return false
		}
	}
	return true
}
