package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type documentRecallIndex struct {
	interfaces.KnowledgeBaseService
	results     []*types.SearchResult
	err         error
	panicOnLoad bool
	keywordOnly bool
	requests    []types.SearchParams
}

func (s *documentRecallIndex) GetKnowledgeBasesByIDsOnly(context.Context, []string) ([]*types.KnowledgeBase, error) {
	if s.panicOnLoad {
		panic("adapter unavailable")
	}
	return []*types.KnowledgeBase{kbWithIndexes("kb-1", !s.keywordOnly, s.keywordOnly, "")}, nil
}
func (s *documentRecallIndex) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return nil
}
func (s *documentRecallIndex) HybridSearch(_ context.Context, _ string, params types.SearchParams) ([]*types.SearchResult, error) {
	s.requests = append(s.requests, params)
	return s.results, s.err
}
func recallSearchResult(c *types.Chunk) *types.SearchResult {
	return &types.SearchResult{ID: c.ID, KnowledgeID: c.KnowledgeID, KnowledgeBaseID: c.KnowledgeBaseID,
		Content: c.Content, ChunkIndex: c.ChunkIndex, ChunkType: string(c.ChunkType), ContentRevision: c.ContentRevision,
		SourceChunk: c, Score: 0.9, SourceLocators: c.SourceLocators, StartAt: c.StartAt, EndAt: c.EndAt, ParentChunkID: c.ParentChunkID}
}
func recallFixture(t *testing.T) (*ReadDocumentTool, *readDocChunkRepo, *documentRecallIndex) {
	t.Helper()
	read, repo := newReadDocumentFixture(8)
	for i, c := range repo.ordered {
		c.Content = fmt.Sprintf("Alternate section wording %d. The assessment is improving.", i)
		c.ContentRevision = 9
		c.StartAt = i * 100
		c.EndAt = c.StartAt + len(c.Content)
		c.ParentChunkID = "parent-section"
		c.SourceLocators = types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: i + 1}}
	}
	index := &documentRecallIndex{}
	for _, c := range repo.ordered {
		index.results = append(index.results, recallSearchResult(c))
	}
	constraint := NewDocumentScopeState()
	_, err := constraint.bind(t.Context(), DocumentScopePlan{Reason: "model selects actual source", Sources: []DocumentSourceSelection{{KnowledgeID: "doc-1", FileName: "manual.pdf"}}}, read.knowledgeService, read.searchTargets)
	require.NoError(t, err)
	read.WithDocumentConstraint(constraint).WithDocumentSearch(NewSearchKnowledgeTool(index, read.knowledgeService, read.chunkService, read.searchTargets, nil, nil))
	return read, repo, index
}
func TestReadDocumentZeroLiteralMatchesRecallsOriginalIdentityWithinDocument(t *testing.T) {
	read, repo, index := recallFixture(t)
	result, err := read.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","query":"requested topic"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, 0, result.Data["match_count"], "semantic neighbours must not become literal hits")
	scope := result.Data["query_scope"].(map[string]any)
	require.Equal(t, true, scope["document_scan_complete"])
	require.Equal(t, "semantic_within_document", scope["fallback"])
	require.Equal(t, "recalled", scope["fallback_status"])
	require.Contains(t, scope["fallback_notice"], "not literal matches or proof")
	require.Len(t, index.requests, 1)
	require.Equal(t, "requested topic", index.requests[0].QueryText)
	require.Equal(t, []string{"doc-1"}, index.requests[0].KnowledgeIDs)
	require.True(t, index.requests[0].DisableKeywordsMatch)
	require.False(t, index.requests[0].DisableVectorMatch)
	rows := result.Data["chunks"].([]map[string]any)
	require.Len(t, rows, readDocumentRecallLimit)
	for _, row := range rows {
		c := repo.ordered[row["chunk_index"].(int)]
		require.Equal(t, c.ID, row["chunk_id"])
		require.Equal(t, "doc-1", row["knowledge_id"])
		require.Equal(t, "kb-1", row["knowledge_base"])
		require.Equal(t, c.ContentRevision, row["content_revision"])
		require.Equal(t, c.Content, row["content"])
		require.Equal(t, c.StartAt, row["start_at"])
		require.Equal(t, c.EndAt, row["end_at"])
		require.Equal(t, c.ParentChunkID, row["parent_chunk_id"])
		require.Equal(t, c.SourceLocators, row["source_locators"])
		require.Equal(t, true, row["content_complete"])
		require.Equal(t, "semantic_recall", row["role"])
	}
	require.Contains(t, result.Output, "semantic_within_document")
}
func TestReadDocumentRecallUnavailableIsExplicit(t *testing.T) {
	for _, scenario := range []string{"not_configured", "retrieval_failure", "keyword_only", "missing_original", "adapter_panic"} {
		t.Run(scenario, func(t *testing.T) {
			read, _, index := recallFixture(t)
			switch scenario {
			case "not_configured":
				read.WithDocumentSearch(nil)
			case "retrieval_failure":
				index.err = errors.New("retrieval unavailable")
			case "keyword_only":
				index.keywordOnly = true
			case "adapter_panic":
				index.panicOnLoad = true
			case "missing_original":
				for _, result := range index.results {
					result.SourceChunk = nil
				}
			}
			result, err := read.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","query":"requested topic"}`))
			require.NoError(t, err)
			require.True(t, result.Success, "completed literal scan stays successful")
			require.Equal(t, 0, result.Data["fetched_chunks"])
			scope := result.Data["query_scope"].(map[string]any)
			require.Equal(t, "unavailable", scope["fallback_status"])
			require.Contains(t, scope["fallback_notice"], "Zero literal matches")
			require.Contains(t, scope["fallback_notice"], "recall is unavailable")
			require.Contains(t, result.Output, "recall is unavailable")
		})
	}
}
func TestReadDocumentRecallCannotCrossAuthorizedOrBoundScope(t *testing.T) {
	t.Run("provider_ignores_document_filter", func(t *testing.T) {
		read, repo, index := recallFixture(t)
		foreign := *repo.ordered[0]
		foreign.ID = "foreign-chunk"
		foreign.KnowledgeID = "foreign-document"
		index.results = append([]*types.SearchResult{recallSearchResult(&foreign)}, index.results...)
		mismatch := *repo.ordered[1]
		mismatch.TenantID = 99
		index.results[2].SourceChunk = &mismatch
		result, err := read.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","query":"requested topic"}`))
		require.NoError(t, err)
		require.NotContains(t, chunkIDsFromData(t, result.Data), "foreign-chunk")
		require.NotContains(t, chunkIDsFromData(t, result.Data), mismatch.ID)
		require.NotContains(t, result.Output, "foreign-document")
	})
	for _, scenario := range []string{"outside_authorization", "outside_binding"} {
		t.Run(scenario, func(t *testing.T) {
			read, _, index := recallFixture(t)
			read.knowledgeService.(*readDocKnowledgeService).docs["other-document"] = &types.Knowledge{ID: "other-document", KnowledgeBaseID: "kb-1", TenantID: 7}
			if scenario == "outside_authorization" {
				read.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", TenantID: 7, KnowledgeIDs: []string{"doc-1"}}}
			}
			result, _ := read.Execute(t.Context(), json.RawMessage(`{"id":"other-document","query":"requested topic"}`))
			require.False(t, result.Success)
			require.True(t, result.PreflightRejected)
			require.Empty(t, index.requests)
		})
	}
	t.Run("physical_page_boundary", func(t *testing.T) {
		read, repo, index := recallFixture(t)
		repo.ordered[0].Content = "<!-- Page: 1 -->\nOnly this page may be read."
		result, err := read.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":1,"query":"requested topic"}`))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.Empty(t, index.requests)
	})
}
func TestReadDocumentRecallOnlyFollowsCompleteZeroMatchLiteralScan(t *testing.T) {
	for _, input := range []string{`{"id":"doc-1","query":"Alternate"}`, `{"id":"doc-1","query":"requested topic","offset":1}`, `{"id":"doc-1","query":"requested.*topic","regex":true}`} {
		t.Run(input, func(t *testing.T) {
			read, _, index := recallFixture(t)
			result, err := read.Execute(t.Context(), json.RawMessage(input))
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Empty(t, index.requests)
		})
	}
}
func TestReadDocumentRecallPreservesPartialBodyProjectionAndContinuation(t *testing.T) {
	read, repo, index := recallFixture(t)
	repo.ordered[0].Content = strings.Repeat("Original source sentence. ", 1000)
	index.results = []*types.SearchResult{recallSearchResult(repo.ordered[0])}
	result, err := read.Execute(WithOutputBudget(t.Context(), 1500), json.RawMessage(`{"id":"doc-1","query":"requested topic"}`))
	require.NoError(t, err)
	row := result.Data["chunks"].([]map[string]any)[0]
	require.Equal(t, false, row["content_complete"])
	require.NotNil(t, row["content_source_view"])
	require.Positive(t, row["next_content_offset"])
	require.Equal(t, 9, row["content_revision"])
}
