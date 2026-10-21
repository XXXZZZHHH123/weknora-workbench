import { get, post, put } from '@/utils/request'

export type ResourcePermission = 'viewer' | 'editor'
export type SpaceResourcePermission = 'none' | ResourcePermission
export type ResourceKind = 'knowledge-base' | 'research-workbench'
export interface ResourceAccessPolicy {
  space_permission: SpaceResourcePermission
  members: Array<{ user_id: string; permission: ResourcePermission }>
}
export interface ResourcePermissionsView {
  policy: ResourceAccessPolicy | null
  owner_id: string
  can_manage: boolean
  legacy: boolean
  source_sharing_approved?: boolean
  can_manage_source?: boolean
  source_sharing_requested?: boolean
  pending_workbenches?: Array<{ id: string; name: string; owner_id: string; source_sharing_approved: boolean }>
}
type Envelope<T> = { success: boolean; data: T; message?: string }
function base(kind: ResourceKind, id: string) {
  const encoded = encodeURIComponent(id)
  return kind === 'knowledge-base' ? `/api/v1/knowledge-bases/${encoded}` : `/api/v1/workspaces/research/${encoded}`
}
export function getResourcePermissions(kind: ResourceKind, id: string): Promise<Envelope<ResourcePermissionsView>> {
  return get(`${base(kind, id)}/permissions`)
}
export function updateResourcePermissions(kind: ResourceKind, id: string, policy: ResourceAccessPolicy): Promise<Envelope<ResourcePermissionsView>> {
  return put(`${base(kind, id)}/permissions`, policy)
}
export function updateWorkbenchSourceSharing(id: string, approved: boolean): Promise<Envelope<unknown>> {
  return put(`${base('research-workbench', id)}/source-sharing`, { approved })
}
export function requestWorkbenchSourceSharing(id: string): Promise<Envelope<unknown>> {
  return post(`${base('research-workbench', id)}/source-sharing/request`, {})
}
