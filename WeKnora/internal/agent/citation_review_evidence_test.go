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

func citationToolMessage(name, payload string) chat.Message {
	return chat.Message{Role: "tool", Name: name, ToolCallID: "actual-read-call", Content: payload}
}

func citationPayload(chunkID, content string, complete bool) string {
	data, _ := json.Marshal(map[string]any{
		"document":  map[string]any{"knowledge_id": "doc-real", "title": "Observed field survey"},
		"truncated": true, "next_offset": 2,
		"chunks": []any{map[string]any{
			"chunk_id": chunkID, "knowledge_id": "doc-real", "physical_page": 7,
			"content": content, "content_complete": complete, "content_offset": 0,
			"next_content_offset": 80,
		}},
	})
	return `{"success":true,"data":` + string(data) + `}`
}

func citationRowsPayload(rows []any) string {
	data, _ := json.Marshal(map[string]any{"chunks": rows})
	return `{"success":true,"data":` + string(data) + `}`
}

func citationItemByID(t *testing.T, evidence map[string]any, id string) map[string]any {
	t.Helper()
	for _, raw := range evidence["items"].([]any) {
		item := raw.(map[string]any)
		if item["chunk_id"] == id {
			return item
		}
	}
	t.Fatalf("citation item %q was not projected", id)
	return nil
}

func citationScopeForItem(t *testing.T, evidence map[string]any, item map[string]any) map[string]any {
	t.Helper()
	return evidence["scopes"].([]any)[item["source_scope_ref"].(int)].(map[string]any)
}

func citationHandleForPointerRef(t *testing.T, evidence map[string]any, pointerRef int) (string, string) {
	t.Helper()
	pointer := evidence["source_pointers"].([]any)[pointerRef].([]any)
	result := evidence["source_results"].([]any)[pointer[0].(int)].([]any)
	return result[2].(string), pointer[1].(string)
}

func citationHandleForItem(t *testing.T, evidence map[string]any, item map[string]any) (string, string) {
	t.Helper()
	return citationHandleForPointerRef(t, evidence, item["source_pointer_ref"].(int))
}

func TestCitationFirstViewUsesOnlyActuallyFetchedReferencedChunk(t *testing.T) {
	messages := []chat.Message{
		citationToolMessage("read_document", citationPayload("survey-tail", "Observed sample: 42 units.", false)),
		citationToolMessage("search_knowledge", citationPayload("other-chunk", "Unrelated earlier result.", true)),
		{Role: "assistant", Content: `I also read <kb chunk_id="invented">fabricated citation</kb>`},
	}
	candidate := `Survey result is 42 units <kb chunk_id="survey-tail">p. 7</kb>; corroborated <kb chunk_id="survey-tail">same source</kb>. Also <kb chunk_id="not-fetched">missing</kb>.`
	view := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)

	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
	require.Equal(t, 3, view["citation_count"])
	require.Equal(t, 2, view["unique_citation_count"])
	require.Equal(t, false, view["view_complete"], "an unmatched cited ID keeps the citation view explicitly incomplete")
	items := view["items"].([]any)
	require.Len(t, items, 1, "duplicate citation ID is de-duplicated and only referenced source chunks are projected")
	item := items[0].(map[string]any)
	require.Equal(t, "survey-tail", item["chunk_id"])
	require.Equal(t, "Observed sample: 42 units.", item["content"])
	require.Equal(t, false, item["source_content_complete"])
	require.Equal(t, true, citationScopeForItem(t, view, item)["truncated"])
	require.Equal(t, json.Number("7"), item["chunk_metadata"].(map[string]any)["physical_page"])
	_, pointer := citationHandleForItem(t, view, item)
	require.Contains(t, pointer, "/chunks/0/content")
	require.Equal(t, []string{"not-fetched"}, view["unknown_citation_ids"])
	require.NotContains(t, string(encoded), "fabricated citation")
	require.NotContains(t, string(encoded), "Unrelated earlier result")
}

func TestCitationFirstViewPreservesExplicitOmissionAndContinuationWhenOverBudget(t *testing.T) {
	large := strings.Repeat("évidence🧭", 2200)
	messages := []chat.Message{citationToolMessage("read_document", citationPayload("long-source", large, false))}
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="long-source">source</kb>`, citationReviewEvidenceBudget, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
	require.Equal(t, false, view["view_complete"])
	items := view["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, true, item["content_included"])
	require.Equal(t, "review_prefix", item["content_state"])
	require.Equal(t, len(large), item["content_bytes"], "content budget is measured in UTF-8 bytes")
	require.NotEqual(t, large, item["content"])
	require.Greater(t, item["content_omitted_bytes"].(int), 0)
	require.NotContains(t, string(encoded), large)
	pointerRef, ok := item["source_pointer_ref"].(int)
	require.True(t, ok)
	require.GreaterOrEqual(t, pointerRef, 0)
	require.Less(t, pointerRef, len(view["source_pointers"].([]any)))
	require.Equal(t, false, item["source_content_complete"])
}

func TestCitationViewCompactsRepeatedLocatorQuotesAndKeepsLaterBody(t *testing.T) {
	large := strings.Repeat("long source\nrow ", 900)
	normalizedQuote := strings.Repeat("long source row ", 900)
	messages := make([]chat.Message, 0, 6)
	var candidate strings.Builder
	for i := 0; i < 5; i++ {
		id := "long-doc-" + strconv.Itoa(i)
		row := map[string]any{
			"chunk_id": id, "knowledge_id": "doc-" + strconv.Itoa(i),
			"content": large, "content_offset": 120, "content_offset_space": "chunk_unicode_runes",
			"content_complete": false, "next_content_offset": 1024,
			"source_locators": []any{
				map[string]any{"type": "slide", "slide": 3, "source_hash": "sha-" + strconv.Itoa(i), "mapping": "exact", "partial": true, "quote": normalizedQuote},
				map[string]any{"type": "slide", "slide": 4, "mapping": "exact", "quote": "long source unrelated phrase"},
			},
		}
		data, _ := json.Marshal(map[string]any{
			"document": map[string]any{"knowledge_id": "doc-" + strconv.Itoa(i), "title": "Distinct report " + strconv.Itoa(i)},
			"chunks":   []any{row}, "offset_space": "chunk_unicode_runes", "truncated": true, "next_offset": 4,
		})
		messages = append(messages, citationToolMessage("read_document", `{"success":true,"data":`+string(data)+`}`))
		candidate.WriteString(`<kb chunk_id="` + id + `"/>`)
	}
	lastID := "short-final-doc"
	lastContent := "Final cited row remains visible."
	lastData, _ := json.Marshal(map[string]any{
		"document": map[string]any{"knowledge_id": "short-doc", "title": "Final report"},
		"chunks":   []any{map[string]any{"chunk_id": lastID, "knowledge_id": "short-doc", "content": lastContent, "content_offset": 0, "content_complete": true}},
		"page":     2, "page_complete": true, "offset_space": "physical_page_chunks",
	})
	messages = append(messages, citationToolMessage("read_document", `{"success":true,"data":`+string(lastData)+`}`))
	candidate.WriteString(`<kb chunk_id="` + lastID + `"/>`)

	view := citedSourceEvidenceFromMessages(messages, candidate.String(), citationReviewEvidenceBudget, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
	require.Equal(t, false, view["view_complete"], "omitted long source bodies remain explicitly incomplete")
	items := view["items"].([]any)
	require.Len(t, items, 6, "all cited identities and continuation pointers are reserved before bodies")
	for i := 0; i < 5; i++ {
		item := items[i].(map[string]any)
		require.Equal(t, "review_prefix", item["content_state"])
		require.Greater(t, item["content_omitted_bytes"].(int), 0)
		locators := item["chunk_metadata"].(map[string]any)["source_locators"].([]any)
		locator := locators[0].(map[string]any)
		require.Equal(t, true, locator["quote_omitted"])
		require.NotContains(t, locator, "quote")
		require.Equal(t, "slide", locator["type"])
		require.Equal(t, "exact", locator["mapping"])
		require.Equal(t, true, locator["partial"])
		require.Equal(t, "sha-"+strconv.Itoa(i), locator["source_hash"])
		unmatchedLocator := locators[1].(map[string]any)
		require.Equal(t, "long source unrelated phrase", unmatchedLocator["quote"], "a quote without exact whitespace-folded containment remains intact")
		require.NotContains(t, unmatchedLocator, "quote_omitted")
		pointerRef, ok := item["source_pointer_ref"].(int)
		require.True(t, ok, "the immutable source row remains readable after quote elision")
		require.GreaterOrEqual(t, pointerRef, 0)
		require.Less(t, pointerRef, len(view["source_pointers"].([]any)))
	}
	last := items[5].(map[string]any)
	require.Equal(t, lastID, last["chunk_id"])
	require.Equal(t, lastContent, last["content"], "a too-large earlier body does not prevent a later short cited body from appearing")
}

func TestCitationFirstViewBoundsLargeUnknownAndAmbiguousIDLists(t *testing.T) {
	const count = 40
	idSuffix := strings.Repeat("x", 220)
	rowsA := make([]any, 0, count)
	rowsB := make([]any, 0, count)
	var candidate strings.Builder
	for i := 0; i < count; i++ {
		unknownID := "unknown-" + strconv.Itoa(i) + "-" + idSuffix
		ambiguousID := "ambiguous-" + strconv.Itoa(i) + "-" + idSuffix
		candidate.WriteString(`<kb chunk_id="` + unknownID + `"/>`)
		candidate.WriteString(`<kb chunk_id="` + ambiguousID + `"/>`)
		rowsA = append(rowsA, map[string]any{"chunk_id": ambiguousID, "knowledge_id": "doc-a", "content": "first version"})
		rowsB = append(rowsB, map[string]any{"chunk_id": ambiguousID, "knowledge_id": "doc-a", "content": "second version"})
	}
	messages := []chat.Message{
		citationToolMessage("read_document", citationRowsPayload(rowsA)),
		citationToolMessage("read_document", citationRowsPayload(rowsB)),
	}
	view := citedSourceEvidenceFromMessages(messages, candidate.String(), citationReviewEvidenceBudget, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
	require.Equal(t, citationReviewEvidenceBudget, view["budget_bytes"])
	require.Equal(t, false, view["view_complete"])
	require.Greater(t, view["unknown_citation_ids_omitted"].(int), 0)
	require.Greater(t, view["ambiguous_citation_ids_omitted"].(int), 0)
	require.LessOrEqual(t, len(view["unknown_citation_ids"].([]string)), 24)
	require.LessOrEqual(t, len(view["ambiguous_citation_ids"].([]string)), 24)
}

func TestCitationFirstViewReportsOverlongIDsAndEmptyBodiesWithoutClaimingSupport(t *testing.T) {
	longID := strings.Repeat("z", 300)
	messages := []chat.Message{citationToolMessage("read_document", citationPayload("empty-source", "", true))}
	candidate := `<kb chunk_id="empty-source"/><kb chunk_id="` + longID + `"/>`
	view := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), view["budget_bytes"].(int))
	require.Equal(t, citationReviewEvidenceBudget, view["budget_bytes"])
	require.Equal(t, 1, view["unknown_citation_ids_omitted"])
	item := view["items"].([]any)[0].(map[string]any)
	require.Equal(t, true, item["content_empty"])
	require.Equal(t, false, item["content_included"])
	require.Equal(t, "source_empty", item["content_state"])
	require.NotContains(t, item, "content", "an empty body is represented explicitly without fabricating a string field")
	require.NotContains(t, item, "content_support_note")
	require.Equal(t, true, item["source_content_complete"])
}

func TestCitationFirstViewRaisesImpossibleTinyBudgetToAdvertisedMinimum(t *testing.T) {
	view := citedSourceEvidenceFromMessages(nil, "", 256, true)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.Equal(t, citationReviewMinimumBudget, view["budget_bytes"])
	require.LessOrEqual(t, len(encoded), view["budget_bytes"].(int))
}

func TestMetadataEvidenceGateRequiresSuccessfulActualCatalogRows(t *testing.T) {
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: tools.ToolListDocuments,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"documents": []map[string]interface{}{
			{"knowledge_id": "doc-a"}, {"knowledge_id": "doc-b"},
		}}},
	}}}}
	require.True(t, hasCurrentSourceMetadataEvidence(steps), "actual document catalog rows make metadata-only claims reviewable")
	e := sourceConsistencyEngine(t, &mockChat{})
	require.True(t, e.needsSourceConsistencyCheck(&types.AgentState{RoundSteps: steps}), "metadata-only turns must enter the same source-consistency critic")

	failed := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name:   tools.ToolListDocuments,
		Result: &types.ToolResult{Success: false, Data: map[string]interface{}{"documents": []map[string]interface{}{{"knowledge_id": "doc-a"}}}},
	}}}}
	require.False(t, hasCurrentSourceMetadataEvidence(failed), "failed directory calls are not metadata evidence")

	schema := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: "describe_tools", Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"documents": []map[string]interface{}{{"knowledge_id": "schema-example"}}}},
	}}}}
	require.False(t, hasCurrentSourceMetadataEvidence(schema), "tool schemas and examples are not actual directory results")

	datasets := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: tools.ToolStructuredData, Args: map[string]interface{}{"operation": "list_datasets"},
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"datasets": []map[string]interface{}{{"dataset_id": "dataset-real"}}}},
	}}}}
	require.True(t, hasCurrentSourceMetadataEvidence(datasets), "actual dataset catalog rows also require consistency review")

	nonCatalogOperation := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: tools.ToolStructuredData, Args: map[string]interface{}{"operation": "query"},
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"datasets": []map[string]interface{}{{"dataset_id": "incidental"}}}},
	}}}}
	require.False(t, hasCurrentSourceMetadataEvidence(nonCatalogOperation))

	emptyDocuments := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: tools.ToolListDocuments,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			"documents": []map[string]interface{}{}, "knowledge_base_id": "kb-real",
			"total_docs": int64(0), "page": 1, "page_size": 20,
		}},
	}}}}
	require.True(t, hasCurrentSourceMetadataEvidence(emptyDocuments), "an actual empty directory page with knowledge-base scope and total count is reviewable")
	emptyUnscoped := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name:   tools.ToolListDocuments,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"documents": []map[string]interface{}{}, "total_docs": int64(0)}},
	}}}}
	require.False(t, hasCurrentSourceMetadataEvidence(emptyUnscoped), "an empty array without a real directory scope is insufficient")
	emptyDatasets := []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: tools.ToolStructuredData, Args: map[string]interface{}{"operation": "list_datasets"},
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{"datasets": []map[string]interface{}{}}},
	}}}}
	require.True(t, hasCurrentSourceMetadataEvidence(emptyDatasets), "list_datasets returns the full actual catalog, including an empty one")
	metadataMessages := e.sourceConsistencyMessages(&types.AgentState{RoundSteps: emptyDatasets}, nil)
	require.Len(t, metadataMessages, 2)
	require.Contains(t, metadataMessages[0].Content, `"catalog_operation":"list_datasets"`, "the critic needs the exact executed catalog operation to interpret an empty result")
}

func TestIndependentReviewReceivesCitationFirstJSONSourceView(t *testing.T) {
	messages := []chat.Message{
		citationToolMessage("read_document", citationPayload("tail-report", "The field office recorded 12 visits.", true)),
	}
	conversation := []chat.Message{{Role: "user", Content: "How many visits were recorded?"}}
	review := independentStructuredReviewWithConversation(messages, conversation, conversation[0].Content,
		`The report records 12 visits. <kb chunk_id="tail-report">p. 7</kb>`)
	require.Len(t, review, 2)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &input))
	evidence := input["citation_source_evidence"].(map[string]any)
	items := evidence["items"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, "tail-report", items[0].(map[string]any)["chunk_id"])
	require.Equal(t, "The field office recorded 12 visits.", items[0].(map[string]any)["content"])
}

func TestCitationViewRetainsLaterPhysicalReadAndLiteralSearchScope(t *testing.T) {
	row := map[string]any{"chunk_id": "same-span", "knowledge_id": "doc-real", "chunk_index": 285, "content": "Observed source statement", "content_offset": 0,
		"source_locators": []any{map[string]any{"type": "slide", "slide": 28, "mapping": "exact", "partial": true, "source_hash": "bound-file"}}}
	searchData := map[string]any{"chunks": []any{row}, "page": 1, "offset_space": "document_chunks",
		"query_scope": map[string]any{"mode": "literal_all_terms_in_one_chunk", "document_scan_complete": true, "notice": "No literal match is not semantic absence."}}
	physicalRow := map[string]any{}
	for k, v := range row {
		physicalRow[k] = v
	}
	physicalRow["page"] = 28
	physicalData := map[string]any{"chunks": []any{physicalRow}, "page": 28, "page_chunks": 1, "page_complete": true, "offset_space": "physical_page_chunks", "document_scope": map[string]any{"matched": true, "knowledge_id": "doc-real", "page": 28, "plan_validated": true, "unprojected_extra": "opaque"}}
	messages := []chat.Message{}
	for i, data := range []map[string]any{searchData, physicalData} {
		payload, _ := json.Marshal(map[string]any{"success": true, "data": data})
		messages = append(messages, chat.Message{Role: "tool", Name: "read_document", ToolCallID: strconv.Itoa(i), Content: string(payload)})
	}
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="same-span"/>`, citationReviewEvidenceBudget, true)
	require.Equal(t, true, view["view_complete"])
	item := view["items"].([]any)[0].(map[string]any)
	require.NotEmpty(t, item["chunk_metadata"].(map[string]any)["source_locators"])
	require.NotEmpty(t, citationScopeForItem(t, view, item)["query_scope"])
	observed := item["additional_source_observations"].([]any)[0].(map[string]any)
	require.Equal(t, json.Number("28"), observed["chunk_metadata"].(map[string]any)["page"])
	scope := view["scopes"].([]any)[observed["source_scope_ref"].(int)].(map[string]any)
	require.Equal(t, true, scope["page_complete"])
	require.Equal(t, json.Number("1"), scope["page_chunks"])
	require.Equal(t, "physical_page_chunks", scope["offset_space"])
	require.Equal(t, true, scope["document_scope"].(map[string]any)["plan_validated"])
	require.Equal(t, true, scope["document_scope_omitted"])
	evidenceID, jsonPointer := citationHandleForPointerRef(t, view, observed["source_pointer_ref"].(int))
	parentPointer := strings.TrimSuffix(jsonPointer, "/content")
	require.NotEqual(t, jsonPointer, parentPointer)
	reader := newReviewEvidenceReader(messages)
	// Read the actual row object, not only its body field, so physical-page
	// metadata remains directly inspectable through the immutable result.
	arguments, _ := json.Marshal(map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": parentPointer})
	result := reader.read(string(arguments))
	require.Equal(t, "ok", result["status"])
	sourceRow := result["value"].(map[string]any)
	require.Equal(t, json.Number("28"), sourceRow["page"])
	require.Equal(t, "Observed source statement", sourceRow["content"])
}

func TestCitationViewReconcilesCompleteSearchWithOffsetZeroRead(t *testing.T) {
	searchRow := map[string]any{
		"chunk_id": "same-source-chunk", "knowledge_id": "doc-real", "chunk_index": 12,
		"content": "Complete indexed chunk body.", "content_offset": 0,
		"content_offset_space": "chunk_unicode_runes", "content_complete": true,
	}
	readRow := map[string]any{}
	for key, value := range searchRow {
		readRow[key] = value
	}
	searchData, _ := json.Marshal(map[string]any{"results": []any{searchRow}, "count": 1})
	readData, _ := json.Marshal(map[string]any{"chunks": []any{readRow}, "truncated": false})
	messages := []chat.Message{
		citationToolMessage("search_knowledge", `{"success":true,"data":`+string(searchData)+`}`),
		citationToolMessage("read_document", `{"success":true,"data":`+string(readData)+`}`),
	}
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="same-source-chunk"/>`, citationReviewEvidenceBudget, true)
	require.Equal(t, true, view["view_complete"])
	require.Empty(t, view["ambiguous_citation_ids"])
	items := view["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	require.Equal(t, "Complete indexed chunk body.", item["content"])
	require.Len(t, item["additional_source_observations"].([]any), 1,
		"search and read at the same explicit complete span remain separate observations")
}

func TestCitationViewKeepsUnknownOrDifferentSearchSpansAmbiguous(t *testing.T) {
	newRow := func(offset any) map[string]any {
		row := map[string]any{"chunk_id": "same-source-chunk", "knowledge_id": "doc-real", "content": "Observed text"}
		if offset != nil {
			row["content_offset"] = offset
			row["content_offset_space"] = "chunk_unicode_runes"
			row["content_complete"] = true
		}
		return row
	}
	for _, tc := range []struct {
		name         string
		searchOffset any
		readOffset   int
	}{
		{name: "unknown search span", searchOffset: nil, readOffset: 0},
		{name: "different offsets", searchOffset: 0, readOffset: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searchData, _ := json.Marshal(map[string]any{"results": []any{newRow(tc.searchOffset)}})
			readData, _ := json.Marshal(map[string]any{"chunks": []any{newRow(tc.readOffset)}})
			messages := []chat.Message{
				citationToolMessage("search_knowledge", `{"success":true,"data":`+string(searchData)+`}`),
				citationToolMessage("read_document", `{"success":true,"data":`+string(readData)+`}`),
			}
			view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="same-source-chunk"/>`, citationReviewEvidenceBudget, true)
			require.Equal(t, false, view["view_complete"])
			require.Equal(t, []string{"same-source-chunk"}, view["ambiguous_citation_ids"])
			require.Empty(t, view["items"], "unknown or conflicting spans must not be promoted as a citation")
		})
	}
}

func TestCitationViewBoundsRepeatedObservationsWithoutHidingOmissions(t *testing.T) {
	messages := []chat.Message{}
	for i := 0; i < 16; i++ {
		m := citationToolMessage("read_document", citationPayload("repeated-span", "Identical source body", true))
		m.ToolCallID = strconv.Itoa(i)
		messages = append(messages, m)
	}
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="repeated-span"/>`, citationReviewEvidenceBudget, true)
	require.Equal(t, false, view["view_complete"])
	item := view["items"].([]any)[0].(map[string]any)
	require.Len(t, item["additional_source_observations"].([]any), 7)
	require.Equal(t, 8, item["source_observations_omitted"])
	encoded, _ := json.Marshal(view)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
}
