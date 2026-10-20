import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { researchFramePath } from './researchFrameSession'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const updateBinding = source.match(/@update-workbench="([^"]+)"/)![1]!
// Vue unwraps setup refs in template assignments; preserve that behavior in the setup harness.
const updateEvent = updateBinding.includes('$event') ? updateBinding.replace('workbench =', 'workbench.value =') : `${updateBinding}($event)`
const code = ts.transpileModule(`${script}\n;const applyUpdateEvent = ($event) => { ${updateEvent} };({ workbench, workbenchError, canShowResearch, chatMounted, chatVisible, frame, isolatedView, frameUrl, generation, scopeGeneration, nativeDrawerOpen, frameIsHome, receive, applyUpdateEvent })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText

function fixture() {
  const auth = reactive({ token: 'token', selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'account-a' } })
  const route = reactive({ params: { workbenchId: 'workbench-a' }, query: {} })
  let header = 1
  const requests: Array<{ id: string; tenant: number; resolve: (data: unknown) => void }> = []
  const unmounts: Array<() => void> = []
  const scope = effectScope()
  const view: any = scope.run(() => runInNewContext(code, {
    computed, nextTick, ref, watch, useAuthStore: () => auth, useRoute: () => route, useRouter: () => ({ replace: async () => {} }),
    useTheme: () => ({ effectiveTheme: ref('light'), setTheme() {} }),
    onMounted() {}, onBeforeUnmount: (fn: () => void) => unmounts.push(fn),
    window: { location: { origin: 'https://test.local' }, removeEventListener() {} },
    authenticateResearchFrame: async () => true, researchFramePath,
    getResearchWorkbench: (id: string) => new Promise(resolve => requests.push({ id, tenant: header, resolve })),
  }))
  return { auth, route, view, requests, setHeader: (tenant: number) => { header = tenant }, close: () => { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('identity changes clear the old workspace synchronously and wait for its persisted tenant header', async () => {
  const f = fixture()
  try {
    assert.equal(f.requests.length, 0)
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy' } })
    await new Promise(setImmediate)
    assert.equal(f.view.canShowResearch.value, true)
    f.view.chatMounted.value = true
    f.view.chatVisible.value = true
    f.auth.selectedTenantId = 2
    assert.equal(f.view.canShowResearch.value, false)
    assert.equal(f.view.chatMounted.value, false)
    assert.equal(f.view.chatVisible.value, false)
    assert.equal(f.requests.length, 1)
    f.setHeader(2)
    await nextTick()
    assert.equal(f.requests[1]!.tenant, 2)
  } finally { f.close() }
})

test('a late workbench response cannot replace the active knowledge-base binding', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.route.params.workbenchId = 'workbench-b'
    await nextTick()
    f.requests[1]!.resolve({ success: true, data: { id: 'workbench-b', knowledge_base_id: 'kb-b', workspace_mode: 'legacy' } })
    await new Promise(setImmediate)
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy' } })
    await new Promise(setImmediate)
    assert.equal(f.view.workbench.value.knowledge_base_id, 'kb-b')
  } finally { f.close() }
})

test('research frame reauthentication keeps the open conversation and validates message origin', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy' } })
    await new Promise(setImmediate)
    const authMessages: unknown[] = []
    f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => authMessages.push(args) } }
    const message = { origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } }
    f.view.receive({ ...message, origin: 'https://untrusted.local' })
    f.view.receive({ ...message, source: {} })
    assert.equal(authMessages.length, 0)
    f.view.receive(message)
    assert.equal(authMessages.length, 0)
    const generation = f.view.generation.value
    const scopeGeneration = f.view.scopeGeneration.value
    f.view.chatMounted.value = true
    f.view.chatVisible.value = true
    f.view.receive({ ...message, data: { type: 'research-auth-expired' } })
    assert.equal(f.view.generation.value, generation + 1)
    assert.equal(f.view.scopeGeneration.value, scopeGeneration)
    assert.equal(f.view.chatVisible.value, true)
  } finally { f.close() }
})

test('leaving before the next tick cancels the scheduled workspace request', async () => {
  const f = fixture()
  f.close()
  await nextTick()
  assert.equal(f.requests.length, 0)
})

test('refreshing a token for the same identity leaves the conversation mounted', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.view.chatMounted.value = true
    f.view.chatVisible.value = true
    const scopeGeneration = f.view.scopeGeneration.value
    const frameGeneration = f.view.generation.value
    f.auth.token = 'refreshed-token'
    await nextTick()
    assert.equal(f.view.chatMounted.value, true)
    assert.equal(f.view.chatVisible.value, true)
    assert.equal(f.view.scopeGeneration.value, scopeGeneration)
    assert.equal(f.view.generation.value, frameGeneration + 1)
    assert.equal(f.requests.length, 1)
  } finally { f.close() }
})

test('an isolated workbench uses the original frame through its own scope and reauthenticates within it', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
    await new Promise(setImmediate)
    const messages: unknown[] = []
    f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => messages.push(args) } }
    const before = f.view.generation.value
    assert.equal(new URL(f.view.frameUrl.value, 'https://test.local').pathname, '/research/workbenches/workbench-a/')
    f.view.receive({ origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } })
    f.view.receive({ origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-auth-expired' } })
    assert.equal(messages.length, 0)
    assert.equal(f.view.generation.value, before + 1)
    assert.equal(new URL(f.view.frameUrl.value, 'https://test.local').pathname, '/research/workbenches/workbench-a/')
    assert.equal(f.view.canShowResearch.value, true)
  } finally { f.close() }
})

test('missing scope metadata cannot silently open the tenant-wide workbench', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a' } })
    await new Promise(setImmediate)
    assert.equal(f.view.canShowResearch.value, false)
    assert.ok(f.view.workbenchError.value)
  } finally { f.close() }
})

test('another workbench response cannot authorize the selected frame', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-b', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
    await new Promise(setImmediate)
    assert.equal(f.view.canShowResearch.value, false)
  } finally { f.close() }
})

test('source preparation messages require the active isolated frame and same origin', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
    await new Promise(setImmediate)
    let sources = 0, guides = 0
    f.view.isolatedView.value = { openSources() { sources++ }, openGuide() { guides++ } }
    f.view.frame.value = { contentWindow: { postMessage() {} } }
    const message = { origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-open-sources' } }
    f.view.receive({ ...message, origin: 'https://other.test' })
    f.view.receive({ ...message, source: {} })
    assert.equal(sources, 0)
    f.view.receive(message)
    f.view.receive({ ...message, data: { type: 'research-open-guide' } })
    assert.equal(sources, 1)
    assert.equal(guides, 0, 'The retired global guide message cannot reopen feature tours')
  } finally { f.close() }
})

test('onboarding update events cannot turn an isolated frame into legacy or replace its binding', async () => {
  for (const update of [
    { id: 'workbench-a', knowledge_base_id: 'kb-a' },
    { id: 'workbench-b', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' },
    { id: 'workbench-a', knowledge_base_id: 'kb-b', workspace_mode: 'isolated' },
    { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy' },
  ]) {
    const f = fixture()
    try {
      await nextTick()
      f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
      await new Promise(setImmediate)
      f.view.chatMounted.value = true
      f.view.chatVisible.value = true
      f.view.applyUpdateEvent({ ...update, onboarding: { status: 'dismissed' } })
      assert.equal(f.view.canShowResearch.value, false)
      assert.equal(f.view.chatMounted.value, false)
      assert.ok(f.view.workbenchError.value)
      const messages: unknown[] = []
      f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => messages.push(args) } }
      f.view.receive({ origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } })
      assert.equal(messages.length, 0)
    } finally { f.close() }
  }
})

test('a verified onboarding update keeps the original workbench scope', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
    await new Promise(setImmediate)
    f.view.applyUpdateEvent({ id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated', onboarding: { status: 'dismissed' } })
    assert.equal(f.view.workbench.value.onboarding.status, 'dismissed')
    assert.equal(f.view.canShowResearch.value, true)
    assert.equal(new URL(f.view.frameUrl.value, 'https://test.local').pathname, '/research/workbenches/workbench-a/')
  } finally { f.close() }
})

test('legacy frames authenticate with their workbench ID and can open the same source preparation', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy' } })
    await new Promise(setImmediate)
    assert.equal(new URL(f.view.frameUrl.value, 'https://test.local').pathname, '/research/workbenches/workbench-a/')
    let sources = 0
    const messages: any[] = []
    f.view.isolatedView.value = { openSources() { sources++ } }
    f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => messages.push(args) } }
    const event = { origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } }
    f.view.receive(event)
    assert.equal(messages.length, 0)
    f.view.receive({ ...event, data: { type: 'research-open-sources' } })
    assert.equal(sources, 1)
    f.view.applyUpdateEvent({ id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'legacy', name: 'Updated name' })
    assert.equal(f.view.canShowResearch.value, true)
    assert.equal(f.view.workbench.value.name, 'Updated name')
  } finally { f.close() }
})

test('an integration route without a workbench never opens tenant-wide research facts', async () => {
  const f = fixture()
  try {
    f.route.params.workbenchId = ''
    await nextTick()
    assert.equal(f.view.canShowResearch.value, false)
    assert.equal(f.view.frameUrl.value, '')
    const messages: unknown[] = []
    f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => messages.push(args) } }
    f.view.receive({ origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } })
    assert.deepEqual(messages, [])
  } finally { f.close() }
})


test('only the active scoped frame can suppress onboarding while its drawer is open', async () => {
  const f = fixture()
  try {
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a', workspace_mode: 'isolated' } })
    await new Promise(setImmediate)
    f.view.frame.value = { contentWindow: { postMessage() {} } }
    const event = { origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-drawer-state', open: true } }
    f.view.receive({...event, origin:'https://untrusted.local'})
    f.view.receive({...event, source:{}})
    assert.equal(f.view.nativeDrawerOpen.value,false)
    f.view.receive(event)
    assert.equal(f.view.nativeDrawerOpen.value,true)
    f.view.receive({...event, data:{type:'research-drawer-state',open:false}})
    assert.equal(f.view.nativeDrawerOpen.value,false)
  } finally { f.close() }
})
