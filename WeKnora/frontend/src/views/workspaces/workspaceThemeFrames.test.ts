import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'

const surfaces = [
  { name: 'knowledge structured', file: './KnowledgeStructuredData.vue', frame: 'frame', receiver: 'receive', generation: 'frameGeneration.value' },
  { name: 'legacy structured', file: '../structured/index.vue', frame: 'frame', receiver: 'receive', generation: 'generation.value' },
  { name: 'materials review', file: '../materials/MaterialsCenter.vue', frame: 'reviewFrame', receiver: 'receiveReview', generation: 'generation' },
  { name: 'research', file: '../research/index.vue', frame: 'frame', receiver: 'receive', generation: 'generation.value' },
] as const

async function fixture(surface: typeof surfaces[number]) {
  const text = readFileSync(new URL(surface.file, import.meta.url), 'utf8')
  const script = text.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
  const code = ts.transpileModule(`${script}\n;({ frame: ${surface.frame}, receive: ${surface.receiver}, get frameGeneration(){return ${surface.generation}}, ${surface.name === 'materials review' ? 'reviewOpen,' : ''} })`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
  const effectiveTheme = ref<'light' | 'dark'>('light'), requested: string[] = [], calls: string[] = [], messages: Array<{ payload: any; target: string }> = []
  const auth = reactive({ token: '', user: { id: 'test-user' }, selectedTenantId: 1, effectiveTenantId: 1, tenant: { id: 1 }, currentTenantRole: 'admin' })
  const route = reactive({ params: { kbId: 'test-kb' }, query: { source_knowledge_id: 'test-original' }, path: '/platform/knowledge-bases/test-kb/structured-data', fullPath: '/platform/knowledge-bases/test-kb/structured-data' })
  const unmounts: Array<() => void> = [], scope = effectScope()
  const view: any = scope.run(() => runInNewContext(code, {
    computed, nextTick, reactive, ref, watch, useAuthStore: () => auth, useRoute: () => route, useRouter: () => ({ push() {}, replace() {} }),
    useTheme: () => ({ effectiveTheme, setTheme(mode: 'light' | 'dark') { requested.push(mode); effectiveTheme.value = mode; return true } }),
    onMounted() {}, onBeforeUnmount: (fn: () => void) => unmounts.push(fn),
    useUploadConfirmStore: () => ({ rejectConfirm() {} }),
    getKnowledgeBaseById: async () => { calls.push('knowledge-base'); return { data: { id: 'test-kb', name: '测试库' } } },
    getDefaultKnowledgeBase: async () => { calls.push('default-base'); return { data: {} } },
    getResearchWorkbench: async () => { calls.push('workbench'); return { success: true, data: { id: 'test-workbench', knowledge_base_id: 'test-kb' } } },
    createMaterialClient: () => ({ async get() { calls.push('materials'); return {} } }),
    workspaceIdentity: (tenant: number, user: string) => `${tenant}:${user}`, workspaceErrorMessage: () => 'error',
    takeIntake: () => null, isTable: () => false, isStructuredFile: () => false, clearTimeout() {},
    window: { location: { origin: 'https://test.local' }, removeEventListener() {} },
  }))
  if (surface.name === 'materials review') view.reviewOpen.value = true
  view.frame.value = { contentWindow: { postMessage: (payload: unknown, target: string) => messages.push({ payload, target }) } }
  for (let i = 0; i < 4; i++) { await Promise.resolve(); await nextTick() }
  const event = (data: any) => ({ origin: 'https://test.local', source: view.frame.value.contentWindow, data })
  return { view, effectiveTheme, requested, calls, messages, event, close: () => { unmounts.forEach(fn => fn()); scope.stop() } }
}

for (const surface of surfaces) {
  test(`${surface.name}: a trusted theme-ready message receives only the effective theme`, async t => {
    const f = await fixture(surface); t.after(f.close)
    const ready = f.event({ type: 'workspace-theme-ready' })
    f.view.receive({ ...ready, origin: 'https://untrusted.example' }); f.view.receive({ ...ready, source: {} })
    assert.equal(f.messages.length, 0)
    f.view.receive(ready)
    assert.equal(f.messages.length, 1)
    assert.deepEqual({ ...f.messages[0]!.payload }, { type: 'workspace-theme', mode: 'light' })
    assert.equal(f.messages[0]!.target, 'https://test.local')
  })

  test(`${surface.name}: theme changes preserve the frame and do not reauthenticate or fetch data`, async t => {
    const f = await fixture(surface); t.after(f.close)
    const generation = f.view.frameGeneration, calls = [...f.calls]
    f.effectiveTheme.value = 'dark'; await nextTick()
    assert.equal(f.messages.length, 1)
    assert.deepEqual({ ...f.messages[0]!.payload }, { type: 'workspace-theme', mode: 'dark' })
    assert.equal(f.view.frameGeneration, generation)
    assert.deepEqual(f.calls, calls)
  })
}

test('research theme requests change the parent preference only from the registered frame and with a valid mode', async t => {
  const f = await fixture(surfaces[3]); t.after(f.close)
  const request = f.event({ type: 'workspace-theme-request', mode: 'dark' })
  f.view.receive({ ...request, origin: 'https://untrusted.example' }); f.view.receive({ ...request, source: {} })
  f.view.receive(f.event({ type: 'workspace-theme-request', mode: 'invalid' }))
  assert.deepEqual(f.requested, [])
  f.view.receive(request); await nextTick()
  assert.deepEqual(f.requested, ['dark'])
  assert.equal(f.messages[0]!.payload.type, 'workspace-theme')
})
