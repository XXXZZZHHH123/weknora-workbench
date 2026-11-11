package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type memoryWebPages struct {
	mu    sync.Mutex
	pages map[string]string
	fail  bool
}

func (s *memoryWebPages) Save(_ context.Context, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", fmt.Errorf("storage unavailable")
	}
	path := fmt.Sprintf("web://page-%d", len(s.pages))
	s.pages[path] = content
	return path, nil
}

func (s *memoryWebPages) Read(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.pages[path]
	if !ok {
		return nil, fmt.Errorf("unavailable page")
	}
	return []byte(content), nil
}

func TestFetchSavedPageSurvivesCacheEvictionAndModelEncoding(t *testing.T) {
	store := &memoryWebPages{pages: map[string]string{}}
	fetcher := newStubWebContentFetcher(map[string]string{}, nil)
	fetch := newWebFetchTool(fetcher).WithPageSource(store)
	var saved string
	for i := 0; i < 9; i++ {
		url := fmt.Sprintf("https://example.com/%d", i)
		fetcher.contents[url] = "first line\nsecond line\nthird line"
		result, err := fetch.Execute(t.Context(), webFetchArgs(WebFetchItem{URL: url, Limit: 3}))
		require.NoError(t, err)
		require.True(t, result.Success)
		row := result.Data["results"].([]map[string]interface{})[0]
		if i == 0 {
			saved = row["full_output_path"].(string)
			encoded := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolWebFetch, result)
			require.Contains(t, encoded, saved)
			require.Contains(t, encoded, `tool="read_file"`)
		}
	}
	require.Len(t, fetch.pages, 8)
	reader := NewReadFileTool(nil).WithWebPages(store)
	result, err := reader.Execute(t.Context(), json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":2,"limit":1}`, saved)))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Output, "second line")
	require.NotContains(t, result.Output, "first line")
	require.Equal(t, 3, result.Data["next_offset"])
	store.fail = true
	fetcher.contents["https://example.com/fail-save"] = "fetched despite storage failure"
	failedSave, err := fetch.Execute(t.Context(), webFetchArgs(WebFetchItem{URL: "https://example.com/fail-save"}))
	require.NoError(t, err)
	require.True(t, failedSave.Success)
	require.Contains(t, failedSave.Output, "could not be saved")
}

func TestSavedWebPageReadsOversizedUnicodeLineWithoutShell(t *testing.T) {
	line := strings.Repeat("正文", 18000)
	store := &memoryWebPages{pages: map[string]string{"web://long": line + "\nlast line"}}
	registry := NewToolRegistry()
	registry.SetMaxToolOutputSize(4096)
	registry.RegisterTool(NewReadFileTool(nil).WithWebPages(store))
	offset, within := 1, 0
	var restored strings.Builder
	for attempts := 0; attempts < 30; attempts++ {
		args, _ := json.Marshal(map[string]interface{}{"path": "web://long", "offset": offset, "line_offset": within})
		result, err := registry.ExecuteTool(t.Context(), ToolReadFile, args)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.True(t, utf8.ValidString(result.Output))
		require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 4096)
		_, part, ok := strings.Cut(result.Output, "```\n")
		require.True(t, ok)
		part, _, ok = strings.Cut(part, "\n```")
		require.True(t, ok)
		restored.WriteString(part)
		if result.Data["next_offset"] == 2 {
			break
		}
		offset = result.Data["next_offset"].(int)
		next := result.Data["next_line_offset"].(int)
		require.Greater(t, next, within)
		within = next
	}
	require.Equal(t, line, restored.String())
}

func TestSearchOptionsAndExplicitPageContent(t *testing.T) {
	svc := &searchOnlyWebService{results: []*types.WebSearchResult{
		{URL: "https://example.com/a", Snippet: "search snippet", Age: "yesterday"},
		{URL: "https://example.com/b", Snippet: "still useful"},
	}}
	fetcher := newStubWebContentFetcher(map[string]string{"https://example.com/a": "verified page"},
		map[string]error{"https://example.com/b": fmt.Errorf("page failed")})
	pages := newWebFetchTool(fetcher).WithPageSource(&memoryWebPages{pages: map[string]string{}})
	search := NewWebSearchTool(svc, 5, "provider").WithPageReader(pages)
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	_, err := search.Execute(ctx, []byte(`{"query":"query","count":2}`))
	require.NoError(t, err)
	require.Empty(t, fetcher.callCount, "page fetching must be opt-in")
	result, err := search.Execute(ctx,
		[]byte(`{"query":"query","count":2,"country":"de","freshness":"pw","content":true}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 2, svc.config.MaxResults)
	require.Equal(t, "DE", svc.config.Filters.Country)
	require.Equal(t, "pw", svc.config.Filters.Freshness)
	rows := result.Data["results"].([]map[string]interface{})
	require.Equal(t, true, rows[0]["page_verified"])
	require.Equal(t, "verified page", rows[0]["page_content"])
	require.Equal(t, "yesterday", rows[0]["age"])
	require.Equal(t, "failed", rows[1]["page_status"])
	require.Equal(t, false, rows[1]["page_verified"])
	require.Equal(t, "still useful", rows[1]["snippet"])
	model := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolWebSearch, result)
	require.Contains(t, model, "verified page")
	require.Contains(t, model, `status="success" verified="true"`)
	require.Contains(t, model, rows[0]["full_output_path"])
	require.Contains(t, model, "still useful")
	// Only values with no nearest legal alternative stay failures: an unsupported
	// filter value has nothing to fall back to. An over-maximum count is clamped
	// instead (see TestAgentWebSearchClampsCountToConfiguredMaximum).
	for _, args := range []string{
		`{"query":"q","count":0}`,
		`{"query":"q","country":"bad"}`, `{"query":"q","freshness":"tomorrow"}`,
	} {
		calls := svc.calls
		result, err := search.Execute(ctx, []byte(args))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, calls, svc.calls)
	}
	failedStorage := newWebFetchTool(fetcher).WithPageSource(&memoryWebPages{fail: true})
	search.WithPageReader(failedStorage)
	result, err = search.Execute(ctx, []byte(`{"query":"q","count":1,"content":true}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	model = modelcontext.NewRegistry(true).ModelToolResultForTool(ToolWebSearch, result)
	require.Contains(t, model, "verified page")
	require.Contains(t, model, "could not be saved")
}

func TestSearchAndFetchKeepPublicationTimeAndFactEvidenceSeparate(t *testing.T) {
	published := time.Date(2031, 6, 16, 9, 0, 0, 0, time.UTC)
	const url = "https://example.com/dated-bulletin"
	const summary = "2031-06-18: 17 units."
	const page = "2031-06-19: 23 units."
	for _, withContent := range []bool{false, true} {
		t.Run(fmt.Sprintf("content=%t", withContent), func(t *testing.T) {
			svc := &searchOnlyWebService{results: []*types.WebSearchResult{{
				URL: url, Title: "Dated bulletin", Snippet: summary, Age: "2 days ago", PublishedAt: &published,
			}}}
			fetcher := newStubWebContentFetcher(map[string]string{url: page}, nil)
			pages := newWebFetchTool(fetcher)
			search := NewWebSearchTool(svc, 1, "provider").WithPageReader(pages)
			ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
			result, err := search.Execute(ctx, []byte(fmt.Sprintf(`{"query":"dated bulletin","content":%t}`, withContent)))
			require.NoError(t, err)
			require.True(t, result.Success)
			before, err := json.Marshal(result.Data)
			require.NoError(t, err)
			registry := modelcontext.NewRegistry(true)
			model := registry.ModelToolResultForTool(ToolWebSearch, result)
			require.Contains(t, model, `<evidence type="search_summary" verified="false" />`)
			require.Contains(t, model, "<match>"+summary+"</match>")
			require.Contains(t, model, `<published source="search_provider" meaning="reported_publication_time">2031-06-16T09:00:00Z</published>`)
			require.Contains(t, model, `<age source="search_provider">2 days ago</age>`)
			require.Contains(t, model, "A successful fetch verifies retrieval, not summary claims absent from the excerpt")
			if withContent {
				require.Contains(t, model, "<fetched_content>"+page+"</fetched_content>")
				require.Contains(t, model, `scope="retrieved_excerpt_only"`)
			} else {
				require.NotContains(t, model, "<fetched_content>")
				require.Empty(t, fetcher.callCount, "evidence guidance must not add an automatic fetch")
			}
			after, err := json.Marshal(result.Data)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "rendering must preserve actual source metadata")

			fetched, err := pages.Execute(ctx, webFetchArgs(WebFetchItem{URL: url}))
			require.NoError(t, err)
			require.True(t, fetched.Success)
			model = registry.ModelToolResultForTool(ToolWebFetch, fetched)
			require.Contains(t, model, `<page id="url://1" status="success" view="excerpt">`)
			require.Contains(t, model, "<content>"+page+"</content>")
			require.NotContains(t, model, summary, "a fetch cannot promote a missing summary value into the page")
			require.NotContains(t, model, "<published", "retrieval must not invent publication/update time")
			require.Contains(t, registry.DecodeOutputText(`<ref id="url://1"/>`), `url="`+url+`"`)
			require.Equal(t, 1, svc.calls)
			require.Equal(t, 1, fetcher.callCount[url], "an explicit fetch may reuse the current snapshot")
		})
	}
}

func TestWebSourceBlockContinuationReadsSameSnapshotAtRawUnicodeOffset(t *testing.T) {
	const rawURL = "https://example.com/period-records"
	page := "# 原始记录🌱\n\n- ## Alpha\n\n  " + strings.Repeat("记录🌱\n", 2400)
	fetcher := newStubWebContentFetcher(map[string]string{rawURL: page}, nil)
	pages := newWebFetchTool(fetcher)
	registry := modelcontext.NewRegistry(true)
	result, err := pages.Execute(t.Context(), webFetchArgs(WebFetchItem{URL: rawURL}))
	require.NoError(t, err)
	require.True(t, result.Success)
	model := registry.ModelToolResultForTool(ToolWebFetch, result)
	var decoded struct {
		Page struct {
			Content struct {
				Blocks []struct {
					Text string `xml:",chardata"`
				} `xml:"source_blocks>block"`
			} `xml:"content"`
			Continue struct {
				URL    string `xml:"url,attr"`
				Offset int    `xml:"next_offset,attr"`
			} `xml:"continue"`
		} `xml:"page"`
	}
	require.NoError(t, xml.Unmarshal([]byte(model), &decoded))
	var shown strings.Builder
	for _, block := range decoded.Page.Content.Blocks {
		shown.WriteString(block.Text)
	}
	require.NotEmpty(t, shown.String())
	require.Equal(t, utf8.RuneCountInString(shown.String()), decoded.Page.Continue.Offset)
	require.Equal(t, string([]rune(page)[:decoded.Page.Continue.Offset]), shown.String())
	args, err := json.Marshal(map[string]interface{}{"items": []WebFetchItem{{
		URL: decoded.Page.Continue.URL, Offset: decoded.Page.Continue.Offset,
	}}})
	require.NoError(t, err)
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: ToolWebFetch, Arguments: string(args)}}}
	registry.DecodeToolCalls(calls)
	next, err := pages.Execute(t.Context(), json.RawMessage(calls[0].Function.Arguments))
	require.NoError(t, err)
	require.True(t, next.Success)
	row := next.Data["results"].([]map[string]interface{})[0]
	require.Equal(t, string([]rune(page)[decoded.Page.Continue.Offset:]), row["raw_content"])
	require.Equal(t, 1, fetcher.callCount[rawURL], "projection and continuation must not refetch the page")
	require.Contains(t, registry.DecodeOutputText(`<ref id="url://1"/>`), `url="`+rawURL+`"`)
}
