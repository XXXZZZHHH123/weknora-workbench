package agent

import (
	"encoding/json"
	"reflect"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

// Delivery review receives navigation/status only, never a second source pool.
// Native registrations and private execution descriptors qualify observations;
// candidate wording, question keywords and missing numbers never do.
func (e *AgentEngine) discoveryReadObservations(state *types.AgentState) map[string]any {
	if state == nil || e.toolRegistry == nil {
		return nil
	}
	var observations []map[string]any
	omitted := 0
	readObserved := false
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			data := call.Result.Data
			var summary map[string]any
			if descriptor, ok := data["_structured_execution"].(agenttools.StructuredOperationDescriptor); ok && descriptor.EffectClass == "read" {
				switch descriptor.Operation {
				case "inspect_source", "query_series", "analyze_table", "read_pdf_table", "query_pdf_table":
					if data["status"] != "ok" {
						continue
					}
					if data["source"] == nil && data["source_file"] == nil && data["inputs"] == nil && data["rows"] == nil && data["values"] == nil {
						continue
					}
					readObserved = true
					summary = discoveryReadFields(data, "status", "kind", "source", "source_file", "dataset_id", "source_context", "hash_verified", "input_complete", "output_truncated", "returned_row_count")
				case "list_datasets", "list_series", "list_charts", "list_pdf_tables":
					if data["pending_sheets"] == nil {
						continue
					}
					summary = discoveryReadFields(data, "dataset_id", "source_context", "pending_sheets")
				default:
					continue
				}
				summary["operation"] = descriptor.Operation
			} else {
				registered, err := e.toolRegistry.GetTool(call.ExecutionName())
				if err != nil {
					continue
				}
				switch registered.(type) {
				case *agenttools.ReadDocumentTool:
					if !sourceNavigationReadObserved(data) && len(sourceNavigationRows(data["chunks"])) == 0 {
						continue
					}
					readObserved = true
					summary = discoveryReadFields(data, "knowledge_id", "knowledge_base_id", "knowledge_title", "page", "requested_pages", "page_complete", "pages_complete", "truncated", "next_offset", "fetched_chunks", "document_scope")
					summary["body_navigation_samples"] = discoveryBodyNavigation(data["chunks"])
					summary["image_catalog_navigation"] = discoveryImageNavigation(data["image_catalog"])
					summary["actual_image_read_status"] = discoveryImageNavigation(data["image_evidence"])
				case *agenttools.SearchKnowledgeTool:
					rows := sourceNavigationRows(data["results"])
					for _, row := range rows {
						if sourceNavigationString(row, "content") != "" {
							readObserved = true
						}
					}
					if len(rows) == 0 {
						continue
					}
					summary = map[string]any{"body_navigation_samples": discoveryBodyNavigation(data["results"])}
				default:
					continue
				}
				summary["tool_name"] = call.ExecutionName()
			}
			// Retain the most recent execution status in chronological order.
			// Earlier catalogs must not crowd out the actual read they led to.
			if len(observations) >= 8 {
				omitted++
				observations = observations[1:]
			}
			observations = append(observations, summary)
		}
	}
	if !readObserved {
		return nil
	}
	for {
		view := map[string]any{"observations": observations, "omitted_observations": omitted, "complete_source_values_omitted": true, "navigation_only": true, "task_coverage_established": false}
		encoded, err := json.Marshal(view)
		if err == nil && len(encoded) <= 12*1024 {
			return view
		}
		if len(observations) == 0 {
			return nil
		}
		observations = observations[1:]
		omitted++
	}
}

func discoveryReadFields(data map[string]any, keys ...string) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		if value, ok := data[key]; ok {
			// Preserve small original protocol objects exactly; oversized values
			// are explicitly omitted, never cut into invalid or misleading JSON.
			encoded, err := json.Marshal(value)
			if err != nil || len(encoded) > 1024 {
				result[key+"_omitted"] = true
				continue
			}
			result[key] = value
		}
	}
	return result
}

func discoveryBodyNavigation(value any) map[string]any {
	rows := sourceNavigationRows(value)
	var samples []map[string]any
	for _, row := range rows {
		if len(samples) >= 4 {
			break
		}
		sample := discoveryReadFields(row, "knowledge_id", "knowledge_base_id", "knowledge_title", "chunk_id", "source_chunk_id", "page")
		body := sourceNavigationString(row, "content")
		if body == "" {
			continue
		}
		sample["navigation_excerpt"] = taskPlanPrefix(body, 1024)
		sample["excerpt_complete"] = len(body) <= 1024
		samples = append(samples, sample)
	}
	return map[string]any{"samples": samples, "omitted_rows": len(rows) - len(samples), "navigation_only": true}
}

func discoveryImageNavigation(value any) map[string]any {
	// Native tools may return a private []documentImage, not []map. Normalize
	// only the bounded prefix, using its actual JSON tags; a persisted JSON
	// replay alone cannot test this runtime representation.
	rows := reflect.ValueOf(value)
	var samples []map[string]any
	if !rows.IsValid() || rows.Kind() != reflect.Slice {
		return map[string]any{"samples": samples, "omitted_rows": 0}
	}
	keys := []string{"reference", "knowledge_id", "chunk_id", "page", "page_hint", "page_verified", "label", "source_part", "read_args", "read_success", "geometry_read_success"}
	for index := 0; index < rows.Len() && index < 4; index++ {
		value := rows.Index(index).Interface()
		row, ok := value.(map[string]any)
		if !ok {
			item := reflect.ValueOf(value)
			if item.Kind() == reflect.Pointer && !item.IsNil() {
				item = item.Elem()
			}
			if item.Kind() != reflect.Struct {
				continue
			}
			row = map[string]any{}
			for i := 0; i < item.NumField(); i++ {
				field := item.Type().Field(i)
				if field.PkgPath != "" {
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")
				for _, key := range keys {
					if tag[0] == key {
						if len(tag) > 1 && tag[1] == "omitempty" && item.Field(i).IsZero() {
							break
						}
						row[key] = item.Field(i).Interface()
						break
					}
				}
			}
		}
		// Project before normalization: long source_context/captions must not
		// crowd out the real selector and read arguments of a typed entry.
		projected := discoveryReadFields(row, keys...)
		encoded, err := json.Marshal(projected)
		if err != nil || len(encoded) > 4096 {
			continue
		}
		row = nil
		if json.Unmarshal(encoded, &row) != nil || row == nil {
			continue
		}
		samples = append(samples, row)
	}
	return map[string]any{"samples": samples, "omitted_rows": rows.Len() - len(samples)}
}
