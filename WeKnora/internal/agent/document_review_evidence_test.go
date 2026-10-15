package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func documentReviewFixture(tool string, success bool, data map[string]any) []types.AgentStep {
	return []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: tool, Result: &types.ToolResult{Success: success, Data: data}}}}}
}

func TestCurrentDocumentBodyTriggersReviewWithoutBecomingTableEvidence(t *testing.T) {
	for _, tool := range []string{"read_document", "search_knowledge"} {
		t.Run(tool, func(t *testing.T) {
			field := "chunks"
			if tool == "search_knowledge" {
				field = "results"
			}
			steps := documentReviewFixture(tool, true, map[string]any{field: []any{map[string]any{
				"chunk_id": "c7", "knowledge_id": "d3", "content": "实际读取的原报告正文。",
			}}})
			require.True(t, hasCurrentDocumentEvidence(steps))
			require.False(t, hasStructuredEvidence(steps))
			require.False(t, needsStructuredAnswerReview(steps, nil, "只转述原报告。"), "document review is a distinct trigger, not table provenance")
		})
	}
}

func TestCurrentDocumentReviewRejectsCandidatesFailuresAndUnidentifiedContent(t *testing.T) {
	cases := []struct {
		name, tool string
		success    bool
		data       map[string]any
	}{
		{"directory only", "search_knowledge", true, map[string]any{"documents": []any{map[string]any{"knowledge_id": "d3", "title": "candidate", "content": "not fetched body"}}}},
		{"failed read", "read_document", false, map[string]any{"chunks": []any{map[string]any{"chunk_id": "c7", "knowledge_id": "d3", "content": "not successful"}}}},
		{"unknown tool", "external_tool", true, map[string]any{"chunks": []any{map[string]any{"chunk_id": "c7", "knowledge_id": "d3", "content": "untrusted identity"}}}},
		{"missing source", "read_document", true, map[string]any{"chunks": []any{map[string]any{"chunk_id": "c7", "content": "no source identity"}}}},
		{"missing chunk", "read_document", true, map[string]any{"chunks": []any{map[string]any{"knowledge_id": "d3", "content": "no citation identity"}}}},
		{"empty body", "read_document", true, map[string]any{"chunks": []any{map[string]any{"chunk_id": "c7", "knowledge_id": "d3", "content": "  "}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, hasCurrentDocumentEvidence(documentReviewFixture(tc.tool, tc.success, tc.data)))
		})
	}
}

func TestCurrentDocumentImageOCRTriggersReviewOnlyWhenActuallyRendered(t *testing.T) {
	for _, imageURL := range []string{"", "resource://image-actual"} {
		steps := documentReviewFixture("read_document", true, map[string]any{"chunks": []any{map[string]any{
			"chunk_id": "c7", "knowledge_id": "d3", "content": "",
			"images": []any{map[string]any{"url": imageURL, "ocr_text": "实际图表文字", "caption": "原图"}},
		}}})
		require.Equal(t, imageURL != "", hasCurrentDocumentEvidence(steps))
		require.False(t, hasStructuredEvidence(steps))
	}
}
