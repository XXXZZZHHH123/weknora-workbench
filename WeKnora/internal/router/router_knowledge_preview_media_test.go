package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type previewMediaKnowledgeService struct {
	interfaces.KnowledgeService
	knowledge      *types.Knowledge
	renderCalls    int
	renderTenant   uint64
	renderCaller   uint64
	renderedPage   int
	renderedPart   string
	renderedSource string
	renderErr      error
}

func (s *previewMediaKnowledgeService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	if s.knowledge == nil || s.knowledge.ID != id {
		return nil, apprepo.ErrKnowledgeNotFound
	}
	return s.knowledge, nil
}

func (s *previewMediaKnowledgeService) RenderKnowledgePreviewMedia(ctx context.Context, _ *types.Knowledge, page int, part, sourceSHA string) ([]byte, error) {
	s.renderCalls++
	s.renderTenant = types.MustTenantIDFromContext(ctx)
	s.renderCaller = types.CallerFromContext(ctx).TenantID
	s.renderedPage, s.renderedPart, s.renderedSource = page, part, sourceSHA
	return []byte("PNG test fixture"), s.renderErr
}

func TestKnowledgePreviewMediaPreservesTypedClientErrorsAndHidesInternalDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"selector", apperrors.NewBadRequestError("Invalid media selector"), http.StatusBadRequest},
		{"source changed", apperrors.NewConflictError("Reload the source"), http.StatusConflict},
		{"internal", errors.New("private storage details"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, service := newKnowledgePreviewMediaRouteTestEngine(t, types.TenantRoleViewer,
				&types.Knowledge{ID: "doc", TenantID: 1, KnowledgeBaseID: "kb"}, &types.KnowledgeBase{ID: "kb", TenantID: 1}, nil, nil)
			service.renderErr = tc.err
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge/doc/preview-media?page=1&part=ppt%2Fmedia%2Fplot.emf&source_sha256="+strings.Repeat("a", 64), nil))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "private storage details")
		})
	}
}

func newKnowledgePreviewMediaRouteTestEngine(
	t *testing.T,
	role types.TenantRole,
	knowledge *types.Knowledge,
	kb *types.KnowledgeBase,
	share interfaces.KBShareService,
	scope *types.TenantAPIKeyScope,
) (*gin.Engine, *previewMediaKnowledgeService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	enabled := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}
	service := &previewMediaKnowledgeService{knowledge: knowledge}
	guards := &rbacGuards{
		cfg:              cfg,
		knowledgeService: service,
		kbService:        &stubWikiKBLookup{kbs: map[string]*types.KnowledgeBase{kb.ID: kb}},
		kbShareService:   share,
	}
	r := gin.New()
	r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: 1, UserID: "preview-user", Role: role})
		ctx = types.WithExecutionTenant(ctx, 1)
		if scope != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *scope)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Next()
	})
	knowledgeHandler := handler.NewKnowledgeHandler(
		cfg, service, nil, share, nil, nil, nil, nil, nil, nil, nil,
	)
	RegisterKnowledgeRoutes(r.Group("/api/v1"), knowledgeHandler, guards)
	return r, service
}

func TestKnowledgePreviewMediaReturnsPNGForAuthorizedViewer(t *testing.T) {
	engine, service := newKnowledgePreviewMediaRouteTestEngine(t, types.TenantRoleViewer,
		&types.Knowledge{ID: "doc", TenantID: 1, KnowledgeBaseID: "kb"},
		&types.KnowledgeBase{ID: "kb", TenantID: 1}, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/knowledge/doc/preview-media?page=4&part=ppt%2Fmedia%2Fplot.emf&source_sha256="+strings.Repeat("a", 64), nil)
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "PNG test fixture", rec.Body.String())
	require.Equal(t, 1, service.renderCalls)
	require.Equal(t, uint64(1), service.renderTenant)
	require.Equal(t, uint64(1), service.renderCaller)
	require.Equal(t, 4, service.renderedPage)
	require.Equal(t, "ppt/media/plot.emf", service.renderedPart)
	require.Equal(t, strings.Repeat("a", 64), service.renderedSource)
}

func TestKnowledgePreviewMediaRejectsMissingOrMalformedSourceSHA(t *testing.T) {
	for _, query := range []string{
		"page=1&part=ppt%2Fmedia%2Fa.emf",
		"page=1&part=ppt%2Fmedia%2Fa.emf&source_sha256=not-a-hash",
		"page=1&part=ppt%2Fmedia%2Fa.emf&source_sha256=" + strings.Repeat("g", 64),
	} {
		t.Run(query, func(t *testing.T) {
			engine, service := newKnowledgePreviewMediaRouteTestEngine(t, types.TenantRoleViewer,
				&types.Knowledge{ID: "doc", TenantID: 1, KnowledgeBaseID: "kb"},
				&types.KnowledgeBase{ID: "kb", TenantID: 1}, nil, nil)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge/doc/preview-media?"+query, nil))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, service.renderCalls)
		})
	}
}

func TestKnowledgePreviewMediaRejectsUnauthorizedAndMissingKnowledgeBeforeRender(t *testing.T) {
	cases := []struct {
		name      string
		knowledge *types.Knowledge
		kb        *types.KnowledgeBase
		status    int
	}{
		{
			name:      "cross tenant without share",
			knowledge: &types.Knowledge{ID: "doc", TenantID: 2, KnowledgeBaseID: "kb-other"},
			kb:        &types.KnowledgeBase{ID: "kb-other", TenantID: 2},
			status:    http.StatusForbidden,
		},
		{
			name:      "unknown knowledge",
			knowledge: nil,
			kb:        &types.KnowledgeBase{ID: "kb", TenantID: 1},
			status:    http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, service := newKnowledgePreviewMediaRouteTestEngine(t, types.TenantRoleViewer,
				tc.knowledge, tc.kb, nil, nil)
			rec := httptest.NewRecorder()
			url := "/api/v1/knowledge/doc/preview-media?page=1&part=ppt%2Fmedia%2Fa.emf&source_sha256=" + strings.Repeat("b", 64)
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Zero(t, service.renderCalls)
		})
	}
}

func TestKnowledgePreviewMediaUsesEffectiveTenantForOrgSharedViewer(t *testing.T) {
	engine, service := newKnowledgePreviewMediaRouteTestEngine(t, types.TenantRoleViewer,
		&types.Knowledge{ID: "shared-doc", TenantID: 2, KnowledgeBaseID: "shared-kb"},
		&types.KnowledgeBase{ID: "shared-kb", TenantID: 2},
		&downloadKBShareStub{permission: types.OrgRoleViewer, source: 2}, nil)
	rec := httptest.NewRecorder()
	url := "/api/v1/knowledge/shared-doc/preview-media?page=2&part=ppt%2Fmedia%2Fshared.wmf&source_sha256=" + strings.Repeat("c", 64)
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, service.renderCalls)
	require.Equal(t, uint64(2), service.renderTenant, "renderer must read from the shared KB owner tenant")
	require.Equal(t, uint64(1), service.renderCaller, "effective tenant must not replace the caller identity")
}

func TestKnowledgePreviewMediaAPIKeyRetrieveStillHonorsKBAllowList(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowedKB string
		status    int
	}{
		{name: "allow-listed KB", allowedKB: "kb", status: http.StatusOK},
		{name: "outside allow-list", allowedKB: "another-kb", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &previewMediaKnowledgeService{
				knowledge: &types.Knowledge{ID: "doc", TenantID: 1, KnowledgeBaseID: "kb"},
			}
			enabled := true
			cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}
			authorizer := middleware.NewAPIKeyRouteAuthorizer()
			// RegisterKnowledgeRoutes records its API-key policy on this same authorizer.
			guards := &rbacGuards{cfg: cfg,
				knowledgeService: service,
				kbService:        &stubWikiKBLookup{kbs: map[string]*types.KnowledgeBase{"kb": &types.KnowledgeBase{ID: "kb", TenantID: 1}}}}
			guards.apiKeyAuthorizer = authorizer
			r := gin.New()
			r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
				ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: 1, UserID: "key-user", Role: types.TenantRoleViewer})
				ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 4,
					Capabilities:     types.StringArray{string(types.APIKeyCapabilityRetrieve)},
					KnowledgeBaseIDs: types.StringArray{tc.allowedKB}})
				c.Request = c.Request.WithContext(ctx)
				c.Set(types.TenantIDContextKey.String(), uint64(1))
				c.Next()
			}, authorizer.Middleware())
			knowledgeHandler := handler.NewKnowledgeHandler(
				cfg, service, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			RegisterKnowledgeRoutes(r.Group("/api/v1"), knowledgeHandler, guards)
			rec := httptest.NewRecorder()
			url := "/api/v1/knowledge/doc/preview-media?page=1&part=ppt%2Fmedia%2Fa.emf&source_sha256=" + strings.Repeat("d", 64)
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusOK {
				require.Equal(t, 1, service.renderCalls)
			} else {
				require.Zero(t, service.renderCalls)
			}
		})
	}
}
