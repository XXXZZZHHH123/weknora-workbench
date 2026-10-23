package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// Raw storage URLs predate KB-scoped routes. Resolve their actual bindings
// before serving bytes so a copied URL cannot bypass an owner's new ACL.
func (s *resourceCatalog) AuthorizeResourceReference(ctx context.Context, reference string) error {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		return access.ErrUnauthorized
	}
	provider, ok := s.repo.(interface{ ResourceAccessDB() *gorm.DB })
	if !ok {
		return access.ErrForbidden
	}
	db := provider.ResourceAccessDB().WithContext(ctx)
	physical, resource, err := s.ResolvePath(ctx, reference)
	if err != nil {
		return err
	}
	if resource == nil {
		resource, err = s.repo.GetByTenantLocation(ctx, caller.TenantID, resourceLocationHash(physical))
		if err != nil {
			return err
		}
	}
	ids := []string{}
	if resource != nil {
		if resource.TenantID != caller.TenantID {
			return access.ErrForbidden
		}
		var bindings int64
		if err := db.Model(&types.ResourceBinding{}).Where("resource_id = ? AND tenant_id = ? AND owner_type = ?", resource.ID, caller.TenantID, types.ResourceOwnerKnowledge).Count(&bindings).Error; err != nil {
			return err
		}
		if bindings == 0 {
			return nil
		}
		err := db.Table("resource_bindings AS b").Joins("JOIN knowledges AS k ON k.id = b.owner_id AND k.tenant_id = b.tenant_id").
			Where("b.resource_id = ? AND b.owner_type = ? AND b.tenant_id = ?", resource.ID, types.ResourceOwnerKnowledge, caller.TenantID).
			Distinct().Pluck("k.knowledge_base_id", &ids).Error
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			// A missing origin must not turn a known KB resource into an
			// unrestricted non-KB artifact during deletion or repair.
			return access.ErrForbidden
		}
	} else {
		if err := db.Unscoped().Model(&types.Knowledge{}).Where("tenant_id = ? AND file_path IN ?", caller.TenantID, []string{reference, physical}).Distinct().Pluck("knowledge_base_id", &ids).Error; err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var kbs []types.KnowledgeBase
	if err := db.Where("id IN ? AND tenant_id = ?", ids, caller.TenantID).Find(&kbs).Error; err != nil {
		return err
	}
	for i := range kbs {
		permission := access.LocalKBPermission(ctx, caller, &kbs[i])
		if permission.IsValid() && permission.HasPermission(types.OrgRoleViewer) {
			return nil
		}
	}
	return access.ErrForbidden
}
