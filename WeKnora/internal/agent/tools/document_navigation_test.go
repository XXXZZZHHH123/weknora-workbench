package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentCandidatesKeepRankedNavigationWithoutBindingOrFullBody(t *testing.T) {
	read, _ := reportScopeFixture(t, "weekly-report.pptx")
	state := NewDocumentScopeState()
	search := NewSearchKnowledgeTool(nil, read.knowledgeService, nil, read.searchTargets, nil, nil).WithDocumentConstraint(state)
	service := read.knowledgeService.(*readDocKnowledgeService)
	service.docs["report-a"].FileHash = "current-source"
	results := []*searchResultWithMeta{
		{SearchResult: &types.SearchResult{ID: "not-authorized-as-evidence", KnowledgeID: "report-a", ContextHeader: "Annual comparisons", Content: "Seasonal equipment utilization, historical series\n" + strings.Repeat("图文", 500) + "FULL_BODY_TAIL", SourceLocators: types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: 7, Mapping: "exact", SourceHash: "current-source"}}}},
		{SearchResult: &types.SearchResult{ID: "second", KnowledgeID: "report-a", Content: "Second ranked match"}},
		{SearchResult: &types.SearchResult{ID: "third", KnowledgeID: "report-a", Content: "Third match must be omitted"}},
	}
	res := search.documentCandidates(context.Background(), results, "historical seasonal behavior")
	require.True(t, res.Success)
	docs := res.Data["documents"].([]map[string]any)
	require.Len(t, docs, 1)
	matches := docs[0]["navigation_matches"].([]map[string]any)
	require.Len(t, matches, 2)
	require.Equal(t, 7, matches[0]["slide_hint"])
	require.Equal(t, "Annual comparisons", matches[0]["heading"])
	require.Equal(t, true, matches[0]["navigation_only"])
	require.Contains(t, matches[0]["excerpt"], "Seasonal equipment utilization")
	require.LessOrEqual(t, utf8.RuneCountInString(matches[0]["excerpt"].(string))+utf8.RuneCountInString(matches[0]["heading"].(string)), 500)
	require.NotContains(t, res.Output, "FULL_BODY_TAIL")
	require.NotContains(t, res.Output, "Third match must be omitted")
	require.NotContains(t, res.Output, "not-authorized-as-evidence")
	require.NotContains(t, res.Data, "chunks")
	require.NotContains(t, res.Data, "results")
	require.Empty(t, state.selectedIDs(), "previews do not silently choose or bind a source")
}

func TestDocumentCandidatesNavigationRejectsUnauthorizedRetiredAndStaleMatches(t *testing.T) {
	read, _ := reportScopeFixture(t, "current-report.pptx")
	service := read.knowledgeService.(*readDocKnowledgeService)
	service.docs["report-a"].FileHash = "new-version"
	service.docs["outside"] = &types.Knowledge{ID: "outside", KnowledgeBaseID: "another-kb", FileName: "outside.pptx"}
	service.docs["retired"] = &types.Knowledge{ID: "retired", KnowledgeBaseID: "kb-1", FileName: "retired.pptx", ParseStatus: "archived"}
	search := NewSearchKnowledgeTool(nil, read.knowledgeService, nil, read.searchTargets, nil, nil)
	res := search.documentCandidates(context.Background(), []*searchResultWithMeta{
		{SearchResult: &types.SearchResult{KnowledgeID: "outside", Content: "OUTSIDE_BODY"}},
		{SearchResult: &types.SearchResult{KnowledgeID: "retired", Content: "RETIRED_BODY"}},
		{SearchResult: &types.SearchResult{KnowledgeID: "report-a", Content: "STALE_BODY", SourceLocators: types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: 2, Mapping: "exact", SourceHash: "old-version"}}}},
	}, "query")
	docs := res.Data["documents"].([]map[string]any)
	require.Len(t, docs, 1)
	require.Equal(t, "report-a", docs[0]["knowledge_id"])
	require.NotContains(t, docs[0], "navigation_matches")
	for _, forbidden := range []string{"OUTSIDE_BODY", "RETIRED_BODY", "STALE_BODY", "outside.pptx", "retired.pptx"} {
		require.NotContains(t, res.Output, forbidden)
	}
}

func TestDocumentCandidatesNavigationHasOneGlobalEncodedBudget(t *testing.T) {
	read, _ := reportScopeFixture(t, "source.pptx")
	service := read.knowledgeService.(*readDocKnowledgeService)
	var results []*searchResultWithMeta
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("report-%d", i)
		service.docs[id] = &types.Knowledge{ID: id, KnowledgeBaseID: "kb-1", FileName: id + ".pptx"}
		results = append(results, &searchResultWithMeta{SearchResult: &types.SearchResult{KnowledgeID: id, ContextHeader: strings.Repeat("页", 100), Content: strings.Repeat("<>&图", 500)}})
	}
	search := NewSearchKnowledgeTool(nil, read.knowledgeService, nil, read.searchTargets, nil, nil)
	res := search.documentCandidates(context.Background(), results, "query")
	total, count := 0, 0
	for _, doc := range res.Data["documents"].([]map[string]any) {
		if matches, ok := doc["navigation_matches"]; ok {
			encoded, err := json.Marshal(map[string]any{"navigation_matches": matches})
			require.NoError(t, err)
			total += utf8.RuneCount(encoded)
			count++
		}
	}
	require.Positive(t, count)
	require.Less(t, count, 30)
	require.LessOrEqual(t, total, 6000)
}
