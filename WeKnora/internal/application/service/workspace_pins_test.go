package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchDefaultOrderUsesCreationNotModification(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	ctx := workspaceContext("alice", 1, types.TenantRoleContributor)
	older := permissionWorkbench(t, s, "alice")
	newer, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Newer"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "newer"})
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", older.ID).Updates(map[string]any{"created_at": now.Add(-2 * time.Hour), "updated_at": now}).Error)
	require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", newer.ID).Updates(map[string]any{"created_at": now.Add(-time.Hour), "updated_at": now.Add(-time.Hour)}).Error)
	rows, err := s.ListWorkbenches(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, newer.ID, rows[0].ID, "editing an old workbench must not move it above a newer one")
}

func TestWorkbenchPinsArePersonalIdempotentAndCannotGrantAccess(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	row := permissionWorkbench(t, s, "alice")
	_, err := s.UpdateWorkbenchPermissions(owner, row.ID, &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}})
	require.NoError(t, err)
	reader := workspaceContext("reader", 1, types.TenantRoleViewer)
	beforePin, err := s.GetWorkbench(owner, row.ID)
	require.NoError(t, err)
	original := beforePin.UpdatedAt
	state, err := s.SetWorkbenchPin(reader, row.ID, true)
	require.NoError(t, err)
	require.True(t, state.IsPinned)
	require.NotNil(t, state.PinnedAt)
	again, err := s.SetWorkbenchPin(reader, row.ID, true)
	require.NoError(t, err)
	require.Equal(t, state.PinnedAt, again.PinnedAt)
	read, err := s.GetWorkbench(reader, row.ID)
	require.NoError(t, err)
	require.True(t, read.IsPinned)
	own, err := s.GetWorkbench(owner, row.ID)
	require.NoError(t, err)
	require.False(t, own.IsPinned)
	require.True(t, original.Equal(own.UpdatedAt))
	_, err = s.SetWorkbenchPin(workspaceContext("reader", 2, types.TenantRoleOwner), row.ID, true)
	workspaceStatus(t, err, 404)
	_, err = s.UpdateWorkbenchPermissions(owner, row.ID, types.NewResourceAccessPolicy())
	require.NoError(t, err)
	_, err = s.SetWorkbenchPin(reader, row.ID, true)
	workspaceStatus(t, err, 404)
	rows, err := s.ListWorkbenches(reader, "")
	require.NoError(t, err)
	require.Empty(t, rows)
	for i := 0; i < 2; i++ {
		state, err = s.SetWorkbenchPin(owner, row.ID, false)
		require.NoError(t, err)
		require.False(t, state.IsPinned)
		require.Nil(t, state.PinnedAt)
	}
}

func TestWorkbenchPinnedGroupPrecedesCreationOrderForOnlyThatUser(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	older := permissionWorkbench(t, s, "alice")
	newer, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Newer"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "newer"})
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", older.ID).Update("created_at", now.Add(-time.Hour)).Error)
	_, err = s.SetWorkbenchPin(owner, older.ID, true)
	require.NoError(t, err)
	rows, err := s.ListWorkbenches(owner, "")
	require.NoError(t, err)
	require.Equal(t, older.ID, rows[0].ID)
	_, err = s.SetWorkbenchPin(owner, older.ID, false)
	require.NoError(t, err)
	rows, err = s.ListWorkbenches(owner, "")
	require.NoError(t, err)
	require.Equal(t, newer.ID, rows[0].ID)
}
