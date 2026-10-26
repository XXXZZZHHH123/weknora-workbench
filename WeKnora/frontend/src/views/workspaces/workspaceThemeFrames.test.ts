import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { isTrustedStructuredFrameMessage, structuredFrameAuthPayload } from '../../utils/structuredFrameHost'
import { computed, defineAsyncComponent, effectScope, h, nextTick, reactive, ref, shallowRef, watch } from 'vue'

const surfaces = [
  { name: 'knowledge structured', file: '../../components/KnowledgeStructuredFrame.vue', frame: 'frame', receiver: 'receive', generation: 'frameGeneration.value' },
  { name: 'legacy structured', file: '../structured/index.vue', frame: 'frame', receiver: 'receive', generation: 'generation.value' },
  { name: 'materials review', file: '../materials/MaterialsCenter.vue', frame: 'reviewFrame', receiver: 'receiveReview', generation: 'generation' },
  { name: 'research', file: '../research/index.vue', frame: 'frame', receiver: 'receive', generation: 'generation.value' },
] as const

async function fixture(surface: typeof surfaces[number], initialView = '') {
  const text = readFileSync(new URL(surface.file, import.meta.url), 'utf8')
  const script = text.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
  const code = ts.transpileModule(`${script}\n;({ frame: ${surface.frame}, receive: ${surface.receiver}, get frameGeneration(){return ${surface.generation}}, ${surface.name === 'materials review' ? 'reviewOpen,' : ''} ${surface.name === 'knowledge structured' ? 'src, modelSettingsRequested,' : ''} })`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  }).outputText
  const effectiveTheme = ref<'light' | 'dark'>('light'), requested: string[] = [], calls: string[] = [], messages: Array<{ payload: any; target: string }> = []
  const events: CustomEvent[] = [], emitted: Array<{ name: string; args: any[] }> = []
  const props = reactive({ knowledgeBaseId: 'test-kb', sourceKnowledgeId: 'test-original', sourceFileType: 'pdf', mode: 'import', view: initialView })
  const auth = reactive({ token: '', user: { id: 'test-user' }, selectedTenantId: 1, effectiveTenantId: 1, tenant: { id: 1 }, currentTenantRole: 'admin' })
  const route = reactive({ params: { kbId: 'test-kb' }, query: { source_knowledge_id: 'test-original' }, path: '/platform/knowledge-bases/test-kb/structured-data', fullPath: '/platform/knowledge-bases/test-kb/structured-data' })
  const unmounts: Array<() => void> = [], scope = effectScope()
  const view: any = scope.run(() => runInNewContext(code, {
    defineProps: () => props, withDefaults: (value: unknown) => value,
    defineEmits: () => (name: string, ...args: any[]) => emitted.push({ name, args }),
    isTrustedStructuredFrameMessage, structuredFrameAuthPayload,
    computed, defineAsyncComponent, h, nextTick, reactive, ref, shallowRef, watch, useAuthStore: () => auth, useRoute: () => route, useRouter: () => ({ push() {}, replace() {} }),
    useTheme: () => ({ effectiveTheme, setTheme(mode: 'light' | 'dark') { requested.push(mode); effectiveTheme.value = mode; return true } }),
    onMounted() {}, onBeforeUnmount: (fn: () => void) => unmounts.push(fn),
    useUploadConfirmStore: () => ({ rejectConfirm() {} }),
    getKnowledgeBaseById: async () => { calls.push('knowledge-base'); return { data: { id: 'test-kb', name: '测试库' } } },
    getDefaultKnowledgeBase: async () => { calls.push('default-base'); return { data: {} } },
    getResearchWorkbench: async () => { calls.push('workbench'); return { success: true, data: { id: 'test-workbench', knowledge_base_id: 'test-kb' } } },
    createMaterialClient: () => ({ async get() { calls.push('materials'); return {} } }),
    workspaceIdentity: (tenant: number, user: string) => `${tenant}:${user}`, workspaceErrorMessage: () => 'error',
    takeIntake: () => null, isTable: () => false, isStructuredFile: () => false, clearTimeout() {},
    CustomEvent,
    window: { location: { origin: 'https://test.local' }, removeEventListener() {}, dispatchEvent(event: CustomEvent) { events.push(event) } },
  }))
  if (surface.name === 'materials review') view.reviewOpen.value = true
  view.frame.value = { contentWindow: { postMessage: (payload: unknown, target: string) => messages.push({ payload, target }) } }
  for (let i = 0; i < 4; i++) { await Promise.resolve(); await nextTick() }
  const event = (data: any) => ({ origin: 'https://test.local', source: view.frame.value.contentWindow, data })
  return { view, props, auth, effectiveTheme, requested, calls, messages, events, emitted, event, close: () => { unmounts.forEach(fn => fn()); scope.stop() } }
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
  const f = await fixture(surfaces.find(surface => surface.name === 'research')!); t.after(f.close)
  const request = f.event({ type: 'workspace-theme-request', mode: 'dark' })
  f.view.receive({ ...request, origin: 'https://untrusted.example' }); f.view.receive({ ...request, source: {} })
  f.view.receive(f.event({ type: 'workspace-theme-request', mode: 'invalid' }))
  assert.deepEqual(f.requested, [])
  f.view.receive(request); await nextTick()
  assert.deepEqual(f.requested, ['dark'])
  assert.equal(f.messages[0]!.payload.type, 'workspace-theme')
})

test('knowledge structured uploads refresh the current directory only from its trusted scoped frame', async t => {
  const f = await fixture(surfaces.find(surface => surface.name === 'knowledge structured')!); t.after(f.close)
  const updated = f.event({ type: 'structured-import-updated', tenant_id: '1', knowledge_base_id: 'test-kb' })
  f.view.receive({ ...updated, origin: 'https://untrusted.example' })
  f.view.receive({ ...updated, source: {} })
  f.view.receive(f.event({ ...updated.data, tenant_id: '2' }))
  f.view.receive(f.event({ ...updated.data, knowledge_base_id: 'other-kb' }))
  assert.equal(f.events.length, 0)
  f.view.receive(updated)
  assert.equal(f.events.length, 1)
  assert.equal(f.events[0]!.type, 'weknora:knowledge-directory-change')
  assert.deepEqual({ ...f.events[0]!.detail }, { kbId: 'test-kb' })
})


test('shared structured host opens model settings in place and keeps PDF source auth through token rotation', async t => {
  const f = await fixture(surfaces.find(surface => surface.name === 'knowledge structured')!); t.after(f.close)
  const generation = f.view.frameGeneration, src = f.view.src.value, calls = [...f.calls]
  f.auth.token = 'current-token'; await nextTick(); f.messages.length = 0
  f.props.view = 'model-settings'; await nextTick()
  assert.equal(f.messages.length, 1)
  assert.equal(f.messages[0]!.payload.type, 'structured-open-model-settings')
  f.auth.token = 'rotated-token'; await nextTick()
  const auth = f.messages.find(message => message.payload.type === 'structured-auth')!.payload
  assert.equal(auth.userId, 'test-user')
  assert.equal(auth.sourceKnowledgeId, 'test-original')
  assert.equal(auth.sourceFileType, 'pdf')
  assert.equal(auth.openModelSettings, true)
  assert.equal(f.view.frameGeneration, generation)
  assert.equal(f.view.src.value, src)
  assert.deepEqual(f.calls, calls)
  const opened = f.event({ type: 'structured-model-settings-opened' })
  f.view.receive({ ...opened, source: {} })
  assert.equal(f.view.modelSettingsRequested.value, true)
  f.view.receive(opened)
  assert.equal(f.view.modelSettingsRequested.value, false)
  assert.equal(f.emitted[0]!.name, 'modelSettingsOpened')
  f.messages.length = 0
  f.view.receive(f.event({ type: 'structured-navigate', view: 'model-settings' }))
  assert.equal(f.messages[0]!.payload.type, 'structured-open-model-settings')
  assert.equal(f.emitted.length, 1, 'PDF review stays mounted while opening its model selector')
})

test('shared structured host sends an initial model selector request with the current Excel source', async t => {
  const f = await fixture(surfaces.find(surface => surface.name === 'knowledge structured')!, 'model-settings'); t.after(f.close)
  f.props.sourceFileType = 'xlsx'
  f.auth.token = 'current-token'; await nextTick()
  const auth = f.messages.find(message => message.payload.type === 'structured-auth')!.payload
  assert.equal(auth.sourceKnowledgeId, 'test-original')
  assert.equal(auth.sourceFileType, 'xlsx')
  assert.equal(auth.openModelSettings, true)
  assert.equal(new URL(f.view.src.value, 'https://test.local').searchParams.has('view'), false)
})
