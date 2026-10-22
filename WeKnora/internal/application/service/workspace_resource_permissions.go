package service

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *WorkspaceService) populateWorkbenchAccess(ctx context.Context, caller types.Caller, row *types.ResearchWorkbench) (*types.KnowledgeBase, error) {
	if row.AccessPolicy != nil && !types.ResourceCanRead(types.ResourcePermissionFor(row.AccessPolicy, row.UserID, caller)) {
		return nil, apperrors.NewNotFoundError("工作台不存在或无权访问")
	}
	kb, err := s.kbs.GetKnowledgeBaseByIDOnly(ctx, row.KnowledgeBaseID)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || kb == nil || kb.DeletedAt.Valid {
		if row.AccessPolicy == nil {
			return nil, apperrors.NewNotFoundError("知识库不存在或无权访问")
		}
		kb = nil
	}
	row.CanReadSource = false
	row.CanEditSource = false
	if kb != nil {
		_, readErr := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, s.shares, nil)
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, readErr
		}
		row.CanReadSource = readErr == nil
		if row.CanReadSource && caller.Role.HasPermission(types.TenantRoleContributor) {
			_, editErr := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleEditor, s.shares, nil)
			row.CanEditSource = editErr == nil
			if kb.AccessPolicy == nil && kb.TenantID == caller.TenantID {
				row.CanEditSource = row.CanEditSource && (kb.CreatorID == caller.UserID || caller.Role.HasPermission(types.TenantRoleAdmin))
			}
		}
	}
	if row.AccessPolicy == nil && !row.CanReadSource {
		return nil, apperrors.NewNotFoundError("知识库不存在或无权访问")
	}
	row.KnowledgeBaseName = ""
	if row.CanReadSource {
		row.KnowledgeBaseName = kb.Name
	}
	row.ScopeTenantID = row.TenantID
	row.CanManage = canManageWorkbench(caller, row)
	row.CanManagePermissions = row.UserID == caller.UserID && caller.Role.HasPermission(types.TenantRoleContributor)
	row.CanEdit = caller.Role.HasPermission(types.TenantRoleContributor) && (row.AccessPolicy == nil || types.ResourceCanEdit(types.ResourcePermissionFor(row.AccessPolicy, row.UserID, caller)))
	row.CanManageSource = kb != nil && kb.TenantID == caller.TenantID && kb.CreatorID != "" && kb.CreatorID == caller.UserID && caller.Role.HasPermission(types.TenantRoleContributor)
	row.SourceSharingApproved = kb != nil && row.SourceSharingApproved && row.SourceSharingApprovedBy != "" && row.SourceSharingApprovedBy == kb.CreatorID
	if !row.SourceSharingApproved || !types.ValidWorkbenchSourceModules(row.SourceSharingModules) || row.SourceSharingModules == nil {
		row.SourceSharingModules = []string{}
	}
	row.SourceDataAvailable = kb != nil && (row.CanReadSource || (row.SourceSharingApproved && len(row.SourceSharingModules) > 0))
	return kb, nil
}

func (s *WorkspaceService) GetWorkbenchPermissions(ctx context.Context, id string) (*types.WorkbenchPermissions, error) {
	row, err := s.workbench(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	if !row.CanManagePermissions {
		return nil, apperrors.NewForbiddenError("仅工作台主人可设置权限")
	}
	return &types.WorkbenchPermissions{Policy: row.AccessPolicy, OwnerID: row.UserID, CanManage: row.CanManage,
		Legacy: row.AccessPolicy == nil, SourceSharingApproved: row.SourceSharingApproved, SourceSharingRequested: row.SourceSharingRequested,
		SourceSharingModules: row.SourceSharingModules, CanManageSource: row.CanManageSource}, nil
}

func (s *WorkspaceService) UpdateWorkbenchPermissions(ctx context.Context, id string, policy *types.ResourceAccessPolicy) (*types.WorkbenchPermissions, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateResourcePolicyMembers(ctx, s.db, caller.TenantID, policy); err != nil {
		return nil, err
	}
	verified, source, err := s.workbenchWithSource(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	sources := []*types.KnowledgeBase{}
	if source != nil {
		sources = append(sources, source)
	}
	err = s.mutateWorkbench(ctx, verified, sources, func(tx *gorm.DB, row *types.ResearchWorkbench) error {
		// An administrator may manage a legacy workbench, but adopting its new
		// independent policy is an ownership decision rather than an RBAC bypass.
		if row.UserID != caller.UserID {
			return apperrors.NewForbiddenError("仅工作台主人可设置权限")
		}
		row.AccessPolicy = policy
		return tx.Save(row).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkbenchPermissions(ctx, id)
}

type WorkbenchSourceSharing struct {
	Approved             bool     `json:"approved"`
	Requested            bool     `json:"requested"`
	CanManageSource      bool     `json:"can_manage_source"`
	SourceSharingModules []string `json:"source_sharing_modules"`
}

func (s *WorkspaceService) RequestWorkbenchSourceSharing(ctx context.Context, id string) (*types.WorkbenchPermissions, error) {
	verified, source, err := s.workbenchWithSource(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	if verified.UserID != caller.UserID {
		return nil, apperrors.NewForbiddenError("仅工作台主人可申请来源数据共享")
	}
	if source == nil || source.TenantID != caller.TenantID || source.CreatorID == "" {
		return nil, apperrors.NewConflictError("来源知识库缺少当前空间的有效主人，不能申请共享")
	}
	err = s.mutateWorkbench(ctx, verified, []*types.KnowledgeBase{source}, func(tx *gorm.DB, row *types.ResearchWorkbench) error {
		if row.UserID != caller.UserID {
			return apperrors.NewForbiddenError("仅工作台主人可申请来源数据共享")
		}
		row.SourceSharingRequested = true
		return tx.Save(row).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkbenchPermissions(ctx, id)
}

func (s *WorkspaceService) workbenchSourceOwner(ctx context.Context, id string) (*types.ResearchWorkbench, *types.KnowledgeBase, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	var row types.ResearchWorkbench
	if err = s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, caller.TenantID).First(&row).Error; err != nil {
		return nil, nil, workspaceDBError(err)
	}
	kb, err := s.kbs.GetKnowledgeBaseByIDOnly(ctx, row.KnowledgeBaseID)
	if err != nil || kb == nil || kb.DeletedAt.Valid || kb.TenantID != caller.TenantID || kb.CreatorID == "" || kb.CreatorID != caller.UserID || !caller.Role.HasPermission(types.TenantRoleContributor) {
		return nil, nil, apperrors.NewForbiddenError("仅来源知识库主人可授权研究数据共享")
	}
	return &row, kb, nil
}

func (s *WorkspaceService) GetWorkbenchSourceSharing(ctx context.Context, id string) (*WorkbenchSourceSharing, error) {
	row, kb, err := s.workbenchSourceOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	approved := row.SourceSharingApproved && row.SourceSharingApprovedBy == kb.CreatorID
	modules := row.SourceSharingModules
	if !approved || !types.ValidWorkbenchSourceModules(modules) || modules == nil {
		modules = []string{}
	}
	return &WorkbenchSourceSharing{Approved: approved, Requested: row.SourceSharingRequested, CanManageSource: true, SourceSharingModules: modules}, nil
}

func (s *WorkspaceService) UpdateWorkbenchSourceSharing(ctx context.Context, id string, approved bool, modules []string) (*WorkbenchSourceSharing, error) {
	if !types.ValidWorkbenchSourceModules(modules) || (approved && len(modules) == 0) {
		return nil, apperrors.NewBadRequestError("请选择至少一个有效页面，页面不能重复")
	}
	verified, kb, err := s.workbenchSourceOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Exec("UPDATE knowledge_bases SET id = id WHERE id = ? AND tenant_id = ? AND deleted_at IS NULL", kb.ID, kb.TenantID).Error; err != nil {
				return err
			}
		}
		var currentKB types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", kb.ID, kb.TenantID).First(&currentKB).Error; err != nil {
			return workspaceDBError(err)
		}
		if currentKB.CreatorID != caller.UserID {
			return apperrors.NewForbiddenError("来源知识库主人已变更，请刷新后重试")
		}
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", id, caller.TenantID).First(&current).Error; err != nil {
			return workspaceDBError(err)
		}
		if current.KnowledgeBaseID != verified.KnowledgeBaseID {
			return apperrors.NewConflictError("工作台来源已变更，请刷新后重试")
		}
		current.SourceSharingApproved = approved
		current.SourceSharingApprovedBy = ""
		current.SourceSharingModules = []string{}
		if approved {
			current.SourceSharingApprovedBy = caller.UserID
			current.SourceSharingModules = append(current.SourceSharingModules, modules...)
		}
		return tx.Save(&current).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkbenchSourceSharing(ctx, id)
}

type WorkbenchSourceKnowledge struct {
	ID            string    `json:"id"`
	FileHash      string    `json:"file_hash"`
	UpdatedAt     time.Time `json:"updated_at"`
	EnableStatus  string    `json:"enable_status"`
	ParseStatus   string    `json:"parse_status"`
	KnowledgeType string    `json:"knowledge_type"`
	FileType      string    `json:"file_type"`
}

type WorkbenchSourceProjection struct {
	KnowledgeBaseID string                     `json:"knowledge_base_id"`
	TenantID        uint64                     `json:"tenant_id"`
	Kind            string                     `json:"kind"`
	Delegated       bool                       `json:"delegated"`
	Permission      string                     `json:"permission"`
	Knowledge       []WorkbenchSourceKnowledge `json:"knowledge"`
}

type WorkbenchSourceScope struct {
	ID              string                      `json:"id"`
	UserID          string                      `json:"user_id"`
	ScopeTenantID   uint64                      `json:"scope_tenant_id"`
	KnowledgeBaseID string                      `json:"knowledge_base_id"`
	CanEdit         bool                        `json:"can_edit"`
	CanManage       bool                        `json:"can_manage"`
	ViewModules     []string                    `json:"view_modules"`
	Sources         []WorkbenchSourceProjection `json:"sources"`
}

func (s *WorkspaceService) sourceProjection(ctx context.Context, kb *types.KnowledgeBase, kind string, delegated bool) (WorkbenchSourceProjection, error) {
	projection := WorkbenchSourceProjection{KnowledgeBaseID: kb.ID, TenantID: kb.TenantID, Kind: kind, Delegated: delegated, Permission: "viewer", Knowledge: []WorkbenchSourceKnowledge{}}
	err := s.db.WithContext(ctx).Model(&types.Knowledge{}).
		Select("id, file_hash, updated_at, enable_status, parse_status, type AS knowledge_type, file_type").
		Where("tenant_id = ? AND knowledge_base_id = ?", kb.TenantID, kb.ID).
		Where("parse_status <> ? OR parse_status IS NULL", types.ParseStatusDeleting).Order("id").Scan(&projection.Knowledge).Error
	return projection, err
}

// This grants only an audited research projection. It never authorizes the raw
// KB endpoints, document downloads, arbitrary SQL, or document retrieval tools.
func (s *WorkspaceService) GetWorkbenchSourceScope(ctx context.Context, id string) (*WorkbenchSourceScope, error) {
	row, kb, err := s.workbenchWithSource(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if !row.SourceDataAvailable {
		return nil, apperrors.NewForbiddenError("来源知识库主人尚未授权研究数据共享，或来源权限已失效")
	}
	caller, _ := workspaceCaller(ctx)
	primary, err := s.sourceProjection(ctx, kb, "primary", !row.CanReadSource)
	if err != nil {
		return nil, err
	}
	scope := &WorkbenchSourceScope{ID: row.ID, UserID: caller.UserID, ScopeTenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID,
		CanEdit: row.CanEdit, CanManage: row.CanManage, Sources: []WorkbenchSourceProjection{primary}}
	scope.ViewModules = row.SourceSharingModules
	if row.CanReadSource {
		scope.ViewModules = types.WorkbenchViewModules()
	}
	_, macro, err := s.defaultKnowledgeBase(ctx, caller)
	if err != nil {
		return nil, err
	}
	if macro != nil && !macro.DeletedAt.Valid && macro.ID != kb.ID {
		if _, err := s.readableKB(ctx, macro.ID); err == nil {
			projection, err := s.sourceProjection(ctx, macro, macroKnowledgeBaseKind, false)
			if err != nil {
				return nil, err
			}
			scope.Sources = append(scope.Sources, projection)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
	return scope, nil
}
