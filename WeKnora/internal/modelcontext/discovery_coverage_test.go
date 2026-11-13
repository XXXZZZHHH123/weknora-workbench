package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryMetadataSurvivesModelProjection(t *testing.T) {
	for _, name := range []string{"structured_list_series", "structured_list_datasets", "structured_list_pdf_tables"} {
		r := NewRegistry(true)
		result := &types.ToolResult{Success: true, Output: `{"series":[],"truncated":true,"value":9007199254740993.00000000000001}`, Data: map[string]any{
			"discovery_coverage": map[string]any{"representation": "structured_field_catalog", "document_body_searched_by_this_operation": false, "truncated": true, "unchanged_repeat": true},
		}}
		output := r.ModelToolResultForTool(name, result)
		require.True(t, json.Valid([]byte(output)), "catalog retains valid JSON")
		require.Contains(t, output, "9007199254740993.00000000000001", "numeric bytes must not round through float64")
		require.Contains(t, output, "discovery_coverage", name)
		require.Contains(t, output, "document_body_searched_by_this_operation", name)
		require.Contains(t, output, "unchanged_repeat", name)
	}
}

func TestLocationRecoverySurvivesFailedAndSuccessfulTextProjection(t *testing.T) {
	for _, success := range []bool{false, true} {
		r := NewRegistry(true)
		result := &types.ToolResult{Success: success, Error: "No page mapping", Data: map[string]any{
			"display_type": "knowledge_chunks_list", "knowledge_id": "real-document", "knowledge_title": "manual.pdf",
			"document":          map[string]any{"knowledge_id": "real-document", "title": "manual.pdf"},
			"chunks":            []map[string]any{{"chunk_id": "real-chunk", "knowledge_id": "real-document", "content": "Verified text"}},
			"location_recovery": map[string]any{"knowledge_id": "real-document", "notice": "read_pdf_table source_cells are physical page coordinates; verify same original identity"},
		}}
		output := r.ModelToolResultForTool("read_document", result)
		require.Contains(t, output, "location_recovery")
		require.Contains(t, output, "source_cells")
		if success {
			require.Contains(t, output, "Verified text")
		}
	}
}
