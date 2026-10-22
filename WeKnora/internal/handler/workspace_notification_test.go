package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWorkspaceNotificationHTTPScopesCallerAndMarksRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notifications.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.TenantMember{}, &types.WorkspaceNotification{}))
	require.NoError(t, db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY, username TEXT);
INSERT INTO users VALUES('actor','Actual operator')`).Error)
	require.NoError(t, db.Create(&types.TenantMember{TenantID: 7, UserID: "recipient", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive}).Error)
	row := &types.WorkspaceNotification{TenantID: 7, RecipientUserID: "recipient", AuditLogID: 1,
		ActorUserID: "actor", Action: types.AuditActionMemberAdded, TargetUserID: "new-member", CreatedAt: time.Now()}
	require.NoError(t, db.Create(row).Error)
	h := NewWorkspaceNotificationHandler(service.NewWorkspaceNotificationService(db))
	r := gin.New()
	r.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = types.TaskInitiator{UserID: "recipient", Role: types.TenantRoleAdmin}.Apply(ctx)
		c.Request = c.Request.WithContext(ctx)
	})
	r.GET("/tenants/:id/notifications", h.List)
	r.PUT("/tenants/:id/notifications/:notification_id/read", h.MarkRead)
	request := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	w := request(http.MethodGet, "/tenants/7/notifications")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Success     bool             `json:"success"`
		Data        []map[string]any `json:"data"`
		UnreadCount int64            `json:"unread_count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.True(t, body.Success)
	require.Equal(t, int64(1), body.UnreadCount)
	require.Len(t, body.Data, 1)
	require.NotContains(t, body.Data[0], "recipient_user_id")
	readURL := fmt.Sprintf("/tenants/7/notifications/%d/read", row.ID)
	require.Equal(t, http.StatusOK, request(http.MethodPut, readURL).Code)
	require.Equal(t, http.StatusOK, request(http.MethodPut, readURL).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodPut, "/tenants/7/notifications/999/read").Code)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/tenants/7/notifications/bad/read").Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/tenants/8/notifications").Code)
	require.NoError(t, db.Model(&types.TenantMember{}).Where("user_id = ?", "recipient").Update("status", "suspended").Error)
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/tenants/7/notifications").Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodPut, readURL).Code)
}
