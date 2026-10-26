import assert from 'node:assert/strict'
import test from 'node:test'
import { directKnowledgeBaseIds, sourceOptions, validateSourceSelection, workbenchSourceLabel } from './workbenchDataSources'
import type { ResearchWorkbench, ResearchWorkbenchSource } from '../../api/workspaces'

const source = (id: string, more = {}): ResearchWorkbenchSource => ({ id: `binding-${id}`, source_type: 'knowledge_base', source_id: id, knowledge_base_id: id, status: 'active', can_read: true, can_manage_source: false, delegated: false, view_modules: [], ...more })
const workbench = (sources: ResearchWorkbenchSource[]): ResearchWorkbench => ({ id: 'wb', name: 'Research', knowledge_base_id: '', scope_version: 2, sources_revision: 3, sources } as ResearchWorkbench)

test('zero, one and eight authorized sources are valid; duplicates and foreign sources are rejected', () => {
  const available = Array.from({ length: 9 }, (_, index) => source(`library-${index}`))
  for (const count of [0, 1, 8]) assert.equal(validateSourceSelection(available.slice(0, count), available), '')
  assert.match(validateSourceSelection(available, available), /最多/)
  assert.match(validateSourceSelection([available[0]!, available[0]!], available), /重复/)
  assert.match(validateSourceSelection([source('foreign')], available), /不可用/)
})

test('chat includes only direct active sources and deduplicates the same backing KB', () => {
  assert.deepEqual(directKnowledgeBaseIds(workbench([
    source('one'), source('central', { source_type: 'central_dataset', knowledge_base_id: 'one' }),
    source('delegated', { delegated: true }), source('unreadable', { can_read: false }), source('deleted', { status: 'removed' }),
  ])), ['one'])
  assert.deepEqual(directKnowledgeBaseIds(workbench([])), [])
})

test('the center backing is offered once and existing restricted bindings remain visible without becoming new selectable sources', () => {
  const options = sourceOptions([{ id: 'center-kb', name: 'Default' }, { id: 'notes', name: 'Notes' }], [{ id: 'center', kind: 'default', name: 'Center', knowledge_base_id: 'center-kb' }], [source('restricted', { can_read: false })])
  assert.deepEqual(options.map(item => [item.source_type, item.source_id, item.disabled]), [['central_dataset', 'center', false], ['knowledge_base', 'notes', false], ['knowledge_base', 'restricted', true]])
  assert.equal(workbenchSourceLabel(workbench([]), []), '未关联来源')
})
