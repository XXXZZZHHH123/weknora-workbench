import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as forms from './workspaceForm'
import * as knowledgeBases from './workspaceKnowledgeBases'
import * as templates from '../research/workbenchTemplates'
import * as featureGuides from '../research/researchFeatureGuides'
import * as sorting from '../../utils/resourceSorting'

type Host = { type: string; props: Record<string, any>; children: Host[]; text: string; parent?: Host }
const node = (type: string): Host => ({ type, props: {}, children: [], text: '' })
const renderer = vue.createRenderer<Host, Host>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: text => ({ ...node('#comment'), text }),
  patchProp: (el, key, _old, value) => { el.props[key] = value }, setText: (el, text) => { el.text = text },
  setElementText: (el, text) => { el.children = []; el.text = text }, parentNode: el => el.parent || null,
  nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] || null,
  insert(el, parent, anchor) {
    if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
    el.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, el)
  },
  remove(el) { el.parent?.children.splice(el.parent.children.indexOf(el), 1); el.parent = undefined },
})
const all = (el: Host, predicate: (n: Host) => boolean): Host[] => [...(predicate(el) ? [el] : []), ...el.children.flatMap(child => all(child, predicate))]
const textOf = (el: Host): string => el.type === '#comment' ? '' : el.text + el.children.map(textOf).join('')
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await vue.nextTick() } }
const passthrough = vue.defineComponent({ setup: (_p, { slots }) => () => vue.h('stub', {}, [slots.default?.(), slots.icon?.()]) })
const button = vue.defineComponent({ setup: (_p, { slots }) => () => vue.h('button', {}, [slots.default?.(), slots.icon?.()]) })
const toolbar = vue.defineComponent({ props: ['query'], emits: ['update:query'], setup: (props, { emit }) => () => vue.h('input', { 'data-search': true, value: props.query, onInput: (value: string) => emit('update:query', value) }) })
const sortControl = vue.defineComponent({ props: ['modelValue'], emits: ['update:modelValue'], setup: (props, { emit }) => () => vue.h('select', { 'data-sort': true, value: props.modelValue, onChange: (value: string) => emit('update:modelValue', value) }) })
const emptyState = vue.defineComponent({ props: ['title', 'description'], setup: (props, { slots }) => () => vue.h('section', {}, [props.title, props.description, slots.default?.()]) })
const workbenches = [
  { can_manage: true, id: 'wb-a', name: '研究 A', description: '独立资料研究', knowledge_base_id: 'kb-a', workspace_mode: 'isolated', template_id: 'industrial', updated_at: '2026-01-01' },
  { can_manage: true, id: 'wb-b', name: '研究 B', description: '', knowledge_base_id: 'kb-b', workspace_mode: 'isolated', template_id: 'blank', updated_at: '2026-02-01' },
]

function fixture(options: { fail?: boolean; items?: typeof workbenches; creating?: boolean; contributor?: boolean; update?: (id: string, data: any) => Promise<any>; remove?: (id: string) => Promise<any> } = {}) {
  const route = vue.reactive({ params: {}, query: {}, name: options.creating ? 'researchWorkbenchCreate' : 'researchWorkbenchList' })
  const auth = vue.reactive({ selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'user-a' }, contributor: options.contributor !== false, hasRole: (): boolean => auth.contributor })
  const navigations: any[] = [], mutations: any[] = []
  const { descriptor } = parse(readFileSync(new URL('./ResearchWorkbenches.vue', import.meta.url), 'utf8'))
  const compiled = ts.transpileModule(compileScript(descriptor, { id: 'research-list-test', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const modules: Record<string, any> = {
    'vue': vue, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: (target: any) => { navigations.push(target); return Promise.resolve() } }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/stores/chatResources': { useChatResourcesStore: () => ({ rawKnowledgeBases: [{ id: 'kb-a', name: '资料 A' }, { id: 'kb-b', name: '资料 B' }], ensureKnowledgeBases: async () => {} }) },
    '@/stores/organization': { useOrganizationStore: () => ({ sharedKnowledgeBases: [], fetchSharedKnowledgeBases: async () => {} }) },
    '@/api/workspaces': { listResearchWorkbenches: async () => { if (options.fail) throw new Error('列表不可用'); return { data: options.items || workbenches } }, getResearchWorkbench: async () => ({ data: null }), updateResearchWorkbench: async (id: string, data: any) => { mutations.push({ action: 'rename', id, data }); return options.update ? options.update(id, data) : { success: true, data: { ...workbenches.find(w => w.id === id), ...data } } }, deleteResearchWorkbench: async (id: string) => { mutations.push({ action: 'delete', id }); return options.remove ? options.remove(id) : { success: true } } },
    '@/views/knowledge/KnowledgeBaseEditorModal.vue': { default: passthrough }, '@/utils/uuid': { createRandomUUID: () => 'request-a' },
    './workspaceForm': forms, './workspaceKnowledgeBases': knowledgeBases, '@/views/research/workbenchTemplates': templates,
    '@/components/ResourceListToolbar.vue': { default: toolbar }, '@/components/ResourceSortControl.vue': { default: sortControl },
    '@/components/EmptyState.vue': { default: emptyState }, '@/utils/resourceSorting': sorting,
    '@/components/ResourcePermissionDrawer.vue': { default: passthrough }, '@/api/resourcePermissions': { updateWorkbenchSourceSharing: async () => ({ success: true }) },
    './workspaces.css': {}, '@/views/research/researchFeatureGuides': featureGuides,
  }
  function load(code: string) {
    const exports: { default?: vue.Component } = {}
    runInNewContext(code, { exports, console, window: new EventTarget(), CustomEvent, require(name: string) {
      const value = modules[name]; if (value === undefined) throw new Error(`Unexpected import: ${name}`)
      return value && typeof value === 'object' && 'default' in value ? { __esModule: true, ...value } : value
    } })
    return exports.default!
  }
  const actions = parse(readFileSync(new URL('./WorkbenchActions.vue', import.meta.url), 'utf8')).descriptor
  modules['./WorkbenchActions.vue'] = { default: load(ts.transpileModule(compileScript(actions, { id: 'research-actions-test', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText) }
  const exports = { default: load(compiled) }
  const root = node('root'), app = renderer.createApp(exports.default!)
  for (const tag of ['t-icon', 't-alert', 't-loading', 't-tooltip']) app.component(tag, passthrough)
  app.component('t-button', button)
  app.component('t-popup', vue.defineComponent({ props: ['visible'], emits: ['update:visible'], setup: (props, { slots }) => () => vue.h('popup', {}, [slots.default?.(), ...(props.visible ? [slots.content?.()] : [])]) }))
  app.component('t-input', vue.defineComponent({ props: ['modelValue'], emits: ['update:modelValue', 'enter'], setup: (props, { emit }) => () => vue.h('input', { 'data-rename-input': true, value: props.modelValue, onInput: (value: string) => emit('update:modelValue', value) }) }))
  app.component('t-dialog', vue.defineComponent({ props: ['visible', 'header', 'confirmBtn'], emits: ['confirm', 'cancel'], setup: (props, { slots, emit }) => () => props.visible ? vue.h('dialog', { 'data-dialog': true }, [props.header, slots.default?.(), vue.h('button', { 'data-confirm': true, onClick: () => emit('confirm') }, props.confirmBtn.content), vue.h('button', { 'data-cancel': true, onClick: () => emit('cancel') }, '取消')]) : null }))
  app.config.warnHandler = () => {}; app.mount(root)
  async function openAction(card: Host, kind: 'rename' | 'delete') {
    const trigger = all(card, el => el.type === 'button' && el.props['aria-haspopup'] === 'menu')[0]; assert.ok(trigger)
    for (const handle of [trigger.props.onKeydown].flat()) handle({ key: 'ArrowDown', preventDefault() {} })
    await flush()
    const item = all(root, el => el.props.role === 'menuitem' && textOf(el) === (kind === 'rename' ? '重命名' : '删除'))[0]; assert.ok(item)
    item.props.onClick({ stopPropagation() {} }); await flush()
  }
  return { root, navigations, mutations, auth, openAction, cards: () => all(root, el => el.props.role === 'link'), find: (key: string) => all(root, el => !!el.props[key])[0]!, close: () => app.unmount() }
}

test('the management card opens the workbench directly with pointer and keyboard', async t => {
  const f = fixture(); t.after(f.close); await flush()
  assert.equal(f.cards().length, 2)
  const card = f.cards().find(el => textOf(el).includes('研究 A'))!
  card.props.onClick({ target: card, currentTarget: card }); await flush()
  assert.equal(f.navigations.at(-1), '/platform/research-workbenches/wb-a')
  assert.equal(f.navigations.length, 1)
  for (const key of ['Enter', ' ']) {
    const previous: number = f.navigations.length
    for (const handler of [card.props.onKeydown].flat()) handler({ key, target: card, currentTarget: card, preventDefault() {} })
    await flush()
    assert.equal(f.navigations.length, previous + 1, `${key} triggers its own navigation`)
    assert.equal(f.navigations.at(-1), '/platform/research-workbenches/wb-a')
  }
  assert.equal(textOf(f.root).includes('一级预览'), false)
  assert.equal(textOf(f.root).includes('工作台预览'), false)
})

test('the knowledge-base shortcut stays scoped and stops the card click', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const card = f.cards().find(el => textOf(el).includes('研究 A'))!
  const source = all(card, el => el.type === 'button' && el.props.class === 'workbench-source')[0]!
  let stopped = false
  source.props.onClick({ stopPropagation() { stopped = true } }); await flush()
  assert.equal(stopped, true)
  assert.equal(f.navigations.at(-1), '/platform/knowledge-bases/kb-a')
  assert.equal(f.navigations.length, 1)
})

test('the reused list toolbar searches source names and clears back to all workbenches', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const search = f.find('data-search'); assert.ok(search)
  search.props.onInput('资料 A'); await flush()
  assert.equal(f.cards().length, 1); assert.ok(textOf(f.cards()[0]!).includes('研究 A'))
  search.props.onInput('不存在'); await flush(); assert.equal(f.cards().length, 0)
  assert.ok(textOf(f.root).includes('没有匹配的工作台'))
  search.props.onInput(''); await flush(); assert.equal(f.cards().length, 2)
})

test('the reused resource sorting changes the visible order without changing source bindings', async t => {
  const f = fixture(); t.after(f.close); await flush()
  assert.ok(textOf(f.cards()[0]!).includes('研究 B'))
  f.find('data-sort').props.onChange('name_asc'); await flush()
  assert.ok(textOf(f.cards()[0]!).includes('研究 A'))
  assert.ok(textOf(f.cards()[0]!).includes('资料 A'))
})

test('a failed list shows the error and does not invent a workbench or show an empty-success message', async t => {
  const f = fixture({ fail: true }); t.after(f.close); await flush()
  assert.equal(f.cards().length, 0)
  assert.equal(textOf(f.root).includes('还没有研究工作台'), false)
  assert.ok(textOf(f.root).includes('重新加载'))
})

test('a successful empty list offers creation without inventing a resource', async t => {
  const f = fixture({ items: [] }); t.after(f.close); await flush()
  assert.equal(f.cards().length, 0)
  assert.ok(textOf(f.root).includes('还没有研究工作台'))
  assert.equal(textOf(f.root).includes('重新加载'), false)
})

test('legacy preview bookmarks redirect to the same workbench and preserve query and hash', () => {
  const source = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('router.ts', source, ts.ScriptTarget.Latest, true)
  let config = ''
  function visit(node: ts.Node) {
    if (ts.isCallExpression(node) && node.expression.getText(ast) === 'createRouter') config = node.arguments[0]!.getText(ast)
    ts.forEachChild(node, visit)
  }
  visit(ast); assert.ok(config)
  const code = ts.transpileModule(`globalThis.routes = (${config.replaceAll('import.meta.env.BASE_URL', "'/'").replaceAll('import.meta.env.DEV', 'false')}).routes`, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
  const context: any = { createWebHistory() {}, SettingsRouteOutlet: {} }; runInNewContext(code, context)
  const routes = context.routes.flatMap((route: any) => [route, ...(route.children || [])])
  const preview = routes.find((route: any) => route.name === 'researchWorkbenchPreview')
  assert.equal(typeof preview.redirect, 'function')
  const result = preview.redirect({ params: { workbenchId: 'real-id' }, query: { mode: 'data' }, hash: '#section' })
  assert.equal(result.name, 'researchWorkbenchDetail'); assert.equal(result.params.workbenchId, 'real-id')
  assert.equal(result.query.mode, 'data'); assert.equal(result.hash, '#section')
  assert.equal(preview.component, undefined)
})

test('owned research cards expose accessible rename and delete actions', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const actions = all(f.root, el => el.type === 'button' && String(el.props['aria-label'] || '').includes('更多操作'))
  assert.equal(actions.length, 2)
})

test('rename validates the name and updates the card without changing its source', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const card = f.cards().find(el => textOf(el).includes('研究 A'))!
  await f.openAction(card, 'rename')
  f.find('data-rename-input').props.onInput('   ')
  f.find('data-confirm').props.onClick(); await flush()
  assert.equal(f.mutations.length, 0)
  f.find('data-rename-input').props.onInput('  新的研究名称  ')
  f.find('data-confirm').props.onClick(); await flush()
  assert.equal(f.mutations[0].action, 'rename'); assert.equal(f.mutations[0].id, 'wb-a')
  assert.deepEqual(Object.keys(f.mutations[0].data), ['name']); assert.equal(f.mutations[0].data.name, '新的研究名称')
  const updated = f.cards().find(el => textOf(el).includes('新的研究名称'))!
  assert.ok(textOf(updated).includes('资料 A'))
  assert.equal(f.navigations.length, 0)
  assert.equal(f.find('data-dialog'), undefined)
})

test('delete requires confirmation and removes only the selected research card', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const open = () => f.openAction(f.cards().find(el => textOf(el).includes('研究 A'))!, 'delete')
  await open()
  assert.equal(f.mutations.length, 0)
  assert.ok(textOf(f.find('data-dialog')).includes('知识库及其中资料会保留'))
  f.find('data-cancel').props.onClick(); await flush()
  assert.equal(f.cards().length, 2); assert.equal(f.mutations.length, 0)
  await open(); f.find('data-confirm').props.onClick(); await flush()
  assert.deepEqual(f.mutations, [{ action: 'delete', id: 'wb-a' }])
  assert.equal(f.cards().length, 1); assert.ok(textOf(f.cards()[0]!).includes('研究 B'))
  assert.equal(f.navigations.length, 0)
})

test('failed deletes keep the card and offer retry without a second confirmation request', async t => {
  const f = fixture({ remove: async () => { throw new Error('没有删除权限') } }); t.after(f.close); await flush()
  await f.openAction(f.cards()[0]!, 'delete')
  f.find('data-confirm').props.onClick(); await flush()
  assert.equal(f.cards().length, 2); assert.ok(f.find('data-dialog'))
  assert.ok(all(f.root, el => el.props.message === '没有删除权限').length)
  assert.equal(f.mutations.length, 1)
})

test('tenant changes discard pending rename results and duplicate submits are ignored', async t => {
  let finish!: (value: any) => void
  const f = fixture({ update: () => new Promise(resolve => { finish = resolve }) }); t.after(f.close); await flush()
  await f.openAction(f.cards().find(el => textOf(el).includes('研究 A'))!, 'rename')
  f.find('data-rename-input').props.onInput('旧空间的改名')
  const confirm = f.find('data-confirm'); confirm.props.onClick(); confirm.props.onClick(); await flush()
  assert.equal(f.mutations.length, 1)
  f.auth.selectedTenantId = 2; await flush()
  finish({ success: true, data: { ...workbenches[0], name: '旧空间的改名' } }); await flush()
  assert.equal(textOf(f.root).includes('旧空间的改名'), false)
  assert.equal(f.find('data-dialog'), undefined)
})

test('viewers can navigate research cards without mutation controls', async t => {
  const f = fixture({ contributor: false }); t.after(f.close); await flush()
  assert.equal(f.cards().length, 2)
  assert.equal(all(f.root, el => el.props['aria-haspopup'] === 'menu').length, 0)
  assert.equal(f.mutations.length, 0)
})

test('a non-successful delete response never removes the research card', async t => {
  const f = fixture({ remove: async () => ({ success: false }) }); t.after(f.close); await flush()
  await f.openAction(f.cards()[0]!, 'delete')
  f.find('data-confirm').props.onClick(); await flush()
  assert.equal(f.cards().length, 2)
  assert.ok(f.find('data-dialog'))
  assert.ok(all(f.root, el => el.props.message === '未能删除工作台，请重试。').length)
})

test('leaving and returning to the same tenant invalidates the old pending action', async t => {
  let finish!: (value: any) => void
  const f = fixture({ update: () => new Promise(resolve => { finish = resolve }) }); t.after(f.close); await flush()
  await f.openAction(f.cards().find(el => textOf(el).includes('研究 A'))!, 'rename')
  f.find('data-rename-input').props.onInput('离开前的改名')
  f.find('data-confirm').props.onClick(); await flush()
  f.auth.selectedTenantId = 2; f.auth.selectedTenantId = 1
  await flush(); finish({ success: true, data: { ...workbenches[0], name: '离开前的改名' } }); await flush()
  assert.equal(textOf(f.root).includes('离开前的改名'), false)
})


test('rename requires an explicit successful response before changing the card', async () => {
  for (const success of [undefined, false, 'true']) {
    const f = fixture({ update: async () => ({ success, data: { ...workbenches[0], name: '不能接受的改名' } }) })
    try {
      await flush()
      await f.openAction(f.cards().find(el => textOf(el).includes('研究 A'))!, 'rename')
      f.find('data-rename-input').props.onInput('不能接受的改名')
      f.find('data-confirm').props.onClick(); await flush()
      assert.equal(f.cards().some(el => textOf(el).includes('不能接受的改名')), false, String(success))
      assert.ok(f.find('data-dialog'))
      assert.ok(all(f.root, el => el.props.message === '未能更新工作台，请重试。').length)
    } finally { f.close() }
  }
})


test('readable workbenches without server management permission have no mutation menu', async () => {
  for (const can_manage of [false, undefined]) {
    const f = fixture({ items: workbenches.map(item => ({ ...item, can_manage })) as typeof workbenches })
    try {
      await flush()
      assert.equal(f.cards().length, 2)
      assert.equal(all(f.root, el => el.props['aria-haspopup'] === 'menu').length, 0)
      assert.equal(f.mutations.length, 0)
    } finally { f.close() }
  }
})

test('legacy administrator can retain metadata management without resource permission management', async t => {
  const f = fixture({ items: workbenches.map(item => ({ ...item, can_manage: true, can_manage_permissions: false })) as typeof workbenches })
  t.after(f.close); await flush()
  const trigger = all(f.cards()[0], el => el.type === 'button' && el.props['aria-haspopup'] === 'menu')[0]!
  for (const handler of [trigger.props.onKeydown].flat()) handler({ key: 'ArrowDown', preventDefault() {} })
  await flush()
  const labels = all(f.root, el => el.props.role === 'menuitem').map(textOf)
  assert.equal(labels.includes('成员与权限'), false)
  assert.equal(labels.includes('重命名'), true)
  assert.equal(labels.includes('删除'), true)
})

test('native workbench popup closes on Escape and before opening the confirmation dialog', async t => {
  const f = fixture(); t.after(f.close); await flush()
  const card = f.cards().find(el => textOf(el).includes('研究 A'))!
  const trigger = all(card, el => el.type === 'button' && el.props['aria-haspopup'] === 'menu')[0]!
  assert.equal(trigger.props['aria-expanded'], false)
  assert.equal(all(f.root, el => el.props.role === 'menu').length, 0)
  for (const handler of [trigger.props.onKeydown].flat()) handler({ key: 'ArrowDown', preventDefault() {} })
  await flush(); assert.equal(trigger.props['aria-expanded'], true); assert.equal(all(f.root, el => el.props.role === 'menu').length, 1)
  for (const handler of [trigger.props.onKeydown].flat()) handler({ key: 'Escape', preventDefault() {} })
  await flush(); assert.equal(trigger.props['aria-expanded'], false); assert.equal(all(f.root, el => el.props.role === 'menu').length, 0)
  await f.openAction(card, 'rename')
  assert.equal(trigger.props['aria-expanded'], false); assert.equal(all(f.root, el => el.props.role === 'menu').length, 0); assert.ok(f.find('data-dialog'))
  f.find('data-cancel').props.onClick(); await flush(); assert.equal(f.find('data-dialog'), undefined); assert.equal(f.mutations.length, 0)
})
