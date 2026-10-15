package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const documentTableCalculationToolName = "calculate_document_table"

var documentTableSeparator = regexp.MustCompile(`^:?-{3,}:?$`)
var documentTableNumber = regexp.MustCompile(`^[+\-−]?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)

type documentSourceTable struct {
	Rows          [][]string
	RawRows       []string
	Context       string
	ParseComplete bool
	ParseWarning  string
}

// The parser recognizes source syntax only. It never interprets a column name,
// selects a business scope, infers a missing value or decides a formula.
func documentSourceTables(content string) []documentSourceTable {
	content = documentSourceProse(content)
	lines := strings.Split(content, "\n")
	tables := []documentSourceTable{}
	for index := 0; index+1 < len(lines); index++ {
		header, ok := documentTableCells(lines[index])
		separator, separatorOK := documentTableCells(lines[index+1])
		if !ok || !separatorOK || len(header) != len(separator) {
			continue
		}
		valid := true
		for _, cell := range separator {
			if !documentTableSeparator.MatchString(cell) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		prefix := strings.Join(lines[max(0, index-4):index], "\n")
		if len(prefix) > 2048 {
			prefix = ""
		} // Do not cut UTF-8 or use remote text as a unit header.
		table := documentSourceTable{Rows: [][]string{header}, RawRows: []string{lines[index]}, ParseComplete: true}
		cursor := index + 2
		for ; cursor < len(lines); cursor++ {
			cells, rowOK := documentTableCells(lines[cursor])
			if !rowOK || len(cells) != len(header) {
				if strings.HasPrefix(strings.TrimSpace(lines[cursor]), "|") {
					table.ParseComplete = false
					table.ParseWarning = "原表遇到格式或列数与表头不一致的管道行；仅解析此前连续有效行，该行及后续内容未进入此表，不能宣称原表完整覆盖。请对照该实际分块原文核对。"
				}
				break
			}
			table.Rows = append(table.Rows, cells)
			table.RawRows = append(table.RawRows, lines[cursor])
		}
		// Include the preceding source caption and the first three table rows.
		// Model-supplied units must occur in this actual local header context.
		table.Context = prefix + "\n" + strings.Join(table.RawRows[:min(3, len(table.RawRows))], "\n")
		tables = append(tables, table)
		index = cursor - 1
	}
	return tables
}

func documentTableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if len(line) < 3 || !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	line = line[1 : len(line)-1]
	cells := []string{}
	var cell strings.Builder
	escaped := false
	for _, char := range line {
		if escaped {
			// Only table delimiters and literal backslashes are unescaped
			// here. Dropping a backslash before a digit would invent a new
			// source value (e.g. 12\34 becoming 1234).
			if char != '|' && char != '\\' {
				cell.WriteByte('\\')
			}
			cell.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteRune(char)
	}
	if escaped {
		cell.WriteByte('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells, len(cells) >= 2
}

type documentTableColumn struct {
	Column *int   `json:"column"`
	Unit   string `json:"unit"`
}

type documentTableRequest struct {
	Operation  string                         `json:"operation"`
	ChunkID    string                         `json:"chunk_id"`
	TableIndex *int                           `json:"table_index,omitempty"`
	RowStart   *int                           `json:"row_start,omitempty"`
	RowEnd     *int                           `json:"row_end,omitempty"`
	Columns    map[string]documentTableColumn `json:"columns,omitempty"`
	Expression string                         `json:"expression,omitempty"`
	ResultUnit string                         `json:"result_unit,omitempty"`
}

type documentTableCalculationTool struct {
	agenttools.BaseTool
	steps func() []types.AgentStep
}

func newDocumentTableCalculationTool(steps func() []types.AgentStep) *documentTableCalculationTool {
	return &documentTableCalculationTool{BaseTool: agenttools.NewBaseTool(documentTableCalculationToolName,
		"对本轮实际已读文档/OCR的Markdown表格批量计算，不必逐行抄写数值与引用。先operation=inspect、chunk_id查看真实table_index、行列索引、表头/单位上下文和原始行；再operation=calculate，指定table_index、row_start至row_end（均0起算且包含终点；行0为第一行表头，Markdown分隔行不计）、变量columns={变量:{column:真实列索引,unit:原文单位}}、expression和result_unit。模型结合完整问题和实际表头选择表、行、列、口径、单位、表达式，程序读取真实单元格并保留来源。例：columns绑定变量a,b后expression可用a/b-1；只接受变量、括号、四则及常数0/1，不能把原值写进表达式；result_unit=%自动乘100，表达式不要乘100。单位必须出现在inspect返回的unit_context中；未知单位留空但不能生成百分比。空白/非数值/零分母逐行明确报告，不补零、不静默删行。不读文件、不联网、不执行代码。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation","chunk_id"],"properties":{"operation":{"type":"string","enum":["inspect","calculate"]},"chunk_id":{"type":"string","minLength":1},"table_index":{"type":"integer","minimum":0},"row_start":{"type":"integer","minimum":0},"row_end":{"type":"integer","minimum":0},"columns":{"type":"object","minProperties":1,"maxProperties":16,"additionalProperties":{"type":"object","additionalProperties":false,"required":["column","unit"],"properties":{"column":{"type":"integer","minimum":0},"unit":{"type":"string","maxLength":128}}}},"expression":{"type":"string","maxLength":1024},"result_unit":{"type":"string","enum":["","%"]}}}`)), steps: steps}
}

func (tool *documentTableCalculationTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	fail := func(message string) (*types.ToolResult, error) {
		data := map[string]any{"kind": "source_calculation", "status": "error", "error": message, "notice": "本次未提供计算结果。先inspect核对实际索引和unit_context，再修正计划；失败不代表原表没有数据。"}
		encoded, _ := json.Marshal(data)
		return &types.ToolResult{Success: false, Error: message, Data: data, Output: string(encoded)}, nil
	}
	if len(raw) > 16384 || tool.steps == nil {
		return fail("计算计划或实际来源无效")
	}
	var request documentTableRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fail("参数格式无效：" + err.Error())
	}
	if decoder.Decode(new(any)) != io.EOF || request.ChunkID == "" || len(request.ChunkID) > 256 {
		return fail("需要真实已读chunk_id及单个合法计划")
	}
	var chunk *fetchedDocumentChunk
	for _, step := range tool.steps() {
		for _, call := range step.ToolCalls {
			for _, actual := range fetchedDocumentChunks(call) {
				if actual.ID == request.ChunkID && actual.KnowledgeID != "" {
					copy := actual
					chunk = &copy
					break
				}
			}
		}
	}
	if chunk == nil {
		return fail("chunk_id不是本轮成功读取的实际分块，请先read_document并使用实际引用")
	}
	tables := documentSourceTables(chunk.Content)
	if request.Operation == "inspect" {
		views := []any{}
		for index, table := range tables {
			count := min(40, len(table.Rows))
			views = append(views, map[string]any{"table_index": index, "row_count": len(table.Rows), "column_count": len(table.Rows[0]), "row_index_start": 0, "rows": table.Rows[:count], "rows_truncated": count < len(table.Rows), "unit_context": table.Context, "parse_complete": table.ParseComplete, "parse_warning": table.ParseWarning})
			if len(views) == 16 {
				break
			}
		}
		data := map[string]any{"kind": "document_table_catalog", "chunk_id": chunk.ID, "knowledge_id": chunk.KnowledgeID, "table_count": len(tables), "tables": views, "tables_truncated": len(views) < len(tables), "notice": "所有索引0起算。rows含表头，排除Markdown分隔行。rows_truncated仅表示展示裁剪；parse_complete=false表示源表格式导致解析提前停止，不能将有效前缀当成全表。仅当前已读分块；完整性不代表整份文档覆盖。请按真实表头选择变量列、数据行与原文单位，不能依据目录元数据直接作统计。"}
		encoded, _ := json.Marshal(data)
		return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, nil
	}
	if request.Operation != "calculate" || request.TableIndex == nil || *request.TableIndex < 0 || *request.TableIndex >= len(tables) || request.RowStart == nil || request.RowEnd == nil {
		return fail("calculate需要实际table_index、row_start、row_end；可先inspect查看范围")
	}
	table := tables[*request.TableIndex]
	if *request.RowStart < 0 || *request.RowEnd < *request.RowStart || *request.RowEnd >= len(table.Rows) || *request.RowEnd-*request.RowStart >= 1000 || len(request.Columns) < 1 || len(request.Columns) > 16 || len(request.Expression) < 1 || len(request.Expression) > 1024 || (request.ResultUnit != "" && request.ResultUnit != "%") {
		return fail("行范围、变量、表达式或结果单位无效；单次最多1000行")
	}
	for name, binding := range request.Columns {
		if !sourceCalculationVariable.MatchString(name) || binding.Column == nil || *binding.Column < 0 || *binding.Column >= len(table.Rows[0]) || len(binding.Unit) > 128 {
			return fail("变量名称或真实列索引无效")
		}
		if binding.Unit != "" && !strings.Contains(table.Context, binding.Unit) {
			return fail(fmt.Sprintf("变量%s的单位不在该表实际unit_context中；请核对原文单位，不要猜测或原样重试", name))
		}
	}
	units := map[string]string{}
	for name, binding := range request.Columns {
		units[name] = binding.Unit
	}
	if err := validateSourceExpressionPlan(request.Expression, units, request.ResultUnit); err != nil {
		return fail("表达式或单位计划无效：" + err.Error() + "；result_unit=%已自动缩放，不在表达式中另乘100。先修正同一公式，本次未逐行执行。")
	}
	results := []any{}
	failures := []any{}
	for index := *request.RowStart; index <= *request.RowEnd; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := table.Rows[index]
		variables := map[string]sourceQuantity{}
		inputs := map[string]any{}
		selectedCells := map[string]any{}
		for name, binding := range request.Columns {
			selectedCells[name] = map[string]any{"column": *binding.Column, "source_value": row[*binding.Column]}
		}
		invalid := ""
		for name, binding := range request.Columns {
			text := strings.TrimSpace(row[*binding.Column])
			numericText := text
			if binding.Unit == "%" && strings.HasSuffix(numericText, "%") {
				numericText = strings.TrimSpace(strings.TrimSuffix(numericText, "%"))
			}
			// Normalize separators only after verifying the entire original
			// token. An ambiguous decimal comma or malformed grouping must
			// not silently turn 1,2 into the different number 12.
			if !documentTableNumber.MatchString(numericText) || len(numericText) > 128 {
				invalid = fmt.Sprintf("列%d不是完整十进制数值（空白或非数值未补零）", *binding.Column)
				break
			}
			numeric := strings.ReplaceAll(strings.ReplaceAll(numericText, ",", ""), "−", "-")
			value, ok := new(big.Rat).SetString(numeric)
			if !ok {
				invalid = "数值无效"
				break
			}
			quantity := sourceQuantity{value: value, units: map[string]int{}, unknown: binding.Unit == ""}
			if binding.Unit == "%" {
				quantity.value = new(big.Rat).Quo(value, big.NewRat(100, 1))
			} else if binding.Unit != "" {
				quantity.units[binding.Unit] = 1
			}
			variables[name] = quantity
			inputs[name] = map[string]any{"column": *binding.Column, "source_value": text, "value": numeric, "unit": binding.Unit}
		}
		var result map[string]any
		if invalid == "" {
			var err error
			result, err = evaluateSourceExpression(request.Expression, variables, request.ResultUnit)
			if err != nil {
				invalid = err.Error()
			}
		}
		if invalid != "" {
			failures = append(failures, map[string]any{"row_index": index, "row_label": row[0], "source_row": table.RawRows[index], "selected_cells": selectedCells, "error": invalid})
			continue
		}
		result["row_index"], result["row_label"], result["inputs"], result["source_row"] = index, row[0], inputs, table.RawRows[index]
		results = append(results, result)
	}
	if len(results) == 0 {
		return fail(fmt.Sprintf("所选%d行均未取得有效计算。检查行范围、数值、单位和表达式：%v", *request.RowEnd-*request.RowStart+1, failures))
	}
	data := map[string]any{"kind": "source_calculation", "status": "ok", "chunk_id": chunk.ID, "knowledge_id": chunk.KnowledgeID, "table_index": *request.TableIndex, "row_start": *request.RowStart, "row_end": *request.RowEnd, "selected_row_count": *request.RowEnd - *request.RowStart + 1, "calculated_row_count": len(results), "failed_rows": failures, "calculations": results, "unit_context": table.Context, "parse_complete": table.ParseComplete, "parse_warning": table.ParseWarning, "notice": "仅对模型所选真实表、行、列执行。parse_complete=false时仅有源表有效前缀，未解析行不计入selected_row_count；不能宣称原表完整覆盖。失败行未补零；不能把部分有效行当成全表统计。表头与业务口径仍需模型核对，原报告舍入比例不代表精确变化率。"}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, nil
}
