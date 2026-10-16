package types

import "time"

const (
	ResearchWorkbenchModeLegacy      = "legacy"
	ResearchWorkbenchModeIsolated    = "isolated"
	ResearchWorkbenchTemplateVersion = 1
)

// ResearchWorkbenchTemplate declares empty capabilities, never indicator IDs,
// source bindings, observations or personal research results.
type ResearchWorkbenchTemplate struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version int      `json:"version"`
	Modules []string `json:"modules"`
}

func ResearchWorkbenchTemplates() []ResearchWorkbenchTemplate {
	return []ResearchWorkbenchTemplate{
		{ID: "industrial", Name: "产业研究", Version: ResearchWorkbenchTemplateVersion, Modules: []string{"data", "structure", "judgment", "monitoring", "brief", "strategies", "export"}},
		{ID: "data-tracking", Name: "数据跟踪", Version: ResearchWorkbenchTemplateVersion, Modules: []string{"data", "trends", "compare", "freshness", "export"}},
		{ID: "topic", Name: "专题研究", Version: ResearchWorkbenchTemplateVersion, Modules: []string{"sources", "notes", "evidence", "assistant", "brief"}},
		{ID: "blank", Name: "空白工作台", Version: ResearchWorkbenchTemplateVersion, Modules: []string{"sources", "assistant"}},
	}
}

// These are personal guide acknowledgements, not assertions that source data
// or a research capability has passed its readiness checks.
type ResearchWorkbenchOnboardingTasks struct {
	Sources  bool `json:"sources"`
	Review   bool `json:"review"`
	Research bool `json:"research"`
}

type ResearchWorkbenchOnboarding struct {
	Status string                           `json:"status"`
	Tasks  ResearchWorkbenchOnboardingTasks `json:"tasks"`
}

type ResearchWorkbenchOnboardingRecord struct {
	TenantID     uint64 `gorm:"primaryKey"`
	WorkbenchID  string `gorm:"type:varchar(36);primaryKey"`
	UserID       string `gorm:"type:varchar(512);primaryKey"`
	Status       string `gorm:"type:varchar(16);not null;default:active"`
	TaskSources  bool   `gorm:"not null;default:false"`
	TaskReview   bool   `gorm:"not null;default:false"`
	TaskResearch bool   `gorm:"not null;default:false"`
	UpdatedAt    time.Time
}

func (ResearchWorkbenchOnboardingRecord) TableName() string {
	return "research_workbench_onboarding"
}
