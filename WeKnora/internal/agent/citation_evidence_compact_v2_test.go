package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func compactV2Message(call string, chunks ...map[string]any) chat.Message {
	payload, _ := json.Marshal(map[string]any{"success": true, "data": map[string]any{
		"knowledge_id": "kb-river", "knowledge_base_id": "kb-scope", "total_chunks": 99,
		"truncated": true, "next_offset": 12, "query_scope": map[string]any{"mode": "literal_all_terms_in_one_chunk", "document_scan_complete": false},
		"chunks": chunks,
	}})
	return chat.Message{Role: "tool", Name: tools.ToolSearchKnowledge, ToolCallID: call, Content: string(payload)}
}

func compactV2Chunk(id, body string, extra map[string]any) map[string]any {
	row := map[string]any{"chunk_id": id, "knowledge_id": "kb-river", "chunk_index": 7,
		"content": body, "content_complete": true, "content_offset": 0, "content_offset_space": "chunk_unicode_runes"}
	for key, value := range extra {
		row[key] = value
	}
	return row
}

func compactV2ResolveItem(t *testing.T, evidence map[string]any, id string) map[string]any {
	t.Helper()
	for _, raw := range evidence["items"].([]any) {
		item := raw.(map[string]any)
		if item["chunk_id"] == id {
			return item
		}
	}
	t.Fatalf("no item for %s", id)
	return nil
}

func compactV2ResolveBody(t *testing.T, messages []chat.Message, evidence map[string]any, item map[string]any) string {
	t.Helper()
	pointer := evidence["source_pointers"].([]any)[item["source_pointer_ref"].(int)].([]any)
	result := evidence["source_results"].([]any)[pointer[0].(int)].([]any)
	reader := newReviewEvidenceReader(messages)
	args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": result[2], "json_pointer": pointer[1]})
	require.NoError(t, err)
	out := reader.read(string(args))
	require.Equal(t, "ok", out["status"])
	return out["text"].(string)
}

func TestCitationEvidenceV2FairBodiesDeduplicatesRepeatedOccurrences(t *testing.T) {
	chunks := make([]map[string]any, 11)
	var answer strings.Builder
	for i := range chunks {
		id := fmt.Sprintf("water-source-%02d", i)
		body := strings.Repeat("水文观测", 180) + fmt.Sprintf(" station %d observed upstream", i)
		chunks[i] = compactV2Chunk(id, body, nil)
		answer.WriteString(fmt.Sprintf("结论%d <kb chunk_id=\"%s\"/>；复核%d <kb chunk_id=\"%s\"/>。", i, id, i, id))
	}
	messages := []chat.Message{compactV2Message("call-water-1", chunks...)}
	evidence := citedSourceEvidenceFromMessages(messages, answer.String(), citationReviewEvidenceBudget, true)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
	require.Equal(t, 11, len(evidence["items"].([]any)), "one source item per unique ID, even when its citation repeats")
	for i := range chunks {
		id := fmt.Sprintf("water-source-%02d", i)
		item := compactV2ResolveItem(t, evidence, id)
		require.NotEmpty(t, item["content"], "fair allocation gives every distinct cited body a segment")
		require.Equal(t, "review_prefix", item["content_state"])
		require.Equal(t, false, item["review_segment_complete"])
		require.Equal(t, true, item["source_content_complete"])
		require.Contains(t, compactV2ResolveBody(t, messages, evidence, item), fmt.Sprintf("station %d", i), "reader table resolves to the exact original source pointer")
	}
	view := citationBindingView(answer.String(), messages, evidence, true, citationBindingViewBudget)
	bindingJSON, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(bindingJSON), citationBindingViewBudget)
	require.Equal(t, 22, view["occurrence_count"])
	require.Equal(t, 22, view["included_occurrence_count"])
	require.Equal(t, true, view["projection_complete"])
	keys := view["keys"].([]any)
	rows := view["occurrences"].([]any)
	require.Len(t, keys, 11)
	for n := 0; n < len(rows); n += 2 {
		require.Equal(t, rows[n].([]any)[0], rows[n+1].([]any)[0], "repeated uses store a shared key index")
	}
}

func TestCitationEvidenceV2KeepsUnknownConflictAndTruncatedSourceLimits(t *testing.T) {
	one := compactV2Chunk("ambiguous", "first source", nil)
	two := compactV2Chunk("ambiguous", "different source", nil)
	partial := compactV2Chunk("partial", "partial river segment", map[string]any{"content_complete": false, "content_offset": 12, "next_content_offset": 35, "source_locators": []any{map[string]any{"mapping": "partial", "source_hash": "fixture-hash"}}})
	messages := []chat.Message{compactV2Message("call-one", one), compactV2Message("call-two", two, partial)}
	candidate := `<kb chunk_id="ambiguous"/> <kb chunk_id="unknown-id"/> <kb chunk_id="partial"/>`
	evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	require.Contains(t, evidence["ambiguous_citation_ids"], "ambiguous")
	view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
	keys := view["keys"].([]any)
	require.Equal(t, "ambiguous", keys[0].([]any)[0])
	require.Equal(t, "unknown", keys[1].([]any)[0])
	require.Equal(t, "resolved", keys[2].([]any)[0])
	item := compactV2ResolveItem(t, evidence, "partial")
	require.Equal(t, false, item["source_content_complete"])
	require.Equal(t, json.Number("12"), item["source_content_offset"])
	require.Equal(t, json.Number("35"), item["source_next_content_offset"])
	pointerRef, ok := item["source_pointer_ref"].(int)
	require.True(t, ok)
	require.GreaterOrEqual(t, pointerRef, 0)
	require.Less(t, pointerRef, len(evidence["source_pointers"].([]any)))
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
}

func TestCitationBindingV2OrderChangesKeepExactSourceMappingAndUnicodeOffsets(t *testing.T) {
	messages := []chat.Message{compactV2Message("call-order",
		compactV2Chunk("source-a", "A unique body", nil), compactV2Chunk("source-b", "B unique body", nil))}
	for _, candidate := range []string{
		"上游命中 <kb chunk_id=\"source-b\"/>，下游 <kb chunk_id=\"source-a\"/>。",
		"下游 <kb chunk_id=\"source-a\"/>，上游命中 <kb chunk_id=\"source-b\"/>。",
	} {
		evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
		view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
		keys, rows := view["keys"].([]any), view["occurrences"].([]any)
		require.Len(t, keys, 2)
		require.Len(t, rows, 2)
		for _, raw := range rows {
			row := raw.([]any)
			key := keys[row[0].(int)].([]any)
			item := evidence["items"].([]any)[key[1].(int)].(map[string]any)
			runes := []rune(candidate)
			require.Contains(t, string(runes[row[1].(int):row[2].(int)]), item["chunk_id"].(string))
		}
	}
}

func TestCitationEvidenceV2StrictBudgetsWithChineseAndSourceScopes(t *testing.T) {
	chunks := make([]map[string]any, 17)
	var answer strings.Builder
	for i := range chunks {
		id := fmt.Sprintf("流域-来源-%02d", i)
		chunks[i] = compactV2Chunk(id, strings.Repeat("河道测站数据", 240), map[string]any{"source_locators": []any{map[string]any{"type": "page", "page": i + 1, "mapping": "exact", "partial": i%2 == 1, "source_hash": fmt.Sprintf("hash-%02d", i)}}})
		answer.WriteString("观测 <kb chunk_id=\"" + id + "\"/>。")
	}
	messages := []chat.Message{compactV2Message("call-unicode", chunks...)}
	evidence := citedSourceEvidenceFromMessages(messages, answer.String(), citationReviewEvidenceBudget, true)
	view := citationBindingView(answer.String(), messages, evidence, true, citationBindingViewBudget)
	evidenceJSON, err := json.Marshal(evidence)
	require.NoError(t, err)
	bindingJSON, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(evidenceJSON), citationReviewEvidenceBudget)
	require.LessOrEqual(t, len(bindingJSON), citationBindingViewBudget)
	require.Equal(t, 17, view["occurrence_count"])
	require.Equal(t, 17, view["included_occurrence_count"])
	require.Equal(t, false, evidence["view_complete"], "review-budget prefixes remain explicitly partial")
	require.NotEmpty(t, evidence["scopes"])
	require.NotEmpty(t, evidence["source_pointers"])
}

func TestCitationEvidenceV2PreservesRepeatedFetchOffsetsAndScope(t *testing.T) {
	first := compactV2Chunk("same-body", "river sample body", map[string]any{
		"content_complete": false, "content_offset": 16, "next_content_offset": 33,
	})
	second := compactV2Chunk("same-body", "river sample body", map[string]any{
		"content_complete": true, "content_offset": 16,
	})
	firstMessage := compactV2Message("call-partial", first)
	secondMessage := compactV2Message("call-complete", second)
	firstPayload, err := decodeCitationJSON(firstMessage.Content)
	require.NoError(t, err)
	firstPayload["data"].(map[string]any)["query_scope"] = map[string]any{"page_complete": false, "page": 4}
	encoded, err := json.Marshal(firstPayload)
	require.NoError(t, err)
	firstMessage.Content = string(encoded)
	secondPayload, err := decodeCitationJSON(secondMessage.Content)
	require.NoError(t, err)
	secondPayload["data"].(map[string]any)["query_scope"] = map[string]any{"page_complete": true, "page": 4}
	encoded, err = json.Marshal(secondPayload)
	require.NoError(t, err)
	secondMessage.Content = string(encoded)

	candidate := `<kb chunk_id="same-body"/>`
	evidence := citedSourceEvidenceFromMessages([]chat.Message{firstMessage, secondMessage}, candidate, citationReviewEvidenceBudget, true)
	item := compactV2ResolveItem(t, evidence, "same-body")
	require.Equal(t, false, item["source_content_complete"])
	require.Equal(t, json.Number("33"), item["source_next_content_offset"])
	observations := item["additional_source_observations"].([]any)
	require.Len(t, observations, 1)
	observed := observations[0].(map[string]any)
	require.Equal(t, true, observed["source_content_complete"])
	require.Equal(t, json.Number("16"), observed["source_content_offset"])
	require.NotEqual(t, item["source_scope_ref"], observed["source_scope_ref"], "distinct successful fetch scope remains independently addressable")
	for _, raw := range []any{item, observed} {
		row := raw.(map[string]any)
		pointerIndex := row["source_pointer_ref"].(int)
		pointer := evidence["source_pointers"].([]any)[pointerIndex].([]any)
		result := evidence["source_results"].([]any)[pointer[0].(int)].([]any)
		require.NotEmpty(t, result[2])
		require.Equal(t, "/data/chunks/0/content", pointer[1])
	}
}

func TestCitationEvidenceV2MinimumAndMaximumBudgetsAreHonest(t *testing.T) {
	oversized := compactV2Chunk("large-metadata", "one real source body", map[string]any{
		"source_locators": []any{map[string]any{"mapping": strings.Repeat("x", 2000), "source_hash": "fixture-hash"}},
	})
	message := compactV2Message("call-large", oversized)
	candidate := `<kb chunk_id="large-metadata"/>`
	minimum := citedSourceEvidenceFromMessages([]chat.Message{message}, candidate, 1, true)
	encoded, err := json.Marshal(minimum)
	require.NoError(t, err)
	require.Equal(t, citationReviewMinimumBudget, minimum["budget_bytes"])
	require.LessOrEqual(t, len(encoded), citationReviewMinimumBudget)
	require.LessOrEqual(t, citationReviewViewSize(minimum), citationReviewMinimumBudget)
	require.Equal(t, false, minimum["view_complete"])
	require.Equal(t, true, minimum["omitted_evidence"])
	require.Contains(t, minimum["notice"], "not establish source absence")

	maximum := citedSourceEvidenceFromMessages([]chat.Message{message}, candidate, citationReviewEvidenceBudget*3, true)
	require.Equal(t, citationReviewEvidenceBudget, maximum["budget_bytes"])
	encoded, err = json.Marshal(maximum)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationReviewEvidenceBudget)
}

func TestCitationEvidenceV2UnknownEmptyBodyCannotMarkViewComplete(t *testing.T) {
	view := map[string]any{"view_complete": false, "items": []any{}}
	registry := newCitationReviewRegistry(view)
	record := citationReviewChunk{chunkID: "unknown-empty", knowledgeID: "kb-river", content: "", contentKnown: false}
	items := []any{citationReviewItem(record, 0, false, registry)}
	view["items"] = items

	allocateCitationBodiesFairly(view, items, map[string]citationReviewChunk{"unknown-empty": record}, 4096, false, registry, true)

	require.Equal(t, "content_unknown", items[0].(map[string]any)["content_state"])
	require.Equal(t, false, items[0].(map[string]any)["content_known"])
	require.Equal(t, false, view["view_complete"], "an unknown body cannot be promoted to a complete source-empty projection")
}
