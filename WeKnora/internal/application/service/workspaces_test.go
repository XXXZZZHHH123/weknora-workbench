package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type workspaceKBFixture struct {
	interfaces.KnowledgeBaseService
	rows map[string]*types.KnowledgeBase
}

func (f *workspaceKBFixture) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	row := f.rows[id]
	if row == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return row, nil
}

type workspaceShareFixture struct {
	interfaces.KBShareService
	allowed bool
}

func (f *workspaceShareFixture) CheckTenantKBPermission(_ context.Context, id string, tenant uint64, _ types.TenantRole) (types.OrgMemberRole, bool, error) {
	return types.OrgRoleViewer, f.allowed && id == "shared" && tenant == 1, nil
}
func workspaceContext(user string, tenant uint64, role types.TenantRole) context.Context {
	return types.WithCaller(context.Background(), types.Caller{UserID: user, TenantID: tenant, Role: role})
}
func workspaceString(s string) *string { return &s }
func workspaceStatus(t *testing.T, err error, status int) {
	t.Helper()
	var app *apperrors.AppError
	if !errors.As(err, &app) || app.HTTPCode != status {
		t.Fatalf("error = %v; want HTTP %d", err, status)
	}
}
func workspaceFixture(t *testing.T) (*WorkspaceService, *workspaceKBFixture, *workspaceShareFixture) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "workspace.db")+"?_foreign_keys=on&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	t.Cleanup(func() { sql.Close() })
	if err := db.Exec("CREATE TABLE sessions (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER, user_id VARCHAR(512), title TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Message{}); err != nil {
		t.Fatal(err)
	}
	// Exercise the shipped migration, including FK and uniqueness constraints.
	migration, err := os.ReadFile("../../../migrations/sqlite/000038_workspace_navigation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(migration)).Error; err != nil {
		t.Fatal(err)
	}
	defaultMigration, err := os.ReadFile("../../../migrations/sqlite/000040_default_knowledge_base.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(defaultMigration)).Error; err != nil {
		t.Fatal(err)
	}
	templatesMigration, err := os.ReadFile("../../../migrations/sqlite/000041_research_workbench_templates.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(templatesMigration)).Error; err != nil {
		t.Fatal(err)
	}
	kbs := &workspaceKBFixture{rows: map[string]*types.KnowledgeBase{"own": {ID: "own", TenantID: 1, Name: "Library"}, "foreign": {ID: "foreign", TenantID: 2}, "shared": {ID: "shared", TenantID: 2}}}
	if err := db.AutoMigrate(&types.KnowledgeBase{}); err != nil {
		t.Fatal(err)
	}
	for _, kb := range kbs.rows {
		if err := db.Create(kb).Error; err != nil {
			t.Fatal(err)
		}
	}
	shares := &workspaceShareFixture{}
	return NewWorkspaceService(db, kbs, shares), kbs, shares
}
func TestWorkspaceProjectsOwnershipAndSessions(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	alice := workspaceContext("alice", 1, types.TenantRoleContributor)
	bob := workspaceContext("bob", 1, types.TenantRoleAdmin)
	other := workspaceContext("alice", 2, types.TenantRoleOwner)
	p, err := s.CreateProject(alice, WorkspaceInput{Name: workspaceString(" Project "), KnowledgeBaseID: workspaceString("own")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Project" {
		t.Fatal(p)
	}
	for _, ctx := range []context.Context{bob, other} {
		rows, err := s.ListProjects(ctx)
		if err != nil || len(rows) != 0 {
			t.Fatalf("private list leaked: %v %v", rows, err)
		}
		_, err = s.GetProject(ctx, p.ID)
		workspaceStatus(t, err, 404)
		_, err = s.UpdateProject(ctx, p.ID, WorkspaceInput{Name: workspaceString("stolen")})
		workspaceStatus(t, err, 404)
		workspaceStatus(t, s.DeleteProject(ctx, p.ID), 404)
	}
	for _, row := range []struct {
		id, user string
		tenant   int
	}{{"s1", "alice", 1}, {"s2", "bob", 1}, {"s3", "alice", 2}} {
		if err := s.db.Exec("INSERT INTO sessions(id,tenant_id,user_id,title,created_at,updated_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", row.id, row.tenant, row.user, row.id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&types.Message{SessionID: "s1", Role: "user", Content: "project fixture question"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s2", "s3", "missing"} {
		workspaceStatus(t, s.AssignSession(alice, p.ID, id, false), 404)
	}
	if err = s.AssignSession(alice, p.ID, "s1", false); err != nil {
		t.Fatal(err)
	}
	if err = s.AssignSession(alice, p.ID, "s1", false); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.ProjectSessions(alice, p.ID, 1, 20)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != "s1" {
		t.Fatalf("sessions: %v %d %v", rows, total, err)
	}
	loaded, err := s.GetProject(alice, p.ID)
	if err != nil || loaded.SessionCount != 1 {
		t.Fatalf("count %v %v", loaded, err)
	}
	p2, err := s.CreateProject(alice, WorkspaceInput{Name: workspaceString("Second")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AssignSession(alice, p2.ID, "s1", false); err != nil {
		t.Fatal(err)
	}
	_, total, err = s.ProjectSessions(alice, p.ID, 1, 20)
	if err != nil || total != 0 {
		t.Fatal("move duplicated conversation", err, total)
	}
	if err = s.DeleteProject(alice, p2.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.db.Table("sessions").Where("id = ?", "s1").Count(&count)
	if count != 1 {
		t.Fatal("deleting project deleted conversation")
	}
	_, _, err = s.ProjectSessions(alice, p.ID, 0, 20)
	workspaceStatus(t, err, 400)
	_, _, err = s.ProjectSessions(alice, p.ID, 1, 101)
	workspaceStatus(t, err, 400)
	if err = s.AssignSession(alice, p.ID, "s1", false); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("UPDATE sessions SET deleted_at=CURRENT_TIMESTAMP WHERE id='s1'").Error; err != nil {
		t.Fatal(err)
	}
	_, total, err = s.ProjectSessions(alice, p.ID, 1, 20)
	if err != nil || total != 0 {
		t.Fatal("soft deleted conversation visible", err, total)
	}
	workspaceStatus(t, s.AssignSession(alice, p.ID, "s1", false), 404)
}

func TestWorkspaceWorkbenchBindingAndIdempotency(t *testing.T) {
	s, kbs, shares := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	input := WorkspaceInput{Name: workspaceString("Research"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "request-1"}
	rows, err := s.ListWorkbenches(ctx, "")
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	w, err := s.CreateWorkbench(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateWorkbench(ctx, input)
	if err != nil || retry.ID != w.ID {
		t.Fatal(retry, err)
	}
	changed := input
	changed.Name = workspaceString("Changed")
	_, err = s.CreateWorkbench(ctx, changed)
	workspaceStatus(t, err, 409)
	second := input
	second.IdempotencyKey = "request-2"
	w2, err := s.CreateWorkbench(ctx, second)
	if err != nil || w2.ID == w.ID {
		t.Fatal("one KB must allow multiple workbenches", err)
	}
	rows, err = s.ListWorkbenches(ctx, "own")
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	_, err = s.GetWorkbench(workspaceContext("alice", 2, types.TenantRoleOwner), w.ID)
	workspaceStatus(t, err, 404)
	_, err = s.UpdateWorkbench(workspaceContext("bob", 1, types.TenantRoleContributor), w.ID, WorkspaceInput{Name: workspaceString("other")})
	workspaceStatus(t, err, 403)
	_, err = s.UpdateWorkbench(workspaceContext("bob", 1, types.TenantRoleAdmin), w.ID, WorkspaceInput{Name: workspaceString("Renamed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateWorkbench(workspaceContext("reader", 1, types.TenantRoleViewer), input)
	workspaceStatus(t, err, 403)
	foreign := input
	foreign.IdempotencyKey = "foreign-key"
	foreign.KnowledgeBaseID = workspaceString("foreign")
	_, err = s.CreateWorkbench(ctx, foreign)
	workspaceStatus(t, err, 404)
	shared := input
	shared.IdempotencyKey = "shared-key"
	shared.KnowledgeBaseID = workspaceString("shared")
	shares.allowed = true
	w3, err := s.CreateWorkbench(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	shares.allowed = false
	_, err = s.GetWorkbench(ctx, w3.ID)
	workspaceStatus(t, err, 404)
	rows, err = s.ListWorkbenches(ctx, "")
	if err != nil || len(rows) != 2 {
		t.Fatal("revoked share visible", rows, err)
	}
	if err = s.DeleteWorkbench(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateWorkbench(ctx, input)
	workspaceStatus(t, err, 409)
	delete(kbs.rows, "own")
	_, err = s.GetWorkbench(ctx, w2.ID)
	workspaceStatus(t, err, 404)
}

func TestWorkspaceConcurrentWorkbenchCreate(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	var wg sync.WaitGroup
	results := make(chan string, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Concurrent"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "same-request"})
			if err != nil {
				errs <- err
				return
			}
			results <- row.ID
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	id := ""
	for next := range results {
		if id != "" && id != next {
			t.Fatal("concurrent request created duplicate")
		}
		id = next
	}
	var count int64
	s.db.Model(&types.ResearchWorkbench{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
}

func TestWorkspaceValidationAndPersistence(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	_, err := s.ListProjects(context.Background())
	workspaceStatus(t, err, 401)
	for _, name := range []string{"", " \n", strings.Repeat("长", 129), "bad\x00name"} {
		_, err = s.CreateProject(ctx, WorkspaceInput{Name: workspaceString(name)})
		workspaceStatus(t, err, 400)
	}
	_, err = s.CreateProject(ctx, WorkspaceInput{Name: workspaceString("OK"), Description: workspaceString(strings.Repeat("x", 2001))})
	workspaceStatus(t, err, 400)
	_, err = s.CreateProject(ctx, WorkspaceInput{Name: workspaceString("OK"), KnowledgeBaseID: workspaceString("foreign")})
	workspaceStatus(t, err, 404)
	for _, input := range []WorkspaceInput{{Name: workspaceString("Name")}, {Name: workspaceString("Name"), KnowledgeBaseID: workspaceString("own")}, {Name: workspaceString("Name"), KnowledgeBaseID: workspaceString(""), IdempotencyKey: "k"}} {
		_, err = s.CreateWorkbench(ctx, input)
		workspaceStatus(t, err, 400)
	}
	p, err := s.CreateProject(ctx, WorkspaceInput{Name: workspaceString("Persistent")})
	if err != nil {
		t.Fatal(err)
	}
	s2 := NewWorkspaceService(s.db, s.kbs, s.shares)
	actual, err := s2.GetProject(ctx, p.ID)
	if err != nil || actual.Name != p.Name {
		t.Fatal(actual, err)
	}
	if _, err = s.UpdateProject(ctx, p.ID, WorkspaceInput{Description: workspaceString("new")}); err != nil {
		t.Fatal(err)
	}
	actual, err = s.GetProject(ctx, p.ID)
	if err != nil || actual.Name != p.Name || actual.Description != "new" {
		t.Fatal(actual, err)
	}
}
