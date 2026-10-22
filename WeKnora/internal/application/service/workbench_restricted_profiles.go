package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func validateWorkbenchRestrictedProfiles(ctx context.Context, db *gorm.DB, tenantID uint64, ownerID string, policy *types.ResourceAccessPolicy, profiles []types.WorkbenchRestrictedViewProfile) error {
	if err := types.ValidateWorkbenchRestrictedViewProfiles(profiles); err != nil {
		return apperrors.NewBadRequestError(err.Error())
	}
	ids := map[string]struct{}{}
	for _, profile := range profiles {
		for _, id := range profile.MemberIDs {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	var members []types.TenantMember
	if err := db.WithContext(ctx).Where("tenant_id = ? AND user_id IN ? AND status = ?", tenantID, values, types.TenantMemberStatusActive).Find(&members).Error; err != nil {
		return err
	}
	if len(members) != len(values) {
		return apperrors.NewBadRequestError("受限视角只能选择当前空间的有效成员")
	}
	for _, member := range members {
		if !types.ResourceCanRead(types.ResourcePermissionFor(policy, ownerID, types.Caller{UserID: member.UserID, TenantID: tenantID, Role: member.Role})) {
			return apperrors.NewBadRequestError("请先为受限视角中的成员授予工作台查看或编辑权限")
		}
	}
	return nil
}
