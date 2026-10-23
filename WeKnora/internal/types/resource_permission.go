package types

import (
	"fmt"
	"strings"
)

const (
	ResourcePermissionNone   = "none"
	ResourcePermissionViewer = "viewer"
	ResourcePermissionEditor = "editor"
	ResourcePermissionOwner  = "owner"
)

type ResourceMemberPermission struct {
	UserID     string `json:"user_id"`
	Permission string `json:"permission"`
}

// A nil policy identifies resources whose existing access must survive upgrade.
// New resources always receive an explicit policy, including empty member lists.
type ResourceAccessPolicy struct {
	SpacePermission string                     `json:"space_permission"`
	Members         []ResourceMemberPermission `json:"members"`
}

func NewResourceAccessPolicy() *ResourceAccessPolicy {
	return &ResourceAccessPolicy{SpacePermission: ResourcePermissionNone, Members: []ResourceMemberPermission{}}
}

func ValidateResourceAccessPolicy(policy *ResourceAccessPolicy) error {
	if policy == nil {
		return fmt.Errorf("必须提供资源权限")
	}
	switch policy.SpacePermission {
	case ResourcePermissionNone, ResourcePermissionViewer, ResourcePermissionEditor:
	default:
		return fmt.Errorf("空间默认权限无效")
	}
	if len(policy.Members) > 1000 {
		return fmt.Errorf("指定成员过多")
	}
	seen := make(map[string]bool, len(policy.Members))
	for _, member := range policy.Members {
		if member.UserID == "" || member.UserID != strings.TrimSpace(member.UserID) || len(member.UserID) > 512 || seen[member.UserID] {
			return fmt.Errorf("指定成员无效或重复")
		}
		if member.Permission != ResourcePermissionViewer && member.Permission != ResourcePermissionEditor {
			return fmt.Errorf("成员权限仅支持查看或编辑")
		}
		seen[member.UserID] = true
	}
	return nil
}

// ResourcePermissionFor is only for members of the resource's verified space.
// The caller's role is an upper bound; Admin does not imply a resource grant.
// An empty result requires the caller to apply its existing legacy policy.
func ResourcePermissionFor(policy *ResourceAccessPolicy, ownerID string, caller Caller) string {
	if policy == nil {
		return ""
	}
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.HasPermission(TenantRoleViewer) {
		return ResourcePermissionNone
	}
	if ownerID != "" && ownerID == caller.UserID {
		if caller.Role.HasPermission(TenantRoleContributor) {
			return ResourcePermissionOwner
		}
		return ResourcePermissionViewer
	}
	permission := policy.SpacePermission
	if permission != ResourcePermissionViewer && permission != ResourcePermissionEditor {
		permission = ResourcePermissionNone
	}
	for _, member := range policy.Members {
		if member.UserID == caller.UserID {
			if member.Permission == ResourcePermissionEditor || permission == ResourcePermissionNone && member.Permission == ResourcePermissionViewer {
				permission = member.Permission
			}
		}
	}
	if permission == ResourcePermissionEditor && !caller.Role.HasPermission(TenantRoleContributor) {
		permission = ResourcePermissionViewer
	}
	return permission
}

func ResourceCanRead(permission string) bool {
	return permission == ResourcePermissionViewer || ResourceCanEdit(permission)
}

func ResourceCanEdit(permission string) bool {
	return permission == ResourcePermissionEditor || permission == ResourcePermissionOwner
}
