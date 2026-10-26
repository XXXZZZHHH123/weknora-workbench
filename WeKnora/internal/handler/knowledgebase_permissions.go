package handler

import (
	"context"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func kbCapabilityExtras(ctx context.Context, kb *types.KnowledgeBase) map[string]interface{} {
	caller := types.CallerFromContext(ctx)
	if kb == nil || kb.TenantID != caller.TenantID {
		return map[string]interface{}{"can_manage": false, "can_manage_permissions": false}
	}
	permission := access.LocalKBPermission(ctx, caller, kb)
	if kb.SystemRole == types.KnowledgeBaseSystemRoleCentral {
		admin := caller.Role.HasPermission(types.TenantRoleAdmin) && permission.IsValid()
		return map[string]interface{}{"my_permission": permission, "can_manage": admin, "can_manage_permissions": admin, "can_edit": admin && permission.IsValid() && permission.HasPermission(types.OrgRoleEditor), "can_delete": false, "access_legacy": false}
	}
	owner := kb.CreatorID != "" && caller.UserID == kb.CreatorID && caller.Role.HasPermission(types.TenantRoleContributor)
	canManage := owner
	canEdit := permission.IsValid() && permission.HasPermission(types.OrgRoleEditor) && caller.Role.HasPermission(types.TenantRoleContributor)
	if kb.AccessPolicy == nil {
		canManage = owner || caller.Role.HasPermission(types.TenantRoleAdmin)
		canEdit = caller.Role.HasPermission(types.TenantRoleContributor) && canManage
		permission = types.OrgRoleViewer
		if canEdit {
			permission = types.OrgRoleEditor
		}
		if owner {
			permission = types.OrgRoleAdmin
		}
	}
	return map[string]interface{}{"my_permission": permission, "can_manage": canManage, "can_manage_permissions": owner, "can_edit": canEdit, "access_legacy": kb.AccessPolicy == nil}
}

type kbPermissionsService interface {
	GetKnowledgeBasePermissions(context.Context, string) (*service.KnowledgeBasePermissions, error)
	UpdateKnowledgeBasePermissions(context.Context, string, *types.ResourceAccessPolicy) (*service.KnowledgeBasePermissions, error)
}

func (h *KnowledgeBaseHandler) GetKnowledgeBasePermissions(c *gin.Context) {
	s, ok := h.service.(kbPermissionsService)
	if !ok {
		_ = c.Error(apperrors.NewServiceUnavailableError("资源权限服务不可用"))
		return
	}
	result, err := s.GetKnowledgeBasePermissions(c.Request.Context(), c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func (h *KnowledgeBaseHandler) UpdateKnowledgeBasePermissions(c *gin.Context) {
	s, ok := h.service.(kbPermissionsService)
	if !ok {
		_ = c.Error(apperrors.NewServiceUnavailableError("资源权限服务不可用"))
		return
	}
	var policy types.ResourceAccessPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("权限设置格式无效"))
		return
	}
	result, err := s.UpdateKnowledgeBasePermissions(c.Request.Context(), c.Param("id"), &policy)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
