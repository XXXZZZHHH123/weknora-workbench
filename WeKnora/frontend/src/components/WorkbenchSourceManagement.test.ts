import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as sourceSharing from '../utils/sourceSharingModules'
import * as sourceSelection from '../views/research/workbenchDataSources'
import * as librarySelection from '../views/workspaces/workspaceKnowledgeBases'
import * as workspaceForm from '../views/workspaces/workspaceForm'

// Use the same in-memory Vue host as ResourcePermissionDrawer tests. Only
// transport/stores and TDesign chrome are substituted; the SFCs and picker run.
type Host = { tag: string; props: Record<string, any>; children: Host[]; text: string; parent?: Host }
const node = (tag: string): Host => ({ tag, props: {}, children: [], text: '' })
const renderer = vue.createRenderer<Host, Host>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: text => ({ ...node('#comment'), text }),
  patchProp: (el, key, _old, value) => { el.props[key] = value }, setText: (el, text) => { el.text = text },
  setElementText: (el, text) => { el.text = text; el.children = [] }, parentNode: el => el.parent || null,
  nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] || null,
  insert(el, parent, anchor) { if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1); el.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, el) },
  remove(el) { el.parent?.children.splice(el.parent.children.indexOf(el), 1); el.parent = undefined },
})
const find = (el: Host, predicate: (el: Host) => boolean): Host[] => [...(predicate(el) ? [el] : []), ...el.children.flatMap(child => find(child, predicate))]
const text = (el: Host): string => el.tag === '#comment' ? '' : el.text + el.children.map(text).join('')
const flush = async () => { for (let i = 0; i < 12; i++) { await Promise.resolve(); await vue.nextTick() } }
const deferred = () => { let resolve!: (value: any) => void; const promise = new Promise<any>(r => { resolve = r }); return { promise, resolve } }
const source = (id: string, manage = true) => ({ id, name: id, source_type: 'knowledge_base', source_id: id, status: 'active', can_read: true, can_manage_source: manage, delegated: false, view_modules: [], sharing_approved: false, sharing_modules: [], sharing_requested: false })

function fixture(kind: 'permissions' | 'dialog', options: { read?: (...args: any[]) => Promise<any>; save?: (...args: any[]) => Promise<any> } = {}) {
  const auth = vue.reactive({ selectedTenantId: 7, tenant: { id: 7 }, user: { id: 'owner' }, token: 'fixture-token' })
  const props = vue.reactive({ visible: true, workbenchId: 'workbench' })
  const calls: any[][] = [], emitted: any[][] = [], events: string[] = []
  const modules: Record<string, any> = {
    vue: { ...vue, vModelCheckbox: { mounted(el: Host, binding: any) { el.props.modelValue = binding.value }, updated(el: Host, binding: any) { el.props.modelValue = binding.value } } },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/stores/chatResources': { useChatResourcesStore: () => ({ rawKnowledgeBases: [{ id: 'a', name: 'a' }, { id: 'b', name: 'b' }], ensureKnowledgeBases: async () => {} }) },
    '@/stores/organization': { useOrganizationStore: () => ({ sharedKnowledgeBases: [], fetchSharedKnowledgeBases: async () => {} }) },
    '@/utils/sourceSharingModules': sourceSharing,
    '@/views/research/workbenchDataSources': sourceSelection,
    './workspaceKnowledgeBases': librarySelection,
    './workspaceForm': workspaceForm, '@/views/workspaces/workspaceForm': workspaceForm,
    '@/api/workspaces': {
      getResearchWorkbenchSources: async (...args: any[]) => { calls.push(['read', ...args]); return options.read ? options.read(...args) : { success: true, data: { sources_revision: 3, sources: [source('a'), source('b', false)] } } },
      listCentralDatasets: async () => ({ success: true, data: [] }),
      requestResearchWorkbenchSourceSharing: async (...args: any[]) => { calls.push(['request', ...args]); return { success: true, data: { requested: true } } },
      updateResearchWorkbenchSourceSharing: async (...args: any[]) => { calls.push(['approve', ...JSON.parse(JSON.stringify(args))]); return options.save ? options.save(...args) : { success: true, data: { approved: args[2], source_sharing_modules: args[3] } } },
      updateResearchWorkbenchSources: async (...args: any[]) => { calls.push(['replace', ...JSON.parse(JSON.stringify(args))]); return options.save ? options.save(...args) : { success: true, data: { sources_revision: 4, sources: args[2].map((item: any) => ({ ...source(item.source_id), ...item })) } } },
    },
  }
  function component(path: string): vue.Component {
    const { descriptor } = parse(readFileSync(new URL(path, import.meta.url), 'utf8'))
    const code = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
    const exports: { default?: vue.Component } = {}
    runInNewContext(code, { exports, CustomEvent, console, window: { dispatchEvent: (event: Event) => events.push(event.type) },
      require(name: string) { assert.ok(name in modules, name); return modules[name] && 'default' in modules[name] ? { __esModule: true, ...modules[name] } : modules[name] } })
    return exports.default!
  }
  modules['./WorkbenchSourcePicker.vue'] = { default: component('../views/workspaces/WorkbenchSourcePicker.vue') }
  const target = component(kind === 'permissions' ? './WorkbenchSourcePermissions.vue' : '../views/workspaces/WorkbenchSourcesDialog.vue')
  const root = node('root'), app = renderer.createApp(vue.defineComponent({ setup: () => () => vue.h(target, { ...props,
    onSaved: () => emitted.push(['saved']), 'onUpdate:visible': (value: boolean) => emitted.push(['visible', value]),
    'onBusy-change': (value: boolean) => emitted.push(['busy', value]),
  }) }))
  const passthrough = vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('stub', attrs, slots.default?.()) })
  for (const tag of ['t-dialog', 't-button', 't-loading', 't-alert', 't-icon']) app.component(tag, passthrough)
  app.config.warnHandler = () => {}; app.mount(root)
  const rows = () => find(root, el => el.tag === 'details')
  const button = (row: Host, label: string) => find(row, el => el.tag === 'stub' && text(el) === label)[0]!
  return { root, props, auth, calls, emitted, events, rows, button, close: () => app.unmount(),
    confirm: () => find(root, el => el.tag === 'stub' && el.props.header === '数据来源')[0]!.props.onConfirm(),
    choose: (id: string, checked: boolean) => {
      const label = find(root, el => el.tag === 'label' && find(el, item => item.tag === 'span' && text(item) === id).length > 0)[0]!
      find(label, el => el.tag === 'input')[0]!.props.onChange({ target: { checked } })
    } }
}

test('approving, requesting and revoking remain separate and affect only the selected source', async t => {
  const f = fixture('permissions'); t.after(f.close); await flush()
  let [a, b] = f.rows()
  find(a!, el => el.tag === 'input')[0]!.props['onUpdate:modelValue'](true); await flush()
  find(a!, el => el.tag === 'input' && el.props.value === 'diagram')[0]!.props['onUpdate:modelValue'](['diagram']); await flush()
  f.button(a!, '保存此来源授权').props.onClick(); await flush()
  f.button(b!, '请求来源主人授权').props.onClick(); await flush()
  find(a!, el => el.tag === 'input')[0]!.props['onUpdate:modelValue'](false); await flush()
  f.button(a!, '保存此来源授权').props.onClick(); await flush()
  assert.deepEqual(f.calls.filter(call => call[0] !== 'read'), [
    ['approve', 'workbench', 'a', true, ['diagram']], ['request', 'workbench', 'b'], ['approve', 'workbench', 'a', false, []],
  ])
  assert.match(text(b!), /尚未批准/)
  assert.equal(f.calls.some(call => call[0] === 'replace'), false)
})

test('identity changes release source busy state and ignore a previous account approval response', async t => {
  const pending = deferred(); let attempts = 0
  const f = fixture('permissions', { save: async (...args) => ++attempts === 1 ? pending.promise : { success: true, data: { approved: args[2], source_sharing_modules: args[3] } } }); t.after(f.close); await flush()
  f.button(f.rows()[0]!, '保存此来源授权').props.onClick(); await flush()
  assert.deepEqual(f.emitted, [['busy', true]])
  f.auth.user.id = 'other'; f.props.workbenchId = 'other-workbench'; await flush()
  assert.deepEqual(f.emitted.slice(0, 2), [['busy', true], ['busy', false]])
  pending.resolve({ success: true, data: { approved: false, source_sharing_modules: [] } }); await flush()
  assert.deepEqual(f.events, []); assert.equal(find(f.root, el => el.props.role === 'status').length, 0)
  f.button(f.rows()[0]!, '保存此来源授权').props.onClick(); await flush()
  assert.deepEqual(f.calls.filter(call => call[0] === 'approve')[1], ['approve', 'other-workbench', 'a', false, []])
})

test('source revision conflict preserves the actual picker draft and never reports success', async t => {
  const f = fixture('dialog', { save: async () => { throw new Error('来源版本冲突，请重新读取') } }); t.after(f.close); await flush()
  f.choose('b', false); await flush(); f.confirm(); await flush()
  assert.deepEqual(f.calls.filter(call => call[0] === 'replace'), [['replace', 'workbench', 3, [{ source_type: 'knowledge_base', source_id: 'a' }]]])
  assert.match(find(f.root, el => el.props.theme === 'error')[0]!.props.message, /版本冲突/)
  assert.deepEqual(f.emitted, []); assert.deepEqual(f.events, [])
  f.confirm(); await flush()
  assert.deepEqual(f.calls.filter(call => call[0] === 'replace')[1], f.calls.filter(call => call[0] === 'replace')[0])
})

test('late source saves and reads cannot close or populate a different account workbench', async t => {
  const pendingSave = deferred(), lateRead = deferred(); let reads = 0
  const f = fixture('dialog', { read: async () => ++reads === 2 ? lateRead.promise : { success: true, data: { sources_revision: reads, sources: [source(reads === 1 ? 'a' : 'b')] } }, save: () => pendingSave.promise }); t.after(f.close); await flush()
  f.confirm(); await flush()
  f.auth.user.id = 'second'; await flush()
  f.props.workbenchId = 'current'; await flush()
  lateRead.resolve({ success: true, data: { sources_revision: 99, sources: [source('a')] } })
  pendingSave.resolve({ success: true, data: { sources_revision: 2, sources: [source('a')] } }); await flush()
  assert.deepEqual(f.emitted, []); assert.deepEqual(f.events, [])
  const labels = find(f.root, el => el.tag === 'label')
  assert.equal(find(labels.find(el => text(el).includes('b'))!, el => el.tag === 'input')[0]!.props.checked, true)
  assert.equal(find(labels.find(el => text(el).includes('a'))!, el => el.tag === 'input')[0]!.props.checked, false)
})
