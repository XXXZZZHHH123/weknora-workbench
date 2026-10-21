import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import * as policyHelpers from '../utils/resourcePermissions'

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
function fixture(options: { kind?: 'knowledge-base' | 'research-workbench'; legacy?: boolean; denied?: boolean; pending?: boolean; requested?: boolean; save?: () => Promise<any>; read?: () => Promise<any> } = {}) {
  const auth = vue.reactive({ selectedTenantId: 7, tenant: { id: 7 }, user: { id: 'owner', username: '主人' }, token: 'fixture-token' })
  const props = vue.reactive({ visible: true, kind: options.kind || 'knowledge-base', resourceId: 'resource', resourceName: '资料' })
  const calls: any[] = [], emitted: any[] = [], events: string[] = [], messages: string[] = []
  const modules: Record<string, any> = {
    vue: { ...vue, vModelSelect: {}, vModelText: {} }, 'tdesign-vue-next': { MessagePlugin: { success: (value: string) => messages.push(value) } },
    '@/stores/auth': { useAuthStore: () => auth }, '@/utils/resourcePermissions': policyHelpers,
    '@/views/workspaces/workspaceForm': { workspaceErrorMessage: (value: any) => value.message || 'error' },
    '@/components/settings/SettingDrawer.vue': { default: vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('drawer', attrs, slots.default?.()) }) },
    '@/api/tenant/members': { listMembers: async () => ({ success: true, data: { total: 3, members: [
      { user_id: 'owner', username: '主人', role: 'owner', status: 'active' },
      { user_id: 'reader', username: '只读成员', role: 'viewer', status: 'active' },
      { user_id: 'editor', username: '编辑成员', role: 'contributor', status: 'active' },
    ] } }) },
    '@/api/resourcePermissions': {
      getResourcePermissions: async (...args: any[]) => { calls.push(['get', ...args]); return options.read ? options.read() : { success: true, data: { policy: options.legacy ? null : { space_permission: 'none', members: [] }, owner_id: 'owner', can_manage: !options.denied, legacy: !!options.legacy, source_sharing_requested: !!options.requested, pending_workbenches: options.pending ? [{ id: 'requested-workbench', name: '请求的研究台', owner_id: 'editor', source_sharing_approved: false }] : [] } } },
      updateResourcePermissions: async (...args: any[]) => { calls.push(['put', ...JSON.parse(JSON.stringify(args))]); return options.save ? options.save() : { success: true } },
      requestWorkbenchSourceSharing: async (...args: any[]) => { calls.push(['request-source', ...args]); return { success: true } },
      updateWorkbenchSourceSharing: async (...args: any[]) => { calls.push(['approve-source', ...args]); return { success: true } },
    },
  }
  const { descriptor } = parse(readFileSync(new URL('./ResourcePermissionDrawer.vue', import.meta.url), 'utf8'))
  const code = ts.transpileModule(compileScript(descriptor, { id: 'permissions', inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const exports: { default?: vue.Component } = {}
  runInNewContext(code, { exports, CustomEvent, window: { dispatchEvent: (event: Event) => events.push(event.type) }, console,
    require(name: string) { assert.ok(name in modules, name); return modules[name] && 'default' in modules[name] ? { __esModule: true, ...modules[name] } : modules[name] } })
  const root = node('root'), app = renderer.createApp(vue.defineComponent({ setup: () => () => vue.h(exports.default!, { ...props, 'onUpdate:visible': (value: boolean) => { emitted.push(['visible', value]); props.visible = value }, onSaved: () => emitted.push(['saved']) }) }))
  const passthrough = vue.defineComponent({ setup: (_p, { attrs, slots }) => () => vue.h('stub', attrs, slots.default?.()) })
  for (const tag of ['t-button', 't-icon', 't-loading', 't-alert']) app.component(tag, passthrough)
  app.config.warnHandler = () => {}; app.mount(root)
  return { root, props, auth, calls, emitted, events, messages, close: () => app.unmount(), drawer: () => find(root, el => el.tag === 'drawer')[0]!, select: (label: string) => find(root, el => el.tag === 'select' && el.props['aria-label'] === label)[0]!, forms: () => find(root, el => el.tag === 'form') }
}
test('new knowledge base is private with no selected members, edit members can be granted independently', async t => {
  const f = fixture(); t.after(f.close); await flush()
  assert.match(text(f.root), /尚未授权指定成员/)
  assert.deepEqual(f.calls, [['get', 'knowledge-base', 'resource']])
  f.select('选择空间成员').props['onUpdate:modelValue']('editor'); await flush()
  f.forms()[0].props.onSubmit({ preventDefault() {} }); await flush()
  const member = f.select('编辑成员的权限'); assert.ok(member)
  member.props.onChange({ target: { value: 'editor' } }); await flush()
  f.drawer().props.onConfirm(); await flush()
  assert.deepEqual(f.calls[1], ['put', 'knowledge-base', 'resource', { space_permission: 'none', members: [{ user_id: 'editor', permission: 'editor' }] }])
  assert.deepEqual(f.emitted, [['saved'], ['visible', false]])
})
test('workbench permissions call their own endpoint and preserve legacy audience only on save', async t => {
  const f = fixture({ kind: 'research-workbench', legacy: true }); t.after(f.close); await flush()
  assert.match(text(f.root), /保留原有空间可见范围/)
  assert.equal(f.calls.length, 1)
  f.drawer().props.onConfirm(); await flush()
  assert.deepEqual(f.calls[1], ['put', 'research-workbench', 'resource', { space_permission: 'viewer', members: [] }])
})
test('space viewer edit grant is disabled and cannot be forced by firing a change', async t => {
  const f = fixture(); t.after(f.close); await flush()
  f.select('选择空间成员').props['onUpdate:modelValue']('reader'); await flush(); f.forms()[0].props.onSubmit({ preventDefault() {} }); await flush()
  const select = f.select('只读成员的权限')
  assert.ok(find(select, el => el.tag === 'option' && el.props.value === 'editor')[0]?.props.disabled)
  select.props.onChange({ target: { value: 'editor' } }); await flush(); f.drawer().props.onConfirm(); await flush()
  assert.equal(f.calls[1][3].members[0].permission, 'viewer')
})
test('non-owner cannot save even when triggering drawer confirm directly', async t => {
  const f = fixture({ denied: true }); t.after(f.close); await flush()
  assert.equal(f.drawer().props['confirm-disabled'], true)
  assert.equal(f.forms().length, 0)
  f.drawer().props.onConfirm(); await flush(); assert.equal(f.calls.length, 1)
})
test('save failure preserves the draft and does not announce success', async t => {
  const f = fixture({ save: async () => { throw new Error('权限已变化') } }); t.after(f.close); await flush()
  f.select('空间默认权限').props['onUpdate:modelValue']('editor'); await flush(); f.drawer().props.onConfirm(); await flush()
  assert.equal(f.props.visible, true); assert.equal(f.messages.length, 0)
  assert.match(find(f.root, el => el.props.theme === 'error')[0]?.props.message, /权限已变化/)
})
test('late save after identity switch cannot emit success or invalidate current user lists', async t => {
  const pending = deferred(), f = fixture({ save: () => pending.promise }); t.after(f.close); await flush()
  f.drawer().props.onConfirm(); await flush(); f.auth.user.id = 'other'; await flush()
  pending.resolve({ success: true }); await flush()
  assert.deepEqual(f.emitted, []); assert.deepEqual(f.events, []); assert.deepEqual(f.messages, [])
})
test('workbench owner requests source approval without changing resource permissions', async t => {
  const f = fixture({ kind: 'research-workbench' }); t.after(f.close); await flush()
  const button = find(f.root, el => el.tag === 'stub' && text(el) === '请求来源共享授权')[0]
  assert.ok(button); button.props.onClick(); await flush()
  assert.deepEqual(f.calls[1], ['request-source', 'resource'])
  assert.equal(f.calls.some(call => call[0] === 'put'), false)
  assert.match(text(f.root), /已请求，等待知识库主人授权/)
})
test('knowledge base owner can approve then revoke only explicitly requested workbench data', async t => {
  const f = fixture({ pending: true }); t.after(f.close); await flush()
  assert.match(text(f.root), /请求的研究台/)
  const button = (label: string) => find(f.root, el => el.tag === 'stub' && text(el) === label)[0]!
  button('允许共享').props.onClick(); await flush()
  assert.deepEqual(f.calls[1], ['approve-source', 'requested-workbench', true])
  button('撤销授权').props.onClick(); await flush()
  assert.deepEqual(f.calls[2], ['approve-source', 'requested-workbench', false])
  assert.equal(f.calls.some(call => call[0] === 'put'), false)
})
