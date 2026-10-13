import assert from 'node:assert/strict'
import test from 'node:test'
import { answerProgressStage } from './answerProgress.ts'

test('actual phase events are visible before an answer and never expose draft text', () => {
  assert.equal(answerProgressStage([]), 'waiting')
  assert.equal(answerProgressStage([{ type: 'progress', stage: 'sources' }]), 'sources')
  assert.equal(answerProgressStage([{ type: 'progress', stage: 'verification', content: 'unverified draft' }]), 'verification')
  assert.equal(answerProgressStage([{ type: 'progress', stage: 'made-up-stage' }]), 'waiting')
})

test('pending tools take priority, then resume the last real backend phase', () => {
  const events = [{ type: 'progress', stage: 'generation' }, {
    type: 'tool_call', tool_name: 'structured_data', arguments: { operation: 'query_series' }, pending: true,
  }]
  assert.equal(answerProgressStage(events), 'data')
  events[1].pending = false
  assert.equal(answerProgressStage(events), 'generation')
  assert.equal(answerProgressStage([{ type: 'tool_call', tool_name: 'structured_inspect_source', pending: true }]), 'verification')
  assert.equal(answerProgressStage([{ type: 'tool_call', tool_name: 'search_knowledge', pending: true }]), 'sources')
})

test('base structured tool uses real wrapper operation with inner execution arguments', () => {
  for (const [operation, expected] of [['query_series', 'data'], ['inspect_source', 'verification'], ['list_datasets', 'sources'], ['list_series', 'sources']] as const) {
    assert.equal(answerProgressStage([{
      type: 'tool_call', tool_name: 'structured_data', operation,
      arguments: { series_id: 'real-series', operation: 'rows' }, pending: true,
    }]), expected)
  }
})
