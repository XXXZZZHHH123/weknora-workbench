package types

import (
	"gorm.io/gorm"
	"time"
)

// AssistantProject groups existing conversations without changing their owner.
type AssistantProject struct {
	ID              string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64    `json:"-" gorm:"index:idx_assistant_project_owner"`
	UserID          string    `json:"-" gorm:"type:varchar(512);index:idx_assistant_project_owner"`
	Name            string    `json:"name" gorm:"type:varchar(128)"`
	Description     string    `json:"description"`
	KnowledgeBaseID string    `json:"knowledge_base_id,omitempty" gorm:"type:varchar(36)"`
	SessionCount    int64     `json:"session_count" gorm:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AssistantProjectSession struct {
	TenantID  uint64    `json:"-" gorm:"primaryKey"`
	UserID    string    `json:"-" gorm:"type:varchar(512);primaryKey"`
	SessionID string    `json:"session_id" gorm:"type:varchar(36);primaryKey"`
	ProjectID string    `json:"project_id" gorm:"type:varchar(36);index"`
	CreatedAt time.Time `json:"created_at"`
}

// ResearchWorkbench is a named entry into the existing tenant research service.
// Its ID does not imply independent observations, judgments, or research results.
type ResearchWorkbench struct {
	ID                string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64         `json:"-" gorm:"uniqueIndex:idx_research_create_key;index"`
	UserID            string         `json:"-" gorm:"type:varchar(512);uniqueIndex:idx_research_create_key"`
	Name              string         `json:"name" gorm:"type:varchar(128)"`
	Description       string         `json:"description"`
	KnowledgeBaseID   string         `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	KnowledgeBaseName string         `json:"knowledge_base_name,omitempty" gorm:"-"`
	IdempotencyKey    string         `json:"-" gorm:"type:varchar(128);uniqueIndex:idx_research_create_key"`
	RequestHash       string         `json:"-" gorm:"type:varchar(64)"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `json:"-" gorm:"index"`
}
