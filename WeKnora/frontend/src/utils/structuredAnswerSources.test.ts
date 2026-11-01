import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { isStructuredImportId, parseExcelCell, structuredAnswerSources } from './structuredAnswerSources'
const dataset = 'd'.repeat(24), hash = 'a'.repeat(64)
const auditShape = JSON.parse(readFileSync(new URL('./fixtures/structured-answer-native-history.json', import.meta.url), 'utf8'))
const result = (patch: Record<string, any> = {}) => ({
  kind: 'structured_analysis', status: 'ok', dataset_id: dataset, sql: 'SELECT value FROM data', columns: ['value'], rows: [[5]],
  input_count: 1, input_complete: true, inputs: { v1: { input_count: 1, valid_count: 1, example_cells: ['B2'], series: { sheet: 'Values', unit: 'USD' } } },
  source: { file: 'source.xlsx', sha256: hash }, ...patch,
})
const call = (data: any, patch = {}) => ({ type: 'tool_call', tool_name: 'structured_data', success: true, pending: false, tool_data: data, ...patch })
test('only completed, successful structured tool results with real scope and source identities are normalized', () => {
  const raw = result({ source_context: { knowledge_base_id: 'kb-actual', knowledge_base_tenant_id: 77 } })
  const sources = structuredAnswerSources([call(raw)], true)
  assert.equal(sources.length, 1)
  assert.equal(sources[0].data, raw)
  assert.deepEqual(sources[0].context, { knowledge_base_id: 'kb-actual', knowledge_base_tenant_id: 77 })
  assert.equal(structuredAnswerSources([call(raw)], false).length, 0)
  assert.equal(structuredAnswerSources([call(raw, { success: false })]).length, 0)
  assert.equal(structuredAnswerSources([call(raw, { pending: true })]).length, 0)
  assert.equal(structuredAnswerSources([call(result({ source_context: undefined }))])[0].context, null)
  assert.equal(structuredAnswerSources([call(result({ source: { file: 'source.xlsx', sha256: 'bad' } }))]).length, 0)
  assert.equal(structuredAnswerSources([call(result({ dataset_id: '../other' }))]).length, 0)
})
test('live wrapped payload and persisted text result retain source context without parsing answer prose', () => {
  const data = result({ source_context: { knowledge_base_id: 'kb' } })
  assert.equal(structuredAnswerSources([call({ data })])[0].data.dataset_id, dataset)
  const persisted = call(data)
  assert.equal(structuredAnswerSources([persisted])[0].data.source.sha256, hash)
  assert.equal(structuredAnswerSources([call({ content_items: [{ type: 'text', text: JSON.stringify(data) }], output: JSON.stringify(data) })]).length, 0)
  assert.equal(structuredAnswerSources([{ ...persisted, tool_name: 'assistant_answer' }]).length, 0)
  assert.equal(structuredAnswerSources([call({ ...data, status: 'error' })]).length, 0)
})
test('series query source cells and SQL examples are surfaced only from exact cell coordinates', () => {
  const query = { dataset_id: dataset, status: 'ok', series: { series_id: 'series-1', sheet: 'Values', unit: 'USD' }, rows: [{ source_cell: 'B2', value: 5 }], source: { file: 'source.xlsx', sha256: hash, sheet: 'Values' }, source_context: { knowledge_base_id: 'kb' } }
  const sources = structuredAnswerSources([call(query, { arguments: { operation: 'query_series' } })])
  assert.equal(sources.length, 1)
  assert.equal(sources[0].operation, 'query_series')
  assert.deepEqual(parseExcelCell('$AA$20'), { row: 20, column: 27, address: 'AA20' })
  for (const invalid of ['A0', 'XFE1', 'A1048577', 'A1:B2', 'A1\n', 3, '1A']) assert.equal(parseExcelCell(invalid), null)
})
test('PDF source cards need actual table identity, source hash and verified scope; auto acceptance stays distinct', () => {
  const pdf = { kind: 'structured_analysis', status: 'ok', table_id: 'pdft_0123456789abcdef', sql: 'select count(*) from data', columns: ['count'], rows: [[3]], source_kind: 'pdf_table', source: { file: 'paper.pdf', sha256: hash }, source_context: { knowledge_base_id: 'kb' }, coverage: { source_document_completeness: 'unknown' } }
  assert.equal(structuredAnswerSources([call(pdf, { arguments: { operation: 'query_pdf_table' } })])[0].kind, 'pdf')
  const list = { kind: 'structured_pdf_tables', status: 'ok', source_context: { knowledge_base_id: 'kb' }, tables: [{ table_id: 'pdft_0123456789abcdef', status: 'auto_accepted', source: { file: 'paper.pdf', sha256: hash } }] }
  const cards = structuredAnswerSources([call(list, { arguments: { operation: 'list_pdf_tables' } })])
  assert.equal(cards.length, 1)
  assert.equal(cards[0].data.status, 'auto_accepted')
  assert.equal(structuredAnswerSources([call({ ...pdf, source_context: undefined })])[0].context, null)
  assert.equal(structuredAnswerSources([call({ ...pdf, table_id: 'not-real' })]).length, 0)
})

test('sanitized audit-shaped live and restored agent_steps events expose native numeric sources', () => {
  const live = structuredAnswerSources([auditShape.live_query_event])
  assert.equal(live.length, 1)
  assert.equal(live[0].operation, 'query_series')
  assert.deepEqual(live[0].context, { knowledge_base_id: 'kb-synthetic', knowledge_base_tenant_id: 77 })

  const call = auditShape.agent_step.tool_calls[0]
  const result = call.result
  // Mirrors the persisted session.agent_steps reconstruction in useChatStreamHandler.
  const restored = {
    type: 'tool_call',
    tool_name: call.target?.name || call.name,
    arguments: call.target?.args || call.args,
    pending: false,
    success: result?.success !== false,
    output: result?.output || '',
    tool_data: result?.data,
  }
  const historical = structuredAnswerSources([restored])
  assert.equal(historical.length, 1)
  assert.equal(historical[0].operation, 'analyze_table')
  assert.equal(historical[0].data.inputs.v1.example_cells[0], 'B2')
  assert.equal(historical[0].context?.knowledge_base_id, 'kb-synthetic')
})

test('native PDF facade tool names are protocol aliases only and retain actual page evidence', () => {
  const pdf = { kind: 'structured_source_inspection', status: 'ok', table_id: 'pdft_0123456789abcdef',
    source_kind: 'pdf_table', review_status: 'auto_accepted', source: { file_name: 'synthetic.pdf', file: 'synthetic.pdf', sha256: hash, knowledge_id: 'pdf-source', knowledge_base_id: 'kb-synthetic' },
    fragments: [{ page: 7, rows: [{ values: { value: '5' }, source_cells: { value: [{ page: 7, text: '5', column: 1 }] } }] }],
    source_context: { knowledge_base_id: 'kb-synthetic', knowledge_base_tenant_id: 77 } }
  const events = ['structured_query_pdf_table', 'structured_read_pdf_table'].map(tool_name => ({ type: 'tool_call', tool_name, success: true, pending: false, tool_data: { ...pdf, kind: tool_name === 'structured_query_pdf_table' ? 'structured_analysis' : pdf.kind } }))
  assert.deepEqual(events.map(event => structuredAnswerSources([event])[0]?.kind), ['pdf', 'pdf'])
  assert.equal(structuredAnswerSources([{ ...events[0], tool_name: 'structured_guess_answer' }]).length, 0)
})

test('the same content identity in separate knowledge bases retains both source cards', () => {
  const events = ['first-library', 'second-library'].map(knowledge_base_id =>
    call(result({ source_context: { knowledge_base_id, knowledge_base_tenant_id: 7 } }),
      { arguments: { operation: 'analyze_table' } }))
  const cards = structuredAnswerSources([...events, events[0]])
  assert.equal(cards.length, 2)
  assert.deepEqual(cards.map(card => card.context?.knowledge_base_id), ['first-library', 'second-library'])
})

test('import identifiers are validated before being placed in a request path', () => {
  assert.equal(isStructuredImportId('a'.repeat(24)), true)
  assert.equal(isStructuredImportId('../' + 'a'.repeat(24)), false)
})
