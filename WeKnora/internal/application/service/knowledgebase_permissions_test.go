package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKnowledgeBaseMemberPermissionsPersistAndRevoke(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "permissions.db")), &gorm.Config{})
	require.NoError(t, err)
	sql, _ := db.DB()
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.TenantMember{}, &types.ResearchWorkbench{}, &types.Knowledge{}, &types.StoredResource{}, &types.ResourceBinding{}, &types.ResourceAccessGrant{}, &types.CentralDataset{}, &types.ResearchWorkbenchSourceBinding{}))
	repo := repository.NewKnowledgeBaseRepository(db)
	s := &knowledgeBaseService{repo: repo}
	for _, member := range []types.TenantMember{
		{TenantID: 7, UserID: "alice", Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "bob", Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{TenantID: 7, UserID: "visitor", Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
		{TenantID: 8, UserID: "outsider", Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
	} {
		require.NoError(t, db.Create(&member).Error)
	}
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", Name: "Personal", AccessPolicy: types.NewResourceAccessPolicy()}
	require.NoError(t, repo.CreateKnowledgeBase(context.Background(), kb))
	resource := &types.StoredResource{ID: "resource", TenantID: 7, Handle: "a-resource", PhysicalPath: "local://7/source.pdf", LocationHash: "resource-location", State: types.ResourceStateActive}
	require.NoError(t, db.Create(resource).Error)
	require.NoError(t, db.Create(&types.Knowledge{ID: "source", TenantID: 7, KnowledgeBaseID: kb.ID}).Error)
	require.NoError(t, db.Create(&types.ResourceBinding{ResourceID: resource.ID, TenantID: 7, OwnerType: types.ResourceOwnerKnowledge, OwnerID: "source", Relation: types.ResourceRelationSourceFile}).Error)
	grant := &types.ResourceAccessGrant{TokenHash: "test-token-hash", ResourceID: resource.ID, ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(grant).Error)
	alice := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "alice", Role: types.TenantRoleContributor})
	admin := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "admin", Role: types.TenantRoleAdmin})
	_, err = s.GetKnowledgeBasePermissions(admin, kb.ID)
	workspaceStatus(t, err, 403)
	for _, uid := range []string{"outsider", "unknown", "visitor"} {
		_, err = s.UpdateKnowledgeBasePermissions(alice, kb.ID, &types.ResourceAccessPolicy{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: uid, Permission: "editor"}}})
		workspaceStatus(t, err, 400)
	}
	policy := &types.ResourceAccessPolicy{SpacePermission: "none", Members: []types.ResourceMemberPermission{{UserID: "bob", Permission: "editor"}}}
	_, err = s.UpdateKnowledgeBasePermissions(admin, kb.ID, policy)
	workspaceStatus(t, err, 403)
	_, err = s.UpdateKnowledgeBasePermissions(alice, kb.ID, policy)
	require.NoError(t, err)
	require.NoError(t, db.First(grant, "id = ?", grant.ID).Error)
	require.NotNil(t, grant.RevokedAt, "a previously issued capability URL must be invalidated when its source ACL changes")
	loaded, err := repo.GetKnowledgeBaseByID(context.Background(), kb.ID)
	require.NoError(t, err)
	require.Equal(t, policy, loaded.AccessPolicy)
	bob := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "bob", Role: types.TenantRoleContributor})
	require.Len(t, filterLocalKBPolicies(bob, []*types.KnowledgeBase{loaded}), 1)
	_, err = s.UpdateKnowledgeBasePermissions(bob, kb.ID, types.NewResourceAccessPolicy())
	workspaceStatus(t, err, 403)
	_, err = s.UpdateKnowledgeBasePermissions(alice, kb.ID, types.NewResourceAccessPolicy())
	require.NoError(t, err)
	loaded, err = repo.GetKnowledgeBaseByID(context.Background(), kb.ID)
	require.NoError(t, err)
	require.Empty(t, filterLocalKBPolicies(bob, []*types.KnowledgeBase{loaded}))
	legacy := &types.KnowledgeBase{ID: "legacy", TenantID: 7, CreatorID: "alice"}
	require.Len(t, filterLocalKBPolicies(admin, []*types.KnowledgeBase{legacy}), 1)
	for _, row := range []types.ResearchWorkbench{
		{ID: "undisclosed", TenantID: 7, UserID: "researcher", Name: "Private title", KnowledgeBaseID: kb.ID, IdempotencyKey: "unrequested", AccessPolicy: types.NewResourceAccessPolicy()},
		{ID: "requested", TenantID: 7, UserID: "researcher", Name: "Sent for approval", KnowledgeBaseID: kb.ID, IdempotencyKey: "requested", AccessPolicy: types.NewResourceAccessPolicy(), SourceSharingRequested: true, SourceSharingApproved: true, SourceSharingApprovedBy: "alice", SourceSharingModules: []string{"market", "diagram"}},
	} {
		require.NoError(t, db.Create(&row).Error)
	}
	permissions, err := s.GetKnowledgeBasePermissions(alice, kb.ID)
	require.NoError(t, err)
	require.Len(t, permissions.PendingWorkbenches, 1)
	require.Equal(t, "requested", permissions.PendingWorkbenches[0].ID)
	require.Equal(t, []string{"market", "diagram"}, permissions.PendingWorkbenches[0].SourceSharingModules)
	require.True(t, permissions.PendingWorkbenches[0].SourceSharingApproved)
	require.NoError(t, db.Model(&types.ResearchWorkbench{}).Where("id = ?", "requested").Update("source_sharing_approved_by", "former-owner").Error)
	permissions, err = s.GetKnowledgeBasePermissions(alice, kb.ID)
	require.NoError(t, err)
	require.Empty(t, permissions.PendingWorkbenches[0].SourceSharingModules)
	require.False(t, permissions.PendingWorkbenches[0].SourceSharingApproved)
}

func TestResourceURLRespectsCurrentKnowledgeBasePolicy(t *testing.T) {
	catalog, db := newResourceCatalogForTest(t)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}, &types.WikiPage{}))
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "alice", Role: types.TenantRoleContributor})
	ref, err := catalog.Register(ctx, 7, "local://7/files/source.pdf", interfaces.ResourceRegistration{})
	require.NoError(t, err)
	kb := &types.KnowledgeBase{ID: "private", TenantID: 7, CreatorID: "alice", AccessPolicy: types.NewResourceAccessPolicy()}
	require.NoError(t, db.Create(kb).Error)
	require.NoError(t, db.Create(&types.Knowledge{ID: "source", TenantID: 7, KnowledgeBaseID: kb.ID, FilePath: ref}).Error)
	require.NoError(t, catalog.Bind(ctx, ref, types.ResourceOwnerKnowledge, "source", types.ResourceRelationSourceFile))
	validator := catalog.(interface {
		AuthorizeResourceReference(context.Context, string) error
	})
	require.NoError(t, validator.AuthorizeResourceReference(ctx, ref))
	for _, role := range []types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin, types.TenantRoleOwner} {
		other := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "bob", Role: role})
		for _, path := range []string{ref, "local://7/files/source.pdf"} {
			require.ErrorIs(t, validator.AuthorizeResourceReference(other, path), access.ErrForbidden)
		}
	}
	kb.AccessPolicy.Members = []types.ResourceMemberPermission{{UserID: "bob", Permission: "viewer"}}
	require.NoError(t, db.Save(kb).Error)
	bob := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "bob", Role: types.TenantRoleContributor})
	require.NoError(t, validator.AuthorizeResourceReference(bob, ref))
	kb.AccessPolicy.Members = nil
	require.NoError(t, db.Save(kb).Error)
	require.ErrorIs(t, validator.AuthorizeResourceReference(bob, ref), access.ErrForbidden)
}

func TestResourceURLWithMissingKnowledgeOriginFailsClosed(t *testing.T) {
	catalog, db := newResourceCatalogForTest(t)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.Knowledge{}))
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "alice", Role: types.TenantRoleContributor})
	ref, err := catalog.Register(ctx, 7, "local://7/files/orphan.pdf", interfaces.ResourceRegistration{})
	require.NoError(t, err)
	require.NoError(t, catalog.Bind(ctx, ref, types.ResourceOwnerKnowledge, "removed-doc", types.ResourceRelationSourceFile))
	validator := catalog.(interface {
		AuthorizeResourceReference(context.Context, string) error
	})
	for _, path := range []string{ref, "local://7/files/orphan.pdf"} {
		require.ErrorIs(t, validator.AuthorizeResourceReference(ctx, path), access.ErrForbidden,
			"a knowledge binding with a missing origin cannot become an unrestricted artifact")
	}
}
