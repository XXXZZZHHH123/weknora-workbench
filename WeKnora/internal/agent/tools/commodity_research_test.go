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

func TestCommodityResearchSignsCallerInsteadOfExecutionTenant(t *testing.T) {
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
	t.Setenv("COMMODITY_RESEARCH_URL", server.URL)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 99)
	ctx = commodityResearchTestScope(ctx)
	result, err := NewCommodityResearchTool().Execute(ctx, json.RawMessage(`{"operation":"brief"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Output, "尚无简报")
}

func TestCommodityResearchRefusesModelScopeAndMissingIdentity(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_URL", "http://localhost:1")
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	tool := NewCommodityResearchTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"summary"}`))
	require.Error(t, err)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "a", Role: types.TenantRoleViewer})
	ctx = commodityResearchTestScope(ctx)
	for _, args := range []string{`{"operation":"summary","tenant_id":99}`, `{"operation":"brief","user_id":"other"}`, `{"operation":"summary","url":"http://other"}`} {
		_, err = tool.Execute(ctx, json.RawMessage(args))
		require.ErrorContains(t, err, "未声明")
	}
}

func commodityResearchTestScope(ctx context.Context) context.Context {
	caller := types.CallerFromContext(ctx)
	ctx = types.WithResearchWorkbenchScope(ctx, types.ResearchWorkbenchScope{
		ID: "00000000-0000-4000-8000-000000000007", TenantID: caller.TenantID, UserID: caller.UserID,
		KnowledgeBaseID: "synthetic-library", KnowledgeBaseTenantID: 13,
	})
	return types.WithResearchAuthorization(ctx, "Bearer synthetic-test-authorization")
}

func TestCommodityResearchMissingScopeReturnsCorrectableFailureWithoutRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	t.Setenv("COMMODITY_RESEARCH_URL", server.URL)
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	tool := NewCommodityResearchTool()
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"indicators","search":"requested field"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "scope_required", result.Data["reason_code"])
	require.Equal(t, false, result.Data["request_started"])
	require.Zero(t, calls)
	require.False(t, CommodityResearchScopeAvailable(ctx))
	scoped := commodityResearchTestScope(ctx)
	require.True(t, CommodityResearchScopeAvailable(scoped))
	wrong := types.WithCaller(scoped, types.Caller{TenantID: 8, UserID: "reader", Role: types.TenantRoleViewer})
	require.False(t, CommodityResearchScopeAvailable(wrong), "a saved scope cannot follow another caller")
}

func TestCommodityResearchPreservesServiceConflictDetailsWithoutRetryOrFallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"detail":{"code":"source_revision_changed","message":"The current authorized source revision changed.","private":"not-forwarded"}}`)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	t.Setenv("COMMODITY_RESEARCH_URL", server.URL)
	ctx := commodityResearchTestScope(types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer}))
	result, err := NewCommodityResearchTool().Execute(ctx, json.RawMessage(`{"operation":"indicators"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, http.StatusConflict, result.Data["http_status"])
	require.Equal(t, "source_revision_changed", result.Data["reason_code"])
	require.Contains(t, result.Error, "authorized source revision changed")
	require.NotContains(t, result.Output, "not-forwarded")
	require.Equal(t, 1, calls)
}
