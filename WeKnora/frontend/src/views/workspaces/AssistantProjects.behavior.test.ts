import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as forms from './workspaceForm'
import * as sorting from '../../utils/resourceSorting'

type Host = { type: string; props: Record<string, any>; children: Host[]; text: string; parent?: Host }
const node = (type: string): Host => ({ type, props: {}, children: [], text: '' })
const renderer = vue.createRenderer<Host, Host>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: text => ({ ...node('#comment'), text }),
  patchProp: (el, key, _old, value) => { el.props[key] = value }, setText: (el, text) => { el.text = text },
  setElementText: (el, text) => { el.children = []; el.text = text }, parentNode: el => el.parent || null,
  nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] || null,
  insert(el, parent, anchor) { if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1); el.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, el) },
  remove(el) { el.parent?.children.splice(el.parent.children.indexOf(el), 1); el.parent = undefined },
})
const all = (el: Host, predicate: (n: Host) => boolean): Host[] => [...(predicate(el) ? [el] : []), ...el.children.flatMap(child => all(child, predicate))]
const text = (el: Host): string => el.type === '#comment' ? '' : el.text + el.children.map(text).join('')
const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await vue.nextTick() } }
const deferred = () => { let resolve!: (value: any) => void; const promise = new Promise<any>(r => { resolve = r }); return { promise, resolve } }
const plain = (value: unknown) => JSON.parse(JSON.stringify(value))
const initial = [
  { id: 'project-a', name: 'Project A', description: 'Research notes', session_count: 3, updated_at: '2026-01-01' },
  { id: 'project-b', name: 'Project B', description: '', session_count: 1, updated_at: '2026-02-01' },
]
function fixture(options: { detail?: boolean; viewer?: boolean; failList?: boolean; list?: () => Promise<any>; update?: (id: string, data: any) => Promise<any>; remove?: (id: string) => Promise<any> } = {}) {
  const route = vue.reactive({ params: options.detail ? { projectId: 'project-a' } : {} as { projectId?: string } })
  const auth = vue.reactive({ selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'user-a' }, token: 'test-token', viewer: options.viewer !== false, hasRole: (role: string): boolean => auth.viewer && role === 'viewer' })
  let items = initial.map(p => ({ ...p }))
  const calls: Array<{ operation: string; args: any[] }> = [], navigations: any[] = [], events: string[] = []
  const browser = new EventTarget(); browser.addEventListener('weknora:workspaces-changed', () => events.push('changed'))
  const api = {
    listAssistantProjects: async () => { calls.push({ operation: 'list', args: [] }); if (options.failList) throw new Error('List unavailable'); return options.list ? options.list() : { success: true, data: items.map(p => ({ ...p })) } },
    getAssistantProject: async (id: string) => { calls.push({ operation: 'get', args: [id] }); return { success: true, data: { ...items.find(p => p.id === id) } } },
    listProjectSessions: async (id: string) => { calls.push({ operation: 'sessions', args: [id] }); return { success: true, data: [{ id: 'session-existing', title: 'Existing conversation' }], total: 1 } },
    updateAssistantProject: async (id: string, data: any) => { calls.push({ operation: 'rename', args: [id, data] }); if (options.update) return options.update(id, data); items = items.map(p => p.id === id ? { ...p, ...data } : p); return { success: true, data: { ...items.find(p => p.id === id), session_count: 0 } } },
    deleteAssistantProject: async (id: string) => { calls.push({ operation: 'delete', args: [id] }); if (options.remove) return options.remove(id); items = items.filter(p => p.id !== id); return { success: true } },
    createAssistantProject: async () => { throw new Error('Unexpected create') },
    assignSessionToProject: async () => { throw new Error('Unexpected assignment') },
  }
  const passive = vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('stub', attrs, [slots.default?.(), slots.icon?.()]) })
  const toolbar = vue.defineComponent({ props: ['query'], emits: ['update:query'], setup: (p, { emit }) => () => vue.h('input', { 'data-search': true, value: p.query, onInput: (v: string) => emit('update:query', v) }) })
  const sort = vue.defineComponent({ props: ['modelValue'], emits: ['update:modelValue'], setup: (p, { emit }) => () => vue.h('select', { 'data-sort': true, value: p.modelValue, onChange: (v: string) => emit('update:modelValue', v) }) })
  const modules: Record<string, any> = {
    vue, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: (target: any) => { navigations.push(target); return Promise.resolve() } }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/stores/menu': { useMenuStore: () => ({ changeIsFirstSession() {} }) },
    '@/api/workspaces': api, '@/api/chat': { createSessions: async () => { throw new Error('Unexpected session creation') }, getSessionsList: async () => ({ data: [] }) },
    './workspaceForm': forms, './workspaces.css': {}, '@/utils/resourceSorting': sorting,
    '@/components/ResourceListToolbar.vue': { default: toolbar }, '@/components/ResourceSortControl.vue': { default: sort },
    '@/components/EmptyState.vue': { default: vue.defineComponent({ props: ['title', 'description'], setup: (p, { slots }) => () => vue.h('section', {}, [p.title, p.description, slots.default?.()]) }) },
  }
  function load(file: string) {
    const { descriptor } = parse(readFileSync(new URL(file, import.meta.url), 'utf8'))
    const compiled = ts.transpileModule(compileScript(descriptor, { id: file, inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
    const exports: { default?: vue.Component } = {}
    runInNewContext(compiled, { exports, console, window: browser, CustomEvent, require(name: string) { if (!(name in modules)) throw new Error(`Unexpected import ${name}`); const value = modules[name]; return value && typeof value === 'object' && 'default' in value ? { __esModule: true, ...value } : value } })
    return exports.default!
  }
  if (existsSync(new URL('./ProjectActions.vue', import.meta.url))) modules['./ProjectActions.vue'] = { default: load('./ProjectActions.vue') }
  const root = node('root'), app = renderer.createApp(load('./AssistantProjects.vue'))
  for (const name of ['t-icon', 't-alert', 't-loading', 't-tooltip', 't-pagination']) app.component(name, passive)
  app.component('t-button', vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('button', attrs, [slots.default?.(), slots.icon?.()]) }))
  app.component('t-popup', vue.defineComponent({ props: ['visible'], emits: ['update:visible'], setup: (p, { slots, emit }) => () => vue.h('popup', {}, [vue.h('span', { onClick: () => emit('update:visible', !p.visible) }, slots.default?.()), ...(p.visible ? [slots.content?.()] : [])]) }))
  app.component('t-input', vue.defineComponent({ props: ['modelValue'], emits: ['update:modelValue', 'enter'], setup: (p, { emit, attrs }) => () => vue.h('input', { ...attrs, value: p.modelValue, onInput: (value: string) => emit('update:modelValue', value), onEnter: () => emit('enter') }) }))
  app.component('t-dialog', vue.defineComponent({ props: ['visible', 'header', 'confirmBtn', 'cancelBtn'], emits: ['confirm', 'cancel', 'close', 'update:visible'], setup: (p, { slots, emit, attrs }) => () => p.visible ? vue.h('dialog', { ...attrs, confirmBtn: p.confirmBtn, cancelBtn: p.cancelBtn, onConfirm: () => emit('confirm'), onCancel: () => emit('cancel'), onClose: () => emit('close'), onVisible: (v: boolean) => emit('update:visible', v) }, [p.header, slots.default?.()]) : null }))
  app.config.warnHandler = () => {}; app.mount(root)
  const cards = () => all(root, n => n.props.role === 'link')
  const find = (key: string) => all(root, n => !!n.props[key])[0]
  const dialog = () => all(root, n => n.type === 'dialog')[0]
  async function open(kind: string, id = 'project-a') {
    const parent = cards().find(c => String(c.props['aria-label']).includes(items.find(p => p.id === id)?.name || 'Project A')) || root
    const trigger = all(parent, n => n.type === 'button' && n.props['aria-haspopup'] === 'menu')[0]; assert.ok(trigger, 'project more trigger exists')
    for (const handle of [trigger.props.onKeydown].flat()) handle({ key: 'ArrowDown', preventDefault() {} }); await flush()
    const item = all(parent, n => n.props.role === 'menuitem' && text(n) === kind)[0]; assert.ok(item, 'requested action exists')
    item.props.onClick({ stopPropagation() {} }); await flush()
  }
  async function setName(value: string) { const input = all(root, n => n.type === 'input' && n.props['aria-label'] === '项目名称')[0]; assert.ok(input); input.props.onInput(value); await flush() }
  const error = () => all(root, n => typeof n.props.message === 'string').map(n => n.props.message).join('')
  return { root, auth, route, calls, navigations, events, cards, find, dialog, open, setName, error, close: () => app.unmount() }
}

test('project cards open directly by pointer and keyboard without a preview button', async t => {
  const f = fixture(); t.after(f.close); await flush(); assert.equal(f.cards().length, 2)
  const card = f.cards().find(c => text(c).includes('Project A'))!; card.props.onClick(); await flush()
  assert.equal(f.navigations.at(-1), '/platform/projects/project-a')
  for (const key of ['Enter', ' ']) { const before = f.navigations.length; for (const handle of [card.props.onKeydown].flat()) handle({ key, target: card, currentTarget: card, preventDefault() {} }); await flush(); assert.equal(f.navigations.length, before + 1) }
  assert.equal(text(f.root).includes('预览项目'), false)
})
test('project list reuses search and sort while preserving authoritative conversation counts', async t => {
  const f = fixture(); t.after(f.close); await flush(); assert.ok(f.find('data-search')); assert.match(text(f.cards()[0]!), /Project B/)
   f.find('data-search')!.props.onInput('notes'); await flush(); assert.equal(f.cards().length, 1); assert.match(text(f.cards()[0]!), /3 条对话/)
  f.find('data-search')!.props.onInput('none'); await flush(); assert.equal(f.cards().length, 0); assert.match(text(f.root), /没有匹配的项目/)
  f.find('data-search')!.props.onInput(''); f.find('data-sort')!.props.onChange('name_asc'); await flush(); assert.match(text(f.cards()[0]!), /Project A/)
})
test('a failed project list shows retry without an invented empty-success state', async t => {
  const f = fixture({ failList: true }); t.after(f.close); await flush(); assert.equal(f.cards().length, 0); assert.match(f.error(), /List unavailable/); assert.doesNotMatch(text(f.root), /还没有项目/)
})

test('rename validates, updates only the name and refreshes the authoritative count', async t => {
  const f = fixture(); t.after(f.close); await flush(); await f.open('重命名'); await f.setName(' '); f.dialog()!.props.onConfirm(); await flush()
  assert.equal(f.calls.some(c => c.operation === 'rename'), false); assert.match(f.error(), /请填写名称/)
  await f.setName('  Renamed project  '); f.dialog()!.props.onConfirm(); await flush()
  assert.deepEqual(plain(f.calls.find(c => c.operation === 'rename')?.args), ['project-a', { name: 'Renamed project' }])
  const card = f.cards().find(c => text(c).includes('Renamed project'))!; assert.ok(card); assert.match(text(card), /Research notes/); assert.match(text(card), /3 条对话/)
  assert.equal(f.calls.filter(c => c.operation === 'list').length, 2); assert.equal(f.navigations.length, 0); assert.deepEqual(f.events, ['changed']); assert.equal(f.dialog(), undefined)
})
test('rename with unchanged trimmed name closes without a mutation', async t => {
  const f = fixture(); t.after(f.close); await flush(); await f.open('重命名'); await f.setName('  Project A '); f.dialog()!.props.onConfirm(); await flush()
  assert.equal(f.calls.some(c => c.operation === 'rename'), false); assert.equal(f.dialog(), undefined); assert.deepEqual(f.events, [])
})
test('project deletion confirms retention of conversations and cancellation sends no request', async t => {
  const f = fixture(); t.after(f.close); await flush(); await f.open('删除')
  assert.match(text(f.dialog()!), /对话及消息仍保留在历史对话中/); assert.equal(f.calls.some(c => c.operation === 'delete'), false)
  f.dialog()!.props.onCancel(); await flush(); assert.equal(f.dialog(), undefined); assert.equal(f.cards().length, 2); assert.equal(f.calls.some(c => c.operation === 'delete'), false)
  await f.open('删除'); f.dialog()!.props.onConfirm(); await flush()
  assert.deepEqual(f.calls.filter(c => c.operation === 'delete'), [{ operation: 'delete', args: ['project-a'] }]); assert.equal(f.cards().length, 1); assert.match(text(f.cards()[0]!), /Project B/)
  assert.deepEqual(f.events, ['changed']); assert.equal(f.navigations.length, 0)
})
test('detail rename refreshes the project and its session bindings', async t => {
  const f = fixture({ detail: true }); t.after(f.close); await flush(); await f.open('重命名'); await f.setName('Renamed detail'); f.dialog()!.props.onConfirm(); await flush()
  assert.match(text(f.root), /Renamed detail/); assert.match(text(f.root), /Existing conversation/)
  assert.equal(f.calls.filter(c => c.operation === 'get').length, 2); assert.equal(f.calls.filter(c => c.operation === 'sessions').length, 2); assert.equal(f.navigations.length, 0)
})
test('deleting an open project returns to the project list without session mutations', async t => {
  const f = fixture({ detail: true }); t.after(f.close); await flush(); await f.open('删除'); f.dialog()!.props.onConfirm(); await flush()
  assert.equal(f.navigations.at(-1), '/platform/projects'); assert.equal(f.calls.filter(c => c.operation === 'delete').length, 1); assert.deepEqual(f.events, ['changed'])
})
for (const kind of ['重命名', '删除']) {
  test(`${kind} preserves the dialog and draft after a refused request`, async t => {
    const refuse = async () => { throw new Error('Permission changed') }
    const f = fixture({ update: refuse, remove: refuse }); t.after(f.close); await flush(); await f.open(kind); if (kind === '重命名') await f.setName('Retry draft')
    f.dialog()!.props.onConfirm(); await flush(); assert.ok(f.dialog()); assert.equal(f.error(), 'Permission changed'); assert.equal(f.cards().length, 2); assert.deepEqual(f.events, [])
    if (kind === '重命名') assert.equal(all(f.root, n => n.props['aria-label'] === '项目名称')[0]!.props.value, 'Retry draft')
    f.dialog()!.props.onConfirm(); await flush(); assert.equal(f.calls.filter(c => c.operation === (kind === '重命名' ? 'rename' : 'delete')).length, 2)
  })
  test(`${kind} rejects missing or non-true success acknowledgements`, async t => {
    for (const ack of [undefined, null, {}, '<html>Proxy error</html>', { success: false }, { success: 'true' }]) {
      const f = fixture({ update: async () => ack, remove: async () => ack }); t.after(f.close); await flush(); await f.open(kind); if (kind === '重命名') await f.setName('Renamed')
      f.dialog()!.props.onConfirm(); await flush(); assert.ok(f.dialog()); assert.ok(f.error()); assert.equal(f.cards().length, 2); assert.deepEqual(f.events, []); assert.equal(f.navigations.length, 0)
    }
  })
}
test('rename rejects another project or an unexpected returned name', async t => {
  for (const data of [{ id: 'other-project', name: 'Renamed' }, { id: 'project-a', name: 'Another name' }]) {
    const f = fixture({ update: async () => ({ success: true, data }) }); t.after(f.close); await flush(); await f.open('重命名'); await f.setName('Renamed'); f.dialog()!.props.onConfirm(); await flush()
    assert.ok(f.dialog()); assert.ok(f.error()); assert.deepEqual(f.events, []); assert.match(text(f.cards().find(c => text(c).includes('Project A'))!), /Project A/)
  }
})
for (const change of ['actor', 'tenant', 'token', 'project', 'permission'] as const) {
  test(`a pending rename ignores ${change} ABA and submits only once`, async t => {
    const wait = deferred(), f = fixture({ update: () => wait.promise }); t.after(f.close); await flush(); await f.open('重命名'); await f.setName('Stale name')
    const dialog = f.dialog()!; dialog.props.onConfirm(); dialog.props.onConfirm(); await flush(); assert.equal(f.calls.filter(c => c.operation === 'rename').length, 1)
    assert.equal(f.dialog()!.props.cancelBtn.disabled, true); f.dialog()!.props.onCancel(); await flush(); assert.ok(f.dialog())
    if (change === 'actor') { f.auth.user.id = 'user-b'; f.auth.user.id = 'user-a' }
    else if (change === 'tenant') { f.auth.selectedTenantId = 2; f.auth.selectedTenantId = 1 }
    else if (change === 'token') { f.auth.token = 'different-token'; f.auth.token = 'test-token' }
    else if (change === 'project') { f.route.params.projectId = 'project-b'; delete f.route.params.projectId }
    else { f.auth.viewer = false; f.auth.viewer = true }
    await flush(); wait.resolve({ success: true, data: { id: 'project-a', name: 'Stale name' } }); await flush()
    assert.deepEqual(f.events, []); assert.equal(f.navigations.length, 0); assert.equal(f.dialog(), undefined); assert.doesNotMatch(text(f.root), /Stale name/)
  })
}
test('unmounting a pending delete cannot publish a late success', async () => {
  const wait = deferred(), f = fixture({ remove: () => wait.promise }); await flush(); await f.open('删除'); f.dialog()!.props.onConfirm(); await flush(); f.close(); wait.resolve({ success: true }); await flush()
  assert.deepEqual(f.events, []); assert.equal(f.navigations.length, 0)
})
for (const missing of ['role', 'actor', 'tenant', 'token'] as const) {
  test(`without ${missing}, no mutation trigger, menu or menu role is rendered`, async t => {
    const f = fixture({ viewer: missing !== 'role' }); t.after(f.close); await flush()
    if (missing === 'actor') f.auth.user.id = ''
    if (missing === 'tenant') { f.auth.selectedTenantId = 0; f.auth.tenant.id = 0 }
    if (missing === 'token') f.auth.token = ''
    await flush(); assert.equal(all(f.root, n => n.props['aria-haspopup'] === 'menu').length, 0); assert.equal(all(f.root, n => n.props.role === 'menu' || n.props.role === 'menuitem').length, 0)
  })
}
test('malformed list acknowledgements cannot expose project mutation actions', async t => {
  for (const ack of [{ data: initial }, { success: 'true', data: initial }, { success: false, data: initial }]) {
    const f = fixture({ list: async () => ack }); t.after(f.close); await flush(); assert.equal(f.cards().length, 0); assert.ok(f.error()); assert.equal(all(f.root, n => n.props['aria-haspopup'] === 'menu').length, 0)
  }
})
