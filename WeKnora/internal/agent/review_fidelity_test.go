package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

// Synthetic wording only; the narrowed verdict mirrors the observed failure
// shape (reviewer accepted a weaker proposition than the candidate stated).
const narrowedVerdict = `{"complete":true,"assessment":"Main comparison is supported.","audit":[{"claim":"Both reports treat the factor as a pricing variable, but its effect is opposite in the later period.","candidate_statement":"The factor was a shared driver of both rallies.","candidate_fidelity":"narrowed","basis":"source","assessment":"Later evidence shows the factor suppressed prices.","evidence":[]}]}`

func TestReviewFidelityRevisionRequiresDeclarations(t *testing.T) {
	_, _, err := reviewFidelityRevision([]reviewClaimCheck{{Claim: "c", Fidelity: reviewFidelityAsWritten}})
	require.ErrorContains(t, err, "candidate_statement")
	_, _, err = reviewFidelityRevision([]reviewClaimCheck{{Claim: "c", CandidateStatement: "s"}})
	require.ErrorContains(t, err, "candidate_fidelity")
	_, _, err = reviewFidelityRevision([]reviewClaimCheck{{Claim: "c", CandidateStatement: "s", Fidelity: "mostly"}})
	require.Error(t, err)
	feedback, narrowed, err := reviewFidelityRevision([]reviewClaimCheck{{Claim: "c", CandidateStatement: "s", Fidelity: reviewFidelityAsWritten}})
	require.NoError(t, err)
	require.False(t, narrowed)
	require.Empty(t, feedback)
}

func TestReviewFidelityNarrowedOrContradictedAcceptanceBecomesRevision(t *testing.T) {
	for _, fidelity := range []string{reviewFidelityNarrowed, reviewFidelityContradicted} {
		feedback, narrowed, err := reviewFidelityRevision([]reviewClaimCheck{
			{Claim: "supported A", CandidateStatement: "stated A", Fidelity: reviewFidelityAsWritten},
			{Claim: "weaker B", CandidateStatement: "strong B", Fidelity: fidelity, Assessment: "evidence shows opposite effect"},
		})
		require.NoError(t, err)
		require.True(t, narrowed)
		require.Contains(t, feedback, "strong B")
		require.Contains(t, feedback, "weaker B")
		require.NotContains(t, feedback, "stated A", "only non-faithful checks become revision advice")
	}
	long := make([]rune, 2000)
	for i := range long {
		long[i] = '棉'
	}
	feedback, _, err := reviewFidelityRevision([]reviewClaimCheck{{Claim: string(long), CandidateStatement: "s", Fidelity: reviewFidelityNarrowed}})
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(feedback)), reviewFidelityFeedbackLimit)
}

func TestNarrowedAcceptanceIsNotDeliveredUnchanged(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(narrowedVerdict)}}
	engine := sourceConsistencyEngine(t, model)
	repair, err := engine.answerNeedsRepair(t.Context(), "Compare the drivers.", "The factor was a shared driver of both rallies.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, reviewReviseAnswer, engine.answerReviewAction)
	require.Contains(t, engine.answerReviewFeedback, "pricing variable")
}

func TestAcceptedVerdictWithoutFidelityIsProtocolFailure(t *testing.T) {
	missing := `{"complete":true,"assessment":"ok","audit":[{"claim":"c","basis":"source","assessment":"a","evidence":[]}]}`
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(missing)}}
	engine := sourceConsistencyEngine(t, model)
	_, err := engine.evaluateAnswerCompletenessOnce(t.Context(), "Compare the drivers.", "A candidate.", sourceConsistencyState(), 0)
	require.Error(t, err)
}

func TestNarrowedAcceptanceRevisesThenDeliversRecheckedRevision(t *testing.T) {
	const query = "Compare the drivers in the two reports."
	const draft = "The factor was a shared driver of both rallies."
	const revised = "The factor drove the earlier rally but suppressed prices later."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(draft),
		sourceConsistencyVerdict(narrowedVerdict),
		chatAnswer(`{"answer":"` + revised + `"}`),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	messages := []chat.Message{{Role: "system", Content: "Use current actual evidence."}, {Role: "user", Content: query}}
	_, err := engine.executeLoop(t.Context(), state, query, messages, emptyTools(), "fidelity-session", "fidelity-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, revised, state.FinalAnswer, "the reviewer-verified narrowing reaches the delivered text")
	require.NotContains(t, state.FinalAnswer, "shared driver")
	firstCritic := callWithFormat(model, sourceConsistencySchema)
	secondCritic := callWithFormatFrom(model, sourceConsistencySchema, firstCritic+1)
	require.Greater(t, secondCritic, firstCritic, "the revised text is reviewed again before delivery")
	require.Equal(t, revised, candidateReviewInput(t, model, secondCritic, "candidate_answer")["candidate_answer"])
}
