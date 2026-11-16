package tools

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSearchOutputProvesExcerptWithoutReplacingResultIdentity(t *testing.T) {
	chunk := &types.Chunk{ID: "chunk-a", KnowledgeID: "doc-a", KnowledgeBaseID: "library-a", ContentRevision: 7,
		ChunkIndex: 12, ChunkType: types.ChunkTypeText, ParentChunkID: "parent-a", StartAt: 20, EndAt: 48,
		Content: "前文🙂\nThe observed value is 46.2.\n尾文", SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 3}}}
	for _, tc := range []struct {
		name, body string
		complete   bool
	}{
		{"full", chunk.Content, true}, {"excerpt", "The observed value is 46.2.", false}, {"rewritten", "This is invented.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &types.SearchResult{ID: chunk.ID, KnowledgeID: chunk.KnowledgeID, KnowledgeBaseID: chunk.KnowledgeBaseID,
				Content: tc.body, ContentRevision: chunk.ContentRevision, ChunkIndex: chunk.ChunkIndex, ChunkType: chunk.ChunkType,
				StartAt: chunk.StartAt, EndAt: chunk.EndAt, ParentChunkID: chunk.ParentChunkID, SourceLocators: chunk.SourceLocators, SourceChunk: chunk}
			output := (&SearchKnowledgeTool{}).formatOutput(context.Background(), []*searchResultWithMeta{{SearchResult: result, KnowledgeBaseID: "library-a"}}, []string{"library-a"}, "observed", SearchModeKeyword)
			row := output.Data["results"].([]map[string]any)[0]
			require.Equal(t, result.ContentRevision, row["content_revision"])
			require.Equal(t, result.ChunkIndex, row["chunk_index"])
			require.Equal(t, result.SourceLocators, row["source_locators"])
			require.Equal(t, tc.complete, row["content_complete"])
			if tc.name == "excerpt" {
				view := row["content_source_view"].(*types.ChunkContentView)
				require.Equal(t, tc.body, contentViewText(t, chunk.Content, view))
				require.Equal(t, chunk.ContentRevision, view.OriginRevision)
			}
			if tc.name == "rewritten" {
				require.Equal(t, true, row["content_rewritten"])
				require.NotContains(t, row, "content_source_view")
			}
		})
	}
	// A hydrated body with a different identity cannot overwrite the retrieved
	// result's revision or manufacture a projection for its displayed text.
	result := &types.SearchResult{ID: chunk.ID, KnowledgeID: chunk.KnowledgeID, KnowledgeBaseID: chunk.KnowledgeBaseID,
		Content: "The observed value is 46.2.", ContentRevision: 8, SourceChunk: chunk}
	output := (&SearchKnowledgeTool{}).formatOutput(context.Background(), []*searchResultWithMeta{{SearchResult: result, KnowledgeBaseID: "library-a"}}, []string{"library-a"}, "observed", SearchModeKeyword)
	row := output.Data["results"].([]map[string]any)[0]
	require.Equal(t, 8, row["content_revision"])
	require.NotContains(t, row, "content_source_view")
}
