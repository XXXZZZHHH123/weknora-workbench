package agent

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

// Exact protocol operations only: a prefix match cannot create a capability.
// Progress accounting never authorizes a call or supplies answer evidence.
func structuredProgressOperation(call types.ToolCall) bool {
	if call.ExecutionName() == agenttools.ToolStructuredCapabilities {
		return true
	}
	op, ok := agenttools.StructuredOperationFromCall(call)
	if !ok {
		return false
	}
	switch op {
	case "describe_tools", "list_datasets", "list_series", "list_charts", "query_series", "compare_series",
		"convert_price", "inspect_source", "get_result_evidence", "analyze_table", "list_pdf_tables",
		"read_pdf_table", "query_pdf_table", "resolve_fields", "execute_field_plan":
		return true
	}
	return false
}

func progressJSON(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var canonical any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&canonical) != nil {
		return nil
	}
	return canonical
}

func structuredProgressKeys(call types.ToolCall) [][sha256.Size]byte {
	data := call.Result.Data
	if len(data) == 0 || data["status"] == "error" || data["execution_started"] == false {
		return nil
	}
	if call.ExecutionName() == agenttools.ToolStructuredCapabilities {
		var keys [][sha256.Size]byte
		entries, _ := progressJSON(data["tools"]).([]any)
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			// Refresh generation and manifest transport metadata can change
			// without changing any exposed tool schema or capability.
			delete(entry, "generation")
			delete(entry, "manifest_sha256")
			body, err := json.Marshal(entry)
			if err == nil {
				keys = append(keys, sha256.Sum256(body))
			}
		}
		return keys
	}
	canonical := progressJSON(data)
	if canonical == nil {
		return nil
	}
	stripStructuredProgressTransport(canonical)
	raw, err := json.Marshal(canonical)
	if err != nil || string(raw) == "{}" {
		return nil
	}
	return [][sha256.Size]byte{sha256.Sum256(raw)}
}

// SQL spelling, timing and execution IDs are not fresh observations. Keep
// values, source identities, versions, units and actual scope. result_hash is
// excluded because it also binds SQL text; it is still enforced by validators.
func stripStructuredProgressTransport(value any) {
	switch node := value.(type) {
	case map[string]any:
		for _, key := range []string{"sql", "sql_sha256", "result_id", "result_hash", "execution", "_structured_execution", "execution_id", "elapsed_ms", "duration_ms", "generated_at", "cache_hit", "hash_input_keys", "view_ref", "view_digest", "view_notice", "notice", "model_advice", "advice_notice"} {
			delete(node, key)
		}
		for key, child := range node {
			switch key {
			case "rows", "sample_rows", "selected_rows", "records", "values", "source_cells":
				continue // Business record keys are data, even if named sql/query/notice.
			}
			// Chart provenance is a serialized protocol object, not user prose.
			// Normalize its execution metadata exactly like the original result.
			if key == "provenance" || key == "source_provenance" {
				if raw, ok := child.(string); ok {
					var decoded any
					decoder := json.NewDecoder(strings.NewReader(raw))
					decoder.UseNumber()
					if decoder.Decode(&decoded) == nil {
						child = decoded
						node[key] = decoded
					}
				}
			}
			stripStructuredProgressTransport(child)
		}
	case []any:
		for _, child := range node {
			stripStructuredProgressTransport(child)
		}
	}
}

func savedChartHandle(call types.ToolCall) string {
	if call.Result == nil || !call.Result.Success || call.Result.Data["kind"] != "data_chart" || call.Result.Data["status"] != "ok" {
		return ""
	}
	artifact, _ := progressJSON(call.Result.Data["artifact"]).(map[string]any)
	handle, _ := artifact["handle"].(string)
	if _, ok := types.ParseResourcePath(handle); !ok {
		return ""
	}
	return handle
}

func savedChartProgress(call types.ToolCall) bool { return savedChartHandle(call) != "" }

func chartProgressKeys(call types.ToolCall) [][sha256.Size]byte {
	data := call.Result.Data
	projection := map[string]any{}
	switch data["kind"] {
	case "chart_source_catalog":
		var keys [][sha256.Size]byte
		for _, field := range []string{"sources", "input_sources"} {
			entries, _ := progressJSON(data[field]).([]any)
			for _, raw := range entries {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				// Seal IDs bind executions, so identical SQL executed under a new
				// call ID yields another handle. Its source metadata, actual sample
				// cells, units and range are the directory evidence; duplicating
				// that directory entry or reordering it does not add progress.
				if entry["provenance"] != nil {
					delete(entry, "source_id")
				}
				stripStructuredProgressTransport(entry)
				stripChartProgressHandles(entry)
				body, err := json.Marshal(entry)
				if err == nil {
					keys = append(keys, sha256.Sum256(body))
				}
			}
		}
		// Growing rejection lists and omitted counts report a bound, not new
		// source content. They must not turn a repeated rejected query into
		// endless successful inspect progress.
		return keys
	case "chart_source_mapping":
		parent := data["parent_source_id"]
		if parent == nil {
			parent = call.ExecutionArgs()["source_id"]
		}
		projection = map[string]any{"parent_source_id": parent, "mapping": data["mapping"]}
		if data["rows"] != nil && data["columns"] != nil && data["provenance"] != nil {
			// A new SQL execution can issue another parent handle for identical
			// data. Compare the exact mapped observations and normalized source
			// witness, not its presentation identity.
			projection = map[string]any{"mapping": data["mapping"], "rows": data["rows"], "columns": data["columns"], "provenance": data["provenance"], "source_truncated": data["source_truncated"]}
		}
	case "data_chart":
		if handle := savedChartHandle(call); handle != "" {
			projection["saved_chart"] = handle
		}
	}
	if len(projection) == 0 {
		return nil
	}
	canonical := progressJSON(projection)
	stripStructuredProgressTransport(canonical)
	stripChartProgressHandles(canonical)
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil
	}
	return [][sha256.Size]byte{sha256.Sum256(raw)}
}

// Only the renderer's protocol handles are execution identities. Real source
// IDs and record fields remain part of the observation. Hashes of a complete
// audit projection include transport/SQL spelling; actual rows and source
// snapshots above remain bound independently for this progress-only view.
func stripChartProgressHandles(value any) {
	switch node := value.(type) {
	case map[string]any:
		for _, key := range []string{"parent_provenance_sha256", "root_provenance_sha256", "complete_metadata_sha256", "complete_metadata_bytes"} {
			delete(node, key)
		}
		for key, child := range node {
			switch key {
			case "rows", "sample_rows", "selected_rows", "records", "values", "source_cells":
				continue
			case "source_id", "root_source_id", "parent_source_id":
				if id, ok := child.(string); ok && strings.HasPrefix(id, "chart-source-") {
					delete(node, key)
					continue
				}
			}
			stripChartProgressHandles(child)
		}
	case []any:
		for _, child := range node {
			stripChartProgressHandles(child)
		}
	}
}
