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

func reportScopeFixture(t *testing.T, titles ...string) (*ReadDocumentTool, *RequestedDocumentConstraint) {
	t.Helper()
	tool, repo := newReadDocumentFixture(3)
	service := tool.knowledgeService.(*readDocKnowledgeService)
	var docs []*types.Knowledge
	for i, title := range titles {
		id := "report-a"
		if i > 0 {
			id = "report-b"
		}
		k := &types.Knowledge{ID: id, Title: title, FileName: title, FileType: "pptx", TenantID: 7, KnowledgeBaseID: "kb-1"}
		service.docs[id] = k
		docs = append(docs, k)
	}
	service.pages = map[string]*types.PageResult{"kb-1": {Data: docs, Total: int64(len(docs))}}
	for _, c := range repo.ordered {
		c.KnowledgeID = "report-a"
	}
	repo.ordered[0].Content = "<!-- Slide number: 11 -->\n# 第11页\n另一页郑棉01-05价差。"
	repo.ordered[1].Content = "<!-- Slide number: 12 -->\n# 第12页\n郑棉01-05价差\n![第12页](target.png)\n<!-- Slide number: 13 -->\n# 第13页\n禁止混入的下一页。"
	repo.ordered[1].ImageInfo = `[{"url":"target.png","caption":"第12页 红线2401 约250元/吨"},{"url":"foreign.png","caption":"第13页 红线2301 约900元/吨"}]`
	repo.ordered[2].Content = "第13页的继续内容。"
	constraint := NewDocumentScopeState()
	if len(titles) > 0 {
		_, err := constraint.bind(context.Background(), DocumentScopePlan{Reason: "model-selected actual source", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: titles[0], Page: 12}}}, tool.knowledgeService, tool.searchTargets)
		require.NoError(t, err)
	}
	return tool.WithDocumentConstraint(constraint), constraint
}

func TestReadDocumentPhysicalPageClipsNeighboursAndImages(t *testing.T) {
	tool, _ := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"report-a","offset":0,"limit":20}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, 12, res.Data["page"])
	require.Equal(t, []string{"chunk-1"}, chunkIDsFromData(t, res.Data))
	require.Contains(t, res.Output, "红线2401")
	require.NotContains(t, res.Output, "禁止混入")
	require.NotContains(t, res.Output, "红线2301")
	scope := res.Data["document_scope"].(map[string]interface{})
	require.Equal(t, true, scope["matched"])
	require.Equal(t, "report-a", scope["knowledge_id"])
	require.Equal(t, true, scope["has_visual_evidence"])
}

func TestReadDocumentDoesNotReplacePhysicalPageWithChunkOffset(t *testing.T) {
	tool, _ := newReadDocumentFixture(20)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":12}`))
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Contains(t, res.Error, "不能把第 12 个分块")
	require.NotContains(t, res.Data, "document_scope")
}

func TestReadDocumentPageContinuesAcrossChunks(t *testing.T) {
	tool, repo := newReadDocumentFixture(4)
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\n一页。"
	repo.ordered[1].Content = "<!-- Slide number: 2 -->\n目标开头。"
	repo.ordered[2].Content = "目标延续。"
	repo.ordered[3].Content = "<!-- Slide number: 3 -->\n下一页。"
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":2,"limit":1}`))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, 1, res.Data["next_offset"])
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":2,"offset":1}`))
	require.NoError(t, err)
	require.Contains(t, res.Output, "目标延续")
	require.NotContains(t, res.Output, "下一页")
}

func TestReadDocumentPageDoesNotReturnTrailingBoundaryMarkerAsContent(t *testing.T) {
	tool, repo := newReadDocumentFixture(3)
	repo.ordered[0].Content = "<!-- Slide number: 3 -->\nPrevious page text.\n<!-- Slide number: 4 -->"
	repo.ordered[1].Content = "Requested page text."
	repo.ordered[2].Content = "<!-- Slide number: 5 -->\nNext page text."
	repo.ordered[0].SourceLocators = types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: 3, Mapping: "exact", Partial: true, Quote: "Previous page text."}}
	repo.ordered[1].SourceLocators = types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: 4, Mapping: "exact", Quote: "Requested page text."}}
	repo.ordered[2].SourceLocators = types.SourceLocators{{Type: types.SourceLocatorSlide, Slide: 5, Mapping: "exact", Quote: "Next page text."}}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":4,"limit":20}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, []string{"chunk-1"}, chunkIDsFromData(t, res.Data))
	require.Equal(t, 1, res.Data["page_chunks"])
	require.Equal(t, true, res.Data["page_complete"])
	require.NotContains(t, res.Output, "Slide number: 4")
	require.NotContains(t, res.Output, "Previous page text.")
	require.NotContains(t, res.Output, "Next page text.")
	require.Contains(t, res.Output, "Requested page text.")

	row := res.Data["chunks"].([]map[string]interface{})[0]
	require.Equal(t, "chunk-1", row["chunk_id"])
	require.Equal(t, 4, row["page"])
	require.Equal(t, 4, row["source_locators"].(types.SourceLocators)[0].Slide)
}

func TestReadDocumentPageRetainsMarkdownHeadingTitle(t *testing.T) {
	tool, repo := newReadDocumentFixture(3)
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\nEarlier page."
	repo.ordered[1].Content = "# Page 2 Actual source title\nPage body."
	repo.ordered[2].Content = "<!-- Slide number: 3 -->\nLater page."

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","page":2,"limit":20}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, []string{"chunk-1"}, chunkIDsFromData(t, res.Data))
	require.Contains(t, res.Output, "# Page 2 Actual source title")
	require.Contains(t, res.Output, "Page body.")
	require.NotContains(t, res.Output, "Earlier page.")
	require.NotContains(t, res.Output, "Later page.")
	require.Equal(t, true, res.Data["page_complete"])
}

func TestReadDocumentPageBudgetCanContinueToLateChartTableAndImage(t *testing.T) {
	tool, repo := newReadDocumentFixture(4)
	repo.ordered[0].Content = "<!-- Slide number: 11 -->\n上一页。"
	repo.ordered[1].Content = "<!-- Slide number: 12 -->\n# 第12页\n" + strings.Repeat("该页说明", 150)
	repo.ordered[2].Content = "| 原图图例 | 曲线RGB颜色 | 最右端约值 |\n| 2401 | #FF0000 | 250 |\n![第12页](target.png)"
	repo.ordered[2].ImageInfo = `[{"url":"target.png","caption":"本页图表","ocr_text":"参见第13页的说明"}]`
	repo.ordered[3].Content = "<!-- Slide number: 13 -->\n不应混入的其他图。"
	ctx := WithOutputBudget(context.Background(), 1000)
	first, err := tool.Execute(ctx, json.RawMessage(`{"id":"doc-1","page":12}`))
	require.NoError(t, err)
	require.Equal(t, 1, first.Data["next_offset"])
	require.Equal(t, false, first.Data["page_complete"])
	require.Equal(t, true, first.Data["truncated"])
	next, err := tool.Execute(ctx, json.RawMessage(`{"id":"doc-1","page":12,"offset":1}`))
	require.NoError(t, err)
	require.Contains(t, next.Output, "#FF0000")
	require.Contains(t, next.Output, "target.png")
	require.NotContains(t, next.Output, "不应混入")
	require.Equal(t, 12, next.Data["page"])
	require.Equal(t, 12, next.Data["chunks"].([]map[string]interface{})[0]["page"])
	require.Equal(t, true, next.Data["document_scope"].(map[string]interface{})["has_visual_evidence"])
}

func TestModelPlannedScopeRejectsForeignDocumentAndInvalidDateProof(t *testing.T) {
	tool, state := reportScopeFixture(t, "棉花周报20240310.pptx", "棉花周报20250310.pptx")
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"report-b"}`))
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Contains(t, res.Error, "另一报告")
	_, err = state.bind(context.Background(), DocumentScopePlan{Reason: "model provided wrong date", Sources: []DocumentSourceSelection{{KnowledgeID: "report-b", FileName: "棉花周报20250310.pptx", Date: &DocumentDateEvidence{Date: "2024-03-10", SourceField: "file_name", SourceText: "20240310"}}}}, tool.knowledgeService, tool.searchTargets)
	require.Error(t, err)
	require.Contains(t, err.Error(), "日期")
	_, err = state.selection("report-a")
	require.NoError(t, err, "invalid plans cannot replace a previously verified source")
	// Comparison is an explicit model plan; no comparison keyword is inspected.
	_, err = state.bind(context.Background(), DocumentScopePlan{Reason: "user explicitly requests comparing both sources", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "棉花周报20240310.pptx"}, {KnowledgeID: "report-b", FileName: "棉花周报20250310.pptx"}}}, tool.knowledgeService, tool.searchTargets)
	require.NoError(t, err)
	_, err = state.selection("report-b")
	require.NoError(t, err)
}

func TestRequestedReportIgnoresDeletingDuplicateButRefusesRetiredExplicitSource(t *testing.T) {
	for _, status := range []string{types.ParseStatusDeleting, "deleted", "archived", "soft-deleted"} {
		t.Run(status, func(t *testing.T) {
			tool, constraint := reportScopeFixture(t, "棉花周报20240310(2).pptx", "棉花周报20240310(2).pptx")
			retired := tool.knowledgeService.(*readDocKnowledgeService).docs["report-b"]
			if status == "soft-deleted" {
				retired.DeletedAt.Valid = true
			} else {
				retired.ParseStatus = status
			}
			requested, err := constraint.selection("report-a")
			require.NoError(t, err)
			require.Equal(t, "report-a", requested.KnowledgeID)
			_, err = restrictDocumentTargets(context.Background(), tool.searchTargets, tool.knowledgeService, []string{"report-b"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "删除")
			tool.WithDocumentConstraint(nil)
			res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"report-b","page":12}`))
			require.NoError(t, err)
			require.False(t, res.Success)
			require.Contains(t, res.Error, "删除")
		})
	}
}

func TestSearchKnowledgeBoundPageCannotFallBackToOtherReport(t *testing.T) {
	read, constraint := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	search := NewSearchKnowledgeTool(nil, read.knowledgeService, read.chunkService, read.searchTargets, nil, nil).WithDocumentConstraint(constraint)
	res, err := search.Execute(context.Background(), json.RawMessage(`{"query":"郑棉01-05价差"}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, "report-a", res.Data["knowledge_id"])
	require.NotContains(t, res.Output, "红线2301")
	res, err = search.Execute(context.Background(), json.RawMessage(`{"query":"郑棉01-05价差","document_ids":["doc-1"]}`))
	require.NoError(t, err)
	require.False(t, res.Success)
}

func TestUnboundDocumentScopeNeverGuessesSourceFromQuestionWording(t *testing.T) {
	tool, _ := reportScopeFixture(t, "棉花周报20240310.pptx")
	tool.WithDocumentConstraint(NewDocumentScopeState())
	for _, query := range []string{"不要引用其他报告", "3月10日周报", "compare another report", "any metric"} {
		args, _ := json.Marshal(map[string]any{"id": "report-a", "query": query})
		res, err := tool.Execute(context.Background(), args)
		require.NoError(t, err)
		require.False(t, res.Success)
		require.Contains(t, res.Error, "来源计划")
	}
	plan := DocumentScopePlan{Reason: "the QA model selected the actual source using conversation context", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "棉花周报20240310.pptx", Page: 12}}}
	args, _ := json.Marshal(ReadDocumentInput{ID: "report-a", SourceScope: &plan})
	res, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, true, res.Data["document_scope"].(map[string]any)["plan_validated"])
}

func TestDocumentConstraintDoesNotBroadenExplicitMentionScope(t *testing.T) {
	tool, constraint := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", TenantID: 7, KnowledgeIDs: []string{"doc-1"}}}
	_, err := constraint.bind(context.Background(), DocumentScopePlan{Reason: "model-selected unavailable source", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "棉花周报20240310(2).pptx"}}}, tool.knowledgeService, tool.searchTargets)
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope")
}

type foreignDocumentIndex struct {
	interfaces.KnowledgeBaseService
	requested []string
}

func (f *foreignDocumentIndex) GetKnowledgeBasesByIDsOnly(context.Context, []string) ([]*types.KnowledgeBase, error) {
	return []*types.KnowledgeBase{kbWithIndexes("kb-1", false, true, "")}, nil
}
func (f *foreignDocumentIndex) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return nil
}
func (f *foreignDocumentIndex) HybridSearch(_ context.Context, _ string, params types.SearchParams) ([]*types.SearchResult, error) {
	f.requested = params.KnowledgeIDs
	return []*types.SearchResult{{ID: "foreign-chart", KnowledgeID: "doc-1", KnowledgeBaseID: "kb-1", Content: "郑棉01-05价差 红线2301", Score: 0.99}}, nil
}

func TestSearchKnowledgeDropsForeignResultEvenWhenIndexIgnoresDocumentFilter(t *testing.T) {
	read, constraint := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	_, err := constraint.bind(context.Background(), DocumentScopePlan{Reason: "model selects complete document", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "棉花周报20240310(2).pptx"}}}, read.knowledgeService, read.searchTargets)
	require.NoError(t, err)
	index := &foreignDocumentIndex{}
	search := NewSearchKnowledgeTool(index, read.knowledgeService, nil, read.searchTargets, nil, nil).WithDocumentConstraint(constraint)
	res, err := search.Execute(context.Background(), json.RawMessage(`{"query":"郑棉01-05价差","mode":"keyword"}`))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, []string{"report-a"}, index.requested)
	require.Equal(t, 0, res.Data["count"])
	require.NotContains(t, res.Output, "红线2301")
	require.NotContains(t, res.Data, "document_scope")
}

func TestPlannedDateEvidenceUsesActualMetadata(t *testing.T) {
	k := &types.Knowledge{Title: "真实文件", Metadata: types.JSON(`{"report_date":"2024-03-10"}`)}
	require.NoError(t, validateDocumentDateEvidence(k, &DocumentDateEvidence{Date: "2024-03-10", SourceField: "metadata.report_date", SourceText: "2024-03-10"}))
	require.Error(t, validateDocumentDateEvidence(k, &DocumentDateEvidence{Date: "2025-03-10", SourceField: "metadata.report_date", SourceText: "2025-03-10"}))
	require.Error(t, validateDocumentDateEvidence(k, &DocumentDateEvidence{Date: "2024-03-10", SourceField: "uploaded_at", SourceText: "2024-03-10"}))
}

func TestScopePlanRejectsIDOnlyFalseIdentityAndGuessedDate(t *testing.T) {
	read, _ := reportScopeFixture(t, "source20240310.pptx")
	state := NewDocumentScopeState()
	for _, selection := range []DocumentSourceSelection{
		{KnowledgeID: "report-a"},
		{KnowledgeID: "report-a", FileName: "different.pptx"},
		{KnowledgeID: "report-a", Title: "different title"},
		{KnowledgeID: "report-a", FileName: "source20240310.pptx", Date: &DocumentDateEvidence{Date: "2024-05-09", SourceField: "file_name", SourceText: "20240509"}},
	} {
		_, err := state.bind(context.Background(), DocumentScopePlan{Reason: "model candidate", Sources: []DocumentSourceSelection{selection}}, read.knowledgeService, read.searchTargets)
		require.Error(t, err)
		require.Empty(t, state.selectedIDs())
	}
}

func TestUnboundSearchProvidesMetadataOnlyThenExplicitPlanReadsActualPage(t *testing.T) {
	read, _ := reportScopeFixture(t, "source20240310.pptx")
	index := &foreignDocumentIndex{}
	search := NewSearchKnowledgeTool(index, read.knowledgeService, read.chunkService, read.searchTargets, nil, nil)
	registry := NewToolRegistry()
	registry.RegisterTool(search)
	registry.RegisterTool(read)
	PrepareDocumentScope(registry)
	// The index finds a different authorized candidate with a similar chart.
	res, err := search.Execute(context.Background(), json.RawMessage(`{"query":"any report chart","mode":"keyword"}`))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, "document_info", res.Data["display_type"])
	require.Equal(t, true, res.Data["requires_source_scope"])
	require.NotContains(t, res.Output, "红线2301")
	require.NotContains(t, res.Data, "chunks")
	require.NotContains(t, res.Data, "results")
	res, err = read.Execute(context.Background(), json.RawMessage(`{"id":"report-a","page":12}`))
	require.NoError(t, err)
	require.False(t, res.Success, "ID/page alone cannot bypass a required source plan")
	plan := DocumentScopePlan{Reason: "QA model selects requested source and physical page from authorized metadata", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "source20240310.pptx", Page: 12, Date: &DocumentDateEvidence{Date: "2024-03-10", SourceField: "file_name", SourceText: "20240310"}}}}
	args, _ := json.Marshal(ReadDocumentInput{ID: "report-a", SourceScope: &plan})
	res, err = registry.ExecuteTool(context.Background(), ToolReadDocument, args)
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, 12, res.Data["page"])
	require.NotContains(t, res.Output, "红线2301")
	res, err = search.Execute(context.Background(), json.RawMessage(`{"query":"any report chart","document_ids":["doc-1"]}`))
	require.NoError(t, err)
	require.False(t, res.Success, "the shared boundary survives later search calls")
}

func TestVerifiedScopeIsPerTurnAndSupportsExplicitComparativePlans(t *testing.T) {
	read, _ := reportScopeFixture(t, "first20240310.pptx", "second20250310.pptx")
	registry := NewToolRegistry()
	registry.RegisterTool(read)
	PrepareDocumentScope(registry)
	tool, err := registry.GetTool(ToolBindDocumentScope)
	require.NoError(t, err)
	plan := DocumentScopePlan{Reason: "QA model understands an actual multi-source comparison", Sources: []DocumentSourceSelection{{KnowledgeID: "report-a", FileName: "first20240310.pptx", Page: 12}, {KnowledgeID: "report-b", FileName: "second20250310.pptx", Page: 12}}}
	args, _ := json.Marshal(plan)
	res, err := registry.ExecuteTool(context.Background(), tool.Name(), args)
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Len(t, res.Data["documents"], 2)
	res, err = read.Execute(context.Background(), json.RawMessage(`{"id":"report-a"}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	PrepareDocumentScope(registry)
	res, err = read.Execute(context.Background(), json.RawMessage(`{"id":"report-a"}`))
	require.NoError(t, err)
	require.False(t, res.Success, "a new turn cannot inherit a previous source plan")
}

func TestDocumentScopePlanRejectsUnknownFieldsEvenWithoutRegistryValidation(t *testing.T) {
	for _, encoded := range []string{
		`{"sources":[],"selection_reason":"model","tenant_id":999}`,
		`{"sources":[{"knowledge_id":"real","expected_title":"title","user_id":"other"}],"selection_reason":"model"}`,
		`{"sources":[{"knowledge_id":"real","expected_title":"title","date_evidence":{"date":"2024-03-10","source_field":"title","source_text":"20240310","override":true}}],"selection_reason":"model"}`,
	} {
		var plan DocumentScopePlan
		require.Error(t, json.Unmarshal([]byte(encoded), &plan))
	}
}
