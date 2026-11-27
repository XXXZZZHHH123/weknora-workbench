package agent

import (
	"context"
	"encoding/json"
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
	require.Regexp(t, "没有取得可核实的来源回执|No verifiable source receipt was obtained", answer)
}

func TestTerminalPublicationRejectsEveryPlaceholder(t *testing.T) {
	fallbacks := append([]string{""}, terminalFallbacks[:]...)
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", fmt.Sprint(enabled))
			for idx, fallback := range fallbacks {
				t.Run(fmt.Sprint(idx), func(t *testing.T) {
					for _, held := range []bool{false, true} {
						for _, hasReceipt := range []bool{false, true} {
							e := newTestEngine(t, &mockChat{})
							e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
							e.toolRegistry = qa4ReplayRegistry()
							e.holdCollaborationAnswer = held
							e.turnReview.Query = "Which observations were verified?"
							e.turnReview.DeferTerminalClose = true
							state := &types.AgentState{}
							if hasReceipt {
								call := terminalLatestSeriesCall()
								call.Args["source_question"] = e.turnReview.Query
								state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
							}
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
							require.Empty(t, stream.contents)
							e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
							done, visible := stream.snapshot()
							if hasReceipt {
								require.Equal(t, 1, done)
								require.Equal(t, []string{visible}, stream.contents)
								require.Nil(t, complete.Incomplete)
								require.False(t, terminalCandidateFallback(visible))
								for _, fact := range []string{"Equipment.xlsx", "Operating hours", "Elapsed time", "47.25", "2021-06-03", "B15"} {
									require.Contains(t, visible, fact)
								}
								require.NotContains(t, visible, e.turnReview.Query)
								require.NotContains(t, visible, "No verifiable source receipt")
							} else {
								require.Zero(t, done)
								require.Empty(t, stream.contents)
								require.Empty(t, visible)
								require.NotNil(t, complete.Incomplete)
								require.Contains(t, complete.Incomplete.Notice, e.turnReview.Query)
							}
							require.Equal(t, visible, complete.FinalAnswer)
							require.Equal(t, visible, state.FinalAnswer)
						}
					}
				})
			}
		})
	}
}

func TestTerminalPublicationRevalidatesCurrentSourceBeforeFirstEmission(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, change := range []string{"authorization", "snapshot", "new unrelated receipt"} {
			t.Run(fmt.Sprintf("%t/%s", enabled, change), func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", fmt.Sprint(enabled))
				e := newTestEngine(t, &mockChat{})
				e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
				e.toolRegistry = qa4ReplayRegistry()
				e.turnReview.TerminalSensitive, e.turnReview.DeferTerminalClose = true, true
				e.turnReview.Query = "What reading was observed?"
				const candidate = "Equipment.xlsx reports 47.25 on 2021-06-03."
				state := &types.AgentState{RoundSteps: []types.AgentStep{
					{Thought: "Preparing the source read.", ToolCalls: []types.ToolCall{terminalLatestSeriesCall()}},
					{Thought: candidate, ReasoningContent: "The source reports 47.25."},
				}}
				stream := &terminalAnswerRecorder{}
				stream.attach(e.eventBus)
				var complete event.AgentCompleteData
				e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
					complete = evt.Data.(event.AgentCompleteData)
					return nil
				})
				e.commitTerminalResult(t.Context(), state, "run", candidate, "", "stalled", false)
				require.Empty(t, stream.contents)
				switch change {
				case "authorization":
					e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "other-library"}}
				case "snapshot":
					state.RoundSteps[0].ToolCalls[0].Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
				case "new unrelated receipt":
					other := terminalLatestSeriesCall()
					other.ID, other.Args["dataset_id"], other.Result.Data["dataset_id"] = "other", "other", "other"
					state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{other}})
				}
				e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
				done, visible := stream.snapshot()
				require.Equal(t, visible, state.FinalAnswer)
				require.Equal(t, visible, complete.FinalAnswer)
				if change == "new unrelated receipt" {
					require.Equal(t, 1, done)
					require.Equal(t, []string{visible}, stream.contents)
					require.Nil(t, complete.Incomplete)
					require.Equal(t, candidate, visible)
					return
				}
				require.Zero(t, done)
				require.Empty(t, stream.contents)
				require.NotNil(t, complete.Incomplete)
				require.Equal(t, string(terminalSourceChanged), complete.Incomplete.Reason)
				require.NotContains(t, visible, "Equipment.xlsx")
				require.NotContains(t, visible, "47.25")
				stored, err := json.Marshal(complete.AgentSteps)
				require.NoError(t, err)
				require.NotContains(t, string(stored), "47.25")
				require.NotContains(t, string(stored), "Equipment.xlsx")
				require.Contains(t, string(stored), "Preparing the source read.")
				require.Empty(t, complete.KnowledgeRefs)
				require.Empty(t, complete.EvidenceRefs)
			})
		}
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
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
			stream := &terminalAnswerRecorder{}
			stream.attach(e.eventBus)
			state, err := e.executeLoop(t.Context(), &types.AgentState{}, "Explain the verified outcome.", emptyMessages(), emptyTools(), "run", "message")
			require.NoError(t, err)
			done, visible := stream.snapshot()
			require.Zero(t, done)
			require.Empty(t, visible)
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
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
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
				require.Empty(t, complete.FinalAnswer)
				require.NotNil(t, complete.Incomplete)
				require.Contains(t, complete.Incomplete.Notice, "What did the source report?")
			} else {
				require.Empty(t, complete.FinalAnswer)
				require.Nil(t, complete.Incomplete)
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
