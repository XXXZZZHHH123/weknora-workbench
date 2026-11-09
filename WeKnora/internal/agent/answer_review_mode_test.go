package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStandardAnswerReviewSkipsAdditionsAndIndependentCritic(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "exceptional"}[force], func(t *testing.T) {
			model := &additionsChat{mockChat: mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}}
			engine, state, candidate := directCitationReadFixture(t, &model.mockChat, true)
			engine.chatModel = model
			engine.config.AnswerReviewMode = "standard"
			engine.config.CandidateAdditionsEnabled = true
			engine.taskPlan.ReasoningDepth = "synthesis"
			decision := engine.checkCandidate(t.Context(), "Report the indicator from the source.", candidate, "", state, nil, force)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.NoError(t, decision.Err)
			require.Equal(t, candidate, decision.Candidate)
			require.False(t, decision.SourceReviewAttempted, "standard approval must not pretend an independent semantic review occurred")
			require.Empty(t, model.calls)
			require.Empty(t, model.selectionCalls)
			require.True(t, engine.candidateApprovalCurrent(candidate, state))
			state.FinalAnswer = candidate
			require.Len(t, engine.finalAnswerKnowledgeReferences(state), 1)
			engine.config.AnswerReviewMode = "strict"
			require.False(t, engine.candidateApprovalCurrent(candidate, state), "switching to strict invalidates a standard delivery approval")
		})
	}
}

func TestStandardAnswerReviewDoesNotRequireAnotherModelToClassifyCitationIntent(t *testing.T) {
	model := &additionsChat{}
	engine, state, _ := directCitationReadFixture(t, &model.mockChat, true)
	engine.chatModel = model
	engine.config.AnswerReviewMode = "standard"
	decision := engine.checkCandidate(t.Context(), "Explain the observed indicator.", "The returned indicator measures covered area.", "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Empty(t, model.calls)
	require.Empty(t, model.selectionCalls)
	require.Empty(t, engine.currentCandidateKnowledgeReferences(decision.Candidate, state, nil), "the runtime does not invent a citation the model omitted")
}

func TestStrictAndLegacyAnswerReviewKeepTheExistingCritic(t *testing.T) {
	for _, mode := range []string{"", "strict", "unknown"} {
		t.Run("mode="+mode, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
			engine, state, candidate := directCitationReadFixture(t, model, true)
			engine.config.AnswerReviewMode = mode
			decision := engine.checkCandidate(t.Context(), "Report the observed indicator.", candidate, "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.NoError(t, decision.Err)
			require.True(t, decision.SourceReviewAttempted)
			require.Len(t, model.calls, 1)
		})
	}
}

func TestStandardAnswerReviewRejectsUnknownFailedAndConflictedCitationIdentities(t *testing.T) {
	for _, mutation := range []string{"unknown", "failed", "conflicted"} {
		t.Run(mutation, func(t *testing.T) {
			model := &mockChat{}
			engine, state, candidate := directCitationReadFixture(t, model, true)
			engine.config.AnswerReviewMode = "standard"
			switch mutation {
			case "unknown":
				candidate += `<kb chunk_id="invented-source" />`
			case "failed":
				state.RoundSteps[0].ToolCalls[0].Result.Success = false
			case "conflicted":
				call := state.RoundSteps[0].ToolCalls[0]
				encoded, err := json.Marshal(call.Result)
				require.NoError(t, err)
				call.Result = nil
				require.NoError(t, json.Unmarshal(encoded, &call.Result))
				call.ID = "changed-source"
				call.Result.Data["chunks"].([]any)[1].(map[string]any)["content"] = "A different observation."
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
			}
			decision := engine.checkCandidate(t.Context(), "Report the actual indicator.", candidate, "", state, nil, false)
			require.Equal(t, candidateRevise, decision.Disposition)
			require.Equal(t, reviewReviseAnswer, engine.answerReviewAction)
			require.False(t, engine.candidateApprovalCurrent(candidate, state))
			require.Empty(t, engine.turnReview.UnverifiedCandidate)
			require.Empty(t, model.calls, "an invalid identity is diagnosed without a semantic model call")
		})
	}
}

func TestStandardAnswerReviewRejectsChangedStructuredEvidenceVersion(t *testing.T) {
	engine, state := rawCitationFixture(t)
	engine.config.AnswerReviewMode = "standard"
	engine.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0])
	id, _ := selectRawCitation(t, engine, 1, 1)
	candidate := `The selected observation is 12.500 kg.<evidence evidence_id="` + id + `" />`
	decision := engine.checkCandidate(t.Context(), "Report the selected observation.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.False(t, decision.SourceReviewAttempted)
	state.RoundSteps[0].ToolCalls[0].Result.Data["changed_version"] = true
	decision = engine.checkCandidate(t.Context(), "Report the selected observation.", candidate, "", state, nil, false)
	require.Equal(t, candidateRevise, decision.Disposition)
	require.False(t, engine.candidateApprovalCurrent(candidate, state))
}

func TestStandardAnswerReviewKeepsNumericFailureAndGroundedPartialAnswer(t *testing.T) {
	model := &mockChat{}
	engine := numericRecoveryTestEngine(t, model)
	engine.config.AnswerReviewMode = "standard"
	state, messages, query := numericRecoveryFixture()
	for _, exhausted := range []bool{false, true} {
		engine.turnReview.NumericWarningAllowed = exhausted
		if exhausted {
			engine.turnReview.Repairs = maxTurnCandidateRepairs
		}
		decision := engine.checkCandidate(t.Context(), query, "The measured value is 138.67 and the uncomputed total is 999.", "", state, messages, false)
		require.Equal(t, candidateNumericFailure, decision.Disposition)
		require.Equal(t, []string{"999"}, decision.Unsupported)
		require.Equal(t, "unsupported_tokens", decision.NumericFeedback["check_status"])
		require.Empty(t, engine.turnReview.UnverifiedCandidate)
		require.Empty(t, model.calls)
	}
	// A model-authored supported part with an honest gap is deliverable without
	// making the missing statistic appear calculated or invoking another critic.
	candidate := "The measured value is 138.67. The additional total has not been calculated."
	decision := engine.checkCandidate(t.Context(), query, candidate, "", state, messages, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Empty(t, decision.Unsupported)
	require.Equal(t, candidate, decision.Candidate)
	require.Empty(t, model.calls)
}

func TestStandardAnswerReviewPreservesActualArtifactDeliveryChecks(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":true,"delivery_check":{"status":"delivered","assessment":"The answer says the chart is delivered."}}`)}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.AnswerReviewMode = "standard"
	engine.taskPlan = chartDeliveryPlan()
	state := chartReferenceState()
	delete(state.RoundSteps[0].ToolCalls[0].Result.Data, "artifact")
	decision := engine.checkCandidate(t.Context(), "Deliver the requested chart.", "![Chart]("+chartReferenceFixture+")", "", state, nil, false)
	require.Equal(t, candidateRevise, decision.Disposition, "a resource string is not proof of a successful saved artifact")
	require.NoError(t, decision.Err)
	require.Len(t, model.calls, 1, "non-text actions retain the existing delivery check")
	require.False(t, engine.candidateApprovalCurrent(decision.Candidate, state))
}

func TestStandardAnswerReviewNormalAndExceptionalDeliveryPublishCheckedContent(t *testing.T) {
	for _, exceptional := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stalled"}[exceptional], func(t *testing.T) {
			model := &mockChat{}
			engine, state, candidate := directCitationReadFixture(t, model, true)
			engine.config.AnswerReviewMode = "standard"
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			query := "Report the observed indicator."
			messages := []chat.Message{{Role: "user", Content: query}}
			if exceptional {
				engine.finishStalledTurn(t.Context(), state, "standard-session", &types.ChatResponse{Content: candidate}, false, query, messages)
				require.Empty(t, model.calls)
			} else {
				wireCandidate := `The indicator measures covered area.<ref id="` + engine.modelContext.CitableChunkHandle("contract-supporting") + `"/>`
				model.responses = []mockResponse{chatAnswer(wireCandidate)}
				_, err := engine.executeLoop(context.Background(), state, query, messages, nil, "standard-session", "standard-message")
				require.NoError(t, err)
				require.Len(t, model.calls, 1, "only the main answer generation is needed")
			}
			require.True(t, state.IsComplete)
			require.Equal(t, candidate, state.FinalAnswer)
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
			require.True(t, engine.candidateApprovalCurrent(candidate, state))
			require.Len(t, engine.finalAnswerKnowledgeReferences(state), 1)
		})
	}
}
