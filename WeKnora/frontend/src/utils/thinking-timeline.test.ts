import assert from 'node:assert/strict'
import test from 'node:test'
import type { ComposerTranslation } from 'vue-i18n'
import {
  formatThoughtDuration,
  getThinkingTimelineStepTitle,
  isThinkingTimelineStep,
  structuredOperation,
} from './thinking-timeline.ts'

// Echo translator: returns the key plus interpolation values for assertions.
const t = ((key: string, values?: Record<string, unknown>) =>
  values ? `${key}|${JSON.stringify(values)}` : key) as unknown as ComposerTranslation

test('structured operations come from arguments or the split tool name', () => {
  assert.equal(structuredOperation('structured_data', { operation: 'query_series' }), 'query_series')
  assert.equal(structuredOperation('structured_data', '{"operation":"inspect_source"}'), 'inspect_source')
  assert.equal(structuredOperation('structured_list_series', {}), 'list_series')
  assert.equal(structuredOperation('read_document', {}), '')
})

test('internal runtime steps are hidden, real source steps are shown', () => {
  assert.equal(isThinkingTimelineStep('structured_data', { operation: 'describe_tools' }), false)
  assert.equal(isThinkingTimelineStep('expand_task_tools', {}), false)
  assert.equal(isThinkingTimelineStep('thinking', {}), false)
  assert.equal(isThinkingTimelineStep('', {}), false)
  assert.equal(isThinkingTimelineStep('structured_data', { operation: 'query_series' }), true)
  assert.equal(isThinkingTimelineStep('read_document', {}), true)
  assert.equal(isThinkingTimelineStep('bind_document_scope', {}), true)
})

test('step titles are readable and protocol-based', () => {
  assert.equal(getThinkingTimelineStepTitle(t, { tool_name: 'structured_data', arguments: { operation: 'inspect_source' } }),
    'agentStream.thinkingTimeline.steps.inspectSource')
  assert.equal(getThinkingTimelineStepTitle(t, { tool_name: 'structured_data', arguments: { operation: 'unknown_op' } }),
    'agentStream.thinkingTimeline.steps.structuredFallback')
  assert.equal(getThinkingTimelineStepTitle(t, { tool_name: 'read_document', tool_data: { document: { title: '周报.pptx' } } }),
    'agentStream.thinkingTimeline.steps.readDocumentNamed|{"title":"周报.pptx"}')
  assert.equal(getThinkingTimelineStepTitle(t, { tool_name: 'read_document' }), 'agentStream.thinkingTimeline.steps.readDocument')
  assert.equal(getThinkingTimelineStepTitle(t, { tool_name: 'some_new_tool' }), 'agentStream.thinkingTimeline.steps.generic')
})

test('thought duration formats seconds and minutes, empty when unknown', () => {
  assert.equal(formatThoughtDuration(t, 0), '')
  assert.equal(formatThoughtDuration(t, undefined), '')
  assert.equal(formatThoughtDuration(t, 400), 'agentStream.thinkingTimeline.thoughtForSeconds|{"seconds":1}')
  assert.equal(formatThoughtDuration(t, 12_400), 'agentStream.thinkingTimeline.thoughtForSeconds|{"seconds":12}')
  assert.equal(formatThoughtDuration(t, 125_000), 'agentStream.thinkingTimeline.thoughtForMinutes|{"minutes":2,"seconds":5}')
})
