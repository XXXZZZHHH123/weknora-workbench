package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type versionUploadKnowledge struct {
	interfaces.KnowledgeService
	row          *types.Knowledge
	usedGrant    bool
	uploadedBody string
}

func (s *versionUploadKnowledge) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return s.row, nil
}

func (s *versionUploadKnowledge) ReplaceKnowledgeFile(ctx context.Context, id string, uploaded *multipart.FileHeader, _ string, _ map[string]string) (*types.Knowledge, error) {
	s.usedGrant = access.HasKBGrant(ctx, s.row.KnowledgeBaseID, s.row.TenantID, types.OrgRoleEditor)
	file, err := uploaded.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	s.uploadedBody = string(data)
	updated := *s.row
	updated.ID = id
	updated.FileHash = "new hash"
	return &updated, nil
}

func TestUploadKnowledgeFileVersionChecksScopeAndUploadLimit(t *testing.T) {
	t.Setenv("MAX_FILE_SIZE_MB", "1")
	tests := []struct {
		name string
		role types.TenantRole
		kbID string
		data string
		want int
	}{
		{"write", types.TenantRoleAdmin, "kb", "new content", http.StatusOK},
		{"read only", types.TenantRoleViewer, "kb", "new content", http.StatusForbidden},
		{"other knowledge base", types.TenantRoleAdmin, "other", "new content", http.StatusNotFound},
		{"too large", types.TenantRoleAdmin, "kb", strings.Repeat("x", (1<<20)+1), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kb := &types.KnowledgeBase{ID: "kb", TenantID: 7, CreatorID: "researcher", AccessPolicy: types.NewResourceAccessPolicy()}
			knowledge := &versionUploadKnowledge{row: &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: tt.kbID}}
			h := &KnowledgeHandler{kgService: knowledge, kbService: &downloadKBStub{kb: kb}}
			router := gin.New()
			router.Use(middleware.ErrorHandler(), func(c *gin.Context) {
				ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: 7, UserID: "researcher", Role: tt.role})
				c.Request = c.Request.WithContext(types.WithExecutionTenant(ctx, 7))
				c.Next()
			})
			router.POST("/knowledge-bases/:id/knowledge/:knowledge_id/file-version", h.UploadKnowledgeFileVersion)
			var body bytes.Buffer
			upload := multipart.NewWriter(&body)
			file, err := upload.CreateFormFile("file", "new.xlsx")
			require.NoError(t, err)
			_, err = io.WriteString(file, tt.data)
			require.NoError(t, err)
			require.NoError(t, upload.Close())
			req := httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb/knowledge/doc/file-version", &body)
			req.Header.Set("Content-Type", upload.FormDataContentType())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, tt.want, response.Code, response.Body.String())
			if tt.want == http.StatusOK {
				require.True(t, knowledge.usedGrant)
				require.Equal(t, tt.data, knowledge.uploadedBody)
				require.Contains(t, response.Body.String(), `"id":"doc"`)
			} else {
				require.Empty(t, knowledge.uploadedBody)
			}
		})
	}
}
