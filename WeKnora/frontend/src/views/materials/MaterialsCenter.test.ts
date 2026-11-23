import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import * as icons from 'tdesign-icons-vue-next'
import * as vue from 'vue'
import { fixture } from '../../test-utils/vueFixture'
import * as intake from './intake'

const file = new URL('./MaterialsCenter.vue', import.meta.url)
const { descriptor } = parse(readFileSync(file, 'utf8'), { filename: file.pathname })
const { bindings } = compileScript(descriptor, { id: 'materials-center' })
const template = compileTemplate({
  source: descriptor.template!.content,
  filename: file.pathname,
  id: 'materials-center',
  compilerOptions: { bindingMetadata: bindings },
})
assert.deepEqual(template.errors, [])

function elements(node: unknown): vue.VNode[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!vue.isVNode(node)) return []
  return [node, ...elements(node.children)]
}

function setup(t: TestContext, mode?: 'url') {
  const f = fixture(t)
  const { vm } = f.component(file, {
    'vue-router': { useRoute: () => ({ query: { add: '1', mode, kb_id: 'kb-1' } }), useRouter: () => ({}) },
    'tdesign-vue-next': { MessagePlugin: {} },
    'tdesign-icons-vue-next': icons,
    '@/stores/auth': { useAuthStore: () => ({}) },
    '@/utils/authFetch': {},
    '@/composables/useTheme': { useTheme: () => ({ effectiveTheme: vue.ref('light') }) },
    '@/stores/uploadConfirm': { useUploadConfirmStore: () => ({}) },
    '@/api/knowledge-base': {},
    './client': { createMaterialClient: () => ({}) },
    './intake': intake,
  })
  const { render } = f.evaluate(template.code, {
    vue: { ...vue, resolveComponent: (name: string) => name, withCtx: (fn: unknown) => fn, withDirectives: (node: unknown) => node },
  })
  return { vm, render: () => elements(render(vm, [], {}, vm, {}, {})) }
}

test('intake buttons expose one pressed mode and switch the corresponding form', t => {
  const { render } = setup(t)
  function checkMode(pressed: boolean[]) {
    const nodes = render()
    const group = nodes.find(node => node.props?.class === 'upload-mode')
    assert.equal(group?.props?.role, 'group')
    const buttons = elements(group).filter(node => node.type === 'button')
    assert.deepEqual(buttons.map(button => button.children), ['文件上传', '网页采集'])
    assert.deepEqual(buttons.map(button => button.props?.type), ['button', 'button'])
    assert.deepEqual(buttons.map(button => button.props?.['aria-pressed']), pressed)
    assert.equal(nodes.some(node => node.props?.class === 'drop-area'), pressed[0])
    assert.equal(nodes.some(node => node.type === 'input' && node.props?.type === 'url'), pressed[1])
    return buttons
  }

  checkMode([true, false])[1].props?.onClick()
  checkMode([false, true])[1].props?.onClick()
  checkMode([false, true])[0].props?.onClick()
  checkMode([true, false])
})

test('a web intake entry opens the URL form with the matching button pressed', t => {
  const { render } = setup(t, 'url')
  const nodes = render()
  const pressed = nodes.filter(node => node.props?.['aria-pressed'] === true)
  assert.deepEqual(pressed.map(node => node.children), ['网页采集'])
  assert.ok(nodes.some(node => node.type === 'input' && node.props?.type === 'url'))
})

test('existing documents show the failure reason as text and keep the original document link', t => {
  const { vm, render } = setup(t)
  vm.legacyDocuments = [
    { id: 'failed', title: '网页标题', parse_status: 'failed', error_message: '网页未返回可解析的正文' },
    { id: 'done', title: '文档标题', parse_status: 'completed', error_message: '上次解析的旧错误' },
    { id: 'no-reason', title: '旧记录', parse_status: 'failed' },
  ]
  const nodes = render()
  const reasons = nodes.filter(node => node.props?.class === 'legacy-error error-text')
  assert.deepEqual(reasons.map(node => node.children), ['网页未返回可解析的正文'])
  const documentLink = nodes.find(node => node.type === 'router-link' && node.props?.to?.query?.knowledge_id === 'failed')
  assert.equal(documentLink?.props?.to.path, '/platform/knowledge-bases/kb-1')
})
