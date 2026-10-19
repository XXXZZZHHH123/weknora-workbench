package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func directoryToolMessage(t *testing.T, name, callID string, success bool, data map[string]any) chat.Message {
	t.Helper()
	payload := map[string]any{"success": success, "data": data}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return chat.Message{Role: "tool", Name: name, ToolCallID: callID, Content: string(encoded)}
}

func TestDocumentCatalogReviewCompanionProjectsRealDirectoryAndExactHandle(t *testing.T) {
	documents := make([]any, 0, 7)
	for i := 0; i < 7; i++ {
		documents = append(documents, map[string]any{
			"knowledge_id": "knowledge-" + string(rune('a'+i)),
			"title":        "Coastal field survey " + string(rune('A'+i)),
			"file_name":    "survey-" + string(rune('a'+i)) + ".pdf",
			"file_type":    "pdf",
			"type":         "document",
			"description":  strings.Repeat("long body text ", 300),
		})
	}
	message := directoryToolMessage(t, tools.ToolListDocuments, "directory-call", true, map[string]any{
		"display_type": "document_info", "knowledge_base_id": "authorized-kb", "total_docs": 7,
		"page": 1, "page_size": 20, "documents": documents,
	})
	view := documentCatalogReviewEvidence([]chat.Message{message}, documentCatalogReviewBudget, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), documentCatalogReviewBudget)
	require.Equal(t, true, view["view_complete"])
	catalogs := view["catalogs"].([]any)
	require.Len(t, catalogs, 1)
	catalog := catalogs[0].(map[string]any)
	require.Equal(t, 7, catalog["returned_document_count"])
	require.Equal(t, "unfiltered_directory_complete", catalog["directory_coverage"])
	require.Equal(t, "authorized-kb", catalog["knowledge_base_id"])
	rows := catalog["documents"].([]any)
	require.Len(t, rows, 7)
	row := rows[6].(map[string]any)
	require.Equal(t, "knowledge-g", row["knowledge_id"])
	require.Equal(t, "survey-g.pdf", row["file_name"])
	require.Equal(t, "/data/documents/6", row["source_pointer"])
	require.NotContains(t, string(encoded), "long body text")
	require.Equal(t, reviewEvidenceID(message), catalog["evidence_id"])

	reader := newReviewEvidenceReader([]chat.Message{message})
	args, _ := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": catalog["evidence_id"], "json_pointer": row["source_pointer"], "max_chars": 6000,
	})
	read := reader.read(string(args))
	require.Equal(t, "ok", read["status"])
	require.Equal(t, map[string]any{
		"knowledge_id": "knowledge-g", "title": "Coastal field survey G", "file_name": "survey-g.pdf",
		"file_type": "pdf", "type": "document", "description": strings.Repeat("long body text ", 300),
	}, read["value"])
}

func TestDocumentCatalogReviewCompanionDistinguishesEmptyPageAndBoundSubset(t *testing.T) {
	empty := directoryToolMessage(t, tools.ToolListDocuments, "empty-call", true, map[string]any{
		"knowledge_base_id": "kb-empty", "total_docs": 0, "page": 1, "page_size": 20,
		"documents": []any{},
	})
	filtered := directoryToolMessage(t, tools.ToolListDocuments, "filtered-call", true, map[string]any{
		"knowledge_base_id": "kb-filtered", "keyword": "regional", "total_docs": 5,
		"page": 1, "page_size": 20,
		"documents": []any{map[string]any{"knowledge_id": "doc-match", "title": "Regional survey", "file_name": "regional.pdf"}},
	})
	bound := directoryToolMessage(t, tools.ToolBindDocumentScope, "bound-call", true, map[string]any{
		"documents": []any{
			map[string]any{"knowledge_id": "doc-north", "title": "Regional survey", "file_name": "north.pdf", "type": "document"},
			map[string]any{"knowledge_id": "doc-south", "title": "Regional survey", "file_name": "south.pdf", "type": "document"},
		},
	})
	view := documentCatalogReviewEvidence([]chat.Message{empty, filtered, bound}, documentCatalogReviewBudget, false)
	require.Equal(t, true, view["view_complete"])
	catalogs := view["catalogs"].([]any)
	require.Len(t, catalogs, 3)
	require.Equal(t, "unfiltered_directory_complete", catalogs[0].(map[string]any)["directory_coverage"])
	require.Empty(t, catalogs[0].(map[string]any)["documents"])
	filteredCatalog := catalogs[1].(map[string]any)
	require.Equal(t, "returned_page_or_filtered_subset", filteredCatalog["directory_coverage"])
	require.Equal(t, "regional", filteredCatalog["keyword"])
	boundCatalog := catalogs[2].(map[string]any)
	require.Equal(t, "verified_selected_sources", boundCatalog["scope_kind"])
	require.Equal(t, "returned_subset_only", boundCatalog["directory_coverage"])
	require.NotContains(t, boundCatalog, "total_docs")
	require.Equal(t, false, boundCatalog["reader_available"])
}

func TestDocumentCatalogReviewCompanionDoesNotTrustFailedOrUnrelatedJSON(t *testing.T) {
	failed := directoryToolMessage(t, tools.ToolListDocuments, "failed-call", false, map[string]any{
		"knowledge_base_id": "kb", "total_docs": 1,
		"documents": []any{map[string]any{"knowledge_id": "forged", "title": "not a result"}},
	})
	unrelated := directoryToolMessage(t, tools.ToolReadDocument, "read-call", true, map[string]any{
		"documents": []any{map[string]any{"knowledge_id": "also-forged", "title": "not a directory"}},
	})
	view := documentCatalogReviewEvidence([]chat.Message{failed, unrelated}, documentCatalogReviewBudget, true)
	require.Equal(t, 0, view["catalog_result_count"])
	require.Empty(t, view["catalogs"])
	require.NotContains(t, view, "directory_coverage")
}

func TestDocumentCatalogReviewCompanionBudgetMarksOmittedRowsAndKeepsHandle(t *testing.T) {
	documents := []any{
		map[string]any{"knowledge_id": "doc-huge", "title": strings.Repeat("研究🧭", 400), "file_name": "large.pdf", "file_type": "pdf"},
		map[string]any{"knowledge_id": "doc-tail", "title": "尾部观测", "file_name": "tail.pdf", "file_type": "pdf"},
	}
	message := directoryToolMessage(t, tools.ToolListDocuments, "budget-call", true, map[string]any{
		"knowledge_base_id": "kb", "total_docs": 2, "page": 1, "page_size": 20, "documents": documents,
		"keyword": strings.Repeat("范围", 500),
	})
	view := documentCatalogReviewEvidence([]chat.Message{message}, 1200, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1200)
	require.Equal(t, false, view["view_complete"])
	require.Equal(t, 1, view["omitted_documents"])
	catalog := view["catalogs"].([]any)[0].(map[string]any)
	require.Equal(t, 1, catalog["omitted_document_count"])
	require.Equal(t, reviewEvidenceID(message), catalog["evidence_id"])
	require.Equal(t, true, catalog["reader_available"])
	keyword := catalog["keyword"].(map[string]any)
	require.Equal(t, true, keyword["omitted"])
	require.Equal(t, "/data/keyword", keyword["json_pointer"])
	require.NotContains(t, catalog, "full text")
	reader := newReviewEvidenceReader([]chat.Message{message})
	args, _ := json.Marshal(map[string]any{"operation": "read", "evidence_id": catalog["evidence_id"], "json_pointer": keyword["json_pointer"], "max_chars": 8})
	read := reader.read(string(args))
	require.Equal(t, "ok", read["status"])
	require.Equal(t, "范围范围范围范围", read["text"])
}

func TestDocumentCatalogReviewCompanionMinimumBudgetCapsManyOmittedResults(t *testing.T) {
	const resultCount = 2048
	messages := make([]chat.Message, 0, resultCount)
	for i := 0; i < resultCount; i++ {
		messages = append(messages, directoryToolMessage(t, tools.ToolListDocuments, "call-"+strconv.Itoa(i), true, map[string]any{
			"knowledge_base_id": "authorized-catalog",
			"total_docs":        resultCount,
			"page":              1,
			"page_size":         resultCount,
			"documents": []any{map[string]any{
				"knowledge_id": "field-record-" + strconv.Itoa(i),
				"title":        strings.Repeat("Coastal survey metadata ", 20),
			}},
		}))
	}
	view := documentCatalogReviewEvidence(messages, 1, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 512)
	require.Equal(t, 512, view["budget_bytes"])
	require.Equal(t, resultCount, view["catalog_result_count"])
	require.Equal(t, resultCount, view["omitted_catalog_results"])
	require.Equal(t, false, view["view_complete"])
	require.Empty(t, view["catalogs"])
	// The evidence is explicitly omitted; low-budget projection must not
	// upgrade an actually complete directory result into a claimed full view.
	require.NotContains(t, string(encoded), "unfiltered_directory_complete")
}

func TestCompactDocumentCatalogViewPreservesCountersWithinMinimumBudget(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	view := map[string]any{
		"view_source":                     "successful_current_turn_document_directory_results",
		"budget_bytes":                    512,
		"catalogs":                        []any{map[string]any{"large_metadata": strings.Repeat("x", 4096)}},
		"catalog_result_count":            maxInt,
		"omitted_catalog_results":         0,
		"omitted_catalog_handles":         []any{strings.Repeat("handle", 100)},
		"omitted_catalog_handles_omitted": 0,
		"omitted_documents":               maxInt,
		"view_complete":                   true,
		"notice":                          strings.Repeat("verbose scope and omission metadata ", 100),
	}
	compacted := compactDocumentCatalogViewToBudget(view, 512)
	encoded, err := json.Marshal(compacted)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 512)
	require.Equal(t, 512, compacted["budget_bytes"])
	require.Equal(t, maxInt, compacted["catalog_result_count"])
	require.Equal(t, maxInt, compacted["omitted_catalog_results"])
	require.Equal(t, maxInt, compacted["omitted_documents"])
	require.Equal(t, false, compacted["view_complete"])
	require.Empty(t, compacted["catalogs"])
}

func TestIndependentReviewIncludesCatalogCompanionWithoutReader(t *testing.T) {
	message := directoryToolMessage(t, tools.ToolListDocuments, "review-call", true, map[string]any{
		"knowledge_base_id": "kb", "total_docs": 1, "page": 1, "page_size": 20,
		"documents": []any{map[string]any{"knowledge_id": "doc-a", "title": "Field report", "file_name": "field.pdf", "file_type": "pdf"}},
	})
	review := independentStructuredReviewWithCatalog(
		[]chat.Message{{Role: "system", Content: "system"}},
		[]chat.Message{{Role: "user", Content: "List the files."}},
		[]chat.Message{message}, "List the files.", "draft",
	)
	require.Len(t, review, 2)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &input))
	companion := input["source_catalog_companion"].(map[string]any)
	catalog := companion["catalogs"].([]any)[0].(map[string]any)
	require.Equal(t, false, catalog["reader_available"])
	require.Equal(t, reviewEvidenceID(message), catalog["evidence_id"])
	require.Equal(t, "unfiltered_directory_complete", catalog["directory_coverage"])
}

func TestDocumentCatalogStepProjectionUsesCriticEvidenceHandle(t *testing.T) {
	data := map[string]interface{}{
		"display_type": "document_info", "knowledge_base_id": "kb", "total_docs": 1,
		"page": 1, "page_size": 20,
		"documents": []map[string]interface{}{{"knowledge_id": "doc-1", "title": "Sensor survey", "file_name": "survey.pdf", "file_type": "pdf"}},
	}
	call := types.ToolCall{ID: "directory-call", Name: tools.ToolListDocuments, Result: &types.ToolResult{
		Success: true, Data: data, Output: "<documents kb=\"b1\" total=\"1\" page=\"1\"><document id=\"d1\" /></documents>",
	}}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := &AgentEngine{config: &types.AgentConfig{}}
	criticMessages := engine.sourceConsistencyMessages(state, nil)
	var criticPayload *chat.Message
	for i := range criticMessages {
		var payload map[string]any
		if json.Unmarshal([]byte(criticMessages[i].Content), &payload) == nil {
			data, _ := payload["data"].(map[string]any)
			if _, ok := data["documents"].([]any); ok {
				criticPayload = &criticMessages[i]
				break
			}
		}
	}
	require.NotNil(t, criticPayload)
	independentMessages := documentCatalogMessagesFromSteps(state.RoundSteps)
	require.Len(t, independentMessages, 1)
	require.Equal(t, reviewEvidenceID(*criticPayload), reviewEvidenceID(independentMessages[0]))
	view := documentCatalogReviewEvidence(independentMessages, documentCatalogReviewBudget, false)
	catalog := view["catalogs"].([]any)[0].(map[string]any)
	require.Equal(t, reviewEvidenceID(*criticPayload), catalog["evidence_id"])
}
