package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredSingleScopeShortDirectoryKeepsServiceCoverageInModelView(t *testing.T) {
	for _, operation := range []string{"list_datasets", "list_pdf_tables"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			serviceData := map[string]any{
				"source_data_read": false, "catalog_source_types": []string{"published_pdf_tables"},
				"catalog_excludes":          []string{"document_body", "presentation_slides", "original_figures"},
				"directory_notice":          "An empty structured catalog does not establish document absence.",
				"document_source_next_step": "Use currently available document tools within the authorized scope; filenames do not establish content coverage.",
				"tables":                    []any{}, "total_table_count": 0, "next_offset": nil, "output_truncated": false,
			}
			if operation == "list_datasets" {
				delete(serviceData, "tables")
				delete(serviceData, "total_table_count")
				serviceData["datasets"], serviceData["dataset_count"], serviceData["total_dataset_count"] = []any{}, 0, 0
				serviceData["catalog_source_types"] = []string{"registered_spreadsheet_versions", "published_pdf_tables"}
				serviceData["empty_catalog_notice"] = "Changing question/search/latest_only does not turn this complete empty inventory into document retrieval."
			}
			base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
				calls++
				require.Equal(t, "allowed-kb", decodeStructuredScopeIdentity(t, request)["knowledge_base_id"])
				require.NoError(t, json.NewEncoder(w).Encode(serviceData))
			}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "allowed-kb", TenantID: 7}})
			registry := NewToolRegistry()
			directory := map[string]any{"tools": []any{map[string]any{"name": operation, "description": "Structured catalog only", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
			for _, tool := range NewStructuredReadOperationTools(base, directory) {
				registry.RegisterTool(tool)
			}
			result, err := registry.ExecuteTool(structuredScopeContext(), "structured_"+operation, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 1, calls, "coverage advice performs no retrieval or retry")
			require.NotContains(t, result.Output, "output truncated")
			visible := modelcontext.NewRegistry(true).ModelToolResultForTool("structured_"+operation, result)
			var view map[string]any
			require.NoError(t, json.Unmarshal([]byte(visible), &view))
			for _, key := range []string{"source_data_read", "catalog_source_types", "catalog_excludes", "directory_notice", "document_source_next_step", "empty_catalog_notice"} {
				if expected, exists := serviceData[key]; exists {
					want, err := json.Marshal(expected)
					require.NoError(t, err)
					got, err := json.Marshal(view[key])
					require.NoError(t, err)
					require.JSONEq(t, string(want), string(got), key)
				}
			}
			require.NotContains(t, visible, "source_context", "authenticated transport scope stays private")
		})
	}
}

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
					"catalog_source_types": []string{"registered_spreadsheet_versions"},
					"offset":               0, "next_offset": 4, "datasets_truncated": true, "latest_only": true,
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
			require.Equal(t, []any{"registered_spreadsheet_versions"}, view["catalog_source_types"], "an unlisted PDF family must not become queried coverage")
			require.ElementsMatch(t, []any{"document_body", "presentation_slides", "original_figures"}, view["catalog_excludes"])
			require.NotEmpty(t, view["document_source_next_step"], "budget projection must retain document-discovery limitations")
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
