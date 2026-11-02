package tools

import (
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"testing"
)

func TestStructuredMissingScopeIsActionableFailureNotSuccessfulQuery(t *testing.T) {
	calls := 0
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "kb-b", decodeStructuredScopeIdentity(t, r)["knowledge_base_id"])
		_, _ = io.WriteString(w, `{"kind":"structured_analysis","source_kind":"pdf_table","status":"ok","columns":["amount"],"rows":[["7"]]}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 7}})
	for _, operation := range []string{"read_pdf_table", "query_pdf_table", "query_series"} {
		raw, _ := json.Marshal(map[string]any{"operation": operation, "arguments": map[string]any{}})
		result, err := base.Execute(structuredScopeContext(), raw)
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, "scope_selection_required", result.Data["status"])
		require.Contains(t, result.Error, "knowledge_base_id")
		require.Contains(t, result.Error, "顶层")
		require.Zero(t, calls)
	}
	_, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_pdf_table","arguments":{"knowledge_base_id":"kb-b"}}`))
	require.ErrorContains(t, err, "同级")
	require.Zero(t, calls)
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_pdf_table","knowledge_base_id":"kb-b","arguments":{"table_id":"pdft-fixture","sql":"SELECT amount FROM data"}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, calls)
}

func TestStructuredTypedScopeRequiredOnlyForMultipleAuthorizedLibraries(t *testing.T) {
	directory := map[string]any{"tools": []any{map[string]any{"name": "query_pdf_table", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"table_id": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}}, "required": []any{"table_id", "sql"}}}}}
	for _, count := range []int{1, 2} {
		targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}}
		if count == 2 {
			targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 7})
		}
		base := NewStructuredDataTool().WithSearchTargets(targets)
		tools := NewStructuredReadOperationTools(base, directory)
		require.Len(t, tools, 1)
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tools[0].Parameters(), &schema))
		required := schema["required"].([]any)
		if count == 2 {
			require.Contains(t, required, "knowledge_base_id")
		} else {
			require.NotContains(t, required, "knowledge_base_id")
		}
	}
	// Schema projection never mutates the service's original operation schema.
	original := directory["tools"].([]any)[0].(map[string]any)["inputSchema"].(map[string]any)
	require.NotContains(t, original["required"], "knowledge_base_id")
	require.NotContains(t, original["properties"], "knowledge_base_id")
}
