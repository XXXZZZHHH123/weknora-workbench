package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type directoryDeleteServiceStub struct {
	interfaces.KnowledgeService
	calls     int
	recursive bool
	path      string
	err       error
}

func (s *directoryDeleteServiceStub) DeleteKnowledgeFolder(_ context.Context, _ string, path string, recursive ...bool) error {
	s.calls++
	s.path = path
	s.recursive = len(recursive) > 0 && recursive[0]
	return s.err
}

func newDirectoryDeleteRouter(stub *directoryDeleteServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	enabled := true
	h := &KnowledgeHandler{
		cfg:       &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}},
		kbService: &stubFolderKBService{}, kgService: stub,
	}
	router := gin.New()
	router.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "directory-editor")
		ctx = types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "directory-editor", Role: types.TenantRoleAdmin})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.DELETE("/knowledge-bases/:id/knowledge/folders", h.DeleteKnowledgeFolder)
	return router
}

func TestDeleteKnowledgeDirectoryRequiresExplicitRecursiveFlag(t *testing.T) {
	for _, tc := range []struct {
		query     string
		recursive bool
		status    int
	}{
		{query: "", status: http.StatusOK},
		{query: "&recursive=false", status: http.StatusOK},
		{query: "&recursive=true", recursive: true, status: http.StatusOK},
		{query: "&recursive=1", status: http.StatusBadRequest},
		{query: "&recursive=invalid", status: http.StatusBadRequest},
	} {
		t.Run(tc.query, func(t *testing.T) {
			stub := &directoryDeleteServiceStub{}
			router := newDirectoryDeleteRouter(stub)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/knowledge-bases/kb-1/knowledge/folders?path=A_%25/child"+tc.query, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusOK {
				require.Equal(t, 1, stub.calls)
				require.Equal(t, tc.recursive, stub.recursive)
				require.Equal(t, "A_%/child", stub.path)
			} else {
				require.Zero(t, stub.calls)
			}
		})
	}
}

func TestDeleteKnowledgeDirectoryIncompleteCleanupIsNeverSuccess(t *testing.T) {
	stub := &directoryDeleteServiceStub{err: apperrors.NewConflictError("文件夹删除未完成，可重试").WithDetails(map[string]any{"retryable": true})}
	router := newDirectoryDeleteRouter(stub)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/knowledge-bases/kb-1/knowledge/folders?path=Folder&recursive=true", nil))
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Details struct {
				Retryable bool `json:"retryable"`
			} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.False(t, body.Success)
	require.True(t, body.Error.Details.Retryable)
}
