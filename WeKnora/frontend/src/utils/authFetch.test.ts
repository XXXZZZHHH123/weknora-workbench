import assert from 'node:assert/strict'
import test from 'node:test'
import { fetchWithAuthRetry } from './authFetch'

test('raw clients replay a 401 once with the new token and original scope/body', async () => {
  const calls: RequestInit[] = []
  let refreshes = 0
  const response = await fetchWithAuthRetry('/research/api/test', { method: 'POST', body: 'same-body', headers: { 'X-Tenant-ID': '7', 'X-Knowledge-Base-ID': 'kb' } }, {
    token: 'old', isCurrent: () => true,
    refresh: async token => { assert.equal(token, 'old'); refreshes++; return 'new' },
    fetcher: async (_, options) => { calls.push(options!); return new Response('{}', { status: calls.length === 1 ? 401 : 200 }) },
  })
  assert.equal(response.status, 200); assert.equal(refreshes, 1)
  assert.deepEqual(calls.map(call => new Headers(call.headers).get('Authorization')), ['Bearer old', 'Bearer new'])
  for (const call of calls) { assert.equal(call.body, 'same-body'); assert.equal(new Headers(call.headers).get('X-Tenant-ID'), '7'); assert.equal(new Headers(call.headers).get('X-Knowledge-Base-ID'), 'kb') }
})
test('403 and repeated 401 never loop', async () => {
  for (const status of [401, 403]) {
    let calls = 0, refreshes = 0
    const result = await fetchWithAuthRetry('/scope', {}, { token: 'old', isCurrent: () => true,
      refresh: async () => { refreshes++; return 'new' }, fetcher: async () => { calls++; return new Response('', { status }) } })
    assert.equal(result.status, status); assert.equal(calls, status === 401 ? 2 : 1); assert.equal(refreshes, status === 401 ? 1 : 0)
  }
})
test('switching identity during refresh stops the replay', async () => {
  let current = true, calls = 0
  await assert.rejects(fetchWithAuthRetry('/scope', {}, { token: 'old', isCurrent: () => current,
    fetcher: async () => { calls++; return new Response('', { status: 401 }) }, refresh: async () => { current = false; return 'new' } }), /已切换/)
  assert.equal(calls, 1)
})
