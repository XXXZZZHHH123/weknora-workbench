package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type workspaceHandlerKBs struct {
	interfaces.KnowledgeBaseService
}

func (*workspaceHandlerKBs) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if id != "kb" {
		return nil, gorm.ErrRecordNotFound
	}
	return &types.KnowledgeBase{ID: id, TenantID: 1, Name: "Library"}, nil
}

func TestWorkspaceHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "http.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	t.Cleanup(func() { sql.Close() })
	if err = db.Exec("CREATE TABLE sessions(id TEXT PRIMARY KEY, tenant_id INTEGER,user_id TEXT,updated_at DATETIME,deleted_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sqlite/000038_workspace_navigation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(string(migration)).Error; err != nil {
		t.Fatal(err)
	}
	h := NewWorkspaceHandler(service.NewWorkspaceService(db, &workspaceHandlerKBs{}, nil))
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") != "" {
			ctx := types.WithCaller(c.Request.Context(), types.Caller{UserID: c.GetHeader("X-Test-User"), TenantID: 1, Role: types.TenantRoleContributor})
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	})
	r.GET("/projects", h.ListProjects)
	r.POST("/projects", h.CreateProject)
	r.GET("/projects/:id", h.GetProject)
	r.GET("/projects/:id/sessions", h.ProjectSessions)
	r.POST("/research", h.CreateWorkbench)
	request := func(method, path, body, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := request("GET", "/projects", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{"name":"P","tenant_id":2}`, `{"name":"P","user_id":"other"}`, `{"name":"P"} {}`, `{"name":"P","description":"` + strings.Repeat("x", 17000) + `"}`} {
		if w := request("POST", "/projects", body, "alice"); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request("POST", "/projects", `{"name":"P","knowledge_base_id":"kb"}`, "alice")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Data types.AssistantProject `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.ID == "" || result.Data.Name != "P" {
		t.Fatal(result)
	}
	if w = request("GET", "/projects/"+result.Data.ID, "", "bob"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/projects/"+result.Data.ID+"/sessions?page=abc", "", "alice"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/projects/"+result.Data.ID+"/sessions", "", "alice")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("POST", "/research", `{"name":"W","knowledge_base_id":"kb","idempotency_key":"once"}`, "alice")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "idempotency_key") || strings.Contains(w.Body.String(), "request_hash") || !strings.Contains(w.Body.String(), `"knowledge_base_name":"Library"`) {
		t.Fatal(w.Body.String())
	}
}
