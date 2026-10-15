package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestReviewLargeSourceIsProjectedWithActualIdentityAndCoverage(t *testing.T) {
	source := map[string]any{
		"kind": "document_content", "knowledge_id": "d4", "chunk_id": "c12", "truncated": true,
		"coverage": map[string]any{"page": 7, "scan": map[string]any{"complete": false}},
		"content":  strings.Repeat("原表资料：数值650，原始比例-2.4%。", 10000),
	}
	results, scope := boundedReviewEvidence([]chat.Message{{Role: "tool", Name: "read_document", ToolCallID: "call-actual", Content: string(mustJSON(t, source))}}, 48000)
	require.Len(t, results, 1, "a large relevant result must never disappear wholesale")
	require.False(t, scope.ViewComplete)
	require.False(t, results[0].ViewComplete)
	require.Equal(t, "read_document", results[0].Tool)
	require.Equal(t, "call-actual", results[0].ToolCallID)
	require.NotEmpty(t, results[0].OmittedPaths)
	view := results[0].Content.(map[string]any)
	require.Equal(t, "d4", view["knowledge_id"])
	require.Equal(t, "c12", view["chunk_id"])
	require.Equal(t, true, view["truncated"])
	require.Equal(t, false, view["coverage"].(map[string]any)["scan"].(map[string]any)["complete"])
	require.Contains(t, view["content"], "原表资料：数值650")
	require.True(t, utf8.ValidString(view["content"].(string)))
	require.LessOrEqual(t, len(mustJSON(t, results)), 48000)
}

func TestReviewProjectionPreservesEveryResultAndMarksPartialArrays(t *testing.T) {
	rows := []any{}
	for i := 0; i < 2000; i++ {
		rows = append(rows, map[string]any{"source_cell": fmt.Sprintf("B%d", i+1), "value": i + 10})
	}
	large := string(mustJSON(t, map[string]any{"result_id": "r7", "rows": rows, "truncated": false}))
	messages := []chat.Message{
		{Role: "tool", Name: "structured_analyze_table", ToolCallID: "analysis", Content: large},
		{Role: "tool", Name: "read_document", ToolCallID: "read", Content: `{"knowledge_id":"d2","chunk_id":"c5","content":"different required source"}`},
	}
	results, scope := boundedReviewEvidence(messages, 12000)
	require.Len(t, results, 2)
	require.Equal(t, 2, scope.IncludedResults)
	require.Zero(t, scope.OmittedResults)
	require.False(t, results[0].ViewComplete)
	require.True(t, results[1].ViewComplete)
	require.Equal(t, "r7", results[0].Content.(map[string]any)["result_id"])
	require.Equal(t, false, results[0].Content.(map[string]any)["truncated"], "source completeness is distinct from the review view's clipping")
	require.Contains(t, strings.Join(results[0].OmittedPaths, " "), "/rows")
	require.LessOrEqual(t, len(mustJSON(t, results)), 12000)
}

func TestReviewProjectionKeepsSourceCoverageBeforeLargeChunkArrays(t *testing.T) {
	value := map[string]any{
		"chunks":         []any{map[string]any{"chunk_id": "c8", "content": strings.Repeat("原文", 20000)}},
		"coverage":       map[string]any{"complete": false, "reason": "source pagination"},
		"document_scope": map[string]any{"matched": true, "knowledge_id": "d7", "page": 3},
	}
	results, _ := boundedReviewEvidence([]chat.Message{{Role: "tool", Name: "read_document", Content: string(mustJSON(t, value))}}, 6000)
	view := results[0].Content.(map[string]any)
	require.Equal(t, false, view["coverage"].(map[string]any)["complete"])
	require.Equal(t, "d7", view["document_scope"].(map[string]any)["knowledge_id"])
	require.Equal(t, "c8", view["chunks"].([]any)[0].(map[string]any)["chunk_id"])
	require.False(t, results[0].ViewComplete)
}

func TestReviewEvidenceKeepsToolProvenanceAndDoesNotMergeDifferentToolIdentities(t *testing.T) {
	const body = `{"value":777}`
	messages := []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "call-1", Function: chat.FunctionCall{Name: "read_document"}}}},
		{Role: "tool", ToolCallID: "call-1", Content: body},
		{Role: "tool", Name: "structured_analyze_table", ToolCallID: "call-2", Content: body},
		{Role: "tool", Name: "structured_analyze_table", ToolCallID: "call-3", Content: body},
	}
	results, scope := boundedReviewEvidence(messages, 48000)
	require.Len(t, results, 2)
	require.Equal(t, "read_document", results[0].Tool)
	require.Equal(t, "structured_analyze_table", results[1].Tool)
	require.Equal(t, 2, scope.DistinctResults)
	require.True(t, scope.ViewComplete)
}

func TestReviewEnvelopeIncludesNumericDoubtsWithoutTreatingThemAsEvidence(t *testing.T) {
	review := independentStructuredReview([]chat.Message{{Role: "tool", Name: "structured_analyze_table", Content: `{"value":12.3}`}}, "describe actual result", "12.4", []string{"12.4"})
	var envelope struct {
		Numbers  []string             `json:"runtime_unmatched_numbers"`
		Evidence []reviewToolEvidence `json:"source_tool_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &envelope))
	require.Equal(t, []string{"12.4"}, envelope.Numbers)
	require.NotContains(t, string(mustJSON(t, envelope.Evidence)), "12.4")
	require.Contains(t, review[0].Content, "不是对来源语义的最终判断")
}

func TestReviewEnvelopeReportsResultCountOmissionsAndPlainTextTruncation(t *testing.T) {
	messages := []chat.Message{}
	for i := 0; i < 130; i++ {
		messages = append(messages, chat.Message{Role: "tool", Name: "read_document", ToolCallID: fmt.Sprintf("call-%d", i), Content: fmt.Sprintf("document %d %s", i, strings.Repeat("上下文", 1000))})
	}
	results, scope := boundedReviewEvidence(messages, 48000)
	require.False(t, scope.ViewComplete)
	require.Equal(t, 130, scope.DistinctResults)
	require.Equal(t, scope.DistinctResults, scope.IncludedResults+scope.OmittedResults)
	require.Positive(t, scope.OmittedResults)
	require.False(t, results[0].ViewComplete)
	require.NotEmpty(t, results[0].OmittedPaths)
	require.LessOrEqual(t, len(mustJSON(t, results)), 48000)
}
