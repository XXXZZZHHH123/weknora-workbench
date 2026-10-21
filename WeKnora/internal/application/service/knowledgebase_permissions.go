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
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	OwnerID               string `json:"owner_id"`
	SourceSharingApproved bool   `json:"source_sharing_approved"`
}

func canManageKBPolicy(ctx context.Context, kb *types.KnowledgeBase) bool {
	caller := types.CallerFromContext(ctx)
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
		var rows []types.ResearchWorkbench
		if err := provider.ResourceAccessDB().WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_sharing_requested = ?", kb.TenantID, kb.ID, true).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			result.PendingWorkbenches = append(result.PendingWorkbenches, WorkbenchSourceRequest{ID: row.ID, Name: row.Name, OwnerID: row.UserID, SourceSharingApproved: row.SourceSharingApproved && row.SourceSharingApprovedBy == kb.CreatorID})
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
