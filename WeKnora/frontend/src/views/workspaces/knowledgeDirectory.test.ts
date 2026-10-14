import assert from 'node:assert/strict'
import { test } from 'node:test'
import { knowledgeDirectorySelection, newKnowledgeFolderPath } from './knowledgeDirectory'

test('all preview clears the selected parent and stays in the current knowledge-base route', () => {
  assert.deepEqual(knowledgeDirectorySelection({ directory: 'all', folder: '资料/旧目录' }), { all: true, path: '' })
  assert.deepEqual(knowledgeDirectorySelection({ folder: '资料/周报' }), { all: false, path: '资料/周报' })
  assert.deepEqual(knowledgeDirectorySelection({}), { all: false, path: '' })
})

test('folder creation enforces the selected parent, sibling uniqueness and path limits', () => {
  assert.deepEqual(newKnowledgeFolderPath('资料', ' 周报 ', []), { path: '资料/周报', error: '' })
  assert.notEqual(newKnowledgeFolderPath('资料', '周报', ['资料/周报']).error, '')
  assert.equal(newKnowledgeFolderPath('其他', '周报', ['资料/周报']).error, '')
  for (const name of ['', '.', '..', 'bad/path', 'bad\\path', 'bad\u0000name', 'name.']) assert.notEqual(newKnowledgeFolderPath('', name, []).error, '')
  assert.equal(newKnowledgeFolderPath('', '研'.repeat(42), []).error, '')
  assert.notEqual(newKnowledgeFolderPath('', '研'.repeat(43), []).error, '')
  assert.notEqual(newKnowledgeFolderPath(Array(16).fill('a').join('/'), 'b', []).error, '')
})
