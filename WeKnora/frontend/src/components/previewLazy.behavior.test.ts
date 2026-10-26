import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { compileScript, parse } from '@vue/compiler-sfc'
import * as Vue from 'vue'

function renderer() {
  const root: any = { children: [] }
  const instance = Vue.createRenderer<any, any>({
    createElement: tag => ({ tag, props: {}, children: [], parent: null, text: '' }),
    createText: text => ({ text, children: [] }), createComment: text => ({ comment: text, children: [] }),
    setText(node, text) { node.text = text }, setElementText(node, text) { node.text = text; node.children = [] },
    patchProp(node, key, _old, value) { node.props[key] = value },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] || null,
    insert(node, parent, anchor) { if (node.parent) node.parent.children = node.parent.children.filter((item: any) => item !== node); node.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, node) },
    remove(node) { if (node.parent) node.parent.children = node.parent.children.filter((item: any) => item !== node) },
    querySelector: () => null, setScopeId() {}, insertStaticContent: () => [null, null],
  })
  const all = (node: any): any[] => [node, ...(node.children || []).flatMap(all)]
  return { root, instance, find: (fn: any) => all(root).find(fn) }
}
const flush = async () => { for (let i = 0; i < 24; i++) await Promise.resolve(); await Vue.nextTick() }

function previewFixture(options: { deferred?: boolean; failFirst?: boolean } = {}) {
  const filename = process.env.PROTECTED_PREVIEW_TEST_SOURCE || new URL('./ProtectedResourcePreview.vue', import.meta.url)
  const descriptor = parse(readFileSync(filename, 'utf8')).descriptor
  const script = compileScript(descriptor, { id: 'protected-preview-lazy', inlineTemplate: true })
  const code = ts.transpileModule(script.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const target = new EventTarget(), module: any = { exports: {} }
  const output = renderer(), seen: any[] = []
  let loads = 0, resolve!: (value: unknown) => void, close = () => {}
  const pending = new Promise(value => { resolve = value })
  Object.defineProperty(pending, '__esModule', { value: true })
  const component = { props: ['sourceBlob', 'fileName', 'active'], setup(props: any) {
    return () => { seen.push({ ...props }); return Vue.h('article', { 'data-preview': true }, props.fileName) }
  } }
  const loaded = { __esModule: true, default: component }
  vm.runInNewContext(code, { exports: module.exports, Blob, window: target,
    require(name: string) {
      if (name === 'vue') return Vue
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      if (name === '@/utils/protectedResource') return { RESOURCE_PREVIEW_EVENT: 'weknora:resource-preview' }
      if (name === './document-preview.vue') {
        loads++
        if (options.failFirst && loads === 1) throw new Error('unavailable preview chunk')
        return options.deferred && loads === 1 ? pending : loaded
      }
      throw new Error('Unexpected dependency ' + name)
    },
  })
  const app = output.instance.createApp(module.exports.default)
  app.config.globalProperties.$t = (key: string) => key
  app.config.errorHandler = () => {}
  app.component('t-drawer', Vue.defineComponent({ props: { visible: Boolean },
    emits: { 'update:visible': (value: boolean) => typeof value === 'boolean' }, setup(props, { emit, slots }) {
    close = () => emit('update:visible', false)
    return () => Vue.h('aside', {}, props.visible ? slots.default?.() : [])
  } }))
  app.mount(output.root)
  function open(fileName: string, blobURL = 'blob:test') {
    const blob = new Blob([fileName])
    const event = new Event('weknora:resource-preview', { cancelable: true })
    Object.defineProperty(event, 'detail', { value: { blob, blobURL, fileName } })
    target.dispatchEvent(event)
    return { event, blob }
  }
  return { ...output, app, open, close: () => close(), seen, loads: () => loads, resolve: () => resolve(loaded) }
}

test('the global preview listener loads no renderer until a valid file is opened, preserving the latest pending event', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const f = previewFixture({ deferred: true })
  try {
    await flush(); assert.equal(f.loads(), 0)
    assert.equal(f.open('invalid.md', 'https://untrusted.test/file').event.defaultPrevented, false)
    await flush(); assert.equal(f.loads(), 0)
    const first = f.open('first.md'); assert.equal(first.event.defaultPrevented, true)
    await flush(); assert.equal(f.loads(), 1)
    const latest = f.open('latest.md')
    await flush(); f.resolve(); await flush()
    assert.equal(f.loads(), 1)
    assert.equal(f.seen.at(-1).fileName, 'latest.md')
    assert.equal(f.seen.at(-1).sourceBlob, latest.blob)
    f.close(); await flush(); assert.equal(Boolean(f.find((node: any) => node.props?.['data-preview'])), false)
    f.open('reopened.md'); await flush(); assert.equal(f.loads(), 1)
    assert.equal(f.seen.at(-1).fileName, 'reopened.md')
  } finally { f.app.unmount() }
  assert.equal(f.open('after-unmount.md').event.defaultPrevented, false)
})

test('a failed global preview chunk can be retried without losing the selected protected file', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const f = previewFixture({ failFirst: true })
  try {
    f.open('selected.md'); await flush()
    const retry = f.find((node: any) => node.tag === 'button' && node.text === 'preview.retry')
    assert.ok(retry)
    retry.props.onClick(); await flush()
    assert.equal(f.loads(), 2)
    assert.equal(f.seen.at(-1).fileName, 'selected.md')
  } finally { f.app.unmount() }
})

function editorFixture(failFirst = false) {
  const source = parse(readFileSync(new URL('./manual-knowledge-editor.vue', import.meta.url), 'utf8')).descriptor.scriptSetup!.content
  const syntax = ts.createSourceFile('manual-preview.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['previewParser', 'previewLoading', 'previewFailed', 'previewModulePromise', 'previewHTML']
  const declarations = syntax.statements.filter(node =>
    ts.isVariableStatement(node) && node.declarationList.declarations.some(item => ts.isIdentifier(item.name) && names.includes(item.name.text))
    || ts.isFunctionDeclaration(node) && node.name?.text === 'loadPreviewMarkdown'
    || ts.isExpressionStatement(node) && ts.isCallExpression(node.expression)
      && node.expression.expression.getText(syntax) === 'watch' && node.expression.arguments[0]?.getText(syntax) === 'visible')
  const code = ts.transpileModule(declarations.map(node => node.getText(syntax)).join('\n')
    + '\nexports.state = { previewParser, previewLoading, previewFailed, previewHTML, loadPreviewMarkdown };', {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  let loads = 0, resolve!: (value: unknown) => void
  const pending = new Promise(value => { resolve = value })
  Object.defineProperty(pending, '__esModule', { value: true })
  const visible = Vue.ref(false), form = { content: 'original' }, module: any = { exports: {} }
  const parser = { parse: (text: string) => '<b>' + text + '</b>' }
  const scope = Vue.effectScope()
  scope.run(() => vm.runInNewContext(code, { exports: module.exports, ...Vue, visible, form,
    editorGeneration: 0, editorSessionScope: () => 'current', editorSessionCurrent: () => visible.value,
    initialize: async () => {}, setSelectionRange: () => {}, selectionRange: {}, resetForm: () => {},
    saving: Vue.ref(false), kbLoading: Vue.ref(false), contentLoading: Vue.ref(false), kbOptions: Vue.ref([]),
    t: (key: string) => key, safeMarkdownToHTML: (text: string) => 'safe:' + text,
    sanitizeHTML: (text: string) => 'sanitized:' + text,
    require(name: string) { assert.equal(name, 'marked'); loads++; if (failFirst && loads === 1) throw new Error('unavailable markdown chunk'); return loads === 1 && !failFirst ? pending : { marked: parser } },
  }))
  return { ...module.exports.state, visible, form, loads: () => loads, resolve: () => resolve({ marked: parser }), stop: () => scope.stop() }
}

test('manual editor shares one pending code load across close/reopen and sanitizes only the current form', async () => {
  const f = editorFixture()
  try {
    assert.equal(f.loads(), 0)
    f.visible.value = true; await flush(); assert.equal(f.loads(), 1)
    f.visible.value = false; await flush(); f.form.content = 'new session'
    f.visible.value = true; await flush(); assert.equal(f.loads(), 1)
    f.resolve(); await flush()
    assert.equal(f.previewHTML.value, 'sanitized:<b>safe:new session</b>')
    assert.equal(f.previewLoading.value, false)
  } finally { f.stop() }
})

test('manual preview recovers from a failed chunk without interrupting the current editor session', async () => {
  const f = editorFixture(true)
  try {
    f.visible.value = true; await flush()
    assert.equal(f.previewFailed.value, true)
    assert.equal(f.visible.value, true)
    await f.loadPreviewMarkdown(); await flush()
    assert.equal(f.loads(), 2)
    assert.equal(f.previewFailed.value, false)
    assert.equal(f.previewHTML.value, 'sanitized:<b>safe:original</b>')
  } finally { f.stop() }
})
