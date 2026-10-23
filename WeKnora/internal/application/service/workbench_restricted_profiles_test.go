package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func restrictedProfilesFixture(t *testing.T) (*WorkspaceService, *workspaceKBFixture, *types.ResearchWorkbench, context.Context) {
	t.Helper()
	s, kbs, _ := workspaceFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "alice"
	require.NoError(t, s.db.Save(kb).Error)
	row := permissionWorkbench(t, s, "alice")
	kb.AccessPolicy = types.NewResourceAccessPolicy()
	require.NoError(t, s.db.Save(kb).Error)
	for _, member := range []types.TenantMember{
		{UserID: "reader-a", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
		{UserID: "reader-b", TenantID: 1, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{UserID: "reader-c", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
		{UserID: "inactive", TenantID: 1, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended},
		{UserID: "external", TenantID: 2, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
	} {
		require.NoError(t, s.db.Create(&member).Error)
	}
	return s, kbs, row, workspaceContext("alice", 1, types.TenantRoleContributor)
}

func TestWorkbenchRestrictedProfilesAtomicPermissionsAndUserScope(t *testing.T) {
	s, _, row, owner := restrictedProfilesFixture(t)
	profiles := []types.WorkbenchRestrictedViewProfile{
		{ID: "market-group", Name: "Market readers", Modules: []string{"market"}, MemberIDs: []string{"reader-a"}},
		{ID: "diagram-group", Name: "Diagram readers", Modules: []string{"diagram"}, MemberIDs: []string{"reader-a", "reader-b"}},
	}
	approved := true
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	result, err := s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles, SourceSharingApproved: &approved})
	require.NoError(t, err)
	require.True(t, result.SourceSharingApproved)
	require.Equal(t, profiles, result.RestrictedViewProfiles)
	require.Equal(t, []string{"market", "diagram"}, result.SourceSharingModules)
	for _, tc := range []struct {
		user    string
		modules []string
	}{{"reader-a", []string{"market", "diagram"}}, {"reader-b", []string{"diagram"}}, {"reader-c", nil}} {
		ctx := workspaceContext(tc.user, 1, types.TenantRoleViewer)
		wb, err := s.GetWorkbench(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, len(tc.modules) > 0, wb.SourceDataAvailable)
		require.Equal(t, tc.modules, nilIfEmpty(wb.SourceSharingModules))
		raw, err := json.Marshal(wb)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "member_ids")
		require.NotContains(t, string(raw), "market-group")
		scope, err := s.GetWorkbenchSourceScope(ctx, row.ID)
		if len(tc.modules) == 0 {
			workspaceStatus(t, err, 403)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.modules, scope.ViewModules)
		}
	}
	// A profile never grants workbench membership, and an existing session must
	// lose pages on its next source-scope verification after a configuration edit.
	profiles[1].MemberIDs = []string{"reader-b"}
	_, err = s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	require.NoError(t, err)
	scope, err := s.GetWorkbenchSourceScope(workspaceContext("reader-a", 1, types.TenantRoleViewer), row.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"market"}, scope.ViewModules)
	profiles = []types.WorkbenchRestrictedViewProfile{}
	_, err = s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	require.NoError(t, err)
	_, err = s.GetWorkbenchSourceScope(workspaceContext("reader-a", 1, types.TenantRoleViewer), row.ID)
	workspaceStatus(t, err, 403)
	// Direct source readers retain their full, independently granted KB view.
	direct, err := s.GetWorkbenchSourceScope(owner, row.ID)
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchViewModules(), direct.ViewModules)
}

func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

func TestWorkbenchRestrictedProfilesValidateWithoutPartialSave(t *testing.T) {
	s, _, row, owner := restrictedProfilesFixture(t)
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	valid := types.WorkbenchRestrictedViewProfile{ID: "valid", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"reader-a"}}
	invalid := [][]types.WorkbenchRestrictedViewProfile{
		{{ID: "same", Name: "First", Modules: []string{"market"}}, {ID: "same", Name: "Second", Modules: []string{"diagram"}}},
		{{ID: "x", Name: "", Modules: []string{"diagram"}}},
		{{ID: "x", Name: "View", Modules: []string{"diagram", "diagram"}}},
		{{ID: "x", Name: "View", Modules: []string{"unknown"}}},
		{{ID: "x", Name: "View", Modules: []string{}}},
		{{ID: "x", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"reader-a", "reader-a"}}},
		{{ID: "x", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"reader-a"}, AllMembers: true}},
		{{ID: "x", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"external"}}},
		{{ID: "x", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"inactive"}}},
		{{ID: "x", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{"unknown"}}},
	}
	for _, profiles := range invalid {
		_, err := s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
		workspaceStatus(t, err, 400)
		current, err := s.GetWorkbenchPermissions(owner, row.ID)
		require.NoError(t, err)
		require.Equal(t, "none", current.Policy.SpacePermission)
		require.Equal(t, []types.WorkbenchRestrictedViewProfile{}, current.RestrictedViewProfiles)
	}
	profiles := []types.WorkbenchRestrictedViewProfile{valid}
	_, err := s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: types.NewResourceAccessPolicy(), RestrictedViewProfiles: &profiles})
	workspaceStatus(t, err, 400)
	approved := true
	_, err = s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles, SourceSharingApproved: &approved})
	require.NoError(t, err)
	// A membership revocation without reconfiguring groups must always work.
	_, err = s.UpdateWorkbenchPermissions(owner, row.ID, types.NewResourceAccessPolicy())
	require.NoError(t, err)
	_, err = s.GetWorkbenchSourceScope(workspaceContext("reader-a", 1, types.TenantRoleViewer), row.ID)
	workspaceStatus(t, err, 404)
	// The resource ID and a valid group never cross a space boundary.
	_, err = s.UpdateWorkbenchPermissionSettings(workspaceContext("alice", 2, types.TenantRoleOwner), row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	workspaceStatus(t, err, 404)
}

func TestWorkbenchRestrictedProfilesCannotExpandSourceConsent(t *testing.T) {
	s, kbs, row, owner := restrictedProfilesFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "source-owner"
	require.NoError(t, s.db.Save(kb).Error)
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	profiles := []types.WorkbenchRestrictedViewProfile{{ID: "view", Name: "View", Modules: []string{"market", "diagram"}, MemberIDs: []string{"reader-a"}}}
	approved := true
	_, err := s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles, SourceSharingApproved: &approved})
	workspaceStatus(t, err, 403)
	before, err := s.GetWorkbenchPermissions(owner, row.ID)
	require.NoError(t, err)
	require.Equal(t, "none", before.Policy.SpacePermission)
	require.Equal(t, []types.WorkbenchRestrictedViewProfile{}, before.RestrictedViewProfiles)
	_, err = s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	require.NoError(t, err)
	sourceOwner := workspaceContext("source-owner", 1, types.TenantRoleContributor)
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, true, []string{"diagram"})
	require.NoError(t, err)
	reader := workspaceContext("reader-a", 1, types.TenantRoleViewer)
	scope, err := s.GetWorkbenchSourceScope(reader, row.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"diagram"}, scope.ViewModules)
	profiles[0].AllMembers = true
	profiles[0].MemberIDs = []string{}
	_, err = s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	require.NoError(t, err)
	scope, err = s.GetWorkbenchSourceScope(workspaceContext("reader-c", 1, types.TenantRoleViewer), row.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"diagram"}, scope.ViewModules)
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, false, nil)
	require.NoError(t, err)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
	_, err = s.UpdateWorkbenchSourceSharing(sourceOwner, row.ID, true, []string{"diagram"})
	require.NoError(t, err)
	kb.CreatorID = "new-owner"
	require.NoError(t, s.db.Save(kb).Error)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
}

func TestWorkbenchRestrictedProfilesMetadataWritesPreserveSourceApprovalUpperBound(t *testing.T) {
	s, kbs, row, owner := restrictedProfilesFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "source-owner"
	require.NoError(t, s.db.Save(kb).Error)
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	profiles := []types.WorkbenchRestrictedViewProfile{{ID: "limited", Name: "Diagram", Modules: []string{"diagram"}, MemberIDs: []string{"reader-a"}}}
	_, err := s.UpdateWorkbenchPermissionSettings(owner, row.ID, WorkbenchPermissionSettings{ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles})
	require.NoError(t, err)
	_, err = s.UpdateWorkbenchSourceSharing(workspaceContext("source-owner", 1, types.TenantRoleContributor), row.ID, true, []string{"market", "diagram"})
	require.NoError(t, err)
	loaded, err := s.GetWorkbench(owner, row.ID)
	require.NoError(t, err)
	require.False(t, loaded.CanReadSource)
	require.Empty(t, loaded.SourceSharingModules)
	check := func() {
		var current types.ResearchWorkbench
		require.NoError(t, s.db.First(&current, "id = ?", row.ID).Error)
		require.True(t, current.SourceSharingApproved)
		require.Equal(t, []string{"market", "diagram"}, current.SourceSharingModules)
		permissions, err := s.GetWorkbenchPermissions(owner, row.ID)
		require.NoError(t, err)
		require.Equal(t, []string{"market", "diagram"}, permissions.SourceSharingModules)
	}
	_, err = s.UpdateWorkbench(owner, row.ID, WorkspaceInput{Name: workspaceString("Renamed")})
	require.NoError(t, err)
	check()
	_, err = s.RequestWorkbenchSourceSharing(owner, row.ID)
	require.NoError(t, err)
	check()
	status := "dismissed"
	_, err = s.UpdateWorkbenchOnboarding(owner, row.ID, WorkbenchOnboardingInput{Status: &status})
	require.NoError(t, err)
	check()
	_, err = s.UpdateWorkbenchPermissions(owner, row.ID, policy)
	require.NoError(t, err)
	check()
}

func TestWorkbenchNewSourceApprovalWithoutProfilesDoesNotGrantPages(t *testing.T) {
	s, kbs, row, owner := restrictedProfilesFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "source-owner"
	require.NoError(t, s.db.Save(kb).Error)
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	_, err := s.UpdateWorkbenchPermissions(owner, row.ID, policy)
	require.NoError(t, err)
	_, err = s.RequestWorkbenchSourceSharing(owner, row.ID)
	require.NoError(t, err)
	_, err = s.UpdateWorkbenchSourceSharing(workspaceContext("source-owner", 1, types.TenantRoleContributor), row.ID, true, []string{"diagram"})
	require.NoError(t, err)
	reader := workspaceContext("reader-a", 1, types.TenantRoleViewer)
	loaded, err := s.GetWorkbench(reader, row.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RestrictedViewProfiles, "new rows must not acquire grandfathered all-member sharing")
	require.Empty(t, loaded.RestrictedViewProfiles)
	require.False(t, loaded.SourceDataAvailable)
	_, err = s.GetWorkbenchSourceScope(reader, row.ID)
	workspaceStatus(t, err, 403)
	// NULL is reserved for genuinely grandfathered rows, which retain their
	// already explicit legacy source consent until their owner adopts profiles.
	require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", row.ID).Update("restricted_view_profiles", nil).Error)
	legacy, err := s.GetWorkbenchSourceScope(reader, row.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"diagram"}, legacy.ViewModules)
}
