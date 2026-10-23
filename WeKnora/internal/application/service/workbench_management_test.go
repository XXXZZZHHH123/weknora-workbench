package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceWorkbenchManagementProjectionMatchesExistingWritePolicy(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("creator", 1, types.TenantRoleContributor)
	created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Source-bound research"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "manage-projection"})
	require.NoError(t, err)
	require.True(t, created.CanManage)
	require.NoError(t, s.db.Model(created).Update("access_policy", nil).Error)
	for _, tc := range []struct {
		name, user string
		role       types.TenantRole
		canManage  bool
	}{
		{name: "creator contributor", user: "creator", role: types.TenantRoleContributor, canManage: true},
		{name: "creator demoted to viewer", user: "creator", role: types.TenantRoleViewer},
		{name: "peer contributor", user: "peer", role: types.TenantRoleContributor},
		{name: "peer admin", user: "peer", role: types.TenantRoleAdmin, canManage: true},
		{name: "peer owner", user: "peer", role: types.TenantRoleOwner, canManage: true},
		{name: "peer viewer", user: "peer", role: types.TenantRoleViewer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := workspaceContext(tc.user, 1, tc.role)
			loaded, err := s.GetWorkbench(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, tc.canManage, loaded.CanManage)
			rows, err := s.ListWorkbenches(ctx, "own")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.canManage, rows[0].CanManage)
			updated, err := s.UpdateWorkbench(ctx, created.ID, WorkspaceInput{Name: workspaceString("Managed research")})
			if tc.canManage {
				require.NoError(t, err)
				require.True(t, updated.CanManage)
			} else {
				workspaceStatus(t, err, 403)
			}
			data, err := json.Marshal(loaded)
			require.NoError(t, err)
			var public map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &public))
			require.Contains(t, public, "can_manage")
			require.NotContains(t, public, "user_id")
			require.NotContains(t, public, "tenant_id")
		})
	}
	_, err = s.GetWorkbench(workspaceContext("creator", 2, types.TenantRoleOwner), created.ID)
	workspaceStatus(t, err, 404)
}
