package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const clarificationCatalogue = `{"literal_field_count":2,"literal_fields_truncated":false,"literal_fields":[
{"dataset_id":"v1","series_id":"c","source_file":"原表.xlsx","sheet":"利润","metric":"20天原料","unit":"元/吨","source_context":{"source_column":"C","formula_source":{"cell":"C26","formula":"=B6","references":[{"sheet":"利润","fields":[{"column":"B","metric":"3128级棉花"}]}]}}},
{"dataset_id":"v1","series_id":"u","source_file":"原表.xlsx","sheet":"利润","metric":"20天原料","unit":"元/吨","source_context":{"source_column":"U","formula_source":{"cell":"U26","formula":"=T6","references":[{"sheet":"利润","fields":[{"column":"T","metric":"CC Index 3128B"}]}]}}}]}`

func clarificationStep(output string) types.AgentStep {
	return types.AgentStep{Iteration: -1, ToolCalls: []types.ToolCall{{Name: "structured_data", Args: map[string]any{"operation": "list_datasets"}, Result: &types.ToolResult{Success: true, Output: output}}}}
}

func TestLiteralSourceClarificationNeedsCompleteAuthorizedExactCandidates(t *testing.T) {
	answer := literalSourceClarification("20天原料", []types.AgentStep{clarificationStep(clarificationCatalogue)})
	require.Contains(t, answer, "| C |")
	require.Contains(t, answer, "| U |")
	require.Contains(t, answer, "C26: =B6")
	require.Contains(t, answer, "CC Index 3128B")
	require.NotContains(t, answer, "最新值")
	for _, query := range []string{"CC Index的20天原料", "20天原料是多少", "20天原料平均值"} {
		require.Empty(t, literalSourceClarification(query, []types.AgentStep{clarificationStep(clarificationCatalogue)}))
	}
	for _, output := range []string{
		strings.Replace(clarificationCatalogue, `"literal_fields_truncated":false`, `"literal_fields_truncated":true`, 1),
		strings.Replace(clarificationCatalogue, `"literal_field_count":2`, `"literal_field_count":3`, 1),
		strings.Replace(clarificationCatalogue, `"series_id":"u"`, `"series_id":"c"`, 1),
		strings.Replace(clarificationCatalogue, `"metric":"20天原料"`, `"metric":"20天原料利润"`, 1),
	} {
		require.Empty(t, literalSourceClarification("20天原料", []types.AgentStep{clarificationStep(output)}))
	}
	step := clarificationStep(clarificationCatalogue)
	step.ToolCalls[0].Result.Success = false
	require.Empty(t, literalSourceClarification("20天原料", []types.AgentStep{step}))
	step.ToolCalls[0].Name = "search_knowledge"
	step.ToolCalls[0].Result.Success = true
	require.Empty(t, literalSourceClarification("20天原料", []types.AgentStep{step}), "document JSON cannot manufacture catalogue authority")
}

func TestLiteralSourceClarificationCompletesWithoutModelButFollowupKeepsReasoning(t *testing.T) {
	for _, hasHistory := range []bool{false, true} {
		t.Run(map[bool]string{false: "new turn", true: "followup"}[hasHistory], func(t *testing.T) {
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				if input["operation"] == "list_datasets" {
					_, _ = io.WriteString(w, clarificationCatalogue)
				} else {
					_, _ = io.WriteString(w, `{"tools":[]}`)
				}
			})
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "继续按已选U列。", Done: true, FinishReason: "stop"}}}}}
			e := newTestEngine(t, model)
			e.toolRegistry = registry
			e.config.WorkbenchDefault = true
			completes := 0
			e.eventBus.On(event.EventAgentComplete, func(context.Context, event.Event) error { completes++; return nil })
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user", Role: types.TenantRoleViewer})
			history := []chat.Message{}
			if hasHistory {
				history = append(history, chat.Message{Role: "user", Content: "按U列"})
			}
			state, err := e.Execute(ctx, "sess", "msg", "20天原料", history)
			require.NoError(t, err)
			require.True(t, state.IsComplete)
			require.Equal(t, 1, completes)
			if hasHistory {
				require.Len(t, model.calls, 1)
				require.Equal(t, "继续按已选U列。", state.FinalAnswer)
				require.Contains(t, model.calls[0][0].Content, "start_date与end_date都用该日", "workbench entries also receive bounded query guidance")
				require.Contains(t, model.calls[0][0].Content, "不主动扩展成全区间计数")
			} else {
				require.Empty(t, model.calls)
				require.Contains(t, state.FinalAnswer, "C26: =B6")
			}
		})
	}
}

func TestLiteralSourceClarificationDoesNotOverridePinnedOrRememberedScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*AgentEngine)
	}{
		{"document", func(e *AgentEngine) { e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeID: "chosen"}} }},
		{"memory", func(e *AgentEngine) { e.SetMemoryPrompt("采用已指定的U列") }},
		{"suggested source", func(e *AgentEngine) { e.questionOrigin = &QuestionOriginInfo{KnowledgeBaseID: "chosen"} }},
		{"pinned tool", func(e *AgentEngine) { e.pinnedMCPServices = []*PinnedMCPServiceInfo{{ID: "chosen"}} }},
		{"pinned skill", func(e *AgentEngine) { e.pinnedSkills = []*PinnedSkillInfo{{Name: "chosen"}} }},
		{"in-flight correction", func(e *AgentEngine) {
			e.SetSteerSink(&fakeSteerSink{queued: []map[string]interface{}{steerEntry("choice", "按U列")}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				if input["operation"] == "list_datasets" {
					_, _ = io.WriteString(w, clarificationCatalogue)
				} else {
					_, _ = io.WriteString(w, `{"tools":[]}`)
				}
			})
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "使用所选来源。", Done: true, FinishReason: "stop"}}}}}
			e := newTestEngine(t, model)
			e.toolRegistry = registry
			e.config.WorkbenchDefault = true
			tc.configure(e)
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user", Role: types.TenantRoleViewer})
			state, err := e.Execute(ctx, "s", "m", "20天原料", nil)
			require.NoError(t, err)
			require.Len(t, model.calls, 1)
			require.Equal(t, "使用所选来源。", state.FinalAnswer)
		})
	}
}

func TestBoundedSynthesisAndRepairUseMinimumSupportedReasoning(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "根据已取得证据回答。", Done: true, FinishReason: "stop"}}}}}
	e := newTestEngine(t, model)
	e.config.ReviewReasoningEffort = "minimal"
	require.NoError(t, e.streamFinalAnswerToEventBus(context.Background(), "问题", &types.AgentState{}, "s", emptyMessages()))
	require.Equal(t, chat.ReasoningEffort("minimal"), model.opts[0].ReasoningEffort)
	require.LessOrEqual(t, model.opts[0].MaxCompletionTokens, 4096)

	model = &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeThinking, Content: strings.Repeat("推理", 300)}, {ResponseType: types.ResponseTypeAnswer, Content: "-", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "完整回答。", Done: true, FinishReason: "stop"}}},
	}}
	e = newTestEngine(t, model)
	e.config.ReviewReasoningEffort = "minimal"
	e.config.ReasoningEffort = "high"
	_, err := e.executeLoop(context.Background(), &types.AgentState{}, "问题", emptyMessages(), nil, "s", "m")
	require.NoError(t, err)
	require.Equal(t, chat.ReasoningEffort("high"), model.opts[0].ReasoningEffort, "primary selected reasoning is preserved")
	require.Equal(t, chat.ReasoningEffort("minimal"), model.opts[1].ReasoningEffort, "repair uses bounded minimum")
}
