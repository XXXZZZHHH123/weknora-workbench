import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { parse } from '@vue/compiler-sfc'
import * as Vue from 'vue'

const source = parse(readFileSync(new URL('./index.vue', import.meta.url), 'utf8')).descriptor.scriptSetup!.content
const syntax = ts.createSourceFile('research-workspace.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
const declarations = syntax.statements.filter(node =>
  ts.isImportDeclaration(node) && ['vue', './ResearchChatPanel.vue'].includes((node.moduleSpecifier as ts.StringLiteral).text)
  || ts.isFunctionDeclaration(node) && node.name?.text === 'makeResearchChatPanel'
  || ts.isVariableStatement(node) && node.declarationList.declarations.some(item => ts.isIdentifier(item.name) && item.name.text === 'ResearchChatPanel'))
const code = ts.transpileModule(declarations.map(node => node.getText(syntax)).join('\n')
  + '\nexports.panel = () => ResearchChatPanel;', {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function fixture(failFirst = false) {
  let loads = 0
  const visibility = Vue.ref(false)
  const root: any = { children: [] }
  const renderer = Vue.createRenderer<any, any>({
    createElement: tag => ({ tag, props: {}, children: [], parent: null, text: '' }),
    createText: text => ({ text, children: [] }), createComment: text => ({ comment: text, children: [] }),
    setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
    patchProp(node, key, _old, value) { node.props[key] = value },
    parentNode: node => node.parent, nextSibling: () => null,
    insert(node, parent, anchor) { if (node.parent) node.parent.children = node.parent.children.filter((item: any) => item !== node); node.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, node) },
    remove(node) { if (node.parent) node.parent.children = node.parent.children.filter((item: any) => item !== node) },
    querySelector: () => null, setScopeId() {}, insertStaticContent: () => [null, null],
  })
  const component = { props: ['visible'], setup(props: any) { return () => Vue.h('aside', { 'data-chat': true, hidden: !props.visible }, 'Loaded chat') } }
  const module: any = { exports: {} }
  vm.runInNewContext(code, { exports: module.exports, chatVisible: visibility, require(name: string) {
    if (name === 'vue') return Vue
    if (name === './ResearchChatPanel.vue') {
      loads++
      if (failFirst && loads === 1) throw new Error('simulated unavailable chat chunk')
      return { __esModule: true, default: component }
    }
    throw new Error('Unexpected dependency ' + name)
  } })
  let mounted = false
  const app = renderer.createApp({ setup() { return () => {
    if (visibility.value) mounted = true
    const binding = module.exports.panel(), panel = Vue.isRef(binding) ? binding.value : binding
    return Vue.h('main', {}, mounted ? [Vue.h(panel, { visible: visibility.value })] : [])
  } } })
  app.config.errorHandler = () => {}
  app.mount(root)
  const all = (node: any): any[] => [node, ...(node.children || []).flatMap(all)]
  const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); await Vue.nextTick() }
  return { root, app, visibility, loads: () => loads, flush, find: (fn: any) => all(root).find(fn) }
}

test('workbench opening does not load the chat component; first open loads it once and collapse keeps it mounted', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const f = fixture()
  try {
    await f.flush(); assert.equal(f.loads(), 0)
    f.visibility.value = true; await f.flush(); assert.equal(f.loads(), 1)
    assert.ok(f.find((node: any) => node.props?.['data-chat']))
    f.visibility.value = false; await f.flush()
    f.visibility.value = true; await f.flush()
    assert.equal(f.loads(), 1)
  } finally { f.app.unmount() }
})

test('a failed first chat chunk shows a retry control and successful retry mounts the original panel', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const f = fixture(true)
  try {
    f.visibility.value = true; await f.flush()
    const retry = f.find((node: any) => node.tag === 'button' && node.text === '重试')
    assert.ok(retry, 'A failed lazy load must remain recoverable')
    retry.props.onClick(); await f.flush()
    assert.equal(f.loads(), 2)
    assert.ok(f.find((node: any) => node.props?.['data-chat']))
  } finally { f.app.unmount() }
})
