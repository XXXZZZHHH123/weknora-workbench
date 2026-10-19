package chatpipeline

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type wikiRetractionPageLookup struct {
	interfaces.WikiPageService
	pages map[string]*types.WikiPage
}

func (s wikiRetractionPageLookup) GetPageByID(_ context.Context, id string) (*types.WikiPage, error) {
	page, ok := s.pages[id]
	if !ok {
		return nil, errWikiPageNotFoundForTest
	}
	return page, nil
}

var errWikiPageNotFoundForTest = &wikiPageLookupTestError{}

type wikiPageLookupTestError struct{}

func (*wikiPageLookupTestError) Error() string { return "wiki page missing" }

func TestWikiBoostWithholdsMarkedChunksFromLegacyChatRetrieval(t *testing.T) {
	marked := &types.WikiPage{ID: "page-pending", KnowledgeBaseID: "kb-1"}
	types.SetWikiRetractionSource(marked, "deleted-source", false)
	plugin := &PluginWikiBoost{wikiService: wikiRetractionPageLookup{pages: map[string]*types.WikiPage{
		marked.ID: marked,
	}}}
	chatManage := &types.ChatManage{PipelineState: types.PipelineState{
		SearchResult: []*types.SearchResult{
			{ID: "wp-page-pending", KnowledgeBaseID: "kb-1", ChunkType: types.ChunkTypeWikiPage, Content: "PRIVATE_STALE_WIKI_CHAT_283"},
			{ID: "document-chunk", KnowledgeBaseID: "kb-1", ChunkType: types.ChunkTypeText, Content: "LIVE_DOCUMENT_CHAT_491"},
		},
		RerankResult: []*types.SearchResult{
			{ID: "wp-page-pending", KnowledgeBaseID: "kb-1", ChunkType: types.ChunkTypeWikiPage, Content: "PRIVATE_STALE_WIKI_CHAT_283"},
		},
	}}
	pluginErr := plugin.OnEvent(context.Background(), types.CHUNK_RERANK, chatManage, func() *PluginError { return nil })
	require.Nil(t, pluginErr)
	require.Len(t, chatManage.SearchResult, 1)
	require.Equal(t, "document-chunk", chatManage.SearchResult[0].ID)
	require.Empty(t, chatManage.RerankResult)
	require.NotContains(t, chatManage.SearchResult[0].Content, "PRIVATE_STALE_WIKI_CHAT_283")
}
