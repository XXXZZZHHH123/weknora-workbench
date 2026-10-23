import assert from 'node:assert/strict'
import test from 'node:test'
import { createNotificationInbox, notificationInboxState } from './workspaceNotifications'
import type { WorkspaceNotification, WorkspaceNotificationPage } from '../api/tenant/notifications'

const row = (id: number, tenantId = 7): WorkspaceNotification => ({
  id, tenant_id: tenantId, audit_log_id: id, actor_user_id: 'actor', action: 'rbac.member_added',
  target_user_id: 'member', target_id: '', details: {}, created_at: '2026-01-01T00:00:00Z', read_at: null,
})
const page = (rows: WorkspaceNotification[], cursor = 0, unread = rows.length): WorkspaceNotificationPage => ({
  success: true, data: rows, next_cursor: cursor, unread_count: unread,
})
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('an old workspace response cannot overwrite the next workspace inbox or unread badge', async () => {
  const first = deferred<WorkspaceNotificationPage>()
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, (tenantId) => tenantId === 7 ? first.promise : Promise.resolve(page([row(8, 8)])), async () => ({ success: true }))
  const oldRequest = inbox.load(7)
  inbox.reset()
  await inbox.load(8)
  first.resolve(page([row(7)], 7, 15))
  await oldRequest
  assert.deepEqual(state.rows.map((item) => item.tenant_id), [8])
  assert.equal(state.unread, 1)
  assert.equal(state.loading, false)
})

test('logging out clears the inbox and discards a delayed mark-read response', async () => {
  const response = deferred<{ success: boolean }>()
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, async () => page([row(1)]), () => response.promise)
  await inbox.load(7)
  const read = inbox.read(7, 1)
  inbox.reset()
  response.resolve({ success: true })
  await read
  assert.deepEqual(state, notificationInboxState())
})

test('a late mark-read from the previous workspace cannot decrement the current badge', async () => {
  const response = deferred<{ success: boolean }>()
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, async (tenantId) => page([row(tenantId, tenantId)]), () => response.promise)
  await inbox.load(7)
  const read = inbox.read(7, 7)
  inbox.reset()
  await inbox.load(8)
  response.resolve({ success: true })
  await read
  assert.equal(state.unread, 1)
  assert.equal(state.rows[0].read_at, null)
  assert.equal(state.rows[0].tenant_id, 8)
})

test('pagination keeps descending pages and marking read is idempotent', async () => {
  let reads = 0
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state,
    async (_tenantId, afterId) => afterId ? page([row(1)], 0, 2) : page([row(2)], 2, 2),
    async () => { reads++; return { success: true } },
  )
  await inbox.load(7)
  await inbox.load(7, true)
  assert.deepEqual(state.rows.map((item) => item.id), [2, 1])
  await inbox.read(7, 2)
  await inbox.read(7, 2)
  assert.equal(reads, 1)
  assert.equal(state.unread, 1)
  assert.ok(state.rows[0].read_at)
})

test('revoked access clears previously loaded results without retaining the old badge', async () => {
  const state = notificationInboxState()
  let revoked = false
  const inbox = createNotificationInbox(state, async () => {
    if (revoked) throw { status: 403 }
    return page([row(1)])
  }, async () => ({ success: true }))
  await inbox.load(7)
  revoked = true
  await inbox.load(7)
  assert.deepEqual(state.rows, [])
  assert.equal(state.unread, 0)
  assert.equal(state.cursor, 0)
  assert.match(state.error, /权限/)
})

test('an obsolete failed request cannot report an error inside the next identity', async () => {
  const old = deferred<WorkspaceNotificationPage>()
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, async () => old.promise, async () => ({ success: true }))
  const request = inbox.load(7)
  inbox.reset()
  old.reject({ status: 403 })
  await request
  assert.deepEqual(state, notificationInboxState())
})

test('a refresh started before a successful mark-read cannot resurrect the unread state', async () => {
  const refresh = deferred<WorkspaceNotificationPage>()
  let loads = 0
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, async () => ++loads === 1 ? page([row(1)]) : refresh.promise, async () => ({ success: true }))
  await inbox.load(7)
  const request = inbox.load(7)
  await inbox.read(7, 1)
  refresh.resolve(page([row(1)]))
  await request
  assert.equal(state.unread, 0)
  assert.ok(state.rows[0].read_at)
})

test('opening the drawer supersedes a pending badge-only poll and loads the full first page', async () => {
  const badge = deferred<WorkspaceNotificationPage>()
  const state = notificationInboxState()
  const inbox = createNotificationInbox(state, async (_tenantId, _after, limit) => limit === 1 ? badge.promise : page([row(2), row(1)]), async () => ({ success: true }))
  const poll = inbox.load(7, false, 1)
  await inbox.load(7, false, 50, true)
  badge.resolve(page([row(2)], 2, 2))
  await poll
  assert.deepEqual(state.rows.map((item) => item.id), [2, 1])
  assert.equal(state.cursor, 0)
})
