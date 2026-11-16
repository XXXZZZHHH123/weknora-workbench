package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

const toolStopSynthesisBudget = 60 * time.Second

type toolStopSynthesisDeadlineKey struct{}
type toolStopReasonKey struct{}

// Stop unproductive result preparation without discarding already executed
// evidence. This is one terminal, tool-free synthesis under the ordinary
// numeric, citation and source checks, never approval of an unchecked draft.
// Pure document stalls retain their existing no-extra-model-call boundary.
func (e *AgentEngine) finishResultWorkflowStall(ctx context.Context, state *types.AgentState, query, sessionID string, messages []chat.Message, reasons ...string) (bool, error) {
	if !hasResultWorkflowEvidence(state.RoundSteps) {
		return false, nil
	}
	common.PipelineInfo(ctx, "Agent", "result_workflow_stalled", map[string]any{"round": state.CurrentRound})
	reason := "Repeated result preparation did not produce new evidence."
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return true, e.finishBoundedResultAnswer(ctx, query, state, sessionID, messages, reason, standardAnswerFinalBudget, sourceProgressStalledFallback)
}

// Both result-workflow stalls and exhausted iterations use this single no-tool
// synthesis and ordinary candidate validation. Keep the parent context live so
// a child deadline can still publish the existing bounded failure response.
func (e *AgentEngine) finishBoundedResultAnswer(
	ctx context.Context, query string, state *types.AgentState, sessionID string, messages []chat.Message,
	reason string, budget time.Duration, fallback string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, toolStopReasonKey{}, reason)
	deadline := time.Now().Add(budget)
	if existing, ok := ctx.Value(toolStopSynthesisDeadlineKey{}).(time.Time); ok && existing.Before(deadline) {
		deadline = existing
	}
	finalCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := e.streamFinalAnswerToEventBus(finalCtx, query, state, sessionID, messages, finalAnswerOptions{stopReason: reason}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.commitTerminalResult(ctx, state, sessionID, fallback, "", "failed", false)
		return fmt.Errorf("failed to synthesize final answer after tool execution stopped: %w", err)
	}
	if strings.TrimSpace(state.FinalAnswer) == "" {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.commitTerminalResult(ctx, state, sessionID, fallback, "", "failed", false)
		return fmt.Errorf("final synthesis ended without a committed answer")
	}
	state.IsComplete = true
	return nil
}

func hasResultWorkflowEvidence(steps []types.AgentStep) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			if len(executedDocumentTableResults(call)) > 0 {
				return true
			}
			operation, native := agenttools.StructuredOperationFromCall(call)
			if !native || !structuredProgressHasResults(operation) {
				continue
			}
			contract, err := structuredresult.ValidatePayload(call.Result.Data)
			if err == nil && contract != nil {
				return true
			}
		}
	}
	return false
}
