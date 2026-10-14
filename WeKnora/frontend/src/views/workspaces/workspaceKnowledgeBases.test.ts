import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildWorkspaceNavigation } from '../../components/workspaceNavigation'
import { validateWorkbenchSource } from './workspaceForm'
import { workspaceKnowledgeBases } from './workspaceKnowledgeBases'

const own = { id: 'owned', name: '自有资料', description: '完整详情', type: 'document' }
const shared = { knowledge_base: { id: 'shared', name: '共享资料', type: 'document' } }

test('verified shared knowledge bases can be preselected to create a workbench', () => {
  const libraries = workspaceKnowledgeBases([own], [shared], '1:user', '1:user')
  assert.deepEqual(libraries.map(kb => kb.id), ['owned', 'shared'])
  assert.equal(validateWorkbenchSource('shared', libraries.map(kb => kb.id)), '')
})

test('global knowledge-base navigation opens verified shared resources directly', () => {
  const libraries = workspaceKnowledgeBases([], [shared], '1:user', '1:user')
  assert.deepEqual(buildWorkspaceNavigation([], libraries, []).knowledgeBases, [
    { id: 'shared', title: '共享资料', path: '/platform/knowledge-bases/shared' },
  ])
})

test('resources use real IDs to remove repeated shares and preserve own details', () => {
  const duplicateOwn = { knowledge_base: { id: own.id, name: '旧共享名称' } }
  const libraries = workspaceKnowledgeBases([own, own], [duplicateOwn, shared, shared], '1:user', '1:user')
  assert.equal(libraries[0]?.description, own.description)
  assert.deepEqual(libraries, [own, shared.knowledge_base])
  assert.deepEqual(duplicateOwn.knowledge_base, { id: own.id, name: '旧共享名称' })
})

test('old shared lists cannot become candidates after tenant or account switches', () => {
  for (const identity of ['2:user', '1:other', '']) {
    assert.deepEqual(workspaceKnowledgeBases([own], [shared], identity, '1:user'), [own])
  }
  assert.deepEqual(workspaceKnowledgeBases([own], [shared], '1:user', ''), [own])
  assert.deepEqual(workspaceKnowledgeBases([own], [shared], '', ''), [own])
})

test('empty identifiers and absent shared records do not invent knowledge bases', () => {
  const libraries = workspaceKnowledgeBases([{ id: '', name: '无效' }, own], [
    { knowledge_base: null }, { knowledge_base: { id: '', name: '无效共享' } }, shared,
  ], '1:user', '1:user')
  assert.deepEqual(libraries, [own, shared.knowledge_base])
  assert.deepEqual(workspaceKnowledgeBases([], [], '1:user', '1:user'), [])
})
