package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceInputs(values ...types.WorkbenchSourceInput) *[]types.WorkbenchSourceInput {
	if values == nil {
		values = []types.WorkbenchSourceInput{}
	}
	return &values
}

func TestWorkbenchV2CanStartEmptyAndPreservesIdentityAfterRemovingAllSources(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Independent"), Sources: sourceInputs(), IdempotencyKey: "v2-empty"})
	require.NoError(t, err)
	require.Equal(t, 2, w.ScopeVersion)
	require.EqualValues(t, 1, w.SourcesRevision)
	require.Empty(t, w.KnowledgeBaseID)
	grant, err := s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.Empty(t, grant.Sources)
	require.EqualValues(t, 1, grant.SourcesRevision)
	view, err := s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: 1, Sources: *sourceInputs(types.WorkbenchSourceInput{SourceType: "knowledge_base", SourceID: "own"})})
	require.NoError(t, err)
	require.EqualValues(t, 2, view.SourcesRevision)
	require.Len(t, view.Sources, 1)
	_, err = s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: 1, Sources: []types.WorkbenchSourceInput{}})
	workspaceStatus(t, err, 409)
	view, err = s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: 2, Sources: []types.WorkbenchSourceInput{}})
	require.NoError(t, err)
	require.EqualValues(t, 3, view.SourcesRevision)
	loaded, err := s.GetWorkbench(owner, w.ID)
	require.NoError(t, err)
	require.Equal(t, w.ID, loaded.ID)
	require.Empty(t, loaded.KnowledgeBaseID)
	grant, err = s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.Empty(t, grant.Sources)
	var tombstones int64
	require.NoError(t, s.db.Model(&types.ResearchWorkbenchSourceBinding{}).Where("workbench_id=? AND status=?", w.ID, "removed").Count(&tombstones).Error)
	require.EqualValues(t, 1, tombstones)
	repeated, err := s.GetWorkbench(owner, w.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, repeated.SourcesRevision)
}

func TestWorkbenchV2RetainsButDoesNotAuthorizeExistingUnavailableSource(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "alice"
	kb.AccessPolicy = &types.ResourceAccessPolicy{SpacePermission: types.ResourcePermissionViewer, Members: []types.ResourceMemberPermission{}}
	require.NoError(t, s.db.Save(kb).Error)
	owner := workspaceContext("bob", 1, types.TenantRoleContributor)
	inputs := []types.WorkbenchSourceInput{{SourceType: "knowledge_base", SourceID: "own"}}
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Separate owner"), Sources: &inputs, IdempotencyKey: "v2-unavailable"})
	require.NoError(t, err)
	kb.AccessPolicy = types.NewResourceAccessPolicy()
	require.NoError(t, s.db.Save(kb).Error)
	view, err := s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: w.SourcesRevision, Sources: inputs})
	require.NoError(t, err)
	require.Len(t, view.Sources, 1)
	require.Equal(t, "unavailable", view.Sources[0].Status)
	require.False(t, view.Sources[0].CanRead)
	grant, err := s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.Empty(t, grant.Sources)
	require.Len(t, grant.UnavailableSources, 1)
	_, err = s.UpdateSourceBindingSharing(owner, w.ID, view.Sources[0].ID, WorkbenchSourceSharingInput{Approved: true, Modules: []string{"diagram"}})
	workspaceStatus(t, err, 403)
}

func TestWorkbenchV2SourcesEnforceCountDuplicatesAndNewSourceAuthorization(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	for _, inputs := range [][]types.WorkbenchSourceInput{
		{{SourceType: "knowledge_base", SourceID: "own"}, {SourceType: "knowledge_base", SourceID: "own"}},
		make([]types.WorkbenchSourceInput, 9),
	} {
		_, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Invalid"), Sources: &inputs, IdempotencyKey: "v2-invalid"})
		workspaceStatus(t, err, 400)
	}
	inputs := []types.WorkbenchSourceInput{{SourceType: "knowledge_base", SourceID: "foreign"}}
	_, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Forbidden"), Sources: &inputs, IdempotencyKey: "v2-forbidden"})
	workspaceStatus(t, err, 404)
}

func TestWorkbenchV2AcceptsEightDistinctAuthorizedSources(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	inputs := []types.WorkbenchSourceInput{}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("source-%d", i)
		kb := &types.KnowledgeBase{ID: id, TenantID: 1, Name: id}
		require.NoError(t, s.db.Create(kb).Error)
		kbs.rows[id] = kb
		inputs = append(inputs, types.WorkbenchSourceInput{SourceType: "knowledge_base", SourceID: id})
	}
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Eight sources"), Sources: &inputs, IdempotencyKey: "v2-eight"})
	require.NoError(t, err)
	require.Len(t, w.SourcesSummary, 8)
	require.True(t, w.CanReadSource)
	grant, err := s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.Len(t, grant.Sources, 8)
}

func TestWorkbenchV2HistoricalTombstonesDoNotCountAsActiveSources(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Repeated source edits"), Sources: sourceInputs(), IdempotencyKey: "history-limit"})
	require.NoError(t, err)
	revision := w.SourcesRevision
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("historical-%d", i)
		kb := &types.KnowledgeBase{ID: id, TenantID: 1, Name: id}
		require.NoError(t, s.db.Create(kb).Error)
		kbs.rows[id] = kb
		view, err := s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: revision, Sources: []types.WorkbenchSourceInput{{SourceType: "knowledge_base", SourceID: id}}})
		require.NoError(t, err)
		revision = view.SourcesRevision
		require.Len(t, view.Sources, 1)
		loaded, err := s.GetWorkbench(owner, w.ID)
		require.NoError(t, err)
		require.True(t, loaded.CanReadSource)
		require.True(t, loaded.CanViewSourceModule("judgments"))
		grant, err := s.GetWorkbenchSourceScope(owner, w.ID)
		require.NoError(t, err)
		require.Len(t, grant.Sources, 1)
		require.Empty(t, grant.UnavailableSources)
	}
	view, err := s.UpdateWorkbenchSources(owner, w.ID, WorkbenchSourcesInput{ExpectedRevision: revision, Sources: []types.WorkbenchSourceInput{}})
	require.NoError(t, err)
	require.Empty(t, view.Sources)
	grant, err := s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.Empty(t, grant.Sources)
	require.Empty(t, grant.UnavailableSources)
	var count int64
	require.NoError(t, s.db.Model(&types.ResearchWorkbenchSourceBinding{}).Where("workbench_id=?", w.ID).Count(&count).Error)
	require.EqualValues(t, 10, count)
}

func assertNoWorkbenchSourceGrant(t *testing.T, s *WorkspaceService, ctx context.Context, id string) {
	t.Helper()
	scope, err := s.GetWorkbenchSourceScope(ctx, id)
	require.NoError(t, err)
	require.Empty(t, scope.Sources)
	require.Empty(t, scope.ViewModules)
}
