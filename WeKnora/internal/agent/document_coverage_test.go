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

type comparisonChunks struct {
	interfaces.ChunkRepository
	rows map[string][]*types.Chunk
}

func (c *comparisonChunks) ListPagedChunksByKnowledgeID(_ context.Context, tenant uint64, id string, pagination *types.Pagination, _ []types.ChunkType, _ []string, _ string, _ string, _ string, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	if tenant != 7 {
		return nil, 0, fmt.Errorf("unexpected tenant")
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

func comparisonCoverageFixture(t *testing.T, model *mockChat) (*AgentEngine, *types.AgentState, context.Context) {
	t.Helper()
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "comparison-reader", Role: types.TenantRoleViewer})
	catalog := &comparisonCatalog{documents: map[string]*types.Knowledge{}}
	repo := &comparisonChunks{rows: map[string][]*types.Chunk{}}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault = true
	engine.config.AnswerReviewMode = "standard"
	engine.taskPlan = explicitTextDeliveryPlan()
	engine.taskPlan.CoverageTargets = []taskCoverageTarget{{GoalIndex: 0, RequestBasis: comparisonQuestion, Criteria: "各期月度数据及口径变化", SourceScope: "相关周报", ByDocument: true}}
	engine.toolRegistry = agenttools.NewToolRegistry()
	chunks := &comparisonChunkService{repository: repo}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "comparison-library", TenantID: 7}}
	engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(catalog, chunks, targets))
	engine.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, catalog, chunks, targets, nil, nil))
	engine.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(catalog, targets))
	agenttools.PrepareDocumentScope(engine.toolRegistry, comparisonQuestion)
	agenttools.SetDocumentCoveragePolicy(engine.toolRegistry, engine.plansCoverageByDocument())
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
		{"week-c", "检修设备周报", "2025-06-15", []int{3}, []string{"月度开机率（计划工时）｜54.5%"}},
		{"week-b", "现场设备周报", "2025-06-08", []int{9}, []string{"月度开机率（计划工时）｜56.5%"}},
	} {
		filename := report.title + report.date + ".pdf"
		catalog.documents[report.id] = &types.Knowledge{ID: report.id, Title: report.title, FileName: filename, FileType: "pdf", KnowledgeBaseID: "comparison-library", TenantID: 7, ParseStatus: types.ParseStatusCompleted}
		for index, body := range report.bodies {
			repo.rows[report.id] = append(repo.rows[report.id], &types.Chunk{
				ID: fmt.Sprintf("%s-paragraph-%d", report.id, index), KnowledgeID: report.id, KnowledgeBaseID: "comparison-library",
				TenantID: 7, IsEnabled: true, Content: body, ChunkType: types.ChunkTypeText, ChunkIndex: index, ContentRevision: i + 1,
				SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Mapping: "exact", Page: report.pages[index]}},
			})
		}
		plan.Sources = append(plan.Sources, agenttools.DocumentSourceSelection{KnowledgeID: report.id, Title: report.title,
			FileName: filename, Date: &agenttools.DocumentDateEvidence{Date: report.date, SourceField: "file_name", SourceText: report.date}})
		navigation = append(navigation, map[string]any{"knowledge_id": report.id, "title": report.title})
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

func TestDocumentCoverageReviewSeesUnreadVersionAndRecoversOnce(t *testing.T) {
	model := &mockChat{responses: []mockResponse{discoveryReply("recover", "目前只读到年度口径。补查其他周报中的月度表头及实际同口径数据。")}}
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

	original := candidateDecision{Disposition: candidateAccepted, Candidate: "年度表口径已查清。", CandidateHash: "candidate", TaskHash: "task", EvidenceHash: "source", SourceScopeHash: "scope"}
	decision := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, original, state, conversation)
	require.Equal(t, candidateNeedSource, decision.Disposition)
	require.Contains(t, model.calls[0][1].Content, "actual_document_coverage")
	require.Contains(t, model.calls[0][1].Content, "新增周报，可能含新口径")
	require.Contains(t, model.calls[0][1].Content, "directory_continuations")
	require.Contains(t, model.calls[0][1].Content, "年度开机率")
	require.Contains(t, model.calls[0][0].Content, "matching table definitions")

	engine.resumeEvidenceCollection(ctx, state, &conversation, &loopGuards{}, "session")
	require.True(t, engine.turnReview.DocumentCoverageRecoveryUsed)
	repeated := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, original, state, conversation)
	require.Equal(t, candidateRevise, repeated.Disposition)
	require.Contains(t, engine.answerRepairAdvice(), "本轮检索未返回相关内容")
	require.Len(t, model.calls, 1)
}

func TestDocumentCoverageReviewRetainsUncitedBodiesAndTheirOwnPages(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		discoveryReply("keep", ""),
		discoveryReply("recover", "旧口径与新版分开核对，不要把取整表述和表内精确值混为同一表。"),
	}}
	engine, state, ctx := comparisonCoverageFixture(t, model)
	conversation := appendComparisonReads(t, engine, state, ctx, "week-c", "week-a", "week-d", "week-b")
	view := engine.documentCoverage(state)
	require.Empty(t, view.gapNotice())
	require.Equal(t, []int{8}, view.Bound[0].Evidence[0].Pages)
	require.Equal(t, []int{11}, view.Bound[0].Evidence[1].Pages)
	require.Equal(t, 1, view.Bound[0].Evidence[0].Revision)
	decision := candidateDecision{Disposition: candidateAccepted, TaskHash: "task", EvidenceHash: "unchanged-evidence", SourceScopeHash: "scope"}
	progress := engine.checkDiscoveryDeliveryPhase(ctx, comparisonQuestion, decision, state, conversation, "progress", map[string]any{"source_batches": 4})
	require.Equal(t, candidateAccepted, progress.Disposition)
	budget := engine.turnReview.DiscoveryReview.budget
	decision.Candidate = "各份来源都属于同一口径。"
	decision.CandidateHash = "draft"
	delivery := engine.checkDiscoveryOnlyDelivery(ctx, comparisonQuestion, decision, state, conversation)
	require.Equal(t, candidateNeedSource, delivery.Disposition, "all documents returning text does not establish semantic completion")
	require.Same(t, budget, engine.turnReview.DiscoveryReview.budget)
	var input map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(model.calls[1][1].Content), &input))
	bodies := string(input["actual_comparison_body_evidence"])
	for _, want := range []string{"约为四成", "41.25%", "全年计划工时", "54.5%", "56.5%", `"page":8`, `"page":11`} {
		require.Contains(t, bodies, want)
	}
	require.Contains(t, string(input["actual_document_coverage"]), `"verified_date_evidence"`)
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
		discoveryReply("keep", ""),
	}}
	engine, state, ctx := comparisonCoverageFixture(t, model)
	engine.taskPlan.DeliveryRequirements[0].Format = "prose"
	conversation := appendComparisonReads(t, engine, state, ctx, "week-a")
	require.NoError(t, engine.streamFinalAnswerToEventBus(ctx, comparisonQuestion, state, "session", conversation))
	require.True(t, state.IsComplete)
	require.Contains(t, state.FinalAnswer, "已读的班次设备周报展示了年度开机率")
	require.Contains(t, state.FinalAnswer, "《雨天设备周报》尚未读到正文")
	require.Contains(t, state.FinalAnswer, "《检修设备周报》尚未读到正文")
	require.Contains(t, state.FinalAnswer, "《现场设备周报》尚未读到正文")
}
