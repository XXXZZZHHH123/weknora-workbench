package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestWorkspaceNewWorkbenchHasIsolatedTemplateContract(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	row, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("New research"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "isolated-contract"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err = json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if actual["workspace_mode"] != "isolated" || actual["template_id"] != "blank" || actual["template_version"] != float64(1) || actual["scope_tenant_id"] != float64(1) {
		t.Fatalf("new workbench must return isolated template and verified tenant scope: %s", encoded)
	}
}

func workspaceInt(value int) *int    { return &value }
func workspaceBool(value bool) *bool { return &value }

func TestWorkspaceTemplateCatalogAndValidation(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	catalog, err := s.ListWorkbenchTemplates(ctx)
	if err != nil || len(catalog) != 4 {
		t.Fatalf("catalog: %v %v", catalog, err)
	}
	for _, template := range catalog {
		input := WorkspaceInput{Name: workspaceString("Research"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: template.ID,
			TemplateID: workspaceString(template.ID), TemplateVersion: workspaceInt(1)}
		row, err := s.CreateWorkbench(ctx, input)
		if err != nil || row.TemplateID != template.ID || row.TemplateVersion != 1 || row.WorkspaceMode != types.ResearchWorkbenchModeIsolated {
			t.Fatalf("template create: %v %v", row, err)
		}
		retry, err := s.CreateWorkbench(ctx, input)
		if err != nil || retry.ID != row.ID {
			t.Fatalf("template retry: %v %v", retry, err)
		}
		input.TemplateID = workspaceString("blank")
		if template.ID != "blank" {
			_, err = s.CreateWorkbench(ctx, input)
			workspaceStatus(t, err, 409)
		}
		_, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{TemplateID: workspaceString("blank")})
		workspaceStatus(t, err, 400)
		_, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{TemplateVersion: workspaceInt(1)})
		workspaceStatus(t, err, 400)
	}
	for _, input := range []WorkspaceInput{
		{TemplateID: workspaceString("unknown")}, {TemplateID: workspaceString("")},
		{TemplateVersion: workspaceInt(0)}, {TemplateVersion: workspaceInt(-1)}, {TemplateVersion: workspaceInt(2)},
	} {
		input.Name, input.KnowledgeBaseID, input.IdempotencyKey = workspaceString("Invalid"), workspaceString("own"), "invalid"
		_, err := s.CreateWorkbench(ctx, input)
		workspaceStatus(t, err, 400)
	}
	_, err = s.ListWorkbenchTemplates(context.Background())
	workspaceStatus(t, err, 401)
	// Each response owns its slices; no caller can mutate the global catalog.
	catalog[0].Modules[0] = "modified"
	fresh, _ := s.ListWorkbenchTemplates(ctx)
	if fresh[0].Modules[0] == "modified" {
		t.Fatal("catalog mutation leaked between callers")
	}
}

func TestWorkspaceIsolatedBindingAndLegacyRetry(t *testing.T) {
	s, _, shares := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	shares.allowed = true
	input := WorkspaceInput{Name: workspaceString("Isolated"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "immutable"}
	row, err := s.CreateWorkbench(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{KnowledgeBaseID: workspaceString("shared")})
	workspaceStatus(t, err, 409)
	row, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{Name: workspaceString("Renamed"), KnowledgeBaseID: workspaceString("own")})
	if err != nil || row.Name != "Renamed" || row.KnowledgeBaseID != "own" {
		t.Fatalf("safe metadata update: %v %v", row, err)
	}
	legacy := WorkspaceInput{Name: workspaceString("Legacy"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "legacy-key"}
	raw, _ := json.Marshal([]string{"Legacy", "", "own"})
	hash := sha256.Sum256(raw)
	if err = s.db.Exec("INSERT INTO research_workbenches(id,tenant_id,user_id,name,knowledge_base_id,idempotency_key,request_hash) VALUES(?,?,?,?,?,?,?)",
		"legacy", 1, "alice", "Legacy", "own", "legacy-key", hex.EncodeToString(hash[:])).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CreateWorkbench(ctx, legacy)
	if err != nil || replayed.ID != "legacy" || replayed.WorkspaceMode != "legacy" || replayed.TemplateID != "" || replayed.TemplateVersion != 0 {
		t.Fatalf("legacy retry must preserve old scope: %v %v", replayed, err)
	}
	legacy.TemplateID = workspaceString("blank")
	_, err = s.CreateWorkbench(ctx, legacy)
	workspaceStatus(t, err, 409)
	updated, err := s.UpdateWorkbench(ctx, "legacy", WorkspaceInput{KnowledgeBaseID: workspaceString("shared")})
	if err != nil || updated.KnowledgeBaseID != "shared" {
		t.Fatalf("legacy rebinding compatibility: %v %v", updated, err)
	}
}

func TestWorkspaceOnboardingIsPersonalAndPersistent(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	alice := workspaceContext("alice", 1, types.TenantRoleContributor)
	reader := workspaceContext("reader", 1, types.TenantRoleViewer)
	row, err := s.CreateWorkbench(alice, WorkspaceInput{Name: workspaceString("Guide"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "guide"})
	if err != nil {
		t.Fatal(err)
	}
	row.AccessPolicy = &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	if err := s.db.Save(row).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	s.db.Model(&types.ResearchWorkbenchOnboardingRecord{}).Count(&count)
	if count != 0 || row.Onboarding.Status != "active" || row.Onboarding.Tasks.Sources {
		t.Fatal("reads must not create or pre-complete guide state", row, count)
	}
	row, err = s.UpdateWorkbenchOnboarding(reader, row.ID, WorkbenchOnboardingInput{Status: workspaceString("dismissed"), Tasks: &WorkbenchOnboardingTasksInput{Sources: workspaceBool(true)}})
	if err != nil || row.Onboarding.Status != "dismissed" || !row.Onboarding.Tasks.Sources {
		t.Fatalf("reader personal preference: %v %v", row, err)
	}
	s2 := NewWorkspaceService(s.db, s.kbs, s.shares)
	rows, err := s2.ListWorkbenches(reader, "own")
	if err != nil || len(rows) != 1 || rows[0].Onboarding.Status != "dismissed" || !rows[0].Onboarding.Tasks.Sources {
		t.Fatalf("guide must survive reopening: %v %v", rows, err)
	}
	other, err := s2.GetWorkbench(alice, row.ID)
	if err != nil || other.Onboarding.Status != "active" || other.Onboarding.Tasks.Sources {
		t.Fatal("another person's guide leaked", other, err)
	}
	for _, status := range []string{"skipped", "active", "completed", "dismissed"} {
		row, err = s.UpdateWorkbenchOnboarding(reader, row.ID, WorkbenchOnboardingInput{Status: workspaceString(status), Tasks: &WorkbenchOnboardingTasksInput{Review: workspaceBool(true)}})
		if err != nil || row.Onboarding.Status != status || !row.Onboarding.Tasks.Sources || !row.Onboarding.Tasks.Review {
			t.Fatalf("status/partial tasks: %v %v", row, err)
		}
	}
	row, err = s.UpdateWorkbenchOnboarding(reader, row.ID, WorkbenchOnboardingInput{Tasks: &WorkbenchOnboardingTasksInput{Sources: workspaceBool(false)}})
	if err != nil || row.Onboarding.Tasks.Sources || !row.Onboarding.Tasks.Review || row.Onboarding.Status != "dismissed" {
		t.Fatalf("explicit false/omitted state: %v %v", row, err)
	}
	for _, input := range []WorkbenchOnboardingInput{{}, {Status: workspaceString("unknown")}, {Tasks: &WorkbenchOnboardingTasksInput{}}} {
		_, err := s.UpdateWorkbenchOnboarding(reader, row.ID, input)
		workspaceStatus(t, err, 400)
	}
	_, err = s.UpdateWorkbench(reader, row.ID, WorkspaceInput{Name: workspaceString("not allowed")})
	workspaceStatus(t, err, 403)
	_, err = s.UpdateWorkbenchOnboarding(workspaceContext("reader", 2, types.TenantRoleOwner), row.ID, WorkbenchOnboardingInput{Status: workspaceString("skipped")})
	workspaceStatus(t, err, 404)
	if err = s.DeleteWorkbench(alice, row.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateWorkbenchOnboarding(reader, row.ID, WorkbenchOnboardingInput{Status: workspaceString("skipped")})
	workspaceStatus(t, err, 404)
}

func TestWorkspaceOnboardingChecksRevokedAccess(t *testing.T) {
	s, _, shares := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	shares.allowed = true
	row, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Shared guide"), KnowledgeBaseID: workspaceString("shared"), IdempotencyKey: "shared-guide"})
	if err != nil {
		t.Fatal(err)
	}
	shares.allowed = false
	if err := s.db.Model(row).Update("access_policy", nil).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateWorkbenchOnboarding(ctx, row.ID, WorkbenchOnboardingInput{Status: workspaceString("dismissed")})
	workspaceStatus(t, err, 404)
	var count int64
	s.db.Model(&types.ResearchWorkbenchOnboardingRecord{}).Count(&count)
	if count != 0 {
		t.Fatal("revoked user wrote guide state")
	}
}
