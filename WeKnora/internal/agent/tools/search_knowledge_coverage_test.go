package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/reranking"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

var comparisonReportTitles = map[string]string{
	"report-a": "设备周报 春初", "report-b": "设备周报 春中",
	"report-c": "设备周报 春末", "report-d": "设备周报 夏初",
}

type comparisonSearchIndex struct {
	stubKnowledgeBaseService
	hits       map[string][]*types.SearchResult
	errors     map[string]error
	foreign    []*types.SearchResult
	embeddings atomic.Int32
}

func (s *comparisonSearchIndex) HybridSearch(_ context.Context, _ string, params types.SearchParams) ([]*types.SearchResult, error) {
	if len(params.KnowledgeIDs) == 1 {
		id := params.KnowledgeIDs[0]
		return append(append([]*types.SearchResult{}, s.hits[id]...), s.foreign...), s.errors[id]
	}
	var rows []*types.SearchResult
	for _, hits := range s.hits {
		rows = append(rows, hits...)
	}
	return append(rows, s.foreign...), nil
}

func (s *comparisonSearchIndex) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return map[string]string{"equipment-kb": "embedding"}
}

func (s *comparisonSearchIndex) GetQueryEmbedding(context.Context, string, string) ([]float32, error) {
	s.embeddings.Add(1)
	return []float32{1}, nil
}

type comparisonSearchDocuments struct {
	interfaces.KnowledgeService
	rows map[string]*types.Knowledge
}

func (s *comparisonSearchDocuments) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	return s.rows[id], nil
}

func comparisonSearchHit(knowledge *types.Knowledge, suffix string, page, index int, score float64, body string) *types.SearchResult {
	chunk := &types.Chunk{
		ID: knowledge.ID + "-" + suffix, KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID,
		TenantID: knowledge.TenantID, IsEnabled: true, ChunkIndex: index, ChunkType: types.ChunkTypeText,
		Content: body, ContentRevision: page, EndAt: len([]rune(body)),
		SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: page, Mapping: "exact", SourceHash: knowledge.FileHash}},
	}
	return &types.SearchResult{
		SourceChunk: chunk, ID: chunk.ID, KnowledgeID: chunk.KnowledgeID, KnowledgeBaseID: chunk.KnowledgeBaseID,
		Content: body, KnowledgeTitle: knowledge.Title, Score: score, ChunkIndex: index, ChunkType: string(chunk.ChunkType),
		ContentRevision: chunk.ContentRevision, EndAt: chunk.EndAt, SourceLocators: chunk.SourceLocators,
	}
}

func comparisonSearchFixture(t *testing.T) (*SearchKnowledgeTool, *comparisonSearchIndex, *stubReranker, DocumentScopePlan) {
	t.Helper()
	docs := &comparisonSearchDocuments{rows: map[string]*types.Knowledge{}}
	index := &comparisonSearchIndex{hits: map[string][]*types.SearchResult{}, errors: map[string]error{}}
	plan := DocumentScopePlan{Reason: "比较四份设备周报中的维护周期"}
	for n, id := range []string{"report-d", "report-b", "report-a", "report-c"} {
		doc := &types.Knowledge{
			ID: id, Title: comparisonReportTitles[id], KnowledgeBaseID: "equipment-kb", TenantID: 1,
			FileHash: strings.Repeat(string(rune('a'+n)), 64),
		}
		docs.rows[id] = doc
		plan.Sources = append(plan.Sources, DocumentSourceSelection{KnowledgeID: id, Title: doc.Title, Coverage: []DocumentCoverageBinding{{Target: 0}}})
		body, score := "计划在四月进行翻修，预计停机 5 天。", 0.1
		if id == "report-d" {
			body = "计划在五月进行翻修，预计停机 5 天。"
		}
		if id == "report-a" {
			score = 0.99
		}
		index.hits[id] = []*types.SearchResult{comparisonSearchHit(doc, "main", 2*n+3, 0, score, body)}
	}
	docA := docs.rows["report-a"]
	for i := 1; i <= 203; i++ {
		body := fmt.Sprintf("编号 %d 的设备准备工作。", i)
		index.hits[docA.ID] = append(index.hits[docA.ID], comparisonSearchHit(docA, fmt.Sprint(i), 2, i, 0.8, body))
	}
	model := &stubReranker{scores: make([]float64, reranking.DefaultMaxCandidates)}
	for i := range model.scores {
		model.scores[i] = 0.9
	}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: docA.KnowledgeBaseID, TenantID: 1}}
	tool := NewSearchKnowledgeTool(index, docs, nil, targets, model,
		&config.Config{Conversation: &config.ConversationConfig{RerankThreshold: 0.3, EmbeddingTopK: 220}})
	registry := NewToolRegistry()
	registry.RegisterTool(tool)
	PrepareDocumentScope(registry, "只看设备周报第3页")
	SetDocumentCoverageTargets(registry, []DocumentCoverageTarget{{ByDocument: true}})
	return tool, index, model, plan
}

func executeComparisonSearch(t *testing.T, ctx context.Context, tool *SearchKnowledgeTool, plan *DocumentScopePlan, limit int) *types.ToolResult {
	t.Helper()
	targetIndex := 0
	args, err := json.Marshal(SearchKnowledgeInput{CoverageTargetIndex: &targetIndex, Query: "设备翻修计划分别是什么？", SourceScope: plan, Limit: limit})
	require.NoError(t, err)
	result, err := tool.Execute(ctx, args)
	require.NoError(t, err)
	return result
}

func comparisonStatuses(result *types.ToolResult) map[string]string {
	statuses := map[string]string{}
	rows, _ := result.Data["document_coverage"].([]map[string]any)
	for _, row := range rows {
		statuses[row["knowledge_id"].(string)] = row["status"].(string)
	}
	return statuses
}

func TestBoundSearchRotatesCandidateBatchAndKeepsSameNameReportsSeparate(t *testing.T) {
	tool, index, reranker, plan := comparisonSearchFixture(t)
	a, b := index.hits["report-a"][0], index.hits["report-b"][0]
	documents := tool.knowledgeService.(*comparisonSearchDocuments).rows
	docA, docB := documents[a.KnowledgeID], documents[b.KnowledgeID]
	docA.Title, docB.Title = "设备月报", "设备月报"
	for i := range plan.Sources {
		plan.Sources[i].Title = documents[plan.Sources[i].KnowledgeID].Title
	}
	a.KnowledgeTitle, b.KnowledgeTitle = docA.Title, docB.Title
	a.SourceLocators[0].Page, b.SourceLocators[0].Page = 2, 3
	index.hits[docA.ID] = append(index.hits[docA.ID], comparisonSearchHit(docA, "same-page-8", 8, 204, 0.7, a.Content))
	secret := &types.Knowledge{ID: "private-report", KnowledgeBaseID: "equipment-kb"}
	index.foreign = []*types.SearchResult{comparisonSearchHit(secret, "hidden", 4, 0, 1, a.Content)}
	result := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	require.True(t, result.Success)
	require.EqualValues(t, 1, index.embeddings.Load(), "same-model targets share their embedding")
	require.Len(t, reranker.documents, reranking.DefaultMaxCandidates)
	firstBatch := strings.Join(reranker.documents[:4], "\n")
	require.Contains(t, firstBatch, comparisonReportTitles["report-d"])
	require.Contains(t, firstBatch, comparisonReportTitles["report-c"])
	require.Equal(t, 2, strings.Count(firstBatch, "设备月报"))
	require.Equal(t, map[string]string{"report-a": "hit", "report-b": "hit", "report-c": "limit_truncated", "report-d": "limit_truncated"}, comparisonStatuses(result))

	rows := result.Data["results"].([]map[string]any)
	require.Len(t, rows, 2)
	require.Equal(t, "report-a", rows[0]["knowledge_id"])
	require.Equal(t, 2, rows[0]["source_locators"].(types.SourceLocators)[0].Page)
	origins := rows[0]["origins"].([]map[string]any)
	require.Len(t, origins, 1)
	require.Equal(t, "report-a-same-page-8", origins[0]["chunk_id"])
	require.Equal(t, 8, origins[0]["content_revision"])
	require.Equal(t, 8, origins[0]["source_locators"].(types.SourceLocators)[0].Page)
	require.Equal(t, "report-b", rows[1]["knowledge_id"])
	require.Equal(t, 3, rows[1]["source_locators"].(types.SourceLocators)[0].Page)
	require.NotContains(t, rows[1], "origins")
	registry := modelcontext.NewRegistry(true)
	view := registry.ModelToolResult(result)
	require.Equal(t, 2, strings.Count(view, "计划在四月进行翻修"))
	require.Contains(t, view, `physical_pages="8"`)
	require.NotEmpty(t, registry.CitableChunkHandle("report-a-same-page-8"))
	require.NotEmpty(t, registry.CitableChunkHandle("report-b-main"))
	require.Empty(t, registry.CitableChunkHandle("private-report-hidden"))
}

func TestBoundSearchReportsSeparateFailureNoMatchAndBudget(t *testing.T) {
	tool, index, _, plan := comparisonSearchFixture(t)
	index.hits["report-b"] = nil
	index.errors["report-c"] = context.DeadlineExceeded
	result := executeComparisonSearch(t, WithOutputBudget(t.Context(), 80), tool, &plan, 2)
	require.True(t, result.Success)
	require.Equal(t, map[string]string{"report-a": "budget_truncated", "report-b": "no_match", "report-c": "failed", "report-d": "budget_truncated"}, comparisonStatuses(result))
	require.Zero(t, result.Data["count"])
	view := modelcontext.NewRegistry(true).ModelToolResult(result)
	for _, status := range []string{"no_match", "failed", "budget_truncated"} {
		require.Contains(t, view, `status="`+status+`"`)
	}
	result = executeComparisonSearch(t, t.Context(), tool, nil, 1)
	require.Equal(t, "limit_truncated", comparisonStatuses(result)["report-d"])
	require.Equal(t, "hit", comparisonStatuses(result)["report-a"])
}

func TestSearchOriginsDoNotInheritRetractionThatWasNotVerified(t *testing.T) {
	tool, index, _, plan := comparisonSearchFixture(t)
	// Duplicate visible bodies cannot bypass a retraction, stale revision or
	// missing original body evidence even if the index returns them together.
	page := &types.WikiPage{ID: "retracted", KnowledgeBaseID: "equipment-kb", TenantID: 1}
	types.SetWikiRetractionSource(page, "deleted-source", false)
	tool.WithWikiPageService(wikiRetractionLookup{page: page})
	wiki := index.hits["report-b"][0]
	wiki.ID, wiki.SourceChunk.ID = "wp-retracted", "wp-retracted"
	wiki.ChunkType, wiki.SourceChunk.ChunkType = string(types.ChunkTypeWikiPage), types.ChunkTypeWikiPage
	stale := index.hits["report-c"][0]
	stale.SourceLocators[0].SourceHash = strings.Repeat("f", 64)
	index.hits["report-d"][0].Content = index.hits["report-a"][0].Content
	index.hits["report-d"][0].SourceChunk = nil

	result := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	rows := result.Data["results"].([]map[string]any)
	require.Len(t, rows, 2)
	require.NotContains(t, rows[0], "origins")
	require.Equal(t, "report-d", rows[1]["knowledge_id"])
	registry := modelcontext.NewRegistry(true)
	view := registry.ModelToolResult(result)
	require.NotContains(t, view, `navigation_only="true"`)
	require.Contains(t, view, comparisonReportTitles["report-d"])
	require.NotContains(t, view, comparisonReportTitles["report-b"])
	require.NotContains(t, view, comparisonReportTitles["report-c"])
	require.Empty(t, registry.CitableChunkHandle("wp-retracted"))
	require.Empty(t, registry.CitableChunkHandle("report-c-main"))

	for i := range plan.Sources {
		if plan.Sources[i].KnowledgeID == "report-a" {
			plan.Sources[i].UserPages = &DocumentPageConstraint{Pages: []int{3}, UserQuote: "只看设备周报第3页"}
		}
	}
	restricted := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	require.False(t, restricted.Success)
	require.True(t, restricted.PreflightRejected)
	require.NotContains(t, restricted.Output, "计划在四月进行翻修")
}

func TestCoverageBudgetDropsSameDocumentExtraBeforeOtherDocuments(t *testing.T) {
	tool, index, _, _ := comparisonSearchFixture(t)
	results := []*searchResultWithMeta{
		{SearchResult: index.hits["report-a"][0]},
		{SearchResult: index.hits["report-a"][1]},
		{SearchResult: index.hits["report-b"][0]},
	}
	// Set the budget to fit the two representatives exactly. The extra
	// higher-ranking row must be sacrificed for the other document.
	representatives := tool.formatOutput(t.Context(), []*searchResultWithMeta{results[0], results[2]}, nil, "翻修", SearchModeHybrid)
	budget := (len([]rune(representatives.Output))*5 + 3) / 4
	_, shown := tool.formatWithinBudget(WithOutputBudget(t.Context(), budget), results, nil, "翻修", SearchModeHybrid, []string{"report-a", "report-b"})
	require.Len(t, shown, 2)
	require.Equal(t, "report-a-main", shown[0].ID)
	require.Equal(t, "report-b-main", shown[1].ID)
}

func TestSearchDoesNotRestoreBelowThresholdDocumentAsOrigin(t *testing.T) {
	tool, _, reranker, plan := comparisonSearchFixture(t)
	reranker.scores[1] = 0.01 // The second document in the round-robin batch.
	result := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	require.Equal(t, "no_match", comparisonStatuses(result)["report-b"])
	require.Equal(t, "hit", comparisonStatuses(result)["report-c"])
	registry := modelcontext.NewRegistry(true)
	view := registry.ModelToolResult(result)
	require.NotContains(t, view, comparisonReportTitles["report-b"])
	require.NotContains(t, view, `physical_pages="5"`)
	require.Empty(t, registry.CitableChunkHandle("report-b-main"))
	require.NotEmpty(t, registry.CitableChunkHandle("report-c-main"))
}

func TestSearchExposesEachFailedTargetWhenAllFail(t *testing.T) {
	tool, index, _, plan := comparisonSearchFixture(t)
	for _, source := range plan.Sources {
		index.errors[source.KnowledgeID] = context.DeadlineExceeded
	}
	result := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	require.False(t, result.Success)
	require.Equal(t, map[string]string{"report-a": "failed", "report-b": "failed", "report-c": "failed", "report-d": "failed"}, comparisonStatuses(result))
	require.Equal(t, 4, strings.Count(modelcontext.NewRegistry(true).ModelToolResult(result), `status="failed"`))
}

func TestUnboundIdenticalTextOffersEachDocumentAsNavigation(t *testing.T) {
	tool, _, _, _ := comparisonSearchFixture(t)
	result := executeComparisonSearch(t, t.Context(), tool, nil, 1)
	require.Equal(t, "document_info", result.Data["display_type"])
	require.Equal(t, true, result.Data["requires_source_scope"])
	navigation := map[string]int{}
	for _, doc := range result.Data["documents"].([]map[string]any) {
		matches, _ := doc["navigation_matches"].([]map[string]any)
		if len(matches) > 0 {
			require.True(t, matches[0]["navigation_only"].(bool))
			navigation[doc["knowledge_id"].(string)] = matches[0]["page_hint"].(int)
		}
	}
	require.Equal(t, map[string]int{"report-a": 7, "report-b": 5, "report-c": 9, "report-d": 3}, navigation)
	registry := modelcontext.NewRegistry(true)
	view := registry.ModelToolResult(result)
	require.Contains(t, view, comparisonReportTitles["report-b"])
	require.Contains(t, view, comparisonReportTitles["report-c"])
	require.Empty(t, registry.CitableChunkHandle("report-a-main"))
	require.Empty(t, registry.CitableChunkHandle("report-b-main"))
}

func TestCoverageBudgetKeepsShorterDocumentWhenFirstIsTooLarge(t *testing.T) {
	tool, index, _, _ := comparisonSearchFixture(t)
	a := *index.hits["report-a"][0]
	a.Content = strings.Repeat("设备说明", 2000)
	results := []*searchResultWithMeta{{SearchResult: &a}, {SearchResult: index.hits["report-b"][0]}}
	_, shown := tool.formatWithinBudget(WithOutputBudget(t.Context(), 1000), results, nil, "翻修", SearchModeHybrid, []string{"report-a", "report-b"})
	require.Len(t, shown, 1)
	require.Equal(t, "report-b-main", shown[0].ID)
}

func TestRequiredPagesSurviveCandidateDisplayAndTextLimits(t *testing.T) {
	tool, index, model, plan := comparisonSearchFixture(t)
	doc := tool.knowledgeService.(*comparisonSearchDocuments).rows["report-a"]
	location := 0
	tool.documentConstraint.coverageTargets[0] = DocumentCoverageTarget{
		Locations: []DocumentRequiredLocation{{Period: "四月", Pages: []int{6, 12}, Fact: "月度开机率"}},
	}
	plan.Sources = []DocumentSourceSelection{{KnowledgeID: doc.ID, Title: doc.Title,
		Coverage: []DocumentCoverageBinding{{Target: 0, Location: &location}}}}
	page6 := comparisonSearchHit(doc, "page-six", 6, 205, 0.2, "月度开机率定义")
	page12 := comparisonSearchHit(doc, "page-twelve", 12, 206, 0.1, "月度开机率为 45%")
	index.hits[doc.ID] = append(index.hits[doc.ID], page6, page12)

	result := executeComparisonSearch(t, t.Context(), tool, &plan, 2)
	require.Len(t, model.documents, reranking.DefaultMaxCandidates)
	require.Contains(t, model.documents[0], page6.Content)
	require.Contains(t, model.documents[1], page12.Content)
	require.Equal(t, map[int]string{6: "hit", 12: "hit"}, searchPageStatuses(result))
	for _, row := range result.Data["results"].([]map[string]any) {
		require.Contains(t, []string{page6.ID, page12.ID}, row["chunk_id"])
	}

	result = executeComparisonSearch(t, t.Context(), tool, nil, 1)
	require.Equal(t, map[int]string{6: "hit", 12: "limit_truncated"}, searchPageStatuses(result))
	shown := result.Data["results"].([]map[string]any)
	require.Equal(t, page6.ID, shown[0]["chunk_id"])

	query := "设备翻修计划分别是什么？"
	rows := []*searchResultWithMeta{{SearchResult: page6, KnowledgeBaseID: doc.KnowledgeBaseID}, {SearchResult: page12, KnowledgeBaseID: doc.KnowledgeBaseID}}
	format := func(rows []*searchResultWithMeta) int {
		text := tool.formatOutput(t.Context(), rows, []string{doc.KnowledgeBaseID}, query, SearchModeHybrid).Output
		return (len([]rune(text))*5 + 3) / 4
	}
	result = executeComparisonSearch(t, WithOutputBudget(t.Context(), format(rows)), tool, nil, 3)
	require.Equal(t, map[int]string{6: "hit", 12: "hit"}, searchPageStatuses(result))
	require.Equal(t, 2, result.Data["count"])

	result = executeComparisonSearch(t, WithOutputBudget(t.Context(), max(format(rows[:1]), format(rows[1:]))), tool, nil, 2)
	require.Equal(t, map[int]string{6: "hit", 12: "budget_truncated"}, searchPageStatuses(result))

	model.scores[1] = 0.01
	result = executeComparisonSearch(t, t.Context(), tool, nil, 2)
	require.Equal(t, map[int]string{6: "hit", 12: "below_threshold"}, searchPageStatuses(result))
	locations := []searchLocation{{doc.KnowledgeBaseID, doc.ID, 6}, {doc.KnowledgeBaseID, doc.ID, 12}}
	candidates := roundRobinSearchCandidates(rows, nil, 1, locations...)
	require.Equal(t, []map[string]any{
		{"knowledge_id": doc.ID, "knowledge_base_id": doc.KnowledgeBaseID, "page": 6, "target_index": 0,
			"location_index": 0, "period": "四月", "fact": "月度开机率", "status": "hit"},
		{"knowledge_id": doc.ID, "knowledge_base_id": doc.KnowledgeBaseID, "page": 12, "target_index": 0,
			"location_index": 0, "period": "四月", "fact": "月度开机率", "status": "candidate_truncated"},
	}, locationSearchCoverage(tool.documentConstraint.coverageRequirements(0), map[string]bool{doc.ID: true},
		locations, rows, candidates, candidates, candidates, candidates))
}

func searchPageStatuses(result *types.ToolResult) map[int]string {
	statuses := map[int]string{}
	for _, row := range result.Data["document_coverage"].([]map[string]any) {
		statuses[row["page"].(int)] = row["status"].(string)
	}
	return statuses
}

func TestComparisonRotationRequiresCurrentMultiDocumentTarget(t *testing.T) {
	tool, _, model, plan := comparisonSearchFixture(t)
	tool.documentConstraint.coverageTargets = []DocumentCoverageTarget{{ByDocument: true}, {ByDocument: true}, {}}
	plan.Sources[0].Coverage = append(plan.Sources[0].Coverage, DocumentCoverageBinding{Target: 1})
	for i := range plan.Sources {
		plan.Sources[i].Coverage = append(plan.Sources[i].Coverage, DocumentCoverageBinding{Target: 2})
	}

	run := func(target *int, sourceScope *DocumentScopePlan) (*types.ToolResult, []string) {
		t.Helper()
		args, err := json.Marshal(SearchKnowledgeInput{Query: "设备翻修计划", SourceScope: sourceScope, CoverageTargetIndex: target, Limit: 2})
		require.NoError(t, err)
		result, err := tool.Execute(t.Context(), args)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		return result, append([]string(nil), model.documents...)
	}
	ordinary, ordinaryCandidates := run(nil, &plan)
	for _, index := range []int{1, 2} {
		result, candidates := run(&index, nil)
		require.NotContains(t, result.Data, "document_coverage")
		require.Equal(t, ordinaryCandidates, candidates)
		require.Equal(t, ordinary.Data["results"], result.Data["results"])
	}
	active := 0
	result, candidates := run(&active, nil)
	require.NotEqual(t, ordinaryCandidates, candidates)
	require.Equal(t, "hit", comparisonStatuses(result)["report-b"])
	require.NotContains(t, comparisonStatuses(ordinary), "report-b")
}
