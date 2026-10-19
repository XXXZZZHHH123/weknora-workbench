package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func successfulSearchResult(t *testing.T, callID string, data any) chat.Message {
	t.Helper()
	content, err := json.Marshal(map[string]any{"success": true, "data": data})
	require.NoError(t, err)
	return chat.Message{Role: "tool", Name: "read_document", ToolCallID: callID, Content: string(content)}
}

func reviewSearchMatches(t *testing.T, response map[string]any) []reviewEvidenceSearchMatch {
	t.Helper()
	raw, ok := response["matches"]
	require.True(t, ok)
	matches, ok := raw.([]reviewEvidenceSearchMatch)
	require.True(t, ok)
	return matches
}

func TestReviewEvidenceSearchUsesLiteralUnicodeOffsetsAndEscapedPointers(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "query-a", map[string]any{
		"rows": []any{map[string]any{"a/b~c": "前🌾 USDA 调查记录"}},
	})})
	record := reader.records[0]
	response := reader.search("USDA", record.ID, "", 24)
	require.Equal(t, "ok", response["status"])
	require.Equal(t, true, response["scan_complete"])
	require.Equal(t, 1, response["match_count"])
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 1)
	match := matches[0]
	require.Equal(t, record.ID, match.EvidenceID)
	require.Equal(t, "/data/rows/0/a~1b~0c", match.JSONPointer)
	require.Equal(t, utf8.RuneCountInString("前🌾 "), match.MatchOffset)
	require.Equal(t, 4, match.MatchLength)
	require.Contains(t, match.ContextText, "USDA")
	require.LessOrEqual(t, match.ContentOffset, match.MatchOffset)

	args, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": match.EvidenceID,
		"json_pointer": match.JSONPointer, "offset": match.MatchOffset, "max_chars": match.MatchLength,
	})
	require.NoError(t, err)
	read := reader.read(string(args))
	require.Equal(t, "USDA", read["text"], "the reported pointer and Unicode offset must resolve through the existing immutable reader")
}

func TestReviewEvidenceSearchOnlyUsesSuccessfulToolDataLeaves(t *testing.T) {
	failed, err := json.Marshal(map[string]any{"success": false, "data": map[string]any{"content": "needle-failed"}})
	require.NoError(t, err)
	accessOnly, err := json.Marshal(map[string]any{"runtime_source_access": map[string]any{"description": "needle-access"}})
	require.NoError(t, err)
	schemaMessage := chat.Message{Role: "assistant", Name: "structured_data", Content: `{"schema":"needle-schema"}`}
	reader := newReviewEvidenceReader([]chat.Message{
		{Role: "tool", Name: "read_document", ToolCallID: "failed", Content: string(failed)},
		{Role: "tool", Name: "read_document", ToolCallID: "access", Content: string(accessOnly)},
		successfulSearchResult(t, "plan", map[string]any{"content": "needle-plan"}),
		{Role: "tool", Name: "plan_task", ToolCallID: "plan-task", Content: `{"success":true,"data":{"content":"needle-plan-task"}}`},
		{Role: "tool", Name: expandTaskToolsName, ToolCallID: "expand", Content: `{"success":true,"data":{"content":"needle-expand"}}`},
		{Role: "tool", Name: "structured_data", ToolCallID: "schema-tool", Content: `{"success":true,"data":{"operation":"describe_tools","schema":"needle-schema-result"}}`},
		schemaMessage,
		successfulSearchResult(t, "valid", map[string]any{"chunks": []any{map[string]any{"content": "needle-valid"}}}),
	})
	response := reader.search("needle", "", "", 24)
	require.Equal(t, "ok", response["status"])
	require.Equal(t, 2, response["match_count"], "successful generic tool data can contain real returned text; only known planner/catalog protocols are excluded")
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 2)
	require.Contains(t, matches[0].ContextText, "needle-plan")
	require.Contains(t, matches[1].ContextText, "needle-valid")
	for _, match := range matches {
		require.NotContains(t, match.ContextText, "failed")
		require.NotContains(t, match.ContextText, "access")
		require.NotContains(t, match.ContextText, "schema")
		require.NotContains(t, match.ContextText, "expand")
	}
}

func TestReviewEvidenceSearchValidatesHandleAndDataSubtreePointers(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "one", map[string]any{"field": "needle"})})
	require.Equal(t, "unknown_evidence_id", reader.search("needle", "forged", "", 24)["status"])
	require.Equal(t, "invalid_scope_pointer", reader.search("needle", reader.records[0].ID, "/execution_scope", 24)["status"])

	record := reader.records[0]
	missing := reader.search("needle", record.ID, "/data/not-present", 24)
	require.Equal(t, "invalid_scope_pointer", missing["status"])
	require.Equal(t, "invalid_arguments", reader.search("needle", "", "/data/field", 24)["status"], "an explicit pointer must name one real result handle")
	require.Equal(t, "invalid_arguments", reader.search("", "", "", 24)["status"])
	require.Equal(t, "invalid_arguments", reader.search(strings.Repeat("x", reviewEvidenceSearchMaxQueryRunes+1), "", "", 24)["status"])

	valid := reader.search("needle", record.ID, "/data/field", 24)
	require.Equal(t, true, valid["scan_complete"])
	require.Equal(t, 1, valid["match_count"])
}

func TestReviewEvidenceSearchReturnsRoundRobinMatchesAcrossResults(t *testing.T) {
	messages := []chat.Message{
		successfulSearchResult(t, "first", map[string]any{"body": strings.Repeat("x", 40)}),
		successfulSearchResult(t, "second", map[string]any{"body": strings.Repeat("x", 40)}),
		successfulSearchResult(t, "third", map[string]any{"body": strings.Repeat("x", 40)}),
	}
	reader := newReviewEvidenceReader(messages)
	response := reader.search("x", "", "", 6)
	require.Equal(t, 120, response["match_count"])
	require.Equal(t, 114, response["matches_omitted"])
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 6)
	require.Equal(t, reader.records[0].ID, matches[0].EvidenceID)
	require.Equal(t, reader.records[1].ID, matches[1].EvidenceID)
	require.Equal(t, reader.records[2].ID, matches[2].EvidenceID)
	require.Equal(t, reader.records[0].ID, matches[3].EvidenceID)
	require.Equal(t, 0, matches[0].MatchOffset)
	require.Equal(t, 1, matches[3].MatchOffset)
}

func TestReviewEvidenceSearchBudgetMakesPartialAndZeroHitsNonSemantic(t *testing.T) {
	largeText := strings.Repeat("a", reviewEvidenceSearchRuneBudget) + "needle"
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "large", map[string]any{"content": largeText})})
	reader.remaining = 4000
	response := reader.search("needle", "", "", 24)
	require.Equal(t, "partial", response["status"])
	require.Equal(t, false, response["scan_complete"])
	require.Equal(t, reviewEvidenceSearchRuneBudget, response["checked_runes"])
	require.Equal(t, reviewEvidenceSearchRuneBudget, reader.searchScannedRunes)
	require.Equal(t, 0, response["match_count"], "the literal after the scan cap is not inspected")
	require.Contains(t, response["notice"], "does not prove semantic absence")
	require.GreaterOrEqual(t, reader.remaining, 0)

	budgeted := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "many", map[string]any{"content": strings.Repeat("x", 200)})})
	budgeted.remaining = 1800
	startBudget := budgeted.remaining
	limited := budgeted.search("x", "", "", 24)
	require.Equal(t, "ok", limited["status"])
	encoded, err := json.Marshal(limited)
	require.NoError(t, err)
	returnedRunes := utf8.RuneCount(encoded)
	require.Equal(t, startBudget-returnedRunes, budgeted.remaining, "returned search JSON consumes the same character budget as reads")
	require.GreaterOrEqual(t, budgeted.remaining, 0)
	require.Greater(t, limited["matches_omitted"], 0, "search trims match context/results instead of exceeding the output budget")

	noHit := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "no-hit", map[string]any{"content": "scanned content"})})
	negative := noHit.search("absent-literal", "", "", 24)
	require.Equal(t, true, negative["scan_complete"])
	require.Equal(t, 0, negative["match_count"])
	require.Contains(t, negative["notice"], "does not prove semantic absence")
}

func TestReviewEvidenceSearchResumesPartialStringWithLiteralOverlap(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "partial", map[string]any{"content": "zzXYtail"})})
	reader.searchScannedRunes = reviewEvidenceSearchRuneBudget - 4
	response := reader.search("XYZ", "", "", 24)
	require.Equal(t, "partial", response["status"])
	require.Equal(t, false, response["scan_complete"])
	paths, ok := response["omitted_scan_paths"].([]reviewEvidenceSearchOmission)
	require.True(t, ok)
	require.Len(t, paths, 1)
	require.Equal(t, "/data/content", paths[0].JSONPointer)
	require.Equal(t, 2, paths[0].NextContentOffset, "resume at the first start position that may straddle the checked prefix")
}

func TestReviewEvidenceSearchBoundsCandidateStorageAcrossManyResults(t *testing.T) {
	messages := make([]chat.Message, 300)
	for i := range messages {
		messages[i] = successfulSearchResult(t, "candidate-"+strconv.Itoa(i), map[string]any{"text": "needle"})
	}
	reader := newReviewEvidenceReader(messages)
	response := reader.search("needle", "", "", 24)
	require.Equal(t, 300, response["match_count"])
	require.Equal(t, reviewEvidenceSearchCandidateBudget, response["stored_match_candidate_count"])
	require.Greater(t, response["matches_omitted"], 0)
	require.Equal(t, false, response["matches_complete"])
	require.Len(t, reviewSearchMatches(t, response), 24)
}

func TestReviewEvidenceSearchNodeAndDepthBudgetsFailPartial(t *testing.T) {
	reader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "nodes", map[string]any{"text": "needle"})})
	reader.searchVisitedNodes = reviewEvidenceSearchNodeBudget - 1
	response := reader.search("needle", "", "", 24)
	require.Equal(t, "partial", response["status"])
	require.Equal(t, false, response["scan_complete"])
	require.Equal(t, 0, response["match_count"])
	require.Equal(t, 1, response["visited_nodes"])
	paths, ok := response["omitted_scan_paths"].([]reviewEvidenceSearchOmission)
	require.True(t, ok)
	require.Len(t, paths, 1)
	require.Equal(t, "/data", paths[0].JSONPointer)
	require.Equal(t, 0, paths[0].NextContentOffset)

	var deep any = "needle"
	for i := 0; i < reviewEvidenceSearchDepthLimit+2; i++ {
		deep = map[string]any{"nested": deep}
	}
	deepReader := newReviewEvidenceReader([]chat.Message{successfulSearchResult(t, "depth", deep)})
	depthResponse := deepReader.search("needle", "", "", 24)
	require.Equal(t, "partial", depthResponse["status"])
	require.Equal(t, false, depthResponse["scan_complete"])
	require.Equal(t, 0, depthResponse["match_count"])
	require.LessOrEqual(t, depthResponse["visited_nodes"], reviewEvidenceSearchDepthLimit+1)
}

func TestReviewEvidenceSearchTrimsOmissionDetailsWithinOutputBudget(t *testing.T) {
	messages := make([]chat.Message, reviewEvidenceSearchMaxMatches+1)
	for i := range messages {
		messages[i] = successfulSearchResult(t, "omission-"+strconv.Itoa(i), map[string]any{"text": "needle"})
	}
	reader := newReviewEvidenceReader(messages)
	reader.searchVisitedNodes = reviewEvidenceSearchNodeBudget
	reader.remaining = 1200
	response := reader.search("needle", "", "", 24)
	require.Equal(t, "partial", response["status"])
	require.Equal(t, false, response["scan_complete"])
	require.Equal(t, reviewEvidenceSearchMaxMatches+1, response["partial_result_count"])
	require.Equal(t, true, response["output_truncated"])
	require.Greater(t, response["omitted_scan_path_details"], 0)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.LessOrEqual(t, utf8.RuneCount(encoded), 1200)
}

func TestReviewEvidenceSearchLimitedViewIncludesLaterResultEvidence(t *testing.T) {
	messages := make([]chat.Message, 40)
	for i := range messages {
		messages[i] = successfulSearchResult(t, "distributed-"+strconv.Itoa(i), map[string]any{"text": "field-note"})
	}
	reader := newReviewEvidenceReader(messages)
	response := reader.search("field-note", "", "", 8)
	matches := reviewSearchMatches(t, response)
	require.Len(t, matches, 8)
	require.Equal(t, 40, response["match_count"])
	require.Equal(t, 32, response["matches_omitted"])
	require.False(t, response["matches_complete"].(bool))
	first, last := reader.byID[matches[0].EvidenceID], reader.byID[matches[len(matches)-1].EvidenceID]
	require.Less(t, first, 10)
	require.Greater(t, last, 30, "a limited result view must not systematically exclude later actual source calls")
}
