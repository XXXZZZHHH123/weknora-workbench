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
	IsPinned                bool                             `json:"is_pinned" gorm:"-"`
	PinnedAt                *time.Time                       `json:"pinned_at" gorm:"-"`
	ID                      string                           `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID                uint64                           `json:"-" gorm:"uniqueIndex:idx_research_create_key;index"`
	UserID                  string                           `json:"-" gorm:"type:varchar(512);uniqueIndex:idx_research_create_key"`
	OwnerID                 string                           `json:"owner_id" gorm:"-"`
	Name                    string                           `json:"name" gorm:"type:varchar(128)"`
	Description             string                           `json:"description"`
	KnowledgeBaseID         string                           `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	SourcesRevision         int64                            `json:"sources_revision" gorm:"not null;default:0"`
	ScopeVersion            int                              `json:"scope_version" gorm:"-"`
	SourcesSummary          []WorkbenchSourceSummary         `json:"sources" gorm:"-"`
	SourceCount             int                              `json:"source_count" gorm:"-"`
	EffectiveViewModules    []string                         `json:"view_modules" gorm:"-"`
	KnowledgeBaseName       string                           `json:"knowledge_base_name,omitempty" gorm:"-"`
	CanManage               bool                             `json:"can_manage" gorm:"-"`
	CanManagePermissions    bool                             `json:"can_manage_permissions" gorm:"-"`
	CanEdit                 bool                             `json:"can_edit" gorm:"-"`
	CanReadSource           bool                             `json:"can_read_source" gorm:"-"`
	CanEditSource           bool                             `json:"can_edit_source" gorm:"-"`
	CanManageSource         bool                             `json:"can_manage_source" gorm:"-"`
	SourceDataAvailable     bool                             `json:"source_data_available" gorm:"-"`
	AccessPolicy            *ResourceAccessPolicy            `json:"-" gorm:"serializer:json;type:text"`
	SourceSharingApproved   bool                             `json:"source_sharing_approved" gorm:"not null;default:false"`
	SourceSharingRequested  bool                             `json:"source_sharing_requested" gorm:"not null;default:false"`
	SourceSharingApprovedBy string                           `json:"-" gorm:"type:varchar(512);not null;default:''"`
	SourceSharingModules    []string                         `json:"source_sharing_modules" gorm:"serializer:json;type:text"`
	RestrictedViewProfiles  []WorkbenchRestrictedViewProfile `json:"-" gorm:"serializer:json;type:text"`
	ApprovedSourceModules   []string                         `json:"-" gorm:"-"`
	DefaultKnowledgeBase    *ResearchWorkbenchSource         `json:"default_knowledge_base,omitempty" gorm:"-"`
	ScopeTenantID           uint64                           `json:"scope_tenant_id" gorm:"-"`
	WorkspaceMode           string                           `json:"workspace_mode" gorm:"type:varchar(16);not null;default:legacy"`
	TemplateID              string                           `json:"template_id" gorm:"type:varchar(32);not null;default:''"`
	TemplateVersion         int                              `json:"template_version" gorm:"not null;default:0"`
	Onboarding              ResearchWorkbenchOnboarding      `json:"onboarding" gorm:"-"`
	IdempotencyKey          string                           `json:"-" gorm:"type:varchar(128);uniqueIndex:idx_research_create_key"`
	RequestHash             string                           `json:"-" gorm:"type:varchar(64)"`
	CreatedAt               time.Time                        `json:"created_at"`
	UpdatedAt               time.Time                        `json:"updated_at"`
	DeletedAt               gorm.DeletedAt                   `json:"-" gorm:"index"`
}

type WorkbenchPermissions struct {
	ScopeVersion           int                              `json:"scope_version"`
	SourcesRevision        int64                            `json:"sources_revision"`
	Policy                 *ResourceAccessPolicy            `json:"policy"`
	OwnerID                string                           `json:"owner_id"`
	CanManage              bool                             `json:"can_manage"`
	Legacy                 bool                             `json:"legacy"`
	SourceSharingApproved  bool                             `json:"source_sharing_approved"`
	SourceSharingRequested bool                             `json:"source_sharing_requested"`
	SourceSharingModules   []string                         `json:"source_sharing_modules"`
	CanManageSource        bool                             `json:"can_manage_source"`
	RestrictedViewProfiles []WorkbenchRestrictedViewProfile `json:"restricted_view_profiles"`
}

// A profile only narrows an independently authorized workbench's visible pages.
// It never grants knowledge-base access or makes the workbench itself visible.
type WorkbenchRestrictedViewProfile struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Modules    []string `json:"modules"`
	MemberIDs  []string `json:"member_ids"`
	AllMembers bool     `json:"all_members"`
}

// ResearchWorkbenchSource is derived from a tenant's explicit system binding;
// neither a KB display name nor client input can grant it a source role.
type ResearchWorkbenchSource struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	TenantID        uint64 `json:"tenant_id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
}
