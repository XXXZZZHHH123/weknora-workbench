package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// limitingWikiService returns at most limit pages, like the real store.
type limitingWikiService struct {
	*fakeWikiPageService
	limits []int
}

func (s *limitingWikiService) SearchPages(
	ctx context.Context, kbID, query string, limit int,
) ([]*types.WikiPage, error) {
	s.limits = append(s.limits, limit)
	pages, err := s.fakeWikiPageService.SearchPages(ctx, kbID, query, limit)
	if len(pages) > limit {
		pages = pages[:limit]
	}
	return pages, err
}

// Under a document scope, out-of-scope pages ranked first used to fill the
// limit and hide the in-scope page ranked just below them.
func TestWikiSearchScopedOverfetchesBeforeFiltering(t *testing.T) {
	var pages []*types.WikiPage
	for i := 0; i < 3; i++ {
		pages = append(pages, &types.WikiPage{
			KnowledgeBaseID: "kb-1", Slug: fmt.Sprintf("other-%d", i), Title: "Other", Content: "engine",
			PageType: "entity", SourceRefs: types.StringArray{"doc-other|Other"},
		})
	}
	pages = append(pages, &types.WikiPage{
		KnowledgeBaseID: "kb-1", Slug: "wanted", Title: "Wanted", Content: "engine",
		PageType: "entity", SourceRefs: types.StringArray{"doc-wanted|Wanted"},
	})
	service := &limitingWikiService{fakeWikiPageService: &fakeWikiPageService{
		searchResults: map[string][]*types.WikiPage{"kb-1": pages},
	}}
	tool := NewWikiSearchTool(service, nil,
		[]WikiScope{{KnowledgeBaseID: "kb-1", KnowledgeIDs: []string{"doc-wanted"}}}, NewWikiRouteResolver())

	args, _ := json.Marshal(map[string]any{"query": "engine", "limit": 2})
	res, err := tool.Execute(context.Background(), args)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Execute: res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, "wanted") {
		t.Fatalf("in-scope page below the limit was not found: %s", res.Output)
	}
	if len(service.limits) == 0 || service.limits[0] <= 2 {
		t.Fatalf("scoped search did not over-fetch: limits=%v", service.limits)
	}
}

func TestWikiSearchStructuredResultsMatchRenderedHitsAndExcludeNeighbours(t *testing.T) {
	hit := newTestWikiPage("kb-1", "concept/hit")
	hit.Title = "Rendered title"
	hit.PageType = "concept"
	hit.Summary = "Summary actually rendered for this hit."
	hit.Content = "A matching term appears in the retrieved body."
	hit.OutLinks = []string{"concept/neighbor"}
	service := &fakeWikiPageService{searchResults: map[string][]*types.WikiPage{"kb-1": {hit}}}
	tool := NewWikiSearchTool(service, nil, NewWikiScopesFromKBIDs([]string{"kb-1"}), NewWikiRouteResolver())
	args, err := json.Marshal(map[string]any{"query": "matching"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: result=%+v err=%v", result, err)
	}
	rows, ok := result.Data["actual_search_results"].([]wikiSearchResultEvidence)
	if !ok || len(rows) != 1 {
		t.Fatalf("actual_search_results = %#v, want one rendered search hit", result.Data["actual_search_results"])
	}
	row := rows[0]
	if row.KBID != "kb-1" || row.Slug != hit.Slug || row.Title != hit.Title || row.PageType != hit.PageType {
		t.Fatalf("search result identity = %+v", row)
	}
	if row.Summary != hit.Summary || row.SummaryStatus != "shown" || row.MatchSnippet == "" {
		t.Fatalf("search result visible text = %+v", row)
	}
	if !strings.Contains(result.Output, "<summary>"+row.Summary+"</summary>") ||
		!strings.Contains(result.Output, "<match_snippet>"+row.MatchSnippet+"</match_snippet>") {
		t.Fatalf("structured text does not match rendered output: row=%+v output=%s", row, result.Output)
	}
	foundKBs := result.Data["found_kbs"].(map[string][]string)
	if len(foundKBs["concept/neighbor"]) == 0 {
		t.Fatalf("test setup failed: linked neighbour missing from routing metadata: %v", foundKBs)
	}
	if strings.Contains(fmt.Sprint(rows), "concept/neighbor") {
		t.Fatalf("linked neighbour was misreported as a search hit: %+v", rows)
	}
	if !strings.Contains(result.Data["found_kbs_scope"].(string), "linked neighbours") {
		t.Fatalf("found_kbs scope note is missing: %v", result.Data["found_kbs_scope"])
	}
	second, err := tool.Execute(context.Background(), args)
	if err != nil || second == nil || !second.Success {
		t.Fatalf("second Execute: result=%+v err=%v", second, err)
	}
	secondRows := second.Data["actual_search_results"].([]wikiSearchResultEvidence)
	if len(secondRows) != 1 || secondRows[0].SummaryStatus != "already_seen_omitted" ||
		secondRows[0].Summary != "(summary omitted, already seen in previous search)" {
		t.Fatalf("already-seen summary projection = %+v", secondRows)
	}
	if strings.Contains(fmt.Sprint(secondRows), hit.Summary) {
		t.Fatalf("previously seen hidden summary leaked into structured data: %+v", secondRows)
	}
}

func TestWikiSearchStructuredResultsDoNotExposeWithdrawnSummaryOrSnippet(t *testing.T) {
	page := newTestWikiPage("kb-1", "concept/withdrawn")
	page.Title = "Withdrawn title"
	page.Summary = "PRIVATE_WITHDRAWN_SUMMARY"
	page.Content = "PRIVATE_WITHDRAWN_MATCHING_BODY"
	types.SetWikiRetractionSource(page, "source-retracted", false)
	service := &fakeWikiPageService{searchResults: map[string][]*types.WikiPage{"kb-1": {page}}}
	tool := NewWikiSearchTool(service, nil, NewWikiScopesFromKBIDs([]string{"kb-1"}), NewWikiRouteResolver())
	args, err := json.Marshal(map[string]any{"query": "matching"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: result=%+v err=%v", result, err)
	}
	rows := result.Data["actual_search_results"].([]wikiSearchResultEvidence)
	if len(rows) != 1 {
		t.Fatalf("actual_search_results = %+v, want one hit", rows)
	}
	row := rows[0]
	if row.EvidenceState != "retraction_pending" || row.Summary != "" || row.MatchSnippet != "" || row.SummaryStatus != "withdrawn" {
		t.Fatalf("withdrawn result status leaked hidden fields or lost state: %+v", row)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"PRIVATE_WITHDRAWN_SUMMARY", "PRIVATE_WITHDRAWN_MATCHING_BODY"} {
		if strings.Contains(string(encoded), hidden) || strings.Contains(result.Output, hidden) {
			t.Fatalf("withdrawn page text leaked: data=%s output=%s", encoded, result.Output)
		}
	}
	if row.EvidenceNotice == "" || !strings.Contains(result.Output, row.EvidenceNotice) {
		t.Fatalf("withdrawal notice is not consistent with rendered output: row=%+v output=%s", row, result.Output)
	}
}
