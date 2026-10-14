import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, reactive } from 'vue'

const source = readFileSync(process.env.KNOWLEDGE_HOOK_TEST_SOURCE || new URL('./useKnowledgeBase.ts', import.meta.url), 'utf8')
const actual = ts.transpileModule(source.slice(source.indexOf('export default function')).replace('export default function', 'function useKnowledgeBase'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText
const tick = () => new Promise(resolve => setImmediate(resolve))
function deferred() {
  let resolve!: (value: unknown) => void
  const promise = new Promise(value => { resolve = value })
  return { promise, resolve }
}
function library() {
  const auth = { user: { id: 'actor-a' }, effectiveTenantId: 10000 }
  const route = { params: { kbId: 'kb-a' } }
  const state = { cardList: ref([]), total: ref(0) }
  const metadata: Array<ReturnType<typeof deferred>> = [], chunks: Array<ReturnType<typeof deferred>> = []
  const watches: Array<() => void> = [], unmount: Array<() => void> = []
  const hook = vm.runInNewContext(actual + '\nuseKnowledgeBase("kb-a")', {
    ref, reactive, watch: (_source: unknown, callback: () => void) => watches.push(callback), onBeforeUnmount: (callback: () => void) => unmount.push(callback),
    useRoute: () => route, useAuthStore: () => auth, useI18n: () => ({ t: (key: string) => key }),
    knowledgeStore: () => state, storeToRefs: (value: unknown) => value, formatStringDate: String,
    getKnowledgeDetails: () => { const result = deferred(); metadata.push(result); return result.promise },
    getKnowledgeDetailsCon: () => { const result = deferred(); chunks.push(result); return result.promise },
    listKnowledgeFiles: async () => ({ data: [], total: 0 }),
    console: { error: () => {} },
  })
  return { hook, auth, route, state, metadata, chunks, watches, unmount }
}
function detail(id = 'file-a', kb = 'kb-a') {
  return { success: true, data: { id, knowledge_base_id: kb, file_name: `${id}.xlsx`, updated_at: '2026-01-01', file_type: 'xlsx' } }
}

test('native document details validate metadata before requesting parsed chunks', async () => {
  const current = library()
  current.hook.getCardDetails({ id: 'file-a' })
  assert.equal(current.chunks.length, 0)
  current.metadata[0].resolve(detail())
  await tick()
  assert.equal(current.hook.details.id, 'file-a')
  assert.equal(current.chunks.length, 1)
  current.chunks[0].resolve({ success: true, data: [{ id: 'chunk-a', content: 'Document content' }], total: 1 })
  await tick()
  assert.equal(current.hook.details.md[0].id, 'chunk-a')
  assert.equal(current.hook.details.chunkLoading, false)
})

for (const invalid of [detail('other-file'), detail('file-a', 'kb-b'), { success: false, error: { message: 'no permission' } }]) {
  test('wrong-document, wrong-library and denied metadata cannot fetch or reveal chunks: ' + JSON.stringify(invalid), async () => {
    const current = library()
    current.hook.getCardDetails({ id: 'file-a' })
    current.metadata[0].resolve(invalid)
    await tick()
    assert.equal(current.hook.details.id, '')
    assert.equal(current.chunks.length, 0)
    assert.equal(current.hook.details.chunkLoading, false)
    assert.ok(current.hook.details.chunkLoadError)
  })
}

test('a late metadata response cannot overwrite the more recently opened file', async () => {
  const current = library()
  current.hook.getCardDetails({ id: 'file-a' })
  current.hook.getCardDetails({ id: 'file-b' })
  current.metadata[1].resolve(detail('file-b'))
  await tick()
  current.metadata[0].resolve(detail())
  await tick()
  assert.equal(current.hook.details.id, 'file-b')
  assert.equal(current.chunks.length, 1)
})

const changes: Record<string, (current: ReturnType<typeof library>) => void> = {
  actor: current => { current.auth.user.id = 'actor-b' },
  tenant: current => { current.auth.effectiveTenantId = 10001 },
  library: current => { current.route.params.kbId = 'kb-b' },
  unmount: current => { current.unmount.forEach(callback => callback()) },
}
for (const [name, change] of Object.entries(changes)) {
  test('metadata remains private after ' + name + ' changes', async () => {
    const current = library()
    current.hook.getCardDetails({ id: 'file-a' })
    change(current)
    current.metadata[0].resolve(detail())
    await tick()
    assert.equal(current.hook.details.id, '')
    assert.equal(current.chunks.length, 0)
  })
  test('late parsed chunks cannot update a drawer after ' + name + ' changes', async () => {
    const current = library()
    current.hook.getCardDetails({ id: 'file-a' })
    current.metadata[0].resolve(detail())
    await tick()
    assert.equal(current.chunks.length, 1)
    change(current)
    current.chunks[0].resolve({ success: true, data: [{ id: 'old-chunk' }], total: 1 })
    await tick()
    assert.equal(current.hook.details.md.length, 0)
  })
}

test('a scope switch clears cached document metadata and invalidates pagination', () => {
  const current = library()
  Object.assign(current.hook.details, { id: 'file-a', title: 'Old document', md: [{ id: 'old-chunk' }], tags: [{ id: 'old-tag' }] })
  current.watches.forEach(callback => callback())
  assert.equal(current.hook.details.id, '')
  assert.deepEqual(Array.from(current.hook.details.md), [])
  current.hook.getfDetails('file-a', 2)
  assert.equal(current.chunks.length, 0)
})
