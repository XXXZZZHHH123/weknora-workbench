package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWikiReadPageWithholdsPendingAndFailedRetractionContent(t *testing.T) {
	for _, failed := range []bool{false, true} {
		state := "pending"
		if failed {
			state = "failed"
		}
		t.Run(state, func(t *testing.T) {
			page := newTestWikiPage("kb-retraction", "concept/shared")
			page.Summary = "PRIVATE_RETRACT_SUMMARY_784"
			page.Content = "PRIVATE_RETRACT_BODY_784"
			types.SetWikiRetractionSource(page, "deleted-source", failed)
			service := &fakeWikiPageService{pages: map[string]*types.WikiPage{
				wikiPageKey("kb-retraction", page.Slug): page,
			}}
			tool := NewWikiReadPageTool(service, nil, NewWikiScopesFromKBIDs([]string{"kb-retraction"}), nil)
			result, err := tool.Execute(context.Background(), json.RawMessage(`{"slugs":["concept/shared"]}`))
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Contains(t, result.Output, "retraction_"+state)
			require.Contains(t, strings.ToLower(result.Output), "consult the current source documents")
			require.NotContains(t, result.Output, "PRIVATE_RETRACT_SUMMARY_784")
			require.NotContains(t, result.Output, "PRIVATE_RETRACT_BODY_784")
		})
	}
}

func TestWikiSearchAndNeighbourSummariesWithholdRetractionMarkedFacts(t *testing.T) {
	flagged := newTestWikiPage("kb-retraction", "concept/flagged")
	flagged.Summary = "PRIVATE_NEIGHBOR_SUMMARY_319"
	flagged.Content = "PRIVATE_MATCH_BODY_319"
	types.SetWikiRetractionSource(flagged, "deleted-source", false)
	parent := newTestWikiPage("kb-retraction", "concept/parent")
	parent.OutLinks = types.StringArray{flagged.Slug}
	service := &fakeWikiPageService{
		pages: map[string]*types.WikiPage{
			wikiPageKey("kb-retraction", flagged.Slug): flagged,
			wikiPageKey("kb-retraction", parent.Slug):  parent,
		},
		searchResults: map[string][]*types.WikiPage{"kb-retraction": {flagged}},
	}
	scopes := NewWikiScopesFromKBIDs([]string{"kb-retraction"})

	readTool := NewWikiReadPageTool(service, nil, scopes, nil)
	read, err := readTool.Execute(context.Background(), json.RawMessage(`{"slugs":["concept/parent"]}`))
	require.NoError(t, err)
	require.True(t, read.Success)
	require.Contains(t, read.Output, "source retraction retraction_pending")
	require.NotContains(t, read.Output, "PRIVATE_NEIGHBOR_SUMMARY_319")

	searchTool := NewWikiSearchTool(service, nil, scopes, nil)
	search, err := searchTool.Execute(context.Background(), json.RawMessage(`{"query":"matched phrase"}`))
	require.NoError(t, err)
	require.True(t, search.Success)
	require.Contains(t, search.Output, "retraction_pending")
	require.Contains(t, strings.ToLower(search.Output), "consult the current source documents")
	require.NotContains(t, search.Output, "PRIVATE_NEIGHBOR_SUMMARY_319")
	require.NotContains(t, search.Output, "PRIVATE_MATCH_BODY_319")
}

func TestWikiIndexOverviewDoesNotRenderMarkedPageSummary(t *testing.T) {
	output := renderIndexOverviewForAgent(&types.WikiIndexResponse{
		Groups: []types.WikiIndexGroup{{
			Type: types.WikiPageTypeConcept, Total: 1,
			Items: []types.WikiIndexEntry{{Slug: "concept/shared", Title: "Shared", Summary: "PRIVATE_INDEX_SUMMARY_261", EvidenceState: "retraction_failed"}},
		}},
	})
	require.Contains(t, output, "retraction_failed")
	require.Contains(t, output, "consult current source documents")
	require.NotContains(t, output, "PRIVATE_INDEX_SUMMARY_261")
	require.True(t, strings.Contains(output, "[[concept/shared|Shared]]"))
}
