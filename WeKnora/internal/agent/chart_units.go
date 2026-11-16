package agent

import (
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
)

// Unit state comes from verified result metadata, never question/label text.
// Unknown units are usable as raw values; mixed is positive conflict evidence.
func validateChartUnits(table chart.Table, spec chart.Spec) error {
	unit := ""
	for _, index := range spec.Y {
		if index < 0 || index >= len(table.Columns) {
			return fmt.Errorf("绘图必须选择真实Y列")
		}
		column := table.Columns[index]
		if column.UnitMetadata != nil && column.UnitMetadata.Status == "mixed" {
			return fmt.Errorf("Y列%q包含已知混合单位（%s），不能使用同一纵轴；请按来源真实单位筛选或拆图，不能用未知单位覆盖冲突", column.Label(), column.UnitMetadata.Reason)
		}
		u := chartExplicitUnit(column)
		if u != "" {
			if unit != "" && unit != u {
				return fmt.Errorf("同一纵轴的显式单位不同（%s、%s）；请按真实单位分别选取来源并拆图，不会自动合并或换算", unit, u)
			}
			unit = u
		}
	}
	return nil
}

func chartExplicitUnit(column chart.Column) string {
	if column.UnitMetadata != nil {
		switch column.UnitMetadata.Status {
		case "declared", "derived":
			return column.UnitMetadata.Symbol
		default:
			return ""
		}
	}
	return column.Unit
}

// The legacy PNG renderer rejects multiple unknown series. Its presentation
// copy has no asserted common unit when any selected Y is unknown. The sealed
// source, semantic review and returned audit retain all original metadata.
func chartUnitPresentation(table chart.Table, spec chart.Spec) chart.Table {
	out := table
	out.Columns = append([]chart.Column(nil), table.Columns...)
	unknown := false
	for _, index := range spec.Y {
		unknown = unknown || strings.TrimSpace(chartExplicitUnit(table.Columns[index])) == ""
	}
	if unknown {
		for _, index := range spec.Y {
			out.Columns[index].Unit = ""
			out.Columns[index].UnitMetadata = nil
		}
	}
	return out
}

// Transposing without unit_index must not erase known unit conflicts.
func projectedChartUnit(columns []chart.Column) *structuredresult.Unit {
	var mixed *structuredresult.Unit
	symbols := map[string]bool{}
	reasons, evidence := []string{}, []map[string]any{}
	seenEvidence := map[string]bool{}
	for _, column := range columns {
		if u := chartExplicitUnit(column); u != "" {
			symbols[u] = true
		}
		if column.UnitMetadata != nil && column.UnitMetadata.Status == "mixed" {
			copy := *column.UnitMetadata
			mixed = &copy
			if copy.Reason != "" {
				reasons = append(reasons, copy.Reason)
			}
		}
		if column.UnitMetadata != nil {
			for _, entry := range column.UnitMetadata.Evidence {
				key := reviewDigest(entry)
				if !seenEvidence[key] {
					evidence = append(evidence, entry)
					seenEvidence[key] = true
				}
			}
		}
	}
	if mixed != nil {
		mixed.Reason = strings.Join(reasons, "; ")
		mixed.Evidence = evidence
		return mixed
	}
	if len(symbols) > 1 {
		return &structuredresult.Unit{Status: "mixed", EvidenceLevel: "none", BindingScope: "result_column", Evidence: evidence, Reason: "Selected original observation columns declare different units"}
	}
	return nil
}
