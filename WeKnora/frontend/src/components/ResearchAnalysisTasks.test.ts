import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { computed, effectScope, h, nextTick, reactive, ref, watch } from 'vue'
import ts from 'typescript'
import { parse, compileTemplate } from '@vue/compiler-sfc'
const source = readFileSync(process.env.ANALYSIS_WIDGET_TEST_SOURCE || new URL('./ResearchAnalysisTasks.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(script + '\n;({ expanded, openTask, notifyCompleted, wake, rows, ignoreTask: typeof ignoreTask === "function" ? ignoreTask : undefined })', { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
function fixture() {
  const calls = { start: 0, stop: 0, refresh: 0, closed: 0, notices: [] as any[], opens: [] as any[], read: [] as string[], ignored: [] as string[] }
  const store = reactive({ jobs: [] as any[], scopeVersion: 0, activeCount: 0, unreadIds: [] as string[], pendingNotifications: [] as any[], visible: false,
    dismissingIds: [] as string[], ignoreFails: false,
    start: async () => { calls.start++ }, stop: () => { calls.stop++ }, refresh: async () => { calls.refresh++ }, markRead: (id: string) => { calls.read.push(id) },
    async dismiss(id: string) { calls.ignored.push(id); if (this.ignoreFails) return false; this.jobs = this.jobs.map(job => job.id === id ? { ...job, dismissed: true } : job); return true },
    takeNotifications() { const result = this.pendingNotifications.slice(); this.pendingNotifications = []; return result } })
  const mounted: Array<() => void> = [], cleanup: Array<() => void> = [], listeners = new Map<string, unknown>()
  const document = { visibilityState: 'visible', addEventListener: (name: string, callback: unknown) => listeners.set(name, callback), removeEventListener: (name: string) => listeners.delete(name) }
  const scope = effectScope(), view = scope.run(() => runInNewContext(code, { computed, h, ref, watch, useAnalysisTasksStore: () => store, useRouter: () => ({ push: async (target: unknown) => { calls.opens.push(target) } }),
    onMounted: (fn: () => void) => mounted.push(fn), onBeforeUnmount: (fn: () => void) => cleanup.push(fn), NotifyPlugin: { success: (notice: unknown) => { calls.notices.push(notice); return Promise.resolve({ close() {} }) }, close: () => { calls.closed++ } },
    analysisPhaseLabel: () => '', document, window: { addEventListener: document.addEventListener, removeEventListener: document.removeEventListener } })) as any
  mounted.forEach(fn => fn())
  return { store, calls, view, document, listeners, close() { cleanup.forEach(fn => fn()); scope.stop() } }
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
