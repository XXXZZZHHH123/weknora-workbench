package agent

import (
	"context"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

// Stop unproductive result preparation without discarding already executed
// evidence. This is one terminal, tool-free synthesis under the ordinary
// numeric, citation and source checks, never approval of an unchecked draft.
// Pure document stalls retain their existing no-extra-model-call boundary.
func (e *AgentEngine) finishResultWorkflowStall(ctx context.Context, state *types.AgentState, query, sessionID string, messages []chat.Message) (bool, error) {
	if !hasResultWorkflowEvidence(state.RoundSteps) {
		return false, nil
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return true, err
	}
	common.PipelineInfo(ctx, "Agent", "result_workflow_stalled", map[string]any{"round": state.CurrentRound})
	bounded, cancel := context.WithTimeout(ctx, standardAnswerFinalBudget)
	defer cancel()
	err := e.streamFinalAnswerToEventBus(bounded, query, state, sessionID, messages)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		e.commitTerminalResult(ctx, state, sessionID, sourceProgressStalledFallback, "", "stalled", false)
	}
	return true, err
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
