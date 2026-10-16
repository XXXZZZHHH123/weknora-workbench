import assert from 'node:assert/strict'
import test from 'node:test'
import { isolatedWorkbenchScope, templateById, workbenchModuleNames, workbenchTemplates } from './workbenchTemplates'

test('four templates define generic module slots and have no business records', () => {
  assert.deepEqual(workbenchTemplates.map(template => template.id), ['industrial', 'data-tracking', 'topic', 'blank'])
  for (const template of workbenchTemplates) {
    assert.ok(template.modules.length > 0)
    assert.ok(template.modules.every(id => workbenchModuleNames[id]))
    assert.deepEqual(Object.keys(template).sort(), ['description', 'id', 'modules', 'name'])
  }
  assert.deepEqual(templateById('unknown-future-template').modules, ['sources', 'assistant'])
})

test('an isolated overview must identify the actual workbench and knowledge base', () => {
  assert.equal(isolatedWorkbenchScope({ id: 'test-workbench', knowledge_base_id: 'test-kb', workspace_mode: 'isolated' }, 'test-workbench', 'test-kb'), true)
  assert.equal(isolatedWorkbenchScope({ id: 'old-workbench', knowledge_base_id: 'test-kb', workspace_mode: 'isolated' }, 'test-workbench', 'test-kb'), false)
  assert.equal(isolatedWorkbenchScope({ id: 'test-workbench', knowledge_base_id: 'other-kb', workspace_mode: 'isolated' }, 'test-workbench', 'test-kb'), false)
  assert.equal(isolatedWorkbenchScope({ id: 'test-workbench', knowledge_base_id: 'test-kb', workspace_mode: 'legacy' }, 'test-workbench', 'test-kb'), false)
})
