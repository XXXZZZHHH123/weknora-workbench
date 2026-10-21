package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourcePermissionPolicy(t *testing.T) {
	policy := NewResourceAccessPolicy()
	owner := Caller{TenantID: 1, UserID: "owner", Role: TenantRoleContributor}
	require.Equal(t, ResourcePermissionOwner, ResourcePermissionFor(policy, "owner", owner))
	for _, role := range []TenantRole{TenantRoleOwner, TenantRoleAdmin, TenantRoleContributor, TenantRoleViewer} {
		other := Caller{TenantID: 1, UserID: "other", Role: role}
		require.Equal(t, ResourcePermissionNone, ResourcePermissionFor(policy, "owner", other))
	}
	policy.Members = []ResourceMemberPermission{{UserID: "reader", Permission: ResourcePermissionViewer}, {UserID: "editor", Permission: ResourcePermissionEditor}}
	for _, tc := range []struct {
		user string
		role TenantRole
		want string
	}{
		{"reader", TenantRoleAdmin, ResourcePermissionViewer},
		{"editor", TenantRoleContributor, ResourcePermissionEditor},
		{"editor", TenantRoleViewer, ResourcePermissionViewer},
		{"owner", TenantRoleViewer, ResourcePermissionViewer},
		{"editor", "", ResourcePermissionNone},
	} {
		require.Equal(t, tc.want, ResourcePermissionFor(policy, "owner", Caller{TenantID: 1, UserID: tc.user, Role: tc.role}))
	}
	policy.SpacePermission = ResourcePermissionEditor
	require.Equal(t, ResourcePermissionEditor, ResourcePermissionFor(policy, "owner", Caller{TenantID: 1, UserID: "reader", Role: TenantRoleContributor}), "selected read cannot lower an all-space editor grant")
	require.Equal(t, ResourcePermissionViewer, ResourcePermissionFor(policy, "owner", Caller{TenantID: 1, UserID: "any", Role: TenantRoleViewer}))
	require.Equal(t, "", ResourcePermissionFor(nil, "owner", owner), "legacy policy must remain distinguishable from private")
}

func TestResourcePolicyValidation(t *testing.T) {
	for _, policy := range []*ResourceAccessPolicy{
		nil,
		{SpacePermission: "owner"},
		{SpacePermission: "none", Members: []ResourceMemberPermission{{UserID: "a", Permission: "owner"}}},
		{SpacePermission: "none", Members: []ResourceMemberPermission{{UserID: "a", Permission: "viewer"}, {UserID: "a", Permission: "editor"}}},
		{SpacePermission: "none", Members: []ResourceMemberPermission{{UserID: " a ", Permission: "viewer"}}},
	} {
		require.Error(t, ValidateResourceAccessPolicy(policy))
	}
	require.NoError(t, ValidateResourceAccessPolicy(NewResourceAccessPolicy()))
}
