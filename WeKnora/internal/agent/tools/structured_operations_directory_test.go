package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredNativeMultiScopeDirectoryPreservesProjectionThroughRegistry(t *testing.T) {
	for _, budget := range []int{3000, DefaultMaxToolOutput} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
				identity := decodeStructuredScopeIdentity(t, request)
				kb := identity["knowledge_base_id"].(string)
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				require.Equal(t, "list_datasets", payload["operation"])
				require.Equal(t, strings.Repeat("a", 64), request.Header.Get("X-Structured-Manifest-SHA256"))
				w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat("a", 64))
				datasets := []map[string]any{}
				for index := 0; index < 4; index++ {
					datasets = append(datasets, wideStructuredDataset(fmt.Sprintf("%s-source-%d", kb, index)))
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"datasets": datasets, "dataset_count": 12, "total_dataset_count": 37,
					"offset": 0, "next_offset": 4, "datasets_truncated": true, "latest_only": true,
					"field_candidates":      []map[string]any{{"dataset_id": kb + "-source-0", "series_id": "observed-series", "metric": "Actual measure"}},
					"field_candidate_count": 1, "field_candidate_stage": "published_headers", "field_candidate_scope": "current_dataset_page",
				}))
			}).WithSearchTargets(types.SearchTargets{
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7},
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 29},
			})
			registry := NewToolRegistry()
			registry.SetMaxToolOutputSize(budget)
			registry.RegisterTool(base)
			installed, err := registry.InstallStructuredManifest(structuredProtocolDirectory("a"))
			require.NoError(t, err)
			require.True(t, installed.Success)
			result, err := registry.ExecuteTool(structuredScopeContext(), "structured_list_datasets", json.RawMessage(`{"question":"Find the requested source"}`))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			require.Equal(t, "scope_selection_required", result.Data["status"])
			require.NotContains(t, result.Output, "... [output truncated:", "the generic registry fallback must never split directory JSON")
			view := structuredDirectoryView(t, result, budget)
			require.Equal(t, "scope_selection_required", view["status"])
			require.Equal(t, true, view["directory_metadata_truncated"])
			require.Equal(t, false, view["source_data_read"])
			require.Equal(t, true, view["catalog_data_read"])
			require.Equal(t, strings.Repeat("a", 64), view["manifest_version"])
			require.Equal(t, float64(1), view["generation"])
			descriptor := view["_structured_execution"].(map[string]any)
			require.Equal(t, "list_datasets", descriptor["canonical_operation"])
			require.Equal(t, "structured_list_datasets", descriptor["tool_name"])
			require.Equal(t, view["schema_sha256"], descriptor["schema_sha256"])
			catalogs := structuredRecallRows(view["knowledge_base_catalogs"])
			require.Len(t, catalogs, 2)
			canonical := structuredRecallRows(result.Data["knowledge_base_catalogs"])
			for index, kb := range []string{"kb-a", "kb-b"} {
				row := catalogs[index]
				require.Equal(t, kb, row["knowledge_base_id"])
				require.Equal(t, "queried", row["catalog_status"])
				require.Equal(t, true, row["datasets_truncated"])
				require.Equal(t, true, row["latest_only"])
				visible := structuredRecallRows(row["datasets"])
				require.NotEmpty(t, visible, "each authorized scope retains a real source identity")
				require.Equal(t, kb+"-source-0", visible[0]["dataset_id"])
				require.Equal(t, fmt.Sprint(len(visible)), fmt.Sprint(row["next_offset"]), "resume at the first whole omitted dataset in this scope")
				original := structuredRecallRows(canonical[index]["datasets"])
				require.Len(t, original, 4, "model projection must not replace canonical directory evidence")
				require.Len(t, original[0]["sheets"], 120)
				require.Equal(t, json.Number("4"), canonical[index]["next_offset"])
			}
		})
	}
}

func TestStructuredDiagnosticDirectoryKeepsExistingOmissionsAndRepairsInvalidOutput(t *testing.T) {
	descriptor := StructuredOperationDescriptor{Operation: "list_datasets", ToolName: "structured_list_datasets",
		SchemaSHA256: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64), ProtocolVersion: "2", Generation: 3,
		ScopeMode: "directory", EffectClass: "read"}
	for _, output := range []string{
		`{"status":"scope_selection_required","datasets":[{"dataset_id":"actual-source"}],"offset":0,"next_offset":1,"datasets_truncated":true,"directory_metadata_truncated":true}`,
		"", "not json", `{"status":"scope_selection_required"} trailing`, "null",
	} {
		t.Run(output, func(t *testing.T) {
			result := &types.ToolResult{Success: true, Output: output, Data: map[string]any{
				"status": "scope_selection_required", "datasets": []map[string]any{wideStructuredDataset("actual-source"), wideStructuredDataset("omitted-source")},
				"offset": 0, "next_offset": 2, "datasets_truncated": true, "canonical_only_note": "must not reappear in a valid projected view",
			}}
			attachStructuredDiagnosticDescriptor(result, descriptor)
			result = fitStructuredNativeDirectory(WithOutputBudget(structuredScopeContext(), 3000), result)
			require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 3000)
			view := structuredDirectoryView(t, result, 3000)
			require.Equal(t, strings.Repeat("b", 64), view["manifest_version"])
			require.Equal(t, float64(3), view["generation"])
			require.Equal(t, "actual-source", structuredRecallRows(view["datasets"])[0]["dataset_id"])
			if strings.HasPrefix(output, `{"status":"scope_selection_required","datasets"`) {
				require.NotContains(t, view, "canonical_only_note")
				require.NotContains(t, result.Output, "omitted-source", "diagnostics cannot resurrect data deliberately omitted from the model view")
				require.Equal(t, float64(1), view["next_offset"])
			}
			require.Len(t, result.Data["datasets"], 2)
			require.Equal(t, 2, result.Data["next_offset"])
			require.Contains(t, result.Data, "canonical_only_note")
		})
	}
}
