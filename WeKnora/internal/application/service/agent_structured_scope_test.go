package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRegisterToolsStructuredDataKeepsAuthenticatedKnowledgeScope(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(raw, &identity))
		require.Equal(t, "shared-kb", identity["knowledge_base_id"])
		require.Equal(t, float64(29), identity["knowledge_base_tenant_id"])
		require.Equal(t, float64(7), identity["tenant_id"])
		_, _ = io.WriteString(w, `{"status":"ok","datasets":[]}`)
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "service-key")
	require.NoError(t, os.WriteFile(keyFile, []byte(strings.Repeat("k", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", keyFile)
	t.Setenv("STRUCTURED_DATA_URL", server.URL)
	registry := tools.NewToolRegistry()
	svc := newToolSurfaceService(&types.KnowledgeBase{ID: "shared-kb"})
	config := &types.AgentConfig{AllowedTools: []string{tools.ToolStructuredData}, SearchTargets: types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "shared-kb", TenantID: 29}}}
	require.NoError(t, svc.registerTools(t.Context(), registry, config, nil, nil, "bound-session"))
	tool, err := registry.GetTool(tools.ToolStructuredData)
	require.NoError(t, err)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "caller", Role: types.TenantRoleViewer})
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	_, err = tool.Execute(ctx, json.RawMessage(`{"operation":"list_datasets","knowledge_base_id":"outside-kb","arguments":{}}`))
	require.ErrorContains(t, err, "授权范围")
	require.Equal(t, 1, requests)
}
