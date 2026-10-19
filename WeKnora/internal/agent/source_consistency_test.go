package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceConsistencyState() *types.AgentState {
	return &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		ID: "fetched-call", Name: "read_document", Result: &types.ToolResult{Success: true, Data: map[string]any{
			"document": map[string]any{"title": "原始调查报告", "file_name": "survey.pdf"},
			"chunks": []map[string]any{{"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content": "统计对象为覆盖面积，单位为公顷。", "page": 7,
				"images": []map[string]any{{"url": "https://source.invalid/chart.png", "caption": "图中指标为覆盖面积", "ocr_text": "西部区域：覆盖面积"}},
			}},
		}},
	}}}}}
}

func sourceConsistencyEngine(t *testing.T, model *mockChat) *AgentEngine {
	e := newTestEngine(t, model)
	e.config.WorkbenchDefault = true
	e.config.TaskPlanningEnabled = true
	return e
}

func sourceConsistencyVerdict(text string) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: text, Done: true, FinishReason: "stop"}}}
}

// sourceAcceptedVerdict explicitly opts a flow fixture into the accepted
// source-review protocol. Bare complete=true responses remain unmodified.
func sourceAcceptedVerdict() mockResponse {
	return mockResponse{acceptSourceReview: true}
}

func TestSourceConsistencyRequiresInternalWorkbenchModeAndActualCurrentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		workbench, planning, source bool
	}{
		{"legacy", false, true, true}, {"planning_disabled", true, false, true}, {"no_source", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockChat{}
			e := newTestEngine(t, m)
			e.config.WorkbenchDefault = tc.workbench
			e.config.TaskPlanningEnabled = tc.planning
			state := &types.AgentState{}
			if tc.source {
				state = sourceConsistencyState()
			}
			repair, err := e.answerNeedsRepair(t.Context(), "完整问题", "一段长度足够的完整普通回答。", "", state)
			require.NoError(t, err)
			require.False(t, repair)
			require.Empty(t, m.calls)
		})
	}
	e := sourceConsistencyEngine(t, &mockChat{})
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "plan_task", Result: &types.ToolResult{Success: true, Data: map[string]any{"plan": "Read a report and explain its indicator."}}}}}}}
	require.False(t, e.needsSourceConsistencyCheck(state), "a plan is not fetched evidence")
	state = sourceConsistencyState()
	state.RoundSteps[0].ToolCalls[0].Result.Success = false
	require.False(t, e.needsSourceConsistencyCheck(state), "a failed read is not fetched evidence")
}

func TestSourceConsistencyUsesCompleteRequestAndRealSourceTierInExistingCall(t *testing.T) {
	verdict := sourceConsistencyVerdict(`{"complete":false,"feedback":"将面积解释为产量没有来源依据；仅交付所问指标。OCR未证明看过原图。"}`)
	verdict.chunks[0].Usage = &types.TokenUsage{PromptTokens: 70, CompletionTokens: 20, TotalTokens: 90}
	m := &mockChat{responses: []mockResponse{verdict}}
	e := sourceConsistencyEngine(t, m)
	e.config.ReviewReasoningEffort = "minimal"
	state := sourceConsistencyState()
	question := strings.Repeat("严格依据原始调查的统计含义；", 200) + "只说明这个指标是什么。"
	repair, err := e.answerNeedsRepair(t.Context(), question, "该指标是产量，且原图标签相互冲突。", "", state)
	require.NoError(t, err)
	require.True(t, repair)
	require.Len(t, m.calls, 1, "the existing completeness call also performs source consistency")
	require.Equal(t, 90, state.TurnUsage.TotalTokens)
	require.GreaterOrEqual(t, m.opts[0].MaxCompletionTokens, 2048)
	require.LessOrEqual(t, m.opts[0].MaxCompletionTokens, 6144)
	require.Equal(t, chat.ReasoningEffort("minimal"), m.opts[0].ReasoningEffort)
	require.Equal(t, "none", m.opts[0].ToolChoice)
	require.Empty(t, m.opts[0].Tools)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(m.calls[0][1].Content), &input))
	require.Equal(t, question, input["current_user_question"])
	require.Equal(t, "该指标是产量，且原图标签相互冲突。", input["candidate_answer"])
	require.NotContains(t, input, "execution_evidence", "do not include a second opaque truncated evidence view")
	require.Contains(t, m.calls[0][1].Content, "公顷")
	require.Contains(t, m.calls[0][1].Content, "覆盖面积")
	require.Contains(t, m.calls[0][0].Content, "metric identity")
	require.Contains(t, m.calls[0][0].Content, "OCR mapping/layout")
	require.Contains(t, m.calls[0][0].Content, "Unsupported extra conclusions require repair")
	evidence, scope := e.sourceConsistencyEvidence(state, nil)
	require.True(t, scope.ViewComplete)
	access := evidence[0].Content.(map[string]any)["runtime_source_access"].(map[string]any)
	require.Equal(t, json.Number("1"), access["fetched_document_chunks"])
	require.Equal(t, json.Number("1"), access["fetched_image_annotation_groups"])
	require.Equal(t, json.Number("0"), access["original_images_returned"])
	require.Equal(t, json.Number("0"), access["viewable_original_images_in_generation_context"])
	require.Contains(t, e.answerRepairAdvice(), "模型核验意见（非新事实")
	require.Contains(t, e.answerRepairAdvice(), "仅交付所问指标")
	require.Contains(t, e.answerRepairAdvice(), "不要重复取数")
	// Projection counts are reviewer access metadata only. They never modify
	// durable results or authorize a numerical claim in the real answer guard.
	require.NotContains(t, state.RoundSteps[0].ToolCalls[0].Result.Data, "runtime_source_access")
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: "structured_query_series", Result: &types.ToolResult{Success: true, Output: `{"kind":"structured_result_evidence","status":"ok","values":[{"value":40}]}`}}}})
	e.sourceConsistencyEvidence(state, nil)
	unsupported := ungroundedStructuredNumbers("覆盖面积为1公顷。", answerNumericEvidence("覆盖面积为1公顷。", state.RoundSteps, nil), state.RoundSteps)
	require.Contains(t, unsupported, "1", "the projected fetched-chunk count cannot become a source magnitude")
}

func TestSourceConsistencyImageAccessUsesReturnedBytesAndActualContext(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	e.config.ChatModelSupportsVision = true
	state := sourceConsistencyState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	result.Images = []string{"data:image/png;base64,cG5n"}
	result.ImageDescriptions = []string{"独立视觉模型观察到的面积标签"}
	for _, attached := range []bool{false, true} {
		messages := []chat.Message{}
		if attached {
			messages = append(messages, chat.Message{Role: "user", Images: result.Images})
		}
		evidence, _ := e.sourceConsistencyEvidence(state, messages)
		access := evidence[0].Content.(map[string]any)["runtime_source_access"].(map[string]any)
		require.Equal(t, json.Number("1"), access["original_images_returned"])
		want := 0
		if attached {
			want = 1
		}
		require.Equal(t, json.Number(strconv.Itoa(want)), access["viewable_original_images_in_generation_context"])
		require.Len(t, access["separate_vision_descriptions"], 1)
		serialized, _ := json.Marshal(evidence)
		require.NotContains(t, string(serialized), "base64,cG5n", "metadata never embeds pixel payloads")
	}
	e.config.ChatModelSupportsVision = false
	evidence, _ := e.sourceConsistencyEvidence(state, []chat.Message{{Role: "user", Images: result.Images}})
	access := evidence[0].Content.(map[string]any)["runtime_source_access"].(map[string]any)
	require.Equal(t, json.Number("0"), access["viewable_original_images_in_generation_context"])
}

func TestSourceConsistencyEvidenceIsBoundedAndReportsOmissions(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	state := sourceConsistencyState()
	state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]map[string]any)[0]["content"] = strings.Repeat("真实来源长正文。", 20000)
	evidence, scope := e.sourceConsistencyEvidence(state, nil)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 24000)
	require.False(t, scope.ViewComplete)
	require.Contains(t, string(encoded), "omitted_paths")
	require.Contains(t, string(encoded), "runtime_source_access")
	require.Contains(t, string(encoded), "fetched_document_chunks")
	require.True(t, hasCurrentDocumentEvidence(state.RoundSteps), "projection never mutates durable source facts")
}

func TestSourceConsistencyVerdictRequiresReceiptAndRejectsMalformedProtocol(t *testing.T) {
	for _, text := range []string{`{"complete":true}`, `{"complete":true,"feedback":""}`} {
		m := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(text)}}
		e := sourceConsistencyEngine(t, m)
		e.answerReviewFeedback = "obsolete feedback"
		repair, err := e.evaluateAnswerCompletenessOnce(t.Context(), "Explain the source indicator.", "该指标的名称和单位均按原文保留。", sourceConsistencyState(), 0)
		require.Error(t, err, "a bare acceptance cannot bypass the source audit receipt")
		require.True(t, repair)
		require.Empty(t, e.answerReviewFeedback)
		require.Len(t, m.calls, 1)
	}
	m := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	e := sourceConsistencyEngine(t, m)
	repair, err := e.answerNeedsRepair(t.Context(), "Explain the source indicator.", "该指标的名称和单位均按原文保留。", "", sourceConsistencyState())
	require.NoError(t, err)
	require.False(t, repair)
	require.Len(t, m.calls, 1)
	for _, text := range []string{`{}`, `{"complete":"true"}`, `{"complete":false,"feedback":3}`, `{"complete":true,"other":false}`, `{"complete":true} trailing`, `{"complete":false,"complete":true}`, `{"complete":true,"feedback":null}`} {
		e := sourceConsistencyEngine(t, &mockChat{responses: []mockResponse{sourceConsistencyVerdict(text)}})
		e.answerReviewFeedback = "obsolete feedback"
		repair, err := e.answerNeedsRepair(t.Context(), "Explain the source indicator.", "该指标按原文保留。", "", sourceConsistencyState())
		require.Error(t, err)
		require.True(t, repair)
		require.Empty(t, e.answerReviewFeedback)
	}
	e = newTestEngine(t, &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":true,"feedback":"unexpected"}`)}})
	repair, err = e.answerNeedsRepair(t.Context(), "Return one symbol.", "?", "", &types.AgentState{})
	require.Error(t, err, "non-workbench completeness still has the complete-only strict contract")
	require.True(t, repair)
}

func TestFinalSynthesisRepairsSemanticSourceMismatchWithoutLeakingDraft(t *testing.T) {
	m := &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict("该指标代表产量，而且原图排版有冲突。"),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"保留覆盖面积的原始含义；删去未经原图核对的冲突结论。"}`),
		sourceConsistencyVerdict("该指标代表覆盖面积，单位为公顷。"),
		sourceAcceptedVerdict(),
	}}
	e := sourceConsistencyEngine(t, m)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	state := sourceConsistencyState()
	err := e.streamFinalAnswerToEventBus(context.Background(), "只说明报告的指标含义与单位。", state, "session", emptyMessages())
	require.NoError(t, err)
	require.Equal(t, "该指标代表覆盖面积，单位为公顷。", state.FinalAnswer)
	require.Len(t, m.calls, 4)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
	repairPrompt := ""
	for _, msg := range m.calls[2] {
		repairPrompt += msg.Content
	}
	require.Contains(t, repairPrompt, "模型核验意见（非新事实")
	require.Contains(t, repairPrompt, "删去未经原图核对的冲突结论")
	require.NotContains(t, visible, "产量")
	require.NotContains(t, visible, "排版")
}

func TestSourceConsistencyAlsoChecksStructuredMetricIdentityWithoutNumericChanges(t *testing.T) {
	m := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"应保留原字段的单位和指标含义，不能把实测面积说成产量。"}`)}}
	e := sourceConsistencyEngine(t, m)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_query_series", ID: "query-call", Result: &types.ToolResult{Success: true, Output: `{"kind":"structured_result_evidence","status":"ok","field_name":"实测面积","unit":"公顷","values":[{"value":40}]}`}}}}}}
	require.True(t, hasStructuredEvidence(state.RoundSteps))
	repair, err := e.answerNeedsRepair(t.Context(), "解释这个字段的口径。", "这是以公顷计量的产量数据。", "", state)
	require.NoError(t, err)
	require.True(t, repair)
	require.Contains(t, m.calls[0][1].Content, "实测面积")
	require.Contains(t, e.answerRepairAdvice(), "指标含义")
}

func TestSourceConsistencyNormalLoopRepairIncludesAdviceAndKeepsDraftPrivate(t *testing.T) {
	m := &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict("初步答复将覆盖面积称为产量。"),
		sourceConsistencyVerdict(`{"answer":"该指标代表产量，原图排版冲突。"}`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"按已读取正文保留面积含义；不要将OCR提取问题断言为原图冲突。"}`),
		sourceConsistencyVerdict(`{"answer":"该指标代表覆盖面积，单位为公顷。"}`),
		sourceAcceptedVerdict(),
	}}
	e := sourceConsistencyEngine(t, m)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	state := sourceConsistencyState()
	_, err := e.executeLoop(t.Context(), state, "只解释原文指标的含义和单位。", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, "该指标代表覆盖面积，单位为公顷。", state.FinalAnswer)
	require.Len(t, m.calls, 5, "source consistency reuses the final completeness check rather than adding a second audit call")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
	repairPrompt := ""
	for _, message := range m.calls[3] {
		repairPrompt += message.Content
	}
	require.Contains(t, repairPrompt, "模型核验意见（非新事实")
	require.Contains(t, repairPrompt, "按已读取正文保留面积含义")
	require.NotContains(t, visible, "产量")
	require.NotContains(t, visible, "排版冲突")
}

func TestSourceConsistencyFailureKeepsExistingBoundedRepairBudget(t *testing.T) {
	responses := []mockResponse{}
	for i := 0; i < 3; i++ {
		responses = append(responses, sourceConsistencyVerdict("该指标代表产量，原图存在标签冲突。"), sourceConsistencyVerdict(`{"complete":false,"feedback":"指标含义与已读取来源不一致。"}`))
	}
	m := &mockChat{responses: responses}
	e := sourceConsistencyEngine(t, m)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	state := sourceConsistencyState()
	require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "说明原文指标。", state, "session", emptyMessages()))
	require.Len(t, m.calls, 6, "one existing final candidate plus at most two repairs, each with one combined check")
	require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, incompleteAnswerFallback, visible)
}

func TestSourceConsistencyRepairsMisinterpretedExecutedCalculation(t *testing.T) {
	const quote = "当前延迟42毫秒，较上期减少3毫秒。"
	const citation = `<kb doc="latency.md" chunk_id="source-latency" kb_id="authorized-kb" />`
	const candidate = "当前延迟42毫秒，较上期减少3毫秒；上期延迟39毫秒。" + citation
	const corrected = quote + citation
	model := &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict(candidate),
		sourceConsistencyVerdict(`{"answer":` + strconv.Quote(candidate) + `}`),
		// A contradictory accept-with-repair response must not release the
		// candidate merely because its arithmetic values are genuine.
		sourceConsistencyVerdict(`{"complete":true,"feedback":"计算表达式没有支持所声称的基期关系，且用户未要求该派生值；删去附加推算，保留原文披露。"}`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"计算表达式没有支持所声称的基期关系，且用户未要求该派生值；删去附加推算，保留原文披露。"}`),
		sourceConsistencyVerdict(`{"answer":` + strconv.Quote(corrected) + `}`),
		sourceAcceptedVerdict(),
	}}
	e := sourceConsistencyEngine(t, model)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	document := documentNumbersStep("read_document", true, "chunks", []map[string]any{{
		"chunk_id": "source-latency", "knowledge_id": "authorized-doc", "content": quote,
	}})
	calculation := `{"kind":"source_calculation","status":"ok","calculations":[{"expression":"a - b","value":"39","unit":"毫秒","unit_status":"known","inputs":{"a":{"value":"42","unit":"毫秒","chunk_id":"source-latency","quote":"当前延迟42毫秒，较上期减少3毫秒。"},"b":{"value":"3","unit":"毫秒","chunk_id":"source-latency","quote":"当前延迟42毫秒，较上期减少3毫秒。"}}}]}`
	state := &types.AgentState{RoundSteps: []types.AgentStep{document, {ToolCalls: []types.ToolCall{{
		ID: "executed-calculation", Name: "calculate_source_values", Result: &types.ToolResult{Success: true, Output: calculation},
	}}}}}
	// The number is a real executor result; numeric membership alone cannot
	// establish that the formula answers the requested business relationship.
	require.Empty(t, ungroundedStructuredNumbers(candidate, answerNumericEvidence(candidate, state.RoundSteps), state.RoundSteps))
	_, err := e.executeLoop(t.Context(), state, "只转述当前延迟和来源披露的变化。", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Contains(t, state.FinalAnswer, quote)
	require.NotContains(t, state.FinalAnswer, "39")
	require.Len(t, model.calls, 6)
	// The semantic reviewer sees both the actual expression and the source
	// relationship. Its rejection must reach repair before terminal delivery.
	require.Contains(t, model.calls[2][1].Content, "a - b")
	require.Contains(t, model.calls[2][1].Content, quote)
	require.Equal(t, model.calls[2][1].Content, model.calls[3][1].Content, "protocol retry keeps the same candidate and evidence")
	require.Contains(t, model.calls[4][len(model.calls[4])-1].Content, "删去附加推算")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.NotContains(t, visible, "39")
}

func TestSourceConsistencyNormalLoopFailureDoesNotReauditRuntimeFallback(t *testing.T) {
	responses := []mockResponse{sourceConsistencyVerdict("完整的初始候选需要实际来源审核。")}
	for i := 0; i < 3; i++ {
		responses = append(responses, sourceConsistencyVerdict(`{"answer":"来源原文表示面积，但这里将其解释为产量。"}`), sourceConsistencyVerdict(`{"complete":false,"feedback":"不要更换指标含义。"}`))
	}
	m := &mockChat{responses: responses}
	e := sourceConsistencyEngine(t, m)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	state := sourceConsistencyState()
	_, err := e.executeLoop(t.Context(), state, "解释原文指标。", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
	require.Len(t, m.calls, 7, "initial draft, three reviewed candidates and three combined verdicts; no audit of runtime failure notice")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, incompleteAnswerFallback, visible)
}

func documentTableSummaryFixture(t *testing.T, labels []string) *types.AgentState {
	t.Helper()
	rows := []map[string]any{}
	for i, label := range labels {
		rows = append(rows, map[string]any{"row_index": i + 1, "row_label": label, "value": 987654.321, "inputs": map[string]any{"raw": 7654321}, "source_row": "raw numeric source row should not be copied into the identity summary"})
	}
	data := map[string]any{"kind": "source_calculation", "status": "ok", "chunk_id": "read-chunk", "knowledge_id": "read-document", "table_index": 2, "row_start": 1, "row_end": len(labels), "selected_row_count": len(labels), "calculated_row_count": len(labels), "parse_complete": true, "calculations": rows}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	return &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: documentTableCalculationToolName, ID: "table-execution", Result: &types.ToolResult{Success: true, Data: data, Output: string(encoded)}}}}}}
}

func TestDocumentTableConsistencySummaryPreservesDetailAndAggregateLabelsWithoutClassifying(t *testing.T) {
	labels := []string{"Region A", "Region B", "Region C", "All regions"}
	state := documentTableSummaryFixture(t, labels)
	evidence, scope := documentTableConsistencySummaries(state)
	require.True(t, scope.ViewComplete)
	require.Equal(t, 1, scope.IncludedResults)
	require.Len(t, evidence, 1)
	data := evidence[0].Content.(map[string]any)
	require.Equal(t, "read-chunk", data["chunk_id"])
	require.Equal(t, json.Number("2"), data["table_index"])
	require.Equal(t, json.Number("1"), data["row_start"])
	require.Equal(t, json.Number("4"), data["row_end"])
	require.Equal(t, json.Number("4"), data["calculated_row_count"])
	rows := data["executed_rows"].([]any)
	require.Len(t, rows, 4)
	for i, raw := range rows {
		row := raw.(map[string]any)
		require.Equal(t, map[string]any{"row_index": json.Number(strconv.Itoa(i + 1)), "row_label": labels[i]}, row)
	}
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "987654")
	require.NotContains(t, string(encoded), "7654321")
	require.NotContains(t, string(encoded), "detail_count")
	require.NotContains(t, string(encoded), "source_row")
	require.Contains(t, sourceConsistencyReviewInstructions, "already includes an aggregate row")
}

func TestDocumentTableAnalysisFeedsFinalSourceGateAndOnlyExecutedRowSummary(t *testing.T) {
	state := documentTableSummaryFixture(t, []string{"Region A", "All regions"})
	call := &state.RoundSteps[0].ToolCalls[0]
	actual := call.Result.Data
	call.Name = documentTableAnalysisToolName
	call.Result.Success = false // partial batches still retain successful rows
	call.Result.Data = map[string]any{"kind": "document_table_analysis", "status": "partial", "model_advice": "unexecuted count 777", "executions": []any{
		map[string]any{"success": false, "tool_name": documentTableCalculationToolName, "result": map[string]any{"kind": "source_calculation", "status": "ok", "calculated_row_count": 777}},
		map[string]any{"success": true, "tool_name": documentTableCalculationToolName, "result": actual},
	}}
	require.True(t, hasStructuredEvidence(state.RoundSteps))
	evidence, scope := documentTableConsistencySummaries(state)
	require.True(t, scope.ViewComplete)
	require.Len(t, evidence, 1)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "Region A")
	require.Contains(t, string(encoded), "All regions")
	require.NotContains(t, string(encoded), "777")
}

func TestDocumentTableConsistencySummaryIgnoresSchemasInspectFailuresAndOtherTools(t *testing.T) {
	for _, change := range []func(*types.ToolCall){
		func(call *types.ToolCall) {
			call.Name = "structured_data"
			call.Args = map[string]any{"operation": "describe_tools"}
		},
		func(call *types.ToolCall) { call.Result.Success = false },
		func(call *types.ToolCall) { call.Result.Data["kind"] = "document_table_catalog" },
		func(call *types.ToolCall) { call.Result.Data["status"] = "error" },
		func(call *types.ToolCall) {
			call.Result.Data = map[string]any{"tools": []any{map[string]any{"name": documentTableCalculationToolName, "kind": "source_calculation", "status": "ok", "calculated_row_count": 100}}}
		},
		func(call *types.ToolCall) { delete(call.Result.Data, "calculations") },
	} {
		state := documentTableSummaryFixture(t, []string{"Section A", "Total"})
		change(&state.RoundSteps[0].ToolCalls[0])
		evidence, scope := documentTableConsistencySummaries(state)
		require.Empty(t, evidence)
		require.Zero(t, scope.DistinctResults)
		require.True(t, scope.ViewComplete)
	}
	state := documentTableSummaryFixture(t, []string{"Section A", "Total"})
	state.RoundSteps[0].ToolCalls[0].Result.Data = nil
	evidence, scope := documentTableConsistencySummaries(state)
	require.Len(t, evidence, 1, "actual successful execution may retain only canonical output")
	require.True(t, scope.ViewComplete)
}

func TestDocumentTableConsistencySummaryHasIndependentBudgetAndExplicitOmissions(t *testing.T) {
	labels := []string{}
	for i := 0; i < 600; i++ {
		labels = append(labels, "Detail "+strconv.Itoa(i)+strings.Repeat(" source label", 20))
	}
	labels = append(labels, "Aggregate of preceding details")
	state := documentTableSummaryFixture(t, labels)
	evidence, scope := documentTableConsistencySummaries(state)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 8192)
	require.False(t, scope.ViewComplete)
	require.Contains(t, string(encoded), "omitted_paths")
	require.Equal(t, len(labels), len(state.RoundSteps[0].ToolCalls[0].Result.Data["calculations"].([]map[string]any)), "summary projection cannot mutate actual execution rows")
}

func TestSourceCheckerReceivesCompleteIndependentTableIdentitySummaryDespiteGeneralEvidencePressure(t *testing.T) {
	labels := []string{"Unit A", "Unit B", "Unit C", "Combined units"}
	state := documentTableSummaryFixture(t, labels)
	for i := 0; i < 35; i++ {
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "schema-" + strconv.Itoa(i), Name: "structured_data", Result: &types.ToolResult{Success: true, Output: strings.Repeat("schema documentation ", 10000)}}}})
	}
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"计算行数已包括汇总行，不应再把汇总额外加到明细计数。"}`)}}
	engine := sourceConsistencyEngine(t, model)
	repair, err := engine.answerNeedsRepair(t.Context(), "说明实际记录范围。", "本表包含四条明细以及汇总。", "", state)
	require.NoError(t, err)
	require.True(t, repair)
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	scope := input["document_table_execution_scope"].(map[string]any)
	require.Equal(t, true, scope["view_complete"])
	summary := input["document_table_execution_summary"].([]any)
	require.Len(t, summary, 1)
	content := summary[0].(map[string]any)["content"].(map[string]any)
	require.Len(t, content["executed_rows"], 4)
	require.Equal(t, float64(4), content["calculated_row_count"])
	for _, label := range labels {
		require.Contains(t, model.calls[0][1].Content, label)
	}
	require.NotContains(t, content, "detail_count", "the model decides detail/aggregate meaning from actual labels, not a business rule")
	require.Contains(t, engine.answerRepairAdvice(), "非新事实")
}
