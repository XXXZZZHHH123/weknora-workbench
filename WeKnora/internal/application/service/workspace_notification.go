package service

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

var (
	ErrWorkspaceNotificationForbidden = errors.New("workspace notification access denied")
	ErrWorkspaceNotificationNotFound  = errors.New("workspace notification not found")
)

type WorkspaceNotificationService struct{ db *gorm.DB }

func NewWorkspaceNotificationService(db *gorm.DB) *WorkspaceNotificationService {
	return &WorkspaceNotificationService{db: db}
}

type WorkspaceNotificationPage struct {
	Data        []types.WorkspaceNotification `json:"data"`
	NextCursor  uint64                        `json:"next_cursor"`
	UnreadCount int64                         `json:"unread_count"`
}

func (s *WorkspaceNotificationService) recipient(ctx context.Context, tenantID uint64) (string, error) {
	caller := types.CallerFromContext(ctx)
	if tenantID == 0 || caller.TenantID != tenantID || caller.UserID == "" || types.IsSyntheticUserID(caller.UserID) {
		return "", ErrWorkspaceNotificationForbidden
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&types.TenantMember{}).
		Where("tenant_id = ? AND user_id = ? AND status = ?", tenantID, caller.UserID, types.TenantMemberStatusActive).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count == 0 {
		return "", ErrWorkspaceNotificationForbidden
	}
	return caller.UserID, nil
}

func (s *WorkspaceNotificationService) List(ctx context.Context, tenantID, afterID uint64, limit int) (*WorkspaceNotificationPage, error) {
	userID, err := s.recipient(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	page := &WorkspaceNotificationPage{Data: []types.WorkspaceNotification{}}
	query := s.db.WithContext(ctx).Model(&types.WorkspaceNotification{}).
		Where("tenant_id = ? AND recipient_user_id = ?", tenantID, userID)
	if err := query.Session(&gorm.Session{}).Where("read_at IS NULL").Count(&page.UnreadCount).Error; err != nil {
		return nil, err
	}
	if afterID > 0 {
		query = query.Where("id < ?", afterID)
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&page.Data).Error; err != nil {
		return nil, err
	}
	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.NextCursor = page.Data[len(page.Data)-1].ID
	}
	if len(page.Data) > 0 {
		ids := make(map[string]bool)
		for _, row := range page.Data {
			ids[row.ActorUserID] = true
			if row.TargetUserID != "" {
				ids[row.TargetUserID] = true
			}
		}
		userIDs := make([]string, 0, len(ids))
		for id := range ids {
			userIDs = append(userIDs, id)
		}
		var users []struct{ ID, Username string }
		if err := s.db.WithContext(ctx).Table("users").Select("id, username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			return nil, err
		}
		names := make(map[string]string, len(users))
		for _, user := range users {
			names[user.ID] = user.Username
		}
		for i := range page.Data {
			page.Data[i].ActorName = names[page.Data[i].ActorUserID]
			page.Data[i].TargetName = names[page.Data[i].TargetUserID]
		}
	}
	return page, nil
}

func (s *WorkspaceNotificationService) MarkRead(ctx context.Context, tenantID, notificationID uint64) error {
	userID, err := s.recipient(ctx, tenantID)
	if err != nil {
		return err
	}
	var row types.WorkspaceNotification
	query := s.db.WithContext(ctx).Where("tenant_id = ? AND recipient_user_id = ? AND id = ?", tenantID, userID, notificationID)
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWorkspaceNotificationNotFound
		}
		return err
	}
	if row.ReadAt != nil {
		return nil
	}
	return s.db.WithContext(ctx).Model(&types.WorkspaceNotification{}).
		Where("tenant_id = ? AND recipient_user_id = ? AND id = ? AND read_at IS NULL", tenantID, userID, notificationID).
		Update("read_at", time.Now()).Error
}
