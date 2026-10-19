package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadDocumentLiteralQueryRequiresEveryTermInOneChunkAndDoesNotProveAbsence(t *testing.T) {
	tool, repo := newReadDocumentFixture(2)
	repo.ordered[0].Content = "The report discusses cotton inventory."
	repo.ordered[1].Content = "Warehouse receipts are explained separately."

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","query":"cotton warehouse"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 0, result.Data["match_count"], "terms split across chunks are not a single-chunk match")

	scope := result.Data["query_scope"].(map[string]any)
	require.Equal(t, "literal_all_terms_in_one_chunk", scope["mode"])
	require.Equal(t, []string{"cotton", "warehouse"}, scope["literal_terms"])
	require.Equal(t, true, scope["document_scan_complete"])
	notice := scope["notice"].(string)
	require.Contains(t, notice, "No match does not prove the document lacks the requested information")
	require.Contains(t, notice, "terms may be worded differently or occur in different chunks")
	require.Contains(t, result.Output, "<query_scope>", "the semantic limitation is also visible in the rendered tool result")
	require.Contains(t, result.Output, "literal_all_terms_in_one_chunk")
}

func TestReadDocumentRegexScopeIsExplicitlyNonSemantic(t *testing.T) {
	tool, repo := newReadDocumentFixture(2)
	repo.ordered[0].Content = "Cotton and acreage appear here."
	repo.ordered[1].Content = "Warehouse receipts appear here."

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","query":"sunflower|barley","regex":true}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 0, result.Data["match_count"])
	scope := result.Data["query_scope"].(map[string]any)
	require.Equal(t, "regular_expression_in_one_chunk", scope["mode"])
	require.NotContains(t, scope, "literal_terms")
	require.Equal(t, true, scope["document_scan_complete"])
	require.Contains(t, scope["notice"].(string), "not semantic coverage of the topic")
	require.Contains(t, scope["notice"].(string), "No match does not prove the document lacks the requested information")
}

func TestReadDocumentQueryOffsetDoesNotClaimWholeDocumentScan(t *testing.T) {
	tool, repo := newReadDocumentFixture(4)
	for i, chunk := range repo.ordered {
		chunk.Content = []string{
			"Earlier section mentions cotton.",
			"Later section mentions acreage.",
			"More discussion of acreage follows.",
			"The ending has no requested literal.",
		}[i]
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","query":"cotton","offset":1}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 0, result.Data["match_count"], "the earlier match lies before the requested scan offset")
	scope := result.Data["query_scope"].(map[string]any)
	require.Equal(t, 1, scope["offset"])
	require.Equal(t, true, scope["scan_complete"], "the requested suffix was fully scanned")
	require.Equal(t, false, scope["document_scan_complete"], "a suffix scan cannot claim a whole-document scan")
	require.Contains(t, scope["notice"].(string), "No match does not prove the document lacks the requested information")
	require.NotContains(t, result.Output, `"document_scan_complete":true`)
}
