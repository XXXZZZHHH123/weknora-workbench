package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchV2DirectCenterCannotExpandPrivateSourceSharedPages(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	private := kbs.rows["own"]
	private.CreatorID = "alice"
	private.AccessPolicy = types.NewResourceAccessPolicy()
	require.NoError(t, s.db.Save(private).Error)
	center := &types.KnowledgeBase{ID: "central-backing", TenantID: 1, Name: "Center", CreatorID: "admin", SystemRole: types.KnowledgeBaseSystemRoleCentral, AccessPolicy: &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}}
	require.NoError(t, s.db.Create(center).Error)
	kbs.rows[center.ID] = center
	require.NoError(t, s.db.Create(&types.CentralDataset{ID: "central", TenantID: 1, Kind: "macro", Name: "Center", BackingKnowledgeBaseID: center.ID}).Error)
	inputs := []types.WorkbenchSourceInput{{SourceType: "knowledge_base", SourceID: "own"}, {SourceType: "central_dataset", SourceID: "central"}}
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Mixed research"), Sources: &inputs, IdempotencyKey: "mixed-center"})
	require.NoError(t, err)
	w.AccessPolicy = &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	w.RestrictedViewProfiles = []types.WorkbenchRestrictedViewProfile{{ID: "diagrams", Name: "Diagrams", AllMembers: true, Modules: []string{"diagram"}, MemberIDs: []string{}}}
	require.NoError(t, s.db.Save(w).Error)
	binding := ""
	for _, source := range w.SourcesSummary {
		if source.SourceType == "knowledge_base" {
			binding = source.ID
		}
	}
	require.NotEmpty(t, binding)
	_, err = s.UpdateSourceBindingSharing(owner, w.ID, binding, WorkbenchSourceSharingInput{Approved: true, Modules: types.WorkbenchViewModules()})
	require.NoError(t, err)
	reader := workspaceContext("reader", 1, types.TenantRoleViewer)
	loaded, err := s.GetWorkbench(reader, w.ID)
	require.NoError(t, err)
	require.False(t, loaded.CanReadSource)
	require.Equal(t, []string{"diagram"}, loaded.EffectiveViewModules)
	require.True(t, loaded.CanViewSourceModule("diagram"))
	require.False(t, loaded.CanViewSourceModule("judgments"))
	grant, err := s.GetWorkbenchSourceScope(reader, w.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"diagram"}, grant.ViewModules)
	require.Len(t, grant.Sources, 2)
	_, err = s.UpdateSourceBindingSharing(owner, w.ID, binding, WorkbenchSourceSharingInput{Approved: false})
	require.NoError(t, err)
	grant, err = s.GetWorkbenchSourceScope(reader, w.ID)
	require.NoError(t, err)
	require.Empty(t, grant.ViewModules)
	require.Len(t, grant.Sources, 1, "independent center read remains available without granting private research pages")
}

func TestWorkbenchV2OnlyRequestedSharingRevealsPrivateWorkbenchTitleToSourceOwner(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	kb := kbs.rows["own"]
	kb.CreatorID = "alice"
	kb.AccessPolicy = &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	require.NoError(t, s.db.Save(kb).Error)
	inputs := []types.WorkbenchSourceInput{{SourceType: "knowledge_base", SourceID: "own"}}
	owner := workspaceContext("bob", 1, types.TenantRoleContributor)
	sourceOwner := workspaceContext("alice", 1, types.TenantRoleContributor)
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Private title"), Sources: &inputs, IdempotencyKey: "approval-request"})
	require.NoError(t, err)
	native := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(s.db)}
	permissions, err := native.GetKnowledgeBasePermissions(sourceOwner, kb.ID)
	require.NoError(t, err)
	require.Empty(t, permissions.PendingWorkbenches)
	binding := w.SourcesSummary[0].ID
	_, err = s.RequestSourceBindingSharing(workspaceContext("outsider", 1, types.TenantRoleContributor), w.ID, binding)
	workspaceStatus(t, err, 404)
	requested, err := s.RequestSourceBindingSharing(owner, w.ID, binding)
	require.NoError(t, err)
	require.True(t, requested.Requested)
	permissions, err = native.GetKnowledgeBasePermissions(sourceOwner, kb.ID)
	require.NoError(t, err)
	require.Len(t, permissions.PendingWorkbenches, 1)
	require.Equal(t, binding, permissions.PendingWorkbenches[0].SourceBindingID)
	_, err = s.GetWorkbench(sourceOwner, w.ID)
	workspaceStatus(t, err, 404)
}
