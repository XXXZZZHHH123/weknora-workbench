package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func nativeWorkbenchDeleteFixture(t *testing.T) (*WorkspaceService, *knowledgeBaseService, context.Context) {
	t.Helper()
	s, _, _ := workspaceFixture(t)
	native := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(s.db), asynqClient: kbDeleteTaskEnqueuer{}}
	s.kbs = native
	ctx := types.WithCaller(ctxWithTenantStorage(1, "local"), types.Caller{TenantID: 1, UserID: "alice", Role: types.TenantRoleOwner})
	return s, native, ctx
}

func TestDeleteKnowledgeBaseDetachesSourceAndKeepsBothWorkbenches(t *testing.T) {
	s, native, ctx := nativeWorkbenchDeleteFixture(t)
	bound, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Primary"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "primary"})
	require.NoError(t, err)
	otherKB := &types.KnowledgeBase{ID: "separate-source", TenantID: 1, Name: "Separate source"}
	require.NoError(t, s.db.Create(otherKB).Error)
	consumer, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Macro consumer"), KnowledgeBaseID: &otherKB.ID, IdempotencyKey: "consumer"})
	require.NoError(t, err)
	// Default is an auxiliary source, not every workbench's primary binding.
	require.NoError(t, s.db.Create(&types.DefaultKnowledgeBaseBinding{TenantID: 1, Kind: macroKnowledgeBaseKind, KnowledgeBaseID: "own"}).Error)
	require.NoError(t, native.DeleteKnowledgeBase(ctx, "own"))
	detached, err := s.GetWorkbench(ctx, bound.ID)
	require.NoError(t, err)
	require.Empty(t, detached.KnowledgeBaseID)
	require.False(t, detached.SourceDataAvailable)
	grant, err := s.GetWorkbenchSourceScope(ctx, bound.ID)
	require.NoError(t, err)
	require.Empty(t, grant.Sources)
	rows, err := s.ListWorkbenches(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	remaining, err := s.GetWorkbench(ctx, consumer.ID)
	require.NoError(t, err)
	require.Nil(t, remaining.DefaultKnowledgeBase)
	var archived types.ResearchWorkbench
	require.NoError(t, s.db.Unscoped().First(&archived, "id = ?", bound.ID).Error)
	require.False(t, archived.DeletedAt.Valid)
	require.Equal(t, bound.Name, archived.Name)
	var binding types.DefaultKnowledgeBaseBinding
	require.NoError(t, s.db.First(&binding, "tenant_id = ? AND kind = ?", 1, macroKnowledgeBaseKind).Error)
	require.Equal(t, "own", binding.KnowledgeBaseID, "deletion must retain the reserved system identity")
}

type deleteAfterWorkbenchKBRead struct {
	interfaces.KnowledgeBaseService
	native *knowledgeBaseService
	after  func()
}

func (s *deleteAfterWorkbenchKBRead) GetKnowledgeBaseByIDOnly(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	row, err := s.native.GetKnowledgeBaseByIDOnly(ctx, id)
	if err == nil && s.after != nil {
		after := s.after
		s.after = nil
		after()
	}
	return row, err
}

func TestWorkspaceCreateCannotPersistAfterItsVerifiedKnowledgeBaseIsDeleted(t *testing.T) {
	s, native, ctx := nativeWorkbenchDeleteFixture(t)
	s.kbs = &deleteAfterWorkbenchKBRead{native: native, after: func() {
		// Finish a real source deletion between authorization and insertion.
		require.NoError(t, native.DeleteKnowledgeBase(ctx, "own"))
	}}
	_, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Racing creation"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "after-delete"})
	workspaceStatus(t, err, 404)
	var count int64
	require.NoError(t, s.db.Unscoped().Model(&types.ResearchWorkbench{}).Count(&count).Error)
	require.Zero(t, count, "a failed create must not leave a late orphan after cascade")
}

func TestDeleteKnowledgeBaseServiceRejectsForeignSourceBeforeQueueingCleanup(t *testing.T) {
	s, native, ctx := nativeWorkbenchDeleteFixture(t)
	queue := &recordingKBDeleteEnqueuer{}
	native.asynqClient = queue
	err := native.DeleteKnowledgeBase(ctx, "foreign")
	require.ErrorIs(t, err, repository.ErrKnowledgeBaseNotFound)
	require.Zero(t, queue.calls)
	var row types.KnowledgeBase
	require.NoError(t, s.db.First(&row, "id = ?", "foreign").Error)
	require.Equal(t, uint64(2), row.TenantID)
	var archived int64
	require.NoError(t, s.db.Unscoped().Model(&types.KnowledgeBase{}).Where("deleted_at IS NOT NULL").Count(&archived).Error)
	require.Zero(t, archived)
}

var _ interfaces.KnowledgeBaseService = (*deleteAfterWorkbenchKBRead)(nil)
