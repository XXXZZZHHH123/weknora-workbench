package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This is a durable reservation, not an asynchronously consumed task. Wiki
// consumers use different task_type lanes. The authorized DELETE request drains
// the directory synchronously; another request can resume after interruption.
const folderDeletionTaskType = "knowledge:folder_delete"

func folderDeletionRows(tx *gorm.DB, tenantID uint64, kbID string) *gorm.DB {
	return tx.Model(&types.TaskPendingOp{}).Where(
		"tenant_id = ? AND task_type = ? AND scope = ? AND scope_id = ? AND op = ?",
		tenantID, folderDeletionTaskType, "knowledge_base", kbID, "delete")
}

func folderDeletionPathColumn(tx *gorm.DB) string {
	if tx.Dialector.Name() == "postgres" {
		return "(payload->>'path')"
	}
	return "json_extract(payload, '$.path')"
}

func validFolderDeletionPath(tenantID uint64, kbID, path string) error {
	if tenantID == 0 || strings.TrimSpace(kbID) == "" || path == "" || path != types.NormalizeKnowledgeFolderPath(path) {
		return apperrors.NewBadRequestError("不能删除知识库根目录或无效路径")
	}
	return nil
}

// lockDirectoryKnowledgeBase prevents a previously authorized request from
// creating work after the source KB has been deleted. Call after folderNamespace
// (which also reserves SQLite's writer before reads).
func lockDirectoryKnowledgeBase(tx *gorm.DB, tenantID uint64, kbID string) error {
	var kb types.KnowledgeBase
	err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", tenantID, kbID).Take(&kb).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperrors.NewNotFoundError("知识库不存在")
	}
	return err
}

// rejectFolderDeletion serializes namespace writes with Begin/Finish. Paths
// under a reservation cannot be populated or moved away while cleanup runs.
// Ancestor rename/delete also checks descendants so it cannot hide the marker.
func rejectFolderDeletion(tx *gorm.DB, tenantID uint64, kbID string, paths []string, descendants bool) error {
	column := folderDeletionPathColumn(tx)
	conditions := tx.Session(&gorm.Session{NewDB: true})
	ancestors := make(map[string]bool)
	for _, path := range paths {
		for _, ancestor := range directoryPaths(path) {
			ancestors[ancestor] = true
		}
		if descendants && path != "" {
			conditions = conditions.Or(column+" LIKE ? ESCAPE ?", escapeLikeKeyword(path)+"/%", likeEscapeChar)
		}
	}
	if len(ancestors) > 0 {
		values := make([]string, 0, len(ancestors))
		for path := range ancestors {
			values = append(values, path)
		}
		conditions = conditions.Or(column+" IN ?", values)
	}
	if len(ancestors) == 0 {
		return nil // The KB root itself cannot be reserved for folder deletion.
	}
	var count int64
	if err := folderDeletionRows(tx, tenantID, kbID).Where(conditions).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apperrors.NewConflictError("文件夹删除未完成，请重试删除后再修改该目录")
	}
	return nil
}

func (r *knowledgeRepository) BeginKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path string) error {
	if err := validFolderDeletionPath(tenantID, kbID, path); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		var existing int64
		if err := folderDeletionRows(tx, tenantID, kbID).Where(folderDeletionPathColumn(tx)+" = ?", path).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil // Retry this exact deletion, never another directory's one.
		}
		if err := rejectFolderDeletion(tx, tenantID, kbID, []string{path}, true); err != nil {
			return err
		}
		exists, err := folderExists(tx, tenantID, kbID, path)
		if err != nil {
			return err
		}
		if !exists {
			return apperrors.NewNotFoundError("文件夹不存在")
		}
		// Keep even a legacy implicit directory visible if a later batch fails.
		if err := ensureFolderAncestors(tx, tenantID, kbID, path); err != nil {
			return err
		}
		payload, err := json.Marshal(struct {
			Path string `json:"path"`
		}{Path: path})
		if err != nil {
			return err
		}
		return tx.Create(&types.TaskPendingOp{
			TenantID: tenantID, TaskType: folderDeletionTaskType, Scope: "knowledge_base",
			ScopeID: kbID, Op: "delete", Payload: payload, EnqueuedAt: time.Now().UTC(),
		}).Error
	})
}

func (r *knowledgeRepository) ListKnowledgeFolderDeletionBatch(ctx context.Context, tenantID uint64, kbID, path string, limit int) ([]string, error) {
	if err := validFolderDeletionPath(tenantID, kbID, path); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, apperrors.NewBadRequestError("删除批次大小无效")
	}
	var ids []string
	// Unlike the document list, do not exclude parse_status=deleting: a failed
	// vector/chunk cleanup must be resumed before its source row can disappear.
	err := directorySubtree(directoryScope(r.db.WithContext(ctx).Model(&types.Knowledge{}), tenantID, kbID), "folder_path", path).
		Order("id ASC").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

// Claim uses the existing pending-op lease columns. Requests normally release
// their lease on return; expiry permits retry after a crashed process. The
// request's cleanup deadline is shorter than this lease.
func (r *knowledgeRepository) ClaimKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path string) (string, error) {
	if err := validFolderDeletionPath(tenantID, kbID, path); err != nil {
		return "", err
	}
	lease := uuid.NewString()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		var row types.TaskPendingOp
		if err := folderDeletionRows(tx, tenantID, kbID).Where(folderDeletionPathColumn(tx)+" = ?", path).Take(&row).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if row.ClaimedAt != nil && row.ClaimedAt.After(now.Add(-time.Minute)) {
			return apperrors.NewConflictError("文件夹正在删除，请稍后重试")
		}
		return tx.Model(&types.TaskPendingOp{}).Where("id = ?", row.ID).
			Updates(map[string]any{"claimed_at": now, "dedup_key": lease}).Error
	})
	return lease, err
}

func (r *knowledgeRepository) ReleaseKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path, lease string) error {
	if err := validFolderDeletionPath(tenantID, kbID, path); err != nil {
		return err
	}
	if lease == "" {
		return apperrors.NewBadRequestError("删除租约无效")
	}
	return folderDeletionRows(r.db.WithContext(ctx), tenantID, kbID).
		Where(folderDeletionPathColumn(r.db)+" = ? AND dedup_key = ?", path, lease).
		Updates(map[string]any{"claimed_at": nil, "dedup_key": ""}).Error
}

func (r *knowledgeRepository) FinishKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path, lease string) error {
	if err := validFolderDeletionPath(tenantID, kbID, path); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		reservation := folderDeletionRows(tx, tenantID, kbID).Where(folderDeletionPathColumn(tx)+" = ? AND dedup_key = ?", path, lease)
		var pending, documents int64
		if err := reservation.Count(&pending).Error; err != nil {
			return err
		}
		if pending == 0 {
			return apperrors.NewConflictError("文件夹删除记录已失效，请刷新后重试")
		}
		if err := directorySubtree(directoryScope(tx.Model(&types.Knowledge{}), tenantID, kbID), "folder_path", path).Count(&documents).Error; err != nil {
			return err
		}
		if documents > 0 {
			return apperrors.NewConflictError("文件夹删除未完成，可重试；目录中仍有文件")
		}
		if err := directorySubtree(directoryScope(tx, tenantID, kbID), "path", path).Delete(&types.KnowledgeFolder{}).Error; err != nil {
			return err
		}
		return reservation.Delete(&types.TaskPendingOp{}).Error
	})
}
