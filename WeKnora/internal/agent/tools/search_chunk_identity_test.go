package tools

import (
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSearchChunkIdentityOnlyComesFromActualRowAndProvesExcerpt(t *testing.T) {
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
			result := &types.SearchResult{ID: chunk.ID, KnowledgeID: chunk.KnowledgeID, KnowledgeBaseID: chunk.KnowledgeBaseID, Content: tc.body, SourceChunk: chunk}
			row := map[string]any{}
			appendSearchChunkIdentity(row, result)
			require.Equal(t, 7, row["content_revision"])
			require.Equal(t, 12, row["chunk_index"])
			require.Equal(t, chunk.SourceLocators, row["source_locators"])
			require.Equal(t, tc.complete, row["content_complete"])
			require.Equal(t, chunk.StartAt, row["start_at"])
			require.Equal(t, chunk.EndAt, row["end_at"])
			require.Equal(t, chunk.ParentChunkID, row["parent_chunk_id"])
			if tc.name == "excerpt" {
				view := row["content_source_view"].(*types.ChunkContentView)
				require.Equal(t, tc.body, contentViewText(t, chunk.Content, view))
				require.Equal(t, chunk.ContentRevision, view.OriginRevision)
				require.Equal(t, "chunk_unicode_runes", row["content_offset_space"])
			}
			if tc.name == "rewritten" {
				require.Equal(t, true, row["content_rewritten"])
				require.NotContains(t, row, "content_source_view")
			}
		})
	}
	for _, result := range []*types.SearchResult{{ID: chunk.ID, ContentRevision: 0}, {ID: "wrong-id", SourceChunk: chunk}} {
		row := map[string]any{}
		appendSearchChunkIdentity(row, result)
		require.NotContains(t, row, "content_revision")
		require.NotContains(t, row, "content_complete")
	}
}
