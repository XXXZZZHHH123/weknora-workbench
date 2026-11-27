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

func citationRecoveryPayload(t *testing.T, feedback string) map[string]any {
	t.Helper()
	start := strings.Index(feedback, "{")
	require.GreaterOrEqual(t, start, 0)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(feedback[start:]), &payload))
	return payload
}

func TestCitationRecoveryIdentifiesEachTagAndCurrentAvailableChunk(t *testing.T) {
	full, _, _ := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	unknown := &types.ToolResult{Success: true, Data: map[string]any{"chunks": []map[string]any{{"chunk_id": "identity-unknown", "content": "Body without document identity."}}}}
	failed := &types.ToolResult{Success: false, Data: map[string]any{"chunks": []map[string]any{{"chunk_id": "failed-body", "knowledge_id": "contract-document", "content": "Failed source."}}}}
	state.RoundSteps = append(state.RoundSteps, citationViewState(unknown, failed).RoundSteps...)
	candidate := `A.<kb chunk_id="identity-unknown" /> B.<kb chunk_id="unread" /> C.<evidence evidence_id="unissued" />`
	history := []chat.Message{{Role: "tool", Name: "read_document", Content: `{"success":true,"data":{"chunks":[{"chunk_id":"historical","knowledge_id":"contract-document","content":"A historical body."}]}}`}}
	feedback, counts, _ := e.candidateCitationRecovery(t.Context(), candidate, state, history)
	payload := citationRecoveryPayload(t, feedback)
	issues := payload["issues"].([]any)
	require.Len(t, issues, 3)
	require.Equal(t, 1, counts["source_identity_unknown"])
	require.Equal(t, 1, counts["source_not_observed"])
	require.Equal(t, 1, counts["structured_reference_invalid"])
	for _, raw := range issues {
		issue := raw.(map[string]any)
		require.NotEmpty(t, issue["citation_label"])
		require.NotEmpty(t, issue["reason_code"])
		require.Equal(t, []any{"view-shared"}, issue["available_chunk_ids"])
	}
	anchors := payload["available_chunks"].([]any)
	require.Len(t, anchors, 1)
	anchor := anchors[0].(map[string]any)
	require.Equal(t, "view-shared", anchor["chunk_id"])
	require.NotEmpty(t, anchor["citation_handle"])
	require.NotEmpty(t, anchor["tool_call_id"])
	require.NotEmpty(t, anchor["json_pointer"])
	require.NotContains(t, feedback, "historical")
	require.NotContains(t, feedback, "failed-body")
}

func TestCitationSemanticRecoveryKeepsModelCategoryAndValidatesCandidateIDs(t *testing.T) {
	full, _, _ := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full)
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	candidate := `The finding applies to every interval.<kb chunk_id="view-shared" />`
	reported := []citationSemanticIssue{{CitationID: "view-shared", ReasonCode: "citation_scope_unsupported", Explanation: "The cited passage covers one interval; the claim covers every interval."}}
	feedback, err := e.citationSemanticRecovery(candidate, state, nil, reported)
	require.NoError(t, err)
	payload := citationRecoveryPayload(t, feedback)
	issue := payload["issues"].([]any)[0].(map[string]any)
	require.Equal(t, "citation_scope_unsupported", issue["reason_code"])
	require.Equal(t, `<kb chunk_id="view-shared" />`, issue["citation_label"])
	require.Equal(t, []any{"view-shared"}, issue["available_chunk_ids"], "an available identity does not assert semantic coverage")
	require.Equal(t, reported[0].Explanation, issue["explanation"])
	reported[0].CitationID = "not-in-candidate"
	_, err = e.citationSemanticRecovery(candidate, state, nil, reported)
	require.ErrorContains(t, err, "current candidate citation")
	reported[0].CitationID, reported[0].ReasonCode = "view-shared", "invented_category"
	_, err = e.citationSemanticRecovery(candidate, state, nil, reported)
	require.ErrorContains(t, err, "unknown semantic")
}

func TestSourceReviewRejectionReturnsPerCitationScopeDiagnostic(t *testing.T) {
	full, _, _ := realCitationViewResults(t)
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"citation_check":{"requirement":"required","reason":"The claim needs the actual supporting passage."},"feedback":"Limit the claim to the observed interval or use another genuinely supporting passage.","next_action":"revise_answer","citation_issues":[{"citation_id":"view-shared","reason_code":"citation_scope_unsupported","explanation":"The passage covers one interval, while the adjacent conclusion covers every interval."}]}`)}}
	e := sourceConsistencyEngine(t, model)
	state := citationViewState(full)
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Describe the observed scope.", `Every interval has the finding.<kb chunk_id="view-shared" />`, state, 0)
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, reviewReviseAnswer, e.answerReviewAction)
	require.Contains(t, e.answerReviewFeedback, "Limit the claim")
	require.Contains(t, e.answerReviewFeedback, `"reason_code":"citation_scope_unsupported"`)
	require.Contains(t, e.answerReviewFeedback, `"available_chunk_ids":["view-shared"]`)
}

func TestCitationRecoveryReportsEachForgedScopeLabelAgainstRealChunk(t *testing.T) {
	full, _, _ := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full)
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	for _, tag := range []string{
		`<kb chunk_id="view-shared" kb_id="foreign-library" />`,
		`<kb chunk_id="view-shared" knowledge_base_id="foreign-library" />`,
		`<kb chunk_id="view-shared" knowledge_id="foreign-document" />`,
		`<kb chunk_id="view-shared" chunk_id="view-shared" />`,
		`<kb chunk_id="view-shared" knowledge_id="contract-document" knowledge_id="contract-document" />`,
	} {
		t.Run(tag, func(t *testing.T) {
			candidate := "说明。" + tag
			require.Len(t, e.currentCandidateKnowledgeReferences(candidate, state, nil), 1, "the actual chunk remains real; only the explicit tag scope is forged")
			feedback, counts, needsRead := e.candidateCitationRecovery(t.Context(), candidate, state, nil)
			require.False(t, needsRead, "a tag protocol failure does not require re-fetching the same body")
			require.Equal(t, map[string]int{"citation_scope_invalid": 1}, counts)
			payload := citationRecoveryPayload(t, feedback)
			issues := payload["issues"].([]any)
			require.Len(t, issues, 1)
			issue := issues[0].(map[string]any)
			require.Equal(t, tag, issue["citation_label"])
			require.Equal(t, "view-shared", issue["citation_id"])
			require.Equal(t, "citation_scope_invalid", issue["reason_code"])
			require.Equal(t, []any{"view-shared"}, issue["available_chunk_ids"])
			require.Equal(t, float64(len("说明。")), issue["candidate_start_byte"])
		})
	}
}

func TestCitationRecoveryKeepsRepeatedScopeFailuresSeparate(t *testing.T) {
	full, _, _ := realCitationViewResults(t)
	e := sourceConsistencyEngine(t, &mockChat{})
	state := citationViewState(full)
	e.appendToolResults(emptyMessages(), state.RoundSteps[0])
	first := `<kb chunk_id="view-shared" kb_id="foreign-library" />`
	second := `<kb chunk_id="view-shared" knowledge_id="foreign-document" />`
	candidate := `合法。<kb chunk_id="view-shared" kb_id="contract-library" />` + " 错误一。" + first + " 错误二。" + second
	feedback, counts, _ := e.candidateCitationRecovery(t.Context(), candidate, state, nil)
	require.Equal(t, map[string]int{"citation_scope_invalid": 2}, counts)
	issues := citationRecoveryPayload(t, feedback)["issues"].([]any)
	require.Len(t, issues, 2)
	require.Equal(t, first, issues[0].(map[string]any)["citation_label"])
	require.Equal(t, second, issues[1].(map[string]any)["citation_label"])
}
