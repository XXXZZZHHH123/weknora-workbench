package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type plannedDocumentKnowledge struct {
	interfaces.KnowledgeService
	document *types.Knowledge
}

func (s *plannedDocumentKnowledge) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if id != s.document.ID {
		return nil, fmt.Errorf("not found")
	}
	return s.document, nil
}

type plannedDocumentIndex struct {
	*sourceDiscoveryKnowledgeService
	document *types.Knowledge
}

func (s *plannedDocumentIndex) HybridSearch(_ context.Context, _ string, _ types.SearchParams) ([]*types.SearchResult, error) {
	return []*types.SearchResult{{ID: "actual-page-chunk", KnowledgeID: s.document.ID, KnowledgeBaseID: s.document.KnowledgeBaseID, KnowledgeTitle: s.document.Title,
		Content: "目录匹配正文不可在范围验证前作为答题事实", Score: 0.99}}, nil
}

type plannedDocumentChunks struct {
	interfaces.ChunkService
	repo *plannedDocumentRepo
}

func (s *plannedDocumentChunks) GetRepository() interfaces.ChunkRepository { return s.repo }

type plannedDocumentRepo struct{ interfaces.ChunkRepository }

func (*plannedDocumentRepo) ListPagedChunksByKnowledgeID(_ context.Context, tenant uint64, id string, _ *types.Pagination, _ []types.ChunkType, _ []string, _ string, _ string, _ string, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	if tenant != 7 || id != "actual-document" {
		return nil, 0, fmt.Errorf("wrong scope")
	}
	return []*types.Chunk{{ID: "actual-page-chunk", KnowledgeID: id, KnowledgeBaseID: "granted-base", TenantID: tenant, ChunkIndex: 0, ChunkType: types.ChunkTypeText, IsEnabled: true,
		Content: "<!-- Slide number: 4 -->\n# 第4页\n实际该页的图例：类型甲、类型乙。"}}, 1, nil
}
func (*plannedDocumentRepo) ListChunksByParentIDs(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return nil, nil
}

func TestWorkbenchQAUsesFullContextToSubmitAndVerifySourcePlan(t *testing.T) {
	const filename = "季度资料20250402.pptx"
	document := &types.Knowledge{ID: "actual-document", TenantID: 7, KnowledgeBaseID: "granted-base", Title: "实际季度资料", FileName: filename, FileType: "pptx", ParseStatus: types.ParseStatusCompleted}
	knowledge := &plannedDocumentKnowledge{document: document}
	chunks := &plannedDocumentChunks{repo: &plannedDocumentRepo{}}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-base", TenantID: 7}}
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewSearchKnowledgeTool(&plannedDocumentIndex{sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{}, document: document}, knowledge, chunks, targets, nil, nil))
	registry.RegisterTool(agenttools.NewReadDocumentTool(knowledge, chunks, targets))
	// This is the normal QA model's function call, not a separate classifier or
	// a keyword-driven runtime choice. The plan can accompany the actual read.
	args := `{"id":"d1","source_scope":{"selection_reason":"完整上下文明确指定该资料日期及物理页，禁止与别的报告混用","sources":[{"knowledge_id":"d1","expected_file_name":"` + filename + `","page":4,"date_evidence":{"date":"2025-04-02","source_field":"file_name","source_text":"20250402"}}]}}`
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, ToolCalls: []types.LLMToolCall{{ID: "model-read-plan", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: args}}}, Done: true, FinishReason: "tool_calls"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: `所选资料该页的图例是类型甲和类型乙。<ref id="c1" />`, Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: `所选资料该页的图例是类型甲和类型乙。<ref id="c1" />`, Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	question := "不要比较其他报告。继续回答已指定资料该页的图例。"
	history := []chat.Message{{Role: "user", Content: "来源为2025年4月2日的季度资料，第4页。"}, {Role: "assistant", Content: "接下来仅核对这份资料该页。"}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", question, history)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Len(t, model.calls, 3, "the fetched document gets one semantic source review after the draft")
	first := model.calls[0]
	foundQuestion, foundContext, foundMetadata := false, false, false
	for _, message := range first {
		foundQuestion = foundQuestion || message.Role == "user" && strings.Contains(message.Content, question)
		foundContext = foundContext || message.Content == history[0].Content
		if message.Role == "tool" {
			foundMetadata = foundMetadata || strings.Contains(message.Content, filename)
			require.NotContains(t, message.Content, "目录匹配正文不可")
		}
	}
	require.True(t, foundQuestion)
	require.True(t, foundContext)
	require.True(t, foundMetadata)
	require.Contains(t, state.FinalAnswer, "类型甲和类型乙")
	require.Contains(t, state.FinalAnswer, `chunk_id="actual-page-chunk"`)
	_, reviewMessages, reviewUser, reviewInput := uniqueJSONUserInput(t, model.calls, "unverified_draft")
	var reviewSystem strings.Builder
	for _, message := range reviewMessages {
		if message.Role == "system" {
			reviewSystem.WriteString(message.Content)
			reviewSystem.WriteByte('\n')
		}
	}
	require.Contains(t, reviewSystem.String(), "独立复核上下文")
	require.Contains(t, reviewUser.Content, "实际该页的图例")
	require.NotContains(t, reviewUser.Content, "目录匹配正文不可")
	var draft string
	require.NoError(t, json.Unmarshal(reviewInput["unverified_draft"], &draft))
	require.Contains(t, draft, "类型甲和类型乙")
	require.True(t, hasCurrentDocumentEvidence(state.RoundSteps))
	require.False(t, hasStructuredEvidence(state.RoundSteps), "document review must not manufacture table evidence")
	foundPlan := false
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Name == agenttools.ToolReadDocument {
				var scope map[string]any
				raw, _ := json.Marshal(call.Result.Data["document_scope"])
				require.NoError(t, json.Unmarshal(raw, &scope))
				require.Equal(t, true, scope["plan_validated"])
				require.Equal(t, float64(4), scope["page"])
				foundPlan = true
			}
		}
	}
	require.True(t, foundPlan)
}
