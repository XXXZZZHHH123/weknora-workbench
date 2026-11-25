package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/modelcontext"
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

func TestDocumentNavigationGuidesTargetedReadOfLaterRevision(t *testing.T) {
	ctx := t.Context()
	read, repo := newReadDocumentFixture(3)
	service := read.knowledgeService.(*readDocKnowledgeService)
	early := &types.Knowledge{ID: "earlier", FileName: "运量简报-第一期.pdf", TenantID: 7, KnowledgeBaseID: "kb-1"}
	later := service.docs["doc-1"]
	later.Title, later.FileName = "运量简报-第三期", "运量简报-第三期.pdf"
	service.docs[early.ID] = early
	const revision = "将运量预测从80上调到90。"
	repo.ordered[0].Content = "<!-- PDF page 1 -->\n报告前言。"
	repo.ordered[1].Content = "<!-- PDF page 2 -->\n本次修订：" + revision
	repo.ordered[2].Content = "<!-- PDF page 3 -->\n另一期的无关附件。"
	state := NewDocumentScopeState()
	read.WithDocumentConstraint(state)
	search := NewSearchKnowledgeTool(nil, service, nil, read.searchTargets, nil, nil).WithDocumentConstraint(state)
	res := search.documentCandidates(ctx, []*searchResultWithMeta{
		{SearchResult: &types.SearchResult{KnowledgeID: early.ID, Content: "维持原运量预测。"}},
		{SearchResult: &types.SearchResult{KnowledgeID: later.ID, Content: revision, SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 2, Mapping: "exact"}}}},
	}, "预测后来有调整吗？")
	prompt := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolSearchKnowledge, res)
	require.Contains(t, prompt, revision)
	require.Contains(t, prompt, "不可直接引用、计算或认定图表数值")
	require.Contains(t, prompt, "已看到的较晚期摘录提示有数字调整等更新，且这些更新会改变准备写入回答的结论")
	require.Contains(t, prompt, "在用户允许的范围内一次绑定所需的前后期来源，按页码或相关内容定向补读正文或原图")
	require.Contains(t, prompt, "只问普通定义或术语时，直接解释概念，无需逐期查找材料")
	require.Empty(t, state.selectedIDs())

	plan := DocumentScopePlan{Reason: "核对较晚期的运量预测是否有调整", Sources: []DocumentSourceSelection{
		{KnowledgeID: early.ID, FileName: early.FileName},
		{KnowledgeID: later.ID, FileName: later.FileName, Page: 2},
	}}
	args, err := json.Marshal(ReadDocumentInput{ID: later.ID, Page: 2, SourceScope: &plan})
	require.NoError(t, err)
	res, err = read.Execute(ctx, args)
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Equal(t, []string{early.ID, later.ID}, state.selectedIDs())
	require.Equal(t, 2, res.Data["page"])
	require.Contains(t, res.Output, revision)
	require.NotContains(t, res.Output, "无关附件")
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
