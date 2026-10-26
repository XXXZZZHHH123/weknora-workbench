package access

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCentralKnowledgeBaseCapsContributorWritesWithoutOpeningPrivateReads(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "center", TenantID: 7, CreatorID: "contributor", SystemRole: types.KnowledgeBaseSystemRoleCentral, AccessPolicy: types.NewResourceAccessPolicy()}
	caller := types.Caller{TenantID: 7, UserID: "contributor", Role: types.TenantRoleContributor}
	ctx := types.WithCaller(context.Background(), caller)
	grant, err := ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleViewer, grant.Permission)
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	require.ErrorIs(t, err, ErrForbidden)
	caller.UserID = "other"
	ctx = types.WithCaller(context.Background(), caller)
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleViewer, nil, nil)
	require.ErrorIs(t, err, ErrForbidden)
	caller.Role = types.TenantRoleAdmin
	ctx = types.WithCaller(context.Background(), caller)
	_, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	require.ErrorIs(t, err, ErrForbidden, "center administration cannot open an unreadable private KB")
	kb.AccessPolicy.SpacePermission = types.ResourcePermissionViewer
	grant, err = ResolveKB(ctx, KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	require.NoError(t, err)
	require.Equal(t, types.OrgRoleAdmin, grant.Permission)
}
