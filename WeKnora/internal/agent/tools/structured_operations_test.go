package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Fixture is the actual FastMCP describe_tools directory from server.py. Keeping
// the native tool's schema identical prevents a second hand-written interface
// from dropping series_id, the nullable sheet filter, or SQL required inputs.
func describedStructuredReadTools(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile("testdata/structured_read_operations.json")
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(body, &data))
	return data
}

func operationToolByName(t *testing.T, list []types.Tool, name string) types.Tool {
	t.Helper()
	for _, tool := range list {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing tool %s", name)
	return nil
}

func nativeReadTestTool(t *testing.T, handler http.HandlerFunc) *StructuredDataTool {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	keyPath := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(keyPath, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", keyPath)
	t.Setenv("STRUCTURED_DATA_URL", server.URL)
	return NewStructuredDataTool()
}

func TestStructuredNamedReadToolsExposeRealSchemasAndOnlyAllowKnownReads(t *testing.T) {
	data := describedStructuredReadTools(t)
	base := &StructuredDataTool{}
	reads := NewStructuredReadOperationTools(base, data)
	require.Len(t, reads, 5)
	for _, entry := range data["tools"].([]any) {
		described := entry.(map[string]any)
		tool := operationToolByName(t, reads, "structured_"+described["name"].(string))
		encoded, err := json.Marshal(described["inputSchema"])
		require.NoError(t, err)
		require.JSONEq(t, string(encoded), string(tool.Parameters()))
	}
	query := operationToolByName(t, reads, "structured_query_series")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(query.Parameters(), &schema))
	require.Equal(t, []any{"series_id"}, schema["required"])
	require.Equal(t, "string", schema["properties"].(map[string]any)["series_id"].(map[string]any)["type"])
	list := operationToolByName(t, reads, "structured_list_series")
	require.NoError(t, json.Unmarshal(list.Parameters(), &schema))
	require.Equal(t, "string", schema["properties"].(map[string]any)["sheet"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["type"])

	unknown := map[string]any{"name": "delete_dataset", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}
	confirm := map[string]any{"name": "confirm_fields", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}
	data["tools"] = append(data["tools"].([]any), unknown, confirm)
	require.Len(t, NewStructuredReadOperationTools(base, data), 5)
	require.Empty(t, NewStructuredReadOperationTools(base, map[string]any{"tools": []any{unknown, confirm}}))
	require.Empty(t, NewStructuredReadOperationTools(base, map[string]any{"tools": []any{map[string]any{"name": "query_series"}}}))
	duplicate := data["tools"].([]any)[0]
	data["tools"] = append(data["tools"].([]any), duplicate)
	require.Len(t, NewStructuredReadOperationTools(base, data), 4, "duplicate schemas must not pick a winner")
}

func TestStructuredNamedReadBindsOperationAndSignsRealCaller(t *testing.T) {
	calls := 0
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var input map[string]any
		require.NoError(t, json.Unmarshal(body, &input))
		require.Equal(t, "query_series", input["operation"])
		require.Equal(t, "sum", input["arguments"].(map[string]any)["operation"])
		require.Equal(t, "real-series", input["arguments"].(map[string]any)["series_id"])
		encoded := r.Header.Get("X-Research-Identity")
		identityJSON, err := base64.RawURLEncoding.DecodeString(encoded)
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(identityJSON, &identity))
		require.Equal(t, float64(7), identity["tenant_id"])
		require.Equal(t, "actual-user", identity["user_id"])
		hash := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(strings.Repeat("a", 48)))
		_, _ = mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", encoded, hex.EncodeToString(hash[:])}, "\n")))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
		_, _ = io.WriteString(w, `{"aggregate":777.008062964705883,"source_cells":["C74:C85","D74:D85"]}`)
	})
	tool := operationToolByName(t, NewStructuredReadOperationTools(base, describedStructuredReadTools(t)), "structured_query_series")
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actual-user", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 99)
	result, err := tool.Execute(ctx, json.RawMessage(`{"series_id":"real-series","operation":"sum"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, calls)
	for _, input := range []string{
		`{"tenant_id":99}`, `{"user_id":"other"}`, `{"role":"admin"}`, `{"url":"http://other"}`, `{"endpoint":"http://other"}`, `{"token":"other"}`,
		`{"operation":"delete_dataset","arguments":{}}`, `null`,
	} {
		_, err := tool.Execute(ctx, json.RawMessage(input))
		require.Error(t, err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"series_id":"real-series"}`))
	require.Error(t, err)
	require.Equal(t, 1, calls, "spoofed scope and envelopes must fail before transport")
}

func TestStructuredValidationAndServerErrorsRemainFailedEvidence(t *testing.T) {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actual-user", Role: types.TenantRoleViewer})
	for _, test := range []struct {
		name string
		code int
		body string
		want string
	}{
		{"typed validation", 400, `{"detail":[{"loc":["arguments","series_id"],"msg":"Field required","type":"missing"}]}`, "series_id"},
		{"source error", 200, `{"kind":"structured_source_inspection","status":"error","error":"ranges requires sheet","retryable":true}`, "ranges requires sheet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			})
			tool := operationToolByName(t, NewStructuredReadOperationTools(base, describedStructuredReadTools(t)), "structured_query_series")
			result, err := tool.Execute(ctx, json.RawMessage(`{"dataset_id":"real-version"}`))
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Contains(t, result.Error, test.want)
			require.JSONEq(t, test.body, result.Output)
			require.NotEmpty(t, result.Data)
		})
	}
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"detail":"`+strings.Repeat("x", 17000)+`"}`)
	})
	_, err := base.Execute(ctx, json.RawMessage(`{"operation":"query_series","arguments":{}}`))
	require.ErrorContains(t, err, "超过限制")
}
