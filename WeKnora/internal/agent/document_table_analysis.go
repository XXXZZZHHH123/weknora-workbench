package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const documentTableAnalysisToolName = "analyze_document_tables"

type documentTablePlanFunc func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error)

type documentTableAnalysisTool struct {
	agenttools.BaseTool
	steps    func() []types.AgentStep
	question func() string
	planner  documentTablePlanFunc
}

type documentTableAnalysisPlan struct {
	Status  string                 `json:"status"`
	Message string                 `json:"message"`
	Plans   []documentTableRequest `json:"plans"`
}

// The trusted question is supplied by the server, never by tool arguments.
// A caller can wrap planner to apply request-local codecs, model capability
// settings and actual usage accounting without changing source execution.
func newDocumentTableAnalysisTool(steps func() []types.AgentStep, question func() string, planner documentTablePlanFunc) *documentTableAnalysisTool {
	return &documentTableAnalysisTool{BaseTool: agenttools.NewBaseTool(documentTableAnalysisToolName,
		"根据服务器保存的完整当前问题及真实已读文档表格，一次模型计划后批量执行1至4组来源绑定计算。无需手写每行原值或反复猜列。只从本轮实际成功读取的表格选源、行列、单位及公式，复用calculate_document_table全部校验；原值与计算结果保留引用。参数仅为空对象{}；分析目标只能来自服务器的真人原问题和追加指令，不接收主模型扩写的子任务。证据不足时返回needs_source/clarify；部分失败不冒充完整成功。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)), steps: steps, question: question, planner: planner}
}

var documentTableAnalysisPlanSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["status","message","plans"],"properties":{"status":{"type":"string","enum":["ready","needs_source","clarify"]},"message":{"type":"string","maxLength":2048},"plans":{"type":"array","maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["operation","chunk_id","table_index","row_start","row_end","columns","expression","result_unit"],"properties":{"operation":{"type":"string","enum":["calculate"]},"chunk_id":{"type":"string","minLength":1,"maxLength":256},"table_index":{"type":"integer","minimum":0},"row_start":{"type":"integer","minimum":0},"row_end":{"type":"integer","minimum":0},"columns":{"type":"object","minProperties":1,"maxProperties":16,"additionalProperties":{"type":"object","additionalProperties":false,"required":["column","unit"],"properties":{"column":{"type":"integer","minimum":0},"unit":{"type":"string","maxLength":128}}}},"expression":{"type":"string","minLength":1,"maxLength":1024},"result_unit":{"type":"string","enum":["","%"]}}}}}}`)

const documentTableAnalysisInstructions = `Generate a source-bound document table calculation plan, never a final answer. The server_trusted_request is the complete actual current user request with its trusted context and source restrictions; interpret all of it. Only the server-provided human task defines outputs; there is no main-model subtask text and no authority to add analyses. Choose the necessary source tables, rows, columns, formulas and exact units from the successful current-turn document table catalog. Do not classify the task using keyword rules or assume a metric, denominator, year, unit, label mapping or source not in the actual evidence.
The catalog contains actual parsed table rows including headers; indexes are zero-based, excluding the Markdown separator. Metadata and rows are independently bounded: inspect scope/view_complete/omitted_paths and parse_complete. Missing projected contents are unknown, not absent. Only select tables whose full rows are present in table_rows. If needed table rows or required source context are missing, truncated, or ambiguous, return needs_source or clarify with a concise explanation, no plans and no factual conclusion. Parsed completeness is not completeness of a document or requested business scope. Text/OCR table extraction is not proof of the original image layout.
For a ready plan return 1 to 4 calculate_document_table requests. Reuse a formula across a genuine selected row range rather than expanding a plan per row; include requested detail and aggregate rows appropriately, without counting an aggregate as another detail record. Bind only columns actually used in the expression; do not pad expressions with zero times unrelated columns to defeat unit checks. Select the expression from the requested metric meaning and actual header/unit context. Preserve original values through execution rather than copying observed values into a formula. Only variable names, parentheses, + - * / and constants 0/1 are permitted. Nonempty units must appear in the table's unit_context. Unit scaling is handled by the existing calculator; result_unit=% already scales a dimensionless result, so do not multiply by 100. Unknown units must remain empty and cannot produce a percentage. All source, unit, range and arithmetic checks will run unchanged. Plans and explanations are not source evidence. Do not execute tools. Output only the required JSON status/message/plans object.`

func documentTableIdentity(chunk string, table int) string { return chunk + "/" + strconv.Itoa(table) }

// All successful fetched document tables enter the catalog. Projection is
// structural and independent of question words; omissions never authorize a
// guessed plan. Full-table eligibility is checked again before execution.
func documentTableAnalysisCatalog(steps []types.AgentStep) ([]reviewToolEvidence, reviewEvidenceScope, []reviewToolEvidence, reviewEvidenceScope, map[string]bool) {
	metadata, rows := []chat.Message{}, []chat.Message{}
	seen := map[string]bool{}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			for _, chunk := range fetchedDocumentChunks(call) {
				if chunk.ID == "" || chunk.KnowledgeID == "" {
					continue
				}
				for index, table := range documentSourceTables(chunk.Content) {
					key := documentTableIdentity(chunk.ID, index)
					if seen[key] {
						continue
					}
					seen[key] = true
					view := map[string]any{"chunk_id": chunk.ID, "knowledge_id": chunk.KnowledgeID, "table_index": index, "row_count": len(table.Rows), "column_count": len(table.Rows[0]), "unit_context": table.Context, "parse_complete": table.ParseComplete, "parse_warning": table.ParseWarning}
					if call.Result != nil {
						if scope, ok := call.Result.Data["document_scope"]; ok {
							view["document_scope"] = scope
						}
					}
					encoded, _ := json.Marshal(view)
					metadata = append(metadata, chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(encoded)})
					full := map[string]any{}
					for name, value := range view {
						full[name] = value
					}
					full["rows"] = table.Rows
					encoded, _ = json.Marshal(full)
					rows = append(rows, chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(encoded)})
				}
			}
		}
	}
	meta, metaScope := boundedReviewEvidence(metadata, 8192)
	rowViews, rowScope := boundedReviewEvidence(rows, 48000)
	complete := map[string]bool{}
	for _, entry := range rowViews {
		if !entry.ViewComplete {
			continue
		}
		data, ok := entry.Content.(map[string]any)
		if !ok {
			continue
		}
		chunk, ok := data["chunk_id"].(string)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(fmt.Sprint(data["table_index"]))
		if err != nil {
			continue
		}
		complete[documentTableIdentity(chunk, index)] = true
	}
	return meta, metaScope, rowViews, rowScope, complete
}

func documentTableAnalysisResult(status, message string, executions []map[string]any) *types.ToolResult {
	data := map[string]any{"kind": "document_table_analysis", "status": status, "executions": executions, "notice": "只包含实际执行结果；计划和模型解释不是事实依据。execution_complete仅指所提计划的技术执行，不等于原文或用户问题全部覆盖。部分失败不可说成完整成功，须保留每项真实范围、失败行及来源。"}
	if message != "" {
		data["model_advice"] = message
		data["advice_notice"] = "模型意见或运行时说明，不是新事实或来源。"
	}
	data["execution_complete"] = status == "ok"
	encoded, _ := json.Marshal(data)
	result := &types.ToolResult{Success: status == "ok" || status == "needs_source" || status == "clarify", Data: data, Output: string(encoded)}
	if status == "error" || status == "partial" {
		result.Error = "文档表格计划未全部执行成功；请依据实际失败及范围处理，不得把部分结果当完整结论"
	}
	return result
}

func (tool *documentTableAnalysisTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	fail := func(message string) (*types.ToolResult, error) {
		return documentTableAnalysisResult("error", message, nil), nil
	}
	if len(raw) > 4096 || tool.steps == nil || tool.question == nil || tool.planner == nil {
		return fail("需要实际来源、服务器原问题和可用计划模型")
	}
	var args map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || args == nil || len(args) != 0 || decoder.Decode(new(any)) != io.EOF {
		return fail("参数只允许空对象{}；不能传入替换问题、子任务、计划或来源")
	}
	query := tool.question()
	if strings.TrimSpace(query) == "" || len(query) > 65536 {
		return fail("服务器完整问题不可用或超过计划请求预算；未截断或替换原问题")
	}
	steps := tool.steps()
	metadata, metadataScope, rows, rowScope, eligible := documentTableAnalysisCatalog(steps)
	if metadataScope.DistinctResults == 0 {
		return documentTableAnalysisResult("needs_source", "本轮没有成功读取并解析的实际文档表格；先取得必要正文或表格来源。", nil), nil
	}
	input, err := json.Marshal(map[string]any{"server_trusted_request": query, "table_metadata": metadata, "metadata_scope": metadataScope, "table_rows": rows, "table_rows_scope": rowScope})
	if err != nil {
		return fail("实际表格目录无法编码")
	}
	planCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	disabled := false
	// Include the same contract in the isolated prompt: compatible providers
	// may honor JSON mode without exposing every schema property to the model.
	response, err := tool.planner(planCtx, []chat.Message{{Role: "system", Content: documentTableAnalysisInstructions + "\nRequired JSON response schema:\n" + string(documentTableAnalysisPlanSchema)}, {Role: "user", Content: string(input)}}, &chat.ChatOptions{Temperature: 0, MaxCompletionTokens: 2048, ToolChoice: "none", Thinking: &disabled, ReasoningEffort: chat.ReasoningEffort("off"), ThinkingBudgetTokens: 0, Format: documentTableAnalysisPlanSchema})
	if err != nil {
		return fail("局部表格计划模型调用失败：" + err.Error())
	}
	if planCtx.Err() != nil {
		return fail("局部表格计划超时或已取消；没有执行计划")
	}
	if response == nil || len(response.ToolCalls) > 0 || (response.FinishReason != "" && !isNaturalStopFinishReason(response.FinishReason)) {
		return fail("局部模型没有正常完成合法计划")
	}
	if len(response.Content) > 32768 {
		return fail("局部表格计划超过技术预算")
	}
	var plan documentTableAnalysisPlan
	decoder = json.NewDecoder(strings.NewReader(response.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return fail("局部模型计划不符合严格JSON协议：" + err.Error())
	}
	if decoder.Decode(new(any)) != io.EOF || len(plan.Message) > 8192 || len(plan.Plans) > 4 {
		return fail("局部模型计划不符合严格JSON协议")
	}
	if plan.Status == "needs_source" || plan.Status == "clarify" {
		if len(plan.Plans) != 0 || strings.TrimSpace(plan.Message) == "" {
			return fail("来源不足或待澄清计划不能夹带执行请求，且必须说明缺口")
		}
		return documentTableAnalysisResult(plan.Status, plan.Message, nil), nil
	}
	if plan.Status != "ready" || len(plan.Plans) < 1 {
		return fail("需要1至4项来源绑定计划，或明确needs_source/clarify")
	}
	for _, request := range plan.Plans {
		if request.Operation != "calculate" || request.TableIndex == nil {
			return fail("批量分析只允许对真实表索引执行calculate计划")
		}
		if !eligible[documentTableIdentity(request.ChunkID, *request.TableIndex)] {
			return documentTableAnalysisResult("needs_source", "所选表没有完整进入本次有界目录；不能依据裁剪行或未读来源执行，请补齐所需来源范围。", nil), nil
		}
	}
	executor := newDocumentTableCalculationTool(tool.steps)
	executions := []map[string]any{}
	complete := true
	successful := 0
	for _, request := range plan.Plans {
		if ctx.Err() != nil {
			return fail("任务已取消；未执行剩余计划")
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			return fail("计算计划无法编码")
		}
		result, executionErr := executor.Execute(ctx, encoded)
		if executionErr != nil || result == nil {
			complete = false
			executions = append(executions, map[string]any{"success": false, "tool_name": documentTableCalculationToolName, "error": "计算执行失败，未产生可采纳结果"})
			continue
		}
		execution := map[string]any{"success": result.Success, "tool_name": documentTableCalculationToolName, "result": result.Data}
		if !result.Success {
			execution["attempted_request"] = request
			execution["diagnostic_notice"] = "仅为失败计划的审计信息，不是已执行来源或数值依据。"
		}
		executions = append(executions, execution)
		if !result.Success {
			complete = false
			continue
		}
		successful++
		selected, selectedOK := result.Data["selected_row_count"].(int)
		calculated, calculatedOK := result.Data["calculated_row_count"].(int)
		failures, _ := result.Data["failed_rows"].([]map[string]any)
		if !selectedOK || !calculatedOK || selected != calculated || len(failures) > 0 {
			complete = false
		}
	}
	status := "ok"
	if !complete {
		status = "partial"
		if successful == 0 {
			status = "error"
		}
	}
	return documentTableAnalysisResult(status, "", executions), nil
}
