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
	for _, args := range []string{`{"operation":"summary","tenant_id":99}`, `{"operation":"brief","user_id":"other"}`, `{"operation":"summary","url":"http://other"}`} {
		_, err = tool.Execute(ctx, json.RawMessage(args))
		require.ErrorContains(t, err, "未声明")
	}
}

func TestCommodityResearchChartOperationsSchema(t *testing.T) {
	tool := NewCommodityResearchTool()
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	properties := schema["properties"].(map[string]any)
	operations := properties["operation"].(map[string]any)["enum"]
	for _, operation := range []string{"summary", "indicators", "series", "judgment", "signals", "brief", "strategies", "charts", "cycle"} {
		require.Contains(t, operations, operation)
	}
	template := properties["template"].(map[string]any)
	require.Equal(t, "string", template["type"])
	require.Equal(t, float64(80), template["maxLength"])
	require.Equal(t, false, schema["additionalProperties"])
	for _, text := range []string{"charts", "cycle", "由模型根据完整问题选择指标、模板和日期，程序校验后计算", "往期后续是历史参照，不是预测"} {
		require.Contains(t, tool.Description(), text)
	}
}

type commodityResearchRoundTripper func(*http.Request) (*http.Response, error)

func (f commodityResearchRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCommodityResearchChartReadsSignCurrentScopeAndParameters(t *testing.T) {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 99)
	scope := types.ResearchWorkbenchScope{ID: "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67", TenantID: 7,
		UserID: "reader", ScopeVersion: 2, SourcesRevision: 7}
	ctx = types.WithResearchWorkbenchScope(ctx, scope)
	ctx = types.WithResearchAuthorization(ctx, "Bearer synthetic-current-token")
	key := []byte(strings.Repeat("b", 48))
	for _, args := range []json.RawMessage{
		json.RawMessage(`{"operation":"charts","search":"共享分组","series_id":"kb-fixture","limit":1,"offset":2}`),
		json.RawMessage(`{"operation":"cycle","series_id":"kb-fixture","date":"2024-01-02"}`),
		json.RawMessage(`{"operation":"cycle","series_id":"kb-fixture","template":"自然年"}`),
		json.RawMessage(`{"operation":"cycle","series_id":"kb-fixture","template":"saved-template-id"}`),
		json.RawMessage(`{"operation":"cycle","series_id":"kb-fixture","template":"` + strings.Repeat("字", 80) + `"}`),
	} {
		t.Run(string(args), func(t *testing.T) {
			called := false
			tool := NewCommodityResearchTool()
			tool.endpoint, tool.key = "http://fixture.invalid", key
			tool.client = &http.Client{Transport: commodityResearchRoundTripper(func(request *http.Request) (*http.Response, error) {
				called = true
				require.Equal(t, "POST", request.Method)
				require.Equal(t, "/api/tools/read", request.URL.Path)
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				require.Equal(t, []byte(args), body)
				encoded := request.Header.Get("X-Research-Identity")
				raw, err := base64.RawURLEncoding.DecodeString(encoded)
				require.NoError(t, err)
				var identity map[string]any
				require.NoError(t, json.Unmarshal(raw, &identity))
				require.Equal(t, "reader", identity["user_id"])
				require.Equal(t, float64(7), identity["tenant_id"])
				require.Equal(t, "viewer", identity["role"])
				require.Equal(t, scope.ID, identity["workbench_id"])
				require.Equal(t, float64(2), identity["scope_version"])
				require.Equal(t, float64(7), identity["sources_revision"])
				require.Equal(t, "Bearer synthetic-current-token", request.Header.Get("X-Research-Authorization"))
				authorizationHash := sha256.Sum256([]byte("Bearer synthetic-current-token"))
				require.Equal(t, hex.EncodeToString(authorizationHash[:]), identity["authorization_sha256"])
				bodyHash := sha256.Sum256(body)
				mac := hmac.New(sha256.New, key)
				mac.Write([]byte(strings.Join([]string{request.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", encoded, hex.EncodeToString(bodyHash[:])}, "\n")))
				require.Equal(t, hex.EncodeToString(mac.Sum(nil)), request.Header.Get("X-Research-Signature"))
				return &http.Response{StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{"notice":"往期后续是历史参照，不是预测"}`))}, nil
			})}
			result, err := tool.Execute(ctx, args)
			require.NoError(t, err)
			require.True(t, called)
			require.True(t, result.Success)
			require.Contains(t, result.Output, "往期后续是历史参照，不是预测")
		})
	}
}

func TestCommodityResearchRejectsInvalidTemplateBeforeRequest(t *testing.T) {
	tool := NewCommodityResearchTool()
	tool.endpoint, tool.key = "http://fixture.invalid", []byte(strings.Repeat("a", 48))
	tool.client = &http.Client{Transport: commodityResearchRoundTripper(func(request *http.Request) (*http.Response, error) {
		t.Fatal("invalid template must be rejected before sending a request")
		return nil, nil
	})}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	for _, value := range []string{`null`, `123`, `true`, `[]`, `{}`, `"` + strings.Repeat("字", 81) + `"`, `"` + strings.Repeat("a", 81) + `"`} {
		_, err := tool.Execute(ctx, json.RawMessage(`{"operation":"cycle","series_id":"kb-fixture","template":`+value+`}`))
		require.ErrorContains(t, err, "最长80字的字符串")
	}
	_, err := tool.Execute(ctx, json.RawMessage(`{"operation":"cycle","template":"自然年","tenant_id":99}`))
	require.ErrorContains(t, err, "未声明")
}
