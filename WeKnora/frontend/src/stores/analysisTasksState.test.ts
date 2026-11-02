import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope, ref } from 'vue'
import { createAnalysisTasks, analysisPhaseLabel } from './analysisTasksState'
import type { AnalysisJob, AnalysisTaskOwner } from '../api/researchAnalysisTasks'
const flush = () => new Promise(resolve => setImmediate(resolve))
const job = (state: AnalysisJob['state'] = 'running', id = 'job-a'): AnalysisJob => ({ id, workbench_id: 'wb-a', state, phase: state === 'running' ? 'analyzing' : state, title: '研究逻辑分析', created_at: '2026-10-29T00:00:00Z', updated_at: '2026-10-29T00:00:00Z', result_available: state === 'succeeded' })
function fixture(storage = new Map<string, string>()) {
  const owner = ref<AnalysisTaskOwner | null>({ userId: 'user-a', tenantId: '17', token: 'token-a' }), visible = ref(true)
  const calls: Array<{ owner: AnalysisTaskOwner; isCurrent: () => boolean; resolve: (value: AnalysisJob[]) => void; reject: (error: unknown) => void }> = []
  const dismissals: Array<{ owner: AnalysisTaskOwner; id: string; isCurrent: () => boolean; resolve: (value: AnalysisJob) => void; reject: (error: unknown) => void }> = []
  const scheduled = new Map<number, { callback: () => void; delay: number }>(); let handle = 0
  const scope = effectScope()
  const tasks = scope.run(() => createAnalysisTasks({ owner: () => owner.value, isVisible: () => visible.value,
    storage: { getItem: key => storage.get(key) || null, setItem: (key, value) => { storage.set(key, value) } },
    fetchJobs: (context, isCurrent) => new Promise((resolve, reject) => calls.push({ owner: context, isCurrent, resolve, reject })),
    dismissJob: (context, id, isCurrent) => new Promise((resolve, reject) => dismissals.push({ owner: context, id, isCurrent, resolve, reject })),
    timers: { setTimer: (callback, delay) => { scheduled.set(++handle, { callback, delay }); return handle }, clearTimer: value => { scheduled.delete(value as number) } },
  }))!
  return { tasks, owner, visible, calls, dismissals, scheduled, storage, close() { tasks.stop(); scope.stop() } }
}

test('global discovery restores a running task without client job refs, deduplicates reads and polls active/idle at real intervals', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); const another = f.tasks.refresh(); assert.equal(f.calls.length, 1)
    f.calls[0]!.resolve([job()]); await Promise.all([started, another]); assert.equal(f.tasks.activeCount.value, 1)
    assert.equal([...f.scheduled.values()][0]!.delay, 5000)
    const refreshed = f.tasks.refresh(); f.calls[1]!.resolve([]); await refreshed
    assert.equal([...f.scheduled.values()][0]!.delay, 30000)
    assert.equal(f.storage.size, 0, 'No original text, result or task payload is cached to restore execution')
  } finally { f.close() }
})
test('unmounting the observer never cancels server work and a later observer can discover completion', async () => {
  const f = fixture()
  try {
    const first = f.tasks.start(); f.tasks.stop(); assert.equal(f.calls[0]!.isCurrent(), false)
    f.calls[0]!.resolve([job('succeeded')]); await first; assert.equal(f.tasks.jobs.value.length, 0)
    const next = f.tasks.start(); f.calls[1]!.resolve([job('succeeded')]); await next
    assert.deepEqual(f.tasks.unreadIds.value, ['job-a']); assert.equal(f.tasks.pendingNotifications.value.length, 1)
  } finally { f.close() }
})
test('account/tenant changes clear progress synchronously and late responses cannot notify or replace the new scope', async () => {
  const f = fixture()
  try {
    const first = f.tasks.start(); f.owner.value = { userId: 'user-b', tenantId: '18', token: 'token-b' }
    assert.equal(f.tasks.jobs.value.length, 0); assert.equal(f.calls.length, 2)
    f.calls[1]!.resolve([job('running', 'job-b')]); await flush()
    f.calls[0]!.resolve([job('succeeded', 'job-a')]); await first
    assert.equal(f.tasks.jobs.value[0]!.id, 'job-b'); assert.deepEqual(f.tasks.pendingNotifications.value, [])
  } finally { f.close() }
})
test('logout/invalid tenants issue no polls, and hidden documents wait until visibility returns', async () => {
  const f = fixture()
  try {
    f.owner.value = null; await f.tasks.start(); assert.equal(f.calls.length, 0)
    f.owner.value = { userId: 'user-a', tenantId: 'invalid', token: 'token-a' }; await flush(); assert.equal(f.calls.length, 0)
    f.visible.value = false; f.owner.value = { userId: 'user-a', tenantId: '17', token: 'token-a' }; await f.tasks.refresh(); assert.equal(f.calls.length, 0)
    f.visible.value = true; const refreshed = f.tasks.refresh(); assert.equal(f.calls.length, 1)
    f.calls[0]!.resolve([job()]); await refreshed; f.owner.value = null; assert.equal(f.scheduled.size, 0); assert.equal(f.tasks.visible.value, false)
  } finally { f.close() }
})
test('permission failures stop polling until authentication changes rather than retrying forever', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.reject({ status: 403 }); await started
    assert.equal(f.scheduled.size, 0); await f.tasks.refresh(); assert.equal(f.calls.length, 1)
    f.owner.value = { ...f.owner.value! }; await flush(); assert.equal(f.calls.length, 1, 'Refreshing the same user metadata does not retry forbidden reads')
    f.owner.value = { ...f.owner.value!, token: 'new-token' }; assert.equal(f.calls.length, 2)
    f.calls[1]!.resolve([job()]); await flush(); assert.equal(f.tasks.activeCount.value, 1)
  } finally { f.close() }
})
test('successful completion notifies once and persistent read markers contain only opaque IDs', async () => {
  const f = fixture(), storage = f.storage
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job('succeeded')]); await started
    assert.equal(f.tasks.takeNotifications().length, 1); assert.equal(f.tasks.takeNotifications().length, 0)
    const refreshed = f.tasks.refresh(); f.calls[1]!.resolve([job('succeeded')]); await refreshed
    assert.equal(f.tasks.pendingNotifications.value.length, 0); assert.equal(f.tasks.unreadIds.value.length, 1)
    f.tasks.markRead('job-a'); assert.equal(f.tasks.unreadIds.value.length, 0)
    const saved = [...storage.values()][0]!; assert.deepEqual(JSON.parse(saved), { seen: ['job-a'], notified: ['job-a'] })
    assert.doesNotMatch(saved, /token|workbench|result|created_at/)
  } finally { f.close() }
  const restored = fixture(storage)
  try { const started = restored.tasks.start(); restored.calls[0]!.resolve([job('succeeded')]); await started; assert.equal(restored.tasks.visible.value, false); assert.equal(restored.tasks.pendingNotifications.value.length, 0) } finally { restored.close() }
})
test('failed and interrupted tasks stay readable without being announced as successful', async () => {
  const f = fixture()
  try { const started = f.tasks.start(); f.calls[0]!.resolve([job('failed'), job('interrupted', 'job-b')]); await started; assert.equal(f.tasks.activeCount.value, 0); assert.equal(f.tasks.unreadIds.value.length, 2); assert.equal(f.tasks.pendingNotifications.value.length, 0) } finally { f.close() }
  assert.equal(analysisPhaseLabel({ ...job(), phase: 'unknown' }), '后台运行中')
  assert.equal(analysisPhaseLabel({ ...job('failed'), phase: 'analyzing' }), '分析未完成', 'Terminal state takes precedence over a stale stage')
})
test('diagram jobs share discovery and report generation stages without being labeled as judgment analysis', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start()
    f.calls[0]!.resolve([{ ...job(), purpose: 'diagram', phase: 'generating' }, job('running', 'job-b')])
    await started
    assert.equal(f.tasks.activeCount.value, 2)
    assert.equal(analysisPhaseLabel(f.tasks.jobs.value[0]!), '生成图结构')
    assert.equal(analysisPhaseLabel({ ...job('failed'), purpose: 'diagram', phase: 'generating' }), '生成未完成')
    assert.equal(analysisPhaseLabel({ ...job(), purpose: 'diagram', phase: 'reading_source' }), '读取原件')
  } finally { f.close() }
})

test('ignore waits for the server then hides the notice while keeping the recoverable result', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job('succeeded')]); await started
    assert.equal(typeof f.tasks.dismiss, 'function')
    const ignored = f.tasks.dismiss('job-a')
    assert.equal(f.dismissals.length, 1); assert.equal(f.dismissals[0]!.owner.userId, 'user-a')
    assert.deepEqual(f.tasks.unreadIds.value, ['job-a'], 'No optimistic discard before acknowledgement')
    assert.deepEqual(f.tasks.dismissingIds.value, ['job-a'])
    f.dismissals[0]!.resolve({ ...job('succeeded'), dismissed: true }); assert.equal(await ignored, true)
    assert.deepEqual(f.tasks.unreadIds.value, []); assert.deepEqual(f.tasks.pendingNotifications.value, [])
    assert.equal(f.tasks.visible.value, false); assert.equal(f.tasks.jobs.value[0]!.result_available, true)
    assert.equal(f.tasks.jobs.value[0]!.dismissed, true)
    assert.deepEqual(JSON.parse([...f.storage.values()][0]!).dismissed, ['job-a'])
    assert.doesNotMatch([...f.storage.values()][0]!, /token|markdown|source|result/)
  } finally { f.close() }
})

test('active jobs cannot be ignored and failed ignore requests leave state recoverable', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job()]); await started
    assert.equal(typeof f.tasks.dismiss, 'function')
    assert.equal(await f.tasks.dismiss('job-a'), false); assert.equal(f.dismissals.length, 0)
    const refreshed = f.tasks.refresh(); f.calls[1]!.resolve([job('failed')]); await refreshed
    const ignored = f.tasks.dismiss('job-a'); f.dismissals[0]!.reject({ status: 503 }); assert.equal(await ignored, false)
    assert.deepEqual(f.tasks.unreadIds.value, ['job-a']); assert.notEqual(f.tasks.jobs.value[0]!.dismissed, true)
    assert.deepEqual(f.tasks.dismissingIds.value, []); assert.ok(f.tasks.error.value)
  } finally { f.close() }
})

test('an old poll cannot revive an ignored result and server markers survive a new browser', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job('succeeded')]); await started
    assert.equal(typeof f.tasks.dismiss, 'function')
    const oldPoll = f.tasks.refresh(), ignored = f.tasks.dismiss('job-a')
    f.dismissals[0]!.resolve({ ...job('succeeded'), dismissed: true }); await ignored
    f.calls[1]!.resolve([job('running')]); await oldPoll
    assert.equal(f.tasks.jobs.value[0]!.dismissed, true); assert.equal(f.tasks.visible.value, false)
    assert.equal(f.tasks.activeCount.value, 0, 'An old running response cannot undo a terminal acknowledgement')
    assert.equal(f.tasks.takeNotifications().length, 0)
  } finally { f.close() }
  const next = fixture()
  try {
    const started = next.tasks.start(); next.calls[0]!.resolve([{ ...job('succeeded'), dismissed: true }]); await started
    assert.equal(next.tasks.visible.value, false); assert.equal(next.tasks.jobs.value[0]!.result_available, true)
    assert.deepEqual(next.tasks.unreadIds.value, [])
  } finally { next.close() }
})

test('a dismiss acknowledgement from another account never changes the new account', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job('succeeded')]); await started
    assert.equal(typeof f.tasks.dismiss, 'function')
    const ignored = f.tasks.dismiss('job-a')
    f.owner.value = { userId: 'user-b', tenantId: '18', token: 'token-b' }
    assert.equal(f.dismissals[0]!.isCurrent(), false)
    f.calls[1]!.resolve([job('running', 'job-b')]); await flush()
    f.dismissals[0]!.resolve({ ...job('succeeded'), dismissed: true }); assert.equal(await ignored, false)
    assert.equal(f.tasks.jobs.value[0]!.id, 'job-b'); assert.equal(f.tasks.activeCount.value, 1)
    assert.equal(f.storage.size, 0)
  } finally { f.close() }
})

test('unmounting during ignore cannot leave the next observer permanently busy', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start(); f.calls[0]!.resolve([job('succeeded')]); await started
    const previous = f.tasks.dismiss('job-a'); f.tasks.stop()
    assert.deepEqual(f.tasks.dismissingIds.value, [])
    f.dismissals[0]!.reject({ status: 503 }); assert.equal(await previous, false)
    const resumed = f.tasks.start(); f.calls[1]!.resolve([job('succeeded')]); await resumed
    const retried = f.tasks.dismiss('job-a'); assert.equal(f.dismissals.length, 2)
    f.dismissals[1]!.resolve({ ...job('succeeded'), dismissed: true }); assert.equal(await retried, true)
  } finally { f.close() }
})

test('only the latest available result per workbench and purpose is announced without losing active work', async () => {
  const f = fixture()
  try {
    const started = f.tasks.start()
    f.calls[0]!.resolve([
      { ...job('succeeded', 'new'), created_at: '2026-10-30T00:00:00Z' },
      job('succeeded', 'old'), job('running', 'active'),
      { ...job('succeeded', 'other-workbench'), workbench_id: 'wb-b' },
      { ...job('succeeded', 'diagram'), purpose: 'diagram' },
    ])
    await started
    assert.deepEqual(f.tasks.jobs.value.map(row => row.id), ['new', 'active', 'other-workbench', 'diagram'])
    assert.equal(f.tasks.activeCount.value, 1)
    assert.deepEqual(f.tasks.takeNotifications().map(row => row.id), ['new', 'other-workbench', 'diagram'])
    const refreshed = f.tasks.refresh()
    f.calls[1]!.resolve([job('failed', 'latest-failed'), job('succeeded', 'last-good')]); await refreshed
    assert.equal(f.tasks.jobs.value.some(row => row.id === 'last-good'), true, 'A failed attempt cannot destroy the last recoverable result')
  } finally { f.close() }
})
