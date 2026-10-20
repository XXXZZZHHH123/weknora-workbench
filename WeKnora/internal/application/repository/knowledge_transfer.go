package repository

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// UpdateKnowledgeForTransfer is a compare-and-swap checkpoint. Full-row Save
// could resurrect a deleted document or overwrite a concurrently moved row.
// Accounting changes are committed with the checkpoint, making retry cleanup
// safe even if the worker crashes between the database and queue operations.
func (r *knowledgeRepository) UpdateKnowledgeForTransfer(ctx context.Context, before, after *types.Knowledge) error {
	if before == nil || after == nil || before.ID == "" || before.ID != after.ID || before.TenantID == 0 ||
		before.TenantID != after.TenantID {
		return fmt.Errorf("invalid knowledge transfer checkpoint")
	}
	// PostgreSQL persists timestamps at microsecond precision. Keep the returned
	// checkpoint identical to storage so the next CAS in this task can match it.
	after.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	var storedUpdatedAt time.Time
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Starting a move and committing its destination participate in the
		// same directory namespace as uploads/deletion. Sort both source/target
		// IDs so two opposite transfers cannot acquire locks in reverse order.
		bindingChanged := before.KnowledgeBaseID != after.KnowledgeBaseID
		starting := before.ParseStatus == types.ParseStatusCompleted && after.ParseStatus == types.ParseStatusProcessing
		if bindingChanged || starting {
			kbIDs := []string{before.KnowledgeBaseID}
			if bindingChanged {
				kbIDs = append(kbIDs, after.KnowledgeBaseID)
			}
			sort.Strings(kbIDs)
			for _, kbID := range kbIDs {
				if err := folderNamespace(tx, before.TenantID, kbID); err != nil {
					return err
				}
			}
			// A move already in progress may drain out of its source directory;
			// rejecting that completion would deadlock source cleanup with the
			// existing moving-knowledge protection. It may never enter a reserved
			// target, and a new move cannot start inside a reserved source.
			if err := rejectFolderDeletion(tx, after.TenantID, after.KnowledgeBaseID, []string{after.FolderPath}, false); err != nil {
				return err
			}
		}
		result := tx.Model(&types.Knowledge{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND parse_status = ? AND "+
				"updated_at = ? AND folder_path = ?",
				before.ID,
				before.TenantID,
				before.KnowledgeBaseID,
				before.ParseStatus,
				before.UpdatedAt, before.FolderPath).
			Updates(map[string]any{
				"knowledge_base_id": after.KnowledgeBaseID, "parse_status": after.ParseStatus,
				"metadata": after.Metadata, "storage_size": after.StorageSize, "updated_at": after.UpdatedAt,
				"enable_status": after.EnableStatus, "embedding_model_id": after.EmbeddingModelID,
				"description":   after.Description,
				"processed_at":  after.ProcessedAt,
				"error_message": after.ErrorMessage,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("knowledge changed during transfer")
		}
		delta := after.StorageSize - before.StorageSize
		if delta != 0 {
			result := tx.Model(&types.Tenant{}).Where("id = ?", before.TenantID).
				UpdateColumn("storage_used",
					gorm.Expr("CASE WHEN storage_used + ? < 0 THEN 0 ELSE storage_used + ? END",
						delta,
						delta))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("transfer tenant not found")
			}
		}
		var stored struct{ UpdatedAt time.Time }
		if err := tx.Model(&types.Knowledge{}).Select("updated_at").
			Where("id = ? AND tenant_id = ?", after.ID, after.TenantID).Take(&stored).Error; err != nil {
			return err
		}
		storedUpdatedAt = stored.UpdatedAt
		return nil
	})
	if err == nil {
		after.UpdatedAt = storedUpdatedAt
	}
	return err
}
