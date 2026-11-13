package agent

import (
	"context"
	"encoding/json"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// This is a review cadence, not a failure threshold or a retrieval workflow.
const sourceProgressReviewBatches = 3

func (e *AgentEngine) sourceProgressOperation(call types.ToolCall) (string, bool) {
	if e.toolRegistry == nil || call.Result == nil {
		return "", false
	}
	if descriptor, ok := call.Result.Data["_structured_execution"].(agenttools.StructuredOperationDescriptor); ok {
		raw, err := json.Marshal(call.ExecutionArgs())
		if err != nil {
			return "", false
		}
		actual, _, native := e.toolRegistry.StructuredInvocation(call.ExecutionName(), raw)
		if !native || actual.Operation != descriptor.Operation || actual.EffectClass != descriptor.EffectClass || (actual.EffectClass != "read" && actual.Operation != "resolve_fields") || call.Result.Data["execution_started"] == false {
			return "", false
		}
		return descriptor.Operation, true
	}
	registered, err := e.toolRegistry.GetTool(call.ExecutionName())
	if err != nil {
		return "", false
	}
	switch registered.(type) {
	case *agenttools.ReadDocumentTool, *agenttools.SearchKnowledgeTool, *agenttools.ListDocumentsTool:
		return call.ExecutionName(), true
	}
	return "", false
}

func (e *AgentEngine) sourceExecutionProgress(state *types.AgentState) (int, map[string]any) {
	batches, calls, omitted := 0, 0, 0
	var recent []map[string]any
	if state == nil {
		return 0, nil
	}
	for _, step := range state.RoundSteps {
		batch := false
		for _, call := range step.ToolCalls {
			operation, native := e.sourceProgressOperation(call)
			if !native {
				continue
			}
			batch, calls = true, calls+1
			summary := discoveryReadFields(call.Result.Data, "status", "kind", "reason_code", "retryable", "source_data_read", "catalog_data_read", "pending_sheets")
			summary["operation"], summary["success"] = operation, call.Result.Success
			for key, value := range discoveryReadFields(map[string]any{"arguments": call.ExecutionArgs(), "error": call.Result.Error}, "arguments", "error") {
				summary[key] = value
			}
			recent = append(recent, summary)
			if len(recent) > 8 {
				recent = recent[1:]
				omitted++
			}
		}
		if batch {
			batches++
		}
	}
	return batches, map[string]any{"source_batches": batches, "source_calls": calls, "recent_source_operations": recent, "omitted_operations": omitted, "cadence_is_not_failure": true}
}

// Run after actual tool results have entered the transcript, before waiting for
// a final draft that might never arrive. This spends the SAME once-turn review
// receipt as candidate delivery. Keep/error never grants candidate approval.
func (e *AgentEngine) reviewSourceProgress(ctx context.Context, query string, state *types.AgentState, messages *[]chat.Message, guards *loopGuards, sessionID string) {
	if ctx.Err() != nil || state == nil || messages == nil || guards == nil || e.taskPlan == nil || !e.standardTextDelivery(state) || e.turnReview.DiscoveryReview != nil || e.turnReview.Repairs >= maxTurnCandidateRepairs || guards.evidenceRecoveryAttempts >= maxEvidenceRecoveryAttempts {
		return
	}
	batches, progress := e.sourceExecutionProgress(state)
	if batches < sourceProgressReviewBatches {
		return
	}
	decision := candidateDecision{Disposition: candidateAccepted, TaskHash: e.candidateTaskDigest(query, *messages), EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, *messages)), SourceScopeHash: e.unverifiedSourceScopeDigest(ctx)}
	decision = e.checkDiscoveryDeliveryPhase(ctx, query, decision, state, *messages, "progress", progress)
	if decision.Disposition != candidateNeedSource || ctx.Err() != nil || !e.spendCandidateRepair() {
		return
	}
	// Preserve the real transcript, original task, current iteration and all
	// ordinary execution/quality guards. Only the model chooses the next tools.
	e.resumeEvidenceCollection(ctx, state, messages, guards, sessionID)
	common.PipelineInfo(ctx, "Agent", "source_progress_recovery", map[string]any{"source_batches": batches, "iteration": state.CurrentRound, "repairs": e.turnReview.Repairs, "candidate_checked": false})
}
