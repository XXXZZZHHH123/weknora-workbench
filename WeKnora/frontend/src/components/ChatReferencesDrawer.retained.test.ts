import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import { createRenderer, defineComponent, h, nextTick, onUnmounted, reactive, watch } from 'vue'

const require = createRequire(import.meta.url)
const composable = ts.transpileModule(readFileSync(new URL('../composables/useChatReferencesDrawer.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
const { descriptor } = parse(readFileSync(new URL('./ChatReferencesDrawer.vue', import.meta.url), 'utf8'))
const component = ts.transpileModule(compileScript(descriptor, { id: 'retained-drawer', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
const flush = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)); await nextTick() }
type Node = { tag?: string; text?: string; style: Record<string, unknown>; props: Record<string, unknown>; children: Node[]; parent?: Node }
function fixture() {
  const modules: any = {}, exported: any = {}, auth = reactive({ token: 'test-token-a', selectedTenantId: 1 })
  runInNewContext(composable, { exports: modules, require: (name: string) => name === 'vue' ? require('vue') : require('../utils/referenceSources.ts') })
  let context: any, mounts = 0, unmounts = 0, updates = 0
  const passthrough = defineComponent({ setup: (_p, { slots }) => () => slots.default?.() })
  const Source = defineComponent({ props: ['target', 'active'], emits: ['retention-change'], setup(p, { emit }) {
    mounts++
    emit('retention-change', true, p.target.knowledgeId)
    watch(() => p.active, () => { updates++ })
    onUnmounted(() => { unmounts++ })
    return () => h('synthetic-ppt-viewer', { active: p.active, document: p.target.knowledgeId }, [h('synthetic-slide', { physicalPage: 2 })])
  } })
  runInNewContext(component, { exports: exported, console, localStorage: { getItem: () => null, setItem: () => {} }, window: { innerWidth: 1600, addEventListener: () => {}, removeEventListener: () => {} }, require(name: string) {
    if (name === 'vue') return { ...require('vue'), Transition: passthrough, Teleport: passthrough }
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name === 'vue-router') return { useRouter: () => ({ resolve: () => ({ href: '/document' }) }) }
    if (name === '@/stores/auth') return { useAuthStore: () => auth }
    if (name === '@/composables/useChatReferencesDrawer') return { ...modules, useChatReferencesDrawer: () => context }
    if (name === '@/components/ChatReferenceSourceView.vue') return Source
    if (name.endsWith('.vue')) return defineComponent({ render: () => null })
    if (name.startsWith('@/utils/')) return require('../utils/' + name.slice('@/utils/'.length) + '.ts')
    throw new Error('Unexpected import ' + name)
  } })
  const node = (): Node => ({ style: {}, props: {}, children: [] })
  const root = node()
  const renderer = createRenderer<Node, Node>({
    createElement: tag => ({ ...node(), tag }), createText: text => ({ ...node(), text }), createComment: text => ({ ...node(), text }),
    insert(child, parent, anchor) { if (child.parent) { const old = child.parent.children; old.splice(old.indexOf(child), 1) } child.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; if (i >= 0) parent.children.splice(i, 0, child); else parent.children.push(child) },
    remove(child) { const siblings = child.parent?.children; if (siblings) siblings.splice(siblings.indexOf(child), 1) },
    setElementText(el, text) { el.text = text; el.children = [] }, setText(el, text) { el.text = text },
    patchProp(el, key, _old, value) { el.props[key] = value; if (key === 'style') Object.assign(el.style, value) },
    parentNode: el => el.parent || null, nextSibling: el => { const a = el.parent?.children || []; return a[a.indexOf(el) + 1] || null },
  })
  const app = renderer.createApp({ setup() { context = modules.provideChatReferencesDrawer(); return () => h(exported.default) } })
  for (const name of ['t-icon', 't-button', 't-loading']) app.component(name, { render: () => null })
  app.mount(root)
  const all = (tag: string, current = root): Node[] => [...(current.tag === tag ? [current] : []), ...current.children.flatMap(child => all(tag, child))]
  return { context, root, auth, all, mounts: () => mounts, unmounts: () => unmounts, updates: () => updates, dispose: () => app.unmount() }
}
const target = { knowledgeId: 'synthetic-ppt-a', chunkId: 'synthetic-chunk-a', fileName: 'Synthetic.pptx' }

test('the rendered current PPT DOM survives close/reopen, while hidden child is inactive', async t => {
  const f = fixture(); t.after(f.dispose)
  assert.equal(f.all('synthetic-ppt-viewer').length, 0, 'first screen does not mount or fetch a hidden source')
  f.context.openSource(target); await flush()
  const viewer = f.all('synthetic-ppt-viewer')[0], slide = f.all('synthetic-slide')[0]
  assert.equal(f.mounts(), 1)
  f.context.close(); await flush()
  assert.equal(f.all('aside')[0].style.display, 'none'); assert.equal(viewer.props.active, false)
  assert.equal(f.all('synthetic-slide')[0], slide)
  f.context.openSource({ ...target }); await flush()
  assert.equal(f.all('synthetic-ppt-viewer')[0], viewer); assert.equal(f.all('synthetic-slide')[0], slide); assert.equal(viewer.props.active, true)
  assert.equal(f.mounts(), 1); assert.equal(f.unmounts(), 0)
})

test('changing document or returning to list unmounts the previous viewer rather than collecting DOM', async t => {
  const f = fixture(); t.after(f.dispose)
  f.context.openSource(target); await flush()
  f.context.openSource({ ...target, knowledgeId: 'synthetic-ppt-b' }); await flush()
  assert.equal(f.mounts(), 2); assert.equal(f.unmounts(), 1); assert.equal(f.all('synthetic-ppt-viewer').length, 1)
  f.context.closeSource(); await flush()
  assert.equal(f.unmounts(), 2); assert.equal(f.all('synthetic-ppt-viewer').length, 0)
})

test('auth changes dispose a hidden retained PPT tree and visible auth changes remount a clean viewer', async t => {
  const f = fixture(); t.after(f.dispose)
  f.context.openSource(target); await flush(); f.context.close(); await flush()
  f.auth.token = 'test-token-b'; await flush()
  assert.equal(f.all('synthetic-ppt-viewer').length, 0); assert.equal(f.context.source.value, null)
  f.context.openSource(target); await flush()
  const oldViewer = f.all('synthetic-ppt-viewer')[0]
  f.auth.selectedTenantId = 2; await flush()
  assert.equal(f.all('synthetic-ppt-viewer').length, 1); assert.notEqual(f.all('synthetic-ppt-viewer')[0], oldViewer)
  assert.equal(f.unmounts(), 2)
})

test('a source that cannot retain is unmounted on close', async t => {
  const f = fixture(); t.after(f.dispose)
  f.context.openSource(target); await flush()
  f.context.setSourceRetained(false); f.context.close(); await flush()
  assert.equal(f.context.source.value, null); assert.equal(f.all('synthetic-ppt-viewer').length, 0); assert.equal(f.all('aside').length, 0)
})
