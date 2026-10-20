package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectTaskSkipsSourceCriticAfterNumericGrounding(t *testing.T) {
	model := &mockChat{}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	decision := e.checkCandidate(t.Context(), "Report the indicator.", "The report describes covered area.", "", sourceConsistencyState(), nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Empty(t, model.calls, "no critic call for a direct lookup")
	require.True(t, e.candidateApprovalCurrent("The report describes covered area.", sourceConsistencyState()), "terminal delivery accepts the direct decision")
}

func TestSynthesisAndUnplannedTasksKeepSourceCritic(t *testing.T) {
	for _, plan := range []*taskEvidencePlan{{ReasoningDepth: "synthesis"}, nil, {}} {
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
	e.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	// Existing fragment definition: a very short body after long reasoning.
	reasoning := strings.Repeat("considering the source table ", 20)
	decision := e.checkCandidate(t.Context(), "Report the indicator.", "The", reasoning, sourceConsistencyState(), nil, false)
	require.NotEqual(t, candidateAccepted, decision.Disposition, "a fragment is not delivered by skipping review")
	require.Len(t, model.calls, 1, "the fragment still gets the completeness check")
}
