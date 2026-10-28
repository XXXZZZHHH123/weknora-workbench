package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const (
	userFeedbackMaxContent   = 5000
	userFeedbackMaxPageURL   = 2048
	userFeedbackMaxPageTitle = 256
	userFeedbackMaxUserAgent = 512
	userFeedbackDefaultLimit = 50
	userFeedbackMaxLimit     = 100
)

type UserFeedbackService struct {
	db *gorm.DB
}

func NewUserFeedbackService(db *gorm.DB) *UserFeedbackService {
	return &UserFeedbackService{db: db}
}

type UserFeedbackInput struct {
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	PageURL   string `json:"page_url"`
	PageTitle string `json:"page_title"`
}

type UserFeedbackListParams struct {
	Status  string
	Kind    string
	AfterID uint64
	Limit   int
}

type UserFeedbackPage struct {
	Items      []types.UserFeedback `json:"items"`
	NextCursor uint64               `json:"next_cursor"`
	OpenCount  int64                `json:"open_count"`
}

func (s *UserFeedbackService) Submit(ctx context.Context, input UserFeedbackInput, userAgent string) (*types.UserFeedback, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if input.Kind != types.UserFeedbackKindBug && input.Kind != types.UserFeedbackKindSuggestion {
		return nil, apperrors.NewBadRequestError("请选择反馈类型")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return nil, apperrors.NewBadRequestError("请填写反馈内容")
	}
	if utf8.RuneCountInString(content) > userFeedbackMaxContent {
		return nil, apperrors.NewBadRequestError("反馈内容不能超过5000个字符")
	}
	row := &types.UserFeedback{
		TenantID:  caller.TenantID,
		UserID:    caller.UserID,
		Kind:      input.Kind,
		Content:   content,
		PageURL:   truncateRunes(strings.TrimSpace(input.PageURL), userFeedbackMaxPageURL),
		PageTitle: truncateRunes(strings.TrimSpace(input.PageTitle), userFeedbackMaxPageTitle),
		UserAgent: truncateRunes(userAgent, userFeedbackMaxUserAgent),
		Status:    types.UserFeedbackStatusOpen,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// List returns feedback across all workspaces, newest first. Callers must be
// system administrators; the route guard enforces that.
func (s *UserFeedbackService) List(ctx context.Context, params UserFeedbackListParams) (*UserFeedbackPage, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = userFeedbackDefaultLimit
	}
	if limit > userFeedbackMaxLimit {
		limit = userFeedbackMaxLimit
	}
	query := s.db.WithContext(ctx).Table("user_feedback AS f").
		Select("f.*, u.username AS username, u.email AS email, t.name AS tenant_name").
		Joins("LEFT JOIN users u ON u.id = f.user_id").
		Joins("LEFT JOIN tenants t ON t.id = f.tenant_id")
	if params.Status != "" {
		query = query.Where("f.status = ?", params.Status)
	}
	if params.Kind != "" {
		query = query.Where("f.kind = ?", params.Kind)
	}
	if params.AfterID > 0 {
		query = query.Where("f.id < ?", params.AfterID)
	}
	page := &UserFeedbackPage{Items: []types.UserFeedback{}}
	if err := query.Order("f.id DESC").Limit(limit + 1).Find(&page.Items).Error; err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	if err := s.db.WithContext(ctx).Model(&types.UserFeedback{}).
		Where("status = ?", types.UserFeedbackStatusOpen).Count(&page.OpenCount).Error; err != nil {
		return nil, err
	}
	return page, nil
}

func (s *UserFeedbackService) SetStatus(ctx context.Context, id uint64, status string) error {
	if status != types.UserFeedbackStatusOpen && status != types.UserFeedbackStatusResolved {
		return apperrors.NewBadRequestError("状态无效")
	}
	res := s.db.WithContext(ctx).Model(&types.UserFeedback{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperrors.NewNotFoundError("反馈不存在")
	}
	return nil
}
