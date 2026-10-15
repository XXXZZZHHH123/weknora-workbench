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

// Legacy workbenches name a tenant research entry; new workbenches carry an
// explicit isolated scope and a versioned template without business data.
type ResearchWorkbench struct {
	ID                string                      `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64                      `json:"-" gorm:"uniqueIndex:idx_research_create_key;index"`
	UserID            string                      `json:"-" gorm:"type:varchar(512);uniqueIndex:idx_research_create_key"`
	Name              string                      `json:"name" gorm:"type:varchar(128)"`
	Description       string                      `json:"description"`
	KnowledgeBaseID   string                      `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	KnowledgeBaseName string                      `json:"knowledge_base_name,omitempty" gorm:"-"`
	ScopeTenantID     uint64                      `json:"scope_tenant_id" gorm:"-"`
	WorkspaceMode     string                      `json:"workspace_mode" gorm:"type:varchar(16);not null;default:legacy"`
	TemplateID        string                      `json:"template_id" gorm:"type:varchar(32);not null;default:''"`
	TemplateVersion   int                         `json:"template_version" gorm:"not null;default:0"`
	Onboarding        ResearchWorkbenchOnboarding `json:"onboarding" gorm:"-"`
	IdempotencyKey    string                      `json:"-" gorm:"type:varchar(128);uniqueIndex:idx_research_create_key"`
	RequestHash       string                      `json:"-" gorm:"type:varchar(64)"`
	CreatedAt         time.Time                   `json:"created_at"`
	UpdatedAt         time.Time                   `json:"updated_at"`
	DeletedAt         gorm.DeletedAt              `json:"-" gorm:"index"`
}
