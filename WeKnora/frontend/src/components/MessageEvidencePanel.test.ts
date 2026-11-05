import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import zh from '../i18n/locales/zh-CN'
import en from '../i18n/locales/en-US'
import * as evidenceSources from '../utils/evidenceSources'
import type { EvidenceDetail, EvidenceReference } from '../api/chat/evidence'

type HostNode = { type: string; text: string; props: Record<string, any>; children: HostNode[]; parent: HostNode | null; focus(): void }
const node = (type: string, text = ''): HostNode => ({ type, text, props: {}, children: [], parent: null, focus() {} })
function allText(root: HostNode): string { return root.text + root.children.map(allText).join('') }

const descriptor = parse(readFileSync(new URL('./MessageEvidencePanel.vue', import.meta.url), 'utf8')).descriptor
const script = compileScript(descriptor, { id: 'evidence-behavior-test', inlineTemplate: true })
const compiled = ts.transpileModule(script.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

function mountPanel(detail: EvidenceDetail, locale: typeof zh | typeof en = zh) {
  const calls: unknown[][] = []
  const modules: Record<string, unknown> = {
    vue,
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key.split('.').reduce((value: any, part) => value?.[part], locale) || key }) },
    '@/stores/auth': { useAuthStore: () => vue.reactive({ token: 'synthetic', selectedTenantId: 7 }) },
    '@/components/document-preview.vue': { __esModule: true, default: { render: () => null } },
    '@/api/chat/evidence': {
      getMessageEvidence: async (...args: unknown[]) => { calls.push(args); return detail },
      previewEvidenceSource: async () => new Blob(['synthetic source']),
    },
    '@/utils/evidenceSources': evidenceSources,
  }
  const exports: { default?: vue.Component } = {}
  vm.runInNewContext(compiled, { exports, console, Blob, document: { activeElement: null }, HTMLElement: class {}, require(id: string) {
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
  const app = renderer.createApp(exports.default!, { sessionId: 'synthetic-session', messageId: 'synthetic-message', references: [detail.reference] })
  const instance = app.mount(root) as unknown as { open(id?: string): Promise<void> }
  return { app, instance, calls, body, root }
}

const reference: EvidenceReference = { id: 'ev_actual', kind: 'structured_result', result_id: 'synthetic-result', result_hash: 'a'.repeat(64), title: 'Actual result' }
function fixture(review_status?: 'unverified', execution = 'source_cell_projection'): EvidenceDetail {
  return { reference: { ...reference, ...(review_status ? { review_status } : {}) }, status: 'snapshot',
    sources: [{ knowledge_base_id: 'kb', knowledge_id: 'doc', source_sha256: 'b'.repeat(64), title: 'Original', file_name: 'original.pdf' }],
    manifest: { result: { columns: ['period', 'amount'], rows: [['2031', '12.500']] },
      column_metadata: [{ name: 'period', unit_metadata: { status: 'unknown', evidence_level: 'none' } }, { name: 'amount', unit_metadata: { status: 'declared', symbol: 'kg', evidence_level: 'reviewed_plan_declaration' } }],
      computation: { execution: { kind: execution }, input_scope: 'actual_loaded_rows', input_count: 2,
        mappings: [{ source_rows: 2, source_columns: 4, projection_output_rows: 1, mapping: { orientation: 'rows', value_indexes: [1] } }] } } }
}

for (const [name, locale] of [['zh', zh], ['en', en]] as const) {
  test(`unverified evidence opens actual protected detail with an explicit checking warning (${name})`, async () => {
    const view = mountPanel(fixture('unverified'), locale)
    try {
      assert.ok(allText(view.root).includes(locale.evidence.checkingButton))
      await view.instance.open()
      await vue.nextTick()
      assert.deepEqual(view.calls[0], ['synthetic-session', 'synthetic-message', 'ev_actual'])
      const text = allText(view.body)
      assert.ok(text.includes(locale.evidence.unverifiedReview))
      assert.ok(text.includes('12.500'))
      assert.ok(text.includes('kg'))
      assert.ok(text.includes(locale.evidence.reviewedPlanUnit))
      assert.ok(text.includes(locale.evidence.calculation))
      assert.ok(text.includes(locale.evidence.calculated))
      assert.ok(text.includes('actual_loaded_rows'), 'the actual processing scope remains visible')
    } finally { view.app.unmount() }
  })
}

test('normal evidence keeps its normal entry and raw projection uses neutral processing copy', async () => {
  for (const execution of ['source_cell_projection', 'sql']) {
    const view = mountPanel(fixture(undefined, execution))
    try {
      assert.ok(allText(view.root).includes(zh.evidence.button))
      assert.ok(!allText(view.root).includes(zh.evidence.checkingButton))
      await view.instance.open()
      await vue.nextTick()
      const text = allText(view.body)
      assert.ok(!text.includes(zh.evidence.unverifiedReview))
      assert.ok(text.includes('本次查询或处理返回的数据'))
      assert.ok(text.includes('查询与处理依据'))
      assert.ok(!text.includes('不是原值'))
      assert.ok(text.includes('12.500'))
    } finally { view.app.unmount() }
  }
})

test('unverified status does not turn a revoked or stale snapshot into readable values', async () => {
  for (const status of ['unavailable', 'stale'] as const) {
    const detail: EvidenceDetail = { reference: { ...reference, review_status: 'unverified' }, status }
    const view = mountPanel(detail)
    try {
      await view.instance.open()
      await vue.nextTick()
      assert.ok(allText(view.body).includes(zh.evidence.unverifiedReview))
      assert.ok(allText(view.body).includes(zh.evidence.unavailable))
      assert.ok(!allText(view.body).includes('12.500'))
      assert.ok(!allText(view.body).includes(zh.evidence.snapshot))
    } finally { view.app.unmount() }
  }
})
