package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchDefaultSourceUsesExplicitTenantBinding(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	require.NoError(t, s.db.AutoMigrate(&types.KnowledgeBase{}))
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	source := &types.KnowledgeBase{ID: "real-default", TenantID: 1, Name: "Arbitrary renamed source"}
	require.NoError(t, s.db.Create(source).Error)
	kbs.rows[source.ID] = source
	require.NoError(t, s.db.Create(&types.DefaultKnowledgeBaseBinding{TenantID: 1, Kind: "macro", KnowledgeBaseID: source.ID}).Error)
	// A same-name KB has no special role.
	kbs.rows["same-name"] = &types.KnowledgeBase{ID: "same-name", TenantID: 1, Name: source.Name}
	row, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Workspace"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "default-source"})
	require.NoError(t, err)
	require.NotNil(t, row.DefaultKnowledgeBase)
	require.Equal(t, source.ID, row.DefaultKnowledgeBase.KnowledgeBaseID)
	require.Equal(t, uint64(1), row.DefaultKnowledgeBase.TenantID)
	require.Equal(t, "macro", row.DefaultKnowledgeBase.Kind)
	readable := &types.ResourceAccessPolicy{SpacePermission: types.ResourcePermissionViewer, Members: []types.ResourceMemberPermission{}}
	row.AccessPolicy = readable
	require.NoError(t, s.db.Save(row).Error)
	// Viewer reads the same current, system-bound source.
	viewer, err := s.GetWorkbench(workspaceContext("reader", 1, types.TenantRoleViewer), row.ID)
	require.NoError(t, err)
	require.Equal(t, source.ID, viewer.DefaultKnowledgeBase.KnowledgeBaseID)
	require.NoError(t, s.db.Delete(source).Error)
	row, err = s.GetWorkbench(ctx, row.ID)
	require.NoError(t, err)
	require.Nil(t, row.DefaultKnowledgeBase)
}

func TestWorkbenchDefaultSourceCannotUseAnotherTenantBinding(t *testing.T) {
	s, kbs, _ := workspaceFixture(t)
	require.NoError(t, s.db.AutoMigrate(&types.KnowledgeBase{}))
	source := &types.KnowledgeBase{ID: "foreign-default", TenantID: 2, Name: "宏观数据"}
	require.NoError(t, s.db.Create(source).Error)
	kbs.rows[source.ID] = source
	require.NoError(t, s.db.Create(&types.DefaultKnowledgeBaseBinding{TenantID: 1, Kind: "macro", KnowledgeBaseID: source.ID}).Error)
	row, err := s.CreateWorkbench(workspaceContext("alice", 1, types.TenantRoleContributor), WorkspaceInput{Name: workspaceString("Workspace"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "foreign-default-source"})
	require.NoError(t, err)
	require.Nil(t, row.DefaultKnowledgeBase)
}
