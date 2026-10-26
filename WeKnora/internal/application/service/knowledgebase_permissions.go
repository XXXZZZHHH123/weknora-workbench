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

type KnowledgeBasePermissions struct {
	Policy             *types.ResourceAccessPolicy `json:"policy"`
	OwnerID            string                      `json:"owner_id"`
	CanManage          bool                        `json:"can_manage"`
	Legacy             bool                        `json:"legacy"`
	PendingWorkbenches []WorkbenchSourceRequest    `json:"pending_workbenches"`
}

type WorkbenchSourceRequest struct {
	SourceBindingID       string   `json:"source_binding_id,omitempty"`
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	OwnerID               string   `json:"owner_id"`
	SourceSharingApproved bool     `json:"source_sharing_approved"`
	SourceSharingModules  []string `json:"source_sharing_modules"`
}

func canManageKBPolicy(ctx context.Context, kb *types.KnowledgeBase) bool {
	caller := types.CallerFromContext(ctx)
	if kb != nil && kb.SystemRole == types.KnowledgeBaseSystemRoleCentral {
		return kb.TenantID == caller.TenantID && caller.Role.HasPermission(types.TenantRoleAdmin) && access.LocalKBPermission(ctx, caller, kb).IsValid()
	}
	return kb != nil && caller.TenantID == kb.TenantID && kb.CreatorID != "" &&
		caller.UserID == kb.CreatorID && caller.Role.HasPermission(types.TenantRoleContributor)
}

func (s *knowledgeBaseService) GetKnowledgeBasePermissions(ctx context.Context, id string) (*KnowledgeBasePermissions, error) {
	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canManageKBPolicy(ctx, kb) {
		return nil, apperrors.NewForbiddenError("仅知识库主人可管理成员与权限")
	}
	result := &KnowledgeBasePermissions{Policy: kb.AccessPolicy, OwnerID: kb.CreatorID, CanManage: true, Legacy: kb.AccessPolicy == nil, PendingWorkbenches: []WorkbenchSourceRequest{}}
	if provider, ok := s.repo.(interface{ ResourceAccessDB() *gorm.DB }); ok {
		var bindings []struct {
			types.ResearchWorkbenchSourceBinding
			WorkbenchName string
			OwnerID       string
		}
		if err := provider.ResourceAccessDB().WithContext(ctx).Table("research_workbench_source_bindings b").
			Select("b.*, w.name AS workbench_name,w.user_id AS owner_id").
			Joins("JOIN research_workbenches w ON w.id=b.workbench_id AND w.deleted_at IS NULL").
			Where("w.tenant_id=? AND b.enabled=? AND b.status=?", kb.TenantID, true, "active").
			Where("(b.source_type=? AND b.source_id=?) OR (b.source_type=? AND b.source_id IN (SELECT id FROM central_datasets WHERE backing_knowledge_base_id=? AND tenant_id=?))", types.WorkbenchSourceKnowledgeBase, kb.ID, types.WorkbenchSourceCentralDataset, kb.ID, kb.TenantID).
			Where("(b.legacy=? OR b.sharing_requested=?) AND (b.sharing_requested=? OR b.sharing_approved=?)", false, true, true, true).Scan(&bindings).Error; err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			approved := binding.SharingApproved && binding.SharingApprovedBy == kb.CreatorID
			if kb.SystemRole == types.KnowledgeBaseSystemRoleCentral && binding.SharingApproved {
				var member types.TenantMember
				approved = provider.ResourceAccessDB().WithContext(ctx).Where("tenant_id=? AND user_id=? AND status=?", kb.TenantID, binding.SharingApprovedBy, types.TenantMemberStatusActive).First(&member).Error == nil && member.Role.HasPermission(types.TenantRoleAdmin)
			}
			modules := []string{}
			if approved && types.ValidWorkbenchSourceModules(binding.SharingModules) {
				modules = append(modules, binding.SharingModules...)
			}
			result.PendingWorkbenches = append(result.PendingWorkbenches, WorkbenchSourceRequest{ID: binding.WorkbenchID, SourceBindingID: binding.ID, Name: binding.WorkbenchName, OwnerID: binding.OwnerID, SourceSharingApproved: approved, SourceSharingModules: modules})
		}
		var rows []types.ResearchWorkbench
		if err := provider.ResourceAccessDB().WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_sharing_requested = ?", kb.TenantID, kb.ID, true).
			Where("NOT EXISTS (SELECT 1 FROM research_workbench_source_bindings b WHERE b.workbench_id=research_workbenches.id AND b.enabled=? AND b.status=? AND (b.legacy=? OR b.sharing_requested=?) AND b.source_id=?)", true, "active", false, true, kb.ID).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			approved := row.SourceSharingApproved && row.SourceSharingApprovedBy == kb.CreatorID
			modules := row.SourceSharingModules
			if !approved || !types.ValidWorkbenchSourceModules(modules) || modules == nil {
				modules = []string{}
			}
			result.PendingWorkbenches = append(result.PendingWorkbenches, WorkbenchSourceRequest{ID: row.ID, Name: row.Name, OwnerID: row.UserID, SourceSharingApproved: approved, SourceSharingModules: modules})
		}
	}
	return result, nil
}

func (s *knowledgeBaseService) UpdateKnowledgeBasePermissions(ctx context.Context, id string, policy *types.ResourceAccessPolicy) (*KnowledgeBasePermissions, error) {
	provider, ok := s.repo.(interface{ ResourceAccessDB() *gorm.DB })
	if !ok || provider.ResourceAccessDB() == nil {
		return nil, apperrors.NewServiceUnavailableError("资源权限存储不可用")
	}
	var result *KnowledgeBasePermissions
	err := provider.ResourceAccessDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var kb types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&kb).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperrors.NewNotFoundError("知识库不存在")
			}
			return err
		}
		if !canManageKBPolicy(ctx, &kb) {
			return apperrors.NewForbiddenError("仅知识库主人可管理成员与权限")
		}
		if err := validateResourcePolicyMembers(ctx, tx, kb.TenantID, policy); err != nil {
			return err
		}
		kb.AccessPolicy, kb.UpdatedAt = policy, time.Now()
		if err := tx.Model(&kb).Select("AccessPolicy", "UpdatedAt").Updates(&kb).Error; err != nil {
			return err
		}
		resourceIDs := tx.Table("resource_bindings AS b").Select("b.resource_id").
			Joins("JOIN knowledges AS k ON k.id = b.owner_id AND k.tenant_id = b.tenant_id").
			Where("b.owner_type = ? AND b.tenant_id = ? AND k.knowledge_base_id = ?", types.ResourceOwnerKnowledge, kb.TenantID, kb.ID)
		if err := tx.Model(&types.ResourceAccessGrant{}).Where("resource_id IN (?) AND revoked_at IS NULL", resourceIDs).Update("revoked_at", time.Now()).Error; err != nil {
			return err
		}
		result = &KnowledgeBasePermissions{Policy: policy, OwnerID: kb.CreatorID, CanManage: true}
		return nil
	})
	if err == nil {
		recordKBActivity(ctx, s.audit, types.CallerFromContext(ctx).TenantID, id, types.AuditActionKBUpdated,
			"knowledge_base", id, types.AuditOutcomeSuccess, map[string]any{"changed_fields": []string{"access_policy"}})
	}
	return result, err
}

// filterLocalKBPolicies runs before counts, profiles, pinning and model source
// selection so private resource names do not enter unrelated users' context.
func filterLocalKBPolicies(ctx context.Context, rows []*types.KnowledgeBase) []*types.KnowledgeBase {
	caller := types.CallerFromContext(ctx)
	visible := make([]*types.KnowledgeBase, 0, len(rows))
	for _, kb := range rows {
		if kb == nil {
			continue
		}
		if kb.AccessPolicy != nil && kb.TenantID == caller.TenantID {
			permission := access.LocalKBPermission(ctx, caller, kb)
			if !permission.IsValid() || !permission.HasPermission(types.OrgRoleViewer) {
				continue
			}
		}
		visible = append(visible, kb)
	}
	return visible
}
