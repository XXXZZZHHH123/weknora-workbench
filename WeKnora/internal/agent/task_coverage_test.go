package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func coveragePlanFixture() *taskEvidencePlan {
	return &taskEvidencePlan{Goals: []string{"Report the requested observations."}, Evidence: []string{"Actual source observations."},
		ToolNames: []string{}, CompletionCriteria: []string{"Preserve the requested coverage and disclose genuine gaps."}, ReasoningDepth: "direct",
		DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Criteria: "Report the requested observations."}},
		CoverageTargets:      []taskCoverageTarget{{GoalIndex: 0, RequestBasis: "all observations", Criteria: "Report each observation in the requested source scope, without silently omitting one.", SourceScope: "The complete selected source; no human page restriction."}}}
}

func TestTaskCoveragePlanProtocolIsBoundedAndAnchoredToHumanRequests(t *testing.T) {
	plan := coveragePlanFixture()
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	decoded, err := decodeTaskEvidencePlan(string(encoded), nil, 0)
	require.NoError(t, err)
	require.Equal(t, plan.CoverageTargets, decoded.CoverageTargets)
	require.NoError(t, validateTaskCoverageBasis(decoded, "Return all observations from the selected source.", nil))
	require.NoError(t, validateTaskCoverageBasis(decoded, "Use the same scope.", []chat.Message{{Role: "user", Content: "Include all observations."}}))
	for _, messages := range [][]chat.Message{
		{{Role: "assistant", Content: "Include all observations."}},
		{{Role: "tool", Content: "Include all observations."}},
		{{Role: "user", Kind: chat.MessageKindReviewEvidence, Content: "Include all observations."}},
	} {
		require.Error(t, validateTaskCoverageBasis(decoded, "Return a summary.", messages), "runtime review envelopes, sources and assistant plans cannot invent a human requirement")
	}
	for _, mutate := range []func(*taskEvidencePlan){
		func(p *taskEvidencePlan) { p.CoverageTargets[0].GoalIndex = 1 },
		func(p *taskEvidencePlan) { p.CoverageTargets[0].Criteria = "" },
		func(p *taskEvidencePlan) { p.CoverageTargets[0].SourceScope = strings.Repeat("界", 513) },
		func(p *taskEvidencePlan) {
			p.CoverageTargets = append(p.CoverageTargets, make([]taskCoverageTarget, 8)...)
		},
	} {
		invalid := coveragePlanFixture()
		mutate(invalid)
		encoded, err := json.Marshal(invalid)
		require.NoError(t, err)
		_, err = decodeTaskEvidencePlan(string(encoded), nil, 0)
		require.Error(t, err)
	}
	var raw map[string]any
	require.NoError(t, json.Unmarshal(encoded, &raw))
	raw["coverage_targets"].([]any)[0].(map[string]any)["completed"] = true
	encoded, err = json.Marshal(raw)
	require.NoError(t, err)
	_, err = decodeTaskEvidencePlan(string(encoded), nil, 0)
	require.Error(t, err, "a planner cannot self-certify coverage")
}

func TestTaskCoverageTracksActualExecutionAndImmutableHandles(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	e.taskPlan = coveragePlanFixture()
	state := sourceConsistencyState()
	state.RoundSteps[0].ToolCalls[0].Args = map[string]any{"id": "actual-document", "page": 2}
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{
		{ID: "unexecuted-plan", Name: "read_document", Args: map[string]any{"id": "not-read"}},
		{ID: "failed-read", Name: "read_document", Result: &types.ToolResult{Success: false, Error: "Original source temporarily unavailable."}},
	}})
	view := e.taskCoverageReview(state)
	require.Equal(t, 1, view["successful_calls"])
	require.Equal(t, 1, view["failed_calls"])
	require.True(t, view["projection_complete"].(bool))
	require.NotContains(t, view, "completed")
	require.NotContains(t, fmt.Sprint(view), "not-read", "planned arguments do not become execution coverage")
	reader := newReviewEvidenceReader(e.sourceConsistencyMessages(state, nil))
	observations := view["observations"].([]any)
	require.Len(t, observations, 2)
	for _, raw := range observations {
		observation := raw.(map[string]any)
		index, exists := reader.byID[observation["evidence_id"].(string)]
		require.True(t, exists, "every handle names exactly the same immutable evidence used by the existing reviewer")
		record := reader.records[index]
		require.Equal(t, record.ToolCallID, observation["tool_call_id"])
		_, err := reviewPointerValue(record.Value, observation["data_json_pointer"].(string))
		require.NoError(t, err)
	}
	require.NotContains(t, observations[0].(map[string]any), "physical_page", "generic retrieval pagination cannot establish physical coverage")
	require.Contains(t, view["notice"], "offset_space")

	for i := range 100 {
		call := state.RoundSteps[0].ToolCalls[0]
		call.ID = fmt.Sprintf("observed-call-%d", i)
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	}
	view = e.taskCoverageReview(state)
	require.Equal(t, 101, view["successful_calls"])
	require.Greater(t, view["omitted_calls"].(int), 0)
	require.False(t, view["projection_complete"].(bool))
	require.LessOrEqual(t, sourceReviewProjectionSize(view), taskCoverageReviewBudget)
}

func TestDirectDocumentCoverageUsesSameReviewAtNaturalAndExceptionalCompletion(t *testing.T) {
	for _, forceReview := range []bool{false, true} {
		t.Run(fmt.Sprintf("force_review_%v", forceReview), func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The answer omits another returned observation from the requested complete scope.","next_action":"revise_answer"}`)}}
			e := sourceConsistencyEngine(t, model)
			e.taskPlan = coveragePlanFixture()
			state := sourceConsistencyState()
			state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"] = []map[string]any{
				{"chunk_id": "alpha-chunk", "knowledge_id": "actual-document", "content": "Measurement alpha: 14 units.", "page": 3, "offset_space": "physical_page"},
				{"chunk_id": "beta-chunk", "knowledge_id": "actual-document", "content": "Measurement beta: 27 units.", "page": 5, "offset_space": "physical_page"},
			}
			query := "Return all observations from the selected source."
			candidate := "The complete result is measurement alpha: 14 units."
			decision := e.checkCandidate(t.Context(), query, candidate, "", state, nil, forceReview)
			require.NoError(t, decision.Err)
			require.Equal(t, candidateRevise, decision.Disposition)
			require.True(t, decision.SourceReviewAttempted)
			require.Empty(t, decision.Unsupported, "matching the first value must not waive semantic coverage")
			require.Len(t, model.calls, 1, "same existing reviewer, no independent coverage model call")
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			require.Equal(t, query, input["current_user_question"])
			require.Contains(t, input, "task_coverage")
			require.Contains(t, input["advisory_task_plan"], "coverage_targets")
			require.Contains(t, fmt.Sprint(input["document_retrieval_companion"]), "Measurement beta: 27 units.")
			require.Contains(t, model.calls[0][0].Content, "temporary model read range cannot narrow a human request")
			require.Zero(t, e.turnReview.Repairs, "semantic checking does not allocate another repair allowance")
		})
	}
}

func TestSourceReviewApprovalExpiresWithTaskCoverageOrRuntimeScope(t *testing.T) {
	for name, mutate := range map[string]func(*AgentEngine, *types.AgentState){
		"coverage": func(e *AgentEngine, _ *types.AgentState) {
			e.taskPlan.CoverageTargets[0].Criteria = "Only the requested selected observation."
		},
		"text_delivery": func(e *AgentEngine, _ *types.AgentState) {
			e.taskPlan.DeliveryRequirements[0].Criteria = "Return a different requested dimension."
		},
		"human_scope": func(e *AgentEngine, _ *types.AgentState) { e.turnReview.Query = "Use only the appendix observations." },
		"source_scope": func(e *AgentEngine, _ *types.AgentState) {
			e.config.KnowledgeIDs = []string{"different-authorized-source"}
		},
		"source_version": func(_ *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls[0].Result.Data["document"].(map[string]any)["version"] = "changed"
		},
		"permissions": func(e *AgentEngine, _ *types.AgentState) {
			e.config.SharedAgentReadOnly = !e.config.SharedAgentReadOnly
		},
	} {
		t.Run(name, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
			e := sourceConsistencyEngine(t, model)
			e.taskPlan = coveragePlanFixture()
			state := sourceConsistencyState()
			candidate := "The returned observation covers the stated area."
			decision := e.checkCandidate(t.Context(), "Return all observations.", candidate, "", state, nil, false)
			require.NoError(t, decision.Err)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.True(t, e.candidateApprovalCurrent(decision.Candidate, state))
			mutate(e, state)
			require.False(t, e.candidateApprovalCurrent(decision.Candidate, state), "approval is bound to exactly the reviewed task, source evidence and scope")
		})
	}
}

func TestDirectDocumentReviewProtocolFailureDoesNotBecomeApprovalOrSemanticRepair(t *testing.T) {
	responses := make([]mockResponse, answerReviewAttempts)
	for i := range responses {
		responses[i] = sourceConsistencyVerdict(`{"complete":true,"feedback":"A conflicting protocol response."}`)
	}
	model := &mockChat{responses: responses}
	e, state, candidate := directCitationReadFixture(t, model, true)
	decision := e.checkCandidate(t.Context(), "Explain the source indicator.", candidate, "", state, nil, false)
	require.Error(t, decision.Err)
	require.Equal(t, candidateProtocolFailure, decision.Disposition)
	require.True(t, decision.SourceReviewAttempted)
	require.False(t, e.candidateApprovalCurrent(candidate, state))
	require.Zero(t, e.turnReview.Repairs, "protocol retries preserve the separate semantic repair budget")
	require.Len(t, model.calls, answerReviewAttempts)
	for _, messages := range model.calls {
		require.Equal(t, model.calls[0][1].Content, messages[1].Content, "protocol retries inspect the same candidate, coverage and immutable evidence")
	}
}

func TestNonSourceGreetingDoesNotAddSourceReview(t *testing.T) {
	model := &mockChat{}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = explicitTextDeliveryPlan()
	decision := e.checkCandidate(t.Context(), "Hello.", "Hello, how can I help?", "", &types.AgentState{}, nil, false)
	require.NoError(t, decision.Err)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Empty(t, model.calls)
}

func TestSourceStallFallbackCannotDeliverStaleTaskEvidenceOrScope(t *testing.T) {
	for name, mutate := range map[string]func(*AgentEngine, *types.AgentState){
		"human_request": func(e *AgentEngine, _ *types.AgentState) { e.turnReview.Query = "Use the corrected requested scope." },
		"source_version": func(_ *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls[0].Result.Data["version"] = "changed"
		},
		"source_scope": func(e *AgentEngine, _ *types.AgentState) { e.config.KnowledgeIDs = []string{"different-source"} },
	} {
		t.Run(name, func(t *testing.T) {
			e := sourceConsistencyEngine(t, &mockChat{})
			state := sourceConsistencyState()
			rememberUnverifiedCitationFixture(t, e, state, "An earlier source-based draft.")
			_, current := e.currentUnverifiedDelivery(t.Context(), state)
			require.True(t, current, "same-task numerically grounded warning delivery remains permitted")
			mutate(e, state)
			e.commitTerminalResult(t.Context(), state, "session", sourceProgressStalledFallback, "", "stalled", false)
			require.Equal(t, sourceProgressStalledFallback, state.FinalAnswer, "resource exhaustion cannot publish a stale draft under a warning")
		})
	}
}

func TestPersistedSteerInvalidatesCandidateCachesAndRetainsEvidenceAndBudgets(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	state := sourceConsistencyState()
	rememberUnverifiedCitationFixture(t, e, state, "An earlier source-based draft.")
	e.turnReview.Repairs, e.turnReview.Checks = 2, 3
	evidenceBefore := reviewDigest(e.sourceConsistencyMessages(state, nil))
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: e.turnReview.Query}}
	e.SetSteerSink(&fakeSteerSink{queued: []map[string]any{steerEntry("corrected-scope", "Use the corrected source scope.")}})
	messages := append([]chat.Message(nil), e.reviewConversation...)
	require.Equal(t, 1, e.drainSteerMessages(t.Context(), state, &messages, "session", "message"))
	require.Empty(t, e.turnReview.UnverifiedCandidate)
	require.Empty(t, e.turnReview.UnverifiedCheckedCandidate)
	require.Empty(t, e.turnReview.Last.CandidateHash)
	require.Equal(t, 2, e.turnReview.Repairs, "steering never opens a second repair allowance")
	require.Equal(t, 3, e.turnReview.Checks)
	require.Equal(t, evidenceBefore, reviewDigest(e.sourceConsistencyMessages(state, nil)), "real observations remain available for a newly reviewed answer")
	require.Contains(t, fmt.Sprint(e.semanticReviewConversation(nil)), "Use the corrected source scope.")
}
