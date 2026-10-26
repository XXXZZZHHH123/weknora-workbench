package service

import (
	"context"
	"errors"
	"net/http"
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
	// Expose the persisted owner only after the caller has passed the
	// workbench read check. Restricted downstream views use this identity to
	// select the owner's explicitly shared records instead of the visitor's
	// private records.
	row.OwnerID = row.UserID
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
	row.ApprovedSourceModules = append([]string{}, row.SourceSharingModules...)
	if !row.CanReadSource {
		row.SourceSharingModules = types.EffectiveWorkbenchSourceModules(row.RestrictedViewProfiles, caller.UserID, row.ApprovedSourceModules)
	}
	row.SourceDataAvailable = kb != nil && (row.CanReadSource || (row.SourceSharingApproved && len(row.SourceSharingModules) > 0))
	if err := s.populateSources(ctx, caller, row); err != nil {
		return nil, err
	}
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
	return &types.WorkbenchPermissions{ScopeVersion: row.ScopeVersion, SourcesRevision: row.SourcesRevision, Policy: row.AccessPolicy, OwnerID: row.UserID, CanManage: row.CanManage,
		Legacy: row.AccessPolicy == nil, SourceSharingApproved: row.SourceSharingApproved, SourceSharingRequested: row.SourceSharingRequested,
		SourceSharingModules: row.ApprovedSourceModules, CanManageSource: row.CanManageSource, RestrictedViewProfiles: row.RestrictedViewProfiles}, nil
}

func (s *WorkspaceService) UpdateWorkbenchPermissions(ctx context.Context, id string, policy *types.ResourceAccessPolicy) (*types.WorkbenchPermissions, error) {
	return s.UpdateWorkbenchPermissionSettings(ctx, id, WorkbenchPermissionSettings{ResourceAccessPolicy: policy})
}

type WorkbenchPermissionSettings struct {
	*types.ResourceAccessPolicy
	RestrictedViewProfiles *[]types.WorkbenchRestrictedViewProfile `json:"restricted_view_profiles,omitempty"`
	SourceSharingApproved  *bool                                   `json:"source_sharing_approved,omitempty"`
}

func (s *WorkspaceService) UpdateWorkbenchPermissionSettings(ctx context.Context, id string, input WorkbenchPermissionSettings) (*types.WorkbenchPermissions, error) {
	policy := input.ResourceAccessPolicy
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
		if err := validateResourcePolicyMembers(ctx, tx, caller.TenantID, policy); err != nil {
			return err
		}
		profiles := row.RestrictedViewProfiles
		if input.RestrictedViewProfiles != nil {
			profiles = *input.RestrictedViewProfiles
		}
		if input.RestrictedViewProfiles != nil {
			if err := validateWorkbenchRestrictedProfiles(ctx, tx, caller.TenantID, row.UserID, policy, profiles); err != nil {
				return err
			}
			row.RestrictedViewProfiles = profiles
		}
		if input.SourceSharingApproved != nil {
			var currentKB types.KnowledgeBase
			if source == nil || source.TenantID != caller.TenantID {
				return apperrors.NewForbiddenError("仅来源知识库主人可授权研究数据共享")
			}
			if err := tx.Where("id = ? AND tenant_id = ?", source.ID, caller.TenantID).First(&currentKB).Error; err != nil {
				return workspaceDBError(err)
			}
			if currentKB.CreatorID != caller.UserID {
				return apperrors.NewForbiddenError("仅来源知识库主人可授权研究数据共享")
			}
			modules := types.WorkbenchRestrictedProfileModules(profiles)
			if *input.SourceSharingApproved && len(modules) == 0 {
				return apperrors.NewBadRequestError("请先配置受限视角页面")
			}
			row.SourceSharingApproved = *input.SourceSharingApproved
			row.SourceSharingApprovedBy = ""
			row.SourceSharingModules = []string{}
			if row.SourceSharingApproved {
				row.SourceSharingApprovedBy = caller.UserID
				row.SourceSharingModules = modules
			}
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
	ID              string                     `json:"id"`
	SourceType      string                     `json:"source_type"`
	SourceID        string                     `json:"source_id"`
	KnowledgeBaseID string                     `json:"knowledge_base_id"`
	TenantID        uint64                     `json:"tenant_id"`
	Kind            string                     `json:"kind"`
	Delegated       bool                       `json:"delegated"`
	Permission      string                     `json:"permission"`
	ViewModules     []string                   `json:"view_modules"`
	Knowledge       []WorkbenchSourceKnowledge `json:"knowledge"`
}

type WorkbenchSourceScope struct {
	InventoryIncluded  bool                           `json:"inventory_included"`
	ScopeVersion       int                            `json:"scope_version"`
	SourcesRevision    int64                          `json:"sources_revision"`
	ID                 string                         `json:"id"`
	UserID             string                         `json:"user_id"`
	ScopeTenantID      uint64                         `json:"scope_tenant_id"`
	KnowledgeBaseID    string                         `json:"knowledge_base_id"`
	CanEdit            bool                           `json:"can_edit"`
	CanManage          bool                           `json:"can_manage"`
	ViewModules        []string                       `json:"view_modules"`
	Sources            []WorkbenchSourceProjection    `json:"sources"`
	UnavailableSources []types.WorkbenchSourceSummary `json:"unavailable_sources"`
}

func (s *WorkspaceService) sourceProjection(ctx context.Context, kb *types.KnowledgeBase, kind string, delegated bool) (WorkbenchSourceProjection, error) {
	projection := WorkbenchSourceProjection{KnowledgeBaseID: kb.ID, TenantID: kb.TenantID, Kind: kind, Delegated: delegated, Permission: "viewer", Knowledge: []WorkbenchSourceKnowledge{}}
	err := s.db.WithContext(ctx).Model(&types.Knowledge{}).
		Select("id, file_hash, updated_at, enable_status, parse_status, type AS knowledge_type, file_type").
		Where("tenant_id = ? AND knowledge_base_id = ?", kb.TenantID, kb.ID).
		Where("LOWER(LTRIM(file_type,'.')) IN ?", []string{"xlsx", "xls", "xlsm", "csv", "tsv"}).
		Where("parse_status <> ? OR parse_status IS NULL", types.ParseStatusDeleting).Order("id").Limit(129).Scan(&projection.Knowledge).Error
	if err == nil && len(projection.Knowledge) > 128 {
		return projection, &apperrors.AppError{Code: apperrors.ErrValidation, HTTPCode: http.StatusRequestEntityTooLarge, Message: "当前研究来源超过128份表格原件容量，请拆分来源后重试；未静默截断数据"}
	}
	return projection, err
}

// This grants only an audited research projection. It never authorizes the raw
// KB endpoints, document downloads, arbitrary SQL, or document retrieval tools.
func (s *WorkspaceService) GetWorkbenchSourceScope(ctx context.Context, id string) (*WorkbenchSourceScope, error) {
	return s.getWorkbenchSourceScope(ctx, id, true)
}

func (s *WorkspaceService) GetWorkbenchSourceScopeMetadata(ctx context.Context, id string) (*WorkbenchSourceScope, error) {
	return s.getWorkbenchSourceScope(ctx, id, false)
}

func (s *WorkspaceService) getWorkbenchSourceScope(ctx context.Context, id string, inventory bool) (*WorkbenchSourceScope, error) {
	row, err := s.GetWorkbench(ctx, id)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	scope := &WorkbenchSourceScope{InventoryIncluded: inventory, ScopeVersion: 2, SourcesRevision: row.SourcesRevision, ID: row.ID, UserID: caller.UserID,
		ScopeTenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID, CanEdit: row.CanEdit, CanManage: row.CanManage,
		ViewModules: []string{}, Sources: []WorkbenchSourceProjection{}, UnavailableSources: []types.WorkbenchSourceSummary{}}
	bindings, err := s.sourceBindings(ctx, row)
	if err != nil {
		return nil, err
	}
	moduleSet := map[string]bool{}
	allDirect, active := true, 0
	for _, binding := range bindings {
		if !binding.Enabled || binding.Status != "active" {
			continue
		}
		resolved := s.resolveBoundSource(ctx, caller, row, binding)
		if binding.Enabled && binding.Status == "active" {
			active++
			allDirect = allDirect && resolved.direct
		}
		if !binding.Enabled || binding.Status != "active" || resolved.kb == nil || (!resolved.direct && len(resolved.modules) == 0) {
			scope.UnavailableSources = append(scope.UnavailableSources, sourceSummary(resolved))
			continue
		}
		kind := "knowledge_base"
		if binding.SourceType == types.WorkbenchSourceCentralDataset {
			kind = macroKnowledgeBaseKind
		} else if resolved.kb.ID == row.KnowledgeBaseID {
			kind = "primary"
		}
		projection := WorkbenchSourceProjection{KnowledgeBaseID: resolved.kb.ID, TenantID: resolved.kb.TenantID, Kind: kind, Delegated: !resolved.direct, Knowledge: []WorkbenchSourceKnowledge{}}
		if inventory {
			var err error
			projection, err = s.sourceProjection(ctx, resolved.kb, kind, !resolved.direct)
			if err != nil {
				return nil, err
			}
		}
		projection.ID = binding.ID
		projection.SourceType = binding.SourceType
		projection.SourceID = binding.SourceID
		projection.Permission = string(resolved.permission)
		projection.ViewModules = append([]string{}, resolved.modules...)
		if projection.Delegated {
			for _, module := range projection.ViewModules {
				moduleSet[module] = true
			}
		}
		scope.Sources = append(scope.Sources, projection)
	}
	for _, module := range types.WorkbenchViewModules() {
		if active > 0 && allDirect || moduleSet[module] {
			scope.ViewModules = append(scope.ViewModules, module)
		}
	}
	return scope, nil
}
