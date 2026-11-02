package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestNumericWarningExceptionalTerminalsStillReviewAndDeliver(t *testing.T) {
	const candidate = "The measured value is 138.67; an optional approximation is 138."
	for _, path := range []string{"stalled", "final synthesis"} {
		t.Run(path, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
			if path == "final synthesis" {
				model.responses = []mockResponse{chatAnswer(candidate), chatAnswer(candidate), chatAnswer(candidate), sourceAcceptedVerdict()}
			}
			engine := numericRecoveryTestEngine(t, model)
			engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
			state, messages, query := numericRecoveryFixture()
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			if path == "stalled" {
				engine.finishStalledTurn(t.Context(), state, "warning-session", &types.ChatResponse{Content: candidate}, false, query, messages)
			} else {
				require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), query, state, "warning-session", messages))
			}
			require.Contains(t, state.FinalAnswer, candidate)
			require.Contains(t, state.FinalAnswer, "138”尚未完成逐项核实")
			require.Equal(t, candidateNumericWarning, engine.turnReview.Last.Disposition)
			require.False(t, engine.candidateApprovalCurrent(candidate, state), "a qualified delivery is not recorded as fully verified")
			critic := callWithFormat(model, sourceConsistencySchema)
			require.GreaterOrEqual(t, critic, 0, "only this numerically uncertain direct answer requires source-model judgment")
			input := candidateReviewInput(t, model, critic, "candidate_answer")
			require.Contains(t, input["numeric_warning_delivery_notice"], "138")
			require.Contains(t, model.calls[critic][1].Content, "unsupported_tokens", "the source model receives actual numeric diagnostics")
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
			require.LessOrEqual(t, engine.turnReview.Repairs, maxTurnCandidateRepairs)
		})
	}
}

func TestNumericWarningBindsCheckedEnvelopeTaskAndEvidence(t *testing.T) {
	const base = "The measured value is 138.67; an optional approximation is 138."
	encoded, err := json.Marshal(map[string]string{"answer": base})
	require.NoError(t, err)
	for _, change := range []string{"none", "candidate", "task", "evidence"} {
		t.Run(change, func(t *testing.T) {
			engine := numericRecoveryTestEngine(t, &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}})
			engine.turnReview.NumericWarningAllowed = true
			state, messages, query := numericRecoveryFixture()
			checked := string(encoded)
			decision := engine.checkCandidate(t.Context(), query, checked, "", state, messages, false)
			require.Equal(t, candidateNumericWarning, decision.Disposition)
			require.Equal(t, checked, engine.turnReview.UnverifiedCheckedCandidate)
			require.Equal(t, base, engine.turnReview.UnverifiedCandidate)
			candidate := checked
			switch change {
			case "candidate":
				candidate = "An unchecked replacement."
			case "task":
				engine.reviewConversationReady = true
				engine.reviewConversation = []chat.Message{{Role: "user", Content: "Use a different report."}}
			case "evidence":
				state.RoundSteps[0].ToolCalls[0].Result.Output = `{"kind":"structured_result_evidence","status":"ok","values":[{"value":45}]}`
			}
			delivery, ok := engine.numericWarningDelivery(candidate, state)
			require.Equal(t, change == "none", ok)
			if ok {
				require.Contains(t, delivery, base)
				require.NotContains(t, delivery, `{"answer"`)
			} else {
				require.Empty(t, delivery)
			}
		})
	}
}

func TestNumericWarningCannotTurnArbitrarySourceIntoVerifiedCoreValue(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The requested value is not in the returned source; answer only the supported measurement.","user_notice":"The additional value is not established by the cited source.","next_action":"revise_answer"}`)}}
	engine := numericRecoveryTestEngine(t, model)
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	engine.turnReview.NumericWarningAllowed = true
	state, messages, query := numericRecoveryFixture()
	decision := engine.checkCandidate(t.Context(), query, "The requested value is 999.", "", state, messages, false)
	require.Equal(t, candidateRevise, decision.Disposition)
	require.Equal(t, []string{"999"}, decision.Unsupported)
	require.Empty(t, engine.turnReview.UnverifiedCandidate)
	require.Len(t, model.calls, 1)
	engine.commitTerminalResult(t.Context(), state, "warning-session", numericFailureFallback, "", "failed", false)
	require.NotContains(t, state.FinalAnswer, "999")

	engine = numericRecoveryTestEngine(t, &mockChat{})
	engine.turnReview.NumericWarningAllowed = true
	engine.config.TaskPlanningEnabled = false
	state, messages, query = numericRecoveryFixture()
	decision = engine.checkCandidate(t.Context(), query, "The requested value is 999.", "", state, messages, false)
	require.Equal(t, candidateNumericFailure, decision.Disposition, "without source judgment a warning cannot silently become accepted")
	require.Empty(t, engine.turnReview.UnverifiedCandidate)
}
