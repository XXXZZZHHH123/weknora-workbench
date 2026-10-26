package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxWorkbenchSources = 8

type WorkbenchSourcesInput struct {
	ExpectedRevision int64                        `json:"expected_revision"`
	Sources          []types.WorkbenchSourceInput `json:"sources"`
}

type WorkbenchSourcesView struct {
	ScopeVersion    int                            `json:"scope_version"`
	SourcesRevision int64                          `json:"sources_revision"`
	Sources         []types.WorkbenchSourceSummary `json:"sources"`
}

type resolvedWorkbenchSource struct {
	binding    types.ResearchWorkbenchSourceBinding
	kb         *types.KnowledgeBase
	permission types.OrgMemberRole
	modules    []string
	direct     bool
	manage     bool
	edit       bool
	retained   bool
}

// Register only an existing Default. This never creates an empty replacement
// for a deleted reservation and never rewrites its ACL or structured snapshots.
func (s *WorkspaceService) registerCentralDefault(ctx context.Context, caller types.Caller) (*types.CentralDataset, error) {
	_, kb, err := s.defaultKnowledgeBase(ctx, caller)
	if err != nil || kb == nil || kb.DeletedAt.Valid {
		return nil, err
	}
	row := types.CentralDataset{ID: kb.ID, TenantID: caller.TenantID, Kind: macroKnowledgeBaseKind,
		Name: kb.Name, BackingKnowledgeBaseID: kb.ID}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "kind"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Where("tenant_id = ? AND kind = ?", caller.TenantID, macroKnowledgeBaseKind).First(&row).Error; err != nil {
			return err
		}
		if row.BackingKnowledgeBaseID != kb.ID {
			return apperrors.NewConflictError("中心数据集来源与Default保留身份不符")
		}
		return tx.Model(&types.KnowledgeBase{}).Where("id = ? AND tenant_id = ?", kb.ID, kb.TenantID).Update("system_role", types.KnowledgeBaseSystemRoleCentral).Error
	})
	return &row, err
}

func (s *WorkspaceService) InitializeCentralDataset(ctx context.Context) (*types.CentralDataset, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !caller.Role.HasPermission(types.TenantRoleAdmin) {
		return nil, apperrors.NewForbiddenError("仅空间管理员可初始化中心数据集")
	}
	if _, err := s.InitializeDefaultKnowledgeBase(ctx); err != nil {
		return nil, err
	}
	return s.registerCentralDefault(ctx, caller)
}

func (s *WorkspaceService) ListCentralDatasets(ctx context.Context) ([]types.CentralDataset, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	var rows []types.CentralDataset
	if err := s.db.WithContext(ctx).Where("tenant_id = ?", caller.TenantID).Order("created_at,id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]types.CentralDataset, 0, len(rows))
	for _, row := range rows {
		if kb, err := s.readableKB(ctx, row.BackingKnowledgeBaseID); err == nil {
			row.Name = kb.Name
			result = append(result, row)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
	return result, nil
}

func validSourceInput(in types.WorkbenchSourceInput) bool {
	return (in.SourceType == types.WorkbenchSourceKnowledgeBase || in.SourceType == types.WorkbenchSourceCentralDataset) &&
		in.SourceID != "" && len(in.SourceID) <= 36 && strings.TrimSpace(in.SourceID) == in.SourceID && !strings.ContainsAny(in.SourceID, "\x00\r\n\t")
}

func (s *WorkspaceService) resolveSourceInput(ctx context.Context, in types.WorkbenchSourceInput) (resolvedWorkbenchSource, error) {
	var result resolvedWorkbenchSource
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return result, err
	}
	if !validSourceInput(in) {
		return result, apperrors.NewBadRequestError("来源类型或ID无效")
	}
	id := in.SourceID
	if in.SourceType == types.WorkbenchSourceCentralDataset {
		var dataset types.CentralDataset
		if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", in.SourceID, caller.TenantID).First(&dataset).Error; err != nil {
			return result, workspaceDBError(err)
		}
		id = dataset.BackingKnowledgeBaseID
	}
	kb, err := s.readableKB(ctx, id)
	if err != nil {
		return result, err
	}
	if in.SourceType == types.WorkbenchSourceKnowledgeBase && kb.SystemRole == types.KnowledgeBaseSystemRoleCentral {
		return result, apperrors.NewBadRequestError("请通过中心数据集选择该系统来源")
	}
	if in.SourceType == types.WorkbenchSourceCentralDataset && kb.TenantID != caller.TenantID {
		return result, apperrors.NewForbiddenError("中心数据集未授权跨空间访问")
	}
	result.kb = kb
	result.binding = types.ResearchWorkbenchSourceBinding{ID: uuid.NewString(), SourceType: in.SourceType, SourceID: in.SourceID,
		SourceTenantID: kb.TenantID, Name: kb.Name, Enabled: true, Status: "active", SharingModules: []string{}}
	return result, nil
}

func (s *WorkspaceService) resolveSourceInputs(ctx context.Context, inputs []types.WorkbenchSourceInput) ([]resolvedWorkbenchSource, error) {
	if len(inputs) > maxWorkbenchSources {
		return nil, apperrors.NewBadRequestError("每个研究台最多配置8个来源")
	}
	seen := map[string]bool{}
	result := make([]resolvedWorkbenchSource, 0, len(inputs))
	for _, in := range inputs {
		resolved, err := s.resolveSourceInput(ctx, in)
		if err != nil {
			return nil, err
		}
		key := resolved.kb.ID
		if seen[key] {
			return nil, apperrors.NewBadRequestError("同一真实来源不能重复配置")
		}
		seen[key] = true
		result = append(result, resolved)
	}
	return result, nil
}

// Real migrations backfill once. Revision zero also supports legacy records
// written by an older concurrent service: the tombstones remain authoritative.
func (s *WorkspaceService) ensureWorkbenchSources(ctx context.Context, row *types.ResearchWorkbench) error {
	if row.SourcesRevision > 0 {
		return nil
	}
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return err
	}
	central, err := s.registerCentralDefault(ctx, caller)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND tenant_id = ?", row.ID, row.TenantID).Error; err != nil {
			return err
		}
		if current.SourcesRevision > 0 {
			row.SourcesRevision = current.SourcesRevision
			return nil
		}
		bindings := []types.ResearchWorkbenchSourceBinding{}
		if current.KnowledgeBaseID != "" {
			var kb types.KnowledgeBase
			if err := tx.Unscoped().First(&kb, "id = ?", current.KnowledgeBaseID).Error; err == nil {
				kind, id := types.WorkbenchSourceKnowledgeBase, kb.ID
				if central != nil && central.BackingKnowledgeBaseID == kb.ID {
					kind, id = types.WorkbenchSourceCentralDataset, central.ID
				}
				status := "active"
				if kb.DeletedAt.Valid {
					status = "deleted"
				}
				bindings = append(bindings, types.ResearchWorkbenchSourceBinding{ID: current.ID, WorkbenchID: current.ID, SourceType: kind, SourceID: id,
					SourceTenantID: kb.TenantID, Name: kb.Name, Enabled: !kb.DeletedAt.Valid, Status: status, Legacy: true,
					SharingApproved: current.SourceSharingApproved, SharingRequested: current.SourceSharingRequested, SharingApprovedBy: current.SourceSharingApprovedBy, SharingModules: current.SourceSharingModules})
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if central != nil && central.BackingKnowledgeBaseID != current.KnowledgeBaseID {
			bindings = append(bindings, types.ResearchWorkbenchSourceBinding{ID: current.ID + "-macro", WorkbenchID: current.ID,
				SourceType: types.WorkbenchSourceCentralDataset, SourceID: central.ID, SourceTenantID: caller.TenantID,
				Name: central.Name, Enabled: true, Status: "active", SharingModules: []string{}})
		}
		for _, binding := range bindings {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "workbench_id"}, {Name: "source_type"}, {Name: "source_id"}}, DoNothing: true}).Create(&binding).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&current).Update("sources_revision", 1).Error; err != nil {
			return err
		}
		row.SourcesRevision = 1
		return nil
	})
}

func (s *WorkspaceService) sourceBindings(ctx context.Context, row *types.ResearchWorkbench) ([]types.ResearchWorkbenchSourceBinding, error) {
	if err := s.ensureWorkbenchSources(ctx, row); err != nil {
		return nil, err
	}
	bindings := []types.ResearchWorkbenchSourceBinding{}
	err := s.db.WithContext(ctx).Where("workbench_id = ? AND enabled = ? AND status = ?", row.ID, true, "active").Order("created_at,id").Limit(maxWorkbenchSources + 1).Find(&bindings).Error
	if err == nil && len(bindings) > maxWorkbenchSources {
		return nil, apperrors.NewConflictError("工作台当前来源超过8个，请核对来源配置；未截断授权范围")
	}
	return bindings, err
}

func (s *WorkspaceService) resolveBoundSource(ctx context.Context, caller types.Caller, row *types.ResearchWorkbench, binding types.ResearchWorkbenchSourceBinding) resolvedWorkbenchSource {
	result := resolvedWorkbenchSource{binding: binding, modules: []string{}}
	if !binding.Enabled || binding.Status != "active" {
		return result
	}
	id := binding.SourceID
	if binding.SourceType == types.WorkbenchSourceCentralDataset {
		var dataset types.CentralDataset
		if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, caller.TenantID).First(&dataset).Error; err != nil {
			return result
		}
		id = dataset.BackingKnowledgeBaseID
	} else if binding.SourceType != types.WorkbenchSourceKnowledgeBase {
		return result
	}
	kb, err := s.kbs.GetKnowledgeBaseByIDOnly(ctx, id)
	if err != nil || kb == nil || kb.DeletedAt.Valid || kb.TenantID != binding.SourceTenantID {
		return result
	}
	// The persisted DB marker is authoritative even when a fixture/service
	// caches the native KB object.
	if binding.SourceType == types.WorkbenchSourceCentralDataset {
		copy := *kb
		copy.SystemRole = types.KnowledgeBaseSystemRoleCentral
		kb = &copy
	}
	result.kb = kb
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, s.shares, nil)
	result.direct = err == nil
	if result.direct {
		result.permission = grant.Permission
		result.modules = types.WorkbenchViewModules()
	}
	result.manage = kb.TenantID == caller.TenantID && ((binding.SourceType == types.WorkbenchSourceCentralDataset && caller.Role.HasPermission(types.TenantRoleAdmin) && result.direct) ||
		(binding.SourceType == types.WorkbenchSourceKnowledgeBase && kb.CreatorID != "" && kb.CreatorID == caller.UserID && caller.Role.HasPermission(types.TenantRoleContributor)))
	if result.direct && caller.Role.HasPermission(types.TenantRoleContributor) {
		_, editErr := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleEditor, s.shares, nil)
		result.edit = editErr == nil
		if kb.AccessPolicy == nil && kb.TenantID == caller.TenantID {
			result.edit = result.edit && (kb.CreatorID == caller.UserID || caller.Role.HasPermission(types.TenantRoleAdmin))
		}
	}
	approved, by, modules := binding.SharingApproved, binding.SharingApprovedBy, binding.SharingModules
	if binding.Legacy {
		approved, by, modules = row.SourceSharingApproved, row.SourceSharingApprovedBy, row.SourceSharingModules
		result.binding.SharingRequested = binding.SharingRequested || row.SourceSharingRequested
	}
	result.binding.SharingApproved = approved
	result.binding.SharingApprovedBy = by
	result.binding.SharingModules = modules
	validApprover := by != "" && by == kb.CreatorID
	if binding.SourceType == types.WorkbenchSourceCentralDataset && by != "" {
		var member types.TenantMember
		validApprover = s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND status = ?", caller.TenantID, by, types.TenantMemberStatusActive).First(&member).Error == nil && member.Role.HasPermission(types.TenantRoleAdmin)
	}
	result.binding.SharingApproved = approved && validApprover && types.ValidWorkbenchSourceModules(modules)
	if !result.binding.SharingApproved {
		result.binding.SharingModules = []string{}
	}
	if !result.direct && approved && validApprover && types.ValidWorkbenchSourceModules(modules) {
		result.modules = types.EffectiveWorkbenchSourceModules(row.RestrictedViewProfiles, caller.UserID, modules)
		if len(result.modules) > 0 {
			result.permission = types.OrgRoleViewer
		}
	}
	return result
}

func sourceSummary(resolved resolvedWorkbenchSource) types.WorkbenchSourceSummary {
	b := resolved.binding
	result := types.WorkbenchSourceSummary{ID: b.ID, SourceType: b.SourceType, SourceID: b.SourceID, Status: b.Status,
		CanRead: resolved.direct, CanEdit: resolved.edit, CanManageSource: resolved.manage, Delegated: !resolved.direct && len(resolved.modules) > 0, ViewModules: resolved.modules,
		SharingApproved: b.SharingApproved, SharingRequested: b.SharingRequested, SharingModules: append([]string{}, b.SharingModules...)}
	if resolved.kb != nil && (resolved.direct || len(resolved.modules) > 0) {
		result.KnowledgeBaseID = resolved.kb.ID
		result.TenantID = resolved.kb.TenantID
		result.Name = resolved.kb.Name
	} else if b.Enabled && b.Status == "active" {
		result.Status = "unavailable"
	}
	return result
}

func (s *WorkspaceService) populateSources(ctx context.Context, caller types.Caller, row *types.ResearchWorkbench) error {
	bindings, err := s.sourceBindings(ctx, row)
	if err != nil {
		return err
	}
	row.ScopeVersion = 2
	row.SourcesSummary = []types.WorkbenchSourceSummary{}
	row.SourceCount = 0
	row.CanReadSource = false
	row.CanEditSource = false
	row.SourceDataAvailable = false
	row.DefaultKnowledgeBase = nil
	allDirect, active := true, 0
	delegatedModules := map[string]bool{}
	for _, binding := range bindings {
		if !binding.Enabled || binding.Status != "active" {
			continue
		}
		resolved := s.resolveBoundSource(ctx, caller, row, binding)
		summary := sourceSummary(resolved)
		row.SourcesSummary = append(row.SourcesSummary, summary)
		row.CanEditSource = row.CanEditSource || resolved.edit
		if binding.SourceType == types.WorkbenchSourceCentralDataset && resolved.direct {
			row.DefaultKnowledgeBase = &types.ResearchWorkbenchSource{KnowledgeBaseID: resolved.kb.ID, TenantID: resolved.kb.TenantID, Kind: macroKnowledgeBaseKind, Name: resolved.kb.Name}
		}
		if binding.Enabled && binding.Status == "active" {
			active++
			allDirect = allDirect && resolved.direct
		}
		if resolved.direct || len(resolved.modules) > 0 {
			row.SourceDataAvailable = true
			row.SourceCount++
		}
		if !resolved.direct {
			for _, module := range resolved.modules {
				delegatedModules[module] = true
			}
		}
	}
	row.CanReadSource = active > 0 && allDirect
	row.EffectiveViewModules = []string{}
	for _, module := range types.WorkbenchViewModules() {
		if row.CanReadSource || delegatedModules[module] {
			row.EffectiveViewModules = append(row.EffectiveViewModules, module)
		}
	}
	return nil
}

func (s *WorkspaceService) GetWorkbenchSources(ctx context.Context, id string) (*WorkbenchSourcesView, error) {
	row, err := s.GetWorkbench(ctx, id)
	if err != nil {
		return nil, err
	}
	return &WorkbenchSourcesView{ScopeVersion: 2, SourcesRevision: row.SourcesRevision, Sources: row.SourcesSummary}, nil
}

func (s *WorkspaceService) UpdateWorkbenchSources(ctx context.Context, id string, input WorkbenchSourcesInput) (*WorkbenchSourcesView, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.workbench(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	if input.ExpectedRevision < 1 || input.Sources == nil {
		return nil, apperrors.NewBadRequestError("请提供当前来源版本与来源数组，空数组表示解除全部来源")
	}
	if len(input.Sources) > maxWorkbenchSources {
		return nil, apperrors.NewBadRequestError("每个研究台最多配置8个来源")
	}
	bindings, err := s.sourceBindings(ctx, row)
	if err != nil {
		return nil, err
	}
	retained := map[string]types.ResearchWorkbenchSourceBinding{}
	for _, binding := range bindings {
		if binding.Enabled && binding.Status == "active" {
			retained[binding.SourceType+":"+binding.SourceID] = binding
		}
	}
	resolved := make([]resolvedWorkbenchSource, 0, len(input.Sources))
	seen := map[string]bool{}
	for _, in := range input.Sources {
		if !validSourceInput(in) {
			return nil, apperrors.NewBadRequestError("来源类型或ID无效")
		}
		key := in.SourceType + ":" + in.SourceID
		if seen[key] {
			return nil, apperrors.NewBadRequestError("来源不能重复配置")
		}
		seen[key] = true
		if old, ok := retained[key]; ok {
			current := s.resolveBoundSource(ctx, caller, row, old)
			current.retained = true
			resolved = append(resolved, current)
		} else {
			current, err := s.resolveSourceInput(ctx, in)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, current)
		}
	}
	backing := map[string]bool{}
	for _, source := range resolved {
		if source.kb != nil {
			if backing[source.kb.ID] {
				return nil, apperrors.NewBadRequestError("同一真实来源不能重复配置")
			}
			backing[source.kb.ID] = true
		}
	}
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].binding.SourceType+":"+resolved[i].binding.SourceID < resolved[j].binding.SourceType+":"+resolved[j].binding.SourceID
	})

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, source := range resolved {
			if source.retained {
				continue
			}
			if tx.Dialector.Name() == "sqlite" {
				if err := tx.Exec("UPDATE knowledge_bases SET id=id WHERE id=? AND deleted_at IS NULL", source.kb.ID).Error; err != nil {
					return err
				}
			}
			var current types.KnowledgeBase
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND tenant_id = ?", source.kb.ID, source.kb.TenantID).Error; err != nil {
				return workspaceDBError(err)
			}
			if _, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, &current, types.OrgRoleViewer, nil, nil); err != nil && current.TenantID == caller.TenantID {
				return apperrors.NewForbiddenError("来源读取权限已改变")
			}
		}
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND tenant_id = ?", row.ID, caller.TenantID).Error; err != nil {
			return workspaceDBError(err)
		}
		if !canManageWorkbench(caller, &current) {
			return apperrors.NewForbiddenError("仅工作台主人可管理来源")
		}
		if current.SourcesRevision != input.ExpectedRevision {
			return apperrors.NewConflictError("来源版本已变化，请刷新后重试")
		}
		var existing []types.ResearchWorkbenchSourceBinding
		if err := tx.Where("workbench_id = ?", row.ID).Find(&existing).Error; err != nil {
			return err
		}
		byKey := map[string]types.ResearchWorkbenchSourceBinding{}
		for _, binding := range existing {
			byKey[binding.SourceType+":"+binding.SourceID] = binding
		}
		keep := []string{}
		primary := ""
		for _, source := range resolved {
			binding := source.binding
			key := binding.SourceType + ":" + binding.SourceID
			if old, ok := byKey[key]; ok {
				if source.retained {
					if !old.Enabled || old.Status != "active" || old.ID != source.binding.ID || old.SharingVersion != source.binding.SharingVersion {
						return apperrors.NewConflictError("来源关联已变化，请刷新后重试")
					}
					binding = old
				} else {
					binding.ID = old.ID
					binding.CreatedAt = old.CreatedAt
				}
			}
			binding.WorkbenchID = row.ID
			keep = append(keep, binding.ID)
			if binding.SourceType == types.WorkbenchSourceKnowledgeBase && primary == "" {
				primary = binding.SourceID
			}
			if !source.retained {
				if err := tx.Save(&binding).Error; err != nil {
					return err
				}
			}
		}

		q := tx.Model(&types.ResearchWorkbenchSourceBinding{}).Where("workbench_id = ?", row.ID)
		if len(keep) > 0 {
			q = q.Where("id NOT IN ?", keep)
		}
		if err := q.Updates(map[string]any{"enabled": false, "status": "removed", "sharing_approved": false, "sharing_approved_by": "", "sharing_modules": "[]", "legacy": false, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]any{"knowledge_base_id": primary, "sources_revision": current.SourcesRevision + 1, "updated_at": time.Now()}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkbenchSources(ctx, id)
}

type WorkbenchSourceSharingInput struct {
	Approved bool     `json:"approved"`
	Modules  []string `json:"modules"`
}

func (s *WorkspaceService) sourceSharingBinding(ctx context.Context, id, bindingID string) (*types.ResearchWorkbench, *resolvedWorkbenchSource, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	var row types.ResearchWorkbench
	if err := s.db.WithContext(ctx).First(&row, "id = ? AND tenant_id = ?", id, caller.TenantID).Error; err != nil {
		return nil, nil, workspaceDBError(err)
	}
	bindings, err := s.sourceBindings(ctx, &row)
	if err != nil {
		return nil, nil, err
	}
	for _, binding := range bindings {
		if binding.ID == bindingID && binding.Enabled && binding.Status == "active" {
			resolved := s.resolveBoundSource(ctx, caller, &row, binding)
			if !resolved.manage {
				return nil, nil, apperrors.NewForbiddenError("仅真实来源主人或中心数据集管理员可授权")
			}
			return &row, &resolved, nil
		}
	}
	return nil, nil, apperrors.NewNotFoundError("来源关联不存在")
}

func (s *WorkspaceService) GetSourceBindingSharing(ctx context.Context, id, bindingID string) (*WorkbenchSourceSharing, error) {
	_, source, err := s.sourceSharingBinding(ctx, id, bindingID)
	if err != nil {
		return nil, err
	}
	return &WorkbenchSourceSharing{Approved: source.binding.SharingApproved, Requested: source.binding.SharingRequested, CanManageSource: true, SourceSharingModules: source.binding.SharingModules}, nil
}

func (s *WorkspaceService) RequestSourceBindingSharing(ctx context.Context, id, bindingID string) (*WorkbenchSourceSharing, error) {
	row, err := s.workbench(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=? AND tenant_id=?", id, caller.TenantID).Error; err != nil {
			return workspaceDBError(err)
		}
		if !canManageWorkbench(caller, &current) {
			return apperrors.NewForbiddenError("仅工作台主人可申请来源授权")
		}
		if current.SourcesRevision != row.SourcesRevision {
			return apperrors.NewConflictError("来源已变化，请刷新后重试")
		}
		var binding types.ResearchWorkbenchSourceBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "id=? AND workbench_id=? AND enabled=? AND status=?", bindingID, id, true, "active").Error; err != nil {
			return workspaceDBError(err)
		}
		return tx.Model(&binding).Updates(map[string]any{"sharing_requested": true, "sharing_version": gorm.Expr("sharing_version+1"), "updated_at": time.Now()}).Error
	})
	if err != nil {
		return nil, err
	}
	return &WorkbenchSourceSharing{Requested: true, SourceSharingModules: []string{}}, nil
}

func (s *WorkspaceService) UpdateSourceBindingSharing(ctx context.Context, id, bindingID string, input WorkbenchSourceSharingInput) (*WorkbenchSourceSharing, error) {
	if !types.ValidWorkbenchSourceModules(input.Modules) || (input.Approved && len(input.Modules) == 0) {
		return nil, apperrors.NewBadRequestError("请选择明确的有效共享页面")
	}
	row, source, err := s.sourceSharingBinding(ctx, id, bindingID)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Exec("UPDATE knowledge_bases SET id=id WHERE id=? AND deleted_at IS NULL", source.kb.ID).Error; err != nil {
				return err
			}
		}
		var kb types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&kb, "id = ? AND tenant_id = ?", source.kb.ID, source.kb.TenantID).Error; err != nil {
			return workspaceDBError(err)
		}
		if source.binding.SourceType == types.WorkbenchSourceKnowledgeBase && kb.CreatorID != caller.UserID {
			return apperrors.NewForbiddenError("来源主人已变化")
		}
		if source.binding.SourceType == types.WorkbenchSourceCentralDataset && (!caller.Role.HasPermission(types.TenantRoleAdmin) || !access.LocalKBPermission(ctx, caller, &kb).IsValid()) {
			return apperrors.NewForbiddenError("中心来源管理员读取权限已失效")
		}
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=? AND tenant_id=?", id, caller.TenantID).Error; err != nil {
			return workspaceDBError(err)
		}
		var binding types.ResearchWorkbenchSourceBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "id = ? AND workbench_id = ? AND enabled = ? AND status = ?", bindingID, id, true, "active").Error; err != nil {
			return workspaceDBError(err)
		}
		binding.SharingApproved = input.Approved
		binding.SharingRequested = false
		binding.SharingApprovedBy = ""
		binding.SharingModules = []string{}
		binding.Legacy = false
		binding.SharingVersion++
		if input.Approved {
			binding.SharingApprovedBy = caller.UserID
			binding.SharingModules = append([]string{}, input.Modules...)
		}
		if err := tx.Save(&binding).Error; err != nil {
			return err
		}
		return tx.Model(row).Update("sources_revision", gorm.Expr("sources_revision + 1")).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetSourceBindingSharing(ctx, id, bindingID)
}
