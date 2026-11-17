import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const { descriptor } = parse(readFileSync(new URL('./ConversationMemoryToggle.vue', import.meta.url), 'utf8'))
const script = compileScript(descriptor, { id: 'conversation-memory-test', inlineTemplate: true }).content
const compiled = ts.transpileModule(script, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText
const exports: Record<string, any> = {}
runInNewContext(compiled, { exports, require: (id: string) => {
  assert.equal(id, 'vue')
  return vue
} })

function fixture(initial: boolean, locked = false) {
  const nodes: any[] = []
  const state = vue.reactive({ disabled: initial, locked })
  const events: boolean[] = []
  const renderer = vue.createRenderer<any, any>({
    createElement(tag) { const node = { tag, props: {} as Record<string, any>, text: '' }; nodes.push(node); return node },
    createText: text => ({ text }), createComment: () => ({}),
    insert() {}, remove() {}, setText(node, text) { node.text = text },
    setElementText(node, text) { node.text = text },
    patchProp(node, key, _old, value) { node.props[key] = value },
    parentNode: () => null, nextSibling: () => null,
  })
  const app = renderer.createApp({ render: () => vue.h(exports.default, {
    ...state, 'onUpdate:disabled': (disabled: boolean) => { events.push(disabled); state.disabled = disabled },
  }) })
  app.mount({})
  return { state, events, nodes, button: nodes.find(node => node.tag === 'button'), close: () => app.unmount() }
}

test('new conversation can choose temporary research and return to ordinary memory', async t => {
  const f = fixture(false); t.after(f.close)
  assert.equal(f.button.props['aria-pressed'], false)
  f.button.props.onClick()
  await vue.nextTick()
  assert.equal(f.state.disabled, true)
  assert.equal(f.button.props['aria-pressed'], true)
  assert.ok(f.nodes.some(node => node.text === '本次不记忆'))
  f.button.props.onClick()
  await vue.nextTick()
  assert.equal(f.state.disabled, false)
  assert.deepEqual(f.events, [true, false])
})

test('persisted temporary conversation stays visibly locked after remount', t => {
  for (let reload = 0; reload < 2; reload++) {
    const f = fixture(true, true); t.after(f.close)
    assert.equal(f.button.props['aria-pressed'], true)
    assert.equal(f.button.props['aria-disabled'], true)
    assert.match(f.button.props.title, /不读取或写入长期记忆/)
    f.button.props.onClick()
    assert.deepEqual(f.events, [])
    assert.equal(f.state.disabled, true)
  }
})
