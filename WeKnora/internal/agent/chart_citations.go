package agent

import (
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/types"
)

// A graph association follows the renderer's exact sealed source identity.
// Matching a filename, a table name or equal numeric values is insufficient.
func (e *AgentEngine) finalChartEvidenceReferences(state *types.AgentState, existing types.EvidenceReferences) types.EvidenceReferences {
	if state == nil {
		return nil
	}
	bySource := map[string]types.ToolCall{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			if isStructuredQueryExecution(call) {
				if table, err := checkedStructuredChartTable(call); err == nil && table.Seal(call.ID+"/structured") == nil {
					bySource[table.ID] = call
				}
			}
			for _, table := range pdfChartInputSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}}) {
				bySource[table.ID] = call
			}
		}
	}
	refs := types.EvidenceReferences{}
	seen := map[string]bool{}
	for _, ref := range existing {
		seen[ref.ID] = true
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if !chartDeliveryExecution(call) || call.Result == nil || !call.Result.Success || call.Result.Data["status"] != "ok" {
				continue
			}
			data := call.Result.Data
			var delivery chart.Delivery
			raw, _ := json.Marshal(data["artifact"])
			if json.Unmarshal(raw, &delivery) != nil {
				continue
			}
			handle, ok := types.ParseResourcePath(delivery.Handle)
			if !ok || !strings.Contains(state.FinalAnswer, "]("+delivery.Handle+")") {
				continue
			}
			parent, ok := bySource[citationString(data["source_id"])]
			if !ok {
				parent, ok = chartCitationParent(citationString(data["provenance"]), bySource, 0)
			}
			if !ok {
				continue
			}
			var base *types.EvidenceReference
			for _, id := range e.sortedCurrentStructuredCitationIDs() {
				observation, current := e.currentStructuredObservation(id, state)
				if current && observation.CallID == parent.ID {
					copy := observation.Reference
					base = &copy
					break
				}
			}
			if base == nil {
				continue
			}
			id := "ev_" + reviewDigest([]any{e.turnReview.StructuredCitationNonce, call.ID, reviewDigest(data)})
			if seen[id] {
				continue
			}
			seen[id] = true
			selectedColumns := []string{}
			encoded, _ := json.Marshal(data["selected_columns"])
			var columns []chart.Column
			if json.Unmarshal(encoded, &columns) != nil {
				continue
			}
			for _, column := range columns {
				selectedColumns = append(selectedColumns, column.Name)
			}
			cells := chartCitationSourceCells(parent, data)
			locators := structuredCellLocators(cells, base.Sources[0].SourceSHA256)
			sources := append([]types.EvidenceSourceSnapshot(nil), base.Sources...)
			for i := range sources {
				sources[i].Locators = locators
			}
			manifest := map[string]any{"schema_version": 1, "tool": renderChartToolName, "tool_call_id": call.ID,
				"result": map[string]any{"columns": selectedColumns, "rows": data["selected_rows"]}, "column_metadata": data["selected_columns"],
				"source_cells": cells, "source_cells_scope": "available_original_spans_for_selected_cells", "source_mapping": data["source_mapping"], "chart_spec": data["chart_spec"], "source_result_id": base.ResultID, "source_result_hash": base.ResultHash,
				"computation": map[string]any{"sql": parent.Result.Data["sql"], "input_count": parent.Result.Data["input_count"], "input_complete": parent.Result.Data["input_complete"], "coverage": parent.Result.Data["coverage"], "provenance": parent.Result.Data["provenance"], "mapping_provenance": data["provenance"], "source_truncated": data["source_truncated"]}, "artifact_handle": delivery.Handle}
			if contract, ok := parent.Result.Data["result_contract"].(map[string]any); ok {
				computation := manifest["computation"].(map[string]any)
				computation["execution"] = contract["execution"]
				computation["source_snapshot"] = contract["source_snapshot"]
				manifest["evidence_capabilities"] = contract["evidence_capabilities"]
			}
			body, err := json.Marshal(manifest)
			if err != nil || len(body) > types.MaxEvidenceManifestBytes {
				continue
			}
			refs = append(refs, types.EvidenceReference{ID: id, Kind: types.EvidenceKindStructuredResult, ResultID: call.ID, ResultHash: reviewDigest(data), Title: base.Title, Summary: "图表实际使用的数据", CandidateHash: candidateTextDigest(state.FinalAnswer), ArtifactIDs: []string{handle, delivery.Handle}, VersionStatus: "snapshot", Sources: sources, Locators: locators, Manifest: body})
		}
	}
	return refs
}

func chartCitationParent(provenance string, sources map[string]types.ToolCall, depth int) (types.ToolCall, bool) {
	if depth > 5 || len(provenance) > 2<<20 {
		return types.ToolCall{}, false
	}
	var data map[string]any
	if json.Unmarshal([]byte(provenance), &data) != nil {
		return types.ToolCall{}, false
	}
	if call, ok := sources[citationString(data["source_id"])]; ok {
		return call, true
	}
	if nested, ok := data["source_provenance"].(string); ok {
		return chartCitationParent(nested, sources, depth+1)
	}
	return types.ToolCall{}, false
}

// Walk the actual executed projection chain. A final map's coordinates refer
// to its immediate input, which may itself be a map. Only the final chart's
// selected X/Y cells acquire original spans; unused mapped series do not.
// SQL result dependencies never acquire raw-cell identity here.
func chartCitationSourceCells(parent types.ToolCall, rendered map[string]any) []any {
	if structuredCitationOperation(parent) != "read_pdf_table" || parent.Result == nil {
		return nil
	}
	tables := pdfChartInputSources([]types.AgentStep{{ToolCalls: []types.ToolCall{parent}}})
	if len(tables) != 1 {
		return nil
	}
	var spec chart.Spec
	encoded, _ := json.Marshal(rendered["chart_spec"])
	if json.Unmarshal(encoded, &spec) != nil || len(spec.Y) == 0 {
		return nil
	}
	var selectedRows [][]string
	encoded, _ = json.Marshal(rendered["selected_rows"])
	if json.Unmarshal(encoded, &selectedRows) != nil || len(selectedRows) == 0 || len(selectedRows) > chart.MaxRows {
		return nil
	}
	indices := append([]int{spec.X}, spec.Y...)
	coordinates := make([]chartProjectionCoordinate, 0, len(selectedRows)*len(indices))
	for ri, row := range selectedRows {
		if len(row) != len(indices) {
			return nil
		}
		for _, ci := range indices {
			coordinates = append(coordinates, chartProjectionCoordinate{ri, ci})
		}
	}
	coordinates, ok := chartOriginalProjectionCoordinates(citationString(rendered["source_id"]), citationString(rendered["provenance"]), tables[0].ID, coordinates, 0)
	if !ok {
		return nil
	}
	data := parent.Result.Data
	rows, _ := data["rows"].([]any)
	columns, _ := data["columns"].([]any)
	cells := []any{}
	seen := map[chartProjectionCoordinate]bool{}
	for _, coordinate := range coordinates {
		ri, ci := coordinate.row, coordinate.column
		if ri < 0 || ri >= len(rows) || ci < 0 || ci >= len(columns) {
			return nil
		}
		if seen[coordinate] {
			continue
		}
		seen[coordinate] = true
		row, ok := rows[ri].(map[string]any)
		if !ok {
			return nil
		}
		column, ok := columns[ci].(map[string]any)
		if !ok {
			return nil
		}
		spans, _ := row["source_cells"].(map[string]any)
		selected, _ := spans[citationString(column["name"])].([]any)
		cells = append(cells, selected...)
	}
	return cells
}
