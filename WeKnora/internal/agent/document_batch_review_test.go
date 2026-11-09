package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func documentBatchReviewFixture() map[string]any {
	return map[string]any{
		"knowledge_id": "batch-document", "knowledge_base_id": "batch-library",
		"document":   map[string]any{"knowledge_id": "batch-document", "title": "Synthetic observations"},
		"chunk_page": 2, "requested_pages": []int{7, 9},
		"page_results": []any{
			map[string]any{"page": 7, "success": true, "page_complete": false, "offset_space": "physical_page_chunks", "offset": 0, "next_content_offset": 24},
			map[string]any{"page": 9, "success": false, "error": "The requested physical page could not be read."},
		},
		"pages_complete": false, "all_pages_succeeded": false,
		"document_scope": map[string]any{"matched": true, "knowledge_id": "batch-document", "pages": []int{7}, "plan_validated": true,
			"user_page_constraint": map[string]any{"pages": []int{7, 9}, "user_quote": "Use the selected physical pages."}},
		"chunks": []any{map[string]any{"knowledge_id": "batch-document", "chunk_id": "batch-chunk", "page": 7,
			"content": "Observed measurement: 6 units.", "content_complete": false, "next_content_offset": 24}},
	}
}

func assertDocumentBatchScope(t *testing.T, scope map[string]any) {
	t.Helper()
	require.Equal(t, json.Number("2"), scope["chunk_page"], "retrieval pagination stays separate from physical pages")
	require.Equal(t, []any{json.Number("7"), json.Number("9")}, scope["requested_pages"])
	require.Equal(t, false, scope["pages_complete"])
	require.Equal(t, false, scope["all_pages_succeeded"])
	pages := scope["page_results"].([]any)
	require.Len(t, pages, 2)
	require.Equal(t, false, pages[0].(map[string]any)["page_complete"])
	require.Equal(t, json.Number("24"), pages[0].(map[string]any)["next_content_offset"])
	require.Equal(t, false, pages[1].(map[string]any)["success"])
	document := scope["document_scope"].(map[string]any)
	require.Equal(t, []any{json.Number("7")}, document["pages"], "actual successful pages do not become the full requested set")
	constraint := document["user_page_constraint"].(map[string]any)
	require.Equal(t, []any{json.Number("7"), json.Number("9")}, constraint["pages"])
	require.Equal(t, "Use the selected physical pages.", constraint["user_quote"])
}

func TestCitationSourceScopePreservesBatchPageGapsAndUserConstraints(t *testing.T) {
	data := documentBatchReviewFixture()
	message := documentRetrievalTestMessage("read_document", "batch-read", true, data)
	view := citedSourceEvidenceFromMessages([]chat.Message{message}, `Observed measurement <kb chunk_id="batch-chunk"/>`, citationReviewEvidenceBudget, true)
	item := citationItemByID(t, view, "batch-chunk")
	assertDocumentBatchScope(t, citationScopeForItem(t, view, item))
	require.Equal(t, json.Number("7"), item["chunk_metadata"].(map[string]any)["page"])
	require.Equal(t, false, item["source_content_complete"])
	require.True(t, view["view_complete"].(bool), "a complete projection still retains incomplete original-source coverage")
}

func TestDocumentCompanionCriticalScopeRetainsBatchGapsWhenOtherMetadataIsOmitted(t *testing.T) {
	data := documentBatchReviewFixture()
	data["large_unrelated_metadata"] = strings.Repeat("uncopied original metadata ", 1000)
	message := documentRetrievalTestMessage("read_document", "batch-read-omitted", true, data)
	view := documentRetrievalCompanionWithBudget([]chat.Message{message}, 4096)
	require.False(t, view["projection_complete"].(bool))
	scopes := view["scopes"].([]any)
	require.Len(t, scopes, 1, "the compact critical metadata fits without silently discarding the batch scope")
	scope := scopes[0].(map[string]any)
	require.False(t, scope["metadata_complete"].(bool))
	require.Equal(t, "/data", scope["metadata_pointer"])
	assertDocumentBatchScope(t, scope["metadata"].(map[string]any))
	reader := newReviewEvidenceReader([]chat.Message{message})
	record := reader.records[reader.byID[scope["evidence_id"].(string)]]
	failedPage, err := reviewPointerValue(record.Value, "/data/page_results/1/success")
	require.NoError(t, err)
	require.Equal(t, false, failedPage)
	require.LessOrEqual(t, sourceReviewProjectionSize(view), 4096)
}

func TestExistingCriticReceivesBatchFailureAndPhysicalCoverageMetadata(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The candidate claims complete coverage despite a partial page and a failed requested page.","next_action":"collect_evidence"}`)}}
	e := sourceConsistencyEngine(t, model)
	data := documentBatchReviewFixture()
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "batch-critic-read", Name: "read_document", Args: map[string]any{"id": "batch-document", "pages": []int{7, 9}}, Result: &types.ToolResult{Success: true, Data: data}}}}}}
	repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Return the observations from the selected physical pages.", `Both selected pages are fully covered. <kb chunk_id="batch-chunk"/>`, state, 0)
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, reviewCollectEvidence, e.answerReviewAction)
	require.Len(t, model.calls, 1, "batch coverage uses the existing final source critic")
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(model.calls[0][1].Content))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&input))
	citation := input["citation_source_evidence"].(map[string]any)
	item := citationItemByID(t, citation, "batch-chunk")
	scopeRef := citationInt(item["source_scope_ref"])
	assertDocumentBatchScope(t, citation["scopes"].([]any)[scopeRef].(map[string]any))
	companion := input["document_retrieval_companion"].(map[string]any)
	assertDocumentBatchScope(t, companion["scopes"].([]any)[0].(map[string]any)["metadata"].(map[string]any))
}
