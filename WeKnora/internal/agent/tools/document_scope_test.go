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
	constraint := NewRequestedDocumentConstraint(`3月10日周报第12页"郑棉01-05价差"图表中，红色线代表哪个合约年度？`)
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

func TestRequestedReportRefusesForeignDocumentAndAmbiguousYears(t *testing.T) {
	tool, _ := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1"}`))
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Contains(t, res.Error, "不能用本文件")
	tool, _ = reportScopeFixture(t, "棉花周报20240310.pptx", "棉花周报20250310.pptx")
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"report-a"}`))
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Contains(t, res.Error, "2 个候选")
	tool.WithDocumentConstraint(NewRequestedDocumentConstraint("2024年3月10日周报第12页"))
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"report-a"}`))
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
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
			requested, err := constraint.resolve(context.Background(), tool.knowledgeService, tool.searchTargets)
			require.NoError(t, err)
			require.Equal(t, "report-a", requested.ID)
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

func TestDocumentSelectorDoesNotConstrainGenericDataOrComparativeReports(t *testing.T) {
	for _, query := range []string{"20天原料多少钱", "3月10日棉花价格", "3月10日周报与其他报告相比如何"} {
		require.Nil(t, NewRequestedDocumentConstraint(query), query)
	}
	r := NewRequestedDocumentConstraint(`请看“棉花周报20240310(2).pptx”第12页`)
	require.NotNil(t, r)
	require.Equal(t, "棉花周报20240310(2).pptx", r.FileName)
}

func TestDocumentConstraintDoesNotBroadenExplicitMentionScope(t *testing.T) {
	tool, constraint := reportScopeFixture(t, "棉花周报20240310(2).pptx")
	tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", TenantID: 7, KnowledgeIDs: []string{"doc-1"}}}
	_, err := constraint.resolve(context.Background(), tool.knowledgeService, tool.searchTargets)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "未找到"))
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
	constraint.Page = 0
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

func TestRequestedReportDateSupportsMetadataAndISO(t *testing.T) {
	r := NewRequestedDocumentConstraint("2024-3-10周报第12页")
	require.NotNil(t, r)
	k := &types.Knowledge{Title: "棉花周报", Metadata: types.JSON(`{"report_date":"2024-03-10"}`)}
	require.True(t, r.matches(k))
	k.Metadata = types.JSON(`{"report_date":"2025-03-10"}`)
	require.False(t, r.matches(k))
}
