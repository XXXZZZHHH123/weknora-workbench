import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { ref } from 'vue'
import { lookupSidebarSession } from './sidebarSessionLookup'
import { resolveSessionOrigin, originGroupKey } from './sessionGrouping'
import { createEmptyBucket, prependSessionToWebBucket, removeSessionFromBuckets } from './sessionSidebarBuckets'

const source = readFileSync(new URL('./menu.vue', import.meta.url), 'utf8')
const start = source.indexOf('const ensureSessionInSidebar = async')
const end = source.indexOf('const rebuildBucketDefinitions', start)
assert.ok(start > 0 && end > start)
const code = ts.transpileModule(source.slice(start, end) + '\nglobalThis.ensure = ensureSessionInSidebar', { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText

function fixture(fetch: (id: string) => Promise<any>) {
  const buckets = ref({ web: createEmptyBucket({ key: 'web', apiSource: 'web', label: 'History', kind: 'web' }) })
  let identity = 'account-a', synced = 0
  const context: any = { bucketRequestToken: 1, sessionLookupIdentity: () => identity,
    sessionBuckets: buckets, menuArr: ref([{ path: 'creatChat', children: [] }]),
    pendingSessionLookups: new Map(), sessionHistoryEpochs: new Map(), lookupSidebarSession, getSession: fetch,
    sessionExistsInBuckets: (id: string) => buckets.value.web.items.some(row => row.id === id),
    menuChildToSessionRow: (row: any) => row, mapSessionRow: (row: any) => row,
    DEFAULT_SESSION_BUCKET_KEY: 'web', resolveSessionOrigin, originGroupKey, prependSessionToWebBucket,
    syncMenuStoreFromBuckets: () => { synced++ },
  }
  runInNewContext(code, context)
  return { context, buckets, synced: () => synced, identity: (next: string) => { identity = next } }
}

test('the first accepted message retries a pending draft lookup and adds history only once', async () => {
  let finish!: (value: any) => void
  let calls = 0
  const f = fixture(async id => {
    if (++calls === 1) return new Promise(resolve => { finish = resolve })
    return { data: { id, has_user_message: true, title: 'Real persisted conversation' } }
  })
  const initial = f.context.ensure('conversation')
  const acknowledged = f.context.ensure('conversation', true)
  assert.equal(calls, 1)
  finish({ data: { id: 'conversation', has_user_message: false } })
  await Promise.all([initial, acknowledged])
  assert.equal(calls, 2)
  assert.equal(f.buckets.value.web.items.length, 1)
  assert.equal(f.buckets.value.web.items[0]!.id, 'conversation')
  await f.context.ensure('conversation', true)
  assert.equal(calls, 2)
  assert.equal(f.synced(), 1)
})

test('a response from a previous account cannot populate the current sidebar', async () => {
  let finish!: (value: any) => void
  const f = fixture(() => new Promise(resolve => { finish = resolve }))
  const pending = f.context.ensure('conversation')
  f.identity('account-b')
  finish({ data: { id: 'conversation', has_user_message: true } })
  await pending
  assert.equal(f.buckets.value.web.items.length, 0)
  assert.equal(f.synced(), 0)
})

test('clearing messages removes the conversation from history while retaining its open route', () => {
  const f = fixture(async () => ({}))
  f.buckets.value.web = prependSessionToWebBucket(f.buckets.value.web, { id: 'conversation' })
  const handlerStart = source.indexOf('const handleSessionMutation =')
  const handlerEnd = source.indexOf('onMounted(async', handlerStart)
  const handler = ts.transpileModule(source.slice(handlerStart, handlerEnd) + '\nglobalThis.mutate = handleSessionMutation', { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText
  let routeChanges = 0
  Object.assign(f.context, { sessionActivity: { dismiss() {} }, updateSessionInBuckets() {}, removeSessionFromBuckets,
    route: { params: { chatid: 'conversation' } }, router: { push() { routeChanges++ } } })
  runInNewContext(handler, f.context)
  f.context.mutate({ detail: { sessionId: 'conversation', messagesCleared: true } })
  assert.equal(f.buckets.value.web.items.length, 0)
  assert.equal(f.buckets.value.web.total, 0)
  assert.equal(routeChanges, 0)
})

test('a late response captured before clearing cannot reinsert a now-empty conversation', async () => {
  let finish!: (value: any) => void
  const f = fixture(() => new Promise(resolve => { finish = resolve }))
  const pending = f.context.ensure('conversation')
  const handlerStart = source.indexOf('const handleSessionMutation =')
  const handlerEnd = source.indexOf('onMounted(async', handlerStart)
  const handler = ts.transpileModule(source.slice(handlerStart, handlerEnd) + '\nglobalThis.mutate = handleSessionMutation', { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText
  Object.assign(f.context, { sessionActivity: { dismiss() {} }, updateSessionInBuckets() {}, removeSessionFromBuckets,
    route: { params: { chatid: 'conversation' } }, router: { push() {} } })
  runInNewContext(handler, f.context)
  f.context.mutate({ detail: { sessionId: 'conversation', messagesCleared: true } })
  finish({ data: { id: 'conversation', has_user_message: true, title: 'Stale before clear' } })
  await pending
  assert.equal(f.buckets.value.web.items.length, 0)
  assert.equal(f.buckets.value.web.total, 0)
})
