package types

import "time"

type UserWorkbenchPin struct {
	TenantID    uint64    `gorm:"primaryKey;autoIncrement:false"`
	UserID      string    `gorm:"primaryKey;type:varchar(512)"`
	WorkbenchID string    `gorm:"primaryKey;type:varchar(36)"`
	PinnedAt    time.Time `gorm:"not null"`
}

func (UserWorkbenchPin) TableName() string { return "user_workbench_pins" }
