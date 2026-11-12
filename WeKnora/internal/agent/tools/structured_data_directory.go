package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

// fitStructuredDirectory preserves JSON records and explicit continuation
// instead of letting the registry cut a source identity in the middle. Only
// directories and complete result-row prefixes are projected; cell values and
// source references are never shortened or rewritten.
func fitStructuredDirectory(ctx context.Context, operation string, result *types.ToolResult) *types.ToolResult {
	if result == nil || !result.Success || result.Data == nil || (operation != "list_datasets" && operation != "list_pdf_tables" && operation != "read_pdf_table" && operation != "query_pdf_table") {
		return result
	}
	budget := OutputBudget(ctx)
	if result.Output != "" && utf8.RuneCountInString(result.Output) <= budget {
		return result
	}
	encoded, err := json.Marshal(result.Data)
	if err != nil {
		return result
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil {
		return result
	}
	_, contractBound := result.Data["result_contract"]
	var contractReference map[string]any
	if contractBound {
		// A v2 contract binds the complete executed result. Only the model view
		// may be projected; do not change/re-sign trusted rows or coverage.
		reference := map[string]any{"projection": true}
		if contract, ok := data["result_contract"].(map[string]any); ok {
			for _, key := range []string{"schema_version", "result_id", "result_hash"} {
				if value, exists := contract[key]; exists {
					reference[key] = value
				}
			}
		}
		delete(data, "result_contract")
		data["result_contract_ref"] = reference
		data["output_view_only"] = true
		contractReference = reference
	}
	// The authenticated context is attached to Data for application use, not
	// injected into the model-visible service response. Preserve that contract
	// both when projecting and when no projection is needed.
	var originalOutput map[string]any
	_ = json.Unmarshal([]byte(result.Output), &originalOutput)
	_, contextWasVisible := originalOutput["source_context"]
	if !contextWasVisible {
		delete(data, "source_context")
	}
	render := func() bool {
		if contractBound {
			delete(data, "result_contract")
			data["result_contract_ref"], data["output_view_only"] = contractReference, true
		}
		encoded, err = json.Marshal(data)
		return err == nil && utf8.RuneCount(encoded) <= budget
	}
	if render() {
		copy := *result
		copy.Output = string(encoded)
		return &copy
	}
	if operation == "query_pdf_table" {
		projectPDFHistoricReviewMetadata(data)
		data["metadata_projection"] = "Historic issue detail copies omitted; original SQL, loaded input scope, hashes, current unresolved issues and each retained row/provenance are unchanged."
		rows, _ := data["rows"].([]any)
		originalRowCount := len(rows)
		provenance, _ := data["row_provenance"].([]any)
		if len(provenance) != len(rows) {
			data["row_provenance_complete"] = false
		}
		data["row_provenance"] = provenance[:min(len(provenance), len(rows))]
		data["executed_returned_row_count"] = len(rows)
		for !render() && len(rows) > 0 {
			rows = rows[:len(rows)-1]
			data["rows"], data["output_truncated"], data["output_complete"] = rows, true, false
			data["returned_row_count"] = len(rows)
			data["row_provenance"] = provenance[:min(len(provenance), len(rows))]
			data["output_projection_notice"] = "Only a complete prefix of executed result rows fits this output budget. Reduce output limit/selected columns, or issue an explicit SQL selection/aggregation for the required scope. No SQL continuation offset is implied; computation input was not reduced."
		}
		if len(rows) == 0 && (originalRowCount > 0 || !render()) {
			data = map[string]any{"status": "query_output_budget_exceeded", "table_id": result.Data["table_id"], "output_truncated": true, "output_complete": false,
				"next_step": "No complete query row fits this output budget. Reduce output limit or selected real columns, or aggregate; the query input and original SQL must not be silently changed."}
			for _, key := range []string{"source", "provenance", "sql", "input_count", "input_complete"} {
				if value, exists := result.Data[key]; exists {
					data[key] = value
				}
			}
			if sql, ok := result.Data["sql"].(string); ok {
				data["executed_sql_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
			}
			if !render() {
				delete(data, "next_step")
			}
			if !render() {
				delete(data, "sql")
				data["sql_omitted_for_output_budget"] = true
			}
		}
	} else if operation == "read_pdf_table" {
		// The immutable plan and historic issue coordinates are already
		// represented by hashes/current coverage; repeating them can consume
		// the whole budget before the first actual source cell is visible.
		delete(data, "plan")
		projectPDFHistoricReviewMetadata(data)
		data["metadata_projection"] = "Plan and historic issue details omitted to preserve complete source rows; hashes and current unresolved/coverage states retained."
		rows := structuredRecallRows(data["rows"])
		data["rows"] = rows
		offset := structuredDirectoryOffset(data["offset"])
		data["returned_row_count"] = len(rows)
		if truncated, _ := data["output_truncated"].(bool); truncated {
			data["next_offset"] = int(offset) + len(rows)
		}
		for !render() && len(rows) > 0 {
			rows = rows[:len(rows)-1]
			data["rows"], data["output_truncated"], data["next_offset"] = rows, true, int(offset)+len(rows)
			data["returned_row_count"] = len(rows)
		}
		if len(rows) == 0 {
			data = map[string]any{"status": "source_row_output_budget_exceeded", "output_truncated": true, "table_id": result.Data["table_id"], "offset": result.Data["offset"], "next_step": "No source row fits this output budget. Use query_pdf_table to select the needed real columns and _row_id, or increase the tool output budget; no absence or complete input is established."}
		}
	} else if operation == "list_pdf_tables" {
		rows := structuredRecallRows(data["tables"])
		// Navigation samples are optional, unlike table identity and current state.
		for _, row := range rows {
			delete(row, "sample_rows")
			row["sample_rows_truncated"] = true
		}
		data["tables"] = rows
		offset := structuredDirectoryOffset(data["offset"])
		for !render() && len(rows) > 1 {
			rows = rows[:len(rows)-1]
			data["tables"], data["output_truncated"], data["next_offset"] = rows, true, int(offset)+len(rows)
		}
		if !render() && len(rows) == 1 {
			// A single unusually wide table still has a recoverable navigation entry.
			original := rows[0]
			stub := map[string]any{"table_id": original["table_id"], "status": original["status"], "publication_status": original["publication_status"], "directory_metadata_truncated": true, "next_operation": "read_pdf_table"}
			data["tables"] = []map[string]any{stub}
			if more, _ := data["output_truncated"].(bool); more {
				data["next_offset"] = int(offset) + 1
			}
			delete(data, "directory_notice")
		}
	} else {
		projectStructuredDatasetDirectory(data, render)
	}
	if !render() && operation == "query_pdf_table" {
		// Even pathological metadata must remain an explicit query failure,
		// with actual source/execution identity instead of a directory retry.
		data = map[string]any{"status": "query_output_budget_exceeded", "table_id": result.Data["table_id"], "output_truncated": true, "output_complete": false}
		if sql, ok := result.Data["sql"].(string); ok {
			data["executed_sql_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
		}
		if source, ok := result.Data["source"].(map[string]any); ok {
			if hash, exists := source["sha256"]; exists {
				data["source_sha256"] = hash
			}
		}
		data["next_step"] = "Reduce query output columns/rows or aggregate; no complete row fits this budget."
		if !render() {
			delete(data, "next_step")
		}
	}
	if !render() && operation != "query_pdf_table" {
		data = map[string]any{"status": "directory_output_budget_exceeded", "output_truncated": true,
			"next_step": "Use an authorized knowledge_base_id and list_pdf_tables with limit:1; absence has not been established."}
		if operation == "list_datasets" {
			data["next_operation"] = "list_datasets"
			data["next_step"] = "Select an authorized knowledge_base_id and request list_datasets with limit:1; Excel fields use list_series. Directory omission does not establish absence."
		}
		if scope, ok := result.Data["source_context"]; ok && contextWasVisible {
			data["source_context"] = scope
		}
		if !render() {
			data = map[string]any{"status": "directory_output_budget_exceeded", "output_truncated": true}
			render()
		}
	}
	copy := *result
	if scope, ok := result.Data["source_context"]; ok && !contextWasVisible {
		data["source_context"] = scope
	}
	copy.Output, copy.Data = string(encoded), data
	if contractBound || operation == "list_datasets" {
		// Directory output projection must not replace the successfully read
		// canonical catalog used by cross-library recall and application code.
		copy.Data = result.Data
	}
	if data["status"] == "query_output_budget_exceeded" {
		copy.Success = false
		copy.Error = "PDF query output exceeds tool budget; request fewer output columns/rows or an explicit aggregation."
	}
	return &copy
}

func structuredDirectoryOffset(value any) int {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		if err == nil && parsed >= 0 && int64(int(parsed)) == parsed {
			return int(parsed)
		}
	case float64:
		if number >= 0 && float64(int(number)) == number {
			return int(number)
		}
	case int:
		if number >= 0 {
			return number
		}
	}
	return 0
}

func projectPDFHistoricReviewMetadata(data map[string]any) {
	if coverage, ok := data["coverage"].(map[string]any); ok {
		if issues := structuredRecallRows(coverage["quality_issues"]); len(issues) > 0 {
			codes := []any{}
			seen := map[string]bool{}
			for _, issue := range issues {
				code, _ := issue["code"].(string)
				if !seen[code] {
					codes = append(codes, code)
					seen[code] = true
				}
			}
			coverage["original_quality_issue_count"], coverage["original_quality_issue_codes"] = len(issues), codes
			delete(coverage, "quality_issues")
		}
	}
}
