package access

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type resourcePolicyLookup struct {
	shareLookup
	kb *types.KnowledgeBase
}

func (f *resourcePolicyLookup) LookupKnowledgeBaseAccess(context.Context, string) (*types.KnowledgeBase, error) {
	return f.kb, nil
}

func TestPrivateKBPolicyAtResolverAndSearch(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	lookup := &resourcePolicyLookup{kb: kb}
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor, types.TenantRoleAdmin, types.TenantRoleOwner} {
		caller := types.Caller{UserID: "bob", TenantID: 7, Role: role}
		ctx := types.WithCaller(context.Background(), caller)
		_, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, lookup, nil)
		require.ErrorIs(t, err, ErrForbidden)
		_, err = ResolveKB(ctx, KBRequest{Caller: caller}, &types.KnowledgeBase{ID: kb.ID, TenantID: 7}, types.OrgRoleViewer, lookup, nil)
		require.ErrorIs(t, err, ErrForbidden, "a persisted document reference cannot erase its parent ACL")
		allowed, err := NewKBPermissions(ctx, lookup).Check(kb.ID, 7, types.OrgRoleViewer)
		require.NoError(t, err)
		require.False(t, allowed)
		require.Error(t, AuthorizeKBAccess(ctx, lookup, []*types.KnowledgeBase{kb}))
	}
	caller := types.Caller{UserID: "bob", TenantID: 7, Role: types.TenantRoleContributor}
	ctx := types.WithCaller(context.Background(), caller)
	kb.AccessPolicy.Members = []types.ResourceMemberPermission{{UserID: "bob", Permission: "editor"}}
	grant, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleEditor, lookup, nil)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleEditor, grant.Permission)
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleAdmin, lookup, nil)
	require.ErrorIs(t, err, ErrForbidden, "content editors cannot manage the resource")
	require.NoError(t, AuthorizeKBAccess(ctx, lookup, []*types.KnowledgeBase{kb}))
	kb.AccessPolicy.Members = nil
	allowed, err := NewKBPermissions(grant.Context(ctx), lookup).Check(kb.ID, 7, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, allowed, "a previously issued context grant does not override a freshly revoked policy")
}

func TestPrivateKBMachineScopeCannotReplaceOwnerConsent(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	caller := types.Caller{TenantID: 7, UserID: "alice", Role: types.TenantRoleOwner}
	base := types.WithCaller(context.Background(), caller)
	for _, scope := range []types.TenantAPIKeyScope{
		{FullAccess: true},
		{KnowledgeBaseIDs: []string{kb.ID}, Capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve), string(types.APIKeyCapabilityIngest)}},
	} {
		ctx := types.WithTenantAPIKeyScope(base, scope)
		_, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil)
		require.ErrorIs(t, err, ErrForbidden)
	}
	kb.AccessPolicy.SpacePermission = "viewer"
	ctx := types.WithTenantAPIKeyScope(base, types.TenantAPIKeyScope{KnowledgeBaseIDs: []string{kb.ID}, Capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve)}})
	_, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil)
	require.NoError(t, err)
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	require.ErrorIs(t, err, ErrForbidden)
}

func TestPrivateKBSharedAgentDoesNotReplaceSourceOwnerConsent(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	caller := types.Caller{TenantID: 8, UserID: "bob", Role: types.TenantRoleContributor}
	base := types.WithCaller(context.Background(), caller)
	agent := &types.CustomAgent{ID: "shared-agent", TenantID: 7, Config: types.CustomAgentConfig{KBSelectionMode: "selected", KnowledgeBases: []string{kb.ID}}}
	ctx := WithSharedAgent(base, agent)
	_, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, &agentLookup{any: true})
	require.ErrorIs(t, err, ErrForbidden)
	allowed, err := NewKBPermissions(ctx, nil).CheckKB(kb, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, allowed)
	shares := &shareLookup{permission: types.OrgRoleViewer}
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, shares, nil)
	require.NoError(t, err, "an explicit owner-authorized organization KB share remains usable")
}

func TestPrivateKBAdmittedWorkerRetainsOnlyItsExactTaskScope(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	ctx, err := WithKBTaskWrite(context.Background(), kb, 7)
	require.NoError(t, err)
	allowed, err := NewKBPermissions(ctx, nil).CheckKB(kb, types.OrgRoleViewer)
	require.NoError(t, err)
	require.True(t, allowed)
	other := &types.KnowledgeBase{ID: "other", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	allowed, err = NewKBPermissions(ctx, nil).CheckKB(other, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = NewKBPermissions(context.Background(), nil).CheckKB(kb, types.OrgRoleViewer)
	require.NoError(t, err)
	require.False(t, allowed)
}
