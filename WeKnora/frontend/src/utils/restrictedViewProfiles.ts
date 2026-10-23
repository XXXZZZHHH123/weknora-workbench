import { sourceSharingModulesDraft, type SourceSharingModule } from './sourceSharingModules'
import type { ResourceAccessPolicy } from '../api/resourcePermissions'

export interface RestrictedViewProfile {
  id: string
  name: string
  modules: SourceSharingModule[]
  member_ids: string[]
  all_members: boolean
}

export function restrictedViewProfilesDraft(value: RestrictedViewProfile[] | null | undefined, previousModules: unknown): RestrictedViewProfile[] {
  if (Array.isArray(value)) return value.map(profile => ({ ...profile, modules: sourceSharingModulesDraft(profile.modules), member_ids: Array.isArray(profile.member_ids) ? [...profile.member_ids] : [] }))
  const modules = sourceSharingModulesDraft(previousModules)
  return modules.length ? [{ id: 'previous-sharing', name: '原有页面配置', modules, member_ids: [], all_members: true }] : []
}

export function eligibleProfileMembers(profiles: RestrictedViewProfile[], policy: ResourceAccessPolicy): RestrictedViewProfile[] {
  const allowed = new Set(policy.members.map(member => member.user_id))
  return profiles.map(profile => ({ ...profile, member_ids: profile.all_members ? [] : profile.member_ids.filter(id => policy.space_permission !== 'none' || allowed.has(id)) }))
}

export function validateRestrictedViewProfiles(profiles: RestrictedViewProfile[]): string {
  if (profiles.length > 32) return '最多设置 32 套受限视角。'
  const ids = new Set<string>()
  for (const profile of profiles) {
    if (!profile.id || ids.has(profile.id)) return '配置标识重复，请重新添加配置。'
    ids.add(profile.id)
    if (!profile.name.trim() || profile.name.trim().length > 80) return '请为每套配置填写 1–80 字的名称。'
    if (!profile.modules.length) return `请为“${profile.name.trim()}”至少选择一个页面。`
  }
  return ''
}
