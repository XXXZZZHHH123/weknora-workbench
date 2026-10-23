import type { TenantRole } from '@/api/tenant/members'

export function tenantMemberAccess(role: TenantRole | '', crossTenantAccess = false) {
  const canManageOwners = role === 'owner' || crossTenantAccess
  const canManage = canManageOwners || role === 'admin'
  return {
    canManage,
    canManageOwners,
    assignableRoles: (canManageOwners ? ['owner', 'admin', 'contributor', 'viewer'] : canManage ? ['admin', 'contributor', 'viewer'] : []) as TenantRole[],
    canManageMember: (target: { user_id: string; role: TenantRole }, currentUserId: string) =>
      canManage && target.user_id !== currentUserId && (target.role !== 'owner' || canManageOwners),
  }
}
