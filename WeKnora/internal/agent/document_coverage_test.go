package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type comparisonCatalog struct {
	interfaces.KnowledgeService
	documents map[string]*types.Knowledge
}

func (c *comparisonCatalog) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if doc := c.documents[id]; doc != nil {
		return doc, nil
	}
	return nil, fmt.Errorf("document unavailable")
}

type comparisonReadTimeoutKey struct{}

type comparisonChunks struct {
	interfaces.ChunkRepository
	rows map[string][]*types.Chunk
}

func (c *comparisonChunks) ListPagedChunksByKnowledgeID(ctx context.Context, tenant uint64, id string, pagination *types.Pagination, _ []types.ChunkType, _ []string, _ string, _ string, _ string, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	if tenant != 7 {
		return nil, 0, fmt.Errorf("unexpected tenant")
	}
	if ctx.Value(comparisonReadTimeoutKey{}) == id {
		return nil, 0, context.DeadlineExceeded
	}
	rows := c.rows[id]
	first := min((pagination.Page-1)*pagination.PageSize, len(rows))
	return rows[first:min(first+pagination.PageSize, len(rows))], int64(len(rows)), nil
}

type comparisonChunkService struct {
	interfaces.ChunkService
	repository *comparisonChunks
}

func (c *comparisonChunkService) GetRepository() interfaces.ChunkRepository { return c.repository }

func (*comparisonChunks) ListChunksByParentIDs(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return nil, nil
}

const comparisonQuestion = "逐份比较设备周报中的月度开机率，并说明口径变化。"

func comparisonCoverageFixture(t *testing.T, model *mockChat, locations ...agenttools.DocumentRequiredLocation) (*AgentEngine, *types.AgentState, context.Context) {
	t.Helper()
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "comparison-reader", Role: types.TenantRoleViewer})
	catalog := &comparisonCatalog{documents: map[string]*types.Knowledge{}}
	repo := &comparisonChunks{rows: map[string][]*types.Chunk{}}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault = true
	engine.config.AnswerReviewMode = "standard"
	engine.taskPlan = explicitTextDeliveryPlan()
	engine.taskPlan.CoverageTargets = []taskCoverageTarget{{GoalIndex: 0, RequestBasis: comparisonQuestion, Criteria: "各期月度数据及口径变化", SourceScope: "相关周报", ByDocument: true, RequiredLocations: locations}}
	engine.toolRegistry = agenttools.NewToolRegistry()
	chunks := &comparisonChunkService{repository: repo}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "comparison-library", TenantID: 7}}
	engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(catalog, chunks, targets))
	engine.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, catalog, chunks, targets, nil, nil))
	engine.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(catalog, targets))
	agenttools.PrepareDocumentScope(engine.toolRegistry, comparisonQuestion)
	engine.setDocumentCoverageTargets()
	binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	plan := agenttools.DocumentScopePlan{Reason: "按同一指标核对各周报告"}
	var navigation []map[string]any
	for i, report := range []struct {
		id, title, date string
		pages           []int
		bodies          []string
	}{
		{"week-d", "雨天设备周报", "2025-06-22", []int{8, 11}, []string{"月度开机率约为四成，口径已调整。", "月度开机率（按本月有效运行时间）｜41.25%"}},
		{"week-a", "班次设备周报", "2025-06-01", []int{2}, []string{"年度开机率（全年计划工时）｜78.6%"}},
		{"week-c", "检修设备周报", "2025-06-15", []int{3}, []string{"六月第三周：生产线已按计划复工。月度开机率（计划工时）｜54.5%"}},
		{"week-b", "现场设备周报", "2025-06-08", []int{9}, []string{"六月第二周：生产线已按计划复工。月度开机率（计划工时）｜56.5%"}},
	} {
		filename := report.title + report.date + ".pdf"
		if len(locations) > 0 && (report.id == "week-d" || report.id == "week-a") {
			report.title, report.date = "设备月度报告", "2025-06-30"
			filename = report.title + report.date + "（初版）.pdf"
			if report.id == "week-d" {
				filename = report.title + report.date + "（修订版）.pdf"
			}
		}
		catalog.documents[report.id] = &types.Knowledge{ID: report.id, Title: report.title, FileName: filename, FileType: "pdf", KnowledgeBaseID: "comparison-library", TenantID: 7, ParseStatus: types.ParseStatusCompleted}
		for index, body := range report.bodies {
			if len(locations) > 0 {
				body = fmt.Sprintf("# Page %d\n%s", report.pages[index], body)
			}
			repo.rows[report.id] = append(repo.rows[report.id], &types.Chunk{
				ID: fmt.Sprintf("%s-paragraph-%d", report.id, index), KnowledgeID: report.id, KnowledgeBaseID: "comparison-library",
				TenantID: 7, IsEnabled: true, Content: body, ChunkType: types.ChunkTypeText, ChunkIndex: index, ContentRevision: i + 1,
				SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Mapping: "exact", Page: report.pages[index]}},
			})
		}
		binding := agenttools.DocumentCoverageBinding{Target: 0}
		if len(locations) == 0 || report.id == "week-d" || report.id == "week-a" {
			bindings := []agenttools.DocumentCoverageBinding{binding}
			if len(locations) > 0 {
				index := 0
				bindings[0].Location = &index
				bindings[0].Pages = []int{11}
			} else if report.id == "week-b" {
				bindings = nil
			}
			plan.Sources = append(plan.Sources, agenttools.DocumentSourceSelection{KnowledgeID: report.id, Title: report.title,
				FileName: filename, Coverage: bindings, Date: &agenttools.DocumentDateEvidence{Date: report.date, SourceField: "file_name", SourceText: report.date}})
		}
		navigation = append(navigation, map[string]any{"knowledge_id": report.id, "title": report.title, "file_name": filename, "knowledge_base_id": "comparison-library",
			"navigation_matches": []map[string]any{{"page_hint": report.pages[len(report.pages)-1], "excerpt": report.bodies[len(report.bodies)-1]}}})
	}
	navigation = append(navigation, map[string]any{"knowledge_id": "week-e", "title": "新增周报，可能含新口径"})
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	binding, err := binder.Execute(ctx, raw)
	require.NoError(t, err)
	require.True(t, binding.Success, binding.Error)
	directory := navigationCall(agenttools.ToolListDocuments, map[string]any{"documents": navigation, "knowledge_base_id": "comparison-library", "next_page": 2})
	return engine, &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{directory}}}}, ctx
}

func appendComparisonReads(t *testing.T, engine *AgentEngine, state *types.AgentState, ctx context.Context, documents ...string) []chat.Message {
	t.Helper()
	reader, err := engine.toolRegistry.GetTool(agenttools.ToolReadDocument)
	require.NoError(t, err)
	var messages []chat.Message
	for _, id := range documents {
		raw := json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))
		result, err := reader.Execute(ctx, raw)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		step := types.AgentStep{ToolCalls: []types.ToolCall{{ID: "read-" + id, Name: agenttools.ToolReadDocument, Result: result}}}
		state.RoundSteps = append(state.RoundSteps, step)
		messages = engine.appendToolResults(messages, step)
	}
	return messages
}

func TestDocumentCoverageReviewTargetsMonthlyTableAndRecoversOnce(t *testing.T) {
	model := &mockChat{responses: []mockResponse{chatAnswer(`{"action":"recover","feedback":"班次设备周报查到的是年度开机率，月度开机率还没核实到。请核对雨天设备周报的月度表。","checks":[{"claim":"月度开机率为78.6%","ref":1,"quote":"年度开机率（全年计划工时）","period":"年度","finding":"conflicts"}],"missing":{"knowledge_id":"week-d","page":11,"query":""}}`)}}
	engine, state, ctx := comparisonCoverageFixture(t, model)
	conversation := appendComparisonReads(t, engine, state, ctx, "week-a")
	search := navigationCall(agenttools.ToolSearchKnowledge, map[string]any{"document_coverage": []map[string]any{
		{"knowledge_id": "week-b", "status": "no_match"},
		{"knowledge_id": "week-c", "status": "failed"},
		{"knowledge_id": "week-d", "status": "budget_truncated"},
	}})
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{search}})
	view := engine.documentCoverage(state)
	require.Equal(t, []int{2}, view.Bound[1].Evidence[0].Pages)
	require.Equal(t, "budget_truncated", view.Bound[0].Status)
	require.Equal(t, "failed", view.Bound[2].Status)
	require.Equal(t, "no_match", view.Bound[3].Status)
	require.Contains(t, view.gapNotice(), "《检修设备周报》本轮读取失败")
	require.Contains(t, view.gapNotice(), "《雨天设备周报》因本轮读取或展示限额未取得完整结果")

	original := candidateDecision{Disposition: candidateAccepted, Candidate: "班次设备周报显示月度开机率为78.6%。", CandidateHash: "candidate", TaskHash: "task", EvidenceHash: "source", SourceScopeHash: "scope"}
	decision := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, original, state, conversation)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	require.Contains(t, model.calls[0][1].Content, "actual_document_coverage")
	require.Contains(t, model.calls[0][1].Content, "新增周报，可能含新口径")
	require.Contains(t, model.calls[0][1].Content, "directory_continuations")
	require.Contains(t, model.calls[0][1].Content, "年度开机率")
	require.Contains(t, model.calls[0][0].Content, "matching table definitions")
	require.Equal(t, "week-d", engine.turnReview.DiscoveryReview.targetedRead.ID)
	require.Equal(t, 11, engine.turnReview.DiscoveryReview.targetedRead.Page)
	read, err := engine.boundedCoverageRead(ctx, comparisonQuestion, state, "session", "message", conversation, decision, false)
	require.NoError(t, err)
	require.NotNil(t, read)
	require.Len(t, read.ToolCalls, 1)
	require.Equal(t, "week-d", read.ToolCalls[0].ExecutionArgs()["id"])
	require.EqualValues(t, 11, read.ToolCalls[0].ExecutionArgs()["page"])
	require.True(t, engine.turnReview.DocumentCoverageRecoveryUsed)
	repeated := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, original, state, conversation)
	require.Equal(t, candidateRevise, repeated.Disposition)
	require.Contains(t, engine.answerRepairAdvice(), "月度开机率还没核实到")
	require.Len(t, model.calls, 1)
}

func TestDocumentCoverageReviewRetainsUncitedBodiesAndTheirOwnPages(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		discoveryReply("keep", ""),
		chatAnswer(`{"action":"revise","feedback":"六月第二周也有同一句复工表述，应和第三周放在一起。","checks":[{"claim":"只有六月第三周提到按计划复工","ref":5,"quote":"生产线已按计划复工。","period":"六月第二周","finding":"conflicts"}],"missing":null}`),
	}}
	engine, state, ctx := comparisonCoverageFixture(t, model)
	conversation := appendComparisonReads(t, engine, state, ctx, "week-c", "week-a", "week-d", "week-b")
	view := engine.documentCoverage(state)
	require.Empty(t, view.gapNotice())
	require.Equal(t, []int{8}, view.Bound[0].Evidence[0].Pages)
	require.Equal(t, []int{11}, view.Bound[0].Evidence[1].Pages)
	require.Equal(t, 1, view.Bound[0].Evidence[0].Revision)
	require.True(t, view.Bound[3].AssociationPending)
	require.Equal(t, "body_returned", view.Bound[3].Status)
	decision := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", EvidenceHash: "unchanged-evidence", SourceScopeHash: "scope"}
	progress := engine.checkDiscoveryDeliveryPhase(ctx, comparisonQuestion, decision, state, conversation, "progress", map[string]any{"source_batches": 4})
	require.Equal(t, candidateAccepted, progress.Disposition)
	budget := engine.turnReview.DiscoveryReview.budget
	decision.Candidate = "只有六月第三周提到按计划复工。"
	decision.CandidateHash = "draft"
	delivery := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, decision, state, conversation)
	require.Equal(t, candidateRevise, delivery.Disposition, "the returned wording supports regrouping without another read")
	require.Nil(t, engine.turnReview.DiscoveryReview.targetedRead)
	require.Contains(t, engine.answerRepairAdvice(), "原文“生产线已按计划复工。”")
	require.Same(t, budget, engine.turnReview.DiscoveryReview.budget)
	var input map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(model.calls[1][1].Content), &input))
	bodies := string(input["actual_comparison_body_evidence"])
	for _, want := range []string{"约为四成", "41.25%", "全年计划工时", "54.5%", "56.5%", `"page":8`, `"page":11`} {
		require.Contains(t, bodies, want)
	}
	require.Contains(t, string(input["actual_document_coverage"]), `"verified_date_evidence"`)
	require.Contains(t, string(input["actual_document_coverage"]), `"coverage_association_pending":true`)
}

func TestDocumentCoverageReviewBindsOnlyMissingSameDayVersion(t *testing.T) {
	model := &mockChat{responses: []mockResponse{chatAnswer(`{"action":"recover","feedback":"同日初版还没核对。请核实它的表头和数据期。","checks":[],"missing":{"knowledge_id":"week-a","page":2,"query":""}}`)}}
	e, state, ctx := comparisonCoverageFixture(t, model, agenttools.DocumentRequiredLocation{Period: "六月", Fact: "开机率"})
	original := agenttools.BoundDocumentSelections(e.toolRegistry)[0]
	original.Coverage = nil
	e.taskPlan.CoverageTargets = nil
	e.setDocumentCoverageTargets()
	binder, err := e.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	args, err := json.Marshal(agenttools.DocumentScopePlan{Sources: []agenttools.DocumentSourceSelection{original}, Reason: "先核对修订版"})
	require.NoError(t, err)
	result, err := binder.Execute(ctx, args)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	conversation := appendComparisonReads(t, e, state, ctx, "week-d")
	candidate := candidateDecision{Disposition: candidateAccepted, Candidate: "六月两版采用同一口径。", CandidateHash: "draft", TaskHash: "task"}
	decision := e.checkDiscoveryOnlyDelivery(ctx, "比较同一天初版和修订版的开机率口径。", candidate, state, conversation)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	require.Contains(t, model.calls[0][1].Content, "（初版）.pdf")
	require.Contains(t, model.calls[0][1].Content, "（修订版）.pdf")
	read, err := e.boundedCoverageRead(ctx, comparisonQuestion, state, "session", "message", conversation, decision, false)
	require.NoError(t, err)
	require.NotNil(t, read)
	require.Len(t, read.ToolCalls, 1)
	call := read.ToolCalls[0]
	require.True(t, call.Result.Success, call.Result.Error)
	require.Equal(t, "week-a", call.ExecutionArgs()["id"])
	require.EqualValues(t, 2, call.ExecutionArgs()["page"])
	require.Contains(t, call.Result.Output, "全年计划工时")
	view := e.documentCoverage(state)
	require.Len(t, view.Bound, 2)
	require.Equal(t, "week-d", view.Bound[0].KnowledgeID)
	require.Equal(t, original.Date, view.Bound[0].DateEvidence)
	require.Equal(t, "body_returned", view.Bound[0].Status)
	require.Equal(t, "body_returned", view.Bound[1].Status)
	require.Len(t, model.calls, 1)
}

func TestDocumentCoverageTaskPlanFlagIsOptional(t *testing.T) {
	for _, byDocument := range []bool{true, false} {
		plan := explicitTextDeliveryPlan()
		plan.Goals = []string{"比较各期周报"}
		plan.CompletionCriteria = []string{"逐份核对实际月度定义"}
		plan.Evidence = []string{"各期相关周报原文"}
		plan.ToolNames = []string{}
		plan.SourceDiscovery = &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"knowledge"}, Query: "设备周报的月度开机率"}
		plan.CoverageTargets = []taskCoverageTarget{{GoalIndex: 0, RequestBasis: comparisonQuestion, Criteria: "核对定义与月度原值", SourceScope: "所需的周报", ByDocument: byDocument}}
		raw, err := json.Marshal(plan)
		require.NoError(t, err)
		decoded, err := decodeTaskEvidencePlan(string(raw), nil, 0)
		require.NoError(t, err)
		require.Equal(t, byDocument, decoded.CoverageTargets[0].ByDocument)
		if !byDocument {
			require.NotContains(t, string(raw), "by_document")
		}
	}
}

func TestDocumentCoverageFinalSynthesisIncludesSpecificGaps(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		chatAnswer("已读的班次设备周报展示了年度开机率。"),
		chatAnswer(`{"action":"keep","feedback":"","checks":[],"missing":null}`),
	}}
	engine, state, ctx := comparisonCoverageFixture(t, model)
	engine.taskPlan.DeliveryRequirements[0].Format = "prose"
	conversation := appendComparisonReads(t, engine, state, ctx, "week-a")
	require.NoError(t, engine.streamFinalAnswerToEventBus(ctx, comparisonQuestion, state, "session", conversation))
	require.True(t, state.IsComplete)
	require.Contains(t, state.FinalAnswer, "已读的班次设备周报展示了年度开机率")
	require.Contains(t, state.FinalAnswer, "《雨天设备周报》尚未读到正文")
	require.Contains(t, state.FinalAnswer, "《检修设备周报》尚未读到正文")
	require.NotContains(t, state.FinalAnswer, "《现场设备周报》尚未读到正文")
}

func TestDocumentCoverageKeepsPeriodAndVersionGapsUntilActualPageRead(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "page_read", true: "timeout"}[timedOut], func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{chatAnswer(`{"action":"recover","feedback":"修订版的六月月度开机率还没核实到。","checks":[],"missing":{"knowledge_id":"week-d","page":11,"query":""}}`)}}
			e, state, ctx := comparisonCoverageFixture(t, model,
				agenttools.DocumentRequiredLocation{Period: "六月", Fact: "月度开机率"},
				agenttools.DocumentRequiredLocation{Period: "七月", Fact: "月度开机率"},
			)
			conversation := appendComparisonReads(t, e, state, ctx, "week-a")
			view := e.documentCoverage(state)
			require.Len(t, view.Locations, 3)
			require.Equal(t, "navigation_only", view.Locations[0].Status, "the revised version remains unread")
			require.Equal(t, "navigation_only", view.Locations[1].Status, "the annual table cannot fill page 11")
			require.Equal(t, "source_not_bound", view.Locations[2].Status)
			require.Contains(t, view.gapNotice(), "（初版）.pdf")
			require.Contains(t, view.gapNotice(), "（修订版）.pdf")
			require.Contains(t, view.gapNotice(), "第 11 页")
			require.Contains(t, view.gapNotice(), "七月")
			require.Contains(t, view.gapNotice(), "月度开机率")

			candidate := candidateDecision{Disposition: candidateAccepted, Candidate: "已查看年度表。", TaskHash: "task", CandidateHash: "draft", SourceScopeHash: "scope"}
			decision := e.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, candidate, state, conversation)
			require.Equal(t, candidateNeedSource, decision.Disposition)
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, "年度开机率")
			require.Contains(t, model.calls[0][1].Content, "七月")
			if timedOut {
				ctx = context.WithValue(ctx, comparisonReadTimeoutKey{}, "week-d")
			}
			read, err := e.boundedCoverageRead(ctx, comparisonQuestion, state, "session", "message", conversation, decision, false)
			require.NoError(t, err)
			require.NotNil(t, read)
			require.Len(t, read.ToolCalls, 1)
			for _, call := range read.ToolCalls {
				require.Equal(t, agenttools.ToolReadDocument, call.Name)
				require.EqualValues(t, 11, call.ExecutionArgs()["page"])
				require.NotContains(t, call.ExecutionArgs(), "query")
			}
			conversation = e.appendToolResults(conversation, *read)
			view = e.documentCoverage(state)
			if timedOut {
				require.Equal(t, "failed", view.Locations[0].LastRead)
				require.Contains(t, view.gapNotice(), "（修订版）.pdf")
			} else {
				require.Equal(t, "page_returned_fact_needs_review", view.Locations[0].Status)
				require.NotContains(t, view.gapNotice(), "（修订版）.pdf")
				require.Contains(t, read.ToolCalls[0].Result.Output, "41.25%")
			}
			require.Contains(t, view.gapNotice(), "（初版）.pdf")
			require.Contains(t, view.gapNotice(), "七月")
			require.True(t, e.turnReview.DocumentCoverageRecoveryUsed)
			again, err := e.boundedCoverageRead(ctx, comparisonQuestion, state, "session", "message", conversation, decision, false)
			require.NoError(t, err)
			require.Nil(t, again)
			require.Len(t, state.RoundSteps, 3, "recovery runs only one batch")
		})
	}
}

func TestDocumentCoverageKeepsRequiredPageBeyondRecentReads(t *testing.T) {
	row := &documentCoverageRow{requiredPages: []int{9}}
	row.addEvidence(map[string]any{"chunk_id": "monthly-table", "content_offset_space": "physical_page_chunk_unicode_runes", "page": 9}, "read_body")
	for i := 1; i <= 12; i++ {
		row.addEvidence(map[string]any{"chunk_id": fmt.Sprintf("unrelated-%d", i),
			"content_offset_space": "physical_page_chunk_unicode_runes", "page": i + 20}, "read_body")
	}
	require.Len(t, row.Evidence, 6)
	require.Equal(t, "monthly-table", row.Evidence[0].ChunkID)
}
