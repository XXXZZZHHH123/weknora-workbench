package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func permissionWorkbench(t *testing.T, s *WorkspaceService, owner string) *types.ResearchWorkbench {
	t.Helper()
	row, err := s.CreateWorkbench(workspaceContext(owner, 1, types.TenantRoleContributor), WorkspaceInput{
		Name: workspaceString("Permission fixture"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "permissions-" + owner,
	})
	require.NoError(t, err)
	return row
}

func TestWorkbenchNewResourcesArePrivateAndAdminHasNoBypass(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	row := permissionWorkbench(t, s, "alice")
	require.Equal(t, types.ResourcePermissionNone, row.AccessPolicy.SpacePermission)
	require.Empty(t, row.AccessPolicy.Members)
	require.True(t, row.CanEdit)
	require.True(t, row.CanManage)
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleAdmin, types.TenantRoleContributor, types.TenantRoleViewer} {
		ctx := workspaceContext("peer", 1, role)
		_, err := s.GetWorkbench(ctx, row.ID)
		workspaceStatus(t, err, 404)
		rows, err := s.ListWorkbenches(ctx, "")
		require.NoError(t, err)
		require.Empty(t, rows)
		_, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{Name: workspaceString("Unauthorized")})
		workspaceStatus(t, err, 404)
		workspaceStatus(t, s.DeleteWorkbench(ctx, row.ID), 404)
		_, err = s.GetWorkbenchPermissions(ctx, row.ID)
		workspaceStatus(t, err, 404)
	}
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	permissions, err := s.GetWorkbenchPermissions(owner, row.ID)
	require.NoError(t, err)
	require.Equal(t, "alice", permissions.OwnerID)
	require.False(t, permissions.Legacy)
	viewer, err := s.GetWorkbench(workspaceContext("alice", 1, types.TenantRoleViewer), row.ID)
	require.NoError(t, err)
	require.False(t, viewer.CanEdit)
	require.False(t, viewer.CanManage)
	_, err = s.UpdateWorkbench(workspaceContext("alice", 1, types.TenantRoleViewer), row.ID, WorkspaceInput{Name: workspaceString("Unauthorized")})
	workspaceStatus(t, err, 403)
}

func TestWorkbenchPermissionLevelsAndMemberValidation(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	row := permissionWorkbench(t, s, "alice")
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	for _, member := range []types.TenantMember{
		{UserID: "editor", TenantID: 1, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{UserID: "reader", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
		{UserID: "inactive", TenantID: 1, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusSuspended},
		{UserID: "external", TenantID: 2, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
	} {
		require.NoError(t, s.db.Create(&member).Error)
	}
	policy := types.NewResourceAccessPolicy()
	policy.Members = []types.ResourceMemberPermission{{UserID: "editor", Permission: "editor"}, {UserID: "reader", Permission: "viewer"}}
	_, err := s.UpdateWorkbenchPermissions(owner, row.ID, policy)
	require.NoError(t, err)
	for _, tc := range []struct {
		user string
		role types.TenantRole
		edit bool
	}{
		{"editor", types.TenantRoleContributor, true}, {"reader", types.TenantRoleViewer, false},
	} {
		ctx := workspaceContext(tc.user, 1, tc.role)
		loaded, err := s.GetWorkbench(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, tc.edit, loaded.CanEdit)
		require.False(t, loaded.CanManage)
		_, err = s.GetWorkbenchPermissions(ctx, row.ID)
		workspaceStatus(t, err, 403)
		_, err = s.UpdateWorkbench(ctx, row.ID, WorkspaceInput{Name: workspaceString("No metadata management")})
		workspaceStatus(t, err, 403)
	}
	for _, invalid := range []*types.ResourceAccessPolicy{
		nil, {SpacePermission: "invalid"},
		{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "unknown", Permission: "viewer"}}},
		{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "inactive", Permission: "viewer"}}},
		{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "external", Permission: "viewer"}}},
		{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "reader", Permission: "editor"}}},
		{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "editor", Permission: "viewer"}, {UserID: "editor", Permission: "editor"}}},
	} {
		_, err := s.UpdateWorkbenchPermissions(owner, row.ID, invalid)
		workspaceStatus(t, err, 400)
	}
	_, err = s.UpdateWorkbenchPermissions(owner, row.ID, &types.ResourceAccessPolicy{SpacePermission: "editor", Members: []types.ResourceMemberPermission{}})
	require.NoError(t, err)
	for _, role := range []types.TenantRole{types.TenantRoleContributor, types.TenantRoleViewer} {
		loaded, err := s.GetWorkbench(workspaceContext("any-member", 1, role), row.ID)
		require.NoError(t, err)
		require.Equal(t, role == types.TenantRoleContributor, loaded.CanEdit)
		require.False(t, loaded.CanManage)
	}
}

func TestWorkbenchDerivedSourceNeedsCurrentKnowledgeOwnerConsent(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "source-owner"
	require.NoError(t, s.db.Save(kb).Error)
	row := permissionWorkbench(t, s, "research-owner")
	owner := workspaceContext("research-owner", 1, types.TenantRoleContributor)
	sourceOwner := workspaceContext("source-owner", 1, types.TenantRoleContributor)
	_, err := s.GetWorkbench(sourceOwner, row.ID)
	workspaceStatus(t, err, 404)
	request, err := s.RequestWorkbenchSourceSharing(owner, row.ID)
	require.NoError(t, err)
	require.True(t, request.SourceSharingRequested)
	require.False(t, request.SourceSharingApproved)
	_, err = s.RequestWorkbenchSourceSharing(workspaceContext("administrator", 1, types.TenantRoleAdmin), row.ID)
	workspaceStatus(t, err, 404)
	approval, err := s.GetWorkbenchSourceSharing(sourceOwner, row.ID)
	require.NoError(t, err)
	require.True(t, approval.Requested)
	require.False(t, approval.Approved)
	_, err = s.UpdateWorkbenchPermissions(owner, row.ID, &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}})
	require.NoError(t, err)
	// Closing the KB must not revoke independently shared workbench metadata.
	kb.AccessPolicy = types.NewResourceAccessPolicy()
	require.NoError(t, s.db.Save(kb).Error)
	reader := workspaceContext("reader", 1, types.TenantRoleViewer)
	loaded, err := s.GetWorkbench(reader, row.ID)
	require.NoError(t, err)
	require.Empty(t, loaded.KnowledgeBaseName)
	require.False(t, loaded.CanReadSource)
	require.False(t, loaded.SourceDataAvailable)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
	for _, ctx := range []context.Context{owner, workspaceContext("admin", 1, types.TenantRoleAdmin)} {
		_, err := s.UpdateWorkbenchSourceSharing(ctx, row.ID, true)
		workspaceStatus(t, err, 403)
	}
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, true)
	require.NoError(t, err)
	for _, knowledge := range []*types.Knowledge{
		{ID: "sheet", TenantID: 1, KnowledgeBaseID: "own", FileType: "xlsx", FileHash: "abc", FileName: "PRIVATE.xlsx", FilePath: "/private/path", Title: "PRIVATE TITLE", ParseStatus: "stored", EnableStatus: "disabled"},
		{ID: "other-library", TenantID: 1, KnowledgeBaseID: "other", FileType: "xlsx"},
		{ID: "deleted-sheet", TenantID: 1, KnowledgeBaseID: "own", FileType: "xlsx"},
		{ID: "deleting-sheet", TenantID: 1, KnowledgeBaseID: "own", FileType: "xlsx", ParseStatus: types.ParseStatusDeleting},
	} {
		require.NoError(t, s.db.Create(knowledge).Error)
	}
	require.NoError(t, s.db.Delete(&types.Knowledge{ID: "deleted-sheet"}).Error)
	scope, err := s.GetWorkbenchSourceScope(reader, row.ID)
	require.NoError(t, err)
	require.Equal(t, "reader", scope.UserID)
	require.False(t, scope.CanEdit)
	require.Len(t, scope.Sources, 1)
	require.True(t, scope.Sources[0].Delegated)
	require.Len(t, scope.Sources[0].Knowledge, 1)
	require.Equal(t, "sheet", scope.Sources[0].Knowledge[0].ID)
	data, err := json.Marshal(scope)
	require.NoError(t, err)
	require.NotContains(t, string(data), "PRIVATE")
	require.NotContains(t, string(data), "file_path")
	require.NotContains(t, string(data), "file_name")
	loaded, err = s.GetWorkbench(reader, row.ID)
	require.NoError(t, err)
	require.True(t, loaded.SourceDataAvailable)
	require.False(t, loaded.CanReadSource)
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, false)
	require.NoError(t, err)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, true)
	require.NoError(t, err)
	kb.CreatorID = "new-source-owner"
	require.NoError(t, s.db.Save(kb).Error)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
	// Losing source access does not prevent the workbench owner deleting it.
	require.NoError(t, s.DeleteWorkbench(owner, row.ID))
}

func TestWorkbenchLegacyVisibilityAndAdministrationRemainUnchanged(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	row := permissionWorkbench(t, s, "alice")
	require.NoError(t, s.db.Model(row).Update("access_policy", nil).Error)
	admin := workspaceContext("admin", 1, types.TenantRoleAdmin)
	loaded, err := s.GetWorkbench(admin, row.ID)
	require.NoError(t, err)
	require.True(t, loaded.CanManage)
	require.True(t, loaded.CanEdit)
	viewer, err := s.GetWorkbench(workspaceContext("reader", 1, types.TenantRoleViewer), row.ID)
	require.NoError(t, err)
	require.False(t, viewer.CanEdit)
	_, err = s.UpdateWorkbenchPermissions(admin, row.ID, types.NewResourceAccessPolicy())
	workspaceStatus(t, err, 403)
}
