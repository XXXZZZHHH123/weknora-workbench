package types

import "time"

// KnowledgeFolder preserves user-created directories even when they are empty.
// Legacy document folder_path values remain readable without a data backfill.
type KnowledgeFolder struct {
	ID              string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64    `json:"-" gorm:"uniqueIndex:idx_knowledge_folder_path"`
	KnowledgeBaseID string    `json:"-" gorm:"type:varchar(36);uniqueIndex:idx_knowledge_folder_path"`
	Path            string    `json:"path" gorm:"type:varchar(1024);uniqueIndex:idx_knowledge_folder_path"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
