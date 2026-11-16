package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSearchOutputPreservesCurrentChunkIdentity(t *testing.T) {
	for _, revision := range []int{0, 7} {
		for _, rewritten := range []bool{false, true} {
			result := &types.SearchResult{ID: "current-source", KnowledgeID: "document", KnowledgeBaseID: "library",
				Content: "Actual source content.", ContentRevision: revision, ContentRewritten: rewritten,
				ChunkType: types.ChunkTypeText, ChunkIndex: 5, StartAt: 20, EndAt: 42, ParentChunkID: "source-parent",
				SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 3, SourceHash: strings.Repeat("a", 64)}}}
			tool := &SearchKnowledgeTool{}
			output := tool.formatOutput(context.Background(), []*searchResultWithMeta{{SearchResult: result, KnowledgeBaseID: "library"}}, []string{"library"}, "source", SearchModeKeyword)
			row := output.Data["results"].([]map[string]any)[0]
			require.Equal(t, revision, row["content_revision"])
			require.Equal(t, result.SourceLocators, row["source_locators"])
			require.Equal(t, result.StartAt, row["start_at"])
			require.Equal(t, result.EndAt, row["end_at"])
			require.Equal(t, result.ParentChunkID, row["parent_chunk_id"])
			require.Equal(t, result.Content, row["content"])
			if rewritten {
				require.Equal(t, true, row["content_rewritten"], "expanded bodies cannot masquerade as original full chunks")
			} else {
				require.NotContains(t, row, "content_rewritten")
			}
		}
	}
}

func TestWriteKnowledgeMetadataHeaderDeduplicatesByKnowledge(t *testing.T) {
	t.Parallel()
	results := []*searchResultWithMeta{
		{
			SearchResult: &types.SearchResult{
				KnowledgeID: "knowledge-1", KnowledgeTitle: "Document <One>",
				KnowledgeCustomMetadata: "region: Shanghai & Suzhou",
			},
			KnowledgeBaseID: "kb-1",
		},
		{
			SearchResult: &types.SearchResult{
				KnowledgeID: "knowledge-1", KnowledgeTitle: "Document <One>",
				KnowledgeCustomMetadata: "region: Shanghai & Suzhou",
			},
			KnowledgeBaseID: "kb-1",
		},
		{
			SearchResult: &types.SearchResult{
				KnowledgeID: "knowledge-2", KnowledgeTitle: "Document Two",
			},
			KnowledgeBaseID: "kb-1",
		},
		{
			SearchResult: &types.SearchResult{
				KnowledgeID: "knowledge-3", KnowledgeTitle: "Document Three",
				KnowledgeCustomMetadata: "owner: Alice",
			},
			KnowledgeBaseID: "kb-2",
		},
	}

	var output strings.Builder
	writeKnowledgeMetadataHeader(&output, results)
	text := output.String()

	require.Equal(t, 1, strings.Count(text, `knowledge_id="knowledge-1"`))
	require.Equal(t, 1, strings.Count(text, `knowledge_id="knowledge-3"`))
	require.NotContains(t, text, `knowledge_id="knowledge-2"`)
	require.Equal(t, 2, strings.Count(text, "<metadata>"))
	require.Contains(t, text, "Document &lt;One&gt;")
	require.Contains(t, text, "Shanghai &amp; Suzhou")
}

func TestWriteKnowledgeMetadataHeaderOmitsEmptySection(t *testing.T) {
	t.Parallel()
	var output strings.Builder
	writeKnowledgeMetadataHeader(&output, []*searchResultWithMeta{{
		SearchResult: &types.SearchResult{KnowledgeID: "knowledge-1"},
	}})
	require.Empty(t, output.String())
}
