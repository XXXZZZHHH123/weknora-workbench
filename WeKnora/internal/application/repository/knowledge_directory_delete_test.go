package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func recursiveDirectoryFixture(t *testing.T) (*gorm.DB, interfaces.KnowledgeRepository, interfaces.KnowledgeFolderDeletionRepository) {
	t.Helper()
	db := setupKnowledgeTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.KnowledgeBase{}, &types.TaskPendingOp{}))
	for _, kb := range []types.KnowledgeBase{{ID: "kb", TenantID: 1}, {ID: "foreign", TenantID: 2}, {ID: "other", TenantID: 1}} {
		require.NoError(t, db.Clauses(clause.OnConflict{DoNothing: true}).Create(&kb).Error)
	}
	repo := NewKnowledgeRepository(db)
	deletion, ok := repo.(interfaces.KnowledgeFolderDeletionRepository)
	require.True(t, ok, "native repository must support durable recursive-directory cleanup")
	return db, repo, deletion
}

func insertRecursiveKnowledge(t *testing.T, db *gorm.DB, tenantID uint64, kbID, path, name string) string {
	t.Helper()
	row := &types.Knowledge{TenantID: tenantID, KnowledgeBaseID: kbID, FolderPath: path,
		FileName: name, Title: name, Type: "file", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, db.Create(row).Error)
	return row.ID
}

func TestRecursiveDirectoryDeleteKeepsReservationUntilAllDocumentsAreGone(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "A_%/nested/empty")
	require.NoError(t, err)
	for _, kb := range []string{"kb", "other"} {
		insertRecursiveKnowledge(t, db, 1, kb, "A_%/nested", "source.md")
	}
	insertRecursiveKnowledge(t, db, 1, "kb", "Axx/nested", "sibling.md")
	insertRecursiveKnowledge(t, db, 2, "foreign", "A_%/nested", "foreign.md")
	// Other generic-queue lanes are neither consumed nor treated as reservations.
	for _, task := range []string{types.TypeWikiIngest, "unrelated:task"} {
		require.NoError(t, db.Create(&types.TaskPendingOp{TenantID: 1, TaskType: task, Scope: "knowledge_base", ScopeID: "kb", Op: "ingest", Payload: json.RawMessage(`{"path":"A_%"}`)}).Error)
	}
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "A_%"))
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "A_%"), "the same directory must be retryable")
	ids, err := deletion.ListKnowledgeFolderDeletionBatch(ctx, 1, "kb", "A_%", 50)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	var conflict *apperrors.AppError
	require.ErrorAs(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "A_%", ""), &conflict)
	require.Equal(t, 409, conflict.HTTPCode)
	require.NoError(t, repo.DeleteKnowledgeList(ctx, 1, ids))
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "A_%", ""))
	var remaining int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).Where("knowledge_base_id = ? AND tenant_id = ?", "kb", 1).Count(&remaining).Error)
	require.Zero(t, remaining)
	require.NoError(t, db.Model(&types.Knowledge{}).Count(&remaining).Error)
	require.Equal(t, int64(3), remaining)
	var tasks []types.TaskPendingOp
	require.NoError(t, db.Find(&tasks).Error)
	require.Len(t, tasks, 2, "only this deletion's reservation may be removed")
	// Tombstones remain recoverable; no physical source row is deleted.
	require.NoError(t, db.Unscoped().Model(&types.Knowledge{}).Count(&remaining).Error)
	require.Equal(t, int64(4), remaining)
}

func TestRecursiveDirectoryReservationGuardsEveryNamespaceWrite(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "Parent/A_%/child")
	require.NoError(t, err)
	inside := insertRecursiveKnowledge(t, db, 1, "kb", "Parent/A_%/child", "inside.md")
	outside := insertRecursiveKnowledge(t, db, 1, "kb", "outside", "outside.md")
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Parent/A_%"))
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "Parent/A_%/new")
	require.ErrorContains(t, err, "删除未完成")
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "Parent", "Elsewhere")
	require.Error(t, err, "renaming an ancestor must not bypass a reserved descendant")
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "outside", "Parent/A_%/incoming")
	require.Error(t, err)
	_, err = repo.UpdateKnowledgeFolderPath(ctx, 1, "kb", []string{outside}, "Parent/A_%/incoming")
	require.Error(t, err)
	_, err = repo.UpdateKnowledgeFolderPath(ctx, 1, "kb", []string{inside}, "outside")
	require.Error(t, err)
	require.Error(t, repo.CreateKnowledge(ctx, &types.Knowledge{ID: "new", TenantID: 1, KnowledgeBaseID: "kb", FolderPath: "Parent/A_%/child", ParseStatus: types.ParseStatusPending}))
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "Parent/Axx/safe")
	require.NoError(t, err, "SQL wildcards in a reserved path are literal")
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "other", "Parent/A_%/safe")
	require.NoError(t, err, "another knowledge base's directory namespace is independent")
	require.Equal(t, "outside", folderPathOf(t, db, outside))
	require.Equal(t, "Parent/A_%/child", folderPathOf(t, db, inside))
}

func TestRecursiveDirectoryFinalizeRollsBackFolderAndReservationTogether(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "Empty/child")
	require.NoError(t, err)
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Empty"))
	require.NoError(t, db.Exec("CREATE TRIGGER reject_directory_finish BEFORE DELETE ON task_pending_ops BEGIN SELECT RAISE(ABORT,'isolated failure'); END").Error)
	require.Error(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", ""))
	var folders, pending int64
	require.NoError(t, db.Model(&types.KnowledgeFolder{}).Count(&folders).Error)
	require.Equal(t, int64(2), folders)
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Equal(t, int64(1), pending)
	require.NoError(t, db.Exec("DROP TRIGGER reject_directory_finish").Error)
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", ""))
}

func TestRecursiveDirectoryDeleteRejectsRootMissingTenantAndDeletedSource(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "Folder")
	require.NoError(t, err)
	for _, path := range []string{"", "../Folder", "Folder//child", "missing"} {
		require.Error(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", path))
	}
	for _, tenant := range []uint64{0, 2} {
		require.Error(t, deletion.BeginKnowledgeFolderDeletion(ctx, tenant, "kb", "Folder"))
	}
	require.NoError(t, db.Where("id = ?", "kb").Delete(&types.KnowledgeBase{}).Error)
	require.Error(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Folder"))
	require.Error(t, repo.CreateKnowledge(ctx, &types.Knowledge{ID: "late", TenantID: 1, KnowledgeBaseID: "kb", FolderPath: "Folder"}))
	require.Error(t, repo.CreateKnowledge(ctx, &types.Knowledge{ID: "late-root", TenantID: 1, KnowledgeBaseID: "kb"}))
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "New")
	require.Error(t, err)
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "Folder", "Renamed")
	require.Error(t, err)
	_, err = repo.UpdateKnowledgeFolderPath(ctx, 1, "kb", []string{"late"}, "Folder")
	require.Error(t, err)
	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Zero(t, pending)
}

func TestRecursiveDirectoryLeaseRejectsConcurrentDeleteAndStaleFinish(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "Empty")
	require.NoError(t, err)
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Empty"))
	first, err := deletion.ClaimKnowledgeFolderDeletion(ctx, 1, "kb", "Empty")
	require.NoError(t, err)
	_, err = deletion.ClaimKnowledgeFolderDeletion(ctx, 1, "kb", "Empty")
	require.Error(t, err)
	require.NoError(t, deletion.ReleaseKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", first))
	second, err := deletion.ClaimKnowledgeFolderDeletion(ctx, 1, "kb", "Empty")
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	// An old request cannot clear a new request's lease or finish its operation.
	require.NoError(t, deletion.ReleaseKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", first))
	require.Error(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", first))
	_, err = deletion.ClaimKnowledgeFolderDeletion(ctx, 1, "kb", "Empty")
	require.Error(t, err)
	// Simulate a crashed request's elapsed lease without sleeping.
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Where("dedup_key = ?", second).Update("claimed_at", time.Now().UTC().Add(-2*time.Minute)).Error)
	third, err := deletion.ClaimKnowledgeFolderDeletion(ctx, 1, "kb", "Empty")
	require.NoError(t, err)
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "Empty", third))
}

func TestRecursiveDirectoryReservationAllowsRootUpload(t *testing.T) {
	_, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "Child")
	require.NoError(t, err)
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Child"))
	require.NoError(t, repo.CreateKnowledge(ctx, &types.Knowledge{ID: "root-upload", TenantID: 1, KnowledgeBaseID: "kb"}))
}

func TestRecursiveDirectoryFinalizeDoesNotRequireDurableFolderRows(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	id := insertRecursiveKnowledge(t, db, 1, "kb", "Legacy/child", "legacy.md")
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Legacy"))
	require.NoError(t, repo.DeleteKnowledgeList(ctx, 1, []string{id}))
	// Legacy cleanup may have no durable rows by the time the last file is gone.
	require.NoError(t, db.Where("tenant_id = ? AND knowledge_base_id = ?", 1, "kb").Delete(&types.KnowledgeFolder{}).Error)
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "Legacy", ""))
	var pending int64
	require.NoError(t, db.Model(&types.TaskPendingOp{}).Count(&pending).Error)
	require.Zero(t, pending)
}

func TestRecursiveDirectoryReservationRejectsTransferEntryAndNewMoveClaims(t *testing.T) {
	db, repo, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	row := &types.Knowledge{ID: "moving-source", TenantID: 1, KnowledgeBaseID: "kb", FolderPath: "SharedPath", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, repo.CreateKnowledge(ctx, row))
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "other", "SharedPath")
	require.NoError(t, err)
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "other", "SharedPath"))
	started := *row
	started.ParseStatus = types.ParseStatusProcessing
	require.NoError(t, repo.UpdateKnowledgeForTransfer(ctx, row, &started))
	finished := started
	finished.KnowledgeBaseID = "other"
	finished.ParseStatus = types.ParseStatusCompleted
	require.Error(t, repo.UpdateKnowledgeForTransfer(ctx, &started, &finished))
	stored, err := repo.GetKnowledgeByID(ctx, 1, row.ID)
	require.NoError(t, err)
	require.Equal(t, "kb", stored.KnowledgeBaseID)
	// An admitted source move may complete after a source reservation appears,
	// so it cannot leave the source delete and transfer waiting on one another.
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "SharedPath"))
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "other", "SharedPath", ""))
	require.NoError(t, repo.UpdateKnowledgeForTransfer(ctx, &started, &finished))
	require.NoError(t, deletion.FinishKnowledgeFolderDeletion(ctx, 1, "kb", "SharedPath", ""))
	late := &types.Knowledge{ID: "late-move", TenantID: 1, KnowledgeBaseID: "kb", FolderPath: "Reserved", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, repo.CreateKnowledge(ctx, late))
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Reserved"))
	claim := *late
	claim.ParseStatus = types.ParseStatusProcessing
	require.Error(t, repo.UpdateKnowledgeForTransfer(ctx, late, &claim))
	stored = &types.Knowledge{}
	require.NoError(t, db.First(stored, "id = ?", late.ID).Error)
	require.Equal(t, types.ParseStatusCompleted, stored.ParseStatus)
}

func TestRecursiveDirectoryBatchIsBoundedAndIncludesInterruptedDeletingRows(t *testing.T) {
	db, _, deletion := recursiveDirectoryFixture(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		id := insertRecursiveKnowledge(t, db, 1, "kb", "Batch/child", "source.md")
		if i == 0 {
			require.NoError(t, db.Model(&types.Knowledge{}).Where("id = ?", id).Update("parse_status", types.ParseStatusDeleting).Error)
		}
	}
	require.NoError(t, deletion.BeginKnowledgeFolderDeletion(ctx, 1, "kb", "Batch"))
	ids, err := deletion.ListKnowledgeFolderDeletionBatch(ctx, 1, "kb", "Batch", 3)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	require.NoError(t, db.Model(&types.Knowledge{}).Where("id IN ?", ids).Update("parse_status", types.ParseStatusDeleting).Error)
	ids, err = deletion.ListKnowledgeFolderDeletionBatch(ctx, 1, "kb", "Batch", 3)
	require.NoError(t, err)
	require.Len(t, ids, 3, "interrupted cleanup stays visible to retries")
	for _, limit := range []int{0, -1, 1001} {
		_, err = deletion.ListKnowledgeFolderDeletionBatch(ctx, 1, "kb", "Batch", limit)
		require.Error(t, err)
	}
}
