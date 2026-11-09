package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func navigationScopeFixture(t *testing.T) (*ReadDocumentTool, *RequestedDocumentConstraint, *readDocChunkRepo) {
	t.Helper()
	tool, repo := newReadDocumentFixture(4)
	repo.ordered[0].Content = "<!-- PDF page 1 -->\nFirst physical page."
	repo.ordered[1].Content = "<!-- PDF page 2 -->\nSecond physical page introduction."
	repo.ordered[2].Content = "Requested continuation evidence."
	repo.ordered[3].Content = "<!-- PDF page 3 -->\nThird physical page."
	state := NewDocumentScopeState("Use only physical pages 1 and 2.")
	tool.WithDocumentConstraint(state)
	_, err := state.bind(context.Background(), DocumentScopePlan{Reason: "Model reads the actual document metadata", Sources: []DocumentSourceSelection{{KnowledgeID: "doc-1", Title: "Engine Manual", Page: 1}}}, tool.knowledgeService, tool.searchTargets)
	require.NoError(t, err)
	return tool, state, repo
}

func TestDocumentReadHintDoesNotOverrideChunkQueryOrLaterPages(t *testing.T) {
	tool, _, _ := navigationScopeFixture(t)
	for _, test := range []struct{ args, text string }{
		{`{"id":"chunk-2"}`, "Requested continuation evidence."},
		{`{"id":"doc-1","page":3}`, "Third physical page."},
		{`{"id":"doc-1","query":"continuation"}`, "Requested continuation evidence."},
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(test.args))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.Contains(t, result.Output, test.text)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"chunk-2","page":2}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []string{"chunk-2"}, chunkIDsFromData(t, result.Data), "a page read must not replace a requested chunk with the first chunk of that page")
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"chunk-2","page":1}`))
	require.NoError(t, err)
	require.False(t, result.Success, "a foreign-page chunk cannot be relabeled as the requested physical page")
}

func TestDocumentUserPageConstraintSurvivesRebindAndRejectsFabricatedQuote(t *testing.T) {
	tool, state, _ := navigationScopeFixture(t)
	selection := DocumentSourceSelection{KnowledgeID: "doc-1", Title: "Engine Manual", UserPages: &DocumentPageConstraint{Pages: []int{1, 2}, UserQuote: "Use only physical pages 1 and 2."}}
	_, err := state.bind(context.Background(), DocumentScopePlan{Reason: "Model interprets the full user page restriction", Sources: []DocumentSourceSelection{selection}}, tool.knowledgeService, tool.searchTargets)
	require.NoError(t, err)
	selection.UserPages.Pages[0] = 3
	selection.UserPages = nil
	selection.Pages = []int{2, 1}
	_, err = state.bind(context.Background(), DocumentScopePlan{Reason: "Replan reading order within the same source", Sources: []DocumentSourceSelection{selection}}, tool.knowledgeService, tool.searchTargets)
	require.NoError(t, err)
	selected, err := state.selection("doc-1")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, selected.UserPages.Pages)
	selected.UserPages.Pages[0] = 3
	selected, err = state.selection("doc-1")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, selected.UserPages.Pages, "state snapshots cannot mutate the frozen restriction")
	for _, test := range []DocumentSourceSelection{
		{KnowledgeID: "doc-1", Title: "Engine Manual", Page: 3},
		{KnowledgeID: "doc-1", Title: "Engine Manual", UserPages: &DocumentPageConstraint{Pages: []int{1, 2, 3}, UserQuote: "Use only physical pages 1 and 2."}},
		{KnowledgeID: "doc-1", Title: "Engine Manual", UserPages: &DocumentPageConstraint{Pages: []int{1, 2}, UserQuote: "fabricated user request"}},
	} {
		_, err = state.bind(context.Background(), DocumentScopePlan{Reason: "invalid rebind", Sources: []DocumentSourceSelection{test}}, tool.knowledgeService, tool.searchTargets)
		require.Error(t, err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":3}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"chunk-3"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	scope := result.Data["document_scope"].(map[string]interface{})
	require.Equal(t, []int{1, 2}, scope["user_page_constraint"].(*DocumentPageConstraint).Pages)
}

func TestDocumentBatchPagesUseOneScanAndKeepPartialCursors(t *testing.T) {
	tool, _, repo := navigationScopeFixture(t)
	repo.requests = nil
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","pages":[1,2],"limit":1}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Len(t, repo.requests, 1, "independent physical-page requests share one ordered document scan")
	require.Equal(t, []string{"chunk-0", "chunk-1"}, chunkIDsFromData(t, result.Data))
	require.Equal(t, false, result.Data["pages_complete"])
	statuses := result.Data["page_results"].([]map[string]interface{})
	require.Equal(t, true, statuses[0]["page_complete"])
	require.Equal(t, false, statuses[1]["page_complete"])
	require.Equal(t, 1, statuses[1]["next_offset"])
	rows := result.Data["chunks"].([]map[string]interface{})
	require.Equal(t, 1, rows[0]["page"])
	require.Equal(t, 2, rows[1]["page"])
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":2,"offset":1}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []string{"chunk-2"}, chunkIDsFromData(t, result.Data))
}

func TestDocumentBatchRejectsInvalidPagesBeforeReading(t *testing.T) {
	for _, args := range []string{
		`{"id":"doc-1","pages":[1,1]}`,
		`{"id":"doc-1","pages":[0,2]}`,
		`{"id":"doc-1","page":1,"pages":[2]}`,
		`{"id":"doc-1","pages":[1,2,3,4,5,6,7,8,9]}`,
	} {
		tool, _, repo := navigationScopeFixture(t)
		repo.requests = nil
		result, err := tool.Execute(context.Background(), json.RawMessage(args))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Empty(t, repo.requests)
	}
}

func TestDocumentScopePlansSeveralPagesInOneSourceEntry(t *testing.T) {
	tool, _, _ := navigationScopeFixture(t)
	plan := DocumentScopePlan{Reason: "Model chooses two actual physical source pages", Sources: []DocumentSourceSelection{{KnowledgeID: "doc-1", Title: "Engine Manual", Pages: []int{1, 3}}}}
	args, err := json.Marshal(ReadDocumentInput{ID: "doc-1", SourceScope: &plan})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []int{1, 3}, result.Data["requested_pages"])
	require.Equal(t, []string{"chunk-0", "chunk-3"}, chunkIDsFromData(t, result.Data))
	require.Equal(t, true, result.Data["pages_complete"])
}

type incompletePhysicalPageRepo struct {
	interfaces.ChunkRepository
	chunks []*types.Chunk
}

func (r incompletePhysicalPageRepo) ListPagedChunksByKnowledgeID(context.Context, uint64, string, *types.Pagination, []types.ChunkType, []string, string, string, string, string, *bool) ([]*types.Chunk, int64, error) {
	return r.chunks, int64(len(r.chunks) + 1), nil
}

type incompletePhysicalPageService struct {
	interfaces.ChunkService
	repo interfaces.ChunkRepository
}

func (s incompletePhysicalPageService) GetRepository() interfaces.ChunkRepository { return s.repo }

func TestDocumentPhysicalPageCannotClaimCompletenessAfterMissingScanRows(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	repo.ordered[0].Content = "<!-- PDF page 1 -->\nVisible partial source."
	tool.chunkService = incompletePhysicalPageService{repo: incompletePhysicalPageRepo{chunks: repo.ordered}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","pages":[1,2]}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "scan is incomplete")
	require.NotContains(t, result.Data, "document_scope")
}

func TestDocumentPhysicalPageRejectsRepositorySourceMismatch(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	repo.ordered[0].Content = "<!-- PDF page 1 -->\nForeign source must not leak."
	repo.ordered[0].KnowledgeID = "other-document"
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.NotContains(t, result.Output, "Foreign source")
	require.NotContains(t, result.Data, "document_scope")
}

func TestDocumentPageQueryReallyFiltersWithinPhysicalPage(t *testing.T) {
	tool, _, repo := navigationScopeFixture(t)
	for index := range repo.ordered {
		repo.ordered[index].Content += "\n" + strings.Repeat("padding ", 10)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":1,"query":"continuation"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, result.Data["match_count"])
	require.Empty(t, chunkIDsFromData(t, result.Data))
	require.Equal(t, false, result.Data["page_complete"])
	require.Equal(t, false, result.Data["query_scope"].(map[string]interface{})["document_scan_complete"])
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":2,"query":"continuation"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, result.Data["match_count"])
	require.Contains(t, result.Output, "Requested continuation evidence.")
	require.NotContains(t, result.Output, "First physical page.")
	require.NotContains(t, result.Output, "Third physical page.")
}

func TestDocumentChunkPaginationIsNotPhysicalPageMetadata(t *testing.T) {
	tool, repo := newReadDocumentFixture(3)
	ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
	repo.ordered[2].Content = "![Source original](" + ref + ")"
	metadata, _ := json.Marshal(map[string]interface{}{types.DocumentImageSourcesKey: []types.DocumentImageSource{{Reference: ref, Page: 13, SourceHash: "file-hash"}}})
	repo.ordered[2].Metadata = types.JSON(metadata)
	tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].FileHash = "file-hash"
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","offset":2,"limit":1}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotContains(t, result.Data, "page")
	require.Equal(t, 3, result.Data["chunk_page"])
	require.Equal(t, 13, result.Data["image_catalog"].([]documentImage)[0].Page)
}
