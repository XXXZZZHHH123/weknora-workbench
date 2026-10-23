import { get, put } from '@/utils/request'

export interface WorkspaceNotification {
  id: number
  tenant_id: number
  audit_log_id: number
  actor_user_id: string
  actor_name?: string
  action: string
  target_user_id: string
  target_name?: string
  target_id: string
  details: Record<string, unknown> | null
  created_at: string
  read_at: string | null
}

export interface WorkspaceNotificationPage {
  success: boolean
  data: WorkspaceNotification[]
  next_cursor: number
  unread_count: number
}

export async function listWorkspaceNotifications(tenantId: number, afterId = 0, limit = 50): Promise<WorkspaceNotificationPage> {
  const params = new URLSearchParams({ limit: String(limit) })
  if (afterId > 0) params.set('after_id', String(afterId))
  return await get(`/api/v1/tenants/${tenantId}/notifications?${params}`) as unknown as WorkspaceNotificationPage
}

export async function markWorkspaceNotificationRead(tenantId: number, id: number): Promise<{ success: boolean }> {
  return await put(`/api/v1/tenants/${tenantId}/notifications/${id}/read`) as unknown as { success: boolean }
}
