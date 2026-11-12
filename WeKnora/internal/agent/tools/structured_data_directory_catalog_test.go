package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func wideStructuredDataset(id string) map[string]any {
	sheets := []string{}
	for index := 0; index < 120; index++ {
		sheets = append(sheets, fmt.Sprintf("Printed sheet %03d %s", index, strings.Repeat("原始标题", 10)))
	}
	return map[string]any{
		"dataset_id": id, "source_file": id + ".xlsx", "sha256": strings.Repeat("a", 64),
		"imported_at": "2025-01-02T03:04:05Z", "is_latest_for_source": true,
		"sheets": sheets, "pending_sheet_states": []map[string]any{{"name": "Unimported", "status": "unsupported", "reasons": []string{strings.Repeat("Unparsed coverage detail ", 1000)}}},
	}
}

func structuredDirectoryView(t *testing.T, result *types.ToolResult, budget int) map[string]any {
	t.Helper()
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), budget)
	var view map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Output), &view))
	return view
}

func TestStructuredExcelBudgetPreservesLatestIdentitiesAndCanonicalCatalog(t *testing.T) {
	original := &types.ToolResult{Success: true, Data: map[string]any{
		"datasets":      []map[string]any{wideStructuredDataset("source-a"), wideStructuredDataset("source-b")},
		"dataset_count": 2, "total_dataset_count": 37, "offset": 0, "next_offset": nil, "datasets_truncated": false, "latest_only": true,
		"source_context":            map[string]any{"knowledge_base_id": "private-current-scope"},
		"catalog_source_types":      []string{"registered_spreadsheet_versions"},
		"catalog_excludes":          []string{"document_body", "presentation_slides", "original_figures"},
		"document_source_next_step": "Choose any further discovery using tools actually available within the authorized scope.",
	}}
	before, err := json.Marshal(original.Data)
	require.NoError(t, err)
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1500), "list_datasets", original)
	view := structuredDirectoryView(t, result, 1500)
	require.NotEqual(t, "directory_output_budget_exceeded", view["status"])
	require.Equal(t, true, view["directory_metadata_truncated"])
	require.Equal(t, false, view["output_complete"])
	require.ElementsMatch(t, []any{"document_body", "presentation_slides", "original_figures"}, view["catalog_excludes"])
	require.Equal(t, []any{"registered_spreadsheet_versions"}, view["catalog_source_types"])
	require.NotEmpty(t, view["document_source_next_step"])
	datasets := structuredRecallRows(view["datasets"])
	require.Len(t, datasets, 2, "large sheet metadata must not hide the second real source")
	for index, id := range []string{"source-a", "source-b"} {
		require.Equal(t, id, datasets[index]["dataset_id"])
		require.Equal(t, id+".xlsx", datasets[index]["source_file"])
		require.Equal(t, true, datasets[index]["is_latest_for_source"])
		require.Equal(t, strings.Repeat("a", 64), datasets[index]["sha256"])
		require.Equal(t, "2025-01-02T03:04:05Z", datasets[index]["imported_at"])
		require.Equal(t, "list_series", datasets[index]["next_operation"])
	}
	require.NotContains(t, result.Output, "source_context", "runtime scope is not model-visible")
	after, err := json.Marshal(result.Data)
	require.NoError(t, err)
	require.Equal(t, before, after, "cross-library callers must receive the successfully read canonical directory")
}

func TestStructuredExcelBudgetRetainsExactSourcePageContinuation(t *testing.T) {
	datasets := []map[string]any{}
	for index := 0; index < 8; index++ {
		datasets = append(datasets, wideStructuredDataset(fmt.Sprintf("source-%d", index)))
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{
		"datasets": datasets, "dataset_count": 40, "offset": 12, "next_offset": 20, "datasets_truncated": true, "latest_only": true,
	}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1200), "list_datasets", original)
	view := structuredDirectoryView(t, result, 1200)
	visible := structuredRecallRows(view["datasets"])
	require.NotEmpty(t, visible)
	require.Less(t, len(visible), 8)
	require.Equal(t, float64(12+len(visible)), view["next_offset"], "resume at the first omitted whole dataset")
	require.Equal(t, true, view["latest_only"])
	require.Equal(t, float64(40), view["dataset_count"])
	require.Equal(t, "source-0", visible[0]["dataset_id"])
	require.Equal(t, 20, result.Data["next_offset"], "the original service page remains available to application code")
}

func TestStructuredMultiScopeLargeExcelRemainsAvailableWithPDFContinuation(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
		identity := decodeStructuredScopeIdentity(t, request)
		kb := identity["knowledge_base_id"].(string)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		arguments := payload["arguments"].(map[string]any)
		require.Equal(t, true, arguments["latest_only"])
		require.Equal(t, float64(8), arguments["limit"])
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"datasets": []map[string]any{wideStructuredDataset(kb + "-source")}, "dataset_count": 3,
			"offset": 0, "next_offset": 1, "datasets_truncated": true, "latest_only": true,
			"pdf_tables":            []map[string]any{{"table_id": kb + "-table", "status": "reviewed", "columns": []string{strings.Repeat("Printed column ", 1000)}}},
			"pdf_table_count":       1,
			"field_candidates":      []map[string]any{{"dataset_id": kb + "-source", "series_id": "real-series", "metric": "Printed measure"}},
			"field_candidate_count": 1, "field_candidate_stage": "published_headers", "field_candidate_scope": "current_dataset_page",
		}))
	}).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 29},
	})
	result, err := base.Execute(WithOutputBudget(structuredScopeContext(), 1800), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Find the requested measurement"}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 0, result.Data["unavailable_knowledge_base_count"])
	require.Equal(t, 2, result.Data["queried_knowledge_base_count"])
	require.Equal(t, true, result.Data["datasets_truncated"])
	require.Len(t, result.Data["field_candidates"], 2)
	view := structuredDirectoryView(t, result, 1800)
	require.ElementsMatch(t, []any{"registered_spreadsheet_versions", "published_pdf_tables"}, view["catalog_source_types"])
	require.ElementsMatch(t, []any{"document_body", "presentation_slides", "original_figures"}, view["catalog_excludes"])
	require.NotEmpty(t, view["document_source_next_step"])
	catalogs := structuredRecallRows(view["knowledge_base_catalogs"])
	require.Len(t, catalogs, 2)
	for index, kb := range []string{"kb-a", "kb-b"} {
		row := catalogs[index]
		require.Equal(t, "queried", row["catalog_status"])
		datasets := structuredRecallRows(row["datasets"])
		require.Len(t, datasets, 1)
		require.Equal(t, kb+"-source", datasets[0]["dataset_id"])
		require.Equal(t, "list_series", datasets[0]["next_operation"])
		require.Equal(t, true, row["datasets_truncated"])
		require.Equal(t, json.Number("1"), row["next_offset"])
		require.Equal(t, "list_datasets", row["dataset_catalog_next_operation"])
		require.Equal(t, true, row["pdf_tables_truncated"])
		require.Equal(t, "list_pdf_tables", row["pdf_catalog_next_operation"])
	}
}

func TestStructuredDatasetBudgetKeepsBoundResultContract(t *testing.T) {
	original := &types.ToolResult{Success: true, Data: map[string]any{
		"datasets":        []map[string]any{wideStructuredDataset("bound-source")},
		"result_contract": map[string]any{"schema_version": 2, "result_id": "bound-result", "result_hash": strings.Repeat("f", 64)},
	}}
	before, err := json.Marshal(original.Data)
	require.NoError(t, err)
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1200), "list_datasets", original)
	view := structuredDirectoryView(t, result, 1200)
	require.NotContains(t, view, "result_contract")
	require.Equal(t, true, view["output_view_only"])
	reference := view["result_contract_ref"].(map[string]any)
	require.Equal(t, "bound-result", reference["result_id"])
	require.Equal(t, "bound-source", structuredRecallRows(view["datasets"])[0]["dataset_id"])
	after, err := json.Marshal(result.Data)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestStructuredDatasetBudgetKeepsAlreadyTruncatedSheetCounts(t *testing.T) {
	dataset := map[string]any{
		"dataset_id": "source-preview", "source_file": "preview.xlsx", "is_latest_for_source": true,
		"sheets": []string{"One"}, "sheet_count": 200, "sheets_truncated": true,
		"pending_sheets": []string{"Pending"}, "pending_sheets_count": 300, "pending_sheets_truncated": true,
		"pending_sheet_states": []map[string]any{{"name": "Pending", "status": "unsupported"}}, "pending_sheet_states_count": 300, "pending_sheet_states_truncated": true,
		"empty_sheets": []string{"Empty"}, "empty_sheets_count": 400, "empty_sheets_truncated": true,
		"coverage_notice": strings.Repeat("Long coverage detail ", 1000),
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"datasets": []map[string]any{dataset}}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1600), "list_datasets", original)
	view := structuredDirectoryView(t, result, 1600)
	visible := structuredRecallRows(view["datasets"])[0]
	for key, count := range map[string]string{"sheet_count": "200", "pending_sheets_count": "300", "pending_sheet_states_count": "300", "empty_sheets_count": "400"} {
		require.Equal(t, json.Number(count), visible[key], "a bounded preview length is not the complete source count")
	}
	for _, key := range []string{"sheets_truncated", "pending_sheets_truncated", "pending_sheet_states_truncated", "empty_sheets_truncated"} {
		require.Equal(t, true, visible[key])
	}
	require.NotContains(t, visible, "pending_sheet_count")
	require.NotContains(t, visible, "empty_sheet_count")
}

func TestStructuredMultiScopeForwardsCatalogFiltersAndHistoricalVersions(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
		identity := decodeStructuredScopeIdentity(t, request)
		kb := identity["knowledge_base_id"].(string)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		arguments := payload["arguments"].(map[string]any)
		require.Equal(t, map[string]any{"question": "Find the earlier source", "search": "Printed measure", "limit": float64(2), "offset": float64(9), "latest_only": false}, arguments)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"datasets": []map[string]any{{"dataset_id": kb + "-old", "is_latest_for_source": false}},
			"offset":   9, "next_offset": 10, "latest_only": false, "datasets_truncated": true,
			"field_candidates": []map[string]any{{"dataset_id": kb + "-old", "series_id": "real-field", "knowledge_base_id": "untrusted-field-metadata"}},
		}))
	}).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 29},
	})
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Find the earlier source","search":"Printed measure","limit":2,"offset":9,"latest_only":false}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	catalogs := structuredRecallRows(result.Data["knowledge_base_catalogs"])
	candidates := structuredRecallRows(result.Data["field_candidates"])
	for index, kb := range []string{"kb-a", "kb-b"} {
		datasets := structuredRecallRows(catalogs[index]["datasets"])
		require.Len(t, datasets, 1)
		require.Equal(t, kb+"-old", datasets[0]["dataset_id"])
		require.Equal(t, false, datasets[0]["is_latest_for_source"])
		require.Equal(t, json.Number("10"), catalogs[index]["next_offset"])
		require.Equal(t, kb, candidates[index]["knowledge_base_id"], "scope comes from the authorized request, never candidate metadata")
	}
}
