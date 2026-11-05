import assert from 'node:assert/strict'
import test from 'node:test'
import type { EvidenceDetail, EvidenceReference } from '../api/chat/evidence'
import { evidenceCellPosition, evidenceSourceLocate, evidenceTableColumnInfo, evidenceTableColumns, evidenceUnitSymbol, findEvidenceReference } from './evidenceSources'
import { extractCitationHtmlPlaceholders, preprocessCitationTags, stripIncompleteCitationTag } from './citationMarkdown'

const sourceHash = 'a'.repeat(64)
const reference: EvidenceReference = { id: 'ev_actual', kind: 'raw_cell', result_id: 'actual', result_hash: 'b'.repeat(64), title: 'Source' }

function detail(cells: Array<Record<string, unknown>>): EvidenceDetail {
  return { reference, status: 'snapshot', sources: [{ knowledge_base_id: 'kb', knowledge_id: 'doc', source_sha256: sourceHash, title: 'Original', file_name: 'original.pdf' }], manifest: { source_cells: cells } }
}

test('evidence citations retain exact identity without pretending to be chunks', () => {
  const html = preprocessCitationTags('Value <evidence evidence_id="ev_actual" />')
  assert.match(html, /class="citation citation-evidence"/)
  assert.match(html, /data-evidence-id="ev_actual"/)
  assert.doesNotMatch(html, /chunk-id|knowledge-id|source_sha256/)
  assert.equal(findEvidenceReference([{ ...reference, id: 'different' }], reference.id), undefined)
  assert.equal(findEvidenceReference([reference], reference.id), reference)
  assert.equal(preprocessCitationTags('<evidence evidence_id="javascript:alert(1)" />'), '')
  assert.equal(stripIncompleteCitationTag('Bound value <eviden'), 'Bound value ')
  assert.match(extractCitationHtmlPlaceholders('<evidence evidence_id="ev_actual" />').htmlSnippets[0], /data-evidence-id="ev_actual"/)
})

test('PDF evidence preserves physical page and actual geometry without converting table indices to boxes', () => {
  const cell = { source_sha256: sourceHash, page: 14, row: 0, column: 6, bbox: [0.2, 0.7, 0.3, 0.8] }
  const locate = evidenceSourceLocate(detail([cell]), 0, 3)
  assert.equal(locate.locators[0].page, 14)
  assert.deepEqual(locate.locators[0].bbox, cell.bbox)
  assert.equal(evidenceCellPosition(cell), 'cell')
  const pageOnly = { source_sha256: sourceHash, page: 14, row: 0, column: 6 }
  assert.equal(evidenceCellPosition(pageOnly), 'page')
  assert.equal(evidenceSourceLocate(detail([pageOnly]), 0, 4).locators[0].bbox, undefined)
})

test('manual and same-page foreign evidence cannot acquire exact source-cell coordinates', () => {
  const manual = { source_sha256: sourceHash, page: 14, origin: 'manual_transcription', bbox: [0,0,1,1], fragment_bbox: [0.1,0.5,0.9,0.8] }
  assert.equal(evidenceCellPosition(manual), 'region')
  assert.deepEqual(evidenceSourceLocate(detail([manual]), 0, 1).locators[0].bbox, manual.fragment_bbox)
  assert.deepEqual(evidenceSourceLocate(detail([{ ...manual, source_sha256: 'c'.repeat(64) }]), 0, 1).locators, [])
  const stale = { ...detail([manual]), status: 'stale' as const }
  assert.equal(evidenceSourceLocate(stale, 0, 2).unavailable, true)
  assert.deepEqual(evidenceSourceLocate(stale, 0, 2).locators, [])
})

test('Excel source addresses stay explicit and top-level locators cannot cross source documents', () => {
  const value=detail([])
  value.sources![0].file_name='source.xlsx'
  value.locators=[{type:'sheet',mapping:'exact',sheet:'Measurements',row_start:9,row_end:9,column_start:4,column_end:4}]
  assert.deepEqual(evidenceSourceLocate(value,0,1).locators,value.locators)
  value.sources!.push({...value.sources![0],knowledge_id:'another-document',source_sha256:'d'.repeat(64)})
  assert.deepEqual(evidenceSourceLocate(value,1,2).locators,[])
})

test('selected chart columns retain their actual ordered unit metadata and declaration level', () => {
  const value = detail([])
  value.manifest = { result: { columns: ['period', 'amount'], rows: [['2024', '19.50']] }, column_metadata: [
    { index: 0, name: 'period', display_name: 'Observed period', unit_metadata: { status: 'unknown', evidence_level: 'none', reason: 'No unit declaration' } },
    { index: 4, name: 'amount', display_name: 'Measured amount', unit_metadata: { status: 'derived', symbol: 'kg', evidence_level: 'deterministic_expression', expression: 'mass * factor', reason: 'Deterministic expression' } },
  ] }
  const columns = evidenceTableColumnInfo(value)
  assert.deepEqual(evidenceTableColumns(value), ['Observed period', 'Measured amount'])
  assert.equal(columns[0].unit?.status, 'unknown')
  assert.equal(columns[1].unit?.evidence_level, 'deterministic_expression')
  assert.equal(columns[1].unit?.expression, 'mass * factor')
  assert.equal(evidenceUnitSymbol(columns[1].unit), 'kg')
  assert.equal(evidenceUnitSymbol({ status: 'unknown', symbol: 'guessed' }), '')
  assert.equal(evidenceUnitSymbol({ status: 'mixed', symbol: 'guessed' }), '')
  value.manifest.column_metadata!.reverse()
  assert.equal(evidenceTableColumnInfo(value)[0].unit, undefined, 'another selected column cannot lend its unit by matching a title')
  assert.deepEqual(evidenceTableColumns(value), ['period', 'amount'])
})

test('unknown chart units and calculation input scope survive detail transport without being promoted to contributors', () => {
  const value = detail([])
  value.manifest = {
    result: { columns: ['count'], rows: [['17']] },
    column_metadata: [{ name: 'count', unit_metadata: { status: 'declared', symbol: 'row', evidence_level: 'reviewed_plan_declaration' } }],
    computation: { sql: 'SELECT COUNT(*) FROM t', input_scope: 'loaded_returned_rows', input_count: 8,
      coverage: { loaded_rows: 8 }, source_truncated: true, sql_result_lineage_verified: false,
      mappings: [{ source_rows: 8, source_columns: 4, projection_output_rows: 2, mapping: { orientation: 'columns', x_index: 0, y_indexes: [2], value_indexes: [1, 3] } }] },
  }
  const replay = JSON.parse(JSON.stringify(value)) as EvidenceDetail
  assert.equal(evidenceTableColumnInfo(replay)[0].unit?.evidence_level, 'reviewed_plan_declaration')
  assert.equal(replay.manifest?.computation?.sql, 'SELECT COUNT(*) FROM t')
  assert.equal(replay.manifest?.computation?.input_scope, 'loaded_returned_rows')
  assert.equal(replay.manifest?.computation?.source_truncated, true)
  assert.equal(replay.manifest?.computation?.sql_result_lineage_verified, false)
  assert.deepEqual(replay.manifest?.computation?.mappings?.[0].mapping.value_indexes, [1, 3])
})
