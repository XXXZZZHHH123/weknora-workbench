import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { runInNewContext } from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, h, nextTick, reactive } from 'vue'

const require = createRequire(import.meta.url)
const { descriptor } = parse(readFileSync(new URL('./WorkspaceNavigationNode.vue', import.meta.url), 'utf8'))
const compiled = ts.transpileModule(compileScript(descriptor, {
  id: 'navigation-node-test', inlineTemplate: true,
}).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
const moduleExports: any = {}
runInNewContext(compiled, { exports: moduleExports, require })

interface Element {
  type: string
  props: Record<string, any>
  children: Element[]
  parent?: Element
  text?: string
  style: Record<string, string>
}
const element = (type: string): Element => ({ type, props: {}, children: [], style: {} })
const renderer = createRenderer<Element, Element>({
  createElement: element,
  createText: (text) => ({ ...element('#text'), text }),
  createComment: (text) => ({ ...element('#comment'), text }),
  setText: (node, text) => { node.text = text },
  setElementText: (node, text) => { node.text = text },
  patchProp: (node, key, _old, value) => { node.props[key] = value },
  parentNode: (node) => node.parent ?? null,
  nextSibling: (node) => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  insert(node, parent, anchor) {
    node.parent = parent
    const at = anchor ? parent.children.indexOf(anchor) : -1
    if (at < 0) parent.children.push(node)
    else parent.children.splice(at, 0, node)
  },
  remove(node) {
    const index = node.parent?.children.indexOf(node) ?? -1
    if (index >= 0) node.parent!.children.splice(index, 1)
  },
})
function all(root: Element): Element[] { return [root, ...root.children.flatMap(all)] }
function fixture() {
  const props = reactive({ title: '知识库', depth: 0, expandable: true, expanded: true, collapsed: false })
  let navigations = 0
  let toggles = 0
  const root = element('root')
  const app = renderer.createApp({ render: () => h(moduleExports.default, {
    ...props, onNavigate: () => { navigations++ }, onToggle: () => { toggles++ },
  }, { default: () => h('span', { 'data-child': true }, '知识库 A') }) })
  app.component('t-tooltip', { props: ['content', 'placement', 'disabled'], setup: (_: unknown, { slots }: any) => () => slots.default?.() })
  app.component('t-icon', { render: () => h('i') })
  app.mount(root)
  return { root, props, app, navigations: () => navigations, toggles: () => toggles }
}

test('expansion control does not navigate and resource title does not collapse the branch', () => {
  const f = fixture()
  try {
    const buttons = all(f.root).filter((node) => node.type === 'button')
    const arrow = buttons.find((node) => node.props['aria-label'] === '收起知识库')!
    arrow.props.onClick()
    assert.equal(f.toggles(), 1)
    assert.equal(f.navigations(), 0)
    buttons.find((node) => node.props['aria-label'] === '知识库')!.props.onClick()
    assert.equal(f.navigations(), 1)
    assert.equal(f.toggles(), 1)
  } finally { f.app.unmount() }
})

test('branch collapse hides children and sidebar collapse keeps only the labeled root entry', async () => {
  const f = fixture()
  try {
    f.props.expanded = false
    await nextTick()
    assert.equal(all(f.root).find((node) => node.props.role === 'group')?.style.display, 'none')
    assert.equal(all(f.root).find((node) => node.props['aria-expanded'] !== undefined)?.props['aria-expanded'], false)
    f.props.collapsed = true
    await nextTick()
    assert.equal(all(f.root).filter((node) => node.type === 'button').length, 1)
    assert.equal(all(f.root).some((node) => node.props['data-child']), false)
    assert.equal(all(f.root).find((node) => node.type === 'button')?.props['aria-label'], '知识库')
  } finally { f.app.unmount() }
})

test('leaf entries have no disclosure control or child group', async () => {
  const f = fixture()
  try {
    f.props.expandable = false
    f.props.depth = 1
    await nextTick()
    assert.equal(all(f.root).filter((node) => node.type === 'button').length, 1)
    assert.equal(all(f.root).some((node) => node.props.role === 'group'), false)
    assert.equal(all(f.root).find((node) => node.props['data-nav-depth'] !== undefined)?.props['data-nav-depth'], 1)
  } finally { f.app.unmount() }
})
