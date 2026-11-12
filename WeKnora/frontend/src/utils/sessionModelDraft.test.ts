import assert from 'node:assert/strict'
import test from 'node:test'
import { sessionModelDraftKey, readSessionModelDraft, writeSessionModelDraft } from './sessionModelDraft'

test('model drafts require a complete identity and use unambiguous account/workspace/session keys', () => {
  assert.equal(sessionModelDraftKey('', 1, 'session'), '')
  assert.equal(sessionModelDraftKey('user', null, 'session'), '')
  assert.equal(sessionModelDraftKey('user', 1, ''), '')
  const keys = new Set([
    sessionModelDraftKey('user', 1, 'a'), sessionModelDraftKey('user', 1, 'b'),
    sessionModelDraftKey('other', 1, 'a'), sessionModelDraftKey('user', 2, 'a'),
    sessionModelDraftKey('a:b', 1, 'c'), sessionModelDraftKey('a', 1, 'b:c'),
  ])
  assert.equal(keys.size, 6)
})

test('browser persistence restores and clears only the requested session and tolerates unavailable storage', () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const data = new Map<string, string>()
  try {
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
      getItem: (key: string) => data.get(key) ?? null,
      setItem: (key: string, value: string) => data.set(key, value),
      removeItem: (key: string) => data.delete(key),
    } })
    writeSessionModelDraft('a', 'model-a')
    writeSessionModelDraft('b', 'model-b')
    writeSessionModelDraft('', 'ignored')
    assert.equal(readSessionModelDraft('a'), 'model-a')
    writeSessionModelDraft('a', '')
    assert.equal(readSessionModelDraft('a'), '')
    assert.equal(readSessionModelDraft('b'), 'model-b')
    assert.equal(data.size, 1)
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('blocked') } })
    assert.doesNotThrow(() => writeSessionModelDraft('a', 'model-a'))
    assert.equal(readSessionModelDraft('a'), '')
  } finally {
    if (original) Object.defineProperty(globalThis, 'localStorage', original)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  }
})
