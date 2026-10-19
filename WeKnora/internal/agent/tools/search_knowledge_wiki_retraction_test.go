package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type wikiRetractionLookup struct {
	interfaces.WikiPageService
	page *types.WikiPage
}

func (s wikiRetractionLookup) GetPageByID(context.Context, string) (*types.WikiPage, error) {
	return s.page, nil
}

func TestSearchKnowledgeWithholdsRetractedWikiChunkBody(t *testing.T) {
	page := &types.WikiPage{ID: "page-verified", KnowledgeBaseID: "kb-1"}
	types.SetWikiRetractionSource(page, "removed-source", false)
	tool := &SearchKnowledgeTool{wikiPageService: wikiRetractionLookup{page: page}}
	result := &searchResultWithMeta{
		SearchResult:    &types.SearchResult{ID: "wp-page-verified", ChunkType: string(types.ChunkTypeWikiPage), Content: "PRIVATE_RETRACTED_WIKI_FACT_618"},
		KnowledgeBaseID: "kb-1",
	}
	formatted := tool.formatOutput(context.Background(), []*searchResultWithMeta{result}, []string{"kb-1"}, "query", SearchModeHybrid)
	require.True(t, formatted.Success)
	require.NotContains(t, formatted.Output, "PRIVATE_RETRACTED_WIKI_FACT_618")
	require.Contains(t, formatted.Output, "current source documents")
	require.Contains(t, formatted.Data, "withheld_retracted_or_unverified_wiki_chunks")
}

func TestSearchKnowledgeFailsClosedForUnresolvableWikiChunkAndKeepsOrdinaryChunks(t *testing.T) {
	ordinary := &searchResultWithMeta{
		SearchResult:    &types.SearchResult{ID: "document-chunk", ChunkType: string(types.ChunkTypeText), Content: "VISIBLE_DOCUMENT_FACT_842"},
		KnowledgeBaseID: "kb-1",
	}
	unresolvableWiki := &searchResultWithMeta{
		SearchResult:    &types.SearchResult{ID: "legacy-wiki-id", ChunkType: string(types.ChunkTypeWikiPage), Content: "PRIVATE_UNVERIFIED_WIKI_FACT_935"},
		KnowledgeBaseID: "kb-1",
	}
	tool := &SearchKnowledgeTool{}
	formatted := tool.formatOutput(context.Background(), []*searchResultWithMeta{unresolvableWiki, ordinary}, []string{"kb-1"}, "query", SearchModeHybrid)
	require.True(t, formatted.Success)
	require.NotContains(t, formatted.Output, "PRIVATE_UNVERIFIED_WIKI_FACT_935")
	require.Contains(t, formatted.Output, "VISIBLE_DOCUMENT_FACT_842")
	require.Contains(t, formatted.Output, "withheld_retracted_or_unverified_wiki_chunks")
}

func TestSearchKnowledgeKeepsWikiChunkOnlyWhenCurrentPageIsVerifiedAndUnmarked(t *testing.T) {
	page := &types.WikiPage{ID: "page-live", KnowledgeBaseID: "kb-1"}
	tool := &SearchKnowledgeTool{wikiPageService: wikiRetractionLookup{page: page}}
	result := &searchResultWithMeta{
		SearchResult:    &types.SearchResult{ID: "wp-page-live", ChunkType: string(types.ChunkTypeWikiPage), Content: "CURRENTLY_VERIFIED_WIKI_FACT_476"},
		KnowledgeBaseID: "kb-1",
	}
	formatted := tool.formatOutput(context.Background(), []*searchResultWithMeta{result}, []string{"kb-1"}, "query", SearchModeHybrid)
	require.True(t, formatted.Success)
	require.True(t, strings.Contains(formatted.Output, "CURRENTLY_VERIFIED_WIKI_FACT_476"))
}
