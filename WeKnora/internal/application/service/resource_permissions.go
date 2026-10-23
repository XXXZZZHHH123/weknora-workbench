package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func validateResourcePolicyMembers(ctx context.Context, db *gorm.DB, tenantID uint64, policy *types.ResourceAccessPolicy) error {
	if err := types.ValidateResourceAccessPolicy(policy); err != nil {
		return apperrors.NewBadRequestError(err.Error())
	}
	if len(policy.Members) == 0 {
		return nil
	}
	ids := make([]string, 0, len(policy.Members))
	for _, member := range policy.Members {
		ids = append(ids, member.UserID)
	}
	var members []types.TenantMember
	if err := db.WithContext(ctx).Where("tenant_id = ? AND user_id IN ? AND status = ?", tenantID, ids, types.TenantMemberStatusActive).Find(&members).Error; err != nil {
		return err
	}
	if len(members) != len(ids) {
		return apperrors.NewBadRequestError("只能选择当前空间的有效成员")
	}
	roles := make(map[string]types.TenantRole, len(members))
	for _, member := range members {
		roles[member.UserID] = member.Role
	}
	for _, member := range policy.Members {
		if member.Permission == types.ResourcePermissionEditor && !roles[member.UserID].HasPermission(types.TenantRoleContributor) {
			return apperrors.NewBadRequestError("空间只读成员不能授予编辑权限")
		}
	}
	return nil
}
