import type { WorkspaceNotification, WorkspaceNotificationPage } from '../api/tenant/notifications'

export interface NotificationInboxState {
  rows: WorkspaceNotification[]
  unread: number
  cursor: number
  loading: boolean
  error: string
  pendingReads: number[]
}

export function notificationInboxState(): NotificationInboxState {
  return { rows: [], unread: 0, cursor: 0, loading: false, error: '', pendingReads: [] }
}

// Keep private inbox results tied to the identity that started each request.
// Reset also invalidates in-flight reads, not just the visible list.
export function createNotificationInbox(
  state: NotificationInboxState,
  loadPage: (tenantId: number, afterId?: number, limit?: number) => Promise<WorkspaceNotificationPage>,
  markRead: (tenantId: number, id: number) => Promise<{ success: boolean }>,
) {
  let generation = 0
  let readRevision = 0
  let loadRequest = 0
  function reset() {
    generation++
    readRevision = 0
    Object.assign(state, notificationInboxState())
  }
  async function load(tenantId: number, append = false, limit = 50, replacePending = false) {
    if (!tenantId || (state.loading && !replacePending) || (append && !state.cursor)) return
    const expected = generation
    const expectedReads = readRevision
    const expectedRequest = ++loadRequest
    state.loading = true
    state.error = ''
    try {
      const response = await loadPage(tenantId, append ? state.cursor : 0, limit)
      if (expected !== generation || expectedRequest !== loadRequest) return
      if (expectedReads !== readRevision) return
      if (!response.success) throw new Error('通知暂时无法加载')
      state.rows = append ? [...state.rows, ...response.data] : response.data
      state.cursor = response.next_cursor || 0
      state.unread = response.unread_count || 0
    } catch (error: any) {
      if (expected !== generation || expectedRequest !== loadRequest) return
      state.error = error?.status === 403 ? '当前空间已无通知访问权限' : '通知暂时无法加载，请重试'
      if (error?.status === 401 || error?.status === 403) {
        state.rows = []
        state.unread = 0
        state.cursor = 0
      }
    } finally {
      if (expected === generation && expectedRequest === loadRequest) state.loading = false
    }
  }
  async function read(tenantId: number, id: number) {
    const row = state.rows.find((item) => item.id === id)
    if (!row || row.read_at || state.pendingReads.includes(id)) return
    const expected = generation
    state.pendingReads.push(id)
    try {
      const response = await markRead(tenantId, id)
      if (expected !== generation) return
      if (!response.success) throw new Error('标记已读失败')
      readRevision++
      const current = state.rows.find((item) => item.id === id)
      if (!current?.read_at) {
        if (current) current.read_at = new Date().toISOString()
        state.unread = Math.max(0, state.unread - 1)
      }
    } catch (error: any) {
      if (expected !== generation) return
      state.error = '标记已读失败，请重试'
      if (error?.status === 401 || error?.status === 403) reset()
    } finally {
      if (expected === generation) state.pendingReads = state.pendingReads.filter((item) => item !== id)
    }
  }
  return { reset, load, read }
}
