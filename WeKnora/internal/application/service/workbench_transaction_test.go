package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func singleConnectionWorkbenchFixture(t *testing.T) (*WorkspaceService, *knowledgeBaseService, context.Context) {
	t.Helper()
	s, native, ctx := nativeWorkbenchDeleteFixture(t)
	pool, err := s.db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	return s, native, ctx
}

func TestWorkspaceMutationsCompleteWithSingleConnectionAndNativeKnowledgeBase(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s, _, ctx := singleConnectionWorkbenchFixture(t)
			created, err := s.CreateWorkbench(ctx, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "single-connection"})
			require.NoError(t, err)
			pool, err := s.db.DB()
			require.NoError(t, err)
			waits := pool.Stats().WaitCount
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if operation == "rename" {
				updated, err := s.UpdateWorkbench(bounded, created.ID, WorkspaceInput{Name: workspaceString("Renamed")})
				require.NoError(t, err, "metadata mutation must not wait for a second connection held by its own transaction")
				require.Equal(t, "Renamed", updated.Name)
				require.Equal(t, "own", updated.KnowledgeBaseID)
			} else {
				require.NoError(t, s.DeleteWorkbench(bounded, created.ID), "deletion must not wait for a second connection held by its own transaction")
				var archived types.ResearchWorkbench
				require.NoError(t, s.db.Unscoped().First(&archived, "id = ?", created.ID).Error)
				require.True(t, archived.DeletedAt.Valid)
				var source types.KnowledgeBase
				require.NoError(t, s.db.First(&source, "id = ?", "own").Error)
			}
			require.Equal(t, waits, pool.Stats().WaitCount, "the mutation must reuse its transaction connection")
		})
	}
}

func workspaceMutation(s *WorkspaceService, ctx context.Context, id, operation string) error {
	if operation == "delete" {
		return s.DeleteWorkbench(ctx, id)
	}
	_, err := s.UpdateWorkbench(ctx, id, WorkspaceInput{Name: workspaceString("Changed")})
	return err
}

func TestWorkspaceSingleConnectionMutationPermissions(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		for _, actor := range []struct {
			name, user string
			tenant     uint64
			role       types.TenantRole
			status     int
		}{
			{"other contributor", "peer", 1, types.TenantRoleContributor, 403},
			{"creator viewer", "alice", 1, types.TenantRoleViewer, 403},
			{"other tenant", "alice", 2, types.TenantRoleOwner, 404},
			{"administrator", "peer", 1, types.TenantRoleAdmin, 0},
		} {
			t.Run(operation+"/"+actor.name, func(t *testing.T) {
				s, _, owner := singleConnectionWorkbenchFixture(t)
				created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "permissions"})
				require.NoError(t, err)
				require.NoError(t, s.db.Model(created).Update("access_policy", nil).Error)
				ctx, cancel := context.WithTimeout(workspaceContext(actor.user, actor.tenant, actor.role), time.Second)
				defer cancel()
				err = workspaceMutation(s, ctx, created.ID, operation)
				if actor.status == 0 {
					require.NoError(t, err)
				} else {
					workspaceStatus(t, err, actor.status)
					var current types.ResearchWorkbench
					require.NoError(t, s.db.First(&current, "id = ?", created.ID).Error)
					require.Equal(t, "Original", current.Name)
				}
			})
		}
	}
}

func TestWorkspaceSingleConnectionSharedKnowledgeBaseMutationsRespectRevocation(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		for _, allowed := range []bool{true, false} {
			t.Run(operation+"/"+map[bool]string{true: "shared", false: "revoked"}[allowed], func(t *testing.T) {
				s, _, owner := singleConnectionWorkbenchFixture(t)
				shares := s.shares.(*workspaceShareFixture)
				shares.allowed = true
				created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("shared"), IdempotencyKey: "shared-mutation"})
				require.NoError(t, err)
				require.NoError(t, s.db.Model(created).Update("access_policy", nil).Error)
				shares.allowed = allowed
				ctx, cancel := context.WithTimeout(owner, time.Second)
				defer cancel()
				err = workspaceMutation(s, ctx, created.ID, operation)
				if allowed {
					require.NoError(t, err)
				} else {
					workspaceStatus(t, err, 404)
					var current types.ResearchWorkbench
					require.NoError(t, s.db.First(&current, "id = ?", created.ID).Error)
					require.Equal(t, "Original", current.Name)
				}
			})
		}
	}
}

func TestWorkspaceMutationRechecksCurrentBindingCreatorAndSource(t *testing.T) {
	for _, operation := range []string{"rename", "delete"} {
		for _, race := range []string{"binding", "creator", "source-deleted"} {
			t.Run(operation+"/"+race, func(t *testing.T) {
				s, native, baseContext := singleConnectionWorkbenchFixture(t)
				owner := types.WithCaller(baseContext, types.Caller{UserID: "alice", TenantID: 1, Role: types.TenantRoleContributor})
				created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "racing-mutation"})
				require.NoError(t, err)
				s.kbs = &deleteAfterWorkbenchKBRead{native: native, after: func() {
					switch race {
					case "binding":
						require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", created.ID).Update("knowledge_base_id", "foreign").Error)
					case "creator":
						require.NoError(t, s.db.Model(&types.ResearchWorkbench{}).Where("id = ?", created.ID).Update("user_id", "another-creator").Error)
					case "source-deleted":
						require.NoError(t, native.DeleteKnowledgeBase(owner, "own"))
					}
				}}
				ctx, cancel := context.WithTimeout(owner, time.Second)
				defer cancel()
				err = workspaceMutation(s, ctx, created.ID, operation)
				workspaceStatus(t, err, map[string]int{"binding": 409, "creator": 403, "source-deleted": 404}[race])
				var current types.ResearchWorkbench
				require.NoError(t, s.db.Unscoped().First(&current, "id = ?", created.ID).Error)
				require.Equal(t, "Original", current.Name)
				require.Equal(t, race == "source-deleted", current.DeletedAt.Valid, "a later mutation must never resurrect a cascaded workbench")
			})
		}
	}
}

func TestWorkspaceSingleConnectionLegacyRebindRejectsDeletedTarget(t *testing.T) {
	s, native, owner := singleConnectionWorkbenchFixture(t)
	require.NoError(t, s.db.Create(&types.KnowledgeBase{ID: "target", TenantID: 1, Name: "Target"}).Error)
	created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "legacy-target"})
	require.NoError(t, err)
	require.NoError(t, s.db.Model(created).Update("workspace_mode", types.ResearchWorkbenchModeLegacy).Error)
	s.kbs = &deleteAfterWorkbenchKBRead{native: native, after: func() {
		require.NoError(t, native.DeleteKnowledgeBase(owner, "target"))
	}}
	ctx, cancel := context.WithTimeout(owner, time.Second)
	defer cancel()
	_, err = s.UpdateWorkbench(ctx, created.ID, WorkspaceInput{Name: workspaceString("Changed"), KnowledgeBaseID: workspaceString("target")})
	workspaceStatus(t, err, 404)
	var current types.ResearchWorkbench
	require.NoError(t, s.db.First(&current, "id = ?", created.ID).Error)
	require.Equal(t, "Original", current.Name)
	require.Equal(t, "own", current.KnowledgeBaseID)
}

func TestWorkspaceMutationUsesFreshLockedRowWithoutClobberingConcurrentMetadata(t *testing.T) {
	s, native, owner := singleConnectionWorkbenchFixture(t)
	created, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Original"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "concurrent-description"})
	require.NoError(t, err)
	s.kbs = &deleteAfterWorkbenchKBRead{native: native, after: func() {
		require.NoError(t, s.db.Model(created).Update("description", "Concurrent description").Error)
	}}
	ctx, cancel := context.WithTimeout(owner, time.Second)
	defer cancel()
	updated, err := s.UpdateWorkbench(ctx, created.ID, WorkspaceInput{Name: workspaceString("Changed")})
	require.NoError(t, err)
	require.Equal(t, "Changed", updated.Name)
	require.Equal(t, "Concurrent description", updated.Description)
}

func TestWorkspaceReadableKnowledgeBasePreservesCanceledRequests(t *testing.T) {
	s, _, ctx := singleConnectionWorkbenchFixture(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.readableKB(canceled, "own")
	require.ErrorIs(t, err, context.Canceled)
	deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	_, err = s.readableKB(deadline, "own")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = s.readableKB(ctx, "missing")
	workspaceStatus(t, err, 404)
}
