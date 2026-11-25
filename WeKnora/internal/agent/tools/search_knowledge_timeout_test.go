package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/api/openaiembeddings"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type embeddingRoundTripper func(*http.Request) (*http.Response, error)

func (send embeddingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	_ = req.Body.Close()
	return send(req)
}

type timedSearchService struct {
	stubKnowledgeBaseService
	client        *openaiembeddings.Client
	lookupIDs     []string
	embeddingIDs  []string
	searchIDs     []string
	search        types.SearchParams
	searchContext context.Context
}

func (s *timedSearchService) GetKnowledgeBasesByIDsOnly(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	s.lookupIDs = ids
	return []*types.KnowledgeBase{s.kb}, nil
}

func (s *timedSearchService) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return map[string]string{s.kb.ID: "shared-model"}
}

func (s *timedSearchService) GetQueryEmbedding(ctx context.Context, kbID, query string) ([]float32, error) {
	s.embeddingIDs = append(s.embeddingIDs, kbID)
	vectors, err := s.client.Embed(ctx, []string{query}, api.EmbedQuery)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (s *timedSearchService) HybridSearch(ctx context.Context, kbID string, params types.SearchParams) ([]*types.SearchResult, error) {
	s.searchIDs = append(s.searchIDs, kbID)
	s.search = params
	s.searchContext = ctx
	return s.results, ctx.Err()
}

func TestSearchKnowledgeEmbeddingBudget(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	const fallbackTimeout = "向量检索暂时超时，已改用关键词检索。"
	const fallbackUnavailable = "向量检索暂时不可用，已改用关键词检索。"
	for _, tc := range []struct {
		name, mode, configured, fallback string
		parent, budget, elapsed          time.Duration
		attempts                         int
		vectorOnly, canceled, navigation bool
		respond                          func(int, context.Context) (int, error)
	}{
		{
			name: "hung embedding leaves time for keyword search", parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: 22 * time.Second, attempts: 1,
			fallback: fallbackTimeout,
			respond: func(_ int, ctx context.Context) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			},
		},
		{
			name: "transport timeout then success", parent: 60 * time.Second, configured: "20s",
			budget: 20 * time.Second, elapsed: 3 * time.Second, attempts: 2,
			respond: func(attempt int, _ context.Context) (int, error) {
				if attempt == 1 {
					time.Sleep(2 * time.Second)
					return 0, context.DeadlineExceeded
				}
				return http.StatusOK, nil
			},
		},
		{
			name: "transport keeps failing", parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: time.Second, attempts: 2,
			fallback: fallbackUnavailable,
			respond:  func(_ int, _ context.Context) (int, error) { return 0, io.ErrUnexpectedEOF },
		},
		{
			name: "short parent budget skips retry immediately", parent: 12 * time.Second,
			budget: 6 * time.Second, elapsed: 2 * time.Second, attempts: 1,
			fallback: fallbackTimeout, navigation: true,
			respond: func(_ int, _ context.Context) (int, error) {
				time.Sleep(2 * time.Second)
				return 0, context.DeadlineExceeded
			},
		},
		{
			name: "provider rejection falls back without retry", parent: 60 * time.Second,
			budget: 22 * time.Second, attempts: 1, fallback: fallbackUnavailable,
			respond: func(_ int, _ context.Context) (int, error) { return http.StatusBadRequest, nil },
		},
		{
			name: "user cancels while embedding", parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: 100 * time.Millisecond, attempts: 1, canceled: true,
			respond: func(_ int, ctx context.Context) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			},
		},
		{
			name: "user cancels during retry backoff", parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: 100 * time.Millisecond, attempts: 1, canceled: true,
			respond: func(_ int, _ context.Context) (int, error) { return 0, io.ErrUnexpectedEOF },
		},
		{
			name: "hybrid request against a vector-only index", parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: 22 * time.Second, attempts: 1, vectorOnly: true,
			respond: func(_ int, ctx context.Context) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			},
		},
		{
			name: "explicit semantic search preserves its mode", mode: SearchModeSemantic, parent: 60 * time.Second,
			budget: 22 * time.Second, elapsed: time.Second, attempts: 2,
			respond: func(_ int, _ context.Context) (int, error) { return 0, context.DeadlineExceeded },
		},
		{
			name: "keyword search skips embedding", mode: SearchModeKeyword, parent: 60 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEARCH_KNOWLEDGE_EMBEDDING_TIMEOUT", tc.configured)
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ctx, cancel := context.WithTimeout(types.WithExecutionTenant(t.Context(), 7), tc.parent)
				defer cancel()
				if tc.canceled {
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
				}
				var requests []*http.Request
				client := openaiembeddings.New(openaiembeddings.Config{
					Endpoint: api.Endpoint{
						URL: "http://127.0.0.1:1/embeddings",
						Client: &http.Client{Transport: embeddingRoundTripper(func(req *http.Request) (*http.Response, error) {
							requests = append(requests, req)
							status, err := tc.respond(len(requests), req.Context())
							if err != nil {
								return nil, err
							}
							body := io.NopCloser(strings.NewReader(`{"data":[{"embedding":[0.1,0.2],"index":0}]}`))
							return &http.Response{StatusCode: status, Body: body}, nil
						})},
					},
					Retry: api.DefaultRetryPolicy(),
				})
				service := &timedSearchService{
					client: client,
					stubKnowledgeBaseService: stubKnowledgeBaseService{
						kb: kbWithIndexes("shared", true, !tc.vectorOnly, types.KnowledgeBaseTypeDocument),
						results: []*types.SearchResult{{
							ID: "result", KnowledgeBaseID: "shared", KnowledgeID: "allowed-doc",
							KnowledgeTitle: "Refund policy", Content: "You can request a refund within the return period.", Score: 0.9,
						}},
					},
				}
				target := &types.SearchTarget{
					Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "shared", TenantID: 9,
					KnowledgeIDs: []string{"allowed-doc"}, TagIDs: []string{"index-tag"}, ScopeTagIDs: []string{"selected-tag"},
				}
				var knowledgeService interfaces.KnowledgeService
				if tc.navigation {
					target = &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "shared", TenantID: 9}
					knowledgeService = &readDocKnowledgeService{docs: map[string]*types.Knowledge{
						"allowed-doc": {ID: "allowed-doc", KnowledgeBaseID: "shared", TenantID: 9, Title: "Refund policy"},
					}}
				}
				tool := NewSearchKnowledgeTool(service, knowledgeService, nil, types.SearchTargets{
					target, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "other", TenantID: 7},
				}, nil, nil)
				if tc.navigation {
					tool.WithDocumentConstraint(NewDocumentScopeState())
				}
				args, err := json.Marshal(SearchKnowledgeInput{
					Query: "refund policy", Mode: tc.mode, KnowledgeBaseIDs: []string{"shared"},
				})
				require.NoError(t, err)
				result, err := tool.Execute(ctx, args)
				require.Equal(t, tc.elapsed, time.Since(start))
				require.Equal(t, []string{"shared"}, service.lookupIDs)
				require.Len(t, requests, tc.attempts)
				if tc.attempts > 0 {
					require.Equal(t, []string{"shared"}, service.embeddingIDs)
				}
				for _, req := range requests {
					deadline, ok := req.Context().Deadline()
					require.True(t, ok)
					require.Equal(t, tc.budget, deadline.Sub(start))
					require.Equal(t, uint64(7), types.MustTenantIDFromContext(req.Context()))
				}
				if tc.canceled {
					require.ErrorIs(t, err, context.Canceled)
					require.Empty(t, service.searchIDs)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NoError(t, ctx.Err())
				require.Equal(t, tc.parent-tc.elapsed, time.Until(start.Add(tc.parent)))
				if tc.vectorOnly || tc.mode == SearchModeSemantic {
					require.False(t, result.Success)
					require.Contains(t, result.Error, "向量检索暂时超时")
					require.NotContains(t, result.Error, "已改用关键词")
					require.Empty(t, service.searchIDs)
					return
				}

				require.True(t, result.Success, result.Error)
				require.Equal(t, []string{"shared"}, service.searchIDs)
				require.Same(t, ctx, service.searchContext)
				require.Equal(t, target.KnowledgeIDs, service.search.KnowledgeIDs)
				require.Equal(t, target.TagIDs, service.search.TagIDs)
				require.Equal(t, target.ScopeTagIDs, service.search.ScopeTagIDs)
				require.Equal(t, tc.fallback != "" || tc.mode == SearchModeKeyword, service.search.DisableVectorMatch)
				require.False(t, service.search.DisableKeywordsMatch)
				modelOutput := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolSearchKnowledge, result)
				require.Contains(t, modelOutput, "Refund policy")
				if tc.navigation {
					require.Equal(t, []string{"shared"}, service.search.KnowledgeBaseIDs)
					require.Contains(t, modelOutput, tc.fallback)
					require.Contains(t, result.Output, tc.fallback)
					require.Equal(t, true, result.Data["requires_source_scope"])
					require.Empty(t, tool.documentConstraint.selectedIDs())
					return
				}
				require.Equal(t, 1, result.Data["count"])
				if tc.fallback != "" {
					require.Empty(t, service.search.QueryEmbedding)
					require.Equal(t, SearchModeKeyword, result.Data["mode"])
					require.Contains(t, modelOutput, tc.fallback)
				} else {
					require.NotContains(t, result.Data, "mode_fallbacks")
					if tc.attempts > 0 {
						require.Equal(t, []float32{0.1, 0.2}, service.search.QueryEmbedding)
					}
				}
			})
		})
	}
}
