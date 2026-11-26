package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func requireTerminalEvidenceGap(t *testing.T, answer string) {
	t.Helper()
	require.False(t, terminalCandidateFallback(answer), "an internal placeholder reached the user")
	require.Regexp(t, "仍待核实|没有取得可用于回答的已核实内容|remains unverified|produced no verified material", answer)
}

func TestTerminalPublicationRejectsEveryPlaceholder(t *testing.T) {
	fallbacks := append([]string{""}, terminalFallbacks[:]...)
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", fmt.Sprint(enabled))
			for idx, fallback := range fallbacks {
				t.Run(fmt.Sprint(idx), func(t *testing.T) {
					for _, held := range []bool{false, true} {
						e := newTestEngine(t, &mockChat{})
						e.holdCollaborationAnswer = held
						e.turnReview.Query = "Which observations were verified?"
						state := &types.AgentState{}
						stream := &terminalAnswerRecorder{}
						stream.attach(e.eventBus)
						var complete event.AgentCompleteData
						e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
							complete = evt.Data.(event.AgentCompleteData)
							return nil
						})

						require.Empty(t, e.publishAnswer(t.Context(), "run", " "+fallback+"\n", "stale-id", false))
						e.commitTerminalResult(t.Context(), state, "run", fallback, "", "failed", false)
						e.commitTerminalResult(t.Context(), state, "run", "a second candidate", "", "answer", false)
						e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
						done, visible := stream.snapshot()
						require.Equal(t, 1, done)
						requireTerminalEvidenceGap(t, visible)
						require.Contains(t, visible, "Which observations were verified?")
						require.Regexp(t, "本轮没有取得可用于回答的已核实内容|this turn produced no verified material",
							strings.ToLower(visible))
						require.Equal(t, visible, complete.FinalAnswer)
						require.Equal(t, visible, state.FinalAnswer)
					}
				})
			}
		})
	}
}

func TestLegacyProviderNeverStreamsAPlaceholderPrefix(t *testing.T) {
	for idx, fallback := range terminalFallbacks {
		t.Run(fmt.Sprint(idx), func(t *testing.T) {
			pieces := []rune(fallback)
			mid := len(pieces) / 2
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
				{Content: " \n" + string(pieces[:mid]), ResponseType: types.ResponseTypeAnswer},
				{Content: string(pieces[mid:]) + "  \n", ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "stop"},
			}}}}
			e := newTestEngine(t, model)
			stream := &terminalAnswerRecorder{}
			stream.attach(e.eventBus)
			state, err := e.executeLoop(t.Context(), &types.AgentState{}, "Explain the verified outcome.", emptyMessages(), emptyTools(), "run", "message")
			require.NoError(t, err)
			done, visible := stream.snapshot()
			require.Equal(t, 1, done)
			requireTerminalEvidenceGap(t, visible)
			require.NotContains(t, visible, fallback)
			require.Equal(t, state.FinalAnswer, visible)
		})
	}
}

func TestCompletionRevalidatesAnUncommittedOrSuspendedAnswer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		complete  bool
		suspended *types.PromptSuspension
	}{
		{name: "restored completion", complete: true},
		{name: "abandoned draft"},
		{name: "approval pending", suspended: &types.PromptSuspension{Kind: "tool_approval"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.turnReview.Query = "What did the source report?"
			state := &types.AgentState{
				IsComplete: tc.complete, FinalAnswer: answerReviewUnavailableFallback, Suspended: tc.suspended,
			}
			var complete event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				complete = evt.Data.(event.AgentCompleteData)
				return nil
			})
			e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
			require.Equal(t, state.FinalAnswer, complete.FinalAnswer)
			if tc.complete {
				requireTerminalEvidenceGap(t, complete.FinalAnswer)
				require.Contains(t, complete.FinalAnswer, "What did the source report?")
			} else {
				require.Empty(t, complete.FinalAnswer)
			}
		})
	}
}

func TestExactApprovedAnswersKeepTheirBytes(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "exact"}}}
	for _, exact := range []string{";", "{\"status\":\"ready\"}", incompleteAnswerFallback + " this is quoted text"} {
		state := &types.AgentState{}
		e.turnReview = turnReviewState{}
		e.commitTerminalResult(t.Context(), state, "run", exact, "", "answer", false)
		require.Equal(t, exact, state.FinalAnswer)
	}
}

func TestTerminalCorrectionPrecedesStreamClosure(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.turnReview.DeferTerminalClose = true
	e.turnReview.Query = "What did the source report?"
	state := &types.AgentState{}
	var events []event.Event
	e.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		events = append(events, evt)
		return nil
	})
	e.commitTerminalResult(t.Context(), state, "run", "Available observations.", "", "answer", false)
	require.Len(t, events, 1)
	require.False(t, events[0].Data.(event.AgentFinalAnswerData).Done)

	// A restored or subsequently invalidated result cannot leak into
	// completion. Both the correction and close keep the original stream ID.
	state.FinalAnswer = answerReviewUnavailableFallback
	e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
	require.Len(t, events, 3)
	require.Equal(t, events[0].ID, events[1].ID)
	require.Equal(t, events[0].ID, events[2].ID)
	correction := events[1].Data.(event.AgentFinalAnswerData)
	require.True(t, correction.Replace)
	require.False(t, correction.Done)
	requireTerminalEvidenceGap(t, correction.Content)
	require.Equal(t, correction.Content, state.FinalAnswer)
	require.True(t, events[2].Data.(event.AgentFinalAnswerData).Done)
}
