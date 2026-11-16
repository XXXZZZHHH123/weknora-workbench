package agent

import (
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
)

// Split a long relation by exact cells in a real dimension column. There is no
// model supplied label/value, implicit aggregation, sorting or unit conversion.
func mapChartSeries(source chart.Table, mapping chartSourceMapping) (chart.Table, error) {
	if mapping.Orientation != "columns" || mapping.XIndex == nil || mapping.SeriesColumn == nil || len(mapping.YIndexes) != 1 || len(mapping.ValueIndexes) == 0 || len(mapping.ValueIndexes) > chart.MaxRows || mapping.LabelIndex != nil || mapping.UnitIndex != nil {
		return chart.Table{}, fmt.Errorf("按真实标签拆系列需要columns方向、x_index、单个y_indexes、series_column及真实value_indexes")
	}
	x, y, label := *mapping.XIndex, mapping.YIndexes[0], *mapping.SeriesColumn
	indices := []int{x, y, label}
	if mapping.UnitColumn != nil {
		indices = append(indices, *mapping.UnitColumn)
	}
	seen := map[int]bool{}
	for _, index := range indices {
		if index < 0 || index >= len(source.Columns) || seen[index] {
			return chart.Table{}, fmt.Errorf("X、Y、series_column、unit_column须为不同的真实列索引")
		}
		seen[index] = true
		column := source.Columns[index]
		if column.Role == "source_row_identifier" || column.Name == "_row_id" || column.DependencyKind == "source_metadata" || column.DependencyKind == "constant_label" || column.DependencyKind == "mixed_constant_values" {
			return chart.Table{}, fmt.Errorf("系列标签、单位和坐标必须引用真实业务源字段，不能使用技术来源ID、CASE技术ID生成标签或常量列")
		}
	}
	if err := validateChartSourceColumns(source, chart.Spec{X: x, Y: []int{y}}); err != nil {
		return chart.Table{}, err
	}
	labels, xs := []string{}, []string{}
	labelIndex, xIndex := map[string]int{}, map[string]int{}
	unitRows := map[string][]int{}
	// Each entry identifies the source row for this output cell; -1 is an
	// absent observation, never a fabricated source coordinate.
	coordinates := [][]int{}
	rows := [][]string{}
	units := map[string]string{}
	unitMissing := map[string]bool{}
	seenRows := map[int]bool{}
	for _, rowIndex := range mapping.ValueIndexes {
		if rowIndex < 0 || rowIndex >= len(source.Rows) || seenRows[rowIndex] || len(source.Rows[rowIndex]) != len(source.Columns) {
			return chart.Table{}, fmt.Errorf("value_indexes须为不重复的真实观测行")
		}
		seenRows[rowIndex] = true
		row := source.Rows[rowIndex]
		name, xv := row[label], row[x]
		if strings.TrimSpace(name) == "" || strings.TrimSpace(xv) == "" || len(name) > 512 {
			return chart.Table{}, fmt.Errorf("真实系列标签和X值不能为空，标签须在绘图长度限制内")
		}
		li, exists := labelIndex[name]
		if !exists {
			if len(labels) >= 6 {
				return chart.Table{}, fmt.Errorf("真实系列超过6条；请显式筛选范围或拆图，不会截断系列")
			}
			labels = append(labels, name)
			li = len(labels)
			labelIndex[name] = li
			for i := range rows {
				rows[i] = append(rows[i], "")
				coordinates[i] = append(coordinates[i], -1)
			}
		}
		ri, exists := xIndex[xv]
		if !exists {
			xs = append(xs, xv)
			ri = len(xs) - 1
			xIndex[xv] = ri
			values := make([]string, 1+len(labels))
			values[0] = xv
			refs := make([]int, len(values))
			for i := range refs {
				refs[i] = -1
			}
			refs[0] = rowIndex
			rows, coordinates = append(rows, values), append(coordinates, refs)
		}
		if coordinates[ri][li] != -1 {
			return chart.Table{}, fmt.Errorf("同一真实标签和X值存在多行；须由模型显式查询或选择额外真实维度，程序不自动求和或覆盖")
		}
		rows[ri][li], coordinates[ri][li] = row[y], rowIndex
		if mapping.UnitColumn != nil {
			unitRows[name] = append(unitRows[name], rowIndex)
			u := row[*mapping.UnitColumn]
			if strings.TrimSpace(u) == "" {
				unitMissing[name] = true
			} else if previous := units[name]; previous != "" && previous != u {
				return chart.Table{}, fmt.Errorf("真实系列%q的unit_column存在多个显式单位；请按真实单位筛选或拆图", name)
			} else {
				units[name] = u
			}
		}
	}
	columns := make([]chart.Column, 1+len(labels))
	columns[0] = source.Columns[x]
	for i, name := range labels {
		columns[i+1] = source.Columns[y]
		columns[i+1].Name, columns[i+1].DisplayName = name, name
		if mapping.UnitColumn != nil {
			// Positive mixed evidence is never cleared by a presentation map.
			if columns[i+1].UnitMetadata != nil && columns[i+1].UnitMetadata.Status == "mixed" {
				continue
			}
			u := units[name]
			if original := chartExplicitUnit(source.Columns[y]); original != "" && u != "" && original != u {
				return chart.Table{}, fmt.Errorf("unit_column与Y列已有显式单位不一致；不能用标签覆盖数值单位")
			}
			columns[i+1].Unit = ""
			if u == "" || unitMissing[name] {
				columns[i+1].UnitMetadata = &structuredresult.Unit{Status: "unknown", BindingScope: "result_column", EvidenceLevel: "none", Reason: "Selected source unit cells are missing; no unit is asserted"}
			} else {
				columns[i+1].Unit = u
				columns[i+1].UnitMetadata = &structuredresult.Unit{Status: "declared", Symbol: u, BindingScope: "result_column", EvidenceLevel: "source_unit_column", Reason: "Copied unchanged from the explicitly selected real unit column; semantic meaning still requires independent review", Evidence: []map[string]any{{"source_id": source.ID, "source_column_index": *mapping.UnitColumn, "source_column": source.Columns[*mapping.UnitColumn].Name, "source_row_indexes": unitRows[name], "source_value": u, "original_annotation_verified": false}}}
			}
		}
	}
	metadata := map[string]any{"kind": "source_cell_projection", "source_id": source.ID, "source_provenance": source.Provenance, "mapping": mapping, "source_rows": len(source.Rows), "source_columns": len(source.Columns), "projection_applied_by_runtime": true, "projection_output_rows": len(rows), "series_row_indexes": coordinates, "series_labels": labels, "source_cells": source.Rows}
	provenance, partial, err := boundedChartMappingMetadata(metadata)
	if err != nil {
		return chart.Table{}, err
	}
	notice := source.Notice + " 按series_column真实单元格逐字拆系列，保留首次出现顺序及缺失；unit_column仅复制真实字段，不能据标签猜单位。"
	if partial {
		notice += " 重复元数据按摘要绑定，精确系列行坐标完整保留。"
	}
	result := chart.Table{Columns: columns, Rows: rows, Provenance: provenance, Notice: notice, Truncated: source.Truncated}
	if err := result.Seal(source.ID + "/series-projection"); err != nil {
		return chart.Table{}, err
	}
	return result, nil
}
