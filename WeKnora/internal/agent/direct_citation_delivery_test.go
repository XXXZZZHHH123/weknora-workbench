package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func directCitationReadFixture(t *testing.T, model *mockChat, enabled bool) (*AgentEngine, *types.AgentState, string) {
	t.Helper()
	e := newTestEngine(t, model, withCitationsEnabled(enabled))
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
	e.taskPlan = explicitTextDeliveryPlan()
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "real-direct-read", Name: "read_document", Result: realCitationReadResult(t)}}}}}
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	handle := e.modelContext.CitableChunkHandle("contract-supporting")
	require.NotEmpty(t, handle)
	candidate := e.modelContext.DecodeOutputText(`The indicator measures covered area.<ref id="` + handle + `"/>`)
	return e, state, candidate
}

func TestDirectCitationMissingUsesExistingCriticAndRequiresRevision(t *testing.T) {
	model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("required", "This knowledge fact requires its actual supporting inline citation.")}}
	e, state, _ := directCitationReadFixture(t, model, true)
	decision := e.checkCandidate(t.Context(), "Report the indicator.", "The indicator measures covered area.", "", state, nil, false)
	require.Equal(t, candidateRevise, decision.Disposition, "direct lookup cannot bypass a required citation merely because its text/numbers are supported")
	require.NoError(t, decision.Err)
	require.Equal(t, reviewReviseAnswer, e.answerReviewAction)
	require.Len(t, model.calls, 1, "the existing quick source critic decides citation intent")
	require.Empty(t, state.KnowledgeRefs, "the runtime must not auto-select the read neighbor or append a reference")
	require.Equal(t, 0, e.turnReview.Repairs, "checking opens no new repair budget")
}

func TestDirectCitationValidRetainsShortcutButEveryReferenceMustQualify(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		model := &mockChat{}
		e, state, candidate := directCitationReadFixture(t, model, true)
		decision := e.checkCandidate(t.Context(), "Report the indicator.", candidate, "", state, nil, false)
		require.Equal(t, candidateAccepted, decision.Disposition)
		require.NoError(t, decision.Err)
		require.Empty(t, model.calls, "a grounded direct lookup with a real current citation retains the existing fast path")
		require.True(t, e.candidateApprovalCurrent(candidate, state))
	})
	for _, corrupt := range []bool{false, true} {
		model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("required", "All cited identities must be genuine current supporting sources.")}}
		e, state, candidate := directCitationReadFixture(t, model, true)
		if corrupt {
			// A real changed observation cannot reuse the previous body's
			// eligibility, even when another selected reference is current.
			call := state.RoundSteps[0].ToolCalls[0]
			encoded, err := json.Marshal(call.Result)
			require.NoError(t, err)
			call.Result = nil
			require.NoError(t, json.Unmarshal(encoded, &call.Result))
			call.ID = "changed-direct-body"
			call.Result.Data["chunks"].([]any)[1].(map[string]any)["content"] = "A different actual body."
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
		} else {
			candidate += `<kb chunk_id="unknown-additional-source" />`
		}
		decision := e.checkCandidate(t.Context(), "Report the indicator.", candidate, "", state, nil, false)
		require.Equal(t, candidateRevise, decision.Disposition)
		require.NoError(t, decision.Err)
		require.Len(t, model.calls, 1, "a valid citation cannot hide an additional unknown or conflicted identity")
	}
}

func TestDirectCitationDisabledRetainsShortcut(t *testing.T) {
	model := &mockChat{}
	e, state, candidate := directCitationReadFixture(t, model, false)
	require.Empty(t, uniqueCandidateCitationIDs(candidate), "the real disabled registry removes inline source tags")
	decision := e.checkCandidate(t.Context(), "Report the indicator.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.NoError(t, decision.Err)
	require.Empty(t, model.calls, "disabled citations preserve the authorized direct lookup policy")
}

func TestDirectCitationUnavailableStructuredSourceCanCompleteThroughExistingCritic(t *testing.T) {
	model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("unavailable", "This actual spreadsheet result has no corresponding knowledge chunk citation.")}}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = explicitTextDeliveryPlan()
	data := map[string]any{"kind": "structured_analysis", "source_kind": "xlsx", "status": "ok", "rows": []any{map[string]any{"field": "covered area", "unit": "hectares"}}}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "actual-direct-spreadsheet", Name: "structured_query_series", Result: &types.ToolResult{Success: true, Data: data, Output: string(encoded)}}}}}}
	decision := e.checkCandidate(t.Context(), "Report the indicator.", "The returned field measures covered area in hectares.", "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.NoError(t, decision.Err)
	require.Len(t, model.calls, 1, "unavailable is a semantic decision in the existing critic, never a fabricated chunk or another model stage")
	require.True(t, e.candidateApprovalCurrent(decision.Candidate, state))
	require.Empty(t, e.finalAnswerKnowledgeReferences(state))
}
