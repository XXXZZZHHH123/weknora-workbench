package modelcontext

import "encoding/json"

// Keep the full source inspection in the persisted ToolResult.Data while
// presenting a compact, lossless view of the requested cells to the model.
// Repeating the file/sheet/status on every cell wastes the result budget and
// can push a second, conflicting table out of the model's visible evidence.
func compactStructuredSource(data map[string]any) (string, bool) {
	if data["kind"] != "structured_source_inspection" || data["status"] != "ok" {
		return "", false
	}
	compact := make(map[string]any, len(data)+1)
	for key, value := range data {
		compact[key] = value
	}
	compact["cell_columns"] = []string{"cell", "value", "formula_if_present", "status_if_not_original"}
	compactCells := func(value any) any {
		cells, ok := value.([]any)
		if !ok {
			return value
		}
		rows := make([]any, 0, len(cells))
		for _, item := range cells {
			cell, ok := item.(map[string]any)
			if !ok {
				rows = append(rows, item)
				continue
			}
			row := []any{cell["cell"], cell["value"]}
			formula, hasFormula := cell["formula"].(string)
			status, _ := cell["value_status"].(string)
			if hasFormula && formula != "" || status != "" && status != "original" {
				row = append(row, cell["formula"], status)
			}
			rows = append(rows, row)
		}
		return rows
	}
	for _, key := range []string{"matches", "ranges", "calculations"} {
		items, ok := data[key].([]any)
		if !ok {
			continue
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			original, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			row := make(map[string]any, len(original))
			for field, value := range original {
				row[field] = value
			}
			for _, field := range []string{"cells", "context_cells"} {
				if cells, exists := row[field]; exists {
					row[field] = compactCells(cells)
				}
			}
			// The match's raw formula is kept in formula, while value always
			// denotes the saved original/cached value, never a recalculation.
			delete(row, "raw_value")
			delete(row, "cached_value")
			out = append(out, row)
		}
		compact[key] = out
	}
	encoded, err := json.Marshal(compact)
	return string(encoded), err == nil
}
