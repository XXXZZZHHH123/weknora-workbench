package types

import "time"

// DefaultKnowledgeBaseBinding reserves one real KB identity per system purpose.
// The reservation survives retryable initialization failures; names confer no role.
type DefaultKnowledgeBaseBinding struct {
	TenantID        uint64    `json:"-" gorm:"primaryKey"`
	Kind            string    `json:"kind" gorm:"type:varchar(32);primaryKey"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);uniqueIndex"`
	CreatedAt       time.Time `json:"created_at"`
}
