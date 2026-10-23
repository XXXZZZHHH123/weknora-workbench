package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredMultiScopeRecallPreservesPDFOnlyLibrary(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		identity := decodeStructuredScopeIdentity(t, r)
		switch identity["knowledge_base_id"] {
		case "kb-pdf":
			_, _ = io.WriteString(w, `{"datasets":[],"pdf_tables":[{"table_id":"pdft-fixture","title":null,"source":{"file_name":"sample.pdf","knowledge_id":"doc-fixture"},"fragments":[{"id":"fragment-1","page":2,"caption":"Quarterly flows (tonnes)","footnotes":["Provisional"]}],"columns":[{"name":"Amount","type":"decimal","unit":"tonnes"}],"coverage":{"logical_row_count":3,"source_document_completeness":"unknown"},"status":"reviewed","extraction_verified":false}],"pdf_notice":"Review is not OCR verification."}`)
		case "kb-excel":
			_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"sheet-fixture","source_file":"other.xlsx","is_latest_for_source":true}],"literal_fields":[]}`)
		default:
			t.Fatalf("unauthorized or guessed library: %v", identity)
		}
	}).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-pdf", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-excel", TenantID: 29},
	})
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"What does the flows table contain?"}}`))
	require.NoError(t, err)
	require.Equal(t, "scope_selection_required", result.Data["status"])
	catalogs := result.Data["knowledge_base_catalogs"].([]map[string]any)
	require.Empty(t, catalogs[0]["datasets"])
	tables := structuredRecallRows(catalogs[0]["pdf_tables"])
	require.Len(t, tables, 1, "an Excel-empty library must retain its published PDF catalog")
	require.Equal(t, "pdft-fixture", tables[0]["table_id"])
	require.Equal(t, "reviewed", tables[0]["status"])
	require.Equal(t, false, tables[0]["extraction_verified"])
	require.Contains(t, result.Output, "Quarterly flows (tonnes)")
	require.Contains(t, result.Output, "Provisional")
	require.Contains(t, result.Output, "unknown")
	require.Equal(t, "Review is not OCR verification.", catalogs[0]["pdf_notice"])
	require.Equal(t, false, catalogs[0]["pdf_tables_truncated"])
	require.Len(t, structuredRecallRows(catalogs[1]["datasets"]), 1)
	require.Empty(t, structuredRecallRows(catalogs[1]["pdf_tables"]))
}

func TestStructuredMultiScopeRecallBoundsPDFCatalogWithoutClaimingAbsence(t *testing.T) {
	var targets types.SearchTargets
	for i := 0; i < 6; i++ {
		targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: fmt.Sprintf("kb-%d", i), TenantID: 7})
	}
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		identity := decodeStructuredScopeIdentity(t, r)
		var tables []map[string]any
		for i := 0; i < 20; i++ {
			tables = append(tables, map[string]any{"table_id": fmt.Sprintf("%s-table-%d", identity["knowledge_base_id"], i), "status": "reviewed"})
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"datasets": []any{}, "pdf_tables": tables}))
	}).WithSearchTargets(targets)
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"List available tables"}}`))
	require.NoError(t, err)
	total := 0
	for _, catalog := range result.Data["knowledge_base_catalogs"].([]map[string]any) {
		listed := len(structuredRecallRows(catalog["pdf_tables"]))
		require.LessOrEqual(t, listed, 16)
		require.Equal(t, 20, catalog["pdf_table_count"])
		require.Equal(t, true, catalog["pdf_tables_truncated"])
		total += listed
	}
	require.Equal(t, 64, total)
	require.Equal(t, true, result.Data["pdf_tables_truncated"])
	require.Contains(t, result.Output, "list_pdf_tables")
}

// Synthetic libraries and headers; not acceptance facts.
func TestStructuredMultiScopeQuestionRecallsFieldsInEveryLibrary(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "list_datasets", payload["operation"])
		identity := decodeStructuredScopeIdentity(t, r)
		library, _ := identity["knowledge_base_id"].(string)
		arguments, _ := payload["arguments"].(map[string]any)
		mu.Lock()
		seen[library], _ = arguments["question"].(string)
		mu.Unlock()
		switch library {
		case "kb-first":
			_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"docs-only","source_file":"a.xlsx","is_latest_for_source":true,"sheets":["S1"]}],"literal_fields":[],"literal_fields_truncated":false}`)
		case "kb-second":
			_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"old","source_file":"m.xlsx","is_latest_for_source":false},{"dataset_id":"measures","source_file":"m.xlsx","is_latest_for_source":true,"sheets":["Prices"]}],"literal_fields":[{"dataset_id":"measures","series_id":"generic.ab","sheet":"Prices","metric":"X9-Y9"}],"literal_fields_truncated":false}`)
		default:
			t.Fatalf("unexpected library %q", library)
		}
	}).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29},
	}).WithKnowledgeBaseMetadata([]*types.KnowledgeBase{
		{ID: "kb-first", TenantID: 7, Name: "Documents"},
		{ID: "kb-second", TenantID: 29, Name: "Measurements"},
	})

	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"find X9-Y9 and summarize"}}`))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"kb-first": "find X9-Y9 and summarize", "kb-second": "find X9-Y9 and summarize"}, seen,
		"every authorized library is queried with its own signed scope and the same question")
	require.Equal(t, "scope_selection_required", result.Data["status"], "recall never selects a library")
	require.Equal(t, "queried_with_question", result.Data["dataset_catalog_status"])
	fields := result.Data["literal_fields"].([]any)
	require.Len(t, fields, 1)
	field := fields[0].(map[string]any)
	require.Equal(t, "kb-second", field["knowledge_base_id"])
	require.Equal(t, "generic.ab", field["series_id"])
	catalogs := result.Data["knowledge_base_catalogs"].([]map[string]any)
	require.Len(t, catalogs, 2)
	require.Equal(t, "Measurements", catalogs[1]["name"])
	datasets := catalogs[1]["datasets"].([]map[string]any)
	require.Len(t, datasets, 1, "only the latest version of each source is listed")
	require.Equal(t, "measures", datasets[0]["dataset_id"])
	require.Contains(t, result.Output, "X9-Y9")

	// Without a question the directory stays unqueried, as before.
	plain, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
	require.NoError(t, err)
	require.Equal(t, "not_queried", plain.Data["dataset_catalog_status"])
}

func TestStructuredMultiScopeRecallKeepsUnavailableLibraryVisible(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		identity := decodeStructuredScopeIdentity(t, r)
		if identity["knowledge_base_id"] == "kb-first" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"datasets":[],"literal_fields":[]}`)
	}).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 7},
	})
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"q"}}`))
	require.NoError(t, err)
	catalogs := result.Data["knowledge_base_catalogs"].([]map[string]any)
	require.Equal(t, "unavailable", catalogs[0]["catalog_status"])
	require.Equal(t, "queried", catalogs[1]["catalog_status"])
}

func TestStructuredMultiScopeRecallMarksLibraryBoundTruncation(t *testing.T) {
	targets := types.SearchTargets{}
	for i := 0; i < maxRecallKnowledgeBases+2; i++ {
		targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), TenantID: 7})
	}
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"datasets":[],"literal_fields":[]}`)
	}).WithSearchTargets(targets)
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"q"}}`))
	require.NoError(t, err)
	require.Len(t, result.Data["knowledge_base_catalogs"].([]map[string]any), maxRecallKnowledgeBases)
	require.Equal(t, true, result.Data["literal_fields_truncated"], "unqueried libraries are reported as truncation, never as absence")
}
