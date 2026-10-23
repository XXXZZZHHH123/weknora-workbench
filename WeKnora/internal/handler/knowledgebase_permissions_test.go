package handler

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeBaseCapabilitiesKeepLegacyManagementSeparateFromPolicyOwner(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		userID               string
		role                 types.TenantRole
		policy               *types.ResourceAccessPolicy
		manage, managePolicy bool
	}{
		{name: "legacy administrator", userID: "admin", role: types.TenantRoleAdmin, manage: true},
		{name: "legacy owner", userID: "alice", role: types.TenantRoleContributor, manage: true, managePolicy: true},
		{name: "private unlisted administrator", userID: "admin", role: types.TenantRoleAdmin, policy: types.NewResourceAccessPolicy()},
		{name: "private owner", userID: "alice", role: types.TenantRoleContributor, policy: types.NewResourceAccessPolicy(), manage: true, managePolicy: true},
		{name: "owner downgraded to viewer", userID: "alice", role: types.TenantRoleViewer, policy: types.NewResourceAccessPolicy()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: tc.userID, Role: tc.role})
			kb := &types.KnowledgeBase{ID: "kb", TenantID: 7, CreatorID: "alice", AccessPolicy: tc.policy}
			capabilities := kbCapabilityExtras(ctx, kb)
			require.Equal(t, tc.manage, capabilities["can_manage"])
			require.Equal(t, tc.managePolicy, capabilities["can_manage_permissions"])
		})
	}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 8, UserID: "alice", Role: types.TenantRoleAdmin})
	foreign := kbCapabilityExtras(ctx, &types.KnowledgeBase{ID: "kb", TenantID: 7, CreatorID: "alice"})
	require.Equal(t, false, foreign["can_manage"])
	require.Equal(t, false, foreign["can_manage_permissions"])
	_, forcedEdit := foreign["can_edit"]
	require.False(t, forcedEdit, "organization-shared editor permissions are resolved independently")
}
