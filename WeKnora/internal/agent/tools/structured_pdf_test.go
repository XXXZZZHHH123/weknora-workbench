package tools

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestPDFStructuredOperationsKeepSignedAuthorizedKnowledgeScope(t *testing.T) {
	for _, operation := range []string{"list_pdf_tables", "read_pdf_table", "query_pdf_table"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				identity := decodeStructuredScopeIdentity(t, r)
				require.Equal(t, "kb-shared", identity["knowledge_base_id"])
				require.Equal(t, float64(29), identity["knowledge_base_tenant_id"])
				require.Equal(t, float64(7), identity["tenant_id"])
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Equal(t, operation, payload["operation"])
				require.NotContains(t, payload, "knowledge_base_id")
				require.NotContains(t, payload["arguments"], "tenant_id")
				digest := sha256.Sum256(body)
				mac := hmac.New(sha256.New, []byte(strings.Repeat("a", 48)))
				_, _ = mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", r.Header.Get("X-Research-Identity"), hex.EncodeToString(digest[:])}, "\n")))
				require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
				_, _ = io.WriteString(w, `{"kind":"structured_analysis","source_kind":"pdf_table","status":"ok","source":{"file_name":"sample.pdf","sha256":"actual-source-digest"},"rows":[["-1.250000000000000000"]],"row_provenance":[{"sql_result_lineage_verified":false}]}`)
			}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-shared", TenantID: 29}})
			arguments := map[string]any{}
			if operation != "list_pdf_tables" {
				arguments["table_id"] = "pdft_actual"
			}
			if operation == "query_pdf_table" {
				arguments["sql"] = "SELECT sum(amount) FROM data"
			}
			encoded, err := json.Marshal(map[string]any{"operation": operation, "knowledge_base_id": "kb-shared", "arguments": arguments})
			require.NoError(t, err)
			result, err := base.Execute(structuredScopeContext(), encoded)
			require.NoError(t, err)
			require.True(t, result.Success)
			var data map[string]any
			canonical, err := json.Marshal(result.Data)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(canonical, &data))
			require.Equal(t, "pdf_table", data["source_kind"])
			require.Equal(t, []any{[]any{"-1.250000000000000000"}}, data["rows"])
			for _, spoof := range []string{
				`{"operation":"query_pdf_table","knowledge_base_id":"kb-outside","arguments":{"table_id":"pdft_actual","sql":"SELECT * FROM data"}}`,
				`{"operation":"query_pdf_table","arguments":{"table_id":"pdft_actual","tenant_id":99}}`,
				`{"operation":"query_pdf_table","arguments":{"table_id":"pdft_actual","url":"http://outside"}}`,
				`{"operation":"publish_pdf_table","arguments":{}}`,
			} {
				_, err = base.Execute(structuredScopeContext(), json.RawMessage(spoof))
				require.Error(t, err)
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestPDFNamedReadOperationsUseServiceSchemasAndRejectPublication(t *testing.T) {
	directory := map[string]any{"tools": []any{}}
	for _, operation := range []string{"list_pdf_tables", "read_pdf_table", "query_pdf_table", "publish_pdf_table"} {
		directory["tools"] = append(directory["tools"].([]any), map[string]any{"name": operation, "description": "PDF read contract", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"table_id": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}}, "additionalProperties": false}})
	}
	tools := NewStructuredReadOperationTools(&StructuredDataTool{}, directory)
	require.Len(t, tools, 3)
	for _, operation := range []string{"list_pdf_tables", "read_pdf_table", "query_pdf_table"} {
		require.NotNil(t, operationToolByName(t, tools, "structured_"+operation))
	}
}
