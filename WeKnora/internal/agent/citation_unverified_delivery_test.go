package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const citationWarningDraft = `面积描述。<kb chunk_id="view-shared" /> 另段说明。<kb chunk_id="other-current" />`

func citationWarningFixture(t *testing.T, model *mockChat) (*AgentEngine, *types.AgentState) {
	t.Helper()
	_, page, other := realCitationViewResults(t)
	valid := &types.ToolResult{Success: true, Data: map[string]any{
		"display_type": "knowledge_chunks_list",
		"knowledge_id": "contract-document", "knowledge_base_id": "contract-library",
		"chunks": []map[string]any{{"chunk_id": "other-current", "knowledge_id": "contract-document", "knowledge_base_id": "contract-library",
			"content_revision": 4, "content_offset": 0, "content_offset_space": "chunk_unicode_runes", "content_complete": true, "content": "另段说明。"}},
	}}
	e := sourceConsistencyEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	state := citationViewState(page, other, valid)
	for _, step := range state.RoundSteps {
		e.appendToolResults(emptyMessages(), step)
	}
	return e, state
}

func TestCitationUnverifiedAfterRepairsKeepsLastDraftAndOnlyValidReferences(t *testing.T) {
	model := &mockChat{}
	e, state := citationWarningFixture(t, model)
	modelDraft := `面积描述。<ref id="` + e.modelContext.CitableChunkHandle("view-shared") + `" /> 另段说明。<ref id="` + e.modelContext.CitableChunkHandle("other-current") + `" />`
	model.responses = []mockResponse{chatAnswer(modelDraft), chatAnswer(modelDraft), chatAnswer("末稿：" + modelDraft)}
	require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "说明已读取的内容。", state, "citation", nil))
	require.Len(t, model.calls, 3)
	require.Equal(t, 2, e.turnReview.Repairs)
	require.True(t, strings.HasPrefix(state.FinalAnswer, `末稿：面积描述。〔引用待核对〕 另段说明。`), state.FinalAnswer)
	require.Contains(t, state.FinalAnswer, `chunk_id="other-current"`)
	require.Equal(t, 1, e.turnReview.CitationUnverifiedDelivery.MarkedCitations)
	require.NotContains(t, state.FinalAnswer, `chunk_id="view-shared"`)
	require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
	e.emitCompletionEvent(t.Context(), state, "citation", "message", time.Now())
	require.Len(t, state.KnowledgeRefs, 1)
	require.Equal(t, "other-current", state.KnowledgeRefs[0].ID)
	require.Empty(t, state.EvidenceRefs)
}

func TestCitationUnverifiedCombinesNumericMarkersWithSingleNotice(t *testing.T) {
	e, state := citationWarningFixture(t, &mockChat{})
	for id, views := range currentCitationSourceViews(state) {
		require.True(t, e.citationWarningIdentitySafe(id, views), "%s: %#v", id, views)
	}
	numeric, _, _ := numericRecoveryFixture()
	state.RoundSteps = append(state.RoundSteps, numeric.RoundSteps...)
	draft := "观测138.67，附加997。" + citationWarningDraft
	d := e.checkCandidate(t.Context(), "说明观测。", draft, "", state, nil, false)
	require.Equal(t, "citation_identity_invalid", d.ReasonCode)
	e.commitTerminalResult(t.Context(), state, "citation", answerReviewUnavailableFallback, "", "failed", false)
	require.Contains(t, state.FinalAnswer, "观测138.67，附加997〔数字待核对〕。")
	require.Contains(t, state.FinalAnswer, "面积描述。〔引用待核对〕")
	require.Equal(t, 1, strings.Count(state.FinalAnswer, "> 提示："))
	require.Equal(t, 1, e.turnReview.CitationUnverifiedDelivery.MarkedNumbers)
}

func TestCitationUnverifiedRejectsUnknownUnauthorizedStaleOrForgedSources(t *testing.T) {
	for _, mutation := range []string{"unknown", "library", "document", "revision", "initial-scope", "scope", "forged-tag", "task", "evidence", "malformed"} {
		t.Run(mutation, func(t *testing.T) {
			e, state := citationWarningFixture(t, &mockChat{})
			draft := citationWarningDraft
			row := state.RoundSteps[1].ToolCalls[0].Result.Data["chunks"].([]map[string]any)[0]
			switch mutation {
			case "unknown":
				draft += `<kb chunk_id="invented-id" />`
			case "library":
				row["knowledge_base_id"] = "unauthorized-library"
			case "document":
				row["knowledge_id"] = "different-document"
			case "revision":
				row["content_revision"] = 9
			case "initial-scope":
				e.config.KnowledgeBases = []string{"different-library"}
			case "forged-tag":
				draft = strings.Replace(draft, `chunk_id="view-shared"`, `chunk_id="view-shared" kb_id="foreign-library"`, 1)
			case "malformed":
				draft += `<kb chunk_id="view-shared"`
			}
			e.checkCandidate(t.Context(), "说明内容。", draft, "", state, nil, false)
			switch mutation {
			case "scope":
				e.config.KnowledgeBases = []string{"different-library"}
			case "task":
				e.turnReview.Query = "新的任务。"
			case "evidence":
				state.RoundSteps[0].ToolCalls[0].Result.Data["changed"] = true
			}
			e.commitTerminalResult(t.Context(), state, "citation", answerReviewUnavailableFallback, "", "failed", false)
			require.False(t, terminalCandidateFallback(state.FinalAnswer))
			require.NotContains(t, state.FinalAnswer, "面积描述")
			require.NotContains(t, state.FinalAnswer, "<kb")
			require.Nil(t, e.turnReview.CitationUnverifiedDelivery)
		})
	}
}

func TestCitationUnverifiedRechecksReceiptBeforePublishing(t *testing.T) {
	e, state := citationWarningFixture(t, &mockChat{})
	e.checkCandidate(t.Context(), "说明内容。", citationWarningDraft, "", state, nil, false)
	e.commitTerminalResult(t.Context(), state, "citation", answerReviewUnavailableFallback, "", "failed", false)
	require.NotNil(t, e.turnReview.CitationUnverifiedDelivery)
	state.RoundSteps[0].ToolCalls[0].Result.Data["changed"] = true
	e.emitCompletionEvent(t.Context(), state, "citation", "message", time.Now())
	require.False(t, terminalCandidateFallback(state.FinalAnswer))
	require.NotContains(t, state.FinalAnswer, "面积描述")
	require.NotContains(t, state.FinalAnswer, "<kb")
	require.Empty(t, state.KnowledgeRefs)
	require.Empty(t, state.EvidenceRefs)
}

func TestCitationUnverifiedStrictIdentityFailureUsesSamePresentation(t *testing.T) {
	e, state := citationWarningFixture(t, &mockChat{})
	e.chatModel = &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict(`{"complete":false,"feedback":"Reconcile the conflicting citation views.","next_action":"revise_answer"}`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"Reconcile the conflicting citation views.","next_action":"revise_answer"}`),
	}}
	e.config.AnswerReviewMode = "strict"
	d := e.checkCandidate(t.Context(), "说明内容。", citationWarningDraft, "", state, nil, false)
	require.Equal(t, "citation_identity_invalid", d.ReasonCode)
	e.commitTerminalResult(t.Context(), state, "citation", answerReviewUnavailableFallback, "", "failed", false)
	require.Contains(t, state.FinalAnswer, "〔引用待核对〕")
	require.NotNil(t, e.turnReview.CitationUnverifiedDelivery)
}
