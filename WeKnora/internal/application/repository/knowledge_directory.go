package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func folderNamespace(tx *gorm.DB, tenantID uint64, kbID string) error {
	// Serialize directory namespace edits, including creation in an empty KB.
	// SQLite needs its write lock before the first read to avoid upgrade races.
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", fmt.Sprintf("knowledge-folders:%d:%s", tenantID, kbID)).Error; err != nil {
			return err
		}
	} else if err := tx.Exec("UPDATE knowledge_folders SET path = path WHERE tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).Error; err != nil {
		return err
	}
	// A KB delete clears its pending operations. Rechecking the source while
	// holding its row lock stops old writes from treating that as permission to
	// recreate files/directories in a soft-deleted KB.
	return lockDirectoryKnowledgeBase(tx, tenantID, kbID)
}

func directoryScope(tx *gorm.DB, tenantID uint64, kbID string) *gorm.DB {
	return tx.Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
}
func directorySubtree(tx *gorm.DB, column, path string) *gorm.DB {
	return tx.Where("("+column+" = ? OR "+column+" LIKE ? ESCAPE ?)", path, escapeLikeKeyword(path)+"/%", likeEscapeChar)
}

func folderExists(tx *gorm.DB, tenantID uint64, kbID, path string) (bool, error) {
	var count int64
	if err := directorySubtree(directoryScope(tx.Model(&types.KnowledgeFolder{}), tenantID, kbID), "path", path).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	err := directorySubtree(directoryScope(tx.Model(&types.Knowledge{}), tenantID, kbID), "folder_path", path).Count(&count).Error
	return count > 0, err
}

func ensureFolderAncestors(tx *gorm.DB, tenantID uint64, kbID, path string) error {
	for _, path := range directoryPaths(path) {
		row := types.KnowledgeFolder{ID: uuid.NewString(), TenantID: tenantID, KnowledgeBaseID: kbID, Path: path}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "knowledge_base_id"}, {Name: "path"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
func directoryPaths(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	rows := make([]string, 0, len(parts))
	for i := range parts {
		rows = append(rows, strings.Join(parts[:i+1], "/"))
	}
	return rows
}

func (r *knowledgeRepository) CreateKnowledgeFolder(ctx context.Context, tenantID uint64, kbID, path string) (*types.KnowledgeFolder, error) {
	if path == "" || path != types.NormalizeKnowledgeFolderPath(path) {
		return nil, apperrors.NewBadRequestError("文件夹路径无效")
	}
	var row types.KnowledgeFolder
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		if err := rejectFolderDeletion(tx, tenantID, kbID, []string{path}, false); err != nil {
			return err
		}
		exists, err := folderExists(tx, tenantID, kbID, path)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError("该文件夹已存在")
		}
		if err := ensureFolderAncestors(tx, tenantID, kbID, path); err != nil {
			return err
		}
		return directoryScope(tx, tenantID, kbID).Where("path = ?", path).First(&row).Error
	})
	return &row, err
}

func (r *knowledgeRepository) DeleteKnowledgeFolder(ctx context.Context, tenantID uint64, kbID, path string) error {
	if path == "" || path != types.NormalizeKnowledgeFolderPath(path) {
		return apperrors.NewBadRequestError("不能删除知识库根目录或无效路径")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		if err := rejectFolderDeletion(tx, tenantID, kbID, []string{path}, true); err != nil {
			return err
		}
		var documents, children int64
		if err := directorySubtree(directoryScope(tx.Model(&types.Knowledge{}), tenantID, kbID), "folder_path", path).Count(&documents).Error; err != nil {
			return err
		}
		if err := directoryScope(tx.Model(&types.KnowledgeFolder{}), tenantID, kbID).Where("path LIKE ? ESCAPE ?", escapeLikeKeyword(path)+"/%", likeEscapeChar).Count(&children).Error; err != nil {
			return err
		}
		if documents > 0 || children > 0 {
			return apperrors.NewConflictError("文件夹非空，请先移出文件和子文件夹")
		}
		result := directoryScope(tx, tenantID, kbID).Where("path = ?", path).Delete(&types.KnowledgeFolder{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperrors.NewNotFoundError("文件夹不存在")
		}
		return nil
	})
}

func (r *knowledgeRepository) renameKnowledgeDirectory(ctx context.Context, tenantID uint64, kbID, from, to string) (int64, error) {
	if from == "" || to == "" || from != types.NormalizeKnowledgeFolderPath(from) || to != types.NormalizeKnowledgeFolderPath(to) {
		return 0, apperrors.NewBadRequestError("文件夹路径无效")
	}
	if from == to {
		return 0, nil
	}
	if strings.HasPrefix(to, from+"/") {
		return 0, apperrors.NewBadRequestError("不能移动到自身子目录")
	}
	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := folderNamespace(tx, tenantID, kbID); err != nil {
			return err
		}
		if err := rejectFolderDeletion(tx, tenantID, kbID, []string{from, to}, true); err != nil {
			return err
		}
		var folders []types.KnowledgeFolder
		var files []types.Knowledge
		if err := directorySubtree(directoryScope(tx, tenantID, kbID), "path", from).Find(&folders).Error; err != nil {
			return err
		}
		if err := directorySubtree(directoryScope(tx, tenantID, kbID), "folder_path", from).Select("id", "folder_path").Find(&files).Error; err != nil {
			return err
		}
		if len(folders) == 0 && len(files) == 0 {
			return apperrors.NewNotFoundError("文件夹不存在")
		}
		exists, err := folderExists(tx, tenantID, kbID, to)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError("目标文件夹已存在，不能合并")
		}
		rewrite := func(path string) (string, error) {
			target := to + strings.TrimPrefix(path, from)
			if target != types.NormalizeKnowledgeFolderPath(target) {
				return "", apperrors.NewBadRequestError("改名后的目录层级或路径过长")
			}
			return target, nil
		}
		for _, folder := range folders {
			target, err := rewrite(folder.Path)
			if err != nil {
				return err
			}
			if err := directoryScope(tx.Model(&types.KnowledgeFolder{}), tenantID, kbID).Where("id = ?", folder.ID).Updates(map[string]any{"path": target, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		for _, file := range files {
			target, err := rewrite(file.FolderPath)
			if err != nil {
				return err
			}
			result := directoryScope(tx.Model(&types.Knowledge{}), tenantID, kbID).Where("id = ?", file.ID).Updates(map[string]any{"folder_path": target, "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			affected += result.RowsAffected
		}
		// Renamed legacy folders also gain a durable identity; data IDs stay intact.
		return ensureFolderAncestors(tx, tenantID, kbID, to)
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}
