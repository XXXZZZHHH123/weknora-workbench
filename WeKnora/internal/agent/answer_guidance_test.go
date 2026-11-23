package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStandardAnswerGuidanceReachesPlannerAndExecutionWithoutChangingUserScope(t *testing.T) {
	const question = "请简要介绍这类活动的一般特点。"
	for _, mode := range []string{"standard", "strict", ""} {
		t.Run(mode, func(t *testing.T) {
			model := &taskPlannerChat{
				plan: &types.ChatResponse{Content: sourceDomainPlan(t, "none", []string{}), FinishReason: "stop"},
				mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
					ResponseType: types.ResponseTypeAnswer, Content: "这里给出所需的一般介绍。", Done: true, FinishReason: "stop",
				}}}}},
			}
			engine := newTestEngine(t, model)
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.config.AnswerReviewMode = mode
			document := &SelectedDocumentInfo{KnowledgeID: "chosen-document", Title: "Chosen reference", FileType: "pdf"}
			engine.selectedDocs = []*SelectedDocumentInfo{document}
			memory := types.WrapMemoryForPrompt("Preference: use plain language.", "")
			engine.SetMemoryPrompt(memory)
			state, err := engine.Execute(t.Context(), "session", "message", question, nil)
			require.NoError(t, err)
			require.Equal(t, "这里给出所需的一般介绍。", state.FinalAnswer)
			require.Len(t, model.requests, 1)
			require.Len(t, model.calls, 1)
			for _, prompt := range []string{model.requests[0][0].Content, model.calls[0][0].Content} {
				if mode == "standard" {
					require.Equal(t, 1, strings.Count(prompt, standardAnswerGuidance), "planner and executor share exactly the same practical policy")
					require.Contains(t, prompt, "文件名措辞、上传顺序或数值接近程度不能证明权威或替代关系")
					require.Contains(t, prompt, "并列呈现有依据的差异并明确无法确定哪版优先")
					require.Contains(t, prompt, "字段或图题中的并列词可能是一个合并口径")
					require.Contains(t, prompt, "用户明确只用某表、某文件或已发布结构化数据时必须遵守")
					require.Contains(t, prompt, "不自行选择默认候选，也不因多候选就一律要求用户再选")
					require.Contains(t, prompt, "保留原始有效数值与样本")
					require.Contains(t, prompt, "未知单位就按原值报告且说明未核到，不补单位")
					require.Contains(t, prompt, "按原问题比较已发现候选的实际覆盖")
					require.Contains(t, prompt, "规划发现文档和结构化数据")
					require.Contains(t, prompt, "报告发布日期、预测月份、文件上传日和最后非空观测日")
					require.Contains(t, prompt, "不把较晚发布的历史复盘或未来预测当作较新的实际观测")
					require.Contains(t, prompt, "用户指定时期或单一来源时，遵从其范围")
				} else {
					require.NotContains(t, prompt, "<standard_answer_policy>", "strict and legacy prompts preserve their prior policy")
				}
			}
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
			require.Equal(t, question, input["current_user_question"])
			require.Contains(t, input["runtime_scope"], "<current_time>", "the clock remains available for actual relative-time questions")
			require.Contains(t, input["runtime_scope"], "Chosen reference")
			require.Contains(t, model.calls[0][1].Content, question)
			require.Equal(t, memory, engine.memoryPrompt, "the policy does not replace recalled memory")
			require.Contains(t, model.calls[0][0].Content, memory)
			require.Equal(t, []*SelectedDocumentInfo{document}, engine.selectedDocs)
			require.Equal(t, "chosen-document", document.KnowledgeID)
		})
	}
}

type inventoryReplayTool struct {
	agenttools.BaseTool
	result string
}

func (tool *inventoryReplayTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Output: tool.result}, nil
}

func TestStandardInventoryReplayKeepsObservationAndPublicationPeriodsDistinct(t *testing.T) {
	const (
		navigation         = `{"documents":[{"title":"仓库周报","report_date":"2025-09-08"},{"title":"仓库复盘","report_date":"2025-09-20"}]}`
		newReport          = "仓库周报发布日2025-09-08，第4页：截至2025-09-05，两处仓库合计库存18吨；上一周为16吨。"
		lateRetrospective  = "仓库复盘发布日2025-09-20，第2页：复盘2025-04-01当日两处仓库库存10吨。"
		inventoryDirectory = `{"series":[{"series_id":"both","source_metric":"北仓和南仓合计库存","unit":"吨","unit_status":"explicit","last_valid_date":"2025-08-01","header_cells":["F1"]},{"series_id":"north","source_metric":"北仓库存","unit":null,"unit_status":"needs_review","last_valid_date":"2025-07-01","header_cells":["B1"]}]}`
	)
	for _, fixture := range []struct {
		name, question, arguments, observation, answer string
		withReports                                    bool
	}{
		{"latest combined", "库内最近两个仓库的合计库存怎么变了？", `{"series_id":"both","operation":"latest"}`, `{"date":"2025-08-01","value":15,"unit":"吨"}`, "据仓库周报第4页，截至2025-09-05，合计库存从16吨增至18吨。", true},
		{"single field", "只用库存表，北仓最后一条记录是多少？", `{"series_id":"north","operation":"latest"}`, `{"date":"2025-07-01","value":4,"unit":null}`, "截至2025-07-01，北仓库存原值为4，原表单位未核实。", false},
		{"requested period", "只用库存表，两处仓库在2025-04-01的合计库存是多少？", `{"series_id":"both","start":"2025-04-01","end":"2025-04-01"}`, `{"date":"2025-04-01","value":10,"unit":"吨"}`, "截至2025-04-01，两处仓库合计库存10吨。", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			calls := func(name, args string) mockResponse {
				return mockResponse{chunks: []types.StreamResponse{{
					ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "tool_calls",
					ToolCalls: []types.LLMToolCall{{ID: name, Type: "function", Function: types.FunctionCall{Name: name, Arguments: args}}},
				}}}
			}
			responses := []mockResponse{calls("structured_list_series", `{}`), calls("structured_query_series", fixture.arguments)}
			domains := []string{"structured"}
			if fixture.withReports {
				domains = append(domains, "knowledge")
				responses = append(responses, calls(agenttools.ToolReadDocument, `{"query":"两处仓库实际库存"}`))
			}
			responses = append(responses, mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: fixture.answer, Done: true, FinishReason: "stop"}}})
			model := &taskPlannerChat{
				plan:     &types.ChatResponse{Content: sourceDomainPlan(t, "discover", domains), FinishReason: "stop"},
				mockChat: mockChat{responses: responses},
			}
			engine := newTestEngine(t, model)
			engine.config.AnswerReviewMode, engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = "standard", true, true
			engine.toolRegistry = agenttools.NewToolRegistry()
			for name, result := range map[string]string{
				"structured_list_series": inventoryDirectory, "structured_query_series": fixture.observation,
				agenttools.ToolSearchKnowledge: navigation, agenttools.ToolReadDocument: newReport + "\n" + lateRetrospective,
			} {
				engine.toolRegistry.RegisterTool(&inventoryReplayTool{BaseTool: agenttools.NewBaseTool(name, "Synthetic inventory source", json.RawMessage(`{"type":"object"}`)), result: result})
			}
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "replay-user", Role: types.TenantRoleViewer})
			state, err := engine.Execute(ctx, "session", "message", fixture.question, nil)
			require.NoError(t, err)
			require.Equal(t, fixture.answer, state.FinalAnswer)
			require.Len(t, model.calls, len(responses))
			require.Len(t, model.requests, 1)
			require.Contains(t, model.requests[0][0].Content, "以能支持同一口径的最新观测领答")
			var evidence strings.Builder
			for _, message := range model.calls[len(model.calls)-1] {
				if message.Role == "tool" {
					evidence.WriteString(message.Content)
				}
			}
			require.Contains(t, evidence.String(), inventoryDirectory)
			require.Contains(t, evidence.String(), fixture.observation)
			if fixture.withReports {
				require.Contains(t, evidence.String(), navigation)
				require.Contains(t, evidence.String(), newReport)
				require.Contains(t, evidence.String(), lateRetrospective)
			} else {
				require.NotContains(t, evidence.String(), navigation)
				require.NotContains(t, evidence.String(), newReport)
				require.NotContains(t, evidence.String(), lateRetrospective)
			}
		})
	}
}
