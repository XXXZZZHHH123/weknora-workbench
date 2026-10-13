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

func TestStructuredDataSignsCallerInsteadOfExecutionTenant(t *testing.T) {
	key := strings.Repeat("a", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		encoded := r.Header.Get("X-Research-Identity")
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(raw, &identity))
		require.Equal(t, float64(7), identity["tenant_id"])
		require.Equal(t, "user-a", identity["user_id"])
		hash := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", encoded, hex.EncodeToString(hash[:])}, "\n")))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"operation":"brief","data":null,"gap":"尚无简报"}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(key), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	t.Setenv("STRUCTURED_DATA_URL", server.URL)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 99)
	result, err := NewStructuredDataTool().Execute(ctx, json.RawMessage(`{"operation":"resolve_fields","arguments":{"question":"现有字段"}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Output, "尚无简报")
}

func TestStructuredDataRefusesModelScopeAndMissingIdentity(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://localhost:1")
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	tool := NewStructuredDataTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
	require.Error(t, err)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "a", Role: types.TenantRoleViewer})
	for _, args := range []string{`{"operation":"list_datasets","arguments":{},"tenant_id":99}`, `{"operation":"list_datasets","arguments":{},"user_id":"other"}`, `{"operation":"list_datasets","arguments":{},"url":"http://other"}`} {
		_, err = tool.Execute(ctx, json.RawMessage(args))
		require.ErrorContains(t, err, "未声明")
	}
}

func TestStructuredDataBindsHumanSourceQuestionBeforeSigning(t *testing.T) {
	key := strings.Repeat("b", 48)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload struct {
			Arguments map[string]any `json:"arguments"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Equal(t, "20天原料", payload.Arguments["source_question"])
		encoded := r.Header.Get("X-Research-Identity")
		hash := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", encoded, hex.EncodeToString(hash[:])}, "\n")))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"needs_clarification"}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(key), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	t.Setenv("STRUCTURED_DATA_URL", server.URL)
	tool := NewStructuredDataTool()
	tool.BindSourceQuestion("20天原料")
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "a", Role: types.TenantRoleViewer})
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"query_series","arguments":{"series_id":"C","source_question":"模型虚构口径"}}`))
	require.NoError(t, err)
	require.Contains(t, result.Output, "needs_clarification")
}
