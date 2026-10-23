package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestTenantMember_AdminCannotDirectlyAssignOwner(t *testing.T) {
	added := false
	ms := &stubMemberService{add: func(_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string) (*types.TenantMember, error) {
		added = true
		return &types.TenantMember{UserID: userID, TenantID: tenantID, Role: role}, nil
	}}
	us := &stubMemberUserService{getByEmail: func(_ context.Context, _ string) (*types.User, error) { return &types.User{ID: "new-user"}, nil }}
	router := memberTestRouter(newTestMemberHandler(ms, us))
	req := httptest.NewRequest(http.MethodPost, "/tenants/1/members", strings.NewReader(`{"email":"new@example.test","role":"owner"}`))
	req.Header.Set("Content-Type", "application/json")
	req = withMemberCtx(req, memberCtxOpts{callerID: "admin"})
	req = req.WithContext(types.WithCaller(req.Context(), types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || added {
		t.Fatalf("code=%d added=%v, want 403 without mutation: %s", w.Code, added, w.Body.String())
	}
}
