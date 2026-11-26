package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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
			require.Equal(t, 1, strings.Count(model.requests[0][0].Content, sourceTimeGuidance))
			for _, prompt := range []string{model.requests[0][0].Content, model.calls[0][0].Content} {
				if mode == "standard" {
					require.Equal(t, 1, strings.Count(prompt, standardAnswerGuidance), "planner and executor share exactly the same practical policy")
					require.Contains(t, prompt, "<standard_answer_policy>\n先给明确结论或数值。")
					require.Contains(t, prompt, "有同口径已读值就给最近值、日期和出处。确无同口径已核值时，给出材料证实的具体事实，注明可核日期和出处。说明具体缺口，当轮交付")
					require.Contains(t, prompt, "不以“待确认”“无法确定”或请用户补件、选版或开放联网收尾")
					require.Contains(t, prompt, "遵守用户明确的来源限制")
					require.Contains(t, prompt, "无需查特定资料的一般问题可直接用可靠通用知识作答")
					require.Contains(t, prompt, "解释概念、机构全称或缩写时结合语境")
					require.Contains(t, prompt, "若允许用通用知识且语境足以唯一消歧，直接说明缩写所指及用户问到的关系")
					require.Contains(t, prompt, "注明通用释义的依据，材料实例给出实读出处。用户明确只据原文时遵守该范围；确有歧义时说明")
					require.Contains(t, prompt, "在用户允许的范围内优先查所选资料库或已有线索指向的授权文档，按完整事件定位并实读记载，区分公布、生效等日期")
					require.Contains(t, prompt, "所选资料库的近期目录未列出相关文件时，仍按事件查找，不只凭库名或标签排除相关资料")
					require.Regexp(t, `对象、方向和区间或时长.*当期观察、随后变化和预测.*摘要和明细沿用同一口径`, prompt)
					require.Contains(t, prompt, "比较重复与更新时按类别说明，区分预告与结果披露")
					require.Contains(t, prompt, "从同主题目录选相关期次，首批各次读取携同一完整 source_scope 实读主题所在位置，导航摘录仅供定位")
					require.Contains(t, prompt, "按实际资料日期归并沿用期、更新前后值及分项、后续复述期次")
					require.Contains(t, prompt, "复述不另计更新")
					require.Contains(t, prompt, "所问表有原题时按原题独立定位，以实际包含所问观测期的表为准；若命中另一表题或另一观测粒度，回到指定原件找目标表")
					require.Contains(t, prompt, "按真实行列方向和分隔位置对齐所问期次与完整字段，列出各来源的行标、列名及交点原值，带回对应的可核分项")
					require.Contains(t, prompt, "日期写法不同时核对实际观测期；有源定义差异的证据才判定口径不同")
					require.Contains(t, prompt, "所问的表与其他分块或图同页时，按已核的物理页及目录定位目标表，分块或邻图的命中只说明该对象")
					require.Contains(t, prompt, "直接披露的原值不能用邻项、他源数值或重建值代替")
					require.Contains(t, prompt, "区分百分比增幅和基期指数")
					require.Contains(t, prompt, "用已读的本期和基期原值复算，按各自披露精度舍入")
					require.Contains(t, prompt, "能核实保存或披露先后时，说明较晚资料给出的值")
					require.Contains(t, prompt, "先说本次采用哪版及理由，依次遵循用户指定、已核实的正式勘误、可辨认的正式版或原始版")
					require.Contains(t, prompt, "证据等强时也点名一份已核版本暂用")
					require.Contains(t, prompt, "并列其他已核版本及各自出处，共同支持的结论照常回答")
					require.Contains(t, prompt, "仅在有明确替代依据时说明正式替代。独立来源各自归属")
					require.Contains(t, prompt, "保存时间不能充当观测期、正式发布日期或替代证据")
					require.Contains(t, prompt, "不凭文件名、上传顺序、数值或自拟用途认定权威或替代")
					require.Regexp(t, `按被引原序列取数.*需要统计或说明覆盖时.*取自同一序列和范围`, prompt)
					require.Contains(t, prompt, "直接用它统计和作图，无需推断其编制规则或从分项重建。用户指定构造口径时按该口径计算")
					require.Contains(t, prompt, "不凭行业惯例或邻字段推定原始单位")
					require.Contains(t, prompt, "简写数值按原文报数，改用完整单位时按已核倍率同步换算数值")
					require.Contains(t, prompt, "单位未核到时照常给出原值，并注明“原表未标单位”。")
					require.Contains(t, prompt, "若问题已给出单位，且原值与同表其他已核数据的量级一致，可按用户给出的单位作答，并注明单位取自问题及同表量级依据。")
					require.Contains(t, prompt, "字段或图题中的并列词可能是一个合并口径")
					require.Contains(t, prompt, "用户明确只用某表、某文件或已发布结构化数据时必须遵守")
					require.Contains(t, prompt, "不自行选择默认候选，也不因多候选就一律要求用户再选")
					require.Contains(t, prompt, "保留原始有效数值与样本")
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
			require.Contains(t, model.calls[0][1].Content, "Chosen reference")
			require.Equal(t, memory, engine.memoryPrompt, "the policy does not replace recalled memory")
			require.Contains(t, model.calls[0][0].Content, memory)
			require.Equal(t, []*SelectedDocumentInfo{document}, engine.selectedDocs)
			require.Equal(t, "chosen-document", document.KnowledgeID)
		})
	}
}

func TestSourceTimeGuidanceKeepsCandidateDatesThroughPlanningAndFallback(t *testing.T) {
	now := time.Now()
	const library = "设备运行库"
	const scope = "只看设备运行库"
	const baseID = "permitted-equipment-library"
	const excludedFile = "outside-scope.pdf"
	for _, tc := range []struct {
		name, question string
		invalidPlan    bool
	}{
		{name: "relative time", question: scope + "，今年设备开工好没好？跟上周比呢？"},
		{name: "explicit current year with invalid plan", question: fmt.Sprintf("%s，请给出%d年截至今天的实时设备开工情况。", scope, now.Year()), invalidPlan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := []struct{ id, filename, date, excerpt string }{
				{"fixture-doc-a", "older-week.pdf", "2021-06-07", "设备开工观测期为6月初"},
				{"fixture-doc-b", "previous-week.pdf", "2021-06-14", "设备开工观测期为6月上旬"},
				{"fixture-doc-c", "latest-week.pdf", "2021-06-21", "设备开工观测截止6月20日，另附下一年度预测"},
				{"fixture-doc-d", "historical-records.xlsx", "2020-12-31", "原表中的设备开工记录截至2020年底"},
				{"fixture-doc-e", "canteen-update.pdf", "2022-01-10", "食堂营业时间调整"},
			}
			service := &sourceTimeSearchFixture{
				sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{},
				documents:                       &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}},
			}
			var permitted []string
			for _, source := range sources {
				permitted = append(permitted, source.id)
				service.addDocument(baseID, source.id, source.filename, source.date, source.excerpt)
			}
			service.addDocument(baseID, "excluded", excludedFile, "2023-01-01", "范围外的设备开工信息")
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: baseID, TenantID: 7, KnowledgeIDs: permitted}}
			registry := agenttools.NewToolRegistry()
			registry.RegisterTool(agenttools.NewSearchKnowledgeTool(service, service.documents, nil, targets, nil, nil))
			registry.RegisterTool(newCountingTool("other_available_tool"))
			plan := sourceDomainPlan(t, "discover", []string{"knowledge"})
			var responses []mockResponse
			if tc.invalidPlan {
				plan = strings.Replace(plan, `"mode":"discover"`, `"mode":"none"`, 1)
				responses = append(responses, mockResponse{chunks: []types.StreamResponse{{
					ToolCalls: []types.LLMToolCall{{ID: "model-discovery", Function: types.FunctionCall{
						Name: agenttools.ToolSearchKnowledge, Arguments: `{"query":"设备开工记录"}`,
					}}}, Done: true, FinishReason: "tool_calls",
				}}})
			}
			responses = append(responses, mockResponse{chunks: []types.StreamResponse{{
				ResponseType: types.ResponseTypeAnswer, Content: "已按所问范围核对可用资料。", Done: true, FinishReason: "stop",
			}}})
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: plan, FinishReason: "stop"}, mockChat: mockChat{responses: responses}}
			engine := newTestEngine(t, model)
			engine.toolRegistry = registry
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled, engine.config.AnswerReviewMode = true, true, "standard"
			engine.questionOrigin = &QuestionOriginInfo{KnowledgeBaseID: baseID, KnowledgeBaseName: library}
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			state, err := engine.Execute(ctx, "session", "message", tc.question, nil)
			require.NoError(t, err)
			require.Len(t, model.requests, 1)
			require.Len(t, model.calls, len(responses))
			var planInput struct {
				Question string `json:"current_user_question"`
				Scope    string `json:"runtime_scope"`
			}
			require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &planInput))
			require.Equal(t, tc.question, planInput.Question)
			require.Contains(t, planInput.Scope, "<current_time>")
			require.Contains(t, planInput.Scope, `name="`+library+`"`)
			if tc.invalidPlan {
				require.Nil(t, engine.taskPlan)
				require.Contains(t, listToolNames(model.opts[0].Tools), "other_available_tool")
				require.NotContains(t, model.calls[0][0].Content, "<advisory_task_plan>")
				require.Len(t, model.calls[0], 2, "fallback waits for the model to request discovery")
			} else {
				require.NotNil(t, engine.taskPlan)
				require.NotContains(t, listToolNames(model.opts[0].Tools), "other_available_tool")
			}
			prompts := []string{model.requests[0][0].Content}
			for _, call := range model.calls {
				prompts = append(prompts, call[0].Content)
				require.Contains(t, call[1].Content, tc.question)
				require.Contains(t, call[1].Content, `name="`+library+`"`)
			}
			for _, prompt := range prompts {
				for _, rule := range []string{
					"current_time 是系统今天", "资料披露日期", "指标观测期", "预测年度",
					"以已发现的同主题资料中能回答所问指标的最晚观测时点为锚", "截至某年某月某日（资料日期）",
					"不先用系统年份缩小范围", "系统年份与资料年份不同不构成拒答理由", "不把预测写成当年实绩",
					"用户明确问系统当前年份、指定年份或实时值时", "有资料缺口先说明",
					"先按已发现的同主题报告日期排序并绑定对应文件，再核对同一指标和口径", "证据只够两期就只说两期",
					"旧表与更新的报告都能回答时，优先采用实际观测更新的资料，说明各自时点", "无关的新文件不参与比较",
					"不设必读文件的最低数量",
				} {
					require.Contains(t, prompt, rule)
				}
			}
			var discovery string
			for _, message := range model.calls[len(model.calls)-1] {
				if message.Role == "tool" && message.Name == agenttools.ToolSearchKnowledge {
					discovery = message.Content
				}
			}
			for _, source := range sources {
				_, entry, found := strings.Cut(discovery, "<source_file_name>"+source.filename+"</source_file_name>")
				require.True(t, found, "%s missing from discovery:\n%s", source.filename, discovery)
				entry, _, _ = strings.Cut(entry, "</document>")
				require.Contains(t, entry, source.date)
				require.Contains(t, entry, source.excerpt)
			}
			require.NotContains(t, discovery, excludedFile)
			require.Contains(t, discovery, `navigation_only="true" requires_read="true"`)
			require.Len(t, service.searches, 1)
			require.ElementsMatch(t, permitted, service.searches[0].params.KnowledgeIDs)
			require.False(t, hasCurrentDocumentEvidence(state.RoundSteps), "discovery previews are not read evidence")

			if !tc.invalidPlan {
				// The planner normally precedes discovery. Its existing input also
				// supports authorized discovery steps already available to a caller.
				engine.planWorkbenchTask(ctx, state, tc.question, []chat.Message{{Role: "user", Content: tc.question}}, "session")
				require.Len(t, model.requests, 2)
				planningInput := model.requests[1][1].Content
				for _, source := range sources {
					require.Contains(t, planningInput, source.filename)
					require.Contains(t, planningInput, source.date)
				}
				require.NotContains(t, planningInput, excludedFile)
			}
		})
	}
}

type sourceTimeSearchFixture struct {
	*sourceDiscoveryKnowledgeService
	documents *sourceTimeDocumentStore
	matches   []*types.SearchResult
}

func (s *sourceTimeSearchFixture) addDocument(baseID, id, filename, date, excerpt string) {
	s.documents.byID[id] = &types.Knowledge{ID: id, TenantID: 7, KnowledgeBaseID: baseID, FileName: filename,
		ParseStatus: types.ParseStatusCompleted, Metadata: types.JSON(fmt.Sprintf(`{"report_date":%q}`, date))}
	s.matches = append(s.matches, &types.SearchResult{ID: "chunk-" + id, KnowledgeID: id, KnowledgeBaseID: baseID, Content: excerpt, Score: 0.99})
}

func (s *sourceTimeSearchFixture) HybridSearch(ctx context.Context, id string, params types.SearchParams) ([]*types.SearchResult, error) {
	_, _ = s.sourceDiscoveryKnowledgeService.HybridSearch(ctx, id, params)
	return s.matches, nil
}

type sourceTimeDocumentStore struct {
	interfaces.KnowledgeService
	byID map[string]*types.Knowledge
}

func (s *sourceTimeDocumentStore) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	return s.byID[id], nil
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
			require.Contains(t, input, "本轮按所问日期和指标定向查找，定位并实读能补答的正文或原图")
			require.Contains(t, input, "补充记载按该来源的指标原名、日期和具体出处另列")
			require.Contains(t, input, "若只核到同口径最近值，给出该值及其日期、出处")
			require.Contains(t, input, "用户限定只用数据库、某表或某文件时，直接交付限定范围内的结论")
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
