package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func chartDeliveryPlan() *taskEvidencePlan {
	return &taskEvidencePlan{ReasoningDepth: "direct", DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: "Plot the requested observed measure against its actual reporting dimension."}}}
}

func explicitTextDeliveryPlan() *taskEvidencePlan {
	return &taskEvidencePlan{ReasoningDepth: "direct", DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Criteria: "Report the requested source-supported result in text."}}}
}

func deliveryVerdict(status string) mockResponse {
	return sourceConsistencyVerdict(`{"complete":true,"assessment":"The actual candidate and current source evidence were considered.","audit":[{"claim":"The requested response is supported at the returned scope.","candidate_statement":"The provided result and its limitation.","candidate_fidelity":"as_written","basis":"limitation","assessment":"The current evidence supports the stated limitation.","evidence":[]}],"delivery_check":{"status":"` + status + `","assessment":"Checked the requested format against current execution."}}`)
}

func failedChartState() *types.AgentState {
	state := sourceConsistencyState()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "failed-render", Name: renderChartToolName, Args: map[string]any{"operation": "render"}, Result: &types.ToolResult{Success: false, Error: "attachment save failed", Data: map[string]any{"kind": "data_chart", "status": "error"}}}}})
	return state
}

func TestNonTextDeliveryPreservesFailedActionExplanation(t *testing.T) {
	const draft = "The requested chart was not created because attachment saving failed.\n\nThe available source table follows."
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_failed_action", true: "explicit_contract"}[declared], func(t *testing.T) {
			response := sourceAcceptedVerdict()
			plan := &taskEvidencePlan{ReasoningDepth: "direct"}
			if declared {
				response, plan = deliveryVerdict("blocked"), chartDeliveryPlan()
			}
			model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{"The available source table follows."}, []string{"The requested chart was not created because attachment saving failed."})}, mockChat: mockChat{responses: []mockResponse{response}}}
			e := sourceConsistencyEngine(t, &model.mockChat)
			e.chatModel, e.config.CandidateAdditionsEnabled, e.taskPlan = model, true, plan
			decision := e.checkCandidate(t.Context(), "Use those observations in the requested presentation.", draft, "", failedChartState(), nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.Equal(t, draft, decision.Candidate)
			require.Empty(t, model.selectionCalls, "an honest unresolved action is not a presentation deletion")
			require.Len(t, model.calls, 1, "direct reasoning never skips delivery/source review")
		})
	}
}

func TestChartContractCannotAcceptTableAsDeliveredChart(t *testing.T) {
	model := &mockChat{responses: []mockResponse{deliveryVerdict("delivered")}}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = chartDeliveryPlan()
	decision := e.checkCandidate(t.Context(), "Provide the requested visual.", "The observed values are in this table.", "", sourceConsistencyState(), nil, false)
	require.Equal(t, candidateRevise, decision.Disposition, "a claimed acceptance cannot substitute a table for a missing saved chart")
	require.Contains(t, e.answerReviewFeedback, "图")
	require.Len(t, model.calls, 1, "missing output is a revision, not a protocol retry")
}

func TestMissingDeliveryCheckNeverApprovesNonTextContract(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}})
	e.taskPlan = chartDeliveryPlan()
	repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Complete the requested visual.", "The values are available.", sourceConsistencyState(), 0)
	require.True(t, repair)
	require.ErrorContains(t, err, "delivery_check")
}

func TestDeliveryContractAloneChecksCompletionWithoutFetchedSources(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":true,"delivery_check":{"status":"delivered","assessment":"A table was returned."}}`)}}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = chartDeliveryPlan()
	repair, err := e.answerNeedsRepair(t.Context(), "Create the requested visual.", "Here is a table of the values.", "", &types.AgentState{})
	require.NoError(t, err)
	require.True(t, repair)
	require.Len(t, model.calls, 1)
}

func TestActualArtifactExecutionKeepsCriticWithoutNewPlannerContract(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The chart uses technical row IDs instead of the requested dimension.","next_action":"revise_answer"}`)}}
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	state := &types.AgentState{RoundSteps: numericChartSteps()}
	decision := e.checkCandidate(t.Context(), "Use the requested observations.", state.RoundSteps[0].ToolCalls[1].Result.Output, "", state, nil, false)
	require.Equal(t, candidateRevise, decision.Disposition)
	require.Len(t, model.calls, 1, "a saved image alone grants no semantic approval")
}

func TestChartDeliveryTaskRevisionInvalidatesPriorApproval(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{deliveryVerdict("blocked")}})
	e.taskPlan = chartDeliveryPlan()
	state := failedChartState()
	const candidate = "The requested chart could not be created because attachment saving failed."
	decision := e.checkCandidate(t.Context(), "Present those observations.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.True(t, e.candidateApprovalCurrent(candidate, state))
	e.taskPlan.DeliveryRequirements[0].Criteria = "Plot a different source scope."
	require.False(t, e.candidateApprovalCurrent(candidate, state), "an approval belongs to its exact delivery contract")
}

func TestChartCompanionExposesActualAxesAndCompleteSelectedRows(t *testing.T) {
	state := &types.AgentState{RoundSteps: numericChartSteps()}
	data := state.RoundSteps[0].ToolCalls[1].Result.Data
	data["chart_spec"] = map[string]any{"x": 0, "y": []int{2}, "type": "bar"}
	data["selected_columns"] = []any{map[string]any{"name": "_row_id"}, map[string]any{"name": "column_2"}}
	data["selected_rows"] = [][]string{{"source-row-a", "1961"}, {"source-row-b", "214.6"}}
	data["source_mapping"] = map[string]any{"orientation": "source_rows"}
	e := sourceConsistencyEngine(t, &mockChat{})
	snapshot := e.buildSourceReviewSnapshot(t.Context(), "![Chart]("+numericChartHandle+")", state, nil)
	companion := snapshot.input["chart_delivery_companion"].(map[string]any)
	require.Equal(t, true, companion["projection_complete"])
	entry := companion["entries"].([]any)[0].(map[string]any)
	require.NotEmpty(t, entry["evidence_id"])
	require.Equal(t, data["selected_rows"], entry["data"].(map[string]any)["selected_rows"])
	require.Equal(t, data["chart_spec"], entry["data"].(map[string]any)["chart_spec"])
	require.Equal(t, data["source_mapping"], entry["data"].(map[string]any)["source_mapping"])
}

func TestChartCompanionLargeRowsKeepExactReaderPath(t *testing.T) {
	state := &types.AgentState{RoundSteps: numericChartSteps()}
	state.RoundSteps[0].ToolCalls[1].Result.Data["selected_rows"] = [][]string{{strings.Repeat("large", chartDeliveryReviewBudget)}}
	e := sourceConsistencyEngine(t, &mockChat{})
	snapshot := e.buildSourceReviewSnapshot(t.Context(), "![Chart]("+numericChartHandle+")", state, nil)
	companion := snapshot.input["chart_delivery_companion"].(map[string]any)
	require.Equal(t, false, companion["projection_complete"])
	entry := companion["entries"].([]any)[0].(map[string]any)
	require.Contains(t, entry["omitted_pointers"], "/data/selected_rows")
	require.NotContains(t, entry["data"].(map[string]any), "selected_rows")
	require.NotNil(t, snapshot.reader, "capacity cannot hide selected data from the semantic reviewer")
	body, err := json.Marshal(companion)
	require.NoError(t, err)
	require.Less(t, len(body), chartDeliveryReviewBudget)
}

func TestDeliveryVerdictRejectedWithoutLosingEvidenceRecovery(t *testing.T) {
	content := `{"complete":false,"feedback":"The original dimension is missing.","next_action":"collect_evidence","delivery_check":{"status":"missing","assessment":"No complete requested chart exists."}}`
	require.True(t, reviewRejectsCandidate(content))
	require.True(t, reviewRequestsAdditionalEvidence(content))
}
