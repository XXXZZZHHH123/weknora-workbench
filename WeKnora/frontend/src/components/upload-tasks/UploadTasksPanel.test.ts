import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { computed, ref } from 'vue'
import { itemPhase, needsParsePolling, summarizeItems, type UploadItem } from '../../stores/uploadTasksState.ts'

const component = readFileSync(new URL('./UploadTasksPanel.vue', import.meta.url), 'utf8')
function actualCode(from: string, to: string) {
  const start = component.indexOf(from), end = component.indexOf(to, start)
  assert.ok(start >= 0 && end > start)
  return ts.transpileModule(component.slice(start, end), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
}
const panelCode = actualCode('const issueCount =', 'watch(issueCount,')
  + actualCode('const headline =', 'const subline =')
  + actualCode('const badge =', '// ---- progress bar')
  + actualCode('const legend =', 'const hint =')

let nextId = 0
function file(parseStatus: string, transfer: UploadItem['transfer'] = 'uploaded'): UploadItem {
  return { id: String(nextId++), batchId: 'batch', name: 'fixture.xlsx', relativePath: '',
    size: 20, loaded: 20, knowledgeId: 'fixture-source', transfer, parseStatus }
}

function panel(initial: UploadItem[]) {
  const items = ref(initial)
  const summary = computed(() => summarizeItems(items.value))
  const display = vm.runInNewContext(panelCode + '\n({ legend, headline, badge })', {
    items, summary, computed, itemPhase,
    t: (key: string, params?: unknown) => ({ key, ...params as Record<string, unknown> }),
  })
  return { items, summary, legend: () => JSON.parse(JSON.stringify(display.legend.value)),
    headline: () => JSON.parse(JSON.stringify(display.headline.value)),
    badge: () => JSON.parse(JSON.stringify(display.badge.value)) }
}

test('a saved original is complete without claiming it is searchable', () => {
  const original = file('stored')
  const display = panel([original])
  assert.deepEqual(display.legend(), [
    { key: 'stored', label: { key: 'uploadTasks.legend.stored' }, count: 1 },
  ])
  assert.deepEqual(display.headline(), { key: 'uploadTasks.titleDone' })
  assert.equal(display.summary.value.ready, 1)
  assert.equal(display.summary.value.stage, 'done')
  assert.equal(display.summary.value.bar.ready, 1)
  assert.deepEqual(display.badge(), { tone: 'success', icon: 'check', ring: null })
  assert.equal(needsParsePolling(original), false)
})

test('mixed results count originals and searchable documents separately', () => {
  const display = panel([file('stored'), file('completed'), file('finalizing'), file('processing'),
    file('stored', 'duplicate'), file('stored', 'failed'), file('cancelled')])
  assert.deepEqual(display.legend().map((entry: any) => [entry.key, entry.count]),
    [['stored', 1], ['ready', 2], ['active', 1], ['failed', 1], ['duplicate', 1]])
  assert.equal(display.summary.value.ready, 3)
  assert.equal(display.summary.value.parsing, 1)
})

test('the parsing headline excludes saved originals from its numerator and denominator', () => {
  const display = panel([file('stored'), file('processing')])
  assert.deepEqual(display.headline(), { key: 'uploadTasks.titleParsing', done: 0, total: 1 })
  assert.deepEqual(display.badge(), { tone: 'brand', icon: 'file-search', ring: 0 })
  display.items.value[1].parseStatus = 'completed'
  assert.deepEqual(display.headline(), { key: 'uploadTasks.titleDone' })
  assert.deepEqual(display.legend().map((entry: any) => [entry.key, entry.count]),
    [['stored', 1], ['ready', 1]])
})

test('the legend updates when an upload finishes saving its original', () => {
  const display = panel([file('pending')])
  assert.deepEqual(display.legend().map((entry: any) => [entry.key, entry.count]), [['active', 1]])
  display.items.value[0].parseStatus = 'stored'
  assert.deepEqual(display.legend().map((entry: any) => [entry.key, entry.count]), [['stored', 1]])
  assert.equal(display.summary.value.stage, 'done')
})

test('unsuccessful transfers do not claim a saved original and document-only counts remain unchanged', () => {
  const unsuccessful = panel([file('stored', 'queued'), file('stored', 'failed'), file('stored', 'duplicate')])
  assert.deepEqual(unsuccessful.legend().map((entry: any) => [entry.key, entry.count]),
    [['waiting', 1], ['failed', 1], ['duplicate', 1]])
  const documents = panel([file('completed'), file('finalizing'), file('processing')])
  assert.deepEqual(documents.legend().map((entry: any) => [entry.key, entry.count]),
    [['ready', 2], ['active', 1]])
  assert.deepEqual(documents.headline(), { key: 'uploadTasks.titleParsing', done: 2, total: 3 })
  assert.equal(documents.badge().ring, 2 / 3)
})
