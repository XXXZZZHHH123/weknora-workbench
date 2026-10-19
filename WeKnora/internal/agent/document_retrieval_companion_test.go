package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func documentRetrievalTestMessage(name, callID string, success bool, data map[string]any) chat.Message {
	payload, _ := json.Marshal(map[string]any{"success": success, "data": data})
	return chat.Message{Role: "tool", Name: name, ToolCallID: callID, Content: string(payload)}
}

func documentRetrievalTestMessageWithExecutionScope(name, callID string, success bool, data, executionScope map[string]any) chat.Message {
	payload, _ := json.Marshal(map[string]any{"success": success, "data": data, "execution_scope": executionScope})
	return chat.Message{Role: "tool", Name: name, ToolCallID: callID, Content: string(payload)}
}

func documentRetrievalObservationMaps(view map[string]any) []map[string]any {
	rows, _ := view["observations"].([]any)
	observations := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if object, ok := row.(map[string]any); ok {
			observations = append(observations, object)
		}
	}
	return observations
}

func TestDocumentRetrievalCompanionDeduplicatesOnlyExactBodyEncoding(t *testing.T) {
	body := "River gauge note: flow remained stable after rainfall."
	rows := []any{
		map[string]any{"chunk_id": "chunk-a", "knowledge_id": "doc-a", "knowledge_base_id": "kb-a", "content": body, "content_offset": 0, "content_complete": true, "source_locators": []any{map[string]any{"page": 2}}},
		map[string]any{"chunk_id": "chunk-b", "knowledge_id": "doc-b", "knowledge_base_id": "kb-b", "content": body, "content_offset": 0, "content_complete": true, "source_locators": []any{map[string]any{"page": 9}}},
	}
	data := map[string]any{"results": rows, "query": "measured flow", "mode": "keyword", "knowledge_base_ids": []any{"kb-a", "kb-b"}, "truncated": false}
	messages := []chat.Message{
		documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-a", true, data, map[string]any{"query": "measured flow", "knowledge_base_ids": []any{"kb-a", "kb-b"}}),
		documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-duplicate", true, data, map[string]any{"query": "measured flow", "knowledge_base_ids": []any{"kb-a", "kb-b"}}),
	}
	view := documentRetrievalCompanion(messages, documentRetrievalCompanionBudget)
	require.NotNil(t, view)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), documentRetrievalCompanionBudget)
	require.Equal(t, true, view["projection_complete"])
	require.Equal(t, json.Number("4"), view["source_observation_count"], "two source rows returned in each of two calls are counted")
	require.Equal(t, json.Number("2"), view["unique_observation_count"])
	require.Equal(t, json.Number("1"), view["unique_body_count"])
	observations := documentRetrievalObservationMaps(view)
	require.Len(t, observations, 2)
	require.Equal(t, "doc-a", observations[0]["knowledge_id"])
	require.Equal(t, "doc-b", observations[1]["knowledge_id"])
	require.Equal(t, json.Number("1"), observations[0]["duplicate_observation_count"])
	// The source has one unique exact body; duplicate body rows must not be
	// interpreted as one source or one underlying measurement.
	bodyRows := view["bodies"].([]any)
	require.Len(t, bodyRows, 1)
	require.Equal(t, body, bodyRows[0].(map[string]any)["content"])
	require.Contains(t, view["coverage_note"], "not as evidence of source or underlying-data equivalence")
	scope := view["scopes"].([]any)[0].(map[string]any)
	require.Equal(t, "measured flow", scope["metadata"].(map[string]any)["query"])
	require.Equal(t, map[string]any{"query": "measured flow", "knowledge_base_ids": []any{"kb-a", "kb-b"}}, scope["metadata"].(map[string]any)["execution_scope"])
	require.Equal(t, true, scope["metadata_complete"])
	require.Equal(t, "/execution_scope", scope["execution_scope_json_pointer"])
}

func TestDocumentRetrievalCompanionPreservesUnknownAndPartialOffsets(t *testing.T) {
	rows := []any{
		map[string]any{"chunk_id": "same-span", "knowledge_id": "doc-a", "content": "same text", "extra_source_field": "kept", "large_counter": json.Number("9007199254740993")},
		map[string]any{"chunk_id": "same-span", "knowledge_id": "doc-a", "content": "same text", "content_offset": 11, "content_offset_space": "chunk_unicode_runes", "content_complete": false, "next_content_offset": 19},
	}
	view := documentRetrievalCompanion([]chat.Message{
		documentRetrievalTestMessage("read_document", "read-call", true, map[string]any{"chunks": rows, "offset": 0, "truncated": true, "next_offset": 1}),
	}, documentRetrievalCompanionBudget)
	require.NotNil(t, view)
	require.Equal(t, true, view["projection_complete"])
	require.Len(t, view["bodies"].([]any), 1, "identical text is encoded once without merging source observations")
	observations := documentRetrievalObservationMaps(view)
	require.Len(t, observations, 2)
	unknownMetadata := observations[0]["source_metadata"].(map[string]any)
	require.NotContains(t, unknownMetadata, "content_offset", "unknown offsets must remain unknown")
	require.Equal(t, "kept", unknownMetadata["extra_source_field"])
	require.Equal(t, json.Number("9007199254740993"), unknownMetadata["large_counter"], "projection must preserve exact JSON number tokens")
	require.Equal(t, true, observations[0]["metadata_complete"])
	require.NotContains(t, observations[0], "metadata_pointer")
	partialMetadata := observations[1]["source_metadata"].(map[string]any)
	require.Equal(t, json.Number("11"), partialMetadata["content_offset"])
	require.Equal(t, false, partialMetadata["content_complete"])
	scopeMetadata := view["scopes"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
	require.Equal(t, true, scopeMetadata["truncated"])
	require.Equal(t, json.Number("1"), scopeMetadata["next_offset"])
	require.Equal(t, true, view["scopes"].([]any)[0].(map[string]any)["metadata_complete"])
}

func TestDocumentRetrievalCompanionDoesNotDeduplicateAcrossDifferentScopes(t *testing.T) {
	row := map[string]any{"chunk_id": "same", "knowledge_id": "doc", "content": "identical fetched text", "content_offset": 0, "content_complete": true}
	first := documentRetrievalTestMessage("read_document", "scope-a", true, map[string]any{
		"chunks": []any{row}, "query_scope": map[string]any{"document_scan_complete": true, "mode": "literal"},
	})
	second := documentRetrievalTestMessage("read_document", "scope-b", true, map[string]any{
		"chunks": []any{row}, "query_scope": map[string]any{"document_scan_complete": false, "mode": "physical_page"},
	})
	view := documentRetrievalCompanion([]chat.Message{first, second}, documentRetrievalCompanionBudget)
	require.NotNil(t, view)
	require.Len(t, documentRetrievalObservationMaps(view), 2,
		"same identity/body/offset from distinct actual scopes stays as two observations")
	require.Len(t, view["bodies"].([]any), 1, "only exact text encoding is shared")
	observations := documentRetrievalObservationMaps(view)
	require.NotEqual(t, observations[0]["evidence_id"], observations[1]["evidence_id"])
	scopes := view["scopes"].([]any)
	require.NotEqual(t,
		scopes[0].(map[string]any)["metadata"].(map[string]any)["query_scope"],
		scopes[1].(map[string]any)["metadata"].(map[string]any)["query_scope"])
}

func TestDocumentRetrievalCompanionBalancesDocumentsAndRespectsUTF8Budget(t *testing.T) {
	rows := []any{}
	for docIndex := 0; docIndex < 2; docIndex++ {
		for rowIndex := 0; rowIndex < 4; rowIndex++ {
			rows = append(rows, map[string]any{
				"chunk_id":     fmt.Sprintf("chunk-%d-%d", docIndex, rowIndex),
				"knowledge_id": fmt.Sprintf("doc-%d", docIndex), "knowledge_base_id": "kb",
				"content": fmt.Sprintf("%s-%d-%d", strings.Repeat("水🧭", 30), docIndex, rowIndex),
			})
		}
	}
	message := documentRetrievalTestMessage("search_knowledge", "balanced", true, map[string]any{"results": rows, "query": "field observation"})
	view := documentRetrievalCompanion([]chat.Message{message}, 1900)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1900)
	require.Equal(t, false, view["projection_complete"])
	observations := documentRetrievalObservationMaps(view)
	require.GreaterOrEqual(t, len(observations), 2)
	require.NotEqual(t, observations[0]["knowledge_id"], observations[1]["knowledge_id"],
		"first round must represent each document before spending the next slot on one document")
	for _, body := range view["bodies"].([]any) {
		content := body.(map[string]any)["content"].(string)
		require.True(t, strings.Contains(content, "水🧭"))
	}
}

func TestDocumentRetrievalCompanionHonorsTinyBudget(t *testing.T) {
	message := documentRetrievalTestMessage("search_knowledge", "tiny", true, map[string]any{"results": []any{
		map[string]any{"chunk_id": "a", "knowledge_id": "doc-a", "content": "a"},
	}})
	view := documentRetrievalCompanion([]chat.Message{message}, 80)
	if view != nil {
		encoded, err := json.Marshal(view)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), 80, "a small requested budget must never be silently expanded")
	}
}

func TestDocumentRetrievalCompanionDoesNotDeduplicateCallsWithoutExecutionScope(t *testing.T) {
	data := map[string]any{"results": []any{map[string]any{"chunk_id": "same", "knowledge_id": "doc", "content": "same body"}}}
	messages := []chat.Message{
		documentRetrievalTestMessage("search_knowledge", "call-1", true, data),
		documentRetrievalTestMessage("search_knowledge", "call-2", true, data),
	}
	view := documentRetrievalCompanion(messages, documentRetrievalCompanionBudget)
	require.Equal(t, json.Number("2"), view["unique_observation_count"], "absent execution args make cross-call equivalence unknown")
	require.Len(t, documentRetrievalObservationMaps(view), 2)
}

func TestDocumentRetrievalCompanionKeepsDifferentExecutionArgumentsSeparate(t *testing.T) {
	data := map[string]any{"results": []any{map[string]any{"chunk_id": "same", "knowledge_id": "doc", "content": "same body"}}}
	first := documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-1", true, data, map[string]any{"query": "rainfall", "mode": "keyword"})
	second := documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-2", true, data, map[string]any{"query": "river level", "mode": "keyword"})
	view := documentRetrievalCompanion([]chat.Message{first, second}, documentRetrievalCompanionBudget)
	require.Equal(t, json.Number("2"), view["unique_observation_count"], "different actual tool arguments are distinct retrieval scopes")
	require.Len(t, documentRetrievalObservationMaps(view), 2)
	scopes := view["scopes"].([]any)
	require.Equal(t, "rainfall", scopes[0].(map[string]any)["metadata"].(map[string]any)["execution_scope"].(map[string]any)["query"])
	require.Equal(t, "river level", scopes[1].(map[string]any)["metadata"].(map[string]any)["execution_scope"].(map[string]any)["query"])
}

func TestDocumentRetrievalCompanionKeepsIdentityBeforeLargeMetadataAndBodies(t *testing.T) {
	rows := []any{}
	for i, docID := range []string{"doc-a", "doc-b"} {
		rows = append(rows, map[string]any{"chunk_id": fmt.Sprintf("chunk-%d", i), "knowledge_id": docID, "knowledge_base_id": "kb", "content": strings.Repeat("长正文", 150), "unbounded_metadata": strings.Repeat("x", 5000)})
	}
	message := documentRetrievalTestMessage("search_knowledge", "compact", true, map[string]any{"results": rows, "unbounded_scope_metadata": strings.Repeat("y", 8000)})
	view := documentRetrievalCompanion([]chat.Message{message}, 1900)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1900)
	observations := documentRetrievalObservationMaps(view)
	require.GreaterOrEqual(t, len(observations), 2)
	require.NotEqual(t, observations[0]["knowledge_id"], observations[1]["knowledge_id"])
	require.Equal(t, false, observations[0]["metadata_complete"])
	require.NotEmpty(t, observations[0]["metadata_pointer"])
}

func TestDocumentRetrievalCompanionDedupUsesFullRowAndScopeMetadata(t *testing.T) {
	firstRow := map[string]any{"chunk_id": "same", "knowledge_id": "doc", "content": "same body", "content_offset": 0, "opaque_row_field": "alpha"}
	secondRow := map[string]any{"chunk_id": "same", "knowledge_id": "doc", "content": "same body", "content_offset": 0, "opaque_row_field": "beta"}
	first := documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-1", true,
		map[string]any{"results": []any{firstRow}, "opaque_scope_field": "alpha"}, map[string]any{"query": "same"})
	second := documentRetrievalTestMessageWithExecutionScope("search_knowledge", "call-2", true,
		map[string]any{"results": []any{secondRow}, "opaque_scope_field": "beta"}, map[string]any{"query": "same"})
	view := documentRetrievalCompanion([]chat.Message{first, second}, documentRetrievalCompanionBudget)
	observations := documentRetrievalObservationMaps(view)
	require.Len(t, observations, 2, "unknown source row and scope metadata participate in exact deduplication")
	require.Equal(t, "alpha", observations[0]["source_metadata"].(map[string]any)["opaque_row_field"])
	require.Equal(t, "beta", observations[1]["source_metadata"].(map[string]any)["opaque_row_field"])
}

func TestDocumentRetrievalCompanionLargeBodyHasExactReaderPointer(t *testing.T) {
	content := strings.Repeat("水🧭", 2600)
	message := documentRetrievalTestMessage("search_knowledge", "oversized", true, map[string]any{
		"results":   []any{map[string]any{"chunk_id": "large", "knowledge_id": "doc", "content": content, "content_complete": false}},
		"truncated": true, "next_offset": 4,
	})
	view := documentRetrievalCompanion([]chat.Message{message}, 1200)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1200)
	require.Equal(t, false, view["projection_complete"])
	observations := documentRetrievalObservationMaps(view)
	require.Len(t, observations, 1)
	observation := observations[0]
	require.Equal(t, false, observation["body_included"])
	reader := newReviewEvidenceReader([]chat.Message{message})
	args, _ := json.Marshal(map[string]any{"operation": "read", "evidence_id": observation["evidence_id"], "json_pointer": observation["json_pointer"], "max_chars": 6000})
	read := reader.read(string(args))
	require.Equal(t, "ok", read["status"])
	require.Equal(t, content, read["text"])
}

func TestDocumentRetrievalCompanionRejectsFailuresAndNonSourceJSON(t *testing.T) {
	fakeRows := []any{map[string]any{"chunk_id": "fake", "knowledge_id": "doc", "content": "not source data"}}
	messages := []chat.Message{
		documentRetrievalTestMessage("read_document", "failed", false, map[string]any{"chunks": fakeRows}),
		{Role: "assistant", Name: "search_knowledge", Content: `{"success":true,"data":{"results":[{"chunk_id":"fake","knowledge_id":"doc","content":"assistant prose"}]}}`},
		{Role: "tool", Name: "describe_tools", ToolCallID: "schema", Content: `{"success":true,"data":{"results":[{"chunk_id":"fake","knowledge_id":"doc","content":"schema"}]}}`},
		documentRetrievalTestMessage("search_knowledge", "nested-json", true, map[string]any{"note": `{"results":[{"chunk_id":"fake","knowledge_id":"doc","content":"nested body"}]}`}),
	}
	view := documentRetrievalCompanion(messages, documentRetrievalCompanionBudget)
	require.NotNil(t, view, "a successful real tool envelope may preserve its retrieval scope even without extracted rows")
	require.Empty(t, documentRetrievalObservationMaps(view),
		"failed calls, assistant/schema envelopes, and JSON text inside a field are not source result rows")
	require.Equal(t, `{"results":[{"chunk_id":"fake","knowledge_id":"doc","content":"nested body"}]}`,
		view["scopes"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["note"])
}

func TestDocumentRetrievalCompanionMarksUnprojectableRealRowsIncomplete(t *testing.T) {
	message := documentRetrievalTestMessage("search_knowledge", "malformed-row", true, map[string]any{
		"results": []any{map[string]any{"id": "not-a-chunk-handle", "content": "actual tool row without required identity"}},
	})
	view := documentRetrievalCompanion([]chat.Message{message}, documentRetrievalCompanionBudget)
	require.NotNil(t, view)
	require.Equal(t, false, view["projection_complete"])
	require.Equal(t, json.Number("1"), view["source_observation_count"])
	require.Equal(t, json.Number("1"), view["omitted_observation_count"])
	require.Empty(t, documentRetrievalObservationMaps(view), "missing real chunk identity is never synthesized")
	omissions := view["omitted_observations"].([]any)
	require.Equal(t, "/data/results/0", omissions[0].(map[string]any)["json_pointer"])
}
