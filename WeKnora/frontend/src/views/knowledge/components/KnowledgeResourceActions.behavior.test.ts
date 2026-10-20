import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as directory from '../../workspaces/knowledgeDirectory'
import * as forms from '../../workspaces/workspaceForm'

type Host = { type: string; props: Record<string, any>; children: Host[]; text: string; parent?: Host }
const node = (type: string): Host => ({ type, props: {}, children: [], text: '' })
const renderer = vue.createRenderer<Host, Host>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: text => ({ ...node('#comment'), text }),
  patchProp: (el, key, _old, value) => { el.props[key] = value }, setText: (el, text) => { el.text = text },
  setElementText: (el, text) => { el.text = text; el.children = [] }, parentNode: el => el.parent || null,
  nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] || null,
  insert(el, parent, anchor) { if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1); el.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(i < 0 ? parent.children.length : i, 0, el) },
  remove(el) { el.parent?.children.splice(el.parent.children.indexOf(el), 1); el.parent = undefined },
})
const all = (n: Host, pred: (n: Host) => boolean): Host[] => [...(pred(n) ? [n] : []), ...n.children.flatMap(x => all(x, pred))]
const text = (n: Host): string => n.type === '#comment' ? '' : n.text + n.children.map(text).join('')
const flush = async () => { for (let i = 0; i < 6; i++) { await Promise.resolve(); await vue.nextTick() } }
const deferred = () => { let resolve!: (v: any) => void; const promise = new Promise<any>(r => { resolve = r }); return { promise, resolve } }
const plainValue = (v: unknown) => JSON.parse(JSON.stringify(v))
function fixture(kind: 'kb' | 'folder', options: { canRename?: boolean; canDelete?: boolean; canEdit?: boolean; read?: () => Promise<any>; mutate?: () => Promise<any>; rootFolder?: boolean } = {}) {
  const auth = vue.reactive({ selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'actor-a' }, token: 'test-token' })
  const calls: Array<{ operation: string; args: any[] }> = [], changes: any[] = [], events = new EventTarget(), emitted: string[] = []
  const base = vue.reactive({ id: 'kb-a', name: 'Original', tenant_id: 1 }), folder = vue.reactive({ path: options.rootFolder ? '' : 'Parent/Original', name: 'Original', total_count: undefined as number | undefined })
  const props = vue.reactive({ knowledgeBase: base, canRename: options.canRename ?? true, canDelete: options.canDelete ?? true,
    kbId: 'kb-a', folder, canEdit: options.canEdit ?? true, allowDelete: true })
  const resources = { invalidate: (...args: any[]) => calls.push({ operation: 'invalidate', args }), ensureKnowledgeBases: async (...args: any[]) => { calls.push({ operation: 'refresh', args }) } }
  const organizations = { fetchSharedKnowledgeBases: async () => {} }
  events.addEventListener('weknora:knowledge-base-changed', () => emitted.push('knowledge-base'))
  events.addEventListener('weknora:workspaces-changed', () => emitted.push('workspaces'))
  events.addEventListener(directory.KNOWLEDGE_DIRECTORY_EVENT, () => emitted.push('directory'))
  const api = {
    getKnowledgeBaseById: async (...args: any[]) => { calls.push({ operation: 'read', args }); return options.read ? options.read() : { success: true, data: { id: base.id, description: 'Keep the existing description.' } } },
    updateKnowledgeBase: async (...args: any[]) => { calls.push({ operation: 'rename-kb', args }); return options.mutate ? options.mutate() : { success: true } },
    deleteKnowledgeBase: async (...args: any[]) => { calls.push({ operation: 'delete-kb', args }); return options.mutate ? options.mutate() : { success: true } },
    renameKnowledgeFolder: async (...args: any[]) => { calls.push({ operation: 'rename-folder', args }); return options.mutate ? options.mutate() : { success: true } },
    deleteKnowledgeFolder: async (...args: any[]) => { calls.push({ operation: 'delete-folder', args }); return options.mutate ? options.mutate() : { success: true } },
  }
  const modules: Record<string, unknown> = {
    vue, 'tdesign-vue-next': { MessagePlugin: { success() {} } }, '@/api/knowledge-base': api,
    '@/stores/auth': { useAuthStore: () => auth }, '@/stores/chatResources': { useChatResourcesStore: () => resources },
    '@/stores/organization': { useOrganizationStore: () => organizations }, '@/views/workspaces/workspaceForm': forms,
    '@/views/workspaces/knowledgeDirectory': directory,
  }
  const file = kind === 'kb' ? './KnowledgeBaseActions.vue' : './KnowledgeFolderActions.vue'
  const { descriptor } = parse(readFileSync(new URL(file, import.meta.url), 'utf8'))
  const compiled = ts.transpileModule(compileScript(descriptor, { id: file, inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const exports: { default?: vue.Component } = {}
  runInNewContext(compiled, { exports, window: events, CustomEvent, console, require(name: string) { if (name in modules) return modules[name]; throw new Error(`Unexpected import ${name}`) } })
  const root = node('root')
  // Keep the passed permissions/target reactive, like each production caller.
  const wrapper = vue.defineComponent({ setup: () => () => vue.h(exports.default!, { ...props, onChanged: (v: any) => changes.push(v), onRenamed: (v: any) => changes.push(v), onDeleted: (v: any) => changes.push(v) }) })
  const actualApp = renderer.createApp(wrapper)
  const plain = vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('stub', attrs, slots.default?.()) })
  actualApp.component('t-icon', plain); actualApp.component('t-alert', plain)
  actualApp.component('t-popup', vue.defineComponent({ setup: (_p, { slots }) => () => vue.h('popup', {}, [slots.default?.(), slots.content?.()]) }))
  actualApp.component('t-dialog', vue.defineComponent({ props: ['visible'], setup: (p, { attrs, slots }) => () => vue.h('dialog', { ...attrs, visible: p.visible }, p.visible ? slots.default?.() : []) }))
  actualApp.component('t-input', vue.defineComponent({ setup: (_p, { attrs }) => () => vue.h('input', attrs) }))
  actualApp.config.warnHandler = () => {}; actualApp.mount(root)
  const button = (label: string) => all(root, n => n.type === 'button' && text(n) === label)[0]
  const dialog = () => all(root, n => n.type === 'dialog')[0]!
  const click = async (label: string) => { const el = button(label); assert.ok(el); el.props.onClick({ stopPropagation() {}, preventDefault() {} }); await flush() }
  const setName = async (value: string) => { const input = all(root, n => n.type === 'input')[0]; assert.ok(input); input.props['onUpdate:modelValue'](value); await flush() }
  const confirm = async () => { dialog().props.onConfirm(); await flush() }
  const error = () => all(root, n => typeof n.props.message === 'string').map(n => n.props.message).join('')
  return { root, props, auth, base, folder, calls, changes, emitted, button, dialog, click, setName, confirm, error, close: () => actualApp.unmount() }
}

test('KB rename changes only name and preserves the fresh authorized description', async t => {
  const f = fixture('kb'); t.after(f.close); await f.click('重命名'); await f.setName('  Renamed  '); await f.confirm()
  assert.deepEqual(plainValue(f.calls.find(c => c.operation === 'rename-kb')?.args), ['kb-a', { name: 'Renamed', description: 'Keep the existing description.' }])
  assert.equal(f.dialog().props.visible, false); assert.deepEqual(plainValue(f.changes), [{ id: 'kb-a', deleted: false, name: 'Renamed' }])
  assert.deepEqual(f.emitted, ['workspaces', 'knowledge-base'])
})
test('KB rename rejects wrong-library metadata before any update', async t => {
  const f = fixture('kb', { read: async () => ({ success: true, data: { id: 'other-kb', description: 'Private' } }) }); t.after(f.close)
  await f.click('重命名'); await f.setName('Renamed'); await f.confirm()
  assert.equal(f.calls.some(c => c.operation === 'rename-kb'), false); assert.equal(f.dialog().props.visible, true); assert.ok(f.error())
})
for (const kind of ['kb', 'folder'] as const) {
  test(`${kind} rename validates blank names and preserves draft after API failure`, async t => {
    const f = fixture(kind, { mutate: async () => { throw new Error('Permission changed') } }); t.after(f.close)
    await f.click('重命名'); await f.setName(' '); await f.confirm(); assert.equal(f.calls.length, 0); assert.ok(f.error())
    await f.setName('Retry name'); await f.confirm(); assert.equal(f.dialog().props.visible, true); assert.equal(f.error(), 'Permission changed')
    assert.equal(all(f.root, n => n.type === 'input')[0]?.props.modelValue, 'Retry name'); assert.equal(f.changes.length, 0)
  })
  test(`${kind} pending delete submits once and ignores late results after actor switch`, async t => {
    const pending = deferred(), f = fixture(kind, { mutate: () => pending.promise }); t.after(f.close)
    await f.click('删除'); await f.confirm(); await f.confirm()
    assert.equal(f.calls.filter(c => c.operation.startsWith('delete-')).length, 1)
    f.auth.user.id = 'actor-b'; await flush(); pending.resolve({ success: true }); await flush()
    assert.equal(f.dialog().props.visible, false); assert.equal(f.changes.length, 0); assert.equal(f.emitted.length, 0)
  })
  test(`${kind} unmount cannot publish a late successful delete`, async () => {
    const pending = deferred(), f = fixture(kind, { mutate: () => pending.promise })
    await f.click('删除'); await f.confirm(); f.close(); pending.resolve({ success: true }); await flush(); assert.equal(f.changes.length, 0); assert.equal(f.emitted.length, 0)
  })
}
for (const change of ['actor', 'tenant', 'kb', 'permission', 'actor-round-trip'] as const) {
  test(`fresh KB read cannot continue a rename after ${change} changes`, async t => {
    const pending = deferred(), f = fixture('kb', { read: () => pending.promise }); t.after(f.close)
    await f.click('重命名'); await f.setName('Renamed'); await f.confirm()
    if (change === 'tenant') f.auth.selectedTenantId = 2
    else if (change === 'kb') f.base.id = 'kb-b'
    else if (change === 'permission') f.props.canRename = false
    else { f.auth.user.id = 'actor-b'; if (change === 'actor-round-trip') f.auth.user.id = 'actor-a' }
    await flush(); pending.resolve({ data: { id: 'kb-a', description: 'Keep' } }); await flush()
    assert.equal(f.calls.some(c => c.operation === 'rename-kb'), false); assert.equal(f.emitted.length, 0)
  })
}
test('read-only KBs and root/read-only folders do not expose mutations', async t => {
  for (const f of [fixture('kb', { canRename: false, canDelete: false }), fixture('folder', { canEdit: false }), fixture('folder', { rootFolder: true })]) {
    t.after(f.close); await flush(); assert.equal(f.button('重命名'), undefined); assert.equal(f.button('删除'), undefined); assert.equal(f.calls.length, 0)
  }
})
test('folder rename keeps its parent path and emits a scoped directory refresh', async t => {
  const f = fixture('folder'); t.after(f.close); await f.click('重命名'); await f.setName('Renamed'); await f.confirm()
  assert.deepEqual(f.calls[0], { operation: 'rename-folder', args: ['kb-a', 'Parent/Original', 'Parent/Renamed'] })
  assert.deepEqual(plainValue(f.changes), [{ from: 'Parent/Original', to: 'Parent/Renamed' }]); assert.deepEqual(f.emitted, ['directory'])
})
for (const change of ['actor', 'tenant', 'kb', 'permission', 'actor-round-trip'] as const) {
  test(`a completed folder rename cannot refresh or navigate after ${change} changes`, async t => {
    const pending = deferred(), f = fixture('folder', { mutate: () => pending.promise }); t.after(f.close)
    await f.click('重命名'); await f.setName('Renamed'); await f.confirm()
    assert.equal(f.calls.filter(c => c.operation === 'rename-folder').length, 1)
    if (change === 'tenant') f.auth.selectedTenantId = 2
    else if (change === 'kb') f.props.kbId = 'kb-b'
    else if (change === 'permission') f.props.canEdit = false
    else { f.auth.user.id = 'actor-b'; if (change === 'actor-round-trip') f.auth.user.id = 'actor-a' }
    await flush(); pending.resolve({ success: true }); await flush(); assert.equal(f.changes.length, 0); assert.equal(f.emitted.length, 0)
  })
}
test('recursive folder deletion confirms its subtree and preserves the dialog on refusal', async t => {
  const f = fixture('folder', { mutate: async () => { throw new Error('Permission changed') } }); t.after(f.close)
  await f.click('删除'); assert.match(text(f.dialog()), /Parent\/Original/); assert.match(text(f.dialog()), /所有子文件夹和文件/); assert.match(text(f.dialog()), /无法撤销/)
  assert.doesNotMatch(text(f.dialog()), /个文件/); await f.confirm()
  assert.deepEqual(plainValue(f.calls[0]), { operation: 'delete-folder', args: ['kb-a', 'Parent/Original', { recursive: true }] })
    assert.equal(f.dialog().props.visible, true); assert.match(f.error(), /删除未完成，可重试/); assert.match(f.error(), /Permission changed/); assert.deepEqual(f.emitted, ['directory'])
})
test('folder delete displays only a known real subtree file count', async t => {
  const f = fixture('folder'); t.after(f.close); f.folder.total_count = 7
  await f.click('删除'); assert.match(text(f.dialog()), /7 个文件（含子目录）/); await f.confirm()
  assert.equal(f.dialog().props.visible, false); assert.deepEqual(f.changes, ['Parent/Original'])
})
test('cross-tenant KB administrator shares can rename but cannot delete the source KB', async t => {
  const f = fixture('kb'); t.after(f.close); f.base.tenant_id = 2; await flush()
  assert.ok(f.button('重命名')); assert.equal(f.button('删除'), undefined); assert.equal(f.calls.length, 0)
})
for (const kind of ['kb', 'folder'] as const) {
  for (const action of ['重命名', '删除']) {
    test(`${kind} ${action} rejects absent, malformed and non-true success acknowledgements`, async t => {
      for (const response of [undefined, null, {}, '<html>Proxy error</html>', { success: false }, { success: 'true' }]) {
        const f = fixture(kind, { mutate: async () => response }); t.after(f.close)
        await f.click(action); if (action === '重命名') await f.setName('Renamed'); await f.confirm()
        assert.equal(f.dialog().props.visible, true); assert.ok(f.error()); assert.equal(f.changes.length, 0)
        assert.deepEqual(f.emitted, kind === 'folder' && action === '删除' ? ['directory'] : [])
        assert.equal(f.calls.some(c => c.operation === 'invalidate'), false)
      }
    })
  }
}
test('KB delete confirmation describes only workbenches using this KB as the main library', async t => {
  const f = fixture('kb'); t.after(f.close); await f.click('删除'); assert.match(text(f.dialog()), /以此库为主知识库的工作台/); await f.confirm()
  assert.deepEqual(f.calls.filter(c => c.operation === 'delete-kb')[0]?.args, ['kb-a']); assert.deepEqual(plainValue(f.changes), [{ id: 'kb-a', deleted: true }])
})
