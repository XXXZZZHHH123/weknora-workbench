package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm/clause"
)

func workbenchTemplate(input WorkspaceInput) (string, int, error) {
	id, version := "blank", types.ResearchWorkbenchTemplateVersion
	if input.TemplateID != nil {
		id = *input.TemplateID
	}
	if input.TemplateVersion != nil {
		version = *input.TemplateVersion
	}
	for _, candidate := range types.ResearchWorkbenchTemplates() {
		if candidate.ID == id && candidate.Version == version {
			return id, version, nil
		}
	}
	return "", 0, apperrors.NewBadRequestError("研究模板或版本不可用，请刷新后重新选择")
}

func legacyWorkbenchRetry(existing types.ResearchWorkbench, input WorkspaceInput, proposed *types.ResearchWorkbench) bool {
	if existing.WorkspaceMode != types.ResearchWorkbenchModeLegacy || input.TemplateID != nil || input.TemplateVersion != nil {
		return false
	}
	raw, _ := json.Marshal([]string{proposed.Name, proposed.Description, proposed.KnowledgeBaseID})
	hash := sha256.Sum256(raw)
	return existing.RequestHash == hex.EncodeToString(hash[:])
}

func (s *WorkspaceService) ListWorkbenchTemplates(ctx context.Context) ([]types.ResearchWorkbenchTemplate, error) {
	if _, err := workspaceCaller(ctx); err != nil {
		return nil, err
	}
	return types.ResearchWorkbenchTemplates(), nil
}

func (s *WorkspaceService) loadWorkbenchOnboarding(ctx context.Context, rows []*types.ResearchWorkbench) error {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	byID := make(map[string]*types.ResearchWorkbench, len(rows))
	for _, row := range rows {
		row.Onboarding.Status = "active"
		ids = append(ids, row.ID)
		byID[row.ID] = row
	}
	var records []types.ResearchWorkbenchOnboardingRecord
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND workbench_id IN ?", caller.TenantID, caller.UserID, ids).Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		if row := byID[record.WorkbenchID]; row != nil {
			row.Onboarding = types.ResearchWorkbenchOnboarding{Status: record.Status, Tasks: types.ResearchWorkbenchOnboardingTasks{Sources: record.TaskSources, Review: record.TaskReview, Research: record.TaskResearch}}
		}
	}
	return nil
}

type WorkbenchOnboardingTasksInput struct {
	Sources  *bool `json:"sources"`
	Review   *bool `json:"review"`
	Research *bool `json:"research"`
}

type WorkbenchOnboardingInput struct {
	Status *string                        `json:"status"`
	Tasks  *WorkbenchOnboardingTasksInput `json:"tasks"`
}

func (s *WorkspaceService) UpdateWorkbenchOnboarding(ctx context.Context, id string, input WorkbenchOnboardingInput) (*types.ResearchWorkbench, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.workbench(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if row.WorkspaceMode != types.ResearchWorkbenchModeIsolated {
		return nil, apperrors.NewConflictError("原有工作台不使用新建引导")
	}
	record := types.ResearchWorkbenchOnboardingRecord{TenantID: caller.TenantID, WorkbenchID: row.ID, UserID: caller.UserID, Status: "active", UpdatedAt: time.Now()}
	updates := map[string]any{}
	if input.Status != nil {
		switch *input.Status {
		case "active", "dismissed", "skipped", "completed":
			record.Status = *input.Status
			updates["status"] = *input.Status
		default:
			return nil, apperrors.NewBadRequestError("引导状态无效")
		}
	}
	if input.Tasks != nil {
		if input.Tasks.Sources != nil {
			record.TaskSources = *input.Tasks.Sources
			updates["task_sources"] = *input.Tasks.Sources
		}
		if input.Tasks.Review != nil {
			record.TaskReview = *input.Tasks.Review
			updates["task_review"] = *input.Tasks.Review
		}
		if input.Tasks.Research != nil {
			record.TaskResearch = *input.Tasks.Research
			updates["task_research"] = *input.Tasks.Research
		}
	}
	if len(updates) == 0 {
		return nil, apperrors.NewBadRequestError("请提供引导状态或任务记录")
	}
	updates["updated_at"] = record.UpdatedAt
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "workbench_id"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&record).Error; err != nil {
		return nil, err
	}
	return s.GetWorkbench(ctx, row.ID)
}
