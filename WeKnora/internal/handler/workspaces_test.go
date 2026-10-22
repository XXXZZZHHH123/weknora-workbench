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
	return &types.KnowledgeBase{ID: id, TenantID: 1, Name: "Library", CreatorID: "alice"}, nil
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
	defaultMigration, err := os.ReadFile("../../migrations/sqlite/000040_default_knowledge_base.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(defaultMigration)).Error; err != nil {
		t.Fatal(err)
	}
	templatesMigration, err := os.ReadFile("../../migrations/sqlite/000041_research_workbench_templates.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(templatesMigration)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Message{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.KnowledgeBase{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.ResearchWorkbench{}, &types.UserWorkbenchPin{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&types.KnowledgeBase{ID: "kb", TenantID: 1, Name: "Library", CreatorID: "alice"}).Error; err != nil {
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
	r.GET("/research/templates", h.ListWorkbenchTemplates)
	r.GET("/research/:id", h.GetWorkbench)
	r.PUT("/research/:id", h.UpdateWorkbench)
	r.GET("/research/:id/permissions", h.GetWorkbenchPermissions)
	r.PUT("/research/:id/permissions", h.UpdateWorkbenchPermissions)
	r.POST("/research/:id/source-sharing/request", h.RequestWorkbenchSourceSharing)
	r.GET("/research/:id/source-sharing", h.GetWorkbenchSourceSharing)
	r.PUT("/research/:id/source-sharing", h.UpdateWorkbenchSourceSharing)
	r.PUT("/research/:id/onboarding", h.UpdateWorkbenchOnboarding)
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

	var research struct {
		Data types.ResearchWorkbench `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &research); err != nil {
		t.Fatal(err)
	}
	if research.Data.WorkspaceMode != "isolated" || research.Data.TemplateID != "blank" || research.Data.TemplateVersion != 1 || research.Data.ScopeTenantID != 1 {
		t.Fatal(w.Body.String())
	}
	for _, body := range []string{
		`{"name":"W","knowledge_base_id":"kb","idempotency_key":"bad","workspace_mode":"legacy"}`,
		`{"name":"W","knowledge_base_id":"kb","idempotency_key":"bad","scope_tenant_id":2}`,
		`{"name":"W","knowledge_base_id":"kb","idempotency_key":"bad","template_id":"unknown"}`,
		`{"name":"W","knowledge_base_id":"kb","idempotency_key":"bad","template_version":2}`,
	} {
		if response := request("POST", "/research", body, "alice"); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if response := request("POST", "/research", `{"name":"Template","knowledge_base_id":"kb","idempotency_key":"template","template_id":"industrial","template_version":1}`, "alice"); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("GET", "/research/templates", "", "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"modules"`) || !strings.Contains(response.Body.String(), `"data-tracking"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	path := "/research/" + research.Data.ID + "/onboarding"
	permissionPath := "/research/" + research.Data.ID + "/permissions"
	if response := request("GET", permissionPath, "", "bob"); response.Code != 404 {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, body := range []string{`null`, `{}`, `{"space_permission":"owner","members":[]}`, `{"space_permission":"none","members":[],"user_id":"bob"}`, `{"space_permission":"none","members":[]} {}`} {
		if response := request("PUT", permissionPath, body, "alice"); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if response := request("PUT", permissionPath, `{"space_permission":"viewer","members":[]}`, "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"owner_id":"alice"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("GET", permissionPath, "", "bob"); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("POST", "/research/"+research.Data.ID+"/source-sharing/request", "", "bob"); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	sharingPath := "/research/" + research.Data.ID + "/source-sharing"
	for _, body := range []string{`{}`, `{"approved":true}`, `{"approved":true,"modules":[]}`, `{"approved":true,"modules":["market","market"]}`, `{"approved":true,"modules":["all"]}`, `{"approved":true,"modules":["market"],"user_id":"bob"}`, `{"approved":true,"modules":["market"]} {}`} {
		if response := request("PUT", sharingPath, body, "alice"); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if response := request("PUT", sharingPath, `{"approved":true,"modules":["market","diagram"]}`, "bob"); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("PUT", sharingPath, `{"approved":true,"modules":["market","diagram"]}`, "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"source_sharing_modules":["market","diagram"]`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("GET", sharingPath, "", "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"source_sharing_modules":["market","diagram"]`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("PUT", sharingPath, `{"approved":false}`, "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"source_sharing_modules":[]`) {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, body := range []string{
		`{"space_permission":"viewer","members":[],"restricted_view_profiles":[{"id":"view","name":"View","modules":["diagram"],"member_ids":[],"all_members":true,"can_manage":true}]}`,
		`{"space_permission":"viewer","members":[],"restricted_view_profiles":[{"id":"view","name":"View","modules":["unknown"],"member_ids":[],"all_members":true}]}`,
		`{"space_permission":"viewer","members":[],"restricted_view_profiles":[],"source_sharing_approved":true}`,
		`{"space_permission":"viewer","members":[],"restricted_view_profiles":[],"source_sharing_approved":"true"}`,
	} {
		if response := request("PUT", permissionPath, body, "alice"); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	profileBody := `{"space_permission":"viewer","members":[],"restricted_view_profiles":[{"id":"view","name":"View","modules":["diagram"],"member_ids":[],"all_members":true}],"source_sharing_approved":true}`
	if response := request("PUT", permissionPath, profileBody, "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"source_sharing_modules":["diagram"]`) || !strings.Contains(response.Body.String(), `"restricted_view_profiles":[{"id":"view"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("GET", "/research/"+research.Data.ID, "", "bob"); response.Code != 200 || strings.Contains(response.Body.String(), "member_ids") || strings.Contains(response.Body.String(), "restricted_view_profiles") {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("PUT", path, `{"status":"dismissed","tasks":{"sources":true}}`, "bob"); response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"dismissed"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := request("GET", "/research/"+research.Data.ID, "", "alice"); response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"active"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, body := range []string{`{"user_id":"alice","status":"skipped"}`, `{"status":"unknown"}`, `{"tasks":{"ready":true}}`, `{"status":"active"} {}`, `{}`} {
		if response := request("PUT", path, body, "bob"); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if response := request("PUT", "/research/"+research.Data.ID, `{"template_id":"topic"}`, "alice"); response.Code != 400 {
		t.Fatal(response.Code, response.Body.String())
	}
}
