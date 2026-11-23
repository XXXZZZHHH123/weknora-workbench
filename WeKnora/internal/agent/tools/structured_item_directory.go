package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const briefFieldDescriptionRunes = 320

// Catalog identities must survive before any individual item's verbose detail.
// Only Output is projected: canonical source records, permissions and their
// pagination remain available to application code and source validation.
func fitStructuredItemDirectory(ctx context.Context, operation string, result *types.ToolResult) *types.ToolResult {
	if result == nil || !result.Success || result.Data == nil {
		return result
	}
	budget := OutputBudget(ctx)
	if result.Output != "" && json.Valid([]byte(result.Output)) && utf8.RuneCountInString(result.Output) <= budget {
		return result
	}
	encoded, err := json.Marshal(result.Data)
	if err != nil {
		return result
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return result
	}
	key := "series"
	fields := []string{"series_id", "metric", "sheet", "unit", "unit_status", "date_mode", "dimensions", "region_name", "status", "sheet_status", "first_date", "last_date", "first_valid_date", "last_valid_date", "record_count", "valid_count", "chart_id", "chart_sheet", "source_series_id", "source_metric", "header_cells", "block_index"}
	if operation == "list_charts" {
		key = "charts"
		fields = []string{"chart_id", "title", "sheet", "part", "anchor_row", "status", "status_counts", "axis_titles", "axes"}
	}
	original := structuredRecallRows(source[key])
	offset := structuredDirectoryOffset(source["offset"])
	rows := make([]map[string]any, 0, len(original))
	for index, item := range original {
		brief := map[string]any{"catalog_offset": offset + index}
		for _, field := range fields {
			if value, exists := item[field]; exists {
				brief[field] = value
			}
		}
		if metadata, ok := item["field_metadata"].(map[string]any); ok && operation == "list_series" {
			// Declared units remain valid metadata; keep their origin visible
			// even when full source-evidence details do not fit this view.
			brief["field_metadata"] = structuredDirectoryKeys(metadata, "unit_source")
		}
		if operation == "list_charts" {
			brief["curve_summary_complete"] = false
			if curves, ok := item["curves"].([]any); ok {
				brief["curve_count"] = len(curves)
				summary := make([]map[string]any, 0, len(curves))
				complete := true
				for _, value := range curves {
					curve, valid := value.(map[string]any)
					complete = complete && valid
					entry := map[string]any{}
					// Keep every returned series, including repeated labels and
					// unknown units. These are graph metadata, never data points.
					for _, field := range []string{"label", "chart_type", "axis", "unit"} {
						if value, exists := curve[field]; exists {
							entry[field] = value
						}
					}
					summary = append(summary, entry)
				}
				brief["curve_summary"], brief["curve_summary_complete"] = summary, complete
			}
		}
		rows = append(rows, brief)
	}
	view := map[string]any{}
	for _, field := range []string{"dataset_id", "status", "total", "offset", "limit", "truncated", "next_offset", "match_mode", "pending_scope", "sheet_status"} {
		if value, exists := source[field]; exists {
			view[field] = value
		}
	}
	var unmapped []map[string]any
	var reviewSummary map[string]any
	if summary, ok := source["catalog_summary"].(map[string]any); ok && operation == "list_series" {
		if _, exists := summary["unmapped_sheets"]; exists {
			reviewSummary = map[string]any{}
			copyStructuredUnmappedSheets(summary, reviewSummary, 8)
			unmapped = structuredRecallRows(reviewSummary["unmapped_sheets"])
			view["catalog_summary"] = reviewSummary
		}
	}
	// source_context is runtime-private unless the service already exposed it.
	if output := structuredOperationOutputObject(result.Output); output != nil && output["source_context"] != nil {
		view["source_context"] = output["source_context"]
	}
	projection := map[string]any{
		"mode": "brief", "details_omitted": true, "executed_returned_count": len(original),
		"source_pagination": map[string]any{"offset": source["offset"], "next_offset": source["next_offset"], "truncated": source["truncated"]},
	}
	view["directory_projection"] = projection
	view["source_data_read"], view["output_truncated"] = false, true
	view["next_operation"] = operation
	view["next_step"] = "Brief entries preserve source identities, labels and coverage; item details and directory summaries are omitted. For complete metadata repeat this operation with the same dataset and sheet/search filters, offset=catalog_offset, limit=1. If that page is still brief, use the actual sheet/header cells with inspect_source or the real series_id with query_series; do not repeat unchanged pages. Omission is not absence and directory metadata is not an observation read."
	if operation == "list_charts" {
		projection["curve_summary_scope"] = "All returned curve metadata for each displayed chart, not observations or verified units. A source-cell selection may cover fewer series than this chart catalog."
	}
	render := func() bool {
		view[key] = rows
		projection["shown_entry_count"], projection["omitted_entry_count"] = len(rows), len(original)-len(rows)
		if len(rows) < len(original) {
			view["truncated"], view["next_offset"] = true, offset+len(rows)
		}
		encoded, err = json.Marshal(view)
		return err == nil && utf8.RuneCount(encoded) <= budget
	}
	trimReviewSheet := func() {
		unmapped = unmapped[:len(unmapped)-1]
		reviewSummary["unmapped_sheets"], reviewSummary["unmapped_sheets_truncated"] = unmapped, true
	}
	for !render() && len(unmapped) > 1 {
		trimReviewSheet()
	}
	for !render() && len(rows) > 1 {
		rows = rows[:len(rows)-1]
	}
	for !render() && len(unmapped) > 0 {
		trimReviewSheet()
	}
	for !render() && len(rows) > 0 {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 && (len(original) > 0 || !render()) {
		// A pathological label cannot be cut into a different source identity.
		// Keep the cursor unchanged and explicitly report a display limit.
		view = map[string]any{"status": "directory_output_budget_exceeded", "output_truncated": true, "source_data_read": false,
			"offset": offset, "next_offset": offset, "truncated": true, "next_operation": operation,
			"next_step": "No complete directory entry fits. Narrow the actual sheet/search filters or request a smaller page; absence is not established."}
		if value, exists := source["dataset_id"]; exists {
			view["dataset_id"] = value
		}
		if value, exists := source["total"]; exists {
			view["total"] = value
		}
		if !render() {
			delete(view, "next_step")
		}
		if !render() {
			view = map[string]any{"status": "directory_output_budget_exceeded", "output_truncated": true}
			render()
		}
		if utf8.RuneCount(encoded) > budget {
			encoded = []byte("{}")
			if budget < 2 {
				encoded = []byte("0")
			}
		}
	} else if operation == "list_series" {
		// Preserve complete rows and exact header locations first. Include
		// short definitions when they fit without dropping another row.
		for index, row := range rows {
			description, ok := original[index]["description"].(string)
			if !ok || utf8.RuneCountInString(description) > briefFieldDescriptionRunes {
				continue
			}
			row["description"] = description
			if !render() {
				delete(row, "description")
			}
		}
		render()
	}
	copy := *result
	copy.Output = string(encoded)
	return &copy
}
