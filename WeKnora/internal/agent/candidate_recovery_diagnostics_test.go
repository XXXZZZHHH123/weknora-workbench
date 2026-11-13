package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCandidatePageProjectionConflictRequestsFullWitnessAndKeepsChecks(t *testing.T) {
	full, page, other := realCitationViewResults(t)
	model := &mockChat{}
	e := sourceConsistencyEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	state := citationViewState(page, other)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	candidate := `该页描述面积用量。<kb chunk_id="view-shared" />`
	decision := e.checkCandidate(t.Context(), "说明两页的指标含义。", candidate, "", state, nil, false)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	require.Equal(t, "citation_identity_invalid", decision.ReasonCode)
	require.Equal(t, 1, decision.CitationIssueCounts["partial_views_need_full_chunk"])
	require.Equal(t, reviewCollectEvidence, e.answerReviewAction)
	require.Contains(t, e.answerReviewFeedback, `"read_document_args":{"content_offset":0,"id":"chunk://`)
	require.Contains(t, e.answerReviewFeedback, "WITHOUT page/pages/query")
	require.Empty(t, model.calls, "identity recovery adds no critic call")
	require.False(t, e.candidateApprovalCurrent(candidate, state))
	// Repeating the same physical pages never grants approval.
	state.RoundSteps = append(state.RoundSteps, citationViewState(page).RoundSteps...)
	require.Equal(t, candidateNeedSource, e.checkCandidate(t.Context(), "说明两页的指标含义。", candidate, "", state, nil, false).Disposition)
	// A real full current witness allows the unchanged proof/hash/version checks
	// to reconcile the page projections, without discarding their source scopes.
	step := citationViewState(full).RoundSteps[0]
	step.ToolCalls[0].ID = "full-current-witness"
	state.RoundSteps = append(state.RoundSteps, step)
	e.appendToolResults(emptyMessages(), step)
	decision = e.checkCandidate(t.Context(), "说明两页的指标含义。", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Len(t, e.currentCandidateKnowledgeReferences(candidate, state, nil), 1)
	require.Empty(t, model.calls)
}

func TestCandidateProjectionRecoveryDoesNotReconcileVersionConflict(t *testing.T) {
	_, page, other := realCitationViewResults(t)
	other.Data["chunks"].([]map[string]any)[0]["content_revision"] = 19
	e := sourceConsistencyEngine(t, &mockChat{})
	e.config.AnswerReviewMode = "standard"
	state := citationViewState(page, other)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	d := e.checkCandidate(t.Context(), "说明来源", `面积。<kb chunk_id="view-shared" />`, "", state, nil, false)
	require.Equal(t, candidateRevise, d.Disposition)
	require.Equal(t, 1, d.CitationIssueCounts["conflicting_source_views"])
	require.NotContains(t, e.answerReviewFeedback, "read_document_args")
}

func TestRepeatedNumericRecoveryTargetsPersistentTokenWithoutDroppingAnalysis(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	e.config.AnswerReviewMode = "standard"
	data := map[string]any{"kind": "structured_analysis", "status": "ok", "rows": [][]any{{685}, {742.25}}}
	raw, _ := json.Marshal(data)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "observed", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: data, Output: string(raw)}}}}}}
	first := "走势回升，区间下界680、上界742.25。"
	d := e.checkCandidate(t.Context(), "绘图并简析变化", first, "", state, nil, false)
	require.Equal(t, candidateNumericFailure, d.Disposition)
	require.Equal(t, []string{"680"}, d.Unsupported)
	require.Empty(t, d.RepeatedUnsupported)
	revised := "走势总体回升，最低680、最高742.25。"
	d = e.checkCandidate(t.Context(), "绘图并简析变化", revised, "", state, nil, false)
	require.Equal(t, candidateNumericFailure, d.Disposition)
	require.Equal(t, []string{"680"}, d.RepeatedUnsupported)
	messages := []chat.Message{{Role: "user", Content: "绘图并简析变化"}}
	e.resumeNumericValidationRepair(t.Context(), state, &messages, &loopGuards{structuredEvidenceRepairs: 2}, "session", revised, d.NumericFeedback)
	feedback := messages[len(messages)-1].Content
	require.Contains(t, feedback, `上次修订后仍未匹配的数字：["680"]`)
	require.Contains(t, feedback, "简析、图件必须保留")
	require.Contains(t, feedback, "不能把它或任何原数值填入calculate_source_values的ref")
	require.Contains(t, feedback, "最低680")
	require.Equal(t, revised, messages[len(messages)-2].Content, "repair receives the actual latest candidate")
	// The repair never modifies values or the verdict in place.
	corrected := strings.ReplaceAll(revised, "最低680、", "最低685、")
	d = e.checkCandidate(t.Context(), "绘图并简析变化", corrected, "", state, nil, false)
	require.Equal(t, candidateAccepted, d.Disposition)
	require.Empty(t, d.Unsupported)
	require.Empty(t, d.RepeatedUnsupported)
	require.Contains(t, d.Candidate, "走势总体回升")
}
