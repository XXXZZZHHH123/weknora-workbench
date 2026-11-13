package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func appendDiscoveryActualObservation(state *types.AgentState) {
	call := state.RoundSteps[0].ToolCalls[0]
	result := *call.Result
	result.Data = make(map[string]any, len(call.Result.Data))
	for k, v := range call.Result.Data {
		result.Data[k] = v
	}
	result.Data["total"] = 1
	result.Output = "New successful native source observation."
	call.Result = &result
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
}

func TestDiscoveryProgressThenNewEvidenceDeliverySharesBudget(t *testing.T) {
	for _, initial := range []string{"keep", "recover", "finish_error"} {
		t.Run(initial, func(t *testing.T) {
			e, state, _, _, _ := sourceProgressFixture(t, "keep")
			first := discoveryReply(initial, "Reconsider the actual requested finding.")
			if initial == "keep" {
				first = discoveryReply("keep", "")
			}
			if initial == "finish_error" {
				first = deliveryReply(`{"action":"keep","feedback":""}`, "length", types.TokenUsage{CompletionTokens: 100})
			}
			first.chunks[0].Usage = &types.TokenUsage{CompletionTokens: 400}
			model := &protocolRepairClockChat{mockChat: mockChat{responses: []mockResponse{first, discoveryReply("recover", "The actual read now supplies relevant evidence; answer the original finding without optional precision.")}}}
			e.chatModel = model
			progress := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", SourceScopeHash: "scope", EvidenceHash: "old"}
			e.checkDiscoveryDeliveryPhase(t.Context(), "Explain the source pattern.", progress, state, nil, "progress", nil)
			old := e.turnReview.DiscoveryReview
			require.Len(t, model.calls, 1)
			appendDiscoveryActualObservation(state)
			old.budget.elapsed = 3 * time.Second
			candidate := progress
			candidate.Candidate, candidate.CandidateHash, candidate.EvidenceHash = "Cannot answer despite the actual read.", "candidate", "new"
			start := time.Now()
			got := e.checkDiscoveryOnlyDelivery(t.Context(), "Explain the source pattern.", candidate, state, nil)
			require.Equal(t, candidateNeedSource, got.Disposition)
			require.Len(t, model.calls, 2)
			require.Equal(t, 368, model.opts[1].MaxCompletionTokens)
			require.Same(t, old.budget, e.turnReview.DiscoveryReview.budget)
			require.Equal(t, 412, old.budget.generation.completionUsed)
			require.InDelta(t, 9*time.Second, model.deadlines[1].Sub(start), float64(200*time.Millisecond))
			require.GreaterOrEqual(t, old.budget.elapsed, 3*time.Second)
			require.Contains(t, model.calls[1][1].Content, candidate.Candidate)
			require.Equal(t, "delivery", e.turnReview.DiscoveryReview.Phase)
			e.turnReview.Repairs = maxTurnCandidateRepairs
			require.Equal(t, candidateNeedSource, e.checkDiscoveryOnlyDelivery(t.Context(), "Explain the source pattern.", candidate, state, nil).Disposition)
			require.Len(t, model.calls, 2, "unchanged incomplete delivery reuses its bound verdict without new model call")
		})
	}
}

func TestDiscoveryProgressDeliveryRequiresNewNativeEvidenceAndRemainingAllowance(t *testing.T) {
	for _, scenario := range []string{"candidate_only", "duplicate", "failed_read", "fake_tool", "no_evidence_hash_change", "scope_change", "task_change", "tokens_spent", "time_spent", "repairs_spent", "input_budget", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			e, state, model, _, _ := sourceProgressFixture(t, "keep")
			progress := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", SourceScopeHash: "scope", EvidenceHash: "old"}
			e.checkDiscoveryDeliveryPhase(t.Context(), "Explain.", progress, state, nil, "progress", nil)
			before := e.turnReview.DiscoveryReview
			candidate := progress
			candidate.Candidate, candidate.CandidateHash, candidate.EvidenceHash = "Different prose.", "candidate", "new"
			ctx := t.Context()
			switch scenario {
			case "candidate_only":
			case "duplicate":
				state.RoundSteps = append(state.RoundSteps, state.RoundSteps[0])
			case "failed_read":
				appendDiscoveryActualObservation(state)
				state.RoundSteps[len(state.RoundSteps)-1].ToolCalls[0].Result.Success = false
			case "fake_tool":
				appendDiscoveryActualObservation(state)
				state.RoundSteps[len(state.RoundSteps)-1].ToolCalls[0].Name = "unregistered_read"
			default:
				appendDiscoveryActualObservation(state)
				switch scenario {
				case "no_evidence_hash_change":
					candidate.EvidenceHash = "old"
				case "scope_change":
					candidate.SourceScopeHash = "new-scope"
				case "task_change":
					candidate.TaskHash = "new-task"
				case "tokens_spent":
					before.budget.generation.completionUsed = discoveryReviewTokens
				case "time_spent":
					before.budget.elapsed = discoveryReviewTimeout
				case "repairs_spent":
					e.turnReview.Repairs = maxTurnCandidateRepairs
				case "input_budget":
					candidate.Candidate = strings.Repeat("x", 8193)
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
			}
			require.Equal(t, candidate, e.checkDiscoveryOnlyDelivery(ctx, "Explain.", candidate, state, nil))
			require.Len(t, model.calls, 1)
			require.Same(t, before, e.turnReview.DiscoveryReview, "a skipped candidate must not acquire a delivery receipt")
		})
	}
}

func TestDiscoveryProtocolRepairIsSharedAcrossProgressAndDelivery(t *testing.T) {
	for _, repairInProgress := range []bool{false, true} {
		t.Run(map[bool]string{true: "progress_repaired", false: "delivery_repaired"}[repairInProgress], func(t *testing.T) {
			e, state, _, _, _ := sourceProgressFixture(t, "keep")
			bad := chatAnswer("invalid")
			bad.chunks[0].Usage = nil
			replies := []mockResponse{discoveryReply("keep", ""), bad, discoveryReply("keep", "")}
			if repairInProgress {
				replies = []mockResponse{bad, discoveryReply("keep", ""), bad}
			}
			model := &protocolRepairClockChat{mockChat: mockChat{responses: replies}}
			e.chatModel = model
			d := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", SourceScopeHash: "scope", EvidenceHash: "old"}
			e.checkDiscoveryDeliveryPhase(t.Context(), "Explain.", d, state, nil, "progress", nil)
			shared := e.turnReview.DiscoveryReview.budget
			appendDiscoveryActualObservation(state)
			d.Candidate, d.CandidateHash, d.EvidenceHash = "Actual candidate", "candidate", "new"
			e.checkDiscoveryOnlyDelivery(t.Context(), "Explain.", d, state, nil)
			require.Len(t, model.calls, 3)
			require.Same(t, shared, e.turnReview.DiscoveryReview.budget)
			require.True(t, shared.protocolRepairUsed)
			if repairInProgress {
				require.Equal(t, "schema", e.turnReview.DiscoveryReview.FailureCode)
				require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
				require.Equal(t, 26, shared.generation.completionUsed)
			} else {
				require.Equal(t, "keep", e.turnReview.DiscoveryReview.Action)
				require.True(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
				require.Equal(t, 31, shared.generation.completionUsed)
				require.Equal(t, 749, model.opts[2].MaxCompletionTokens)
			}
			e.checkDiscoveryOnlyDelivery(t.Context(), "Explain.", d, state, nil)
			require.Len(t, model.calls, 3)
		})
	}
}

func TestDiscoveryDeliveryParentDeadlineStillBoundsRemainingReviewTime(t *testing.T) {
	e, state, _, _, _ := sourceProgressFixture(t, "keep")
	model := &protocolRepairClockChat{mockChat: mockChat{responses: []mockResponse{discoveryReply("keep", ""), discoveryReply("keep", "")}}}
	e.chatModel = model
	d := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", SourceScopeHash: "scope", EvidenceHash: "old"}
	e.checkDiscoveryDeliveryPhase(t.Context(), "Explain.", d, state, nil, "progress", nil)
	appendDiscoveryActualObservation(state)
	d.Candidate, d.CandidateHash, d.EvidenceHash = "Actual candidate", "candidate", "new"
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	e.checkDiscoveryOnlyDelivery(ctx, "Explain.", d, state, nil)
	require.Len(t, model.calls, 2)
	require.Equal(t, deadline, model.deadlines[1])
}
