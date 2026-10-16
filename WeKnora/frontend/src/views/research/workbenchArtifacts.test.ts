import assert from 'node:assert/strict'
import test from 'node:test'
import { observationChart, reusableTemplate, structureBody, structureDetails, structureTree } from './workbenchArtifacts'

test('observations use actual numbers and date spacing; missing or non-finite values break the curve', () => {
  const chart = observationChart([
    { as_of: '2024-01-01T00:00:00Z', value: -5 },
    { as_of: '2024-01-02T00:00:00Z', value: 0 },
    { as_of: '2024-01-03T00:00:00Z', value: null },
    { as_of: '2024-01-05T00:00:00Z', value: 5 },
    { as_of: '2024-01-06T00:00:00Z', value: Infinity },
  ], 100, 100)
  assert.deepEqual(chart, { segments: ['10.00,90.00 26.00,50.00', '74.00,10.00'], min: -5, max: 5 })
  assert.equal(observationChart([{ value: '#N/A' }, { value: null }]).segments.length, 0)
})

test('a constant real series plots at the middle without a fabricated trend', () => {
  const result = observationChart([{ value: 7 }, { value: 7 }], 100, 100)
  assert.deepEqual(result.segments, ['10.00,50.00 90.00,50.00'])
  assert.equal(result.min, 7)
  assert.equal(result.max, 7)
})

test('structure parent relations persist separately from the user text, including ordinary legacy text', () => {
  const parent = { id: 'parent', title: 'Test-only parent', body: structureBody('parent explanation') }
  const child = { id: 'child', title: 'Test-only child', body: structureBody('child explanation', 'parent') }
  assert.deepEqual(structureDetails(child), { text: 'child explanation', parent_id: 'parent' })
  assert.deepEqual(structureDetails({ id: 'plain', title: 'plain', body: 'ordinary text' }), { text: 'ordinary text', parent_id: '' })
  assert.deepEqual(structureTree([parent, child]).map(node => node.depth), [0, 1])
  const cyclic = structureTree([{ ...parent, body: structureBody('', 'child') }, child])
  assert.ok(cyclic.every(node => node.depth <= 1))
})

test('reusable template preserves module slots and tree shape while stripping all business records and source IDs', () => {
  const records = [
    { id: 'private-business-node-1', title: 'sensitive topic', body: structureBody('sensitive value'), source_ids: ['private-source'] },
    { id: 'private-business-node-2', title: 'sensitive conclusion', body: structureBody('other sensitive text', 'private-business-node-1') },
  ]
  const output = reusableTemplate('industrial', ['data', 'structure'], records)
  assert.deepEqual(output.structure_slots, [{ key: 'node_1', parent_key: null, title: '' }, { key: 'node_2', parent_key: 'node_1', title: '' }])
  assert.deepEqual(output.modules, ['data', 'structure'])
  const serialized = JSON.stringify(output)
  for (const secret of ['private-business', 'private-source', 'sensitive']) assert.equal(serialized.includes(secret), false)
})
