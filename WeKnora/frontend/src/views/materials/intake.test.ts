import { test } from 'node:test'
import assert from 'node:assert/strict'
import { offerIntake, takeIntake, isTable, isStructuredFile } from './intake'

test('file handoff is consumed once, with copies of its scope and options', () => {
  const files = [{ name: 'original.xlsx' }] as File[]
  const identity = { userId: 'a', tenantId: '7' }, tags = ['tag-1']
  offerIntake(identity, files, { kbId: 'kb', folder: 'reports', tagIds: tags })
  identity.userId = 'other'; files.length = 0; tags.push('other')
  const result = takeIntake({ userId: 'a', tenantId: '7' })
  assert.equal(result?.files[0].name, 'original.xlsx')
  assert.deepEqual(result?.context.tagIds, ['tag-1'])
  assert.equal(takeIntake({ userId: 'a', tenantId: '7' }), undefined)
})
test('another user or workspace cannot receive dropped private files', () => {
  for (const target of [{ userId: 'b', tenantId: '7' }, { userId: 'a', tenantId: '8' }]) {
    offerIntake({ userId: 'a', tenantId: '7' }, [{ name: 'private.pdf' }] as File[])
    assert.equal(takeIntake(target), undefined)
    assert.equal(takeIntake({ userId: 'a', tenantId: '7' }), undefined)
  }
})
test('an abandoned transfer expires rather than reappearing much later', () => {
  offerIntake({ userId: 'a', tenantId: '7' }, [{ name: 'private.pdf' }] as File[])
  assert.equal(takeIntake({ userId: 'a', tenantId: '7' }, Date.now() + 11 * 60_000), undefined)
})
test('format recommendations distinguish exact structured capability from research-supported tables', () => {
  for (const file of ['tin.xlsx', 'TIN.XLS', 'tin.xlsm', 'tin.csv', 'tin.tsv']) assert.equal(isTable(file), true)
  assert.equal(isStructuredFile('tin.xlsx'), true)
  assert.equal(isStructuredFile('tin.xls'), false)
  assert.equal(isTable('tin.pdf'), false)
})
