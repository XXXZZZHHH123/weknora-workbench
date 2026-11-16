package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBuildSearchResultChunkIdentity(t *testing.T) {
	chunk := &types.Chunk{ID: "chunk-a", KnowledgeID: "doc-a", KnowledgeBaseID: "library-a", Content: "The actual original body.", ContentRevision: 6,
		ChunkIndex: 3, ChunkType: types.ChunkTypeText, StartAt: 18, EndAt: 43, ParentChunkID: "parent-a", SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 2}}}
	document := &types.Knowledge{ID: chunk.KnowledgeID, KnowledgeBaseID: chunk.KnowledgeBaseID, Title: "Source notes"}
	result := (&knowledgeBaseService{}).buildSearchResult(chunk, document, 0.9, types.MatchTypeKeywords, "matched fragment")
	require.Same(t, chunk, result.SourceChunk, "the batched authorized repository row must survive retrieval hydration")
	require.Equal(t, chunk.ContentRevision, result.ContentRevision)
	require.Equal(t, chunk.SourceLocators, result.SourceLocators)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SourceChunk", "the execution witness never enters saved/public references")
	require.NotContains(t, string(encoded), "source_chunk")
}
