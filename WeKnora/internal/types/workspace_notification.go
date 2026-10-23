package types

import "time"

// WorkspaceNotification is a private inbox projection of a successful member
// management event. It never includes invitation tokens or document contents.
type WorkspaceNotification struct {
	ID              uint64      `json:"id" gorm:"primaryKey;autoIncrement"`
	TenantID        uint64      `json:"tenant_id" gorm:"not null;uniqueIndex:uniq_workspace_notification,priority:1;index:idx_workspace_notification_inbox,priority:1"`
	RecipientUserID string      `json:"-" gorm:"type:varchar(36);not null;uniqueIndex:uniq_workspace_notification,priority:2;index:idx_workspace_notification_inbox,priority:2"`
	AuditLogID      uint64      `json:"audit_log_id" gorm:"not null;uniqueIndex:uniq_workspace_notification,priority:3"`
	ActorUserID     string      `json:"actor_user_id" gorm:"type:varchar(36);not null"`
	Action          AuditAction `json:"action" gorm:"type:varchar(64);not null"`
	TargetUserID    string      `json:"target_user_id" gorm:"type:varchar(36);not null;default:''"`
	TargetID        string      `json:"target_id" gorm:"type:varchar(64);not null;default:''"`
	Details         JSON        `json:"details" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt       time.Time   `json:"created_at" gorm:"not null;index:idx_workspace_notification_inbox,priority:3,sort:desc"`
	ReadAt          *time.Time  `json:"read_at"`
	ActorName       string      `json:"actor_name,omitempty" gorm:"-"`
	TargetName      string      `json:"target_name,omitempty" gorm:"-"`
}

func (WorkspaceNotification) TableName() string { return "workspace_notifications" }
