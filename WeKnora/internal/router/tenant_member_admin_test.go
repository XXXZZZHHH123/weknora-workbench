package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestTenantMemberManagementGuardRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enabled := true
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleAdmin, types.TenantRoleContributor, types.TenantRoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}}
			router := gin.New()
			router.POST("/members", g.MemberManagement(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodPost, "/members", nil)
			req = req.WithContext(types.WithCaller(req.Context(), types.Caller{TenantID: 1, UserID: "user", Role: role}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			want := http.StatusForbidden
			if role.HasPermission(types.TenantRoleAdmin) {
				want = http.StatusNoContent
			}
			if w.Code != want {
				t.Fatalf("code=%d want=%d: %s", w.Code, want, w.Body.String())
			}
		})
	}
}

func TestTenantMemberManagementGuardOnlyConfiguredCrossTenantOperatorMayManageOwners(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enabled := true
	for _, crossTenantEnabled := range []bool{false, true} {
		g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled, EnableCrossTenantAccess: crossTenantEnabled}}}
		router := gin.New()
		router.POST("/members", g.MemberManagement(), func(c *gin.Context) {
			if got := types.CanManageTenantOwners(c.Request.Context()); got != crossTenantEnabled {
				t.Errorf("owner access=%v, cross-tenant enabled=%v", got, crossTenantEnabled)
			}
			if got := types.CallerFromContext(c.Request.Context()).Role; got != types.TenantRoleAdmin {
				t.Errorf("caller role unexpectedly changed: %s", got)
			}
			c.Status(http.StatusNoContent)
		})
		ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "operator", Role: types.TenantRoleAdmin})
		ctx = context.WithValue(ctx, types.UserContextKey, &types.User{ID: "operator", CanAccessAllTenants: true})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/members", nil).WithContext(ctx))
		if w.Code != http.StatusNoContent {
			t.Fatalf("code=%d", w.Code)
		}
	}
}
