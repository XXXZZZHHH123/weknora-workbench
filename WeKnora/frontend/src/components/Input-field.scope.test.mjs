import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as Vue from 'vue'

const source = readFileSync(process.env.SCOPED_INPUT_SOURCE || new URL('./Input-field.vue', import.meta.url), 'utf8')
const descriptor = parse(source).descriptor
const compiled = compileScript(descriptor, { id: 'scope-test', inlineTemplate: true })
const code = ts.transpileModule(compiled.content.replaceAll('import.meta.url', "'file:///Input-field.vue'"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

// The actual SFC setup and template run in Vue's renderer. Only browser/remote
// boundaries and child popup rendering are replaced; no completion API is used.
function mountComposer(overrides = {}, remote = {}) {
  const effects = [], sent = [], requests = []
  const models = remote.models ?? [{ id: 'free-a', name: 'Free A' }, { id: 'free-b', name: 'Free B' }]
  const settings = Vue.reactive({
    selectedAgentId: 'foreign-agent', selectedAgentSourceTenantId: 'foreign-tenant',
    isAgentStreamMode: true, isAgentEnabled: true, isWebSearchEnabled: true,
    conversationModels: { selectedChatModelId: 'free-a' }, reasoningEffortOverride: '',
    settings: { selectedKnowledgeBases: ['foreign-kb'], selectedFiles: ['foreign-file'], selectedFileKbMap: { 'foreign-file': 'foreign-kb' } },
    updateConversationModels(value) { effects.push(['settings-model', value]); Object.assign(this.conversationModels, value) },
    addKnowledgeBase(id) { effects.push(['settings-kb', id]) },
    addFile(id) { effects.push(['settings-file', id]) },
    selectKnowledgeBases(ids) { effects.push(['settings-kbs', ids]) },
    removeKnowledgeBase(id) { effects.push(['settings-remove', id]) },
  })
  const resources = Vue.reactive({
    agents: [], disabledOwnAgentIds: [], allModels: models, chatModels: models,
    webSearchProviders: [], validKnowledgeBases: [{ id: 'bound-kb', name: 'Bound KB' }, { id: 'foreign-kb', name: 'Foreign KB' }],
    ensureKnowledgeBases: async () => effects.push(['load-global-kbs']),
    ensureChatModels: async () => {}, isLoaded: () => true,
    fetchKnowledgeBaseById: async id => ({ id, name: id === 'bound-kb' ? 'Bound KB' : 'Foreign KB', knowledge_count: 2 }),
  })
  const api = {
    getKnowledgeBaseById: async id => { requests.push(['kb', id]); return { data: { id, name: 'Bound KB', knowledge_count: 2 } } },
    listKnowledgeFiles: async (id, params) => {
      requests.push(['files', id, params]);
      if (remote.files) return remote.files(id, params)
      return { data: [
        { id: 'bound-file', title: 'Real source', knowledge_base_id: 'bound-kb' },
        { id: 'injected-file', title: 'Foreign source', knowledge_base_id: 'foreign-kb' },
      ], total: 2 }
    },
    listKnowledgeTags: async id => { requests.push(['tags', id]); return { data: [] } },
    searchKnowledge: async () => { throw new Error('scope used a global file search') },
    batchQueryKnowledge: async () => { throw new Error('scope inherited global files') },
  }
  const slotsOnly = { setup(_props, { slots }) { return () => slots.default?.() } }
  const silent = { render: () => null }
  let emitMention
  const mentionPopup = {
    props: ['visible', 'items'], emits: ['select'],
    setup(props, { emit }) { emitMention = item => emit('select', item); return () => props.visible ? Vue.h('div', { class: 'mention-fixture' },
      props.items.map(item => Vue.h('button', { class: `mention-${item.id}`, onClick: () => emit('select', item) }, item.name))) : null },
  }
  const textarea = {
    props: ['modelValue'], emits: ['update:modelValue'],
    setup(props, { emit, expose }) {
      const el = { isConnected: true, selectionStart: 0, selectionEnd: 0, focus() {}, blur() {}, getBoundingClientRect: () => ({ top: 500, bottom: 550, left: 30, right: 350, width: 320, height: 50 }) }
      expose({ $el: { querySelector: () => el }, focus: el.focus })
      return () => Vue.h('textarea', { value: props.modelValue, onInput: event => emit('update:modelValue', event.target.value) })
    },
  }
  const modules = {
    vue: Vue,
    pinia: { storeToRefs: store => Object.fromEntries(Object.keys(store).map(key => [key, Vue.toRef(store, key)])) },
    'vue-router': { useRoute: () => ({ path: '/platform/research/workbench', params: { kbId: 'foreign-route-kb' } }), useRouter: () => ({ push() {} }), onBeforeRouteUpdate() {} },
    'tdesign-vue-next': { MessagePlugin: Object.fromEntries(['warning', 'error', 'info', 'success'].map(level => [level, text => effects.push([level, text])])) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: Vue.ref('zh-CN') }) },
    '@/stores/settings': { useSettingsStore: () => settings },
    '@/stores/browserConnection': { useBrowserConnectionStore: () => ({ watchStatus: () => effects.push(['watch-browser']), unwatchStatus: () => effects.push(['unwatch-browser']) }) },
    '@/stores/ui': { useUIStore: () => ({}) },
    '@/stores/menu': { useMenuStore: () => ({ consumePrefillQuery() { throw new Error('scope consumed the full-page draft') } }) },
    '@/stores/organization': { useOrganizationStore: () => ({ sharedKnowledgeBases: [], sharedAgents: [] }) },
    '@/stores/chatResources': { useChatResourcesStore: () => resources },
    '@/stores/editorResources': { useEditorResourcesStore: () => ({}) },
    '@/stores/deploymentCapabilities': { useDeploymentCapabilitiesStore: () => ({}) },
    '@/api/knowledge-base': api,
    '@/api/mcp-service': { listMCPServices: async () => { throw new Error('scope loaded tools') } },
    '@/api/chat': { stopSession: async (...args) => { effects.push(['stop', ...args]); if (remote.stopFails) throw new Error('isolated stop unavailable') } },
    '@/api/agent': { BUILTIN_QUICK_ANSWER_ID: 'builtin-quick-answer', BUILTIN_SMART_REASONING_ID: 'builtin-smart-reasoning' },
    '@/utils/chatSubmitShortcut': { chatSubmitShortcut: () => 'after' },
    '@/utils/zoom': { getRootZoom: () => 1, rectToCssPx: rect => rect, cssViewportSize: () => ({ width: 1000, height: 800 }) },
    '@/utils/caret': { getCaretCoordinates: () => ({ top: 0, left: 0 }) },
    '@/utils/contextWindow': { formatContextWindow: () => '', isDefaultContextWindow: () => true, effectiveContextWindow: () => 0 },
    '@/utils/tool-capabilities': { deriveKbFilterForAgent: () => null, kbSatisfiesAgentRequirements: () => true, toolsConsumeFiles: () => true },
    '@/utils/agentWebSearch': { isAgentWebSearchEnabled: () => false, isAgentWebSearchReady: () => false, isTenantWebSearchReady: () => false },
    '@/utils/agent-readiness': { getAgentNotReadyReasonKeys: () => [] },
    '@/utils/format-list': { formatLocalizedList: value => value },
    '@/utils/skillTarget': {}, '@/config/toolbox': {}, '@/types/mention': { SKILL_ICON: 'tools' },
    '@/utils/reasoningEffort': { supportedLevels: () => [], levelFromLegacy: () => '', clampLevel: () => '' },
    './MentionSelector.vue': { default: mentionPopup },
    './KnowledgeBaseSelector.vue': { default: silent }, './AgentSelector.vue': { default: silent },
    './AttachmentUpload.vue': { default: silent }, '@/components/icons/BrowserIcon.vue': { default: silent },
  }
  const exports = {}
  const document = { addEventListener() {}, removeEventListener() {}, activeElement: null, querySelector: () => null }
  const storageWrites = []
  vm.runInNewContext(code, {
    exports, require: name => { assert.ok(name in modules, `unmocked remote/child boundary: ${name}`); return name.endsWith('.vue') ? { __esModule: true, ...modules[name] } : modules[name] },
    console: { log() {}, error() {}, warn() {} }, navigator: { platform: 'Linux' }, document, URL, setTimeout,
    HTMLTextAreaElement: class {}, HTMLElement: class {}, requestAnimationFrame: callback => setImmediate(callback),
    window: { addEventListener() {}, removeEventListener() {} },
    localStorage: { getItem: () => 'free-a', setItem: (...args) => storageWrites.push(args), removeItem: (...args) => storageWrites.push(args) },
  })
  function node(type, text = '') { return { type, text, props: {}, children: [], parent: null, getBoundingClientRect: () => ({ top: 500, bottom: 550, left: 30, right: 350, width: 320, height: 50 }), contains: () => false } }
  const body = node('body'), container = node('root')
  const renderer = Vue.createRenderer({
    createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
    setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text; node.children = [] },
    patchProp: (node, key, _prev, value) => { node.props[key] = value },
    insert(child, parent, anchor = null) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, child) },
    remove(child) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] || null,
    querySelector: () => body,
  })
  const props = Vue.reactive({ embeddedMode: true, knowledgeScope: { kbId: 'bound-kb', kbName: 'Bound KB', modelId: 'free-a' },
    sessionId: 'research-session', onSendMsg: (...args) => sent.push(args), ...overrides })
  const app = renderer.createApp({ render: () => Vue.h(exports.default, props) })
  app.config.globalProperties.$t = key => key
  app.component('t-tooltip', slotsOnly); app.component('t-popup', slotsOnly)
  app.component('t-icon', silent); app.component('t-textarea', textarea)
  app.config.warnHandler = () => {}
  app.mount(container)
  const all = () => { const result = []; const walk = item => { result.push(item); item.children.forEach(walk) }; walk(container); walk(body); return result }
  const find = name => all().find(node => String(node.props.class || '').split(' ').includes(name))
  const flush = async () => { await new Promise(setImmediate); await Vue.nextTick(); await new Promise(setImmediate) }
  const enter = async text => {
    const inputHandler = all().find(n => n.type === 'textarea').props.onInput
    for (const handler of Array.isArray(inputHandler) ? inputHandler : [inputHandler]) handler({ target: { value: text } })
    await flush()
  }
  return { app, find, all, effects, sent, requests, settings, storageWrites, flush, props, enter, emitMention: item => emitMention(item) }
}

test('the real scoped input renders the same inline model, @ and send controls, without global addons', async () => {
  const c = mountComposer()
  try {
    await c.flush()
    assert.ok(c.find('kb-btn'), 'the real @ button is hidden')
    assert.ok(c.find('model-selector-trigger'), 'the inline model picker is hidden')
    assert.ok(c.find('send-btn'))
    assert.equal(c.find('agent-mode-btn'), undefined)
    assert.equal(c.find('attachment-upload-btn'), undefined)
    assert.equal(c.find('browser-source-btn'), undefined)
    assert.deepEqual(c.effects, [], 'opening the research input mutated global state')
  } finally { c.app.unmount() }
})

test('real @ and model actions stay local and send only documents from the bound KB', async () => {
  const c = mountComposer()
  const before = JSON.stringify(c.settings)
  try {
    await c.flush()
    assert.ok(c.find('kb-btn'), 'the scoped @ control must be available')
    c.find('kb-btn').props.onMousedown({ preventDefault() {} })
    await c.flush()
    assert.ok(c.find('mention-bound-file'), `the scoped document is missing: ${JSON.stringify({ requests: c.requests, effects: c.effects })}`)
    assert.equal(c.find('mention-injected-file'), undefined)
    assert.equal(c.find('mention-foreign-kb'), undefined)
    c.find('mention-bound-file').props.onClick()
    await c.flush()
    c.find('model-selector-trigger').props.onClick({ stopPropagation() {} })
    await c.flush()
    const text = n => n.text + n.children.map(text).join('')
    const option = c.all().find(n => String(n.props.class || '').split(' ').includes('model-option') && text(n).includes('Free B'))
    assert.ok(option, 'the real model menu did not open')
    option.props.onClick()
    await c.enter('完整问题保持不变。')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.equal(c.sent.length, 1)
    assert.equal(c.sent[0][0], '完整问题保持不变。')
    assert.equal(c.sent[0][1], 'free-b')
    assert.deepEqual(JSON.parse(JSON.stringify(c.sent[0][2])), [{ id: 'bound-kb', name: 'Bound KB', type: 'kb', kb_type: 'document' }, { id: 'bound-file', name: 'Real source', type: 'file', kb_id: 'bound-kb', kb_name: 'Bound KB' }])
    assert.deepEqual(Array.from(c.sent[0][3]), []); assert.deepEqual(Array.from(c.sent[0][4]), [])
    assert.deepEqual(c.effects, []); assert.deepEqual(c.storageWrites, [])
    assert.equal(JSON.stringify(c.settings), before)
    assert.ok(c.requests.some(([kind, id]) => kind === 'files' && id === 'bound-kb'))
  } finally { c.app.unmount() }
})

test('a failed scoped list leaves no stale sources and can be retried from the same @ control', async () => {
  let attempts = 0
  const c = mountComposer({}, { files: async () => {
    if (++attempts === 1) throw new Error('isolated fixture list unavailable')
    return { data: [{ id: 'retry-file', title: 'Available source', knowledge_base_id: 'bound-kb' }], total: 1 }
  } })
  try {
    await c.flush()
    c.find('kb-btn').props.onMousedown({ preventDefault() {} })
    await c.flush()
    assert.equal(c.find('mention-retry-file'), undefined)
    assert.deepEqual(c.effects, [['warning', '无法加载当前知识库资料，请重试。']])
    c.find('kb-btn').props.onMousedown({ preventDefault() {} })
    await c.flush()
    assert.ok(c.find('mention-retry-file'))
  } finally { c.app.unmount() }
})

test('injected popup events cannot select an external KB or an unlisted file with a forged KB label', async () => {
  const c = mountComposer()
  try {
    await c.flush()
    c.find('kb-btn').props.onMousedown({ preventDefault() {} })
    await c.flush()
    for (const item of [
      { id: 'foreign-kb', name: 'External', type: 'kb' },
      { id: 'foreign-file', name: 'External', type: 'file', kbId: 'bound-kb' },
      { id: 'foreign-tool', name: 'External', type: 'mcp' },
    ]) c.emitMention(item)
    await c.enter('未选择资料的完整问题。')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.equal(c.sent.length, 1)
    assert.deepEqual(JSON.parse(JSON.stringify(c.sent[0][2])), [{ id: 'bound-kb', name: 'Bound KB', type: 'kb', kb_type: 'document' }])
    assert.deepEqual(c.effects, [])
  } finally { c.app.unmount() }
})

test('changing the bound KB clears selection and ignores the previous KB response', async () => {
  let finish
  const c = mountComposer({}, { files: () => new Promise(resolve => { finish = resolve }) })
  try {
    await c.flush()
    c.find('kb-btn').props.onMousedown({ preventDefault() {} })
    await c.flush()
    assert.ok(finish)
    await c.enter('旧知识库草稿')
    c.props.knowledgeScope = { kbId: 'another-kb', kbName: 'Another KB' }
    await c.flush()
    finish({ data: [{ id: 'old-file', title: 'Previous source', knowledge_base_id: 'bound-kb' }], total: 1 })
    await c.flush()
    assert.equal(c.find('mention-old-file'), undefined)
    await c.enter('新知识库问题')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.deepEqual(JSON.parse(JSON.stringify(c.sent[0][2])), [{ id: 'another-kb', name: 'Another KB', type: 'kb', kb_type: 'document' }])
    assert.equal(c.all().find(n => n.type === 'textarea').props.value, '')
    assert.deepEqual(c.effects, [])
  } finally { c.app.unmount() }
})

test('an empty model catalog refuses send without inheriting a stale global model', async () => {
  const c = mountComposer({}, { models: [] })
  try {
    await c.flush(); await c.enter('完整问题')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.deepEqual(c.sent, [])
    assert.deepEqual(c.effects, [['warning', 'input.noModel']])
    assert.deepEqual(c.storageWrites, [])
  } finally { c.app.unmount() }
})

test('the real scoped stop button cancels its authenticated session and reply', async () => {
  const c = mountComposer({ isReplying: true, assistantMessageId: 'real-reply' })
  try {
    await c.flush()
    assert.ok(c.find('stop-btn'))
    assert.equal(c.find('send-btn'), undefined)
    await c.find('stop-btn').props.onClick()
    await c.flush()
    assert.deepEqual(c.effects, [['stop', 'research-session', 'real-reply'], ['success', 'input.messages.stopSuccess']])
  } finally { c.app.unmount() }
})

test('scoped stop reports API failure and never invents an assistant ID', async () => {
  for (const [assistantMessageId, stopFails, expected] of [
    ['', false, [['warning', 'input.messages.messageMissing']]],
    ['real-reply', true, [['stop', 'research-session', 'real-reply'], ['error', 'input.messages.stopFailed']]],
  ]) {
    const c = mountComposer({ isReplying: true, assistantMessageId }, { stopFails })
    try {
      await c.flush()
      await c.find('stop-btn').props.onClick()
      assert.deepEqual(c.effects, expected)
      assert.deepEqual(c.sent, [])
    } finally { c.app.unmount() }
  }
})


test('the research composer starts with its bound KB chip and preserves it after sending', async () => {
  const c = mountComposer()
  try {
    await c.flush()
    assert.ok(c.all().some(n => String(n.props.class || '').includes('mention-chip') && n.children.some(child => child.text.includes('Bound KB'))))
    await c.enter('完整问题')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.deepEqual(JSON.parse(JSON.stringify(c.sent[0][2])), [{ id: 'bound-kb', name: 'Bound KB', type: 'kb', kb_type: 'document' }])
    assert.ok(c.find('mention-chip--kb'), 'sending must keep the current KB visible')
  } finally { c.app.unmount() }
})

test('the default KB chip can be removed locally while scope changes restore only the new KB', async () => {
  const c = mountComposer()
  const before = JSON.stringify(c.settings)
  try {
    await c.flush()
    c.find('mention-chip__remove').props.onClick({ stopPropagation() {} })
    await c.flush()
    assert.equal(c.find('mention-chip--kb'), undefined)
    await c.enter('当前工作台问题')
    c.find('send-btn').props.onClick()
    await c.flush()
    assert.deepEqual(Array.from(c.sent[0][2]), [])
    c.props.knowledgeScope = { kbId: 'another-kb', kbName: 'Another KB' }
    await c.flush()
    assert.ok(c.find('mention-chip--kb'))
    assert.equal(JSON.stringify(c.settings), before)
    assert.deepEqual(c.effects, [])
    assert.deepEqual(c.storageWrites, [])
  } finally { c.app.unmount() }
})
