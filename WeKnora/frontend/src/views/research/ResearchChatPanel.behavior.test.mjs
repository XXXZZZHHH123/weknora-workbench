import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { parse, compileScript } from '@vue/compiler-sfc'
import * as Vue from 'vue'
import { createResearchChatSession } from './researchChatSession.ts'

const source = readFileSync(process.env.RESEARCH_CHAT_PANEL_SOURCE || new URL('./ResearchChatPanel.vue', import.meta.url), 'utf8')
const compiled = compileScript(parse(source).descriptor, { id: 'research-chat-panel-behavior', inlineTemplate: true })
const code = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

function mountPanel(remote = async () => ({ data: { id: 'real-draft-session' } })) {
  const requests = [], history = [], chats = []
  const modules = {
    vue: Vue,
    '@/views/chat/index.vue': { __esModule: true, default: { props: ['session_id', 'kbIds', 'scopedKbName'], setup(props) { return () => { chats.push({ id: props.session_id, kbIds: [...props.kbIds], name: props.scopedKbName }); return Vue.h('div', { class: 'chat-body' }) } } } },
    '@/api/chat': { createSessions: async body => { requests.push(body); return remote(body) } },
    '@/stores/settings': { useSettingsStore: () => ({ conversationModels: { selectedChatModelId: 'configured-free-model' } }) },
    '@/stores/menu': { useMenuStore: () => ({ updataMenuChildren: row => history.push(row) }) },
    './researchChatSession': { createResearchChatSession },
  }
  const exports = {}
  vm.runInNewContext(code, { exports, require: name => { assert.ok(name in modules, `unexpected dependency ${name}`); return modules[name] } })
  const node = (type, text = '') => ({ type, text, style: {}, props: {}, children: [], parent: null })
  const container = node('root')
  const renderer = Vue.createRenderer({
    createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
    setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
    patchProp: (target, key, _old, value) => { target.props[key] = value },
    insert(child, parent, anchor = null) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, child) },
    remove(child) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null },
    parentNode: child => child.parent, nextSibling: child => child.parent?.children[child.parent.children.indexOf(child) + 1] || null,
  })
  const props = Vue.reactive({ visible: false, kbId: 'current-kb', kbName: 'Current knowledge base', workbenchName: 'Workbench', workbenchId: 'actual-workbench' })
  const app = renderer.createApp({ render: () => Vue.h(exports.default, props) })
  app.component('t-button', { setup: (_props, { slots }) => () => Vue.h('button', slots.default?.()) })
  app.component('t-icon', { render: () => null })
  app.config.warnHandler = () => {}
  app.mount(container)
  const flush = async () => { await new Promise(setImmediate); await Vue.nextTick(); await new Promise(setImmediate) }
  const text = target => target.text + target.children.map(text).join('')
  return { app, props, requests, history, chats, flush, text: () => text(container) }
}

test('opening and reopening the real panel creates one draft without adding history or a KB banner', async () => {
  const panel = mountPanel()
  try {
    await panel.flush()
    assert.equal(panel.requests.length, 0)
    panel.props.visible = true
    await panel.flush()
    assert.equal(panel.requests.length, 1)
    assert.deepEqual(JSON.parse(JSON.stringify(panel.requests[0])), { title: 'Workbench · 问答', research_workbench_id: 'actual-workbench', agent_config: { enabled: false, knowledge_bases: ['current-kb'] } })
    assert.deepEqual(panel.history, [], 'opening a side panel must never create a history row')
    assert.ok(!panel.text().includes('Current knowledge base'), 'the bound KB belongs in the composer chip, not an upper banner')
    assert.ok(panel.chats.some(chat => chat.id === 'real-draft-session' && chat.kbIds[0] === 'current-kb' && chat.name === 'Current knowledge base'))
    panel.props.visible = false
    await panel.flush()
    panel.props.visible = true
    await panel.flush()
    assert.equal(panel.requests.length, 1)
    assert.ok(panel.chats.every(chat => chat.id === 'real-draft-session'))
  } finally { panel.app.unmount() }
})

test('a late draft response after leaving the workbench cannot populate history or mount a chat', async () => {
  let finish
  const panel = mountPanel(() => new Promise(resolve => { finish = resolve }))
  panel.props.visible = true
  await panel.flush()
  panel.app.unmount()
  finish({ data: { id: 'old-identity-session' } })
  await panel.flush()
  assert.deepEqual(panel.history, [])
  assert.deepEqual(panel.chats, [])
})
