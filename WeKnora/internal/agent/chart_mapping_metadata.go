package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/chart"
)

const chartMappingMetadataBudget = 16 * 1024
const chartMappingMaxDepth = 8

// Mapping coordinates are complete even when optional source detail does not
// fit. The output Table.Seal separately binds every actual output row/column;
// source_id and parent_provenance_sha256 bind the exact complete parent.
func boundedChartMappingMetadata(metadata map[string]any) (string, bool, error) {
	full, err := json.Marshal(metadata)
	if err != nil {
		return "", false, fmt.Errorf("映射来源元数据无法编码")
	}
	if _, err = chartMappingDepth(metadata, 0); err != nil {
		return "", false, err
	}
	if len(full) <= chartMappingMetadataBudget {
		return string(full), false, nil
	}
	view, err := compactChartMappingLayer(metadata, 0)
	if err != nil {
		return "", false, err
	}
	view["projection_partial"] = true
	view["complete_metadata_sha256"] = chartMappingDigest(full)
	view["complete_metadata_bytes"] = len(full)
	view["metadata_notice"] = "仅省略重复的原始元数据、单元格矩阵和列单位证据副本；精确mapping链完整保留，实际输出列/单位与值仍独立绑定。摘要不能证明省略内容、单位原注适用、OCR准确或完整文档覆盖。"
	encoded, err := json.Marshal(view)
	if err != nil || len(encoded) > chartMappingMetadataBudget {
		return "", false, fmt.Errorf("完整映射链超过元数据预算；请减少重复映射或从当前原始来源直接映射，未截断坐标")
	}
	return string(encoded), true, nil
}

func chartMappingDigest(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func chartMappingDepth(layer map[string]any, depth int) (int, error) {
	if layer["kind"] != "source_cell_projection" {
		return depth, nil
	}
	if depth >= chartMappingMaxDepth {
		return 0, fmt.Errorf("映射链最多8层；请从当前原始来源直接映射，未复用不完整坐标")
	}
	nested, ok := layer["source_provenance"].(string)
	if !ok {
		return depth + 1, nil
	}
	var parent map[string]any
	if json.Unmarshal([]byte(nested), &parent) != nil {
		return depth + 1, nil
	}
	return chartMappingDepth(parent, depth+1)
}

func compactChartMappingLayer(layer map[string]any, depth int) (map[string]any, error) {
	if depth >= chartMappingMaxDepth {
		return nil, fmt.Errorf("映射链最多8层；请从当前原始来源直接映射")
	}
	view := map[string]any{}
	for _, key := range []string{"kind", "source_id", "mapping", "source_rows", "source_columns", "projection_applied_by_runtime", "projection_output_rows"} {
		value, ok := layer[key]
		if !ok {
			return nil, fmt.Errorf("映射链缺少实际坐标或范围，不能压缩成完整证明")
		}
		view[key] = value
	}
	parentText, _ := layer["source_provenance"].(string)
	if original, exists := layer["parent_provenance_sha256"]; exists {
		view["parent_provenance_sha256"] = original
	} else {
		view["parent_provenance_sha256"] = chartMappingDigest([]byte(parentText))
	}
	var parent map[string]any
	if json.Unmarshal([]byte(parentText), &parent) == nil && parent["kind"] == "source_cell_projection" {
		compact, err := compactChartMappingLayer(parent, depth+1)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(compact)
		if err != nil {
			return nil, err
		}
		view["source_provenance"] = string(encoded)
	} else {
		root := compactChartMappingRoot(parent)
		root["root_source_id"] = layer["source_id"]
		if _, exists := root["root_provenance_sha256"]; !exists {
			root["root_provenance_sha256"] = chartMappingDigest([]byte(parentText))
		}
		root["projection_partial"] = true
		encoded, err := json.Marshal(root)
		if err != nil {
			return nil, err
		}
		view["source_provenance"] = string(encoded)
	}
	// Earlier compact layers may already have bound an omitted full witness.
	for _, key := range []string{"complete_metadata_sha256", "complete_metadata_bytes"} {
		if value, ok := layer[key]; ok {
			view[key] = value
		}
	}
	return view, nil
}

func compactChartMappingRoot(root map[string]any) map[string]any {
	view := map[string]any{}
	for _, key := range []string{"kind", "source", "table_id", "dataset_id", "record_sha256", "bundle_sha256", "plan_sha256", "source_snapshot_hash", "root_source_id", "root_provenance_sha256", "result_contract_summary", "input_complete", "input_count", "output_truncated", "truncated", "returned_row_count", "sql_source_dependency_verified", "sql_source_dependency_method", "source_column_unit_summary", "coverage_projection", "notice_projection", "warnings_projection", "sql_column_dependencies_projection", "source_semantics_projection"} {
		if value, ok := root[key]; ok {
			view[key] = value
		}
	}
	if contract, ok := root["result_contract"].(map[string]any); ok {
		summary := map[string]any{}
		for _, key := range []string{"schema_version", "result_id", "result_hash", "source_snapshot", "execution", "evidence_capabilities"} {
			if value, ok := contract[key]; ok {
				summary[key] = value
			}
		}
		view["result_contract_summary"] = summary
	}
	for _, key := range []string{"coverage", "notice", "warnings", "sql_column_dependencies", "source_semantics"} {
		if value, ok := root[key]; ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}
			if len(encoded) <= 2048 {
				view[key] = value
			} else {
				view[key+"_projection"] = map[string]any{"sha256": chartMappingDigest(encoded), "original_bytes": len(encoded), "details_omitted": true, "meaning": "原始限制详情未展示，不能由摘要推断已解决或不存在"}
			}
		}
	}
	if provenance, ok := root["provenance"].(map[string]any); ok {
		selected := map[string]any{}
		for _, key := range []string{"scope", "draft_id", "bundle_sha256", "plan_sha256", "table_record_sha256", "sql_source_dependency_verified", "sql_result_lineage_verified", "input_row_count"} {
			if value, ok := provenance[key]; ok {
				selected[key] = value
			}
		}
		view["provenance"] = selected
	}
	// Unit declarations remain in the actual output Columns. Root metadata only
	// repeats their status/symbol/limits; never duplicate their evidence arrays.
	if columns, ok := root["columns"].([]any); ok {
		units := []any{}
		for _, raw := range columns {
			if column, ok := raw.(map[string]any); ok {
				unit := map[string]any{}
				for _, key := range []string{"name", "display_name", "unit", "role", "dependency_kind"} {
					if value, ok := column[key]; ok {
						unit[key] = value
					}
				}
				if metadata, ok := column["unit_metadata"].(map[string]any); ok {
					status := map[string]any{}
					for _, key := range []string{"status", "symbol", "evidence_level", "binding_scope", "expression", "quantity", "reason"} {
						if value, ok := metadata[key]; ok {
							status[key] = value
						}
					}
					unit["unit_metadata_summary"] = status
				}
				units = append(units, unit)
			}
		}
		view["source_column_unit_summary"] = units
	}
	return view
}

type chartProjectionCoordinate struct{ row, column int }

func chartOriginalProjectionCoordinates(sourceID, provenance, originalID string, coordinates []chartProjectionCoordinate, depth int) ([]chartProjectionCoordinate, bool) {
	if sourceID == originalID {
		return coordinates, true
	}
	if sourceID == "" || depth >= 8 || len(provenance) > 2<<20 {
		return nil, false
	}
	var projection struct {
		Kind             string             `json:"kind"`
		SourceID         string             `json:"source_id"`
		SourceProvenance string             `json:"source_provenance"`
		Mapping          chartSourceMapping `json:"mapping"`
		SourceRows       int                `json:"source_rows"`
		SourceColumns    int                `json:"source_columns"`
		Applied          bool               `json:"projection_applied_by_runtime"`
		OutputRows       int                `json:"projection_output_rows"`
	}
	if json.Unmarshal([]byte(provenance), &projection) != nil || projection.Kind != "source_cell_projection" || !projection.Applied || projection.SourceID == "" || projection.Mapping.XIndex == nil || projection.SourceRows <= 0 || projection.SourceColumns <= 0 || projection.OutputRows != len(projection.Mapping.ValueIndexes) {
		return nil, false
	}
	mapping := projection.Mapping
	indices := append([]int{*mapping.XIndex}, mapping.YIndexes...)
	if mapping.Orientation != "rows" && mapping.Orientation != "columns" || len(indices) < 2 || len(indices) > 7 || len(mapping.ValueIndexes) > chart.MaxRows {
		return nil, false
	}
	transformed := make([]chartProjectionCoordinate, 0, len(coordinates))
	for _, cell := range coordinates {
		if cell.row < 0 || cell.row >= len(mapping.ValueIndexes) || cell.column < 0 || cell.column >= len(indices) {
			return nil, false
		}
		parent := chartProjectionCoordinate{mapping.ValueIndexes[cell.row], indices[cell.column]}
		if mapping.Orientation == "rows" {
			parent = chartProjectionCoordinate{indices[cell.column], mapping.ValueIndexes[cell.row]}
		}
		if parent.row < 0 || parent.row >= projection.SourceRows || parent.column < 0 || parent.column >= projection.SourceColumns {
			return nil, false
		}
		transformed = append(transformed, parent)
	}
	return chartOriginalProjectionCoordinates(projection.SourceID, projection.SourceProvenance, originalID, transformed, depth+1)
}
