package tools

// Coverage describes this operation's catalog families, not the existence of
// documents or the availability of any particular document tool in a turn.
func structuredCatalogCoverage(data map[string]any) {
	data["catalog_excludes"] = []string{"document_body", "presentation_slides", "original_figures"}
	data["document_source_next_step"] = "目录未命中不代表全库缺失。是否进一步检索/读取文档，由模型按完整问题及本轮实际可用的已授权能力决定。"
}

// projectStructuredDatasetDirectory changes only the model's discovery view.
// Dataset IDs, version identities and source cells are never shortened, and
// omitted navigation is separate from both transport failure and empty data.
func projectStructuredDatasetDirectory(data map[string]any, fits func() bool) {
	structuredCatalogCoverage(data)
	data["output_truncated"], data["output_complete"] = true, false
	data["directory_metadata_truncated"] = true
	data["directory_notice"] = "Directory metadata is omitted for the output budget, not evidence of absent data. Select a real dataset_id, then list_series for fields. Follow each scope's next_offset with list_datasets; PDF continuation uses list_pdf_tables."
	for _, key := range []string{"scope_notice", "literal_field_notice", "field_candidate_notice", "pdf_notice", "next_step"} {
		delete(data, key)
	}
	catalogs := structuredRecallRows(data["knowledge_base_catalogs"])
	rows := catalogs
	if len(catalogs) == 0 {
		rows = []map[string]any{data}
	} else {
		data["knowledge_base_catalogs"] = catalogs
		// The aggregate catalog already carries each read scope's identity and
		// state; duplicated authorization descriptions need not crowd it out.
		if navigation := structuredRecallRows(data["knowledge_bases"]); len(navigation) > 0 {
			for index, row := range navigation {
				navigation[index] = structuredDirectoryKeys(row, "knowledge_base_id", "name")
			}
			data["knowledge_bases"] = navigation
		}
	}
	for _, row := range rows {
		delete(row, "pdf_notice")
		datasets := structuredRecallRows(row["datasets"])
		if datasets != nil {
			for index, dataset := range datasets {
				datasets[index] = compactStructuredDataset(dataset, false)
			}
			row["datasets"] = datasets
		}
		pdfs := structuredRecallRows(row["pdf_tables"])
		if pdfs != nil {
			for _, table := range pdfs {
				if _, exists := table["sample_rows"]; exists {
					delete(table, "sample_rows")
					table["sample_rows_truncated"] = true
				}
			}
			row["pdf_tables"] = pdfs
		}
	}
	if fits() {
		return
	}
	// Retain every dataset identity before reducing the number of datasets.
	// Wide sheet inventories can be inspected on demand with list_series.
	for _, row := range rows {
		if datasets := structuredRecallRows(row["datasets"]); datasets != nil {
			for index, dataset := range datasets {
				datasets[index] = compactStructuredDataset(dataset, true)
			}
			row["datasets"] = datasets
		}
	}
	if fits() {
		return
	}
	// Field candidates are discovery evidence, never a decision. Omit whole
	// candidates with an explicit flag rather than cut real IDs or headers.
	for _, key := range []string{"literal_fields", "field_candidates"} {
		fields := structuredRecallRows(data[key])
		for len(fields) > 0 && !fits() {
			fields = fields[:len(fields)-1]
			data[key], data[key+"_truncated"] = fields, true
			if key == "literal_fields" {
				data["literal_field_count"] = len(fields)
			} else {
				data["field_candidate_returned_count"] = len(fields)
			}
		}
	}
	if fits() {
		return
	}
	// PDFs and Excel retain independent continuation. Trimming one family
	// must not erase the other or direct Excel-only users to a PDF operation.
	for _, row := range rows {
		if pdfs := structuredRecallRows(row["pdf_tables"]); len(pdfs) > 0 {
			for index, table := range pdfs {
				stub := structuredDirectoryKeys(table, "table_id", "status", "publication_status", "extraction_verified")
				stub["directory_metadata_truncated"], stub["next_operation"] = true, "read_pdf_table"
				pdfs[index] = stub
			}
			row["pdf_tables"] = pdfs
			row["pdf_tables_truncated"], row["pdf_catalog_next_operation"] = true, "list_pdf_tables"
			data["pdf_tables_truncated"] = true
		}
	}
	for !fits() {
		changed := false
		for _, row := range rows {
			pdfs := structuredRecallRows(row["pdf_tables"])
			if len(pdfs) > 0 && !fits() {
				row["pdf_tables"] = pdfs[:len(pdfs)-1]
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// Keep a complete prefix in each scope, so offsets retain the service's
	// ordering and latest_only filter. Do not fabricate a new search ranking.
	for minimum := 1; minimum >= 0 && !fits(); minimum-- {
		for !fits() {
			changed := false
			for _, row := range rows {
				datasets := structuredRecallRows(row["datasets"])
				if len(datasets) <= minimum || fits() {
					continue
				}
				if _, present := row["dataset_count"]; !present {
					row["dataset_count"] = len(datasets)
				}
				if _, present := row["latest_only"]; !present {
					// Legacy responses contained all versions; continuing them
					// with a newly defaulted latest filter would skip entries.
					row["latest_only"] = false
				}
				datasets = datasets[:len(datasets)-1]
				row["datasets"], row["datasets_truncated"] = datasets, true
				row["next_offset"] = structuredDirectoryOffset(row["offset"]) + len(datasets)
				row["dataset_catalog_next_operation"] = "list_datasets"
				changed = true
			}
			if !changed {
				break
			}
		}
		if minimum == 1 && !fits() {
			// Drop redundant prose/navigation before the last real dataset.
			for _, row := range catalogs {
				keep := structuredDirectoryKeys(row, "knowledge_base_id", "name", "catalog_status", "code", "datasets", "dataset_count", "total_dataset_count", "offset", "next_offset", "latest_only", "datasets_truncated", "dataset_catalog_next_operation", "pdf_tables", "pdf_table_count", "pdf_tables_truncated", "pdf_catalog_next_operation", "excel_catalog_status", "pdf_catalog_status")
				for key := range row {
					delete(row, key)
				}
				for key, value := range keep {
					row[key] = value
				}
			}
			keep := structuredDirectoryKeys(data, "kind", "status", "code", "reason_code", "stage", "interruption_code", "execution_started", "source_data_read", "catalog_data_read", "catalog_complete", "catalog_completion_scope", "catalog_source_types", "catalog_excludes", "document_source_next_step", "dataset_catalog_status", "knowledge_base_catalogs", "datasets", "dataset_count", "total_dataset_count", "offset", "next_offset", "latest_only", "datasets_truncated", "dataset_catalog_next_operation", "pdf_tables", "pdf_table_count", "pdf_tables_truncated", "pdf_catalog_next_operation", "source_context", "result_contract_ref", "output_view_only", "_structured_execution", "manifest_version", "schema_sha256", "protocol_version", "generation")
			for key := range data {
				delete(data, key)
			}
			for key, value := range keep {
				data[key] = value
			}
			if len(catalogs) > 0 {
				// The status and each scope's catalog state already identify this
				// view. Remove duplicate navigation labels before a last source;
				// distinct failure codes and stages remain diagnostic evidence.
				delete(data, "kind")
				code, _ := data["code"].(string)
				if reason, _ := data["reason_code"].(string); code != "" && reason == code {
					delete(data, "reason_code")
				}
				if status, _ := data["status"].(string); code != "" && status == code {
					delete(data, "code")
				}
				if complete, _ := data["catalog_complete"].(bool); complete {
					// Every queried scope retains its own family status,
					// truncation flags and continuation. Root summaries merely
					// repeat those states once all scope requests completed.
					delete(data, "dataset_catalog_status")
					delete(data, "datasets_truncated")
					delete(data, "pdf_tables_truncated")
				}
			}
			data["directory_metadata_truncated"], data["output_truncated"], data["output_complete"] = true, true, false
			if !fits() {
				for _, row := range rows {
					datasets := structuredRecallRows(row["datasets"])
					if datasets == nil {
						continue
					}
					for index, dataset := range datasets {
						// Counts and review previews can wait for list_series;
						// preserve exact source/version navigation before ever
						// dropping the last dataset in a scope.
						datasets[index] = structuredDirectoryKeys(dataset, "dataset_id", "source_file", "sha256", "imported_at", "is_latest_for_source", "import_id", "parent_dataset_id")
						datasets[index]["next_operation"] = "list_series"
					}
					row["datasets"] = datasets
				}
			}
		}
	}
}

func structuredDirectoryKeys(source map[string]any, keys ...string) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		if value, exists := source[key]; exists {
			result[key] = value
		}
	}
	return result
}

func compactStructuredDataset(source map[string]any, identityOnly bool) map[string]any {
	result := structuredDirectoryKeys(source, "dataset_id", "source_file", "sha256", "imported_at", "is_latest_for_source", "import_id", "parent_dataset_id", "use_reviewed_mappings", "series_count", "sheet_count", "sheets_truncated", "pending_sheets_count", "pending_sheets_truncated", "pending_sheet_states_count", "pending_sheet_states_truncated", "empty_sheets_count", "empty_sheets_truncated")
	result["directory_metadata_truncated"], result["next_operation"] = true, "list_series"
	if identityOnly {
		return result
	}
	for _, key := range []string{"sheets", "pending_sheets", "pending_sheet_states", "empty_sheets"} {
		values, exists := source[key].([]any)
		if !exists {
			continue
		}
		countKey := map[string]string{"sheets": "sheet_count", "pending_sheets": "pending_sheets_count", "pending_sheet_states": "pending_sheet_states_count", "empty_sheets": "empty_sheets_count"}[key]
		if _, present := result[countKey]; !present {
			result[countKey] = len(values)
		}
		result[key] = values[:min(len(values), 8)]
		if len(values) > 8 {
			result[key+"_truncated"] = true
		}
	}
	return result
}
