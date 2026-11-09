package modelcontext

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func documentViewModelFixture(t *testing.T) (*types.ToolResult, string) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	ref := "document-table-view:sha256:" + digest
	data := map[string]any{
		"kind": "document_table_view", "status": "ok", "view_ref": ref, "view_digest": digest,
		"source_fingerprint": "source-fingerprint-unchanged",
		"view_snapshot": map[string]any{
			"version": "document-table-view-v1", "operation": "select", "source_fingerprint": "source-fingerprint-unchanged",
			"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "table_index": 2,
			"row_indices": []int{1, 2, 5}, "column_indices": []int{0, 2},
			"headers":         []string{"Source <label>", "Exact value"},
			"rows":            [][]string{{"First", "9007199254740993.125000"}, {"Second", "-0.0000"}, {"Third", ""}},
			"raw_source_rows": []string{"| First | ignored | 9007199254740993.125000 |", "| Second | ignored | -0.0000 |", "| Third | ignored | |"},
			"unit_context":    "<source>Exact units: kg & units</source>", "parse_complete": false,
			"parse_warning": "Selected source prefix; full source not parsed",
			"markdown":      "| Source <label> | Exact value |\n| --- | --- |\n| First | 9007199254740993.125000 |",
		},
	}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, ref
}

func TestDocumentTableViewModelOutputShowsExactSelectableReferenceAndCoverage(t *testing.T) {
	result, ref := documentViewModelFixture(t)
	original, err := json.Marshal(result)
	require.NoError(t, err)
	r := NewRegistry(true)
	output := r.ModelToolResultForTool("calculate_document_table", result)
	require.Contains(t, output, `ref="`+ref+`" source_chunk="chunk://1" document="doc://1"`)
	require.Contains(t, output, `selected_row_count="3" selected_column_count="2" parse_complete="false"`)
	require.Contains(t, output, `[{"start":1,"end":2},{"start":5,"end":5}]`)
	require.Contains(t, output, `Selected source prefix; full source not parsed`)
	require.Contains(t, output, `&lt;document_table_view ref="`+ref+`" /&gt;`)
	require.Contains(t, output, "Do not retype or regenerate its rows, headers or numbers")
	require.NotContains(t, output, "9007199254740993.125000", "the immutable view expands at final review; model need not recopy its numeric payload")
	require.NotContains(t, output, "actual-chunk")
	decoder := xml.NewDecoder(strings.NewReader(output))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	after, err := json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, string(original), string(after), "display projection cannot mutate source snapshots, fingerprints, markdown or precision")
}

func TestDocumentTableViewReferencesRemainLiteralAcrossCompleteAndStreamingCodecs(t *testing.T) {
	r := NewRegistry(true)
	result, ref := documentViewModelFixture(t)
	r.ModelToolResultForTool("calculate_document_table", result)
	marker := `<document_table_view ref="` + ref + `" />`
	require.Equal(t, marker, r.DecodeOutputText(marker))
	decoder := r.StreamDecoder()
	var streamed strings.Builder
	for _, fragment := range []string{marker[:13], marker[13:40], marker[40:]} {
		streamed.WriteString(decoder.Feed(fragment))
	}
	streamed.WriteString(decoder.Flush())
	require.Equal(t, marker, streamed.String())
	messages := []chat.Message{{Role: "assistant", Content: marker}}
	encoded := r.EncodeMessages(messages)
	require.Equal(t, marker, encoded[0].Content)
	unknown := `<document_table_view ref="document-table-view:sha256:` + strings.Repeat("f", 64) + `" />`
	require.Equal(t, unknown, r.DecodeOutputText(unknown), "only the sealed final-result resolver can accept or reject a view; codecs must never turn it into a source")
	require.Empty(t, r.DecodeOutputText(`<ref id="evidence://1"/>`), "presentation references cannot authorize structured evidence")
}

func TestDocumentTableViewProjectionDoesNotInheritExternalToolNamespaces(t *testing.T) {
	r := NewRegistry(true)
	result, _ := documentViewModelFixture(t)
	for _, tool := range []string{"mcp_external", "call_mcp_tool", "structured_analyze_table"} {
		require.Equal(t, result.Output, r.ModelToolResultForTool(tool, result), "a matching foreign result kind does not activate the built-in presentation projection")
	}
	result.Data["view_ref"] = "chunk://1"
	output := r.ModelToolResultForTool("calculate_document_table", result)
	require.Contains(t, output, "document_table_view_unavailable")
	require.NotContains(t, output, "delivery_instruction")
}

func TestDocumentTableComputedViewsRemainSelectableWithoutHidingPartialAnalysis(t *testing.T) {
	r := NewRegistry(true)
	calculated, ref := documentViewModelFixture(t)
	calculated.Data["kind"] = "source_calculation"
	calculated.Data["selected_row_count"] = 3
	calculated.Data["calculated_row_count"] = 2
	calculated.Data["failed_rows"] = []any{map[string]any{"row_index": 5, "error": "missing source cell"}}
	snapshot := calculated.Data["view_snapshot"].(map[string]any)
	snapshot["operation"] = "calculate"
	snapshot["computed_column_count"] = 3
	snapshot["headers"] = append(snapshot["headers"].([]string), "Computed value", "Computed unit", "Execution status")
	for index, row := range snapshot["rows"].([][]string) {
		snapshot["rows"].([][]string)[index] = append(row, "", "", "source value unavailable")
	}
	output := r.ModelToolResultForTool("calculate_document_table", calculated)
	require.Contains(t, output, ref)
	require.Contains(t, output, `operation="calculate"`)
	require.Contains(t, output, `<computed_headers count="3">["Computed value","Computed unit","Execution status"]</computed_headers>`)
	require.Contains(t, output, `<calculation_status selected_row_count="3" calculated_row_count="2" failed_row_count="1" />`)
	analysis := &types.ToolResult{Success: false, Error: "Only one actual plan succeeded", Data: map[string]any{
		"kind": "document_table_analysis", "status": "partial", "execution_complete": false,
		"executions": []any{
			map[string]any{"success": true, "tool_name": "calculate_document_table", "result": calculated.Data},
			map[string]any{"success": false, "tool_name": "calculate_document_table", "result": map[string]any{"status": "error", "error": "Source range unavailable"}},
		},
	}}
	output = r.ModelToolResultForTool("analyze_document_tables", analysis)
	require.Contains(t, output, `<document_table_analysis_views success="false" status="partial" execution_complete="false">`)
	require.Contains(t, output, ref)
	require.Contains(t, output, "Source range unavailable")
	require.Contains(t, output, "Only one actual plan succeeded")
	require.NotContains(t, output, "9007199254740993.125000")
	calculated.Success = false
	calculated.Output = "failed source calculation"
	require.NotContains(t, r.ModelToolResultForTool("calculate_document_table", calculated), "document_table_view_available")
}
