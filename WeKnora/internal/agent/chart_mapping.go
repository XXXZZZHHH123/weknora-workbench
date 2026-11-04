package agent

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
)

// The model chooses the meaning and orientation. The runtime copies only cells
// at these checked coordinates; no observation values are accepted in a plan.
type chartSourceMapping struct {
	Orientation  string `json:"orientation"`
	XIndex       *int   `json:"x_index"`
	YIndexes     []int  `json:"y_indexes"`
	ValueIndexes []int  `json:"value_indexes"`
	LabelIndex   *int   `json:"label_index,omitempty"`
	UnitIndex    *int   `json:"unit_index,omitempty"`
}

type mappedChartSource struct {
	Table    chart.Table
	ParentID string
	Mapping  chartSourceMapping
}

func pdfChartInputSources(steps []types.AgentStep) []chart.Table {
	tables := []chart.Table{}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || !isStructuredPDFSourceRead(call) {
				continue
			}
			encoded, err := json.Marshal(call.Result.Data)
			if err != nil || len(encoded) > chart.MaxSourceBytes {
				continue
			}
			var data struct {
				Kind, Status    string
				SourceKind      string `json:"source_kind"`
				TableID         string `json:"table_id"`
				RecordSHA       string `json:"record_sha256"`
				Source          map[string]any
				Columns         []chart.Column
				Rows            []struct{ Values map[string]any }
				Coverage        map[string]any
				PageContext     any  `json:"page_context"`
				OutputTruncated bool `json:"output_truncated"`
				Offset          int  `json:"offset"`
				TotalRowCount   *int `json:"total_row_count"`
			}
			if json.Unmarshal(encoded, &data) != nil {
				continue
			}
			if data.Kind != "structured_source_inspection" || data.Status != "ok" || data.SourceKind != "pdf_table" || data.TableID == "" || len(data.Rows) == 0 || len(data.Rows) > chart.MaxRows {
				continue
			}
			digest, _ := data.Source["sha256"].(string)
			decoded, err := hex.DecodeString(digest)
			file, _ := data.Source["file_name"].(string)
			if err != nil || len(decoded) != 32 || strings.TrimSpace(file) == "" {
				continue
			}
			matrix := make([][]string, 0, len(data.Rows))
			valid := len(data.Columns) >= 2 && len(data.Columns) <= chart.MaxColumns
			for _, row := range data.Rows {
				values := []string{}
				for _, col := range data.Columns {
					raw, exists := row.Values[col.Name]
					cell, err := chart.Cell(raw)
					if !exists || err != nil {
						valid = false
					}
					values = append(values, cell)
				}
				matrix = append(matrix, values)
			}
			if !valid {
				continue
			}
			meta, _ := json.Marshal(map[string]any{"kind": "pdf_source_cells", "source": data.Source, "table_id": data.TableID, "record_sha256": data.RecordSHA, "coverage": data.Coverage, "columns": data.Columns, "offset": data.Offset, "total_row_count": data.TotalRowCount, "returned_row_count": len(data.Rows), "page_context": data.PageContext, "source_snapshot_hash": reviewDigest(call.Result.Data)})
			table := chart.Table{Columns: data.Columns, Rows: matrix, Provenance: string(meta), Truncated: data.OutputTruncated || data.Coverage["table_input_complete"] != true || data.Offset > 0 || data.TotalRowCount == nil || data.TotalRowCount != nil && *data.TotalRowCount != len(data.Rows), Notice: "本轮实际读取的PDF单元格，须按完整任务显式map；来源审核不证明完整文档覆盖或OCR准确。索引均为0起算，不能把技术行ID当业务维度。"}
			if table.Seal(call.ID+"/pdf-cells") == nil && len(tables) < chart.MaxSources {
				tables = append(tables, table)
			}
		}
	}
	return tables
}

func isStructuredPDFSourceRead(call types.ToolCall) bool {
	switch call.ExecutionName() {
	case "structured_read_pdf_table":
		return true
	case "structured_data":
		return call.ExecutionArgs()["operation"] == "read_pdf_table"
	}
	return false
}

func mapChartSource(source chart.Table, mapping chartSourceMapping) (chart.Table, error) {
	if mapping.XIndex == nil || len(mapping.YIndexes) < 1 || len(mapping.YIndexes) > 6 || len(mapping.ValueIndexes) < 1 || len(mapping.ValueIndexes) > chart.MaxRows {
		return chart.Table{}, fmt.Errorf("map需要真实x_index、1至6个y_indexes及value_indexes；不能提交观测值")
	}
	if mapping.Orientation != "rows" && mapping.Orientation != "columns" {
		return chart.Table{}, fmt.Errorf("orientation必须为rows或columns，由模型依据真实表格选择")
	}
	if mapping.Orientation == "rows" && mapping.LabelIndex == nil {
		return chart.Table{}, fmt.Errorf("rows映射需要label_index复制真实行标签，不能用技术行号作图轴或图例；原文无标签时请用引用真实源值且显式列名的查询整形")
	}
	width, values := len(source.Columns), len(source.Rows)
	if mapping.Orientation == "rows" {
		width, values = values, width
	}
	indexes := append([]int{*mapping.XIndex}, mapping.YIndexes...)
	seen := map[int]bool{}
	for _, i := range indexes {
		if i < 0 || i >= width || seen[i] {
			return chart.Table{}, fmt.Errorf("map的X/Y索引必须是不同的真实行或列")
		}
		seen[i] = true
	}
	seen = map[int]bool{}
	for _, i := range mapping.ValueIndexes {
		if i < 0 || i >= values || seen[i] || mapping.LabelIndex != nil && *mapping.LabelIndex == i {
			return chart.Table{}, fmt.Errorf("value_indexes须为不重复的真实观测坐标，不能包含标签坐标")
		}
		seen[i] = true
	}
	columns := make([]chart.Column, len(indexes))
	for i, index := range indexes {
		if mapping.Orientation == "columns" {
			columns[i] = source.Columns[index]
		} else {
			if mapping.LabelIndex != nil {
				if *mapping.LabelIndex < 0 || *mapping.LabelIndex >= len(source.Columns) {
					return chart.Table{}, fmt.Errorf("label_index不是实际来源坐标")
				}
				columns[i].Name = source.Rows[index][*mapping.LabelIndex]
			}
			// Explicitly copy the selected declared unit, never infer it from
			// values, titles, years or the requested indicator.
			if i > 0 && mapping.UnitIndex != nil {
				if *mapping.UnitIndex < 0 || *mapping.UnitIndex >= len(source.Columns) {
					return chart.Table{}, fmt.Errorf("unit_index不是实际来源列元数据")
				}
				unit := source.Columns[*mapping.UnitIndex].Unit
				for _, selected := range mapping.ValueIndexes {
					if source.Columns[selected].Unit != unit {
						return chart.Table{}, fmt.Errorf("所选观测列的显式单位不一致，不能用一列单位覆盖其他观测；须分图或明确合法转换")
					}
				}
				columns[i].Unit = unit
			}
		}
	}
	rows := make([][]string, 0, len(mapping.ValueIndexes))
	for _, value := range mapping.ValueIndexes {
		row := []string{}
		for _, index := range indexes {
			if mapping.Orientation == "rows" {
				row = append(row, source.Rows[index][value])
			} else {
				row = append(row, source.Rows[value][index])
			}
		}
		rows = append(rows, row)
	}
	meta, _ := json.Marshal(map[string]any{"kind": "source_cell_projection", "source_id": source.ID, "source_provenance": source.Provenance, "mapping": mapping, "source_rows": len(source.Rows), "source_columns": len(source.Columns), "projection_applied_by_runtime": true, "projection_output_rows": len(rows), "source_cells": source.Rows})
	table := chart.Table{Columns: columns, Rows: rows, Provenance: string(meta), Truncated: source.Truncated, Notice: source.Notice + " 仅复制显式选择的真实单元格，保留原值、缺失和顺序；所选范围不代表全库。"}
	if err := table.Seal(source.ID + "/map"); err != nil {
		return chart.Table{}, err
	}
	return table, nil
}
