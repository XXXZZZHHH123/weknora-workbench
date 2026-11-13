package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryProtocolRepairUsesSameReceiptDeadlineEvidenceAndTotalBudget(t *testing.T) {
	for _, phase := range []string{"progress", "delivery"} {
		for _, action := range []string{"keep", "recover"} {
			t.Run(phase+"_"+action, func(t *testing.T) {
				feedback := ""
				if action == "recover" {
					feedback = "The original finding remains unanswered; reconsider an available authorized source route."
				}
				first := deliveryReply(`{"action":"recover","feedback":`, "stop", types.TokenUsage{CompletionTokens: 120, TotalTokens: 120})
				e, state, _ := discoveryReviewFixture(t)
				model := &protocolRepairClockChat{mockChat: mockChat{responses: []mockResponse{first, discoveryReply(action, feedback)}}}
				e.chatModel = model
				before := reviewDigest(state.RoundSteps)
				decision := candidateDecision{Disposition: candidateAccepted, Candidate: "Actual pending mapping.", CandidateHash: "candidate", TaskHash: "task", EvidenceHash: "evidence", SourceScopeHash: "scope"}
				got := e.checkDiscoveryDeliveryPhase(t.Context(), "Explain the source pattern.", decision, state, nil, phase, nil)
				require.Len(t, model.calls, 2)
				require.Equal(t, model.deadlines[0], model.deadlines[1])
				require.Equal(t, discoveryReviewInitialTokens, model.opts[0].MaxCompletionTokens)
				require.Equal(t, discoveryReviewTokens-120, model.opts[1].MaxCompletionTokens)
				require.Equal(t, 132, state.TurnUsage.CompletionTokens)
				require.Equal(t, model.calls[0][1].Content, model.calls[1][1].Content)
				require.Equal(t, first.chunks[0].Content, model.calls[1][2].Content)
				require.Contains(t, model.calls[1][3].Content, "Protocol repair only")
				require.Equal(t, before, reviewDigest(state.RoundSteps))
				require.Equal(t, decision.Candidate, got.Candidate)
				require.Equal(t, action, e.turnReview.DiscoveryReview.Action)
				require.Equal(t, "schema", e.turnReview.DiscoveryReview.InitialFailureCode)
				require.Empty(t, e.turnReview.DiscoveryReview.FailureCode)
				require.True(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
				require.Zero(t, e.turnReview.Repairs)
				if action == "recover" {
					require.Equal(t, candidateNeedSource, got.Disposition)
				} else {
					require.Equal(t, decision, got)
				}
				e.checkDiscoveryOnlyDelivery(t.Context(), "Explain the source pattern.", decision, state, nil)
				require.Len(t, model.calls, 2, "later delivery never replenishes the once-only receipt")
			})
		}
	}
}

func TestDiscoveryProtocolRepairConservativelyChargesMissingUsage(t *testing.T) {
	first := chatAnswer("invalid")
	first.chunks[0].Usage = nil
	e, state, model := discoveryReviewFixture(t, first, discoveryReply("keep", ""))
	e.checkDiscoveryOnlyDelivery(t.Context(), "Report mapping status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}, state, nil)
	require.Len(t, model.calls, 2)
	require.Equal(t, discoveryReviewTokens-len("invalid"), model.opts[1].MaxCompletionTokens)
	for _, missingUsage := range []bool{false, true} {
		first := deliveryReply(strings.Repeat("x", discoveryReviewTokens), "stop", types.TokenUsage{CompletionTokens: discoveryReviewTokens})
		if missingUsage {
			first.chunks[0].Usage = nil
		}
		e, state, model := discoveryReviewFixture(t, first)
		e.checkDiscoveryOnlyDelivery(t.Context(), "Report mapping status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}, state, nil)
		require.Len(t, model.calls, 1)
		require.Empty(t, e.turnReview.DiscoveryReview.Action)
		require.Equal(t, "generation_budget", e.turnReview.DiscoveryReview.FailureCode)
		require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
	}
}

func TestDiscoveryProtocolNeverRetriesNonSchemaFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response mockResponse
		failure  string
	}{
		{"finish", deliveryReply(`{"action":"keep","feedback":""}`, "length", types.TokenUsage{CompletionTokens: 12}), "finish"},
		{"tool_calls", mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: "forbidden", Type: "function", Function: types.FunctionCall{Name: "read_document", Arguments: `{}`}}}}}}, "tool_calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, state, model := discoveryReviewFixture(t, tc.response)
			original := candidateDecision{Disposition: candidateAccepted, Candidate: "Actual mapping status."}
			require.Equal(t, original, e.checkDiscoveryOnlyDelivery(t.Context(), "Report mapping status.", original, state, nil))
			require.Len(t, model.calls, 1)
			require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
			require.Equal(t, tc.failure, e.turnReview.DiscoveryReview.FailureCode)
			require.Empty(t, e.turnReview.DiscoveryReview.Action)
		})
	}
	e, state, _ := discoveryReviewFixture(t)
	model := &protocolRepairClockChat{}
	e.chatModel = model
	e.checkDiscoveryOnlyDelivery(t.Context(), "Report status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual status."}, state, nil)
	require.Len(t, model.deadlines, 1)
	require.Equal(t, "transport", e.turnReview.DiscoveryReview.FailureCode)
	require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
}

func TestDiscoveryProtocolHonorsDeadlineWithoutRepair(t *testing.T) {
	e, state, _ := discoveryReviewFixture(t)
	model := &cancelledDiscoveryChat{}
	e.chatModel = model
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	e.checkDiscoveryOnlyDelivery(ctx, "Report status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual status."}, state, nil)
	require.Equal(t, 1, model.callsStarted)
	require.Equal(t, "deadline", e.turnReview.DiscoveryReview.FailureCode)
	require.False(t, e.turnReview.DiscoveryReview.ProtocolRepairAttempted)
	e.checkDiscoveryOnlyDelivery(t.Context(), "Report status.", candidateDecision{Disposition: candidateAccepted, Candidate: "Actual status."}, state, nil)
	require.Equal(t, 1, model.callsStarted)
}

func TestDiscoveryProtocolFailureCodesContainNoModelText(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Equal(t, "cancelled", discoveryReviewResponseFailure(ctx, nil, errors.New("private transport body")))
	require.Equal(t, "nil_response", discoveryReviewResponseFailure(t.Context(), nil, nil))
	require.Equal(t, "stream", discoveryReviewResponseFailure(t.Context(), &streamLLMResult{StreamError: "private provider response"}, nil))
}

func TestDiscoveryProtocolStrictSchemaDoesNotGuessMissingFieldsOrMarkdown(t *testing.T) {
	for _, content := range []string{`{"action":"keep"}`, `{"action":"keep","feedback":null}`, `{"action":"keep","feedback":"","action":"recover"}`, "```json\n{\"action\":\"keep\",\"feedback\":\"\"}\n```"} {
		_, _, valid := decodeDiscoveryDeliveryReview(content)
		require.False(t, valid, content)
	}
}
