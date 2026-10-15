package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeDirectoryWritesRequireKBGrant(t *testing.T) {
	f := newDocumentWriteFixture(t)
	row, err := f.svc.CreateKnowledgeFolder(f.ctx, "kb", "Empty/child")
	require.NoError(t, err)
	require.Equal(t, uint64(7), row.TenantID)
	_, err = f.svc.CreateKnowledgeFolder(f.ctx, "other", "Empty")
	requireForbiddenWrite(t, err)
	requireForbiddenWrite(t, f.svc.DeleteKnowledgeFolder(f.ctx, "other", "Empty/child"))
	caller := types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer}
	ctx := types.WithCaller(context.Background(), caller)
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, f.kbs.values["kb"], types.OrgRoleViewer, nil, nil)
	require.NoError(t, err)
	ctx = grant.Context(ctx)
	_, err = f.svc.CreateKnowledgeFolder(ctx, "kb", "NotAllowed")
	requireForbiddenWrite(t, err)
	requireForbiddenWrite(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Empty/child"))
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Empty/child"))
	tree, err := f.svc.ListKnowledgeFolderTree(f.ctx, "kb")
	require.NoError(t, err)
	found := false
	for _, folder := range tree.Folders {
		if folder.Path == "Empty" {
			found = true
			require.Empty(t, folder.Children)
		}
	}
	require.True(t, found)
	for _, path := range []string{"", "../bad", "bad//path"} {
		_, err = f.svc.CreateKnowledgeFolder(f.ctx, "kb", path)
		require.Error(t, err)
	}
}
