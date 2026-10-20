import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as folders from '../knowledge/folderTree'
import * as directory from './knowledgeDirectory'
import * as forms from './workspaceForm'
import * as permission from '../../utils/kbPermission'
import type { KnowledgeFolderTree } from '../../api/knowledge-base'

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
const hasClass = (el: Host, name: string) => String(el.props.class || '').split(' ').includes(name)
const textOf = (el: Host): string => el.type === '#comment' ? '' : el.text + el.children.map(textOf).join('')
const flush = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await vue.nextTick() } }
const passthrough = vue.defineComponent({ setup: (_p, { slots }) => () => vue.h('stub', {}, [slots.default?.(), slots.actions?.()]) })

function component(file: string, modules: Record<string, unknown>, window?: EventTarget) {
  const { descriptor } = parse(readFileSync(new URL(file, import.meta.url), 'utf8'))
  const compiled = ts.transpileModule(compileScript(descriptor, { id: file, inlineTemplate: true }).content, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const exports: { default?: vue.Component } = {}
  runInNewContext(compiled, { exports, window, CustomEvent, TextEncoder, AbortController, console, require(name: string) {
    if (name === 'vue') return vue
    if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
    if (name in modules) {
      const value = modules[name]
      return value && typeof value === 'object' && 'default' in value ? { __esModule: true, ...value } : value
    }
    throw new Error(`Unexpected import: ${name}`)
  } })
  return exports.default!
}

const folderComponent = component('../knowledge/components/KbFolderTree.vue', { '../folderTree': folders, './KnowledgeFolderActions.vue': { default: passthrough } })
const tree: KnowledgeFolderTree = { root_document_count: 0, total_document_count: 1, folders: [{ path: '资料', name: '资料', document_count: 0, total_count: 1,
  children: [{ path: '资料/周报', name: '周报', document_count: 0, total_count: 1,
    children: [{ path: '资料/周报/2026', name: '2026', document_count: 1, total_count: 1 }] }] }] }
const file = (id = 'real-document', folder = '资料/周报/2026', kb = 'kb-one') => ({ id, knowledge_base_id: kb, folder_path: folder, file_name: '日报.xlsx', file_type: 'xlsx', type: 'file' })
const payload = (items: unknown[], total = items.length, page = 1) => ({ success: true, data: items, total, page, page_size: 50 })

function fixture(options: { tree?: KnowledgeFolderTree; query?: Record<string, string>; files?: (id: string, params: any) => Promise<any>; folders?: () => Promise<any>; permission?: string; admin?: boolean; creator?: string } = {}) {
  const route = vue.reactive({ params: { kbId: 'kb-one' }, query: options.query || {}, name: 'knowledgeBaseDetail' })
  const auth = vue.reactive({ selectedTenantId: 1, tenant: { id: 1 }, user: { id: 'user-one' }, hasRole: () => !!options.admin })
  const org = vue.reactive({ sharedKnowledgeBases: [], getKBPermission: () => options.permission || null, canEditKB: () => permission.permissionCanEditKB(options.permission) })
  const calls: Array<{ id: string; params: any; signal?: AbortSignal }> = [], navigations: any[] = [], libraryLoads: string[] = [], events = new EventTarget()
  const ui = vue.reactive({ sidebarCollapsed: false, sidebarDisplayWidth: 260, sidebarResizing: false, toggleSidebar() {}, resizeSidebar() {} })
  const menu = component('./KnowledgeWorkspaceMenu.vue', {
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: (target: any) => { navigations.push(target); return Promise.resolve() }, replace: (target: any) => { navigations.push(target); return Promise.resolve() } }) },
    'tdesign-vue-next': { MessagePlugin: { success() {}, error() {} } },
    '@/stores/auth': { useAuthStore: () => auth }, '@/stores/ui': { useUIStore: () => ui }, '@/stores/organization': { useOrganizationStore: () => org },
    '@/api/knowledge-base': {
      getKnowledgeBaseById: async (id: string) => { libraryLoads.push(id); return { data: { id, name: '测试资料库', type: 'document', tenant_id: 1, creator_id: options.creator ?? 'user-one' } } },
      listKnowledgeFolders: async () => options.folders ? options.folders() : ({ data: options.tree || tree }),
      listKnowledgeFiles: async (id: string, params: any, request?: { signal?: AbortSignal }) => { calls.push({ id, params, signal: request?.signal }); return options.files ? options.files(id, params) : payload([file()]) },
      createKnowledgeFolder: async () => ({}), deleteKnowledgeFolder: async () => ({}), renameKnowledgeFolder: async () => ({}),
    },
    '@/utils/kbPermission': permission, '@/components/BrandWordmark.vue': { default: passthrough }, '@/components/UserMenu.vue': { default: passthrough },
    '@/components/WorkspaceNavigationNode.vue': { default: passthrough }, '@/components/PanelResizeHandle.vue': { default: passthrough },
    '@/utils/sidebarWidth': { SIDEBAR_COLLAPSED_WIDTH: 60, SIDEBAR_MAX_WIDTH: 420 },
    '@/views/knowledge/components/KbFolderTree.vue': { default: folderComponent }, '@/views/knowledge/folderTree': folders,
    './knowledgeDirectory': directory, './workspaceForm': forms,
  }, events)
  const root = node('root'), app = renderer.createApp(menu)
  for (const tag of ['t-icon', 't-tooltip', 't-popup', 't-dialog', 't-input', 't-alert', 't-skeleton']) app.component(tag, passthrough)
  app.config.warnHandler = () => {}
  app.mount(root)
  const elements = (name: string) => all(root, el => hasClass(el, name))
  const folder = (path: string) => elements('kb-folder-row').find(el => el.props['data-folder-path'] === path)
  const click = async (el?: Host) => { assert.ok(el, 'missing clickable element'); el.props.onClick({ stopPropagation() {}, preventDefault() {} }); await flush() }
  const expand = async (path: string) => { const row = folder(path); assert.ok(row, `missing folder ${path}`); await click(all(row, el => hasClass(el, 'kb-folder-row__toggle'))[0]) }
  return { root, calls, navigations, libraryLoads, route, auth, events, elements, expand, click, close: () => app.unmount() }
}

test('the navigation reaches real files under the lowest folder with one scoped page request', async t => {
  const f = fixture(); t.after(f.close); await flush()
  assert.equal(f.calls.length, 0, 'closed directories do not prefetch files')
  await f.expand('资料'); await f.expand('资料/周报'); await f.expand('资料/周报/2026')
  assert.equal(f.calls.length, 1)
  assert.equal(f.calls[0]!.id, 'kb-one')
  assert.equal(f.calls[0]!.params.folder_path, '资料/周报/2026')
  assert.equal(f.calls[0]!.params.folder_recursive, undefined)
  assert.equal(f.calls[0]!.params.page_size, 50)
  const leaf = f.elements('kb-folder-file')[0]; assert.ok(leaf)
  assert.equal(leaf.props['data-knowledge-id'], 'real-document')
  assert.equal(textOf(leaf), '日报.xlsx')
  await f.click(leaf)
  assert.equal(f.navigations.at(-1).path, '/platform/knowledge-bases/kb-one')
  assert.deepEqual({ ...f.navigations.at(-1).query }, { folder: '资料/周报/2026', knowledge_id: 'real-document' })
})

test('root files, empty folders and literal hostile file names render at their own depth', async t => {
  const f = fixture({ tree: { root_document_count: 1, total_document_count: 1, folders: [{ path: '空目录', name: '空目录', document_count: 0, total_count: 0 }] },
    files: async () => payload([{ ...file('root-file', ''), file_name: '<img src=x onerror=alert(1)>.xlsx' }]) })
  t.after(f.close); await flush()
  assert.equal(f.calls.length, 1); assert.equal(f.calls[0]!.params.folder_path, '')
  assert.equal(f.elements('kb-folder-file').length, 1)
  assert.equal(textOf(f.elements('kb-folder-file')[0]!), '<img src=x onerror=alert(1)>.xlsx')
  assert.equal(all(f.root, el => el.type === 'img').length, 0)
  assert.ok(f.elements('kb-folder-row').some(el => textOf(el).includes('空目录')))
})

for (const path of ['constructor', '__proto__']) {
  test(`valid folder name ${path} cannot collide with page dictionary properties`, async t => {
    const f = fixture({ tree: { root_document_count: 0, total_document_count: 1, folders: [{ path, name: path, document_count: 1, total_count: 1 }] },
      files: async () => payload([file('prototype-safe', path)]) })
    t.after(f.close); await flush(); await f.expand(path)
    assert.equal(f.calls.length, 1); assert.equal(f.elements('kb-folder-file').length, 1)
  })
}

test('paging loads explicit later pages, deduplicates real ids and preserves existing files on failure', async t => {
  let failSecond = true
  const first = Array.from({ length: 50 }, (_, i) => file(`file-${i}`, ''))
  const f = fixture({ tree: { root_document_count: 51, total_document_count: 51, folders: [] }, files: async (_id, params) => {
    if (params.page === 1) return payload(first, 51)
    if (failSecond) throw new Error('目录加载失败')
    return payload([first[0], file('file-last', '')], 51, 2)
  } })
  t.after(f.close); await flush(); assert.equal(f.elements('kb-folder-file').length, 50)
  await f.click(f.elements('kb-folder-files-more')[0]); assert.equal(f.calls[1]!.params.page, 2)
  assert.equal(f.elements('kb-folder-file').length, 50)
  assert.ok(textOf(f.elements('kb-folder-files-status')[0]!).includes('目录加载失败'))
  failSecond = false; await f.click(f.elements('kb-folder-files-retry')[0])
  assert.equal(f.calls[2]!.params.page, 2); assert.equal(f.elements('kb-folder-file').length, 51)
  assert.equal(f.elements('kb-folder-files-more').length, 0)
})

test('wrong knowledge-base or folder evidence cannot become a leaf and has a retry affordance', async t => {
  const f = fixture({ query: { folder: '资料/周报/2026' }, files: async () => payload([file('other', '资料/周报/2026', 'kb-two')]) })
  t.after(f.close); await flush()
  assert.equal(f.elements('kb-folder-file').length, 0)
  assert.equal(f.elements('kb-folder-files-retry').length, 1)
})

for (const switchKind of ['knowledge-base', 'actor'] as const) {
  test(`late file responses after a ${switchKind} switch cannot populate the current tree`, async t => {
    let resolveOld!: (response: any) => void
    const f = fixture({ tree: { root_document_count: 1, total_document_count: 1, folders: [] }, files: async () => f.calls.length === 1
      ? new Promise(resolve => { resolveOld = resolve }) : payload([file('current', '', switchKind === 'knowledge-base' ? 'kb-two' : 'kb-one')]) })
    t.after(f.close); await flush(); assert.equal(f.calls.length, 1)
    if (switchKind === 'knowledge-base') f.route.params.kbId = 'kb-two'
    else f.auth.user.id = 'user-two'
    await flush(); assert.equal(f.calls.length, 2)
    assert.equal(f.calls[0]!.signal?.aborted, true)
    resolveOld(payload([file('old-file', '')])); await flush()
    assert.deepEqual(f.elements('kb-folder-file').map(el => el.props['data-knowledge-id']), ['current'])
  })
}

test('directory page requests run at most two at a time even along a selected deep path', async t => {
  const nested = structuredClone(tree)
  nested.root_document_count = 1; nested.total_document_count = 4
  nested.folders[0]!.document_count = 1; nested.folders[0]!.children![0]!.document_count = 1
  const pending: Array<{ path: string; resolve: (response: any) => void }> = []
  const f = fixture({ tree: nested, query: { folder: '资料/周报/2026' }, files: async (_id, params) => new Promise(resolve => { pending.push({ path: params.folder_path, resolve }) }) })
  t.after(f.close); await flush(); assert.equal(f.calls.length, 2)
  pending[0]!.resolve(payload([file('first', pending[0]!.path)])); await flush(); assert.equal(f.calls.length, 3)
  pending[1]!.resolve(payload([file('second', pending[1]!.path)])); await flush(); assert.equal(f.calls.length, 4)
  pending[2]!.resolve(payload([file('third', pending[2]!.path)])); pending[3]!.resolve(payload([file('fourth', pending[3]!.path)])); await flush()
  assert.equal(f.elements('kb-folder-file').length, 4)
})

test('a visible cached file cannot navigate immediately after the actor changes', async t => {
  const f = fixture({ tree: { root_document_count: 1, total_document_count: 1, folders: [] }, files: async () => payload([file('root', '')]) })
  t.after(f.close); await flush(); const leaf = f.elements('kb-folder-file')[0]; assert.ok(leaf)
  f.auth.user.id = 'user-two'
  leaf.props.onClick({ stopPropagation() {}, preventDefault() {} })
  assert.equal(f.navigations.length, 0)
})

test('wrong folder metadata and malformed pages show errors rather than stray files', async t => {
  let response: any = payload([file('outside', '资料/其他')])
  const f = fixture({ query: { folder: '资料/周报/2026' }, files: async () => response })
  t.after(f.close); await flush(); assert.equal(f.elements('kb-folder-file').length, 0)
  response = { data: 'not-an-array', total: 1 }
  await f.click(f.elements('kb-folder-files-retry')[0]); assert.equal(f.elements('kb-folder-file').length, 0)
  assert.equal(f.elements('kb-folder-files-retry').length, 1)
})

test('matching directory mutations refresh leaves; other knowledge-base events do not', async t => {
  const f = fixture({ tree: { root_document_count: 1, total_document_count: 1, folders: [] }, files: async () => payload([file(`file-${f.calls.length}`, '')]) })
  t.after(f.close); await flush(); assert.equal(f.calls.length, 1)
  f.events.dispatchEvent(new CustomEvent(directory.KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: 'other-kb' } })); await flush()
  assert.equal(f.calls.length, 1)
  f.events.dispatchEvent(new CustomEvent(directory.KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: 'kb-one' } })); await flush()
  assert.equal(f.calls.length, 2)
  assert.deepEqual(f.elements('kb-folder-file').map(el => el.props['data-knowledge-id']), ['file-2'])
})

test('matching KB rename refreshes sidebar metadata and delete leaves its scoped route', async t => {
  const f = fixture(); t.after(f.close); await flush(); assert.equal(f.libraryLoads.length, 1)
  f.events.dispatchEvent(new CustomEvent('weknora:knowledge-base-changed', { detail: { id: 'other-kb', deleted: true } })); await flush()
  assert.equal(f.navigations.length, 0); assert.equal(f.libraryLoads.length, 1)
  f.events.dispatchEvent(new CustomEvent('weknora:knowledge-base-changed', { detail: { id: 'kb-one', name: 'Changed', deleted: false } })); await flush()
  assert.equal(f.libraryLoads.length, 2)
  f.events.dispatchEvent(new CustomEvent('weknora:knowledge-base-changed', { detail: { id: 'kb-one', deleted: true } })); await flush()
  assert.equal(f.navigations.at(-1), '/platform/knowledge-bases')
})

test('same-scope refresh keeps folder actions mounted while fresh partial-deletion counts load', async t => {
  let calls = 0, complete!: (response: any) => void
  const f = fixture({ folders: async () => ++calls === 1 ? { data: tree } : new Promise(resolve => { complete = resolve }) })
  t.after(f.close); await flush()
  const row = f.elements('kb-folder-row').find(el => el.props['data-folder-path'] === '资料'); assert.ok(row)
  f.events.dispatchEvent(new CustomEvent(directory.KNOWLEDGE_DIRECTORY_EVENT, { detail: { kbId: 'kb-one' } })); await flush()
  assert.equal(calls, 2); assert.equal(f.elements('kb-folder-row').find(el => el.props['data-folder-path'] === '资料'), row)
  complete({ data: tree }); await flush()
  assert.equal(f.elements('kb-folder-row').find(el => el.props['data-folder-path'] === '资料'), row)
})

for (const entry of [{ permission: 'viewer', admin: true, expected: false }, { permission: 'editor', admin: false, expected: true }, { creator: '', admin: false, expected: false }, { creator: '', admin: true, expected: true }]) {
  test(`folder affordance honors resolved grants and legacy roles: ${JSON.stringify(entry)}`, async t => {
    const f = fixture(entry); t.after(f.close); await flush()
    assert.equal(all(f.root, el => el.props['aria-label'] === '新建文件夹').length > 0, entry.expected)
  })
}
