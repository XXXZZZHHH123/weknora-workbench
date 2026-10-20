package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type directoryDeleteProbe struct {
	interfaces.KnowledgeRepository
	interfaces.KnowledgeFolderDeletionRepository
	batchSizes []int
	afterBatch func(int)
}

type directoryVectorCleanup struct {
	parentChildRetrieveEngine
	calls [][]string
	err   error
}

func (e *directoryVectorCleanup) DeleteByKnowledgeIDList(_ context.Context, ids []string, _ int, _ string) error {
	e.calls = append(e.calls, append([]string(nil), ids...))
	return e.err
}

func (r *directoryDeleteProbe) ListKnowledgeFolderDeletionBatch(ctx context.Context, tenantID uint64, kbID, path string, limit int) ([]string, error) {
	ids, err := r.KnowledgeFolderDeletionRepository.ListKnowledgeFolderDeletionBatch(ctx, tenantID, kbID, path, limit)
	r.batchSizes = append(r.batchSizes, len(ids))
	if r.afterBatch != nil {
		r.afterBatch(len(r.batchSizes))
	}
	return ids, err
}

func newRecursiveDocumentFixture(t *testing.T) (*documentWriteFixture, *directoryDeleteProbe) {
	t.Helper()
	f := newDocumentWriteFixture(t)
	native := repository.NewKnowledgeRepository(f.db)
	deletion, ok := native.(interfaces.KnowledgeFolderDeletionRepository)
	require.True(t, ok)
	probe := &directoryDeleteProbe{KnowledgeRepository: native, KnowledgeFolderDeletionRepository: deletion}
	f.svc.repo = probe
	_, err := native.CreateKnowledgeFolder(f.ctx, 7, "kb", "Folder/child/empty")
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Update("folder_path", "Folder/child").Error)
	return f, probe
}

func directoryRemaining(t *testing.T, f *documentWriteFixture, model any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, f.db.Model(model).Count(&count).Error)
	return count
}

func TestRecursiveFolderDeleteUsesExistingResourceCleanupAndKeepsOtherKB(t *testing.T) {
	f, probe := newRecursiveDocumentFixture(t)
	require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Update("file_path", "local://isolated-source.md").Error)
	// The default operation still refuses populated directories.
	require.Error(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder"))
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
	require.Equal(t, []int{1, 0}, probe.batchSizes)
	require.Equal(t, []string{"local://isolated-source.md"}, f.files.deleted)
	require.Equal(t, 1, f.graph.calls)
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.Knowledge{}))
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.Chunk{}))
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Zero(t, directoryRemaining(t, f, &types.TaskPendingOp{}))
	var archived types.Knowledge
	require.NoError(t, f.db.Unscoped().First(&archived, "id = ?", "doc").Error)
	require.True(t, archived.DeletedAt.Valid)
	var other types.Knowledge
	require.NoError(t, f.db.First(&other, "id = ?", "other-doc").Error)
	require.Equal(t, "other", other.KnowledgeBaseID)
}

func TestRecursiveFolderDeleteFailureKeepsDirectoryReservationAndCanResume(t *testing.T) {
	f, _ := newRecursiveDocumentFixture(t)
	f.graph.err = errors.New("isolated graph cleanup failure")
	err := f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true)
	require.ErrorContains(t, err, "删除未完成")
	require.Equal(t, int64(3), directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.TaskPendingOp{}))
	var document types.Knowledge
	require.NoError(t, f.db.First(&document, "id = ?", "doc").Error)
	require.Equal(t, types.ParseStatusDeleting, document.ParseStatus)
	require.Empty(t, f.files.deleted)
	f.graph.err = nil
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Zero(t, directoryRemaining(t, f, &types.TaskPendingOp{}))
}

func TestRecursiveFolderDeletePagesWithoutSkippingAndResumesAfterInterruption(t *testing.T) {
	f, probe := newRecursiveDocumentFixture(t)
	for i := 0; i < 123; i++ {
		require.NoError(t, f.db.Create(&types.Knowledge{
			ID: fmt.Sprintf("batch-%03d", i), TenantID: 7, KnowledgeBaseID: "kb",
			FolderPath: "Folder/child", ParseStatus: types.ParseStatusCompleted,
		}).Error)
	}
	// Fail on the second read so the first real cleanup batch has committed.
	ctx, cancel := context.WithCancel(f.ctx)
	probe.afterBatch = func(batch int) {
		if batch == 2 {
			cancel()
		}
	}
	err := f.svc.DeleteKnowledgeFolder(ctx, "kb", "Folder", true)
	require.ErrorContains(t, err, "删除未完成")
	remaining := directoryRemaining(t, f, &types.Knowledge{})
	require.Less(t, remaining, int64(125))
	require.Greater(t, remaining, int64(1))
	require.NotZero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.TaskPendingOp{}))
	probe.afterBatch = nil
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
	for _, size := range probe.batchSizes {
		require.LessOrEqual(t, size, 50, "the complete directory must never be loaded in one batch")
	}
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.Knowledge{}))
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Zero(t, directoryRemaining(t, f, &types.TaskPendingOp{}))
}

func TestRecursiveFolderDeleteRejectsUnadmittedSourceAndRootBeforeMutations(t *testing.T) {
	f, _ := newRecursiveDocumentFixture(t)
	for _, path := range []string{"", "../Folder", "Folder//child"} {
		require.Error(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", path, true))
	}
	requireForbiddenWrite(t, f.svc.DeleteKnowledgeFolder(f.ctx, "other", "Folder", true))
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 7)
	requireForbiddenWrite(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Folder", true))
	require.Equal(t, int64(2), directoryRemaining(t, f, &types.Knowledge{}))
	require.Zero(t, directoryRemaining(t, f, &types.TaskPendingOp{}))
}

func TestRecursiveFolderDeleteFinalConcurrentDocumentReturnsConflictAndRetainsDirectory(t *testing.T) {
	f, probe := newRecursiveDocumentFixture(t)
	probe.afterBatch = func(_ int) {
		// Simulate an older writer that does not honor the new namespace guard.
		if len(probe.batchSizes) == 2 {
			require.NoError(t, f.db.Create(&types.Knowledge{ID: "racing", TenantID: 7, KnowledgeBaseID: "kb", FolderPath: "Folder", ParseStatus: types.ParseStatusCompleted}).Error)
		}
	}
	err := f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true)
	var appErr *apperrors.AppError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, 409, appErr.HTTPCode)
	require.ErrorContains(t, err, "删除未完成")
	require.NotZero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	probe.afterBatch = nil
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
}

func TestRecursiveFolderDeleteClearsIndexedDataBeforeDirectoryCanDisappear(t *testing.T) {
	f, _ := newRecursiveDocumentFixture(t)
	require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Update("embedding_model_id", "isolated-embedding").Error)
	engine := &directoryVectorCleanup{err: errors.New("isolated vector cleanup failure")}
	f.svc.retrieveEngine = parentChildRetrieveRegistry{engine: engine}
	f.svc.modelService = parentChildModelService{embedder: parentChildEmbedder{}}
	ctx := context.WithValue(f.ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7, RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{{
		RetrieverType: types.VectorRetrieverType, RetrieverEngineType: types.PostgresRetrieverEngineType,
	}}}})
	require.ErrorContains(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Folder", true), "删除未完成")
	require.Equal(t, [][]string{{"doc"}}, engine.calls)
	require.Equal(t, int64(2), directoryRemaining(t, f, &types.Knowledge{}))
	require.NotZero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	engine.err = nil
	require.NoError(t, f.svc.DeleteKnowledgeFolder(ctx, "kb", "Folder", true))
	require.Equal(t, [][]string{{"doc"}, {"doc"}}, engine.calls)
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
}

func TestRecursiveFolderDeleteSupportsLegacyImplicitDirectories(t *testing.T) {
	f, _ := newRecursiveDocumentFixture(t)
	require.NoError(t, f.db.Where("tenant_id = ? AND knowledge_base_id = ?", 7, "kb").Delete(&types.KnowledgeFolder{}).Error)
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
	require.Equal(t, int64(1), directoryRemaining(t, f, &types.Knowledge{}))
	require.Zero(t, directoryRemaining(t, f, &types.KnowledgeFolder{}))
	require.Zero(t, directoryRemaining(t, f, &types.TaskPendingOp{}))
}

func TestRecursiveFolderDeleteNeverFollowsAStalePageOutsideItsDirectory(t *testing.T) {
	f, probe := newRecursiveDocumentFixture(t)
	probe.afterBatch = func(batch int) {
		if batch == 1 {
			require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Update("folder_path", "Elsewhere").Error)
		}
	}
	require.ErrorContains(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true), "删除未完成")
	require.Equal(t, int64(2), directoryRemaining(t, f, &types.Knowledge{}))
	require.Zero(t, f.graph.calls)
	require.Empty(t, f.files.deleted)
	probe.afterBatch = nil
	require.NoError(t, f.svc.DeleteKnowledgeFolder(f.ctx, "kb", "Folder", true))
	require.Equal(t, int64(2), directoryRemaining(t, f, &types.Knowledge{}))
}
