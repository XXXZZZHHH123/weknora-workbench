package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const documentTableViewPrefix = "document-table-view:sha256:"
const documentTableViewMaxBytes = 192 * 1024

// A server-owned presentation of actual selected source cells. Digest/ref are
// outside the snapshot to avoid a self-referential digest. Callers receive a
// deep copy; the registered calculator retains the sealed original privately.
type documentTableViewSnapshot struct {
	Version             string           `json:"version"`
	Operation           string           `json:"operation"`
	SourceFingerprint   string           `json:"source_fingerprint"`
	SourceVersion       string           `json:"source_version"`
	ChunkID             string           `json:"chunk_id"`
	KnowledgeID         string           `json:"knowledge_id"`
	TableIndex          int              `json:"table_index"`
	RowIndices          []int            `json:"row_indices"`
	ColumnIndices       []int            `json:"column_indices"`
	ComputedColumnCount int              `json:"computed_column_count,omitempty"`
	SourceCaption       string           `json:"source_caption,omitempty"`
	Headers             []string         `json:"headers"`
	Rows                [][]string       `json:"rows"`
	RawSourceRows       []string         `json:"raw_source_rows"`
	UnitContext         string           `json:"unit_context"`
	ParseComplete       bool             `json:"parse_complete"`
	ParseWarning        string           `json:"parse_warning"`
	Calculations        []map[string]any `json:"calculations,omitempty"`
	FailedRows          []map[string]any `json:"failed_rows,omitempty"`
	Expression          string           `json:"expression,omitempty"`
	Markdown            string           `json:"markdown"`
}

type documentTableSource struct {
	Chunk       fetchedDocumentChunk
	Call        types.ToolCall
	Fingerprint string
	Version     string
}

func documentTableDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func documentTableSources(steps []types.AgentStep, registry *agenttools.ToolRegistry) []documentTableSource {
	var sources []documentTableSource
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Name != "read_document" && call.Name != "search_knowledge" {
				continue
			}
			if registry != nil && !agenttools.IsCurrentDocumentRead(registry, call.Result) {
				continue
			}
			if call.Result == nil || !call.Result.Success {
				continue
			}
			dataDigest, err := documentTableDigest(call.Result.Data)
			if err != nil {
				continue
			}
			var rawRows []map[string]any
			for _, field := range []string{"chunks", "results"} {
				encoded, _ := json.Marshal(call.Result.Data[field])
				var rows []map[string]any
				if json.Unmarshal(encoded, &rows) == nil {
					rawRows = append(rawRows, rows...)
				}
			}
			for index, chunk := range fetchedDocumentChunks(call) {
				if chunk.ID == "" || chunk.KnowledgeID == "" || index >= len(rawRows) {
					continue
				}
				fingerprint, err := documentTableDigest(struct {
					CallID     string
					Name       string
					Chunk      fetchedDocumentChunk
					DataDigest string
				}{call.ID, call.ExecutionName(), chunk, dataDigest})
				if err != nil {
					continue
				}
				sources = append(sources, documentTableSource{Chunk: chunk, Call: call, Fingerprint: fingerprint, Version: documentTableSourceVersion(rawRows[index], chunk)})
			}
		}
	}
	return sources
}

func documentTableSourceVersion(row map[string]any, chunk fetchedDocumentChunk) string {
	content, _ := row["content"].(string)
	rawDigest := sha256.Sum256([]byte(content))
	origin := hex.EncodeToString(rawDigest[:])
	if projection, ok := row["content_source_view"].(map[string]any); ok {
		if digest, ok := projection["origin_sha256"].(string); ok && digest != "" {
			origin = digest
		}
	}
	version, _ := documentTableDigest([]any{chunk.ID, chunk.KnowledgeID, row["content_revision"], origin, row["source_locators"]})
	return version
}

func (tool *documentTableCalculationTool) selectDocumentTableSource(chunkID, fingerprint string) (documentTableSource, error) {
	sources := documentTableSources(tool.steps(), tool.registry)
	var selected *documentTableSource
	for index := range sources {
		source := &sources[index]
		if source.Chunk.ID == chunkID && (fingerprint == "" || source.Fingerprint == fingerprint) {
			selected = source
		}
	}
	if selected == nil {
		return documentTableSource{}, fmt.Errorf("chunk_id或source_fingerprint不是当前来源范围内、本轮成功读取的实际分块；请重新read_document并inspect")
	}
	latest := selected
	for index := range sources {
		source := &sources[index]
		if source.Chunk.ID == chunkID && source.Chunk.KnowledgeID == selected.Chunk.KnowledgeID {
			latest = source
		}
	}
	if latest.Version != selected.Version {
		return documentTableSource{}, fmt.Errorf("所选分块已出现更新版本；请基于最新实际读取重新选择表格")
	}
	return *selected, nil
}

// Keep the established numeric parser unchanged. Exact source cells are paired
// mechanically with its declared tables; no extra table or guessed cell can
// acquire an index through this raw projection.
func exactDocumentSourceTable(content string, tableIndex int) (documentSourceTable, error) {
	numeric := documentSourceTables(content)
	if tableIndex < 0 || tableIndex >= len(numeric) {
		return documentSourceTable{}, fmt.Errorf("table_index不在实际已解析表格中；请先inspect")
	}
	rawText := documentFakeToolEnvelope.ReplaceAllString(documentCodeFence.ReplaceAllString(content, ""), "")
	rawTables := parseDocumentSourceTables(rawText)
	cursor := 0
	for index := 0; index <= tableIndex; index++ {
		found := false
		for cursor < len(rawTables) {
			raw := rawTables[cursor]
			cursor++
			if !sameDocumentTableProjection(raw, numeric[index]) {
				continue
			}
			found = true
			if index == tableIndex {
				return raw, nil
			}
			break
		}
		if !found {
			return documentSourceTable{}, fmt.Errorf("该表原始管道行与已解析索引不能完整对应；不能改写或猜测源单元格，请核对实际原文")
		}
	}
	return documentSourceTable{}, fmt.Errorf("真实原表未找到")
}

func sameDocumentTableProjection(raw, numeric documentSourceTable) bool {
	if len(raw.RawRows) != len(numeric.Rows) {
		return false
	}
	for index, row := range raw.RawRows {
		cells, ok := documentTableCells(documentSourceProse(row))
		if !ok || len(cells) != len(numeric.Rows[index]) {
			return false
		}
		for column := range cells {
			if cells[column] != numeric.Rows[index][column] {
				return false
			}
		}
	}
	return true
}

func documentTableSelectedIndices(request documentTableRequest, rowCount, columnCount int) ([]int, []int, error) {
	rows := append([]int(nil), request.RowIndices...)
	if len(rows) > 0 {
		if request.RowStart != nil || request.RowEnd != nil {
			return nil, nil, fmt.Errorf("select使用row_indices或row_start/row_end，不可混用")
		}
	} else {
		if request.RowStart == nil || request.RowEnd == nil || *request.RowStart < 0 || *request.RowEnd < *request.RowStart || *request.RowEnd-*request.RowStart >= 200 {
			return nil, nil, fmt.Errorf("select需要明确真实行范围或row_indices；最多200行")
		}
		for index := *request.RowStart; index <= *request.RowEnd; index++ {
			rows = append(rows, index)
		}
	}
	columns := append([]int(nil), request.SelectedColumns...)
	for _, list := range []struct {
		values []int
		count  int
		cap    int
	}{{rows, rowCount, 200}, {columns, columnCount, 16}} {
		if len(list.values) == 0 || len(list.values) > list.cap {
			return nil, nil, fmt.Errorf("select最多200行、16个明确选择列")
		}
		seen := map[int]bool{}
		for _, index := range list.values {
			if index < 0 || index >= list.count || seen[index] {
				return nil, nil, fmt.Errorf("select行列索引必须在真实范围内且不能重复")
			}
			seen[index] = true
		}
	}
	return rows, columns, nil
}

func documentTableSourceCaption(table documentSourceTable) string {
	suffix := "\n" + strings.Join(table.RawRows[:min(3, len(table.RawRows))], "\n")
	return strings.TrimSuffix(table.Context, suffix)
}

func documentTableViewCell(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(html.EscapeString(text), "&#34;", "&quot;"), "&#39;", "&apos;")
	// The product's math/emphasis preprocessing runs before Markdown. Named
	// entities preserve literal punctuation through that path without creating
	// unsupported numeric entity tokens for the ordinary source-number guard.
	text = strings.NewReplacer("\\", "&bsol;", "|", "&vert;", "`", "&grave;", "*", "&ast;", "_", "&lowbar;", "[", "&lbrack;", "]", "&rbrack;", "$", "\\$", "~", "\\~").Replace(text)
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "<br>")
}

func renderDocumentTableView(snapshot documentTableViewSnapshot) string {
	var output strings.Builder
	if snapshot.SourceCaption != "" {
		for _, line := range strings.Split(snapshot.SourceCaption, "\n") {
			output.WriteString("> " + documentTableViewCell(line) + "\n")
		}
		output.WriteByte('\n')
	}
	writeRow := func(row []string) {
		output.WriteString("| ")
		for index, cell := range row {
			if index > 0 {
				output.WriteString(" | ")
			}
			output.WriteString(documentTableViewCell(cell))
		}
		output.WriteString(" |\n")
	}
	writeRow(snapshot.Headers)
	separator := make([]string, len(snapshot.Headers))
	for index := range separator {
		separator[index] = "---"
	}
	writeRow(separator)
	for _, row := range snapshot.Rows {
		writeRow(row)
	}
	if !snapshot.ParseComplete {
		output.WriteString("\n解析范围说明：" + documentTableViewCell(snapshot.ParseWarning) + "\n")
	}
	output.WriteString("\n<kb chunk_id=\"" + html.EscapeString(snapshot.ChunkID) + "\" />")
	return output.String()
}

func (tool *documentTableCalculationTool) sealDocumentTableView(snapshot documentTableViewSnapshot) (documentTableViewSnapshot, error) {
	snapshot.Markdown = renderDocumentTableView(snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > documentTableViewMaxBytes {
		return documentTableViewSnapshot{}, fmt.Errorf("所选表格视图超过192KiB预算；请缩小真实行列范围")
	}
	var frozen documentTableViewSnapshot
	if json.Unmarshal(encoded, &frozen) != nil {
		return documentTableViewSnapshot{}, fmt.Errorf("源表视图不能冻结")
	}
	digest, _ := documentTableDigest(frozen)
	ref := documentTableViewPrefix + digest
	tool.viewsMu.Lock()
	defer tool.viewsMu.Unlock()
	if _, exists := tool.views[ref]; !exists && len(tool.views) >= 64 {
		return documentTableViewSnapshot{}, fmt.Errorf("当前任务已达到64个不可变表格视图的预算；请复用实际已有视图")
	}
	if tool.views == nil {
		tool.views = map[string]documentTableViewSnapshot{}
	}
	tool.views[ref] = frozen
	// Return a separate copy: mutating ToolResult.Data must never alter storage.
	var result documentTableViewSnapshot
	_ = json.Unmarshal(encoded, &result)
	return result, nil
}

func attachDocumentTableView(data map[string]any, snapshot documentTableViewSnapshot) {
	digest, _ := documentTableDigest(snapshot)
	data["view_ref"], data["view_digest"] = documentTableViewPrefix+digest, digest
	data["source_fingerprint"], data["view_snapshot"] = snapshot.SourceFingerprint, snapshot
	data["view_notice"] = "可在最终回复单独一行使用<document_table_view ref=\"返回的完整view_ref\" />。服务器只展开本轮已成功执行且来源仍有效的冻结视图，并继续全部来源、数值与语义审核；不要重抄或修改快照。技术选择和解析完整性不等于整份文档或问题覆盖。"
}

func (tool *documentTableCalculationTool) selectDocumentTableView(ctx context.Context, source documentTableSource, request documentTableRequest) (map[string]any, error) {
	if request.TableIndex == nil || len(request.Columns) > 0 || request.Expression != "" || request.ResultUnit != "" {
		return nil, fmt.Errorf("select需要真实table_index及selected_columns；不得夹带计算变量、表达式或改写单位")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	table, err := exactDocumentSourceTable(source.Chunk.Content, *request.TableIndex)
	if err != nil {
		return nil, err
	}
	rows, columns, err := documentTableSelectedIndices(request, len(table.Rows), len(table.Rows[0]))
	if err != nil {
		return nil, err
	}
	snapshot := documentTableViewSnapshot{Version: "document-table-view-v1", Operation: "select", SourceFingerprint: source.Fingerprint, SourceVersion: source.Version, ChunkID: source.Chunk.ID, KnowledgeID: source.Chunk.KnowledgeID, TableIndex: *request.TableIndex, RowIndices: rows, ColumnIndices: columns, UnitContext: table.Context, SourceCaption: documentTableSourceCaption(table), ParseComplete: table.ParseComplete, ParseWarning: table.ParseWarning}
	for _, column := range columns {
		snapshot.Headers = append(snapshot.Headers, table.Rows[0][column])
	}
	for _, index := range rows {
		row := []string{}
		for _, column := range columns {
			row = append(row, table.Rows[index][column])
		}
		snapshot.Rows = append(snapshot.Rows, row)
		snapshot.RawSourceRows = append(snapshot.RawSourceRows, table.RawRows[index])
	}
	snapshot, err = tool.sealDocumentTableView(snapshot)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"kind": "document_table_view", "status": "ok", "operation": "select", "chunk_id": snapshot.ChunkID, "knowledge_id": snapshot.KnowledgeID, "table_index": snapshot.TableIndex, "selected_row_indices": rows, "selected_column_indices": columns, "selected_row_count": len(rows), "parse_complete": snapshot.ParseComplete, "parse_warning": snapshot.ParseWarning, "unit_context": snapshot.UnitContext}
	attachDocumentTableView(data, snapshot)
	return data, nil
}

func (tool *documentTableCalculationTool) calculatedDocumentTableView(source documentTableSource, request documentTableRequest, result map[string]any) (documentTableViewSnapshot, error) {
	table, err := exactDocumentSourceTable(source.Chunk.Content, *request.TableIndex)
	if err != nil {
		return documentTableViewSnapshot{}, err
	}
	columns, seen := []int{0}, map[int]bool{0: true}
	for _, binding := range request.Columns {
		if !seen[*binding.Column] {
			columns = append(columns, *binding.Column)
			seen[*binding.Column] = true
		}
	}
	sort.Ints(columns)
	snapshot := documentTableViewSnapshot{Version: "document-table-view-v1", Operation: "calculate", SourceFingerprint: source.Fingerprint, SourceVersion: source.Version, ChunkID: source.Chunk.ID, KnowledgeID: source.Chunk.KnowledgeID, TableIndex: *request.TableIndex, ColumnIndices: columns, UnitContext: table.Context, SourceCaption: documentTableSourceCaption(table), ParseComplete: table.ParseComplete, ParseWarning: table.ParseWarning, Expression: request.Expression, ComputedColumnCount: 3}
	byRow, failures := map[int]map[string]any{}, map[int]map[string]any{}
	for _, entry := range result["calculations"].([]any) {
		row := entry.(map[string]any)
		byRow[row["row_index"].(int)] = row
		snapshot.Calculations = append(snapshot.Calculations, row)
	}
	for _, entry := range result["failed_rows"].([]any) {
		row := entry.(map[string]any)
		failures[row["row_index"].(int)] = row
		snapshot.FailedRows = append(snapshot.FailedRows, row)
	}
	for _, column := range columns {
		snapshot.Headers = append(snapshot.Headers, table.Rows[0][column])
	}
	snapshot.Headers = append(snapshot.Headers, "计算结果", "结果单位", "执行说明")
	for index := *request.RowStart; index <= *request.RowEnd; index++ {
		row := []string{}
		for _, column := range columns {
			row = append(row, table.Rows[index][column])
		}
		if computed := byRow[index]; computed != nil {
			row = append(row, fmt.Sprint(computed["value"]), fmt.Sprint(computed["unit"]), "")
		} else if failed := failures[index]; failed != nil {
			row = append(row, "", "", documentTableFailureDescription(failed))
		} else {
			return documentTableViewSnapshot{}, fmt.Errorf("实际计算行诊断不完整，未生成可交付视图")
		}
		snapshot.RowIndices = append(snapshot.RowIndices, index)
		snapshot.Rows = append(snapshot.Rows, row)
		snapshot.RawSourceRows = append(snapshot.RawSourceRows, table.RawRows[index])
	}
	return tool.sealDocumentTableView(snapshot)
}

// Execution coordinates and parameter names are diagnostic metadata, not
// observed business numbers. Keep full errors in FailedRows; render only a
// deterministic explanation of the actual technical failure category.
func documentTableFailureDescription(failed map[string]any) string {
	switch failed["reason_code"] {
	case "source_cell_not_decimal":
		return "原始单元格为空或不是完整十进制数值（空白或非数值未补零）"
	case "source_number_invalid":
		return "原始数值未通过解析，未生成计算结果"
	default:
		return "所选算式未取得有效结果，详见执行诊断"
	}
}

func successfulDocumentTableViewResults(steps []types.AgentStep) []map[string]any {
	var results []map[string]any
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil {
				continue
			}
			if call.ExecutionName() == documentTableCalculationToolName && call.Result.Success && call.Result.Data["status"] == "ok" {
				results = append(results, call.Result.Data)
			}
			if call.ExecutionName() != documentTableAnalysisToolName {
				continue
			}
			encoded, _ := json.Marshal(call.Result.Data["executions"])
			var executions []struct {
				Success  bool           `json:"success"`
				ToolName string         `json:"tool_name"`
				Result   map[string]any `json:"result"`
			}
			if json.Unmarshal(encoded, &executions) != nil {
				continue
			}
			for _, execution := range executions {
				if execution.Success && execution.ToolName == documentTableCalculationToolName && execution.Result["status"] == "ok" {
					results = append(results, execution.Result)
				}
			}
		}
	}
	return results
}

func resolveDocumentTableView(registry *agenttools.ToolRegistry, steps []types.AgentStep, ref string) (documentTableViewSnapshot, error) {
	fail := func(message string) (documentTableViewSnapshot, error) {
		return documentTableViewSnapshot{}, fmt.Errorf("文档表格视图不可用：%s", message)
	}
	if registry == nil || !strings.HasPrefix(ref, documentTableViewPrefix) || len(ref) != len(documentTableViewPrefix)+64 {
		return fail("引用格式无效")
	}
	registered, err := registry.GetTool(documentTableCalculationToolName)
	tool, ok := registered.(*documentTableCalculationTool)
	if err != nil || !ok {
		return fail("本轮没有对应的表格执行器")
	}
	tool.viewsMu.RLock()
	frozen, exists := tool.views[ref]
	encoded, encodeErr := json.Marshal(frozen)
	tool.viewsMu.RUnlock()
	if !exists || encodeErr != nil {
		return fail("引用不是本轮实际冻结的视图")
	}
	digest, _ := documentTableDigest(frozen)
	if ref != documentTableViewPrefix+digest || frozen.Markdown != renderDocumentTableView(frozen) {
		return fail("冻结内容或摘要不一致")
	}
	matched := false
	for _, result := range successfulDocumentTableViewResults(steps) {
		if result["view_ref"] != ref || result["view_digest"] != digest || result["source_fingerprint"] != frozen.SourceFingerprint {
			continue
		}
		actual, _ := json.Marshal(result["view_snapshot"])
		var observed documentTableViewSnapshot
		decoder := json.NewDecoder(bytes.NewReader(actual))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&observed) != nil || decoder.Decode(new(any)) != io.EOF {
			return fail("实际执行结果中的快照格式已改变")
		}
		observedDigest, err := documentTableDigest(observed)
		if err != nil || observedDigest != digest {
			return fail("实际执行结果中的快照已改变")
		}
		matched = true
	}
	if !matched {
		return fail("缺少本轮成功执行结果，或结果已更改")
	}
	// Resolve against caller-supplied current steps, not an older closure alone.
	current := &documentTableCalculationTool{steps: func() []types.AgentStep { return steps }, registry: registry}
	source, err := current.selectDocumentTableSource(frozen.ChunkID, frozen.SourceFingerprint)
	if err != nil || source.Version != frozen.SourceVersion {
		return fail("来源已更改、超出当前范围或不再是本轮实际证据；请重新读取并选择")
	}
	var copy documentTableViewSnapshot
	if json.Unmarshal(encoded, &copy) != nil {
		return fail("冻结内容无法读取")
	}
	return copy, nil
}
