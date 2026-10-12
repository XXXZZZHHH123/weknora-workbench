package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceInspectionFixture(t *testing.T) map[string]any {
	t.Helper()
	// Same result shape as source_inspection.py: file provenance lives at the
	// top; every context/range/calculation retains its explicit sheet scope.
	const raw = `{
"kind":"structured_source_inspection","status":"ok",
"dataset_id":"36283d3d-a495-40d2-a13f-e925fc322996","source_file":"棉花数据库(2).xlsx",
"sha256":"original-hash","hash_verified":true,"sheets":["中国棉花平衡表","另一口径"],
"coverage":{"中国棉花平衡表":{"parsed_ranges":["A1:D85"]}},
"matches":[
 {"sheet":"中国棉花平衡表","cell":"A139","value":"2022/23年度","raw_value":"2022/23年度","cached_value":null,"formula":null,"value_status":"original","matched_terms":["2022/23"],"context_range":"A139:D139",
  "context_cells":[{"sheet":"中国棉花平衡表","cell":"B139","value":598,"raw_value":598,"formula":null,"cached_value":null,"value_status":"original"},{"sheet":"中国棉花平衡表","cell":"C139","value":177,"raw_value":177,"formula":null,"cached_value":null,"value_status":"original"}]},
 {"sheet":"另一口径","cell":"A18","value":"2022/23年度","raw_value":"2022/23年度","cached_value":null,"formula":null,"value_status":"original","matched_terms":["2022/23"],"context_range":"A18:C18",
  "context_cells":[{"sheet":"另一口径","cell":"B18","value":650,"raw_value":650,"formula":null,"cached_value":null,"value_status":"original"},{"sheet":"另一口径","cell":"C18","value":127.008062964705883,"raw_value":127.008062964705883,"formula":null,"cached_value":null,"value_status":"original"}]}
],
"ranges":[{"sheet":"中国棉花平衡表","range":"E1:E3","cells":[
 {"sheet":"中国棉花平衡表","cell":"E1","value":775,"raw_value":"=SUM(B139:C139)","formula":"=SUM(B139:C139)","cached_value":775,"value_status":"original"},
 {"sheet":"中国棉花平衡表","cell":"E2","value":null,"raw_value":"=SUM(B140:C140)","formula":"=SUM(B140:C140)","cached_value":null,"value_status":"missing_formula_cache"},
 {"sheet":"中国棉花平衡表","cell":"E3","value":"#VALUE!","raw_value":"#VALUE!","formula":null,"cached_value":null,"value_status":"error"}
]}],
"calculations":[
 {"operation":"sum","sheet":"中国棉花平衡表","value":"775","numeric_value":775,"source_file":"棉花数据库(2).xlsx","sha256":"original-hash","cells":[{"sheet":"中国棉花平衡表","cell":"B139","value":598,"formula":null,"value_status":"original"},{"sheet":"中国棉花平衡表","cell":"C139","value":177,"formula":null,"value_status":"original"}]},
 {"operation":"sum","sheet":"另一口径","value":"777.008062964705883","numeric_value":777.008062964705883,"source_file":"棉花数据库(2).xlsx","sha256":"original-hash","cells":[{"sheet":"另一口径","cell":"B18","value":650,"formula":null,"value_status":"original"},{"sheet":"另一口径","cell":"C18","value":127.008062964705883,"formula":null,"value_status":"original"}]}
],
"truncated":true,"truncation_reasons":["max_cells"],
"scan":{"complete":false,"sheets_scanned":["中国棉花平衡表","另一口径"],"cells_scanned":200}
}`
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &data))
	return data
}

func TestCompactStructuredSourcePreservesFormulaCacheStatusAllConflictsAndProvenance(t *testing.T) {
	data := sourceInspectionFixture(t)
	before, err := json.Marshal(data)
	require.NoError(t, err)
	text, ok := compactStructuredSource(data)
	require.True(t, ok)
	var compact map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &compact))
	for _, key := range []string{"dataset_id", "source_file", "sha256", "hash_verified", "coverage", "sheets", "truncated", "truncation_reasons", "scan"} {
		require.Equal(t, data[key], compact[key], "source and coverage evidence %s", key)
	}
	matches := compact["matches"].([]any)
	require.Len(t, matches, 2, "the second conflicting block must remain visible")
	for i, match := range matches {
		original := data["matches"].([]any)[i].(map[string]any)
		row := match.(map[string]any)
		for _, key := range []string{"sheet", "cell", "value", "formula", "value_status", "context_range", "matched_terms"} {
			require.Equal(t, original[key], row[key])
		}
		require.Len(t, row["context_cells"].([]any), 2)
	}
	formulaCells := compact["ranges"].([]any)[0].(map[string]any)["cells"].([]any)
	require.Equal(t, []any{"E1", float64(775), "=SUM(B139:C139)", "original"}, formulaCells[0])
	require.Equal(t, []any{"E2", nil, "=SUM(B140:C140)", "missing_formula_cache"}, formulaCells[1])
	require.Equal(t, []any{"E3", "#VALUE!", nil, "error"}, formulaCells[2])
	calculations := compact["calculations"].([]any)
	require.Len(t, calculations, 2)
	for i, calculation := range calculations {
		original := data["calculations"].([]any)[i].(map[string]any)
		row := calculation.(map[string]any)
		for _, key := range []string{"operation", "sheet", "value", "numeric_value", "source_file", "sha256"} {
			require.Equal(t, original[key], row[key], "calculation %d field %s", i, key)
		}
	}
	require.Equal(t, "777.008062964705883", calculations[1].(map[string]any)["value"], "decimal result must not be rounded by display compaction")
	after, err := json.Marshal(data)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "source maps and nested cells must remain immutable")
	require.Less(t, len(text), len(before))
}

func TestNamedStructuredOperationsHaveOpaquePolicyAndKeepExternalIdentities(t *testing.T) {
	registry := NewRegistry(true)
	const externalID = "36283d3d-a495-40d2-a13f-e925fc322996"
	registry.RegisterDocument(externalID)
	for _, name := range []string{"structured_list_series", "structured_query_series", "structured_analyze_table", "structured_inspect_source", "structured_get_result_evidence"} {
		require.True(t, HasToolPolicy(name))
		require.False(t, sourceCompactionAllowed(name))
		require.False(t, sourceOutputAllowed(name))
		result := &types.ToolResult{Success: true, Output: `{"dataset_id":"` + externalID + `","knowledge_id":"` + externalID + `","value":777.008062964705883}`}
		require.Equal(t, result.Output, registry.ModelToolResultForTool(name, result), "external IDs must not turn into document handles")
	}
	data := sourceInspectionFixture(t)
	before, err := json.Marshal(data)
	require.NoError(t, err)
	for _, name := range []string{"structured_data", "structured_inspect_source"} {
		result := &types.ToolResult{Success: true, Output: string(before), Data: data}
		text := registry.ModelToolResultForTool(name, result)
		require.Contains(t, text, externalID)
		require.Contains(t, text, "cell_columns")
		require.Contains(t, text, "missing_formula_cache")
		require.Contains(t, text, "775")
		require.Contains(t, text, "777.008062964705883")
		require.Equal(t, string(before), result.Output)
		after, err := json.Marshal(result.Data)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after), "durable result preserves complete raw and cached source cells")
	}
}

func TestCompactStructuredSourceDoesNotRewriteFailedOrUnrelatedResults(t *testing.T) {
	for _, data := range []map[string]any{nil, {"kind": "other", "status": "ok"}, {"kind": "structured_source_inspection", "status": "error", "error": "original SHA256 changed"}} {
		_, ok := compactStructuredSource(data)
		require.False(t, ok)
	}
}
