package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type aclRouteKB struct{ kb *types.KnowledgeBase }

func (f aclRouteKB) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return f.kb, nil
}

type aclRouteShares struct{ interfaces.KBShareService }

func (aclRouteShares) CheckTenantKBPermission(context.Context, string, uint64, types.TenantRole) (types.OrgMemberRole, bool, error) {
	return types.OrgRoleEditor, true, nil
}

func TestResourceContentAndOwnerHTTPGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &types.KnowledgeBase{ID: "library", TenantID: 7, CreatorID: "owner", AccessPolicy: &types.ResourceAccessPolicy{
		SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "editor", Permission: "editor"}, {UserID: "reader", Permission: "viewer"}},
	}}
	for _, enabled := range []bool{true, false} {
		for _, tc := range []struct {
			user   string
			role   types.TenantRole
			tenant uint64
			manage bool
			want   int
		}{
			{"owner", types.TenantRoleContributor, 7, false, 204},
			{"owner", types.TenantRoleContributor, 7, true, 204},
			{"owner", types.TenantRoleViewer, 7, true, 403},
			{"editor", types.TenantRoleContributor, 7, false, 204},
			{"editor", types.TenantRoleContributor, 7, true, 403},
			{"editor", types.TenantRoleViewer, 7, false, 403},
			{"reader", types.TenantRoleAdmin, 7, false, 403},
			{"unlisted", types.TenantRoleAdmin, 7, false, 403},
			{"unlisted", types.TenantRoleOwner, 7, true, 403},
			{"foreign-editor", types.TenantRoleContributor, 8, false, 204},
			{"foreign-editor", types.TenantRoleContributor, 8, true, 403},
		} {
			g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}, kbService: aclRouteKB{kb}, kbShareService: aclRouteShares{}}
			r := gin.New()
			r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
				c.Request = c.Request.WithContext(types.WithCaller(c.Request.Context(), types.Caller{TenantID: tc.tenant, UserID: tc.user, Role: tc.role}))
				c.Next()
			})
			r.PUT("/knowledge-bases/:id", g.kbPolicyGuard(middleware.KBIDFromParam("id"), nil, tc.manage), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/knowledge-bases/library", nil))
			require.Equal(t, tc.want, w.Code, "%s role=%s manage=%v rbac=%v body=%s", tc.user, tc.role, tc.manage, enabled, w.Body.String())
		}
	}
}
