package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func pdfEvidenceSteps(operation string, data map[string]any) []types.AgentStep {
	return []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "pdf-call", Name: "structured_data", Args: map[string]any{"operation": operation}, Result: &types.ToolResult{Success: true, Data: data, Output: "abbreviated model view"}}}}}
}

func pdfQueryEvidence() map[string]any {
	return map[string]any{
		"source_kind": "pdf_table", "kind": "structured_analysis", "status": "ok",
		"table_id": "pdft_ab12cd34", "draft_id": "pdfd_ef56ab78",
		"source":  map[string]any{"file_name": "measurements2028.pdf", "sha256": "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"},
		"columns": []any{"quantity", "ratio"}, "rows": []any{[]any{"-12.500000000000000000", "3.125000000000000000"}},
		"input_count": 3, "returned_row_count": 1,
		"coverage": map[string]any{"logical_row_count": 3, "selected_fragment_count": 2, "table_input_complete": true},
		"plan":     map[string]any{"header_rows": []any{920}, "limit": 9999},
		"sql":      "SELECT 9999", "bbox": []any{0.87, 0.23, 0.99, 0.98},
		"row_provenance": []any{map[string]any{"source_rows": []any{map[string]any{"row": 72, "column": 84, "values": map[string]any{"quantity": "888"}, "text": "777", "bbox": []any{0.67, 0.68}}}}},
	}
}

func TestPDFNumbersUseOnlySQLResultsAndExplicitCounts(t *testing.T) {
	data := pdfQueryEvidence()
	steps := pdfEvidenceSteps("query_pdf_table", data)
	require.Empty(t, ungroundedStructuredNumbers("结果为-12.50、3.125，共3行，来自measurements2028.pdf。", nil, steps))
	for _, number := range []string{"0.87", "0.23", "72", "84", "920", "9999", "888", "777", "2028"} {
		require.Contains(t, ungroundedStructuredNumbers("统计值为"+number+"。", nil, steps), number)
	}
	require.Empty(t, ungroundedStructuredNumbers("PDF表pdft_ab12cd34的结果为-12.5。", nil, steps))
	named := steps[0].ToolCalls[0]
	named.Name = "structured_query_pdf_table"
	named.Args = nil
	require.Empty(t, ungroundedStructuredNumbers("结果为-12.5。", nil, []types.AgentStep{{ToolCalls: []types.ToolCall{named}}}))
}

func TestPDFReadNumbersExcludeSourceSpansRawValuesAndEmbeddedJSON(t *testing.T) {
	data := pdfQueryEvidence()
	data["kind"] = "structured_source_inspection"
	data["columns"] = []any{map[string]any{"name": "Measure2027", "source_columns": map[string]any{"table-1": 43}}}
	data["rows"] = []any{map[string]any{
		"_row_id": "pdfr_ab12cd34", "values": map[string]any{"Measure2027": "-2.25", "note": `{"value":666}`},
		"raw_values":   map[string]any{"Measure2027": "918"},
		"source_rows":  []any{map[string]any{"row": 66}},
		"source_cells": map[string]any{"Measure2027": []any{map[string]any{"text": "917", "page": 71, "row": 66, "column": 43, "bbox": []any{0.86, 0.85}}}},
	}}
	steps := pdfEvidenceSteps("read_pdf_table", data)
	require.Empty(t, ungroundedStructuredNumbers("Measure2027的值为-2.25。", nil, steps))
	for _, number := range []string{"666", "918", "917", "71", "66", "43", "0.86", "2027"} {
		require.Contains(t, ungroundedStructuredNumbers("统计值为"+number+"。", nil, steps), number)
	}
}

func TestPDFCanonicalRuntimeResultsOverrideDisplayAndFailedCalls(t *testing.T) {
	data := pdfQueryEvidence()
	steps := pdfEvidenceSteps("query_pdf_table", data)
	forged := pdfQueryEvidence()
	forged["rows"] = []any{[]any{"7788"}}
	encoded, err := json.Marshal(forged)
	require.NoError(t, err)
	messages := []chat.Message{{Role: "tool", ToolCallID: "pdf-call", Name: "structured_data", Content: string(encoded)}}
	require.Contains(t, ungroundedStructuredNumbers("结果为7788。", messages, steps), "7788")
	require.Empty(t, ungroundedStructuredNumbers("结果为-12.5。", messages, steps))
	steps[0].ToolCalls[0].Result.Success = false
	messages = append(messages, chat.Message{Role: "tool", Content: `{"value":11}`})
	require.Contains(t, ungroundedStructuredNumbers("结果为-12.5。", messages, steps), "-12.5")
}

func TestPDFCatalogueMetadataDoesNotAuthorizeStatistics(t *testing.T) {
	for _, data := range []map[string]any{
		{"source_kind": "pdf_table", "kind": "structured_pdf_tables", "status": "ok", "tables": []any{map[string]any{"columns": []any{map[string]any{"source_columns": map[string]any{"table-1": 74}}}, "coverage": map[string]any{"logical_row_count": 822}}}},
		{"status": "ok", "datasets": []any{}, "pdf_tables": []any{map[string]any{"plan": map[string]any{"header_rows": []any{74}}, "coverage": map[string]any{"logical_row_count": 822}}}},
	} {
		for _, number := range []string{"74", "822"} {
			require.Contains(t, ungroundedStructuredNumbers("统计值为"+number+"。", nil, pdfEvidenceSteps("list_pdf_tables", data)), number)
		}
	}
}
