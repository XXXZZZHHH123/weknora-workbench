package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredGatewayRejectsMisplacedOperationArgumentAndAllowsExplicitCorrection(t *testing.T) {
	calls := 0
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "kb-a", decodeStructuredScopeIdentity(t, r)["knowledge_base_id"])
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "query_pdf_table", body["operation"])
		arguments := body["arguments"].(map[string]any)
		require.Equal(t, "pdft-fixture", arguments["table_id"])
		require.Equal(t, "SELECT quantity FROM data", arguments["sql"])
		_, _ = io.WriteString(w, `{"kind":"structured_analysis","source_kind":"pdf_table","status":"ok","columns":["quantity"],"rows":[["7"]]}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}})
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_pdf_table","knowledge_base_id":"kb-a","table_id":"pdft-fixture","arguments":{"sql":"SELECT quantity FROM data"}}`))
	require.Nil(t, result)
	require.ErrorContains(t, err, `"table_id"`)
	require.ErrorContains(t, err, "operation、arguments、knowledge_base_id")
	require.ErrorContains(t, err, "schema")
	require.Zero(t, calls, "rejected operation arguments must never be moved or executed automatically")
	result, err = base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_pdf_table","knowledge_base_id":"kb-a","arguments":{"table_id":"pdft-fixture","sql":"SELECT quantity FROM data"}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, calls)
	// The clearer diagnostic does not relax forbidden identity parameters.
	for _, raw := range []string{
		`{"operation":"query_pdf_table","knowledge_base_id":"kb-a","tenant_id":7,"arguments":{}}`,
		`{"operation":"query_pdf_table","knowledge_base_id":"kb-a","arguments":{"tenant_id":7}}`,
	} {
		result, err = base.Execute(structuredScopeContext(), json.RawMessage(raw))
		require.Nil(t, result)
		require.Error(t, err)
	}
	require.Equal(t, 1, calls)
}
