package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnverifiedCandidateEarlyReturnsCannotReplaceGroundedDraft(t *testing.T) {
	for _, reason := range []string{"budget", "cancelled", "empty"} {
		t.Run(reason, func(t *testing.T) {
			e, state, id := unverifiedRawCitationFixture(t)
			prior := e.turnReview.UnverifiedCandidate
			ctx := t.Context()
			candidate := "An unchecked extra number is 999."
			switch reason {
			case "budget":
				candidate += strings.Repeat(`<evidence evidence_id="`+id+`" />`, 2000)
				require.False(t, e.candidateEvidenceBudgetFits(candidate, state))
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "empty":
				candidate = ""
			}
			d := e.checkCandidate(ctx, e.turnReview.Query, candidate, "", state, nil, true)
			require.False(t, d.SourceReviewAttempted, "an early return has not established numeric/source-review eligibility")
			require.Equal(t, prior, e.turnReview.UnverifiedCandidate)
			require.NotEqual(t, candidate, e.turnReview.UnverifiedCheckedCandidate)
			if reason != "cancelled" {
				e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
				e.publishFinalKnowledgeReferences(t.Context(), state, "session")
				require.True(t, strings.HasPrefix(state.FinalAnswer, prior))
				require.NotContains(t, state.FinalAnswer, "999")
				require.Contains(t, state.FinalAnswer, "未能完成自动核验")
				require.Len(t, state.EvidenceRefs, 1, "the earlier real checked observation retains its private binding")
			}
		})
	}
}

func TestUnverifiedCandidateEarlyBudgetFailureWithoutPriorDraftHasNoFallback(t *testing.T) {
	e, state := rawCitationFixture(t)
	e.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0])
	id, _ := selectRawCitation(t, e, 1, 1)
	candidate := "The ungrounded number is 999." + strings.Repeat(`<evidence evidence_id="`+id+`" />`, 2000)
	d := e.checkCandidate(t.Context(), "Report the selected observation.", candidate, "", state, nil, true)
	require.Equal(t, candidateRevise, d.Disposition)
	require.False(t, d.SourceReviewAttempted)
	require.Empty(t, e.turnReview.UnverifiedCandidate)
	_, ok := e.unverifiedDelivery()
	require.False(t, ok)
	e.commitTerminalResult(t.Context(), state, "session", answerReviewUnavailableFallback, "", "failed", false)
	require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer)
	require.Empty(t, state.EvidenceRefs)
}
