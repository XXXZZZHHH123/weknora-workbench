package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceProgressFixture(t *testing.T, action string) (*AgentEngine, *types.AgentState, *mockChat, []chat.Message, *loopGuards) {
	e, seed, model := discoveryReviewFixture(t, discoveryReply(action, func() string {
		if action == "recover" {
			return "Reconsider the original qualitative goal using an available authorized source route; the observed pending mapping does not establish absence."
		}
		return ""
	}()))
	e.taskPlan = &taskEvidencePlan{Goals: []string{"Explain the requested pattern."}}
	e.taskProjection = &taskToolProjection{selected: map[string]bool{"structured_list_series": true}}
	base := agenttools.NewStructuredDataTool()
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, map[string]any{"tools": []any{map[string]any{"name": "list_series", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}) {
		e.toolRegistry.RegisterTool(tool)
	}
	state := &types.AgentState{CurrentRound: 1, RoundSteps: []types.AgentStep{seed.RoundSteps[0], seed.RoundSteps[0], seed.RoundSteps[0]}}
	messages := []chat.Message{{Role: "system", Content: "Keep actual scope."}, {Role: "user", Content: "Explain the source pattern."}, {Role: "assistant", Content: "Investigating the selected structured source."}}
	e.reviewConversation = conversationThroughLastHuman(messages)
	e.reviewConversationReady = true
	return e, state, model, messages, &loopGuards{}
}

func TestSourceProgressReviewRecoversBeforeCandidateWithoutResettingIterations(t *testing.T) {
	e, state, model, messages, guards := sourceProgressFixture(t, "recover")
	before := reviewDigest(state.RoundSteps)
	query := "Explain the original source pattern qualitatively."
	e.config.KnowledgeIDs = []string{"only-file"}
	e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "only-file", Title: "Only permitted source"}}
	e.reviewSourceProgress(t.Context(), query, state, &messages, guards, "session")
	require.Len(t, model.calls, 1)
	require.Equal(t, "progress", e.turnReview.DiscoveryReview.Phase)
	require.Equal(t, "recover", e.turnReview.DiscoveryReview.Action)
	require.Empty(t, e.turnReview.DiscoveryReview.CandidateHash)
	require.Zero(t, e.turnReview.Checks, "a plan review is not a candidate check")
	require.Equal(t, 1, e.turnReview.Repairs)
	require.Equal(t, 1, guards.evidenceRecoveryAttempts)
	require.Equal(t, 1, state.CurrentRound)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Nil(t, e.taskProjection, "existing recovery exposes registered capabilities, never new permissions")
	require.Contains(t, messages[len(messages)-1].Content, "original qualitative goal")
	require.Contains(t, model.calls[0][1].Content, query)
	require.Contains(t, model.calls[0][1].Content, "Only permitted source")
	require.Contains(t, model.calls[0][1].Content, "search_knowledge")
	require.NotContains(t, model.calls[0][1].Content, "forbidden_unregistered_tool")
	require.Contains(t, model.calls[0][0].Content, "there is no candidate answer yet")
	require.Equal(t, 512, model.opts[0].MaxCompletionTokens)
	require.Empty(t, model.opts[0].Tools)
	e.reviewSourceProgress(t.Context(), query, state, &messages, guards, "session")
	require.Len(t, model.calls, 1)
	decision := candidateDecision{Disposition: candidateAccepted, Candidate: "Supported final explanation", CandidateHash: "real-final"}
	require.Equal(t, decision, e.checkDiscoveryOnlyDelivery(t.Context(), query, decision, state, messages), "spent progress receipt does not masquerade as a final draft verdict")
	require.Len(t, model.calls, 1)
}

func TestSourceProgressReviewKeepsCorrectStructuredInvestigationAndExactTask(t *testing.T) {
	for _, query := range []string{"Compute the exact monthly average from only the selected table; a chart estimate is insufficient.", "Continue the currently bound structured analysis and report the observed pattern."} {
		t.Run(query, func(t *testing.T) {
			e, state, model, messages, guards := sourceProgressFixture(t, "keep")
			before := reviewDigest(messages)
			e.reviewSourceProgress(t.Context(), query, state, &messages, guards, "session")
			require.Len(t, model.calls, 1)
			require.Equal(t, "keep", e.turnReview.DiscoveryReview.Action)
			require.Zero(t, e.turnReview.Repairs)
			require.Zero(t, guards.evidenceRecoveryAttempts)
			require.NotNil(t, e.taskProjection)
			require.Equal(t, before, reviewDigest(messages))
			// A progress keep must not waive the later mandatory numeric check.
			numericState, conversation, actualQuery := numericRecoveryFixture()
			decision := e.checkCandidate(t.Context(), actualQuery, "The measured value is 987654.321.", "", numericState, conversation, false)
			require.Equal(t, candidateNumericFailure, decision.Disposition)
			require.Contains(t, decision.Unsupported, "987654.321")
			citation := e.checkCandidate(t.Context(), actualQuery, `The source reports a pattern. <kb chunk_id="unread-source" />`, "", numericState, conversation, false)
			require.Equal(t, "citation_identity_invalid", citation.ReasonCode)
			require.NotEqual(t, candidateAccepted, citation.Disposition)
			require.Len(t, model.calls, 1)
		})
	}
}

func TestSourceProgressReceiptDoesNotSkipCandidateSemanticEditor(t *testing.T) {
	progressEngine, state, _, messages, guards := sourceProgressFixture(t, "keep")
	progressEngine.reviewSourceProgress(t.Context(), "Report the observed measure.", state, &messages, guards, "session")
	e, numericState, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{"tonnes", "kg"}))
	e.turnReview.DiscoveryReview = progressEngine.turnReview.DiscoveryReview
	decision := e.checkCandidate(t.Context(), "Report the observed measure.", "The observation is 12.500 tonnes.", "", numericState, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, "The observation is 12.500 kg.", decision.Candidate)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.Len(t, model.calls, 1, "the semantic editor still runs after the shared scope-review call was spent")
	require.Equal(t, "progress", e.turnReview.DiscoveryReview.Phase)
}

func TestSourceProgressReviewRequiresNativeBatchesAndRemainingBudget(t *testing.T) {
	for _, scenario := range []string{"no_tools", "one_batch_many_calls", "two_batches", "fake_registration", "json_descriptor", "action_mislabeled_read", "denied_execution", "cancelled", "repairs_spent", "recovery_spent", "review_spent", "new_human_task", "strict"} {
		t.Run(scenario, func(t *testing.T) {
			e, state, model, messages, guards := sourceProgressFixture(t, "recover")
			ctx := t.Context()
			switch scenario {
			case "no_tools":
				state.RoundSteps = nil
			case "one_batch_many_calls":
				state.RoundSteps = []types.AgentStep{{ToolCalls: append(append([]types.ToolCall{}, state.RoundSteps[0].ToolCalls...), state.RoundSteps[0].ToolCalls...)}}
			case "two_batches":
				state.RoundSteps = state.RoundSteps[:2]
			case "fake_registration":
				e.toolRegistry = agenttools.NewToolRegistry()
				e.toolRegistry.RegisterTool(newCountingTool("structured_list_series"))
			case "json_descriptor":
				state.RoundSteps[0].ToolCalls[0].Result.Data["_structured_execution"] = map[string]any{"canonical_operation": "list_series", "effect_class": "read"}
			case "action_mislabeled_read":
				for _, tool := range agenttools.NewStructuredReadOperationTools(agenttools.NewStructuredDataTool(), map[string]any{"tools": []any{map[string]any{"name": "confirm_fields", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}) {
					e.toolRegistry.RegisterTool(tool)
				}
				call := &state.RoundSteps[0].ToolCalls[0]
				call.Name = "structured_confirm_fields"
				call.Result.Data["_structured_execution"] = agenttools.StructuredOperationDescriptor{Operation: "confirm_fields", EffectClass: "read"}
				_, native := e.sourceProgressOperation(*call)
				require.False(t, native, "the current registered effect class outranks mislabeled result metadata")
			case "denied_execution":
				state.RoundSteps[0].ToolCalls[0].Result.Data["execution_started"] = false
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "repairs_spent":
				e.turnReview.Repairs = maxTurnCandidateRepairs
			case "recovery_spent":
				guards.evidenceRecoveryAttempts = maxEvidenceRecoveryAttempts
			case "review_spent":
				e.turnReview.DiscoveryReview = &discoveryDeliveryReviewTurn{Phase: "delivery", Action: "keep"}
			case "new_human_task":
				e.taskPlan = nil
				e.resetDocumentTurnProgress(guards, "A new actual requirement", state)
			case "strict":
				e.config.AnswerReviewMode = "strict"
			}
			before := reviewDigest(messages)
			e.reviewSourceProgress(ctx, "Explain the source.", state, &messages, guards, "session")
			require.Empty(t, model.calls)
			require.Equal(t, before, reviewDigest(messages))
		})
	}
}

func TestSourceProgressReviewErrorsConsumeOnlySharedCallNotRepairs(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		e, state, model, messages, guards := sourceProgressFixture(t, "keep")
		ctx := t.Context()
		if deadline {
			blocking := &cancelledDiscoveryChat{}
			e.chatModel = blocking
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
		} else {
			model.responses = []mockResponse{deliveryReply("bad json", "stop", types.TokenUsage{})}
		}
		before := reviewDigest(messages)
		started := time.Now()
		e.reviewSourceProgress(ctx, "Explain the source.", state, &messages, guards, "session")
		require.Less(t, time.Since(started), time.Second)
		require.NotNil(t, e.turnReview.DiscoveryReview)
		require.Equal(t, "progress", e.turnReview.DiscoveryReview.Phase)
		require.Empty(t, e.turnReview.DiscoveryReview.Action)
		require.Zero(t, e.turnReview.Repairs)
		require.Equal(t, before, reviewDigest(messages))
	}
}

func TestSourceProgressSummaryIsBoundedAndPreservesActualDiagnostics(t *testing.T) {
	e, state, _, _, _ := sourceProgressFixture(t, "keep")
	for i := 0; i < 20; i++ {
		state.RoundSteps = append(state.RoundSteps, state.RoundSteps[0])
	}
	batches, view := e.sourceExecutionProgress(state)
	require.Equal(t, 23, batches)
	require.Equal(t, 15, view["omitted_operations"])
	require.Len(t, view["recent_source_operations"], 8)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.Contains(t, string(raw), "needs_mapping")
	require.Contains(t, string(raw), "cadence_is_not_failure")
}

func TestSourceProgressReviewSurvivesUnavailableAdvisoryPlan(t *testing.T) {
	for _, action := range []string{"recover", "keep"} {
		t.Run(action, func(t *testing.T) {
			e, state, model, messages, guards := sourceProgressFixture(t, action)
			// This is the documented planning fallback: no fabricated plan and
			// the real registered capabilities remain available.
			e.taskPlan = nil
			e.taskProjection = nil
			e.config.KnowledgeIDs = []string{"only-file"}
			e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "only-file", Title: "Only permitted source"}}
			before := reviewDigest(state.RoundSteps)
			e.reviewSourceProgress(t.Context(), "Explain the observed pattern using only the selected source.", state, &messages, guards, "session")
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, `"advisory_plan":null`)
			require.Contains(t, model.calls[0][1].Content, "Only permitted source")
			require.Equal(t, action, e.turnReview.DiscoveryReview.Action)
			require.Nil(t, e.taskPlan, "the review cannot invent an advisory plan")
			require.Equal(t, before, reviewDigest(state.RoundSteps))
			if action == "recover" {
				require.Equal(t, 1, guards.evidenceRecoveryAttempts)
				require.Equal(t, 1, e.turnReview.Repairs)
			} else {
				require.Zero(t, guards.evidenceRecoveryAttempts)
				require.Zero(t, e.turnReview.Repairs)
			}
			e.reviewSourceProgress(t.Context(), "Explain the observed pattern using only the selected source.", state, &messages, guards, "session")
			require.Len(t, model.calls, 1)
		})
	}
}

func TestSourceProgressNewHumanBoundaryUsesOnlyNewExecutions(t *testing.T) {
	e, state, model, messages, guards := sourceProgressFixture(t, "keep")
	e.taskPlan = nil
	oldStep := state.RoundSteps[0]
	e.resetDocumentTurnProgress(guards, "New actual request", state)
	e.reviewSourceProgress(t.Context(), "New actual request", state, &messages, guards, "session")
	require.Empty(t, model.calls, "old executions cannot trigger the new task's cadence")
	state.RoundSteps = append(state.RoundSteps, oldStep, oldStep)
	e.reviewSourceProgress(t.Context(), "New actual request", state, &messages, guards, "session")
	require.Empty(t, model.calls)
	state.RoundSteps = append(state.RoundSteps, oldStep)
	e.reviewSourceProgress(t.Context(), "New actual request", state, &messages, guards, "session")
	require.Len(t, model.calls, 1)
	require.Contains(t, model.calls[0][1].Content, `"source_batches":3`)
	require.NotContains(t, model.calls[0][1].Content, `"source_batches":6`)
	require.Equal(t, 12, state.TurnUsage.CompletionTokens, "usage from the current execution view is retained")
}
