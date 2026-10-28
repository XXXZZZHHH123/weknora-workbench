package types

import "time"

// UserFeedback is a bug report or suggestion submitted from the web UI.
// Submissions are platform-wide and are reviewed by system administrators.
type UserFeedback struct {
	ID        uint64    `json:"id"         gorm:"primaryKey"`
	TenantID  uint64    `json:"tenant_id"  gorm:"not null"`
	UserID    string    `json:"user_id"    gorm:"type:varchar(36);not null"`
	Kind      string    `json:"kind"       gorm:"type:varchar(16);not null"`
	Content   string    `json:"content"    gorm:"type:text;not null"`
	PageURL   string    `json:"page_url"   gorm:"type:varchar(2048);not null;default:''"`
	PageTitle string    `json:"page_title" gorm:"type:varchar(256);not null;default:''"`
	UserAgent string    `json:"user_agent" gorm:"type:varchar(512);not null;default:''"`
	Status    string    `json:"status"     gorm:"type:varchar(16);not null;default:'open'"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Read-only display fields joined from users/tenants for the admin list.
	Username   string `json:"username,omitempty"    gorm:"->;-:migration"`
	Email      string `json:"email,omitempty"       gorm:"->;-:migration"`
	TenantName string `json:"tenant_name,omitempty" gorm:"->;-:migration"`
}

func (UserFeedback) TableName() string { return "user_feedback" }

const (
	UserFeedbackKindBug        = "bug"
	UserFeedbackKindSuggestion = "suggestion"
	UserFeedbackStatusOpen     = "open"
	UserFeedbackStatusResolved = "resolved"
)
