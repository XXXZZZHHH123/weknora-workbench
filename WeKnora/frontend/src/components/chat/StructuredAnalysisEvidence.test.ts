import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as answerSources from '../../utils/structuredAnswerSources'
import * as sheetView from '../../utils/structuredSheetView'
import * as sourceRead from '../../utils/structuredSourceRead'

type HostNode = { type: string; text: string; props: Record<string, any>; children: HostNode[]; parent: HostNode | null }
const node = (type: string, text = ''): HostNode => ({ type, text, props: {}, children: [], parent: null })
const flatten = (root: HostNode): HostNode[] => [root, ...root.children.flatMap(flatten)]
const allText = (root: HostNode): string => root.text + root.children.map(allText).join('')
const flush = async () => { await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const datasetId = 'd'.repeat(24), importId = 'e'.repeat(24), hash = 'a'.repeat(64)
const result = {
  kind: 'structured_analysis', status: 'ok', dataset_id: datasetId,
  source: { file_name: 'Synthetic original.xlsx', sha256: hash },
  source_context: { knowledge_base_id: 'synthetic-kb', knowledge_base_tenant_id: 77 },
  columns: ['total'], rows: [[100]], sql: 'SELECT SUM(value) FROM data', input_count: 3,
  inputs: { amount: { input_count: 3, valid_count: 2, example_cells: ['B2', 'B3'],
    series: { sheet: 'Records', metric: 'Amount', unit: 'units', header_cells: ['B1'] } } },
}
const liveEvent = { type: 'tool_call', tool_name: 'structured_analyze_table', pending: false, success: true, tool_data: result }
const savedStep = JSON.parse(JSON.stringify({ tool_calls: [{ target: { name: liveEvent.tool_name }, result: { success: true, data: result } }] }))
const restoredEvent = {
  type: 'tool_call', tool_name: savedStep.tool_calls[0].target.name, pending: false,
  success: savedStep.tool_calls[0].result.success, tool_data: savedStep.tool_calls[0].result.data,
}
const descriptor = parse(readFileSync(new URL('./StructuredAnalysisEvidence.vue', import.meta.url), 'utf8')).descriptor
const compiled = ts.transpileModule(compileScript(descriptor, {
  id: 'structured-evidence-test', inlineTemplate: true,
}).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

function mountEvidence(event: any = liveEvent, options: { status?: number; manifest?: Record<string, unknown> } = {}) {
  const requests: Array<{ url: string; kb: string | null; tenant: string | null }> = []
  const exports: { default?: vue.Component } = {}
  const auth = vue.reactive({ token: 'synthetic-token', user: { id: 'synthetic-reader' }, selectedTenantId: 7 })
  const modules: Record<string, unknown> = {
    vue,
    '@/stores/auth': { useAuthStore: () => auth },
    '@/components/document-preview.vue': { __esModule: true, default: { render: () => null } },
    '@/utils/structuredAnswerSources': answerSources,
    '@/utils/structuredSheetView': sheetView,
    '@/utils/structuredSourceRead': sourceRead,
    '@/utils/authFetch': { fetchWithAuthRetry: async (url: string, init: RequestInit) => {
      const headers = new Headers(init.headers)
      requests.push({ url, kb: headers.get('X-Knowledge-Base-ID'), tenant: headers.get('X-Tenant-ID') })
      if (options.status) return new Response(null, { status: options.status })
      if (url.endsWith('/report')) return Response.json({ manifest: { dataset_id: datasetId, import_id: importId, sha256: hash, ...options.manifest } })
      assert.ok(url.startsWith(`/structured/api/imports/${importId}/sheet-view?`))
      return Response.json({ sheet_name: 'Records', range: { start_row: 1, end_row: 4, start_col: 1, end_col: 3 }, cells: [
        { row: 2, column: 2, cell: 'B2', value: 120, display: '120' },
        { row: 3, column: 2, cell: 'B3', value: -20, display: '-20' },
        { row: 4, column: 2, cell: 'B4', value: null, display: '' },
      ] })
    } },
  }
  vm.runInNewContext(compiled, { exports, console, Blob, Headers, URLSearchParams, require(id: string) {
    assert.ok(id in modules, `unexpected component import: ${id}`)
    return modules[id]
  } })
  const body = node('body'), root = node('root')
  const detach = (child: HostNode) => {
    if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1)
    child.parent = null
  }
  const renderer = vue.createRenderer<HostNode, HostNode>({
    patchProp: (el, key, _old, value) => { el.props[key] = value },
    insert(child, parent, anchor) { detach(child); child.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, child) },
    remove: detach,
    createElement: type => node(type), createText: text => node('#text', text), createComment: text => node('#comment', text),
    setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text; el.children = [] },
    parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] || null,
    querySelector: selector => selector === 'body' ? body : null,
    setScopeId() {},
    insertStaticContent(content, parent, anchor) {
      const child = node('#static', content); child.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(index < 0 ? parent.children.length : index, 0, child)
      return [child, child]
    },
  })
  const sources = answerSources.structuredAnswerSources([event], true)
  const app = renderer.createApp(exports.default!, { evidence: sources[0]?.data, sources })
  app.mount(root)
  const click = async (label: string) => {
    const button = flatten(root).find(el => el.type === 'button' && allText(el) === label)
    assert.ok(button, `missing visible source button: ${label}`)
    await button.props.onClick()
    await flush()
  }
  return { app, root, requests, click }
}

for (const [mode, event] of [['live', liveEvent], ['restored', restoredEvent]] as const) {
  for (const address of ['B2', 'B3']) {
    test(`final numeric source opens ${address} in one click with original sheet highlight (${mode})`, async t => {
      const view = mountEvidence(event); t.after(() => view.app.unmount())
      assert.equal(view.requests.length, 0, 'rendering a source never fetches another document')
      assert.ok(allText(view.root).includes(hash), 'the actual source version is visible')
      await view.click(`Records!${address}`)
      assert.equal(view.requests.length, 2, 'one click loads the version manifest and original sheet')
      assert.ok(view.requests[0]!.url.endsWith(`/datasets/${datasetId}/report`))
      assert.equal(new URL(view.requests[1]!.url, 'https://test.invalid').searchParams.get('name'), 'Records')
      assert.ok(view.requests.every(request => request.kb === 'synthetic-kb' && request.tenant === '77'))
      const highlighted = flatten(view.root).filter(el => el.type === 'td' && el.props.class === 'highlighted')
      assert.equal(highlighted.length, 1)
      assert.equal(highlighted[0]!.props['data-source-cell'], address)
      assert.equal(allText(highlighted[0]!), address === 'B2' ? '120' : '-20')
      const text = allText(view.root)
      assert.ok(text.includes('有效 2 条，缺失 1 条'))
      assert.ok(text.includes(result.sql))
      assert.ok(text.includes('仅为输入示例，不代表完整 SQL 行级血缘'))
    })
  }
}

test('numeric source permission denial is explicit and does not display original cells', async t => {
  const view = mountEvidence(liveEvent, { status: 403 }); t.after(() => view.app.unmount())
  await view.click('Records!B3')
  assert.equal(view.requests.length, 1)
  assert.ok(allText(view.root).includes('当前账号或知识库无权读取原始来源'))
  assert.ok(!flatten(view.root).some(el => el.props['data-source-cell']))
})

test('numeric source version and dataset identity mismatches block original-sheet reads', async t => {
  for (const manifest of [{ sha256: 'b'.repeat(64) }, { dataset_id: 'f'.repeat(24) }, { import_id: '../other' }]) {
    const view = mountEvidence(liveEvent, { manifest }); t.after(() => view.app.unmount())
    await view.click('Records!B2')
    assert.equal(view.requests.length, 1)
    assert.ok(flatten(view.root).some(el => el.props.role === 'alert'))
    assert.ok(!flatten(view.root).some(el => el.props['data-source-cell']))
  }
})

test('failed tool results and missing source scope cannot create an actionable numeric source', async t => {
  for (const event of [{ ...liveEvent, success: false }, { ...liveEvent, tool_data: { ...result, source_context: null } }]) {
    const view = mountEvidence(event); t.after(() => view.app.unmount())
    assert.ok(!flatten(view.root).some(el => el.type === 'button' && (allText(el).includes('B2') || allText(el).includes('查看原始表格'))))
    assert.equal(view.requests.length, 0)
  }
})
