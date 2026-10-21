import assert from 'node:assert/strict'
import test from 'node:test'
import { knowledgeSearchSummary } from './knowledgeSearchSummary'

const t = (key: string, values?: Record<string, string | number>) => `${key}:${JSON.stringify(values || {})}`

test('source candidates do not masquerade as an empty content search', () => {
  const result = knowledgeSearchSummary(t, { display_type: 'document_info', requires_source_scope: true, documents: [{ id: 'a' }, { id: 'b' }] })
  assert.match(result, /candidateDocuments/)
  assert.match(result, /<strong>2<\/strong>/)
  assert.doesNotMatch(result, /noResults/)
})

test('source confirmation, empty catalog and missing result data retain distinct meanings', () => {
  assert.match(knowledgeSearchSummary(t, { display_type: 'document_info', source_plan_validated: true, documents: [{}] }), /confirmedDocuments/)
  assert.match(knowledgeSearchSummary(t, { display_type: 'document_info', documents: [{}] }), /foundDocuments/)
  assert.match(knowledgeSearchSummary(t, { display_type: 'document_info', documents: [] }), /noCandidateDocuments/)
  assert.equal(knowledgeSearchSummary(t, { notice: 'No content result yet' }), '')
  assert.equal(knowledgeSearchSummary(t, null), '')
})

test('actual empty searches remain visible, and actual results retain counts', () => {
  assert.match(knowledgeSearchSummary(t, { display_type: 'search_results', results: [], count: 0 }), /noResults/)
  assert.match(knowledgeSearchSummary(t, { count: 0 }), /noResults/)
  assert.match(knowledgeSearchSummary(t, { results: [{}], count: 0 }), /foundResults/)
  assert.match(knowledgeSearchSummary(t, { results: [{}, {}], kb_counts: { fileA: 1, fileB: 1 } }), /foundResultsFromFiles/)
})
