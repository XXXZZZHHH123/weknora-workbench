package types

import "time"

const (
	WorkbenchSourceKnowledgeBase   = "knowledge_base"
	WorkbenchSourceCentralDataset  = "central_dataset"
	KnowledgeBaseSystemRoleCentral = "central"
)

// CentralDataset initially owns one existing Default KB and its published
// snapshots. It is a registry, not a second copy of the observations.
type CentralDataset struct {
	ID                     string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID               uint64    `json:"-" gorm:"uniqueIndex:idx_central_dataset_kind"`
	Kind                   string    `json:"kind" gorm:"type:varchar(32);uniqueIndex:idx_central_dataset_kind"`
	Name                   string    `json:"name" gorm:"type:varchar(128)"`
	BackingKnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);uniqueIndex"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// Removed bindings remain as tombstones so re-running initialization cannot
// resurrect a source, and stored research references retain their provenance.
type ResearchWorkbenchSourceBinding struct {
	ID                string    `json:"id" gorm:"type:varchar(128);primaryKey"`
	WorkbenchID       string    `json:"-" gorm:"type:varchar(36);uniqueIndex:idx_workbench_source"`
	SourceType        string    `json:"source_type" gorm:"type:varchar(32);uniqueIndex:idx_workbench_source"`
	SourceID          string    `json:"source_id" gorm:"type:varchar(36);uniqueIndex:idx_workbench_source"`
	SourceTenantID    uint64    `json:"tenant_id"`
	Name              string    `json:"name" gorm:"type:varchar(128)"`
	Enabled           bool      `json:"enabled"`
	Status            string    `json:"status" gorm:"type:varchar(32)"`
	SharingApproved   bool      `json:"sharing_approved"`
	SharingRequested  bool      `json:"sharing_requested"`
	SharingApprovedBy string    `json:"-" gorm:"type:varchar(512)"`
	SharingModules    []string  `json:"sharing_modules" gorm:"serializer:json;type:text"`
	SharingVersion    int64     `json:"sharing_version"`
	Legacy            bool      `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type WorkbenchSourceInput struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
}

type WorkbenchSourceSummary struct {
	ID               string   `json:"id"`
	SourceType       string   `json:"source_type"`
	SourceID         string   `json:"source_id"`
	KnowledgeBaseID  string   `json:"knowledge_base_id,omitempty"`
	TenantID         uint64   `json:"tenant_id,omitempty"`
	Name             string   `json:"name,omitempty"`
	Status           string   `json:"status"`
	CanRead          bool     `json:"can_read"`
	CanEdit          bool     `json:"can_edit"`
	CanManageSource  bool     `json:"can_manage_source"`
	SharingApproved  bool     `json:"sharing_approved"`
	SharingRequested bool     `json:"sharing_requested"`
	SharingModules   []string `json:"sharing_modules"`
	Delegated        bool     `json:"delegated"`
	ViewModules      []string `json:"view_modules"`
}
