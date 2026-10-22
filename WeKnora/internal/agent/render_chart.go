package agent

import (
	"context"
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
		"根据本轮成功读取的真实数值表格确定性绘图并保存可预览/下载的HTML附件，不调用AI生图、不联网、不执行脚本。先operation=inspect查看真实source_id、列索引、单位、行数、截断和来源；再operation=render选择source_id、x_column、y_columns、chart_type(line/bar/scatter)、x_type(category/numeric/time)、title。模型依据完整请求选择图型、来源与列，不能提交任意数字、推造日期或把目录/元数据当事实。time仅接受源表真实ISO日期，category保持原标签及行序。数值不改写/平滑/补值，空值断线；截断数据明显标注部分范围。单位未知保持未知，多个不同单位需分图。不提供原件图像复刻；实际数据不足时先查询/读取来源，再inspect。保存成功后用返回的resource链接展示文件预览入口。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["inspect","render"]},"source_id":{"type":"string","maxLength":128},"chart_type":{"type":"string","enum":["line","bar","scatter"]},"x_column":{"type":"integer","minimum":0},"y_columns":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"integer","minimum":0}},"x_type":{"type":"string","enum":["category","numeric","time"]},"title":{"type":"string","maxLength":512}}}`)), steps: steps, writer: writer, tenant: types.CallerFromContext(ctx).TenantID, owner: types.SessionOwnerIDFromContext(ctx), session: session, message: message}
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
		Operation string `json:"operation"`
		SourceID  string `json:"source_id"`
		Type      string `json:"chart_type"`
		X         *int   `json:"x_column"`
		Y         []int  `json:"y_columns"`
		XType     string `json:"x_type"`
		Title     string `json:"title"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail("绘图参数必须为单个合法对象，不接受数值、代码、URL或身份参数")
	}
	if input.Operation != "inspect" && input.Operation != "render" {
		return fail("operation必须为inspect或render")
	}
	tables, omitted := chartSources(t.steps())
	if input.Operation == "inspect" {
		views := []any{}
		for _, table := range tables {
			views = append(views, map[string]any{"source_id": table.ID, "columns": table.Columns, "row_count": len(table.Rows), "sample_rows": table.Rows[:min(5, len(table.Rows))], "source_truncated": table.Truncated, "provenance": table.Provenance, "notice": table.Notice})
		}
		for len(views) > 0 {
			encoded, _ := json.Marshal(views)
			if len(encoded) <= 64*1024 {
				break
			}
			views = views[:len(views)-1]
			omitted++
		}
		data := map[string]any{"kind": "chart_source_catalog", "sources": views, "omitted_source_count": omitted, "notice": "source_id仅来自本轮实际成功查询的观测/分析结果或已读原文表格；目录与元数据不可绘图。全部索引0起算。sample_rows仅为展示示例，绘图使用该句柄的全部实际返回行（单表最多2000行），不是重新抄写数值。空值不补零，单位不推断。没有合适句柄时先读取实际来源。"}
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
		return fail("source_id不是本轮成功读取的数值表格；先inspect获取真实句柄")
	}
	title := input.Title
	if title == "" {
		title = "数据图表"
	}
	html, err := chart.Render(*source, chart.Spec{Type: input.Type, X: *input.X, Y: input.Y, XType: input.XType, Title: title})
	if err != nil {
		return fail(err.Error())
	}
	delivered, err := t.writer.SaveChart(ctx, chart.SaveRequest{SessionID: t.session, MessageID: t.message, ToolCallID: meta.ToolCallID, FileName: "chart.html", SourceID: source.ID, HTML: html})
	if err != nil {
		return fail("图表附件保存失败，未交付可下载文件")
	}
	if delivered == nil || !strings.HasPrefix(delivered.Handle, "resource://") || delivered.FileName == "" || delivered.Index < 0 {
		return fail("图表附件服务没有返回有效持久化资源")
	}
	data := map[string]any{"kind": "data_chart", "status": "ok", "artifact": delivered, "source_id": source.ID, "row_count": len(source.Rows), "source_truncated": source.Truncated, "provenance": source.Provenance}
	return &types.ToolResult{Success: true, Data: data, Output: fmt.Sprintf("已按真实来源数据绘图并保存。打开预览或下载：[%s](%s)。本图是确定性重绘，不是原件原图。来源截断=%t；图内保留来源、范围、原值核对表和缺失说明。", delivered.FileName, delivered.Handle, source.Truncated)}, nil
}

// Only successful numeric query executions and fetched document bodies are
// eligible. Tool directories, schemas, model prose and arbitrary nested rows
// are never discovered by a permissive recursive walk.
func chartSources(steps []types.AgentStep) ([]chart.Table, int) {
	tables := []chart.Table{}
	seen := map[string]bool{}
	omitted := 0
	add := func(table chart.Table, identity string) {
		if err := table.Seal(identity); err != nil {
			omitted++
			return
		}
		numeric := false
		for _, column := range table.Columns {
			numeric = numeric || column.Numeric
		}
		if !numeric {
			omitted++
			return
		}
		if seen[table.ID] {
			return
		}
		seen[table.ID] = true
		if len(tables) >= chart.MaxSources {
			omitted++
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
				if table, ok := structuredChartTable(call); ok {
					add(table, call.ID+"/structured")
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
					add(chart.Table{Columns: columns, Rows: table.Rows[1:], Provenance: provenance, Truncated: !table.ParseComplete, Notice: "仅本轮已读分块中的解析表格，不代表整份文档覆盖。原表头保留；单位未单独确认。" + table.ParseWarning}, call.ID+"/"+chunk.ID+fmt.Sprint(index))
				}
			}
		}
	}
	return tables, omitted
}
func structuredChartTable(call types.ToolCall) (chart.Table, bool) {
	raw, err := json.Marshal(call.Result.Data)
	if err != nil || len(raw) > chart.MaxSourceBytes {
		return chart.Table{}, false
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil || data["status"] != "ok" {
		return chart.Table{}, false
	}
	rows, ok := data["rows"].([]any)
	if !ok || len(rows) == 0 || len(rows) > chart.MaxRows {
		return chart.Table{}, false
	}
	// Observation metadata alone and aggregate source examples are not a series.
	analysis := data["kind"] == "structured_analysis"
	if !analysis && data["rows_are"] != "observations" {
		return chart.Table{}, false
	}
	source, ok := data["source"].(map[string]any)
	digest, _ := source["sha256"].(string)
	file, _ := source["file"].(string)
	decoded, decodeErr := hex.DecodeString(digest)
	if !ok || decodeErr != nil || len(decoded) != 32 || strings.TrimSpace(file) == "" {
		return chart.Table{}, false
	}
	columns := []string{}
	matrix := [][]string{}
	if analysis {
		var valid bool
		columns, valid = structuredAnalysisColumns(data["columns"], rows)
		if !valid {
			return chart.Table{}, false
		}
		for _, rawRow := range rows {
			values := rawRow.([]any)
			row := []string{}
			for _, value := range values {
				text, e := chart.Cell(value)
				if e != nil {
					return chart.Table{}, false
				}
				row = append(row, text)
			}
			matrix = append(matrix, row)
		}
	} else {
		first, ok := rows[0].(map[string]any)
		if !ok {
			return chart.Table{}, false
		}
		for key := range first {
			columns = append(columns, key)
		}
		sort.Strings(columns)
		for _, rawRow := range rows {
			object, ok := rawRow.(map[string]any)
			if !ok || len(object) != len(columns) {
				return chart.Table{}, false
			}
			row := []string{}
			for _, name := range columns {
				value, exists := object[name]
				if !exists {
					return chart.Table{}, false
				}
				text, e := chart.Cell(value)
				if e != nil {
					return chart.Table{}, false
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
	for _, key := range []string{"kind", "source", "dataset_id", "series", "filters", "requested_range", "actual_first_date", "actual_last_date", "record_count", "scope_record_count", "missing_count", "inputs", "sql", "result_id", "warnings", "rows_are", "input_complete"} {
		if value, exists := data[key]; exists {
			meta[key] = value
		}
	}
	provenance, _ := json.Marshal(meta)
	truncationKey := "truncated"
	if analysis {
		truncationKey = "output_truncated"
	}
	truncated, known := data[truncationKey].(bool)
	notice := "仅绘制本次工具实际返回行；未从目录或模型记忆补充数值。"
	if data["input_complete"] == false {
		truncated = true
		notice += " 分析输入未完整，不能宣称完整范围。"
	}
	if !known {
		truncated = true
		notice += " 来源未明确完整性，仅视为部分返回结果。"
	}
	return chart.Table{Columns: cols, Rows: matrix, Provenance: string(provenance), Truncated: truncated, Notice: notice}, true
}

const dataChartInstructions = `本轮具备render_chart数据绘图能力。根据完整请求和上下文决定是否需要新图；需要时先从已授权来源取得真实typed行或读取原文表格，再调用render_chart inspect核对source_id/列/单位/范围，选择图型与X/Y列后render保存。模型不逐点重写原值，不凭记忆补数或造日期。查询结果仅部分返回时先取得合适完整范围，或明确交付部分图。没有原件图像不等于无法按真实数据绘图；无需反复翻页寻找绘图执行能力。数据走势图、比较图等精确数据表达使用确定性render_chart，不调用generate_image编造数值。只有保存成功才给出真实附件链接；静态HTML图表可点击预览和下载。原件图像复核与确定性重绘的来源含义需说清。`
