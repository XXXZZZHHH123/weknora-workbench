import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { computed, effectScope, h, nextTick, reactive, ref, watch } from 'vue'
import ts from 'typescript'
import { parse, compileTemplate } from '@vue/compiler-sfc'
const source = readFileSync(process.env.ANALYSIS_WIDGET_TEST_SOURCE || new URL('./ResearchAnalysisTasks.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(script + '\n;({ expanded, label, openTask, notifyCompleted, wake, rows, notificationCount: () => notifications.size, ignoreTask: typeof ignoreTask === "function" ? ignoreTask : undefined, ignoreAll: typeof ignoreAll === "function" ? ignoreAll : undefined, canIgnoreAll: typeof canIgnoreAll !== "undefined" ? canIgnoreAll : undefined })', { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
function fixture() {
  const calls = { start: 0, stop: 0, refresh: 0, closed: 0, bulk: 0, notices: [] as any[], handles: [] as any[], closedHandles: [] as any[], opens: [] as any[], read: [] as string[], ignored: [] as string[] }
  const bulkReplies: Array<(ids: string[] | null) => void> = []
  const store = reactive({ jobs: [] as any[], scopeVersion: 0, activeCount: 0, unreadIds: [] as string[], pendingNotifications: [] as any[], visible: false,
    dismissingIds: [] as string[], dismissingAll: false, ignoreFails: false,
    start: async () => { calls.start++ }, stop: () => { calls.stop++ }, refresh: async () => { calls.refresh++ }, markRead: (id: string) => { calls.read.push(id) },
    async dismiss(id: string) { calls.ignored.push(id); if (this.ignoreFails) return false; this.jobs = this.jobs.map(job => job.id === id ? { ...job, dismissed: true } : job); return true },
    async dismissAll() { calls.bulk++; this.dismissingAll = true; const scope = this.scopeVersion
      const ids = await new Promise<string[] | null>(resolve => bulkReplies.push(resolve))
      if (scope === this.scopeVersion) { this.dismissingAll = false; if (ids) this.jobs = this.jobs.map(job => ids.includes(job.id) && !['queued', 'running'].includes(job.state) ? { ...job, dismissed: true } : job) }
      return ids },
    takeNotifications() { const result = this.pendingNotifications.slice(); this.pendingNotifications = []; return result } })
  const mounted: Array<() => void> = [], cleanup: Array<() => void> = [], listeners = new Map<string, unknown>()
  const document = { visibilityState: 'visible', addEventListener: (name: string, callback: unknown) => listeners.set(name, callback), removeEventListener: (name: string) => listeners.delete(name) }
  const scope = effectScope(), view = scope.run(() => runInNewContext(code, { computed, h, ref, watch, useAnalysisTasksStore: () => store, useRouter: () => ({ push: async (target: unknown) => { calls.opens.push(target) } }),
    onMounted: (fn: () => void) => mounted.push(fn), onBeforeUnmount: (fn: () => void) => cleanup.push(fn), NotifyPlugin: { success: (notice: unknown) => { calls.notices.push(notice); const handle = Promise.resolve({ close() {} }); calls.handles.push(handle); return handle }, close: (handle: unknown) => { calls.closed++; calls.closedHandles.push(handle) } },
    analysisPhaseLabel: () => '', document, window: { addEventListener: document.addEventListener, removeEventListener: document.removeEventListener } })) as any
  mounted.forEach(fn => fn())
  return { store, calls, view, document, listeners, bulkReplies, close() { cleanup.forEach(fn => fn()); scope.stop() } }
}

test('global analysis widget compiles as a real Vue template and lives outside the per-page route', () => {
  const parsed = parse(source).descriptor, compiled = compileTemplate({ id: 'analysis-tasks', source: parsed.template!.content, filename: 'ResearchAnalysisTasks.vue' })
  assert.deepEqual(compiled.errors, [])
  const host = readFileSync(new URL('../views/platform/index.vue', import.meta.url), 'utf8')
  assert.match(host, /<div class="platform-task-dock"><ResearchAnalysisTasks\s*\/><UploadTasksPanel\s*\/><\/div>/)
  assert.doesNotMatch(parsed.template!.content, /percentage|aria-valuenow|ProgressRing/)
})
test('completion notifies once with an explicit result link and never invokes analysis creation or saving', async () => {
  const f = fixture()
  try {
    assert.equal(f.calls.start, 1); const job = { id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', result_available: true }
    f.store.jobs.push(job); f.store.pendingNotifications.push(job); await nextTick(); assert.equal(f.calls.notices.length, 1)
    f.view.notifyCompleted(); assert.equal(f.calls.notices.length, 1)
    const button = f.calls.notices[0].content().children[1]; button.props.onClick(); assert.equal(f.calls.opens[0].params.workbenchId, 'wb-a'); assert.equal(f.calls.opens[0].query.analysis_job, 'job-a'); assert.equal(f.calls.opens[0].query.view, 'judgment'); assert.deepEqual(f.calls.read, ['job-a'])
  } finally { f.close() }
  assert.equal(f.calls.stop, 1); assert.equal(f.listeners.size, 0)
})

test('identity changes close only this widget notice and stale result actions cannot open the old workspace', async () => {
  const f = fixture()
  try {
    const job = { id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', result_available: true }
    f.store.jobs.push(job); f.store.pendingNotifications.push(job); await nextTick()
    const oldAction = f.calls.notices[0].content().children[1].props.onClick
    f.store.jobs = []; f.store.scopeVersion++; await nextTick()
    assert.equal(f.calls.closed, 1); oldAction(); assert.equal(f.calls.opens.length, 0)
  } finally { f.close() }
})
test('hidden documents defer completion notices and focus resumes authoritative status reads', () => {
  const f = fixture()
  try {
    f.document.visibilityState = 'hidden'; f.store.pendingNotifications.push({ id: 'job-a', workbench_id: 'wb-a' }); f.view.notifyCompleted(); assert.equal(f.calls.notices.length, 0)
    f.document.visibilityState = 'visible'; f.view.wake(); assert.equal(f.calls.refresh, 1); assert.equal(f.calls.notices.length, 1)
  } finally { f.close() }
})

test('a diagram completion opens the diagram view rather than the judgment manager', async () => {
  const f = fixture()
  try {
    const job = { id: 'diagram-job', workbench_id: 'wb-a', purpose: 'diagram', state: 'succeeded', result_available: true }
    f.store.jobs.push(job); f.store.pendingNotifications.push(job); await nextTick()
    assert.equal(f.calls.notices.length, 1)
    f.calls.notices[0].content().children[1].props.onClick()
    assert.equal(f.calls.opens[0].query.view, 'diagram')
    assert.equal(f.calls.opens[0].query.analysis_job, 'diagram-job')
  } finally { f.close() }
})

test('finished task prompts offer ignore without opening or saving a result', async () => {
  const f = fixture()
  try {
    const job = { id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', result_available: true }
    f.store.jobs.push(job); f.store.pendingNotifications.push(job); await nextTick()
    const buttons = f.calls.notices[0].content().children.filter((node: any) => node.type === 'button')
    assert.equal(buttons.length, 2)
    assert.equal(buttons[1].children, '忽略')
    await buttons[1].props.onClick()
    assert.deepEqual(f.calls.ignored, ['job-a']); assert.equal(f.calls.opens.length, 0)
    assert.equal(f.view.rows.value.length, 0)
    assert.equal(f.store.jobs[0].result_available, true, 'Ignoring a prompt does not discard the retained result')
  } finally { f.close() }
})

test('task list hides ignored results and rejects ignore actions for active or stale jobs', async () => {
  const f = fixture()
  try {
    assert.equal(typeof f.view.ignoreTask, 'function')
    const running = { id: 'active', workbench_id: 'wb-a', state: 'running' }
    f.store.jobs.push(running, { id: 'done', workbench_id: 'wb-a', state: 'succeeded', dismissed: true })
    assert.equal(f.view.rows.value.length, 1)
    await f.view.ignoreTask(running); assert.equal(f.calls.ignored.length, 0)
    await f.view.ignoreTask({ id: 'old', workbench_id: 'wb-a', state: 'succeeded' }); assert.equal(f.calls.ignored.length, 0)
    f.view.openTask({ id: 'done', workbench_id: 'wb-a' }); assert.equal(f.calls.opens.length, 0)
  } finally { f.close() }
})

test('ignore failure leaves the task and completion prompt available', async () => {
  const f = fixture()
  try {
    assert.equal(typeof f.view.ignoreTask, 'function')
    const job = { id: 'done', workbench_id: 'wb-a', state: 'failed' }
    f.store.jobs.push(job); f.store.ignoreFails = true
    await f.view.ignoreTask(job)
    assert.equal(f.view.rows.value.length, 1); assert.equal(f.calls.closed, 0)
  } finally { f.close() }
})

test('a completion batch creates an independent notice for every task and ignore closes only its own notice', async () => {
  const f = fixture()
  try {
    const first = {id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', result_available: true}
    const second = {id: 'job-b', workbench_id: 'wb-b', purpose: 'diagram', state: 'succeeded', result_available: true}
    f.store.jobs.push(first, second); f.store.pendingNotifications.push(first, second); await nextTick()
    assert.equal(f.calls.notices.length, 2, 'Every consumed completion must have a corresponding notice')
    assert.equal(f.calls.closed, 0, 'The second completion must not replace the first notice')
    assert.equal(f.calls.notices[0].title, '研究分析已完成')
    assert.equal(f.calls.notices[1].title, '产业图配置已生成')
    await f.calls.notices[0].content().children[2].props.onClick()
    assert.deepEqual(f.calls.ignored, ['job-a'])
    assert.equal(f.calls.closed, 1)
    assert.equal(f.calls.closedHandles[0], f.calls.handles[0])
    assert.deepEqual(Array.from(f.view.rows.value, (row: any) => row.id), ['job-b'])
    f.calls.notices[1].content().children[1].props.onClick()
    assert.equal(f.calls.opens[0].query.analysis_job, 'job-b')
  } finally { f.close() }
})

test('failure to ignore one completion leaves both notices and task records intact', async () => {
  const f = fixture()
  try {
    const first = {id: 'job-a', workbench_id: 'wb-a', state: 'succeeded'}
    const second = {id: 'job-b', workbench_id: 'wb-b', state: 'succeeded'}
    f.store.jobs.push(first, second); f.store.pendingNotifications.push(first, second); await nextTick()
    assert.equal(f.calls.notices.length, 2)
    f.store.ignoreFails = true
    await f.calls.notices[0].content().children[2].props.onClick()
    assert.equal(f.calls.closed, 0)
    assert.equal(f.view.rows.value.length, 2)
    assert.deepEqual(f.calls.ignored, ['job-a'])
    assert.equal(f.view.expanded.value, true)
  } finally { f.close() }
  assert.equal(f.calls.closed, 2, 'Unmount closes every remaining notice without dismissing either task')
})

test('read task records have a nonempty entry label and account changes close all owned notices', async () => {
  const f = fixture()
  try {
    const first = {id: 'job-a', workbench_id: 'wb-a', state: 'succeeded'}
    const second = {id: 'job-b', workbench_id: 'wb-b', state: 'succeeded'}
    f.store.jobs.push(first, second)
    assert.equal(f.view.label.value, '研究分析：2 项记录')
    f.store.pendingNotifications.push(first, second); await nextTick()
    f.store.scopeVersion++; await nextTick()
    assert.equal(f.calls.closed, 2)
    assert.deepEqual(f.calls.ignored, [], 'Closing notices on account change is not task dismissal')
  } finally { f.close() }
})

for (const callback of ['onCloseBtnClick', 'onDurationEnd']) test(`${callback} releases only that toast reference without ignoring the task`, async () => {
  const f = fixture()
  try {
    const first = {id: 'job-a', workbench_id: 'wb-a', state: 'succeeded'}
    const second = {id: 'job-b', workbench_id: 'wb-b', state: 'succeeded'}
    f.store.jobs.push(first, second); f.store.pendingNotifications.push(first, second); await nextTick()
    assert.equal(f.view.notificationCount(), 2)
    assert.equal(typeof f.calls.notices[0][callback], 'function')
    f.calls.notices[0][callback]()
    assert.equal(f.view.notificationCount(), 1)
    assert.deepEqual(f.calls.ignored, [])
    assert.equal(f.view.rows.value.length, 2)
    f.store.scopeVersion++; await nextTick()
    assert.equal(f.calls.closed, 1, 'Account cleanup does not retain a toast that already closed')
    assert.equal(f.calls.closedHandles[0], f.calls.handles[1])
  } finally { f.close() }
})

test('bulk ignore only enables for ended jobs and waits for acknowledgement before closing matching notices', async () => {
  const f = fixture()
  try {
    assert.equal(typeof f.view.ignoreAll, 'function')
    const active = { id: 'active', workbench_id: 'wb-a', state: 'running' }
    f.store.jobs.push(active); assert.equal(f.view.canIgnoreAll.value, false)
    const a = { id: 'a', workbench_id: 'wb-a', state: 'succeeded' }, b = { id: 'b', workbench_id: 'wb-b', state: 'succeeded' }
    f.store.jobs.push(a, b); f.store.pendingNotifications.push(a, b); await nextTick()
    assert.equal(f.view.canIgnoreAll.value, true)
    const pending = f.view.ignoreAll(); assert.equal(f.calls.bulk, 1); assert.equal(f.view.canIgnoreAll.value, false); assert.equal(f.calls.closed, 0)
    assert.equal(f.view.rows.value.length, 3)
    const newer = { id: 'newer', workbench_id: 'wb-c', state: 'succeeded' }; f.store.jobs.push(newer)
    f.bulkReplies[0]!(['a', 'off-page']); await pending
    assert.equal(f.calls.closed, 1); assert.equal(f.calls.closedHandles[0], f.calls.handles[0])
    assert.deepEqual(Array.from(f.view.rows.value, (row: any) => row.id), ['active', 'b', 'newer'])
    assert.equal(f.calls.opens.length, 0); assert.equal(f.calls.read.length, 0); assert.deepEqual(f.calls.ignored, [])
  } finally { f.close() }
})

test('bulk ignore failure retains individual prompts and opens the list for retry', async () => {
  const f = fixture()
  try {
    assert.equal(typeof f.view.ignoreAll, 'function')
    const job = { id: 'a', workbench_id: 'wb-a', state: 'succeeded' }; f.store.jobs.push(job); f.store.pendingNotifications.push(job); await nextTick()
    const ignored = f.view.ignoreAll(); f.bulkReplies[0]!(null); await ignored
    assert.equal(f.view.rows.value.length, 1); assert.equal(f.calls.closed, 0); assert.equal(f.view.expanded.value, true)
    assert.equal(f.view.canIgnoreAll.value, true)
    const retry = f.view.ignoreAll(); f.bulkReplies[1]!(['a']); await retry; assert.equal(f.view.rows.value.length, 0)
  } finally { f.close() }
})

test('an old bulk response cannot close a new account notice with a matching task ID', async () => {
  const f = fixture()
  try {
    assert.equal(typeof f.view.ignoreAll, 'function')
    const previous = { id: 'shared-fixture-id', workbench_id: 'old-wb', state: 'succeeded' }
    f.store.jobs.push(previous); f.store.pendingNotifications.push(previous); await nextTick()
    const pending = f.view.ignoreAll(); f.store.scopeVersion++; f.store.jobs = []; f.store.dismissingAll = false; await nextTick()
    const current = { id: previous.id, workbench_id: 'new-wb', state: 'succeeded' }
    f.store.jobs.push(current); f.store.pendingNotifications.push(current); await nextTick()
    assert.equal(f.calls.closed, 1); assert.equal(f.view.notificationCount(), 1)
    f.bulkReplies[0]!([previous.id]); await pending
    assert.equal(f.calls.closed, 1); assert.equal(f.view.notificationCount(), 1)
    assert.equal(f.view.rows.value[0].workbench_id, 'new-wb')
  } finally { f.close() }
})
