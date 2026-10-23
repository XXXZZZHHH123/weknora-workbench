package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type adminInvitationListService struct {
	interfaces.TenantInvitationService
}

func (*adminInvitationListService) ListTenantInvitationsPage(context.Context, uint64, bool, int, int) ([]*types.TenantInvitation, int64, error) {
	return []*types.TenantInvitation{
		{ID: 1, TenantID: 1, Role: types.TenantRoleOwner, Token: "owner-fixture-token", Status: types.TenantInvitationStatusPending},
		{ID: 2, TenantID: 1, Role: types.TenantRoleContributor, Token: "editor-fixture-token", Status: types.TenantInvitationStatusPending},
	}, 2, nil
}

func TestTenantInvitation_AdminLinkVisibilityProtectsOwnerGrants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleAdmin, types.TenantRoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			h := &TenantInvitationHandler{invitationService: &adminInvitationListService{}, userService: &stubMemberUserService{getByIDs: func(context.Context, []string) (map[string]*types.User, error) { return map[string]*types.User{}, nil }}}
			router := gin.New()
			router.Use(errorCapture())
			router.GET("/tenants/:id/invitations", h.ListTenantInvitations)
			req := httptest.NewRequest(http.MethodGet, "/tenants/1/invitations", nil)
			req = req.WithContext(types.WithCaller(req.Context(), types.Caller{TenantID: 1, UserID: "caller", Role: role}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			var response struct {
				Data struct {
					Invitations []types.TenantInvitationResponse `json:"invitations"`
				} `json:"data"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Data.Invitations) != 2 {
				t.Fatalf("unexpected response: code=%d %s", w.Code, w.Body.String())
			}
			if got := response.Data.Invitations[0].InviteURL != ""; got != (role == types.TenantRoleOwner) {
				t.Fatalf("owner invite URL visibility=%v for role %s", got, role)
			}
			if got := response.Data.Invitations[1].InviteURL != ""; got != role.HasPermission(types.TenantRoleAdmin) {
				t.Fatalf("non-owner invite URL visibility=%v for role %s", got, role)
			}
		})
	}
}

func TestTenantInvitation_AdminCannotCreateOwnerGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"invitations", "invite-links"} {
		t.Run(path, func(t *testing.T) {
			h := &TenantInvitationHandler{}
			router := gin.New()
			router.Use(errorCapture())
			router.POST("/tenants/:id/invitations", h.CreateInvitation)
			router.POST("/tenants/:id/invite-links", h.CreateInviteLink)
			req := httptest.NewRequest(http.MethodPost, "/tenants/1/"+path, strings.NewReader(`{"email":"person@example.test","role":"owner"}`))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(types.WithCaller(req.Context(), types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("code=%d, want 403: %s", w.Code, w.Body.String())
			}
		})
	}
}
