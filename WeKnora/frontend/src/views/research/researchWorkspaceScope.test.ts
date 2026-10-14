import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(`${script}\n;({ workbench, canShowResearch, chatMounted, chatVisible, frame, generation, scopeGeneration, receive })`, {
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
    computed, nextTick, ref, watch, useAuthStore: () => auth, useRoute: () => route,
    onMounted() {}, onBeforeUnmount: (fn: () => void) => unmounts.push(fn),
    window: { location: { origin: 'https://test.local' }, removeEventListener() {} },
    getResearchWorkbench: (id: string) => new Promise(resolve => requests.push({ id, tenant: header, resolve })),
  }))
  return { auth, route, view, requests, setHeader: (tenant: number) => { header = tenant }, close: () => { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('identity changes clear the old workspace synchronously and wait for its persisted tenant header', async () => {
  const f = fixture()
  try {
    assert.equal(f.requests.length, 0)
    await nextTick()
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a' } })
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
    f.requests[1]!.resolve({ success: true, data: { id: 'workbench-b', knowledge_base_id: 'kb-b' } })
    await new Promise(setImmediate)
    f.requests[0]!.resolve({ success: true, data: { id: 'workbench-a', knowledge_base_id: 'kb-a' } })
    await new Promise(setImmediate)
    assert.equal(f.view.workbench.value.knowledge_base_id, 'kb-b')
  } finally { f.close() }
})

test('research frame reauthentication keeps the open conversation and validates message origin', async () => {
  const f = fixture()
  try {
    const authMessages: unknown[] = []
    f.view.frame.value = { contentWindow: { postMessage: (...args: unknown[]) => authMessages.push(args) } }
    const message = { origin: 'https://test.local', source: f.view.frame.value.contentWindow, data: { type: 'research-ready' } }
    f.view.receive({ ...message, origin: 'https://untrusted.local' })
    f.view.receive({ ...message, source: {} })
    assert.equal(authMessages.length, 0)
    f.view.receive(message)
    assert.equal(authMessages.length, 1)
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
