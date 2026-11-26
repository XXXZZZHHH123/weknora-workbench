package agent

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCandidateDecisionBindsTaskCandidateAndEvidence(t *testing.T) {
	m := &mockChat{responses: []mockResponse{sourceAcceptedVerdict(), sourceAcceptedVerdict(), sourceAcceptedVerdict()}}
	e := sourceConsistencyEngine(t, m)
	state := sourceConsistencyState()
	first := e.checkCandidate(t.Context(), "Explain the indicator.", "The indicator is covered area.", "", state, nil, false)
	require.Equal(t, candidateAccepted, first.Disposition)
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Use only the second report."}}
	second := e.checkCandidate(t.Context(), "Explain the indicator.", "The indicator is covered area.", "", state, nil, false)
	require.NotEqual(t, first.TaskHash, second.TaskHash)
	require.Equal(t, first.CandidateHash, second.CandidateHash)
	state.RoundSteps[0].ToolCalls[0].Result.Output = "Changed actual observation"
	third := e.checkCandidate(t.Context(), "Explain the indicator.", "The revised indicator is covered area.", "", state, nil, false)
	require.NotEqual(t, second.CandidateHash, third.CandidateHash)
	require.NotEqual(t, second.EvidenceHash, third.EvidenceHash)
}

func TestTerminalCannotReuseApprovalForChangedCandidate(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}})
	state := sourceConsistencyState()
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	e.checkCandidate(t.Context(), "Explain the indicator.", "The indicator is covered area.", "", state, nil, false)
	e.commitTerminalResult(t.Context(), state, "s", "A different unreviewed claim.", "", "answer", false)
	require.False(t, terminalCandidateFallback(state.FinalAnswer))
	require.Contains(t, state.FinalAnswer, "Explain the indicator.")
	require.NotContains(t, state.FinalAnswer, "A different unreviewed claim.")
	e.commitTerminalResult(t.Context(), state, "s", "Duplicate delivery.", "", "failed", false)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
}

func TestTerminalApprovalIgnoresOnlySurroundingWhitespace(t *testing.T) {
	const candidate = "The indicator is covered area."
	const padded = " \t" + candidate + "\r\n"
	for _, tc := range []struct {
		name, approved, submitted, expected string
	}{
		{"padded_submission", candidate, padded, candidate},
		{"padded_approval", padded, candidate, padded},
		{"internal_whitespace", candidate, "The indicator is  covered area.", answerReviewUnavailableFallback},
		{"added_think_block", candidate, candidate + "<think>Unchecked</think>", answerReviewUnavailableFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}})
			state := sourceConsistencyState()
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			decision := e.checkCandidate(t.Context(), "Explain the indicator.", tc.approved, "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.Equal(t, tc.approved, decision.Candidate)

			e.commitTerminalResult(t.Context(), state, "s", tc.submitted, "", "answer", false)

			if tc.expected == answerReviewUnavailableFallback {
				require.False(t, terminalCandidateFallback(state.FinalAnswer))
				require.Contains(t, state.FinalAnswer, "Explain the indicator.")
				require.NotContains(t, state.FinalAnswer, tc.submitted)
			} else {
				require.Equal(t, tc.expected, state.FinalAnswer)
			}
			require.Equal(t, tc.expected == tc.approved, e.candidateApprovalCurrent(state.FinalAnswer, state))
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
		})
	}
}

func TestCancelledFinalSynthesisDoesNotCallModelOrPublish(t *testing.T) {
	model := &mockChat{}
	e := newTestEngine(t, model)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state := sourceConsistencyState()
	e.handleMaxIterations(ctx, "Summarize the report.", state, "s", nil)
	require.Empty(t, model.calls)
	require.Empty(t, state.FinalAnswer)
	require.False(t, state.IsComplete)
	done, visible := recorder.snapshot()
	require.Zero(t, done)
	require.Empty(t, visible)
}

func TestFinalSynthesisDoesNotRefreshSpentCandidateBudget(t *testing.T) {
	model := &mockChat{responses: []mockResponse{chatAnswer("Unfinished response."), completenessVerdict(false)}}
	e := newTestEngine(t, model)
	e.turnReview.Repairs = maxTurnCandidateRepairs
	state := &types.AgentState{}
	require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "Complete the task.", state, "s", nil))
	require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
	require.Len(t, model.calls, 2, "synthesis may inspect its first candidate but gets no new repairs")
	require.Equal(t, maxTurnCandidateRepairs, e.turnReview.Repairs)
}

func TestFilteredDraftIsNeverDeliveredAsAnAnswer(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{Content: "Unverified partial draft", Done: true, FinishReason: "content_filter"}}}}}
	e := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	_, err := e.executeLoop(t.Context(), state, "Explain the report.", emptyMessages(), emptyTools(), "s", "m")
	require.NoError(t, err)
	require.NotContains(t, state.FinalAnswer, "Unverified partial draft")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
}

func TestTerminalCannotReuseApprovalAfterTaskOrEvidenceChanges(t *testing.T) {
	for _, changeTask := range []bool{false, true} {
		e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}})
		state := sourceConsistencyState()
		candidate := "The indicator is covered area."
		e.reviewConversationReady = true
		e.reviewConversation = []chat.Message{{Role: "user", Content: "Explain the indicator."}}
		e.checkCandidate(t.Context(), "Explain the indicator.", candidate, "", state, nil, false)
		if changeTask {
			e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Only use the appendix."})
		} else {
			state.RoundSteps[0].ToolCalls[0].Result.Output = "A changed observation"
		}
		e.commitTerminalResult(t.Context(), state, "s", " \t"+candidate+"\n", "", "answer", false)
		require.False(t, terminalCandidateFallback(state.FinalAnswer))
		require.NotContains(t, state.FinalAnswer, candidate, "a stale task or evidence revision must not be committed")
	}
}
