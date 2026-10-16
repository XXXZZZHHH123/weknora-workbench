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
  { id: 'wb-a', name: '研究 A', description: '独立资料研究', knowledge_base_id: 'kb-a', workspace_mode: 'isolated', template_id: 'industrial', updated_at: '2026-01-01' },
  { id: 'wb-b', name: '研究 B', description: '', knowledge_base_id: 'kb-b', workspace_mode: 'isolated', template_id: 'blank', updated_at: '2026-02-01' },
]

function fixture(options: { fail?: boolean; items?: typeof workbenches; creating?: boolean } = {}) {
  const route = vue.reactive({ params: {}, query: {}, name: options.creating ? 'researchWorkbenchCreate' : 'researchWorkbenchList' })
  const auth = vue.reactive({ selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'user-a' }, hasRole: () => true })
  const navigations: any[] = []
  const { descriptor } = parse(readFileSync(new URL('./ResearchWorkbenches.vue', import.meta.url), 'utf8'))
  const compiled = ts.transpileModule(compileScript(descriptor, { id: 'research-list-test', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const modules: Record<string, any> = {
    'vue': vue, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: (target: any) => { navigations.push(target); return Promise.resolve() } }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/stores/chatResources': { useChatResourcesStore: () => ({ rawKnowledgeBases: [{ id: 'kb-a', name: '资料 A' }, { id: 'kb-b', name: '资料 B' }], ensureKnowledgeBases: async () => {} }) },
    '@/stores/organization': { useOrganizationStore: () => ({ sharedKnowledgeBases: [], fetchSharedKnowledgeBases: async () => {} }) },
    '@/api/workspaces': { listResearchWorkbenches: async () => { if (options.fail) throw new Error('列表不可用'); return { data: options.items || workbenches } }, getResearchWorkbench: async () => ({ data: null }) },
    '@/views/knowledge/KnowledgeBaseEditorModal.vue': { default: passthrough }, '@/utils/uuid': { createRandomUUID: () => 'request-a' },
    './workspaceForm': forms, './workspaceKnowledgeBases': knowledgeBases, '@/views/research/workbenchTemplates': templates,
    '@/components/ResourceListToolbar.vue': { default: toolbar }, '@/components/ResourceSortControl.vue': { default: sortControl },
    '@/components/EmptyState.vue': { default: emptyState }, '@/utils/resourceSorting': sorting,
    './workspaces.css': {},
  }
  const exports: { default?: vue.Component } = {}
  runInNewContext(compiled, { exports, console, window: new EventTarget(), CustomEvent, require(name: string) {
    const value = modules[name]; if (value === undefined) throw new Error(`Unexpected import: ${name}`)
    return value && typeof value === 'object' && 'default' in value ? { __esModule: true, ...value } : value
  } })
  const root = node('root'), app = renderer.createApp(exports.default!)
  for (const tag of ['t-icon', 't-alert', 't-loading', 't-tooltip']) app.component(tag, passthrough)
  app.component('t-button', button); app.config.warnHandler = () => {}; app.mount(root)
  return { root, navigations, cards: () => all(root, el => el.props.role === 'link'), find: (key: string) => all(root, el => !!el.props[key])[0]!, close: () => app.unmount() }
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
  const source = all(card, el => el.type === 'button')[0]!
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
