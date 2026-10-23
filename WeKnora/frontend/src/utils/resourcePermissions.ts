import type { ResourceAccessPolicy, ResourcePermission } from '../api/resourcePermissions'

export function resourcePolicyDraft(policy: ResourceAccessPolicy | null): ResourceAccessPolicy {
  return policy ? { space_permission: policy.space_permission, members: policy.members.map(member => ({ ...member })) }
    : { space_permission: 'viewer', members: [] }
}

export function setResourceMemberPermission(policy: ResourceAccessPolicy, userId: string, permission: ResourcePermission | 'none', role: string): ResourceAccessPolicy {
  if (!userId || permission === 'editor' && role === 'viewer') return policy
  const members = policy.members.filter(member => member.user_id !== userId)
  if (permission !== 'none') members.push({ user_id: userId, permission })
  return { ...policy, members }
}

export function canManageResource(metadata: { can_manage?: boolean; my_permission?: string } | null | undefined): boolean {
  if (typeof metadata?.can_manage === 'boolean') return metadata.can_manage
  return metadata?.my_permission === 'admin' || metadata?.my_permission === 'owner'
}

export function canEditResource(metadata: { can_edit?: boolean; my_permission?: string } | null | undefined): boolean {
  if (typeof metadata?.can_edit === 'boolean') return metadata.can_edit
  return canManageResource(metadata) || metadata?.my_permission === 'editor'
}
