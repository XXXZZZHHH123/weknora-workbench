import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { compile } from '@vue/compiler-dom'
import * as vue from 'vue'

const source = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')
const markup = source.match(/<button\b[^>]*class="parser-hint"[^>]*>[\s\S]*?<\/button>/)?.[0]
assert.ok(markup, 'the parser hint uses a native button')
const { code } = compile(markup, { prefixIdentifiers: true })
const render = new Function('Vue', code)({ ...vue, resolveComponent: (name: string) => name })

function renderHint(canEdit: boolean, unsupportedFileTypes: string[], goToParserSettings = () => {}) {
  return render({
    canEdit, unsupportedFileTypes, goToParserSettings,
    $t: (key: string, params?: { types: string }) => params?.types || key,
  }, [])
}

test('read-only users do not see the parser-engine warning (#4486)', () => {
  assert.equal(renderHint(false, ['pdf']).type, vue.Comment)
})

test('users only see a parser-engine warning when some types lack an engine', () => {
  assert.equal(renderHint(true, []).type, vue.Comment)
})

test('the parser hint keeps the full type list and opens settings through a native button', () => {
  let opened = 0
  const hint = renderHint(true, ['docm', 'odp', 'odt', 'pptm', 'rtf'], () => { opened++ })

  assert.equal(hint.type, 'button')
  assert.equal(hint.props.type, 'button')
  assert.equal(hint.children[1].props.class, 'parser-hint-text')
  assert.match(hint.children[1].children[0].children, /\.docm、\.odp、\.odt、\.pptm、\.rtf/)
  assert.match(hint.children[1].children[1].children, /goToParserSettings/)
  hint.props.onClick()
  assert.equal(opened, 1)
})
