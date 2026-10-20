package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTaskAwareReviewDoesNotRequireMarkdownTranscription(t *testing.T) {
	snapshot, witness := acceptedReceiptSnapshotFixture(t)
	snapshot.input["advisory_task_plan"] = map[string]any{"goals": []string{"Describe the survey scope."}}
	answer := "| Survey | Scope |\n|---|---|\n| First | **Eastern region only** |"
	check := reviewClaimCheck{Claim: "The first survey covers the eastern region only.", Basis: "source",
		Assessment: "The observed source identifies the survey scope.", Evidence: []reviewSourceWitness{witness}}
	require.NoError(t, validateReviewReceipt(answer, "The scope and its limitation are preserved.", []reviewClaimCheck{check}, snapshot))
	check.Evidence[0].EvidenceID = "unseen-source"
	require.NoError(t, validateReviewReceipt(answer, "Reviewed", []reviewClaimCheck{check}, snapshot))
	require.Equal(t, 0, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["verified"], "an unknown source identity is never counted as verified")
}

func TestTaskPlanSurvivesReviewAndRevisionButNotNextTurn(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.taskPlan = &taskEvidencePlan{Goals: []string{"Describe the overall pattern."}, CompletionCriteria: []string{"Supported direction and material turning points."}}
	snapshot := e.buildSourceReviewSnapshot(context.Background(), "A bounded summary", &types.AgentState{}, nil)
	plan := snapshot.input["advisory_task_plan"].(map[string]any)
	require.Equal(t, e.taskPlan.Goals, plan["goals"])
	messages := []chat.Message{{Role: "system", Content: "base"}, {Role: "user", Content: `{"unverified_draft":"old","source_tool_results":[]}`}}
	e.attachTaskPlanToReview(messages)
	var revision map[string]any
	require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &revision))
	require.Equal(t, "old", revision["unverified_draft"])
	require.Contains(t, revision, "advisory_task_plan")
	require.Contains(t, revision, "source_tool_results")
	e.clearTaskToolProjection(&messages)
	require.NotNil(t, e.taskPlan, "source capability recovery must preserve the same human task")
	require.NotNil(t, e.taskReviewPlan())
	e.taskPlan = &taskEvidencePlan{Goals: []string{"stale"}}
	e.prepareTaskPlanning()
	require.Nil(t, e.taskPlan, "new execution must not inherit old advisory goals")
}

func TestPlanAssociationsDoNotRequireExhaustiveGoalOrRowAudit(t *testing.T) {
	snapshot, witness := acceptedReceiptSnapshotFixture(t)
	snapshot.input["advisory_task_plan"] = map[string]any{"goals": []string{"Read the survey.", "Describe its scope."}}
	answer := "Only the eastern region is covered."
	checks := []reviewClaimCheck{{Claim: answer, Basis: "source", Assessment: "Scope is explicit.", Evidence: []reviewSourceWitness{witness}}}
	require.NoError(t, validateReviewReceipt(answer, "The requested scope is answered without enumerating rows", checks, snapshot))
	presentation := reviewClaimCheck{Claim: "The prose is readable.", Basis: "presentation", Assessment: "Formatting does not change the conclusion."}
	require.NoError(t, validateReviewReceipt(answer, "Reviewed", append(checks, presentation), snapshot))
	require.Error(t, validateReviewReceipt(answer, "Reviewed", []reviewClaimCheck{presentation}, snapshot))
	checks[0].Evidence[0].EvidenceID = "unseen-source"
	require.NoError(t, validateReviewReceipt(answer, "Reviewed", checks, snapshot))
	require.Equal(t, 0, reviewReceiptWitnessSummary(checks, snapshot)["verified"], "task alignment never counts an unknown identity as verified")
}
