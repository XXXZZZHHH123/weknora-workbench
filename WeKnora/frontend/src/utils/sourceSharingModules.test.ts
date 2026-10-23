import assert from 'node:assert/strict'
import test from 'node:test'
import { sourceSharingModulesDraft } from './sourceSharingModules'

test('missing or legacy source configuration never implies any page grant', () => {
  for (const value of [undefined, null, true, false, { approved: true }, 'market']) {
    assert.deepEqual(sourceSharingModulesDraft(value), [])
  }
})

test('page drafts contain only supported explicit modules and do not mutate API data', () => {
  const input = ['diagram', 'market', 'all', 'market', null, 'download']
  const draft = sourceSharingModulesDraft(input)
  assert.deepEqual(draft, ['market', 'diagram'])
  draft.push('home')
  assert.deepEqual(input, ['diagram', 'market', 'all', 'market', null, 'download'])
})
