package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestResearchWorkbenchScopeSurvivesDetachAndIsSigned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(raw, &identity))
		require.Equal(t, "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", identity["workbench_id"])
		require.Equal(t, "library-a", identity["knowledge_base_id"])
		require.Equal(t, float64(13), identity["knowledge_base_tenant_id"])
		require.Equal(t, "Bearer synthetic-current-token", r.Header.Get("X-Research-Authorization"))
		digest := sha256.Sum256([]byte("Bearer synthetic-current-token"))
		require.Equal(t, hex.EncodeToString(digest[:]), identity["authorization_sha256"])
		_, _ = w.Write([]byte(`{"data":null,"gap":"当前工作台尚无研究成果"}`))
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	t.Setenv("COMMODITY_RESEARCH_URL", server.URL)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	ctx = types.WithResearchWorkbenchScope(ctx, types.ResearchWorkbenchScope{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7, UserID: "user-a", KnowledgeBaseID: "library-a", KnowledgeBaseTenantID: 13})
	ctx = types.WithResearchAuthorization(ctx, "Bearer synthetic-current-token")
	result, err := NewCommodityResearchTool().Execute(logger.CloneContext(ctx), json.RawMessage(`{"operation":"summary"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
}

func TestScopedResearchWithoutCurrentAuthorizationNeverCallsService(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	t.Setenv("COMMODITY_RESEARCH_URL", server.URL)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	ctx = types.WithResearchWorkbenchScope(ctx, types.ResearchWorkbenchScope{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7, UserID: "user-a", KnowledgeBaseID: "library-a", KnowledgeBaseTenantID: 13})
	_, err := NewCommodityResearchTool().Execute(ctx, json.RawMessage(`{"operation":"summary"}`))
	require.ErrorContains(t, err, "当前登录")
	require.False(t, called)
}

func TestResearchWorkbenchScopeCannotFallBackToTenantReads(t *testing.T) {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	for _, scope := range []types.ResearchWorkbenchScope{
		{ID: "not-an-id", TenantID: 7, UserID: "user-a", KnowledgeBaseID: "library-a", KnowledgeBaseTenantID: 7},
		{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 8, UserID: "user-a", KnowledgeBaseID: "library-a", KnowledgeBaseTenantID: 7},
		{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7, UserID: "user-b", KnowledgeBaseID: "library-a", KnowledgeBaseTenantID: 7},
		{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7, UserID: "user-a"},
	} {
		_, err := NewCommodityResearchTool().Execute(types.WithResearchWorkbenchScope(ctx, scope), json.RawMessage(`{"operation":"summary"}`))
		require.ErrorContains(t, err, "工作台")
	}
}
