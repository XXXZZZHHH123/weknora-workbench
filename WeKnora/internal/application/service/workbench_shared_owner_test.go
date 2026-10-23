package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchSharedOwnerComesFromStoredWorkbench(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	row := permissionWorkbench(t, s, "owner-alice")
	policy := types.NewResourceAccessPolicy()
	policy.SpacePermission = types.ResourcePermissionViewer
	_, err := s.UpdateWorkbenchPermissions(workspaceContext("owner-alice", 1, types.TenantRoleContributor), row.ID, policy)
	require.NoError(t, err)
	loaded, err := s.GetWorkbench(workspaceContext("viewer-bob", 1, types.TenantRoleContributor), row.ID)
	require.NoError(t, err)
	encoded, err := json.Marshal(loaded)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	require.Equal(t, "owner-alice", document["owner_id"])
	require.NotContains(t, document, "user_id")
	require.NotContains(t, document, "restricted_view_profiles")
	_, err = s.GetWorkbench(workspaceContext("viewer-bob", 2, types.TenantRoleOwner), row.ID)
	workspaceStatus(t, err, 404)
}
