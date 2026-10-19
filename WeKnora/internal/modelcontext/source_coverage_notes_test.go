package modelcontext

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestModelDocumentViewPreservesLiteralQueryCoverageWhenRawOutputIsReplaced(t *testing.T) {
	r := NewRegistry(true)
	result := &types.ToolResult{Success: true, Output: "RAW_OUTPUT_MUST_BE_REPLACED", Data: map[string]any{
		"display_type": "knowledge_chunks_list", "knowledge_id": "survey-report",
		"document": map[string]any{"knowledge_id": "survey-report", "title": "Survey report"},
		"chunks":   []map[string]any{{"chunk_id": "passage-real", "knowledge_id": "survey-report", "content": "Actual source passage"}},
		"query_scope": map[string]any{"mode": "literal_all_terms_in_one_chunk", "document_scan_complete": false,
			"notice": "No match does not prove the requested information is absent."},
	}}
	view := r.ModelToolResultForTool("read_document", result)
	require.NotContains(t, view, "RAW_OUTPUT_MUST_BE_REPLACED")
	require.Contains(t, view, "Actual source passage")
	require.Contains(t, view, "literal_all_terms_in_one_chunk")
	require.Contains(t, view, "No match does not prove")
	require.Contains(t, view, `"document_scan_complete":false`)
}

func TestModelRetrievalViewReportsWithheldWikiEvidenceAlongsideRealResults(t *testing.T) {
	r := NewRegistry(true)
	result := &types.ToolResult{Success: true, Output: "RAW_OUTPUT_MUST_BE_REPLACED", Data: map[string]any{
		"display_type": "search_results", "mode": "hybrid", "withheld_retracted_or_unverified_wiki_chunks": 2,
		"results": []map[string]any{{"chunk_id": "source-real", "knowledge_id": "survey-real", "content": "Current source text"}},
	}}
	view := r.ModelToolResultForTool("search_knowledge", result)
	require.Contains(t, view, "Current source text")
	require.NotContains(t, view, "RAW_OUTPUT_MUST_BE_REPLACED")
	require.Contains(t, view, `<withheld_wiki count="2"`)
	require.Contains(t, view, "Consult current source documents")
	require.Contains(t, view, "does not prove the requested information is absent")
}
