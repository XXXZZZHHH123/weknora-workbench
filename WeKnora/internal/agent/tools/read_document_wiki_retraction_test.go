package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type readDocWikiPageLookup struct {
	interfaces.WikiPageService
	page *types.WikiPage
}

func (s readDocWikiPageLookup) GetPageByID(context.Context, string) (*types.WikiPage, error) {
	return s.page, nil
}

func TestReadDocumentRejectsDirectHandleToMarkedWikiChunk(t *testing.T) {
	page := &types.WikiPage{ID: "page-marked", TenantID: 7, KnowledgeBaseID: "kb-1"}
	types.SetWikiRetractionSource(page, "deleted-source", false)
	chunk := &types.Chunk{
		ID: "wp-page-marked", TenantID: 7, KnowledgeBaseID: "kb-1",
		KnowledgeID: "missing-parent-row", ChunkType: types.ChunkTypeWikiPage,
		IsEnabled: true, Content: "PRIVATE_STALE_WIKI_CHUNK_563",
	}
	chunkSvc := &readDocChunkService{repo: &readDocChunkRepo{ordered: []*types.Chunk{chunk}}}
	tool := NewReadDocumentTool(
		&readDocKnowledgeService{docs: map[string]*types.Knowledge{}}, chunkSvc,
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}},
	).WithWikiPageService(readDocWikiPageLookup{page: page})

	_, _, err := tool.resolveTarget(context.Background(), chunk.ID)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "withheld"))
	require.NotContains(t, err.Error(), "PRIVATE_STALE_WIKI_CHUNK_563")
}

func TestReadDocumentFailsClosedWhenWikiChunkPageCannotBeVerified(t *testing.T) {
	chunk := &types.Chunk{
		ID: "wp-page-missing", TenantID: 7, KnowledgeBaseID: "kb-1",
		KnowledgeID: "missing-parent-row", ChunkType: types.ChunkTypeWikiPage,
		IsEnabled: true, Content: "PRIVATE_UNVERIFIED_WIKI_CHUNK_296",
	}
	chunkSvc := &readDocChunkService{repo: &readDocChunkRepo{ordered: []*types.Chunk{chunk}}}
	tool := NewReadDocumentTool(
		&readDocKnowledgeService{docs: map[string]*types.Knowledge{}}, chunkSvc,
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}},
	).WithWikiPageService(readDocWikiPageLookup{page: nil})
	_, _, err := tool.resolveTarget(context.Background(), chunk.ID)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "identity could not be verified"))
}
