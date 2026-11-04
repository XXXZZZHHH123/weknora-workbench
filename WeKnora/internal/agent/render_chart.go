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
	"sync"

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
	mapped                  map[string]mappedChartSource
	reviewPlan              chartPlanReviewer
	receipts                map[string]verifiedChartReceipt
	mu                      sync.Mutex
}

func (e *AgentEngine) SetChartWriter(writer chart.Writer) {
	if e != nil {
		e.chartWriter = writer
	}
}
func newRenderChartTool(ctx context.Context, steps func() []types.AgentStep, writer chart.Writer, session, message string) *renderChartTool {
	return &renderChartTool{BaseTool: agenttools.NewBaseTool(renderChartToolName,
		"从本轮真实来源确定性绘图。先inspect查看sources及input_sources；input_sources是已读PDF二维单元格，先map按完整问题选择行列，不能把横向表的年份行和指标行当成两条观测。map提交mapping:{orientation:rows|columns,x_index,y_indexes,value_indexes,label_index?,unit_index?}：rows表示X/Y分别取真实行、value_indexes取跨列观测；columns表示X/Y取列、value_indexes取观测行。label_index从来源标签格取名；unit_index只显式复制真实列元数据中的单位。map返回可直接render的真实source_id。也可query_pdf_table引用真实源字段整形，不能用重抄数值的SQL常量冒充原表观测。render选择真实source_id、x_column、y_columns(最多6)、chart_type(line/bar/scatter)、x_type(category/numeric/time)、title。依据完整任务选择业务维度，内部行ID不能充当业务轴。不得提交任意观测值、代码或URL，缺失留空，不补值，未知单位保持未知。保存前核验图的指标、维度、单位、范围与完整请求是否一致；失败反馈后修正真实映射。delivery_mode默认chart_only，只交付返回的图片Markdown；明确还要文字分析时用chart_with_analysis。保存失败不能声称交付。",
		json.RawMessage(`{
  "type":"object","additionalProperties":false,"required":["operation"],
  "properties":{
    "operation":{"type":"string","enum":["inspect","map","render"]},
    "source_id":{"type":"string","maxLength":128},
    "mapping":{
      "type":"object","additionalProperties":false,
      "required":["orientation","x_index","y_indexes","value_indexes"],
      "properties":{
        "orientation":{"type":"string","enum":["rows","columns"],"description":"按X/Y序列所在方向命名。rows：X/Y各占原表一行，沿原列生成观测；columns：X/Y各占原表一列，沿原行生成观测。先对照inspect的matrix_shape和实际单元格选定方向。"},
        "x_index":{"type":"integer","minimum":0,"description":"rows时为X序列的原行索引；columns时为X序列的原列索引。均从0起算。"},
        "y_indexes":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"integer","minimum":0},"description":"选择指标系列而非观测点。与x_index同一坐标空间：rows选指标所在原行，columns选指标所在原列，不得与X重复。"},
        "value_indexes":{"type":"array","minItems":1,"maxItems":2000,"items":{"type":"integer","minimum":0},"description":"观测沿另一方向排列：rows时选择原列索引；columns时选择原行索引。顺序原样保留，不含标签坐标。"},
        "label_index":{"type":"integer","minimum":0,"description":"rows必填，指定真实行标签所在原列，不包含在value_indexes；columns的轴名直接复制原列名。"},
        "unit_index":{"type":"integer","minimum":0,"description":"rows时可选，仅从该原列的已声明元数据复制Y单位；全部所选观测列须声明相同单位。未知时省略，不根据标题猜测。"}
      }
    },
    "chart_type":{"type":"string","enum":["line","bar","scatter"]},
    "x_column":{"type":"integer","minimum":0,"description":"render选择的来源结果列；map输出中X固定为0，不再填原表坐标。"},
    "y_columns":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"integer","minimum":0},"description":"render选择的来源结果列；map输出中Y依次为1至输出列数减1，不再填原表坐标。"},
    "x_type":{"type":"string","enum":["category","numeric","time"]},
    "title":{"type":"string","maxLength":512},
    "delivery_mode":{"type":"string","enum":["chart_only","chart_with_analysis"]}
  }
}`)), steps: steps, writer: writer, tenant: types.CallerFromContext(ctx).TenantID, owner: types.SessionOwnerIDFromContext(ctx), session: session, message: message}
}
func (t *renderChartTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
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
		Operation    string              `json:"operation"`
		SourceID     string              `json:"source_id"`
		Type         string              `json:"chart_type"`
		X            *int                `json:"x_column"`
		Y            []int               `json:"y_columns"`
		XType        string              `json:"x_type"`
		Title        string              `json:"title"`
		DeliveryMode string              `json:"delivery_mode"`
		Mapping      *chartSourceMapping `json:"mapping"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail("绘图参数必须为单个合法对象，不接受数值、代码、URL或身份参数")
	}
	if input.Operation != "inspect" && input.Operation != "map" && input.Operation != "render" {
		return fail("operation必须为inspect、map或render")
	}
	if input.DeliveryMode == "" {
		input.DeliveryMode = "chart_only"
	}
	if input.DeliveryMode != "chart_only" && input.DeliveryMode != "chart_with_analysis" {
		return fail("delivery_mode必须为chart_only或chart_with_analysis")
	}
	tables, omitted, rejected := chartSourcesDetailed(t.steps())
	inputs := pdfChartInputSources(t.steps())
	parents := map[string]bool{}
	for _, source := range append(append([]chart.Table{}, tables...), inputs...) {
		parents[source.ID] = true
	}
	for _, mapped := range t.mapped {
		if parents[mapped.ParentID] {
			tables = append(tables, mapped.Table)
		}
	}
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
		inputViews := []any{}
		for _, table := range inputs {
			view := map[string]any{"source_id": table.ID, "columns": table.Columns, "row_count": len(table.Rows), "matrix_shape": map[string]int{"rows": len(table.Rows), "columns": len(table.Columns)}, "sample_rows": table.Rows[:min(8, len(table.Rows))], "source_truncated": table.Truncated, "requires_mapping": true, "provenance": table.Provenance, "notice": table.Notice}
			candidate, _ := json.Marshal(append(append([]any{}, views...), append(inputViews, view)...))
			if len(candidate)+len(diagnostics) > 62*1024 {
				omitted++
				continue
			}
			inputViews = append(inputViews, view)
		}
		data := map[string]any{"kind": "chart_source_catalog", "sources": views, "input_sources": inputViews, "omitted_source_count": omitted, "rejected_source_count": rejectedCount, "source_rejections": rejected, "source_rejections_truncated": rejectedCount > len(rejected), "notice": "先根据完整问题理解真实行列。input_sources须map后绘图；sources可render。全部索引0起算，sample_rows仅展示样例，真实句柄绑定全部已返回行。表ID、result_id不是绘图句柄。无来源时按拒绝原因恢复真实数据，不猜ID、不重抄常量。"}
		body, _ := json.Marshal(data)
		return &types.ToolResult{Success: true, Data: data, Output: string(body)}, nil
	}
	if input.Operation == "map" {
		if input.Mapping == nil {
			return fail("map需要完整mapping与inspect返回的真实source_id")
		}
		for _, source := range inputs {
			if source.ID != input.SourceID {
				continue
			}
			mapped, err := mapChartSource(source, *input.Mapping)
			if err != nil {
				return fail(err.Error())
			}
			if t.mapped == nil {
				t.mapped = map[string]mappedChartSource{}
			}
			if len(t.mapped) >= chart.MaxSources {
				return fail("本轮映射数量已达上限")
			}
			t.mapped[mapped.ID] = mappedChartSource{Table: mapped, ParentID: source.ID, Mapping: *input.Mapping}
			data := map[string]any{"kind": "chart_source_mapping", "source_id": mapped.ID, "columns": mapped.Columns, "rows": mapped.Rows, "mapping": input.Mapping, "source_truncated": mapped.Truncated, "provenance": mapped.Provenance, "notice": "已按真实坐标复制。核对输出columns/rows是否满足完整任务；满足时下一步直接render此source_id，X列为0，Y列依次为1至输出列数减1，无需再生成等价映射。"}
			body, _ := json.Marshal(data)
			return &types.ToolResult{Success: true, Data: data, Output: string(body)}, nil
		}
		return fail("map的source_id不属于当前仍可验证的真实来源")
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
	spec := chart.Spec{Type: input.Type, X: *input.X, Y: input.Y, XType: input.XType, Title: title}
	if spec.XType == "" {
		spec.XType = "category"
	}
	if err := validateChartSourceColumns(*source, spec); err != nil {
		return fail(err.Error())
	}
	png, err := chart.Render(*source, spec)
	if err != nil {
		return fail(err.Error())
	}
	var validation *chartSemanticValidation
	if t.reviewPlan != nil {
		validation, err = t.reviewPlan(ctx, *source, spec)
		if err != nil {
			return fail(err.Error())
		}
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
	columns, rows := selectedChartData(*source, spec)
	data := map[string]any{"kind": "data_chart", "status": "ok", "artifact": delivered, "delivery_mode": input.DeliveryMode, "source_id": source.ID, "row_count": len(source.Rows), "source_truncated": source.Truncated, "provenance": source.Provenance, "chart_spec": spec, "selected_columns": columns, "selected_rows": rows}
	if mapped, ok := t.mapped[source.ID]; ok {
		data["source_mapping"] = mapped.Mapping
	}
	output := fmt.Sprintf("![图表](%s)", delivered.Handle)
	if validation != nil {
		data["semantic_validation"] = validation
	}
	if validation != nil && validation.FullTaskView && validation.CompleteDelivery {
		if t.receipts == nil {
			t.receipts = map[string]verifiedChartReceipt{}
		}
		t.receipts[delivered.Handle] = verifiedChartReceipt{CallID: meta.ToolCallID, TaskHash: validation.TaskHash, DataHash: reviewDigest(data), Output: output, Mode: input.DeliveryMode}
	}
	return &types.ToolResult{Success: true, Data: data, Output: output}, nil
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
		if name == "_row_id" {
			cols[i].Role = "source_row_identifier"
		}
	}
	if analysis && data["source_kind"] == "pdf_table" {
		if data["sql_source_dependency_verified"] != true {
			return chart.Table{}, fmt.Errorf("pdf_sql_source_dependency_not_verified")
		}
		dependencies, ok := data["sql_column_dependencies"].([]any)
		if !ok || len(dependencies) != len(cols) {
			return chart.Table{}, fmt.Errorf("pdf_sql_column_dependencies_missing")
		}
		for i, rawDependency := range dependencies {
			dependency, ok := rawDependency.(map[string]any)
			index, number := dependency["column_index"].(float64)
			kind, _ := dependency["kind"].(string)
			if !ok || !number || index != float64(i) || dependency["column"] != cols[i].Name {
				return chart.Table{}, fmt.Errorf("pdf_sql_column_dependencies_invalid")
			}
			switch kind {
			case "source_value", "source_cardinality", "constant_label", "source_metadata", "mixed_constant_values":
			default:
				return chart.Table{}, fmt.Errorf("pdf_sql_column_dependency_kind_invalid")
			}
			cols[i].DependencyKind = kind
			if dependency["technical_metadata"] == true || kind == "source_metadata" {
				cols[i].Role = "source_row_identifier"
			}
		}
	}
	meta := map[string]any{}
	for _, key := range []string{"kind", "source", "dataset_id", "series", "filters", "requested_range", "actual_first_date", "actual_last_date", "record_count", "scope_record_count", "missing_count", "inputs", "sql", "result_id", "warnings", "rows_are", "input_complete", "input_count", "output_truncated", "truncated", "returned_row_count", "sql_source_dependency_verified", "sql_column_dependencies", "sql_source_dependency_method"} {
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

const dataChartInstructions = `render_chart从已授权且本轮真正读取的数值行或原文表格生成PNG。inspect同时提供可render的sources及须先map的input_sources；已读PDF二维表可直接用map按模型明确的行/列坐标整形，不必绕行原页或SQL，不重抄数值。横向表的维度行和指标行要配对成逐观测记录，内部_row_id是技术身份，不能作为业务轴。SQL替代路径必须引用真实源值，常量数组不是源表取数。inspect/map返回真实source_id，不能猜表ID。最多6个Y系列；更多观测点应由正确行列整形得到，不应通过删除所需数据点来满足系列上限。按完整请求核验变量、维度、单位、范围和图型，渲染前会独立检查。保持原值、真实日期与缺失，不平滑补数，不用generate_image编造数据。图内只需标题、图形、坐标、图例和必要范围标注。只需图时delivery_mode默认chart_only，最终仅输出本轮成功返回的图片Markdown；明确同时要求分析时用chart_with_analysis。失败时保留真实未交付说明，不能以表格代替所要求的图。`
