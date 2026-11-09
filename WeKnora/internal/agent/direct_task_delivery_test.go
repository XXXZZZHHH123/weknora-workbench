package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectTaskUsesSameSourceCriticAfterNumericGrounding(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	e, state, candidate := directCitationReadFixture(t, model, true)
	decision := e.checkCandidate(t.Context(), "Report the indicator.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Len(t, model.calls, 1, "a direct lookup must establish the same source meaning and requested coverage")
	require.True(t, decision.SourceReviewAttempted)
	require.True(t, e.candidateApprovalCurrent(candidate, state), "terminal delivery accepts the reviewed decision")
}

func TestSynthesisAndUnplannedTasksKeepSourceCritic(t *testing.T) {
	for _, plan := range []*taskEvidencePlan{{ReasoningDepth: "synthesis"}, {ReasoningDepth: "direct"}, nil, {}} {
		model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
		e := sourceConsistencyEngine(t, model)
		e.taskPlan = plan
		decision := e.checkCandidate(t.Context(), "Compare the drivers.", "The actual result is available.", "", sourceConsistencyState(), nil, false)
		require.Equal(t, candidateAccepted, decision.Disposition)
		require.Len(t, model.calls, 1, "the critic runs")
	}
}

func TestDirectTaskStillChecksFragmentsAndNumbers(t *testing.T) {
	model := &mockChat{responses: []mockResponse{completenessVerdict(false)}}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = explicitTextDeliveryPlan()
	// Existing fragment definition: a very short body after long reasoning.
	reasoning := strings.Repeat("considering the source table ", 20)
	decision := e.checkCandidate(t.Context(), "Report the indicator.", "The", reasoning, sourceConsistencyState(), nil, false)
	require.NotEqual(t, candidateAccepted, decision.Disposition, "a fragment is not delivered by skipping review")
	require.Len(t, model.calls, 1, "the fragment still gets the completeness check")
}
