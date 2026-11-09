import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import * as Vue from 'vue'
import { compile } from '@vue/compiler-dom'
import { renderToString } from '@vue/server-renderer'

const source = readFileSync(new URL('./AgentStreamDisplay.vue', import.meta.url), 'utf8')
const helpers = source.slice(source.indexOf('const isReferenceDrawerTool ='), source.indexOf('// Delegated handlers'))
const canOpen = source.slice(source.indexOf('const canOpenToolReferences ='), source.indexOf('const openToolReferences ='))
const code = ts.transpileModule(`${helpers}\n${canOpen}\n({ hasExpandableResults, canOpenToolReferences, toolFailureReason })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText
const presentation = vm.runInNewContext(code, {
  isMcpTool: () => false,
  WIKI_EDIT_TOOL_NAMES: new Set(), WIKI_ISSUE_TOOL_NAMES: new Set(),
  getToolReferenceItems: (event: any) => event.references || [],
})

test('failed document and search reads expose their actual error even with no result references', () => {
  for (const tool_name of ['read_document', 'search_knowledge', 'web_search']) {
    const event = { tool_name, success: false, pending: false, error: 'Source selection required', tool_data: { count: 0 } }
    assert.equal(presentation.hasExpandableResults(event), true)
    assert.equal(presentation.canOpenToolReferences(event), false)
    assert.equal(presentation.toolFailureReason(event), event.error)
    assert.equal(presentation.hasExpandableResults({ ...event, references: [{}] }), true)
    assert.equal(presentation.canOpenToolReferences({ ...event, references: [{}] }), false)
  }
  assert.equal(presentation.hasExpandableResults({ tool_name: 'read_document', success: true, references: [{}] }), false)
  assert.equal(presentation.canOpenToolReferences({ success: true, references: [{}] }), true)
  assert.equal(presentation.toolFailureReason({ pending: true, success: false, error: 'stale' }), '')
})

test('both actual timeline templates render failure reasons as escaped text', async () => {
  const templates = source.match(/<div v-if="toolFailureReason\(event\)" class="tool-failure-reason">.*?<\/div>/g) || []
  assert.equal(templates.length, 2, 'live and completed-step views both retain the reason')
  for (const template of templates) {
    const render = new Function('Vue', compile(template, { mode: 'function', prefixIdentifiers: true }).code)(Vue)
    const event = { success: false, error: '<script>bad()</script>\nConcrete failure' }
    const html = await renderToString(Vue.createSSRApp({ data: () => ({ event }), methods: presentation, render }))
    assert.ok(html.includes('&lt;script&gt;bad()&lt;/script&gt;'))
    assert.ok(html.includes('Concrete failure'))
    assert.ok(!html.includes('<script>'))
  }
})
