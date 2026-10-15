import assert from 'node:assert/strict'
import { test } from 'node:test'
import { validateWorkbenchSource, validateWorkspaceName, workspaceErrorMessage, workspaceIdentity } from './workspaceForm'

test('workspace names accept ordinary Chinese names and reject blank, long and control-character input', () => {
  assert.equal(validateWorkspaceName('  库存研究  '), '')
  assert.notEqual(validateWorkspaceName(' \t '), '')
  assert.equal(validateWorkspaceName('研'.repeat(80)), '')
  assert.notEqual(validateWorkspaceName('研'.repeat(81)), '')
  assert.notEqual(validateWorkspaceName('研究\u0000台'), '')
})

test('a workbench requires a currently accessible knowledge base', () => {
  assert.equal(validateWorkbenchSource('kb-a', ['kb-a', 'kb-b']), '')
  assert.notEqual(validateWorkbenchSource('', ['kb-a']), '')
  assert.notEqual(validateWorkbenchSource('kb-b', ['kb-a']), '')
  assert.notEqual(validateWorkbenchSource('kb-a', []), '')
})

test('both user and tenant changes produce a different workspace identity', () => {
  assert.notEqual(workspaceIdentity(1, 'a'), workspaceIdentity(2, 'a'))
  assert.notEqual(workspaceIdentity(1, 'a'), workspaceIdentity(1, 'b'))
  assert.equal(workspaceIdentity(1, 'a'), workspaceIdentity('1', 'a'))
})

test('plain API errors retain their message for permission and retry feedback', () => {
  assert.equal(workspaceErrorMessage({ message: '知识库已不可访问' }), '知识库已不可访问')
  assert.equal(workspaceErrorMessage(new Error('连接失败')), '连接失败')
  assert.equal(workspaceErrorMessage(null), '请求失败，请重试。')
})
