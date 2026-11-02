package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredPDFDirectoryFitsBudgetWithWholeEntriesAndContinuation(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 8; i++ {
		rows = append(rows, map[string]any{"table_id": string(rune('a' + i)), "status": "reviewed", "source": map[string]any{"sha256": "source-fixture"}, "columns": []string{strings.Repeat("真实列", 60)}, "sample_rows": []string{strings.Repeat("navigation only ", 200)}})
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"tables": rows, "total_table_count": 20, "offset": 8, "output_truncated": true, "next_offset": 16, "source_context": map[string]any{"knowledge_base_id": "kb-fixture"}}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1900), "list_pdf_tables", original)
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 1900)
	require.True(t, json.Valid([]byte(result.Output)))
	tables := structuredRecallRows(result.Data["tables"])
	require.NotEmpty(t, tables)
	require.Less(t, len(tables), 8)
	require.Equal(t, 8+len(tables), result.Data["next_offset"])
	require.Equal(t, true, result.Data["output_truncated"])
	require.Equal(t, "a", tables[0]["table_id"])
	require.Contains(t, result.Output, "真实列")
	require.Len(t, rows, 8, "rendering must not modify the unprojected result")
	require.Contains(t, rows[0], "sample_rows")
}

func TestStructuredOversizedSinglePDFRemainsReadableByIdentity(t *testing.T) {
	original := &types.ToolResult{Success: true, Data: map[string]any{"tables": []map[string]any{{"table_id": "pdft-fixture", "status": "reviewed", "publication_status": "published", "columns": []string{strings.Repeat("wide ", 2000)}}}, "offset": 0, "total_table_count": 1, "output_truncated": false, "next_offset": nil}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 700), "list_pdf_tables", original)
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 700)
	tables := structuredRecallRows(result.Data["tables"])
	require.Equal(t, "pdft-fixture", tables[0]["table_id"])
	require.Equal(t, "read_pdf_table", tables[0]["next_operation"])
	require.Equal(t, true, tables[0]["directory_metadata_truncated"])
	require.True(t, json.Valid([]byte(result.Output)))
}

func TestStructuredMultiScopePDFDirectoryBudgetNeverMeansAbsent(t *testing.T) {
	catalogs := []map[string]any{}
	for _, kb := range []string{"kb-a", "kb-b"} {
		catalogs = append(catalogs, map[string]any{"knowledge_base_id": kb, "pdf_tables": []map[string]any{{"table_id": kb + "-table", "columns": []string{strings.Repeat("label", 500)}}}, "pdf_table_count": 1, "pdf_tables_truncated": false})
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"knowledge_base_catalogs": catalogs, "pdf_tables_truncated": false}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 900), "list_datasets", original)
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 900)
	require.True(t, json.Valid([]byte(result.Output)))
	for _, catalog := range structuredRecallRows(result.Data["knowledge_base_catalogs"]) {
		require.Equal(t, float64(1), catalog["pdf_table_count"])
		require.Equal(t, true, catalog["pdf_tables_truncated"])
		require.Equal(t, "list_pdf_tables", catalog["pdf_catalog_next_operation"])
	}
	require.Len(t, catalogs[0]["pdf_tables"], 1)
	// Numerical evidence is never modified by this discovery-only renderer.
	require.Same(t, original, fitStructuredDirectory(WithOutputBudget(context.Background(), 10), "query_pdf_table", original))
}

func TestStructuredPDFReadBudgetKeepsSourceRowsAndExactContinuation(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 8; i++ {
		rows = append(rows, map[string]any{"_row_id": string(rune('a' + i)), "raw_values": map[string]any{"label": strings.Repeat("长标签", 50), "value": "17"}, "source_cells": map[string]any{"value": []map[string]any{{"page": 3, "row": i + 10, "column": 1, "source_sha256": "source-fixture"}}}})
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"table_id": "pdft-fixture", "rows": rows, "offset": 10, "total_row_count": 33, "output_truncated": true, "plan_sha256": "plan-fixture", "record_sha256": "record-fixture", "plan": strings.Repeat("review coordinate ", 200), "coverage": map[string]any{"table_input_complete": false, "unresolved_quality_issues": []any{}, "quality_issues": []map[string]any{{"code": "original_fixture", "message": strings.Repeat("diagnostic ", 400)}}}}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 1800), "read_pdf_table", original)
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 1800)
	require.True(t, json.Valid([]byte(result.Output)))
	visible := structuredRecallRows(result.Data["rows"])
	require.NotEmpty(t, visible)
	require.Less(t, len(visible), 8)
	require.Equal(t, 10+len(visible), result.Data["next_offset"])
	require.Equal(t, len(visible), result.Data["returned_row_count"])
	require.Equal(t, "record-fixture", result.Data["record_sha256"])
	require.Contains(t, result.Output, "source_cells")
	require.Contains(t, result.Output, "unresolved_quality_issues")
	require.Equal(t, false, result.Data["coverage"].(map[string]any)["table_input_complete"])
	require.Contains(t, original.Data, "plan")
	require.Equal(t, "a", visible[0]["_row_id"])
}

func TestStructuredPDFReadBudgetCanExpandWithoutLosingUnseenRows(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 8; i++ {
		rows = append(rows, map[string]any{"_row_id": string(rune('a' + i)), "raw_values": map[string]any{"label": strings.Repeat("field", 100)}})
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"table_id": "pdft-fixture", "rows": rows, "offset": 0, "total_row_count": 8, "output_truncated": false}}
	small := fitStructuredDirectory(WithOutputBudget(context.Background(), 2000), "read_pdf_table", original)
	require.Less(t, len(structuredRecallRows(small.Data["rows"])), 8)
	require.Equal(t, true, small.Data["output_truncated"])
	large := fitStructuredDirectory(WithOutputBudget(context.Background(), 12000), "read_pdf_table", original)
	require.Len(t, structuredRecallRows(large.Data["rows"]), 8)
	require.Equal(t, false, large.Data["output_truncated"])
	require.Equal(t, 8, len(structuredRecallRows(original.Data["rows"])))
	require.True(t, json.Valid([]byte(small.Output)))
	require.True(t, json.Valid([]byte(large.Output)))
}

func TestStructuredSinglePDFStubDoesNotSkipDirectoryEntries(t *testing.T) {
	rows := []map[string]any{}
	for i := 0; i < 8; i++ {
		rows = append(rows, map[string]any{"table_id": string(rune('a' + i)), "status": "reviewed", "columns": []string{strings.Repeat("oversized", 2000)}})
	}
	original := &types.ToolResult{Success: true, Data: map[string]any{"tables": rows, "offset": 8, "total_table_count": 20, "output_truncated": true, "next_offset": 16}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 700), "list_pdf_tables", original)
	require.Len(t, structuredRecallRows(result.Data["tables"]), 1)
	require.Equal(t, 9, result.Data["next_offset"])
	require.Equal(t, true, result.Data["output_truncated"])
	require.True(t, json.Valid([]byte(result.Output)))
}

func TestStructuredPDFBudgetKeepsAuthenticatedContextOutOfModelOutput(t *testing.T) {
	original := &types.ToolResult{Success: true, Output: `{"tables":[{"table_id":"pdft-fixture","columns":["` + strings.Repeat("wide", 2000) + `"]}],"offset":0,"total_table_count":1}`, Data: map[string]any{"tables": []map[string]any{{"table_id": "pdft-fixture", "columns": []string{strings.Repeat("wide", 2000)}}}, "offset": 0, "total_table_count": 1, "source_context": map[string]any{"knowledge_base_id": "authorized-library", "knowledge_base_tenant_id": uint64(7)}}}
	result := fitStructuredDirectory(WithOutputBudget(context.Background(), 700), "list_pdf_tables", original)
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 700)
	require.NotContains(t, result.Output, "source_context")
	require.NotContains(t, result.Output, "authorized-library")
	require.Equal(t, original.Data["source_context"], result.Data["source_context"])
	require.True(t, json.Valid([]byte(result.Output)))
}
