package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
)

const renderChartToolName = agenttools.ToolRenderChart

type renderChartTool struct {
	agenttools.BaseTool
	steps                   func() []types.AgentStep
	writer                  chart.Writer
	tenant                  uint64
	owner, session, message string
}

func (e *AgentEngine) SetChartWriter(writer chart.Writer) {
	if e != nil {
		e.chartWriter = writer
	}
}
func newRenderChartTool(ctx context.Context, steps func() []types.AgentStep, writer chart.Writer, session, message string) *renderChartTool {
	return &renderChartTool{BaseTool: agenttools.NewBaseTool(renderChartToolName,
		"根据本轮成功读取的真实数值表格确定性绘图，保存可预览和下载的PNG图片。先inspect核对真实source_id/列索引/单位/范围，再render选择source_id、x_column、y_columns、chart_type(line/bar/scatter)、x_type(category/numeric/time)、简短title。依据完整问题与对话选择来源和图型，不提交任意数字、代码、URL或推造日期，缺失留空断线，不平滑补值；单位未知保持未知，多个不同单位需分图。图内仅放标题、坐标、图例和必要标注，不放原始表、技术JSON、解释段落。delivery_mode默认chart_only：用户只要求图时最终回复仅包含返回的图片Markdown，不重复价格、统计、过程、来源解释或后续建议；只有用户明确同时要求文字分析时才选择chart_with_analysis并简短回答该分析。内部来源与范围在工具审计中保留，不需要复述到聊天。保存失败不能声称已交付。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["inspect","render"]},"source_id":{"type":"string","maxLength":128},"chart_type":{"type":"string","enum":["line","bar","scatter"]},"x_column":{"type":"integer","minimum":0},"y_columns":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"integer","minimum":0}},"x_type":{"type":"string","enum":["category","numeric","time"]},"title":{"type":"string","maxLength":512},"delivery_mode":{"type":"string","enum":["chart_only","chart_with_analysis"],"description":"根据完整请求选择：默认chart_only，只交付图片；明确同时要求文字分析才用chart_with_analysis。"}}}`)), steps: steps, writer: writer, tenant: types.CallerFromContext(ctx).TenantID, owner: types.SessionOwnerIDFromContext(ctx), session: session, message: message}
}
func (t *renderChartTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	fail := func(message string) (*types.ToolResult, error) {
		return &types.ToolResult{Success: false, Error: message, Data: map[string]any{"kind": "data_chart", "status": "error"}}, nil
	}
	meta, ok := agenttools.ToolExecFromContext(ctx)
	if !ok || meta.SessionID != t.session || meta.AssistantMessageID != t.message || meta.ToolCallID == "" || t.session == "" || t.message == "" || t.tenant == 0 || types.CallerFromContext(ctx).TenantID != t.tenant || t.owner == "" || types.SessionOwnerIDFromContext(ctx) != t.owner {
		return fail("绘图需要本轮已认证会话和工具执行范围")
	}
	if len(raw) > 8192 || t.steps == nil {
		return fail("绘图计划或真实工具记录无效")
	}
	var input struct {
		Operation    string `json:"operation"`
		SourceID     string `json:"source_id"`
		Type         string `json:"chart_type"`
		X            *int   `json:"x_column"`
		Y            []int  `json:"y_columns"`
		XType        string `json:"x_type"`
		Title        string `json:"title"`
		DeliveryMode string `json:"delivery_mode"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail("绘图参数必须为单个合法对象，不接受数值、代码、URL或身份参数")
	}
	if input.Operation != "inspect" && input.Operation != "render" {
		return fail("operation必须为inspect或render")
	}
	if input.DeliveryMode == "" {
		input.DeliveryMode = "chart_only"
	}
	if input.DeliveryMode != "chart_only" && input.DeliveryMode != "chart_with_analysis" {
		return fail("delivery_mode必须为chart_only或chart_with_analysis")
	}
	tables, omitted, rejected := chartSourcesDetailed(t.steps())
	if input.Operation == "inspect" {
		rejectedCount := omitted
		diagnostics, _ := json.Marshal(rejected)
		views := []any{}
		for _, table := range tables {
			views = append(views, map[string]any{"source_id": table.ID, "columns": table.Columns, "row_count": len(table.Rows), "sample_rows": table.Rows[:min(5, len(table.Rows))], "source_truncated": table.Truncated, "provenance": table.Provenance, "notice": table.Notice})
		}
		for len(views) > 0 {
			encoded, _ := json.Marshal(views)
			if len(encoded)+len(diagnostics) <= 62*1024 {
				break
			}
			views = views[:len(views)-1]
			omitted++
		}
		data := map[string]any{"kind": "chart_source_catalog", "sources": views, "omitted_source_count": omitted, "rejected_source_count": rejectedCount, "source_rejections": rejected, "source_rejections_truncated": rejectedCount > len(rejected), "notice": "source_id仅来自本轮实际成功查询的观测/分析结果或已读原文表格；目录与元数据不可绘图。render必须使用sources[].source_id，分析result_id/resolution_id不是绘图句柄。全部索引0起算。sample_rows仅为展示示例，绘图使用该句柄的全部实际返回行（单表最多2000行），不是重新抄写数值。空值不补零，单位不推断。目录为空但source_rejections非空表示真实结果校验失败，不表示没有查询过数据；按具体拒绝原因处理，不循环猜句柄。"}
		body, _ := json.Marshal(data)
		return &types.ToolResult{Success: true, Data: data, Output: string(body)}, nil
	}
	if input.SourceID == "" || len(input.SourceID) > 128 || input.X == nil || t.writer == nil {
		return fail("render需要inspect返回的真实source_id、X/Y列和可用附件服务")
	}
	var source *chart.Table
	for i := range tables {
		if tables[i].ID == input.SourceID {
			source = &tables[i]
			break
		}
	}
	if source == nil {
		result, _ := fail("source_id不是本轮成功读取的数值表格；使用inspect的sources[].source_id，不使用分析result_id/resolution_id")
		available := make([]string, 0, len(tables))
		for _, table := range tables {
			available = append(available, table.ID)
		}
		result.Data["available_source_ids"] = available
		return result, nil
	}
	title := input.Title
	if title == "" {
		title = "数据图表"
	}
	png, err := chart.Render(*source, chart.Spec{Type: input.Type, X: *input.X, Y: input.Y, XType: input.XType, Title: title})
	if err != nil {
		return fail(err.Error())
	}
	delivered, err := t.writer.SaveChart(ctx, chart.SaveRequest{SessionID: t.session, MessageID: t.message, ToolCallID: meta.ToolCallID, FileName: "chart.png", SourceID: source.ID, PNG: png})
	if err != nil {
		return fail("图表附件保存失败，未交付可下载文件")
	}
	if delivered == nil {
		return fail("图表附件服务没有返回有效持久化资源")
	}
	if _, ok := types.ParseResourcePath(delivered.Handle); !ok || !strings.HasSuffix(delivered.FileName, ".png") || delivered.Index < 0 {
		return fail("图表附件服务没有返回有效持久化资源")
	}
	data := map[string]any{"kind": "data_chart", "status": "ok", "artifact": delivered, "delivery_mode": input.DeliveryMode, "source_id": source.ID, "row_count": len(source.Rows), "source_truncated": source.Truncated, "provenance": source.Provenance}
	return &types.ToolResult{Success: true, Data: data, Output: fmt.Sprintf("![图表](%s)", delivered.Handle)}, nil
}

// Only successful numeric query executions and fetched document bodies are
// eligible. Tool directories, schemas, model prose and arbitrary nested rows
// are never discovered by a permissive recursive walk.
func chartSources(steps []types.AgentStep) ([]chart.Table, int) {
	tables, omitted, _ := chartSourcesDetailed(steps)
	return tables, omitted
}

func chartSourcesDetailed(steps []types.AgentStep) ([]chart.Table, int, []map[string]any) {
	tables := []chart.Table{}
	rejected := []map[string]any{}
	seen := map[string]bool{}
	omitted := 0
	reject := func(call types.ToolCall, reason string) {
		omitted++
		if len(rejected) < chart.MaxSources {
			id := call.ID
			if len(id) > 256 {
				digest := sha256.Sum256([]byte(id))
				id = "sha256:" + hex.EncodeToString(digest[:])
			}
			rejected = append(rejected, map[string]any{"tool_call_id": id, "tool_name": call.ExecutionName(), "reason": reason})
		}
	}
	add := func(table chart.Table, identity string, call types.ToolCall) {
		if err := table.Seal(identity); err != nil {
			reject(call, err.Error())
			return
		}
		numeric := false
		for _, column := range table.Columns {
			numeric = numeric || column.Numeric
		}
		if !numeric {
			reject(call, "source_has_no_numeric_column")
			return
		}
		if seen[table.ID] {
			return
		}
		seen[table.ID] = true
		if len(tables) >= chart.MaxSources {
			reject(call, "chart_source_catalog_capacity_exceeded")
			return
		}
		tables = append(tables, table)
	}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			if isStructuredQueryExecution(call) {
				if table, err := checkedStructuredChartTable(call); err == nil {
					add(table, call.ID+"/structured", call)
				} else {
					reject(call, err.Error())
				}
			}
			actual := call
			actual.Name = call.ExecutionName()
			for _, chunk := range fetchedDocumentChunks(actual) {
				if chunk.ID == "" || chunk.KnowledgeID == "" {
					continue
				}
				for index, table := range documentSourceTables(chunk.Content) {
					if len(table.Rows) < 2 {
						continue
					}
					columns := make([]chart.Column, len(table.Rows[0]))
					for i, name := range table.Rows[0] {
						columns[i] = chart.Column{Name: name}
					}
					provenance := fmt.Sprintf("knowledge_id: %s\nchunk_id: %s\ntable_index: %d\nunit_context: %s", chunk.KnowledgeID, chunk.ID, index, table.Context)
					add(chart.Table{Columns: columns, Rows: table.Rows[1:], Provenance: provenance, Truncated: !table.ParseComplete, Notice: "仅本轮已读分块中的解析表格，不代表整份文档覆盖。原表头保留；单位未单独确认。" + table.ParseWarning}, call.ID+"/"+chunk.ID+fmt.Sprint(index), call)
				}
			}
		}
	}
	return tables, omitted, rejected
}
func structuredChartTable(call types.ToolCall) (chart.Table, bool) {
	table, err := checkedStructuredChartTable(call)
	return table, err == nil
}

func checkedStructuredChartTable(call types.ToolCall) (chart.Table, error) {
	if call.Result == nil || !call.Result.Success {
		return chart.Table{}, fmt.Errorf("structured_execution_not_successful")
	}
	raw, err := json.Marshal(call.Result.Data)
	if err != nil || len(raw) > chart.MaxSourceBytes {
		return chart.Table{}, fmt.Errorf("structured_source_payload_over_budget")
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil || data["status"] != "ok" {
		return chart.Table{}, fmt.Errorf("structured_result_not_ok")
	}
	rows, ok := data["rows"].([]any)
	if !ok || len(rows) == 0 || len(rows) > chart.MaxRows {
		return chart.Table{}, fmt.Errorf("structured_rows_missing_or_over_budget")
	}
	// Observation metadata alone and aggregate source examples are not a series.
	analysis := data["kind"] == "structured_analysis"
	if !analysis && data["rows_are"] != "observations" {
		return chart.Table{}, fmt.Errorf("structured_rows_are_not_observations")
	}
	source, ok := data["source"].(map[string]any)
	digest, _ := source["sha256"].(string)
	file, _ := source["file"].(string)
	decoded, decodeErr := hex.DecodeString(digest)
	sourceJSON, sourceErr := json.Marshal(source)
	if !ok || decodeErr != nil || len(decoded) != 32 || strings.TrimSpace(file) == "" || len(file) > 1024 || sourceErr != nil || len(sourceJSON) > 4096 {
		return chart.Table{}, fmt.Errorf("structured_source_identity_invalid_or_over_budget")
	}
	columns := []string{}
	matrix := [][]string{}
	if analysis {
		var valid bool
		columns, valid = structuredAnalysisColumns(data["columns"], rows)
		if !valid {
			return chart.Table{}, fmt.Errorf("structured_analysis_row_shape_invalid")
		}
		for _, rawRow := range rows {
			values := rawRow.([]any)
			row := []string{}
			for _, value := range values {
				text, e := chart.Cell(value)
				if e != nil {
					return chart.Table{}, fmt.Errorf("structured_cell_type_invalid")
				}
				row = append(row, text)
			}
			matrix = append(matrix, row)
		}
	} else {
		first, ok := rows[0].(map[string]any)
		if !ok {
			return chart.Table{}, fmt.Errorf("structured_observation_row_shape_invalid")
		}
		for key := range first {
			columns = append(columns, key)
		}
		sort.Strings(columns)
		for _, rawRow := range rows {
			object, ok := rawRow.(map[string]any)
			if !ok || len(object) != len(columns) {
				return chart.Table{}, fmt.Errorf("structured_observation_row_shape_invalid")
			}
			row := []string{}
			for _, name := range columns {
				value, exists := object[name]
				if !exists {
					return chart.Table{}, fmt.Errorf("structured_observation_row_shape_invalid")
				}
				text, e := chart.Cell(value)
				if e != nil {
					return chart.Table{}, fmt.Errorf("structured_cell_type_invalid")
				}
				row = append(row, text)
			}
			matrix = append(matrix, row)
		}
	}
	cols := make([]chart.Column, len(columns))
	series, _ := data["series"].(map[string]any)
	for i, name := range columns {
		cols[i] = chart.Column{Name: name}
		if !analysis && name == "value" {
			cols[i].Unit, _ = series["unit"].(string)
		}
	}
	meta := map[string]any{}
	for _, key := range []string{"kind", "source", "dataset_id", "series", "filters", "requested_range", "actual_first_date", "actual_last_date", "record_count", "scope_record_count", "missing_count", "inputs", "sql", "result_id", "warnings", "rows_are", "input_complete", "input_count", "output_truncated", "truncated", "returned_row_count"} {
		if value, exists := data[key]; exists {
			meta[key] = value
		}
	}
	provenance, partial, err := chartAuditMetadata(meta)
	if err != nil {
		return chart.Table{}, err
	}
	truncationKey := "truncated"
	if analysis {
		truncationKey = "output_truncated"
	}
	truncated, known := data[truncationKey].(bool)
	notice := "仅绘制本次工具实际返回行；未从目录或模型记忆补充数值。"
	if partial {
		notice += " 输入审计元数据为部分投影，完整元数据已由SHA256绑定；观测行及输入/输出完整性标记未改写。"
	}
	if data["input_complete"] == false {
		truncated = true
		notice += " 分析输入未完整，不能宣称完整范围。"
	}
	if !known {
		truncated = true
		notice += " 来源未明确完整性，仅视为部分返回结果。"
	}
	return chart.Table{Columns: cols, Rows: matrix, Provenance: provenance, Truncated: truncated, Notice: notice}, nil
}

// The full canonical metadata digest binds omitted audit details without
// replacing source rows or enlarging Table.Seal's independent 16 KiB bound.
func chartAuditMetadata(meta map[string]any) (string, bool, error) {
	const budget = 16 * 1024
	full, err := json.Marshal(meta)
	if err != nil {
		return "", false, fmt.Errorf("structured_audit_encoding_invalid")
	}
	if len(full) <= budget {
		return string(full), false, nil
	}
	inputs, ok := meta["inputs"].(map[string]any)
	if !ok || len(inputs) == 0 {
		return "", false, fmt.Errorf("structured_core_audit_over_budget")
	}
	inputBytes, err := json.Marshal(inputs)
	if err != nil {
		return "", false, fmt.Errorf("structured_input_audit_invalid")
	}
	summary := make(map[string]any, len(inputs))
	for id, value := range inputs {
		input, ok := value.(map[string]any)
		if !ok {
			return "", false, fmt.Errorf("structured_input_audit_shape_invalid")
		}
		view := map[string]any{}
		for _, key := range []string{"source", "unit", "unit_status", "input_count", "valid_count", "actual_first_date", "actual_last_date", "valid_first_date", "valid_last_date", "observation_coverage_scope"} {
			if value, exists := input[key]; exists {
				view[key] = value
			}
		}
		if series, ok := input["series"].(map[string]any); ok {
			identity := map[string]any{}
			for _, key := range []string{"series_id", "sheet", "metric", "unit", "unit_status", "header_cells", "date_mode", "first_date", "last_date"} {
				if value, exists := series[key]; exists {
					identity[key] = value
				}
			}
			view["series"] = identity
		}
		summary[id] = view
	}
	projected := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		projected[key] = value
	}
	projected["inputs"] = summary
	allSHA, inputSHA := sha256.Sum256(full), sha256.Sum256(inputBytes)
	projected["audit_projection"] = map[string]any{"projection_partial": true, "encoding": "canonical_json", "original_bytes": len(full), "sha256": hex.EncodeToString(allSHA[:]), "inputs_original_bytes": len(inputBytes), "inputs_sha256": hex.EncodeToString(inputSHA[:]), "input_count": len(inputs), "notice": "inputs仅展示全部输入的核心来源/单位/范围引用；省略的覆盖细节仍由完整SHA256绑定，不能据投影推断其不存在或完整覆盖。"}
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > budget {
		return "", false, fmt.Errorf("structured_core_audit_over_budget")
	}
	return string(encoded), true, nil
}

const dataChartInstructions = `render_chart从已授权且本轮真正读取的typed行或原文表格生成PNG图片，inspect核对真实句柄、列、单位和范围，render确定性绘制。保持原值、真实日期与缺失，不平滑补数，不用generate_image编造数据。图内只需标题、图形、坐标、图例和必要范围标注，技术来源和审计不进图片。根据完整用户请求选择delivery_mode：只要绘图时默认chart_only，最终回答只输出成功返回的图片Markdown，可交付多张图；不额外重复数据表、涨跌统计、来源说明、方法介绍、局限段落或后续建议。明确同时要求文字分析时用chart_with_analysis，只补用户要求的分析。失败或数据不足时简短说明实际缺口，不伪造图片或已完成状态。`
