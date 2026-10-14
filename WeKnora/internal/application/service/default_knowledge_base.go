package service

import (
	"context"
	"errors"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const macroKnowledgeBaseKind = "macro"

type DefaultKnowledgeBaseView struct {
	Kind            string `json:"kind"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Name            string `json:"name"`
	SourceScope     string `json:"source_scope"`
}

func (s *WorkspaceService) defaultKnowledgeBase(ctx context.Context, caller types.Caller) (*types.DefaultKnowledgeBaseBinding, *types.KnowledgeBase, error) {
	var binding types.DefaultKnowledgeBaseBinding
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND kind = ?", caller.TenantID, macroKnowledgeBaseKind).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var kb types.KnowledgeBase
	// Include soft-deleted rows so initialization cannot silently resurrect a KB.
	err = s.db.WithContext(ctx).Unscoped().Where("id = ? AND tenant_id = ?", binding.KnowledgeBaseID, caller.TenantID).First(&kb).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &binding, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &binding, &kb, nil
}

func defaultKnowledgeBaseView(kb *types.KnowledgeBase) *DefaultKnowledgeBaseView {
	if kb == nil || kb.DeletedAt.Valid {
		return nil
	}
	return &DefaultKnowledgeBaseView{Kind: macroKnowledgeBaseKind, KnowledgeBaseID: kb.ID, Name: kb.Name, SourceScope: "tenant_research"}
}

func (s *WorkspaceService) GetDefaultKnowledgeBase(ctx context.Context) (*DefaultKnowledgeBaseView, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	_, kb, err := s.defaultKnowledgeBase(ctx, caller)
	if err != nil {
		return nil, err
	}
	return defaultKnowledgeBaseView(kb), nil
}

func (s *WorkspaceService) InitializeDefaultKnowledgeBase(ctx context.Context) (*DefaultKnowledgeBaseView, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !caller.Role.HasPermission(types.TenantRoleContributor) {
		return nil, apperrors.NewForbiddenError("需要空间编辑权限才能初始化Default知识库")
	}
	binding, kb, err := s.defaultKnowledgeBase(ctx, caller)
	if err != nil {
		return nil, err
	}
	if kb != nil {
		if kb.DeletedAt.Valid {
			return nil, apperrors.NewConflictError("Default知识库已删除，请联系管理员处理")
		}
		return defaultKnowledgeBaseView(kb), nil
	}
	if binding == nil {
		candidate := types.DefaultKnowledgeBaseBinding{TenantID: caller.TenantID, Kind: macroKnowledgeBaseKind, KnowledgeBaseID: uuid.NewString()}
		// Reserve the identifier before calling the native creator. A failed
		// storage check and simultaneous requests all retry this same identity.
		if err = s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "kind"}}, DoNothing: true}).Create(&candidate).Error; err != nil {
			return nil, err
		}
		binding, kb, err = s.defaultKnowledgeBase(ctx, caller)
		if err != nil {
			return nil, err
		}
		if kb != nil {
			if kb.DeletedAt.Valid {
				return nil, apperrors.NewConflictError("Default知识库已删除，请联系管理员处理")
			}
			return defaultKnowledgeBaseView(kb), nil
		}
	}
	native := &types.KnowledgeBase{
		ID: binding.KnowledgeBaseID, Name: "宏观数据", Type: types.KnowledgeBaseTypeDocument,
		Description:              "Default 宏观知识库。宏观来源与数值沿用当前空间研究服务，未复制为文档或表格文件。",
		IndexingStrategy:         types.IndexingStrategy{Disabled: true},
		ProfileConfig:            &types.KnowledgeBaseProfileConfig{Enabled: false},
		AutoTagConfig:            &types.AutoTagConfig{Enabled: false},
		QuestionGenerationConfig: &types.QuestionGenerationConfig{Enabled: false},
	}
	ctx = types.WithExecutionTenant(ctx, caller.TenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, caller.UserID)
	_, createErr := s.kbs.CreateKnowledgeBase(ctx, native)
	// Another request may have inserted the reserved native ID first.
	_, kb, readErr := s.defaultKnowledgeBase(ctx, caller)
	if readErr != nil {
		return nil, readErr
	}
	if kb != nil && !kb.DeletedAt.Valid {
		return defaultKnowledgeBaseView(kb), nil
	}
	if createErr != nil {
		return nil, createErr
	}
	return nil, apperrors.NewConflictError("Default知识库初始化未完成，请重试")
}
