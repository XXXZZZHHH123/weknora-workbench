package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

// These are synchronous native source operations, not a structured_* prefix
// rule. Confirmation, writes, and asynchronous/unknown tools keep their own
// lifecycle and retry accounting.
func structuredProgressOperation(call types.ToolCall) (string, bool) {
	if call.ExecutionName() == agenttools.ToolStructuredCapabilities {
		return "describe_tools", true
	}
	operation, native := agenttools.StructuredOperationFromCall(call)
	if !native {
		return "", false
	}
	switch operation {
	case "describe_tools", "list_datasets", "list_series", "list_charts", "list_pdf_tables", "inspect_source",
		"query_series", "compare_series", "convert_price", "read_pdf_table", "query_pdf_table", "analyze_table", "get_result_evidence":
		return operation, true
	}
	return "", false
}

func structuredProgressHasResults(operation string) bool {
	switch operation {
	case "query_series", "compare_series", "convert_price", "read_pdf_table", "query_pdf_table", "analyze_table", "get_result_evidence":
		return true
	}
	return false
}

// Use copies: result contracts and chart receipts bind the original runtime
// payload. Progress deduplication must never rewrite those immutable witnesses.
func toolProgressCopy(data map[string]any) map[string]any {
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > 8*1024*1024 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if decoder.Decode(&result) != nil {
		return nil
	}
	return result
}

func structuredToolProgressEvidence(operation string, original map[string]any) [][sha256.Size]byte {
	data := toolProgressCopy(original)
	if len(data) == 0 {
		return nil
	}
	if status, _ := data["status"].(string); status != "" && status != "ok" {
		// Error, needs_source and protocol recovery responses do not become
		// evidence because their diagnostics or retryable flag have changed.
		return nil
	}
	if _, err := structuredresult.ValidatePayload(data); err != nil {
		return nil
	}
	stripStructuredProgressTransport(data)
	var keys [][sha256.Size]byte
	// A directory changing order or returning previously seen entries in a
	// different batch is not new source evidence. Empty coverage is still an
	// actual directory observation, so retain the remaining scope metadata.
	for _, field := range []string{"datasets", "series", "charts", "tables", "pdf_tables", "tools", "field_candidates", "literal_fields"} {
		rows, ok := data[field].([]any)
		if !ok || structuredProgressHasResults(operation) {
			continue
		}
		for _, row := range rows {
			keys = append(keys, toolProgressKey(operation+"/"+field, map[string]any{
				"source_context": data["source_context"], "dataset_id": data["dataset_id"], "entry": row,
			}))
		}
		delete(data, field)
	}
	if len(data) > 0 {
		keys = append(keys, toolProgressKey(operation, data))
	}
	return keys
}

// Only known envelope fields are omitted. A user column named "notice",
// "query" or "result_id" inside actual rows remains source data.
func stripStructuredProgressTransport(data map[string]any) {
	for _, key := range []string{"_structured_execution", "result_id", "sql", "query", "question", "search", "notice", "next_step", "model_advice", "advice_notice", "view_ref", "view_digest", "source_ref", "view_notice", "generation", "observed_manifest_sha256"} {
		delete(data, key)
	}
	if contract, ok := data["result_contract"].(map[string]any); ok {
		for _, key := range []string{"result_id", "result_hash", "hash_input_keys"} {
			delete(contract, key)
		}
		if execution, ok := contract["execution"].(map[string]any); ok {
			delete(execution, "sql_sha256")
		}
	}
}

func toolProgressKey(kind string, value any) [sha256.Size]byte {
	raw, _ := json.Marshal(struct {
		Kind string `json:"kind"`
		Data any    `json:"data"`
	}{kind, value})
	return sha256.Sum256(raw)
}

func chartToolProgressEvidence(original map[string]any) [][sha256.Size]byte {
	data := toolProgressCopy(original)
	if len(data) == 0 {
		return nil
	}
	if status, _ := data["status"].(string); status != "" && status != "ok" {
		return nil
	}
	switch data["kind"] {
	case "chart_source_catalog":
		var keys [][sha256.Size]byte
		for _, field := range []string{"sources", "input_sources"} {
			for _, raw := range reviewAsSlice(data[field]) {
				if source := reviewAsMap(raw); len(source) > 0 {
					if view := chartProgressSourceView(source, "sample_rows"); view != nil {
						keys = append(keys, toolProgressKey("chart_catalog/"+field, view))
					}
				}
			}
		}
		// Rejections and their changing prose/call IDs cannot refresh a stall.
		return keys
	case "chart_source_mapping":
		if view := chartProgressSourceView(data, "rows"); view != nil {
			// The actual mapped cells, labels and units matter. Unused mapping
			// arguments, another equivalent mapping chain and new handles do not.
			return [][sha256.Size]byte{toolProgressKey("chart_mapping", view)}
		}
	case "data_chart":
		artifact := reviewAsMap(data["artifact"])
		handle, _ := artifact["handle"].(string)
		if _, valid := types.ParseResourcePath(handle); !valid || data["status"] != "ok" {
			return nil
		}
		view := chartProgressSourceView(data, "selected_rows")
		if view != nil {
			spec := reviewAsMap(data["chart_spec"])
			delete(spec, "title")
			view["chart_spec"] = spec
			// A new PNG handle/title for identical plotted cells is not new
			// execution progress. A genuinely different successful plot is.
			return [][sha256.Size]byte{toolProgressKey("chart_render", view)}
		}
	}
	return nil
}

func chartProgressSourceView(source map[string]any, rowField string) map[string]any {
	columns := source["columns"]
	if rowField == "selected_rows" {
		columns = source["selected_columns"]
	}
	if columns == nil || source[rowField] == nil {
		return nil
	}
	provenance, _ := source["provenance"].(string)
	root, ok := chartProgressRoot(provenance)
	if !ok {
		return nil
	}
	view := map[string]any{"columns": columns, "rows": source[rowField], "source": root}
	if coordinates, valid := chartProgressCoordinates(source, rowField, columns); valid {
		view["original_coordinates"] = coordinates
	} else {
		return nil
	}
	for _, key := range []string{"row_count", "matrix_shape", "mapping_supported", "render_supported", "requires_mapping", "source_truncated"} {
		if value, exists := source[key]; exists {
			view[key] = value
		}
	}
	return view
}

// Equal values from different original cells remain distinct evidence. Reuse
// the chart citation coordinate walker so equivalent remap chains resolve to
// the same cells without treating execution-bound handles as new source data.
func chartProgressCoordinates(source map[string]any, rowField string, columns any) ([sha256.Size]byte, bool) {
	zero := [sha256.Size]byte{}
	sourceID, _ := source["source_id"].(string)
	provenance, _ := source["provenance"].(string)
	if sourceID == "" {
		return zero, false
	}
	originalID, parent := sourceID, provenance
	for depth := 0; depth <= chartMappingMaxDepth; depth++ {
		var layer map[string]json.RawMessage
		if json.Unmarshal([]byte(parent), &layer) != nil || string(layer["kind"]) != `"source_cell_projection"` {
			break
		}
		if json.Unmarshal(layer["source_id"], &originalID) != nil || json.Unmarshal(layer["source_provenance"], &parent) != nil || len(parent) > 2*1024*1024 {
			return zero, false
		}
	}
	rows, width := len(reviewAsSlice(source[rowField])), len(reviewAsSlice(columns))
	if rowField == "sample_rows" {
		rows = citationInt(source["row_count"])
	}
	if rows < 1 || rows > chart.MaxRows || width < 1 || width > chart.MaxColumns {
		return zero, false
	}
	indices := make([]int, width)
	for i := range indices {
		indices[i] = i
	}
	if rowField == "selected_rows" {
		raw, _ := json.Marshal(source["chart_spec"])
		var spec chart.Spec
		if json.Unmarshal(raw, &spec) != nil || len(spec.Y)+1 != width {
			return zero, false
		}
		indices = append([]int{spec.X}, spec.Y...)
	}
	coordinates := make([]chartProjectionCoordinate, 0, rows*width)
	for row := 0; row < rows; row++ {
		for _, column := range indices {
			coordinates = append(coordinates, chartProjectionCoordinate{row: row, column: column})
		}
	}
	original, valid := chartOriginalProjectionCoordinates(sourceID, provenance, originalID, coordinates, 0)
	if !valid {
		return zero, false
	}
	values := make([][2]int, len(original))
	for i, cell := range original {
		values[i] = [2]int{cell.row, cell.column}
	}
	return toolProgressKey("original_cells", values), true
}

func chartProgressRoot(provenance string) (any, bool) {
	if provenance == "" || len(provenance) > 2*1024*1024 {
		return nil, false
	}
	for depth := 0; depth <= chartMappingMaxDepth; depth++ {
		decoder := json.NewDecoder(bytes.NewBufferString(provenance))
		decoder.UseNumber()
		var root map[string]any
		if decoder.Decode(&root) != nil {
			// Document-table provenance is the runtime's literal source locator.
			return provenance, true
		}
		if root["kind"] == "source_cell_projection" {
			parent, _ := root["source_provenance"].(string)
			if parent == "" || len(parent) > 2*1024*1024 {
				return nil, false
			}
			provenance = parent
			continue
		}
		view := map[string]any{}
		for _, key := range []string{"source", "table_id", "dataset_id", "record_sha256", "bundle_sha256", "plan_sha256", "source_snapshot_hash"} {
			if value, exists := root[key]; exists {
				view[key] = value
			}
		}
		for _, field := range []string{"result_contract", "result_contract_summary"} {
			contract := reviewAsMap(root[field])
			if snapshot, exists := contract["source_snapshot"]; exists {
				view["source_snapshot"] = snapshot
			}
		}
		return view, len(view) > 0
	}
	return nil, false
}

// A saved chart is a runtime receipt; validate it independently of progress
// deduplication so an unchanged successful chart does not consume a failure.
func savedChartProgress(call types.ToolCall) bool {
	if call.Result == nil || !call.Result.Success || call.Result.Data["kind"] != "data_chart" || call.Result.Data["status"] != "ok" {
		return false
	}
	artifact := reviewAsMap(call.Result.Data["artifact"])
	handle, _ := artifact["handle"].(string)
	_, valid := types.ParseResourcePath(handle)
	return valid
}
