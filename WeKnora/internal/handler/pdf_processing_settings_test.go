package handler

import (
	"context"
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPDFProcessingAdminSaveDoesNotExposeOrReplaceEngineSecrets(t *testing.T) {
	tenant := secretTenantFixture()
	engine := newTenantHandlerTestEngine(t, types.TenantRoleAdmin, tenant)
	original := *tenant.ParserEngineConfig
	p := types.DefaultPDFProcessingSettings()
	p.OverlapPages = 2
	p.GroupingRequestBytes = 36000
	raw, _ := json.Marshal(p)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/tenants/kv/pdf-processing-settings", strings.NewReader(string(raw)))
	engine.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, p, *tenant.ParserEngineConfig.PDFProcessing)
	require.Equal(t, original.MinerUAPIKey, tenant.ParserEngineConfig.MinerUAPIKey)
	require.Equal(t, original.MinerUEndpoint, tenant.ParserEngineConfig.MinerUEndpoint)
	require.NotContains(t, w.Body.String(), original.MinerUAPIKey)
}
func TestPDFProcessingNonAdminsReadEffectivePolicyButCannotControlIt(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor} {
		tenant := secretTenantFixture()
		engine := newTenantHandlerTestEngine(t, role, tenant)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tenants/kv/pdf-processing-settings", nil))
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), `"can_edit":false`)
		p := types.DefaultPDFProcessingSettings()
		raw, _ := json.Marshal(p)
		w = httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/tenants/kv/pdf-processing-settings", strings.NewReader(string(raw))))
		require.Equal(t, 403, w.Code)
		require.Nil(t, tenant.ParserEngineConfig.PDFProcessing)
	}
}
func TestPDFProcessingRejectsInvalidAndExtraSettings(t *testing.T) {
	for _, body := range []string{`{}`, `{"overlap_pages":0,"grouping_request_bytes":24000,"queue_timeout_seconds":86400,"execution_timeout_seconds":7200}`, `{"overlap_pages":1,"grouping_request_bytes":24000,"queue_timeout_seconds":86400,"execution_timeout_seconds":7200,"api_key":"untrusted"}`, `{"overlap_pages":1.5}`, `{} {}`} {
		tenant := secretTenantFixture()
		engine := newTenantHandlerTestEngine(t, types.TenantRoleAdmin, tenant)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/tenants/kv/pdf-processing-settings", strings.NewReader(body)))
		require.Equal(t, 400, w.Code, w.Body.String())
		require.Nil(t, tenant.ParserEngineConfig.PDFProcessing)
	}
}

func TestPDFProcessingSharedKBUsesOwnerPolicyOnlyAfterAccessCheck(t *testing.T) {
	caller := secretTenantFixture()
	caller.ID = 1
	owner := secretTenantFixture()
	owner.ID = 2
	policy := types.DefaultPDFProcessingSettings()
	policy.OverlapPages = 3
	owner.ParserEngineConfig.PDFProcessing = &policy
	kb := &types.KnowledgeBase{ID: "shared-kb", TenantID: owner.ID}
	shares := &handlerKBShareStub{}
	h := &TenantHandler{service: &stubTenantService{tenant: owner},
		kbService:      &stubKBOnlyService{getByID: func(context.Context, string) (*types.KnowledgeBase, error) { return kb, nil }},
		kbShareService: shares}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, caller.ID)
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, caller)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.GET("/policy", h.GetPDFProcessingSettings)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/policy", nil)
		r.Header.Set("X-Knowledge-Base-ID", kb.ID)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	w := request()
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"overlap_pages":3`)
	require.Contains(t, w.Body.String(), `"can_edit":false`)
	require.NotContains(t, w.Body.String(), owner.ParserEngineConfig.MinerUAPIKey)
	require.Equal(t, 1, shares.calls)
	h.kbShareService = nil
	w = request()
	require.Equal(t, 403, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), `"overlap_pages"`)
}
