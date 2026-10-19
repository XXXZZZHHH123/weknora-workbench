package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func bindingFixtureMessage(callID string, rows ...map[string]any) chat.Message {
	data := map[string]any{
		"knowledge_id":      "doc-source",
		"knowledge_base_id": "kb-source",
		"chunks":            rows,
	}
	payload, _ := json.Marshal(map[string]any{"success": true, "data": data})
	return chat.Message{Role: "tool", Name: tools.ToolReadDocument, ToolCallID: callID, Content: string(payload)}
}

func bindingFixtureRow(id, content string, extra map[string]any) map[string]any {
	row := map[string]any{
		"chunk_id":             id,
		"knowledge_id":         "doc-source",
		"knowledge_base_id":    "kb-source",
		"content":              content,
		"content_complete":     true,
		"content_offset":       0,
		"content_offset_space": "chunk_unicode_runes",
		"page":                 4,
	}
	for key, value := range extra {
		row[key] = value
	}
	return row
}

func bindingKeyForOccurrence(view map[string]any, row []any) []any {
	keys := view["keys"].([]any)
	return keys[row[0].(int)].([]any)
}

func bindingHandleForKey(t *testing.T, evidence map[string]any, key []any) (string, string) {
	t.Helper()
	switch key[0] {
	case "resolved":
		item := evidence["items"].([]any)[key[1].(int)].(map[string]any)
		pointer := evidence["source_pointers"].([]any)[item["source_pointer_ref"].(int)].([]any)
		result := evidence["source_results"].([]any)[pointer[0].(int)].([]any)
		return result[2].(string), pointer[1].(string)
	case "resolved_source_omitted":
		return key[5].(string), key[6].(string)
	default:
		t.Fatalf("key does not carry a readable source: %v", key[0])
		return "", ""
	}
}

func bindingSourceItemForKey(evidence map[string]any, key []any) map[string]any {
	if key[0] != "resolved" {
		return nil
	}
	return evidence["items"].([]any)[key[1].(int)].(map[string]any)
}

func TestCitationBindingViewKeepsUnicodeOccurrencesAndRepeatedUses(t *testing.T) {
	const sourceBody = "Exact source body for this test."
	messages := []chat.Message{bindingFixtureMessage("call-one", bindingFixtureRow("chunk-a", sourceBody, nil))}
	candidate := "甲项为60.6 <kb doc=\"日报\" chunk_id=\"chunk-a\" kb_id=\"kb-source\"/>。乙项为43.7 <kb chunk_id=\"chunk-a\"/>"
	evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
	require.NotNil(t, view)
	rows := view["occurrences"].([]any)
	require.Len(t, rows, 2, "same source used at two answer positions must remain two binding records")
	first := rows[0].([]any)
	second := rows[1].([]any)
	firstTag := strings.Index(candidate, "<kb")
	secondTag := strings.LastIndex(candidate, "<kb")
	require.Equal(t, utf8.RuneCountInString(candidate[:firstTag]), first[1])
	require.Equal(t, utf8.RuneCountInString(candidate[:secondTag]), second[1])
	require.Equal(t, "resolved", bindingKeyForOccurrence(view, first)[0])
	require.Equal(t, "resolved", bindingKeyForOccurrence(view, second)[0])
	require.Contains(t, first[4], "60.6")
	require.Contains(t, second[4], "43.7")
	require.NotContains(t, first[4], sourceBody)
	require.NotContains(t, second[4], sourceBody)
	evidenceItem := bindingSourceItemForKey(evidence, bindingKeyForOccurrence(view, first))
	require.NotNil(t, evidenceItem)
	evidenceID, sourcePointer := bindingHandleForKey(t, evidence, bindingKeyForOccurrence(view, first))
	require.Contains(t, sourcePointer, "/data/chunks/0/content")
	require.NotEmpty(t, evidenceID)
	require.Equal(t, 2, view["occurrence_count"])
	require.Equal(t, true, view["projection_complete"])
}

func TestCitationBindingViewDoesNotBorrowAdjacentRetrievedChunk(t *testing.T) {
	messages := []chat.Message{bindingFixtureMessage(
		"call-results",
		bindingFixtureRow("chunk-cited", "This source does not state the adjacent claim.", nil),
		bindingFixtureRow("chunk-supporting", "The exact adjacent claim is stated here.", nil),
	)}
	candidate := "The exact adjacent claim is stated here <kb chunk_id=\"chunk-cited\"/>"
	evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
	rows := view["occurrences"].([]any)
	require.Len(t, rows, 1)
	entry := rows[0].([]any)
	key := bindingKeyForOccurrence(view, entry)
	require.Equal(t, "resolved", key[0])
	require.Equal(t, "chunk-cited", bindingSourceItemForKey(evidence, key)["chunk_id"])
	encodedKey, err := json.Marshal(key)
	require.NoError(t, err)
	require.NotContains(t, string(encodedKey), "chunk-supporting")
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "chunk-supporting", "a different real row must not replace the chunk actually cited")
}

func TestCitationBindingViewJoinsRealSourceEvidenceAndReaderPointer(t *testing.T) {
	messages := []chat.Message{bindingFixtureMessage(
		"actual-read-result",
		bindingFixtureRow("chunk-cited", "This source does not state the adjacent claim.", nil),
		bindingFixtureRow("chunk-supporting", "The exact adjacent claim is stated here.", nil),
	)}
	candidate := "The exact adjacent claim is stated here <kb chunk_id=\"chunk-cited\"/>"
	evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	reader := newReviewEvidenceReader(messages)
	view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
	require.NotNil(t, view)
	records := view["occurrences"].([]any)
	require.Len(t, records, 1)
	record := records[0].([]any)
	key := bindingKeyForOccurrence(view, record)
	require.Equal(t, "resolved", key[0])
	item := bindingSourceItemForKey(evidence, key)
	require.Equal(t, "chunk-cited", item["chunk_id"])
	evidenceID, sourcePointer := bindingHandleForKey(t, evidence, key)
	require.Contains(t, sourcePointer, "/data/chunks/0/content")
	require.NotContains(t, sourcePointer, "/chunks/1/")

	readArgs, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": evidenceID,
		"json_pointer": sourcePointer, "max_chars": 100,
	})
	require.NoError(t, err)
	readResult := reader.read(string(readArgs))
	require.Equal(t, "ok", readResult["status"])
	require.Equal(t, "This source does not state the adjacent claim.", readResult["text"])
	require.NotContains(t, readResult["text"], "The exact adjacent claim")
	require.Contains(t, sourceConsistencyReviewInstructions, "Another retrieved chunk, read_document context row or same-page image cannot lend support to that citation")
}

func TestCitationBindingViewDistinguishesUnknownAmbiguousAndOmittedBody(t *testing.T) {
	messages := []chat.Message{
		bindingFixtureMessage("ambiguous-one", bindingFixtureRow("chunk-ambiguous", "first version", nil)),
		bindingFixtureMessage("ambiguous-two", bindingFixtureRow("chunk-ambiguous", "different version", nil)),
		bindingFixtureMessage("large-body", bindingFixtureRow("chunk-large", strings.Repeat("source text ", 6000), nil)),
	}
	candidate := "A <kb chunk_id=\"chunk-ambiguous\"/>. B <kb chunk_id=\"chunk-unknown\"/>. C <kb chunk_id=\"chunk-large\"/>"
	evidence := citedSourceEvidenceFromMessages(messages, candidate, citationReviewEvidenceBudget, true)
	view := citationBindingView(candidate, messages, evidence, true, citationBindingViewBudget)
	rows := view["occurrences"].([]any)
	require.Len(t, rows, 3)
	require.Equal(t, "ambiguous", bindingKeyForOccurrence(view, rows[0].([]any))[0])
	require.Equal(t, "unknown", bindingKeyForOccurrence(view, rows[1].([]any))[0])
	large := rows[2].([]any)
	largeKey := bindingKeyForOccurrence(view, large)
	require.Equal(t, "resolved", largeKey[0], "the unique source item is linked even when its body is only a fair-budget prefix")
	largeItem := bindingSourceItemForKey(evidence, largeKey)
	require.NotEqual(t, "source_body", largeItem["content_state"])
	require.Equal(t, true, view["reader_available"])
	_, sourcePointer := bindingHandleForKey(t, evidence, largeKey)
	require.Contains(t, sourcePointer, "/data/chunks/0/content")
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "source text source text", "source content is linked, never copied into binding view")
}

func TestCitationBindingViewRetainsReaderHandleWhenSourceItemWasOmitted(t *testing.T) {
	message := bindingFixtureMessage("oversized-source", bindingFixtureRow("large-metadata-chunk", "Real source text remains readable.", map[string]any{
		"source_locators": []any{map[string]any{"mapping": strings.Repeat("m", 2000), "source_hash": "fixture-hash"}},
	}))
	candidate := "Claim <kb chunk_id=\"large-metadata-chunk\"/>"
	evidence := citedSourceEvidenceFromMessages([]chat.Message{message}, candidate, citationReviewMinimumBudget, true)
	require.Empty(t, evidence["items"].([]any), "oversized metadata triggers explicit source-view omission")
	view := citationBindingView(candidate, []chat.Message{message}, evidence, true, citationBindingViewBudget)
	require.NotNil(t, view)
	row := view["occurrences"].([]any)[0].([]any)
	key := bindingKeyForOccurrence(view, row)
	require.Equal(t, "resolved_source_omitted", key[0])
	require.Equal(t, "large-metadata-chunk", key[1])
	require.Equal(t, "doc-source", key[2])
	require.Equal(t, "read_document", key[3])
	require.NotEmpty(t, key[4])
	require.NotEmpty(t, key[5])
	require.Equal(t, "/data/chunks/0/content", key[6])
	reader := newReviewEvidenceReader([]chat.Message{message})
	args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": key[5], "json_pointer": key[6]})
	require.NoError(t, err)
	result := reader.read(string(args))
	require.Equal(t, "ok", result["status"])
	require.Equal(t, "Real source text remains readable.", result["text"])
}

func TestCitationBindingViewFinalJSONBudgetIncludesGrowingOmissionCounts(t *testing.T) {
	messages := []chat.Message{bindingFixtureMessage("many-citations", bindingFixtureRow("chunk-many", "source marker that must not be duplicated", nil))}
	var candidate strings.Builder
	const total = 160
	for i := 0; i < total; i++ {
		candidate.WriteString(strings.Repeat("水文断面观察", 9))
		candidate.WriteString(" value=17 <kb chunk_id=\"chunk-many\"/>\n")
	}
	answer := candidate.String()
	evidence := citedSourceEvidenceFromMessages(messages, answer, citationReviewEvidenceBudget, true)
	view := citationBindingView(answer, messages, evidence, true, citationBindingViewBudget)
	require.NotNil(t, view)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationBindingViewBudget)
	included := view["included_occurrence_count"].(int)
	omitted := view["omitted_occurrence_count"].(int)
	require.Equal(t, total, included+omitted)
	require.Greater(t, omitted, 0)
	require.Equal(t, false, view["projection_complete"])
	require.NotContains(t, string(encoded), "source marker that must not be duplicated")
	require.Equal(t, "retained_occurrences_only", view["context_truncated_scope"])
	rows := view["occurrences"].([]any)
	truncated := 0
	for _, raw := range rows {
		entry := raw.([]any)
		if entry[9] != true || entry[5] == true || entry[8] == true {
			truncated++
		}
		if entry[9] != true {
			require.Equal(t, entry[1], entry[3])
			require.Equal(t, entry[2], entry[6])
		}
	}
	require.Equal(t, truncated, view["context_truncated_count"])
	require.LessOrEqual(t, truncated, included, "records removed for budget must not remain in the retained-context counter")
}

func TestCitationBindingViewNoCitationDoesNotRequireCitation(t *testing.T) {
	view := citationBindingView("A plain answer with no source markup.", nil, map[string]any{"items": []any{}}, false, citationBindingViewBudget)
	require.NotNil(t, view)
	require.Equal(t, 0, view["occurrence_count"])
	require.Equal(t, 0, view["unknown_occurrence_count"])
	require.Equal(t, true, view["projection_complete"])
	require.Empty(t, view["occurrences"])
}

func TestCitationBindingViewTinyBudgetReturnsNoMisleadingPartialObject(t *testing.T) {
	view := citationBindingView("A <kb chunk_id=\"x\"/>", nil, map[string]any{"items": []any{}}, false, 1)
	require.Nil(t, view, "when even the status envelope cannot fit, do not emit an oversized or misleading view")
}

func TestCitationBindingViewBoundsManyUnknownAndLongIDs(t *testing.T) {
	var candidate strings.Builder
	for i := 0; i < 180; i++ {
		candidate.WriteString("情况 <kb chunk_id=\"unknown-")
		candidate.WriteString(strconv.Itoa(i))
		candidate.WriteString("-")
		candidate.WriteString(strings.Repeat("x", 32))
		candidate.WriteString("-" + strings.Repeat("y", i%9) + "\"/>；")
	}
	longID := strings.Repeat("z", citationBindingMaxKeyIDBytes+80)
	candidate.WriteString("长标识 <kb chunk_id=\"" + longID + "\"/>；")
	candidate.WriteString("另一个长标识 <kb chunk_id=\"" + longID + "-tail\"/>；")
	candidate.WriteString("短标识 <kb chunk_id=\"bounded-unknown\"/>")

	view := citationBindingView(candidate.String(), nil, map[string]any{"items": []any{}}, true, citationBindingViewBudget)
	require.NotNil(t, view)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), citationBindingViewBudget)
	require.Equal(t, citationBindingViewBudget, view["budget_bytes"])
	require.Equal(t, 183, view["unique_citation_count"])
	require.Equal(t, 183, view["occurrence_count"])
	require.Equal(t, 183, view["unknown_occurrence_count"])
	require.Equal(t, false, view["projection_complete"])
	require.Greater(t, view["omitted_occurrence_count"].(int), 0)
	require.LessOrEqual(t, len(view["omitted_occurrences"].([]any)), citationBindingOmissionDetailLimit)
	for _, raw := range view["occurrences"].([]any) {
		row := raw.([]any)
		require.GreaterOrEqual(t, row[0].(int), 0)
		require.Less(t, row[0].(int), len(view["keys"].([]any)), "every retained occurrence index resolves to a retained key")
	}
	for _, raw := range view["omitted_occurrences"].([]any) {
		row := raw.([]any)
		keyIndex := row[1].(int)
		require.True(t, keyIndex == -1 || (keyIndex >= 0 && keyIndex < len(view["keys"].([]any))), "omitted rows never leave dangling key indexes")
	}
	require.NotContains(t, string(encoded), longID)

	constrained := citationBindingView(candidate.String(), nil, map[string]any{"items": []any{}}, true, 2048)
	require.NotNil(t, constrained, "a bounded smaller budget keeps a truthful partial envelope when the status fits")
	constrainedBytes, err := json.Marshal(constrained)
	require.NoError(t, err)
	require.LessOrEqual(t, len(constrainedBytes), 2048)
	require.Equal(t, 2048, constrained["budget_bytes"])
	require.Equal(t, false, constrained["projection_complete"])
	require.Greater(t, constrained["omitted_occurrence_count"].(int), 0)
	for _, raw := range constrained["occurrences"].([]any) {
		row := raw.([]any)
		require.Less(t, row[0].(int), len(constrained["keys"].([]any)))
	}
	require.NotContains(t, string(constrainedBytes), longID)
}

func TestCitationBindingViewFinalBudgetIncludesExactContextCounts(t *testing.T) {
	message := bindingFixtureMessage("many-contexts", bindingFixtureRow("source-span", "Observed source text.", nil))
	var candidate strings.Builder
	const total = 240
	for i := 0; i < total; i++ {
		candidate.WriteString("观测值仍有依据。<kb chunk_id=\"source-span\"/>；")
	}
	answer := candidate.String()
	evidence := citedSourceEvidenceFromMessages([]chat.Message{message}, answer, citationReviewEvidenceBudget, true)
	for _, budget := range []int{2048, 2049, 3072, 4096, 6144, citationBindingViewBudget} {
		view := citationBindingView(answer, []chat.Message{message}, evidence, true, budget)
		require.NotNil(t, view, "a budget large enough for the status envelope should retain a truthful partial view")
		encoded, err := json.Marshal(view)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), budget)
		rows := view["occurrences"].([]any)
		require.Equal(t, len(rows), view["included_occurrence_count"])
		require.Equal(t, total-len(rows), view["omitted_occurrence_count"])
		require.Equal(t, compactBindingTruncatedCount(rows), view["context_truncated_count"],
			"the final serialized counter matches retained rows and cannot invalidate the budget check")
	}
}

func TestCitationIntReadsStrictJSONNumbersForDiagnostics(t *testing.T) {
	require.Equal(t, 42, citationInt(json.Number("42")))
	require.Equal(t, 0, citationInt(json.Number("42.5")))
	require.Equal(t, 0, citationInt(json.Number("-1")))
	require.Equal(t, 0, citationInt(json.Number("not-a-number")))
}
