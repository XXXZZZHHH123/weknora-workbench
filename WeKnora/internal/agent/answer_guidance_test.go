package agent

import (
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

func TestMissingDatabaseDateGuidanceAndSupplementalSource(t *testing.T) {
	const queryTool = "structured_analyze_table"
	const tableResult = `{"kind":"structured_analysis","status":"ok","rows":[],"requested_range":{"start_date":"2021-06-10","end_date":"2021-06-10"},"source":{"file":"设备台账.xlsx"},"source_semantics":{"published_series_dates":[{"series_id":"hours","metric":"运行时长","sheet":"设备","last_valid_date":"2021-06-07","last_date":"2021-06-09"}]}}`
	const reportID = "contract-document"
	const reportFile = "6月10日设备巡检周报"
	const reportChunk = "inspection-report-page"
	const dbConclusion = "设备台账没有6月10日的记录，这项指标最后有值是2021年6月7日。"
	const supplement = "另据6月10日设备巡检周报第2页，运行时长为13小时。"

	for _, onlyTable := range []bool{false, true} {
		name := "authorized supplement"
		if onlyTable {
			name = "only the specified table"
		}
		t.Run(name, func(t *testing.T) {
			question := "设备台账里2021年6月10日的运行时长是多少？"
			answer := dbConclusion
			discovery := "discover"
			responses := []mockResponse{chatToolCall("table", queryTool, `{}`)}
			if onlyTable {
				question += "只查设备台账这张表。"
				discovery = "recover"
			} else {
				responses = append(responses, chatToolCall("report", agenttools.ToolReadDocument, `{"id":"`+reportID+`","page":2}`))
				answer += supplement + `<ref id="c1" />`
			}
			model := &taskPlannerChat{
				plan:     &types.ChatResponse{Content: sourceDomainPlan(t, discovery, []string{"structured"}), FinishReason: "stop"},
				mockChat: mockChat{responses: append(responses, chatAnswer(answer))},
			}
			engine := newTestEngine(t, model, withCitationsEnabled(true))
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.config.AnswerReviewMode = "standard"
			engine.toolRegistry = agenttools.NewToolRegistry()
			var data map[string]any
			require.NoError(t, json.Unmarshal([]byte(tableResult), &data))
			table := &evidenceContinuationTool{
				BaseTool: agenttools.NewBaseTool(queryTool, "Query the authorized table.", json.RawMessage(`{"type":"object"}`)),
				result:   &types.ToolResult{Success: true, Output: tableResult, Data: data},
			}
			report := &types.Knowledge{ID: reportID, KnowledgeBaseID: "inspection-library", TenantID: 7,
				Title: reportFile, FileName: reportFile + ".pdf", FileType: "pdf", ParseStatus: types.ParseStatusCompleted}
			chunks := &citationContractChunkRepository{rows: []*types.Chunk{{
				ID: reportChunk, KnowledgeID: report.ID, KnowledgeBaseID: report.KnowledgeBaseID, TenantID: 7,
				ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "# 第2页\n\n2021年6月10日，设备运行时长为13小时。",
				SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 2, SourceHash: strings.Repeat("b", 64)}},
			}}}
			engine.toolRegistry.RegisterTool(table)
			engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: report},
				&citationContractChunkService{repo: chunks}, types.SearchTargets{{
					Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: report.KnowledgeBaseID, TenantID: report.TenantID,
				}}))
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "authorized-reader", Role: types.TenantRoleViewer})
			state, err := engine.Execute(ctx, "session", "message", question, nil)
			require.NoError(t, err)
			require.True(t, state.IsComplete)
			require.Equal(t, 1, table.calls)
			require.Len(t, model.calls, len(responses)+1)
			input := rawMessageText(model.calls[1])
			require.Contains(t, input, question)
			require.Contains(t, input, "这项指标最后有值是某年某月某日")
			require.Contains(t, input, "补充数字逐项注明独立出处")
			require.Contains(t, input, "用户限定只用数据库、某表或某文件时")
			require.Contains(t, input, `"last_valid_date":"2021-06-07","last_date":"2021-06-09"`)
			docReads := 0
			for _, step := range state.RoundSteps {
				for _, call := range step.ToolCalls {
					if call.ExecutionName() == agenttools.ToolReadDocument {
						require.True(t, call.Result.Success)
						docReads++
					}
				}
			}
			if onlyTable {
				require.Zero(t, docReads)
				require.NotContains(t, listToolNames(model.opts[1].Tools), agenttools.ToolReadDocument)
				require.NotContains(t, input, reportFile)
				require.Equal(t, dbConclusion, state.FinalAnswer)
			} else {
				require.Equal(t, 1, docReads)
				require.Contains(t, listToolNames(model.opts[1].Tools), agenttools.ToolReadDocument)
				finalInput := rawMessageText(model.calls[2])
				require.Contains(t, finalInput, reportFile)
				require.Contains(t, finalInput, "设备运行时长为13小时")
				require.True(t, strings.HasPrefix(state.FinalAnswer, dbConclusion+supplement))
				require.Equal(t, []string{reportChunk}, uniqueCandidateCitationIDs(state.FinalAnswer))
			}
		})
	}
}
