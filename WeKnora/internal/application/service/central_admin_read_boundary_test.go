package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCentralAdminCannotManageAnUnreadableCenterThroughPolicyOrSharing(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	kb := &types.KnowledgeBase{ID: "private-center", TenantID: 1, CreatorID: "alice", Name: "Private center", SystemRole: types.KnowledgeBaseSystemRoleCentral, AccessPolicy: types.NewResourceAccessPolicy()}
	require.NoError(t, s.db.Create(kb).Error)
	kbs.rows[kb.ID] = kb
	require.NoError(t, s.db.Create(&types.CentralDataset{ID: "center", TenantID: 1, Kind: "macro", Name: kb.Name, BackingKnowledgeBaseID: kb.ID}).Error)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	admin := workspaceContext("administrator", 1, types.TenantRoleAdmin)
	inputs := []types.WorkbenchSourceInput{{SourceType: "central_dataset", SourceID: "center"}}
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Private source"), Sources: &inputs, IdempotencyKey: "center-read-boundary"})
	require.NoError(t, err)
	native := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(s.db)}
	_, err = native.GetKnowledgeBasePermissions(admin, kb.ID)
	workspaceStatus(t, err, 403)
	_, err = s.GetSourceBindingSharing(admin, w.ID, w.SourcesSummary[0].ID)
	workspaceStatus(t, err, 403)
	_, err = s.UpdateSourceBindingSharing(admin, w.ID, w.SourcesSummary[0].ID, WorkbenchSourceSharingInput{Approved: true, Modules: []string{"diagram"}})
	workspaceStatus(t, err, 403)
	_, err = native.UpdateKnowledgeBase(admin, kb.ID, "Changed", "", nil, nil)
	workspaceStatus(t, err, 403)
}
