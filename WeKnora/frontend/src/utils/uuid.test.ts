import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'
import { test } from 'node:test'
import { createRandomUUID } from './uuid'
import { makeSteerClientId } from './steerId'

test('request IDs work on HTTP where crypto.randomUUID is unavailable', t => {
  const httpCrypto = { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) }
  t.mock.getter(globalThis, 'crypto', () => httpCrypto as Crypto)
  assert.equal(typeof crypto.randomUUID, 'undefined')

  const ids = Array.from({ length: 256 }, () => createRandomUUID())
  assert.equal(new Set(ids).size, ids.length)
  for (const id of ids) {
    assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  }
  assert.match(makeSteerClientId(), /^[0-9a-f-]{36}$/)
})
