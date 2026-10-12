import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createMaterialClient } from './client'

test('requests pin the verified account and workspace, without identity in JSON', async () => {
  const scope = { token: 'private-token', userId: 'u', tenantId: '7' }
  const fake = (async (url, options) => {
    assert.equal(url, '/research/api/library/hash/process')
    const headers = new Headers(options?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer private-token')
    assert.equal(headers.get('X-Tenant-ID'), '7')
    assert.equal(headers.get('X-Research-User'), 'u')
    assert.equal(options?.body, JSON.stringify({ purpose: 'structured' }))
    return new Response(JSON.stringify({ operation: { status: 'running' } }))
  }) as typeof fetch
  await createMaterialClient(scope, () => scope, fake).post('/api/library/hash/process', { purpose: 'structured' })
})
test('a late response from an earlier workspace cannot enter the new workspace', async () => {
  const scope = { token: 'token', userId: 'u', tenantId: '7' }
  let active = scope
  const fake = (async () => { active = { ...scope, tenantId: '8' }; return new Response('{}') }) as typeof fetch
  await assert.rejects(createMaterialClient(scope, () => active, fake).get('/api/library'), /空间已切换/)
})
test('server permission failures remain visible rather than becoming an empty successful listing', async () => {
  const scope = { token: 'token', userId: 'u', tenantId: '7' }
  const fake = (async () => new Response(JSON.stringify({ detail: '没有知识库权限' }), { status: 403 })) as typeof fetch
  await assert.rejects(createMaterialClient(scope, () => scope, fake).get('/api/library'), /知识库权限/)
})

test('workspace changes during deferred JSON decoding reject the old result', async () => {
  const scope = { token: 'token', userId: 'u', tenantId: '7' }
  let active = scope
  let finishBody!: (body: unknown) => void
  let startedBody!: () => void
  const started = new Promise<void>(resolve => { startedBody = resolve })
  const body = new Promise(resolve => { finishBody = resolve })
  const fake = (async () => ({ ok: true, json: () => { startedBody(); return body } } as Response)) as typeof fetch
  const result = createMaterialClient(scope, () => active, fake).get('/api/library')
  const rejected = assert.rejects(result, /空间已切换/)
  await started
  active = { ...scope, tenantId: '8' }
  finishBody({ assets: [{ filename: 'private-old-space.pdf' }] })
  await rejected
})

test('account changes during deferred original download reject the old bytes', async () => {
  const scope = { token: 'token', userId: 'u', tenantId: '7' }
  let active = scope
  let finishBody!: (body: Blob) => void
  let startedBody!: () => void
  const started = new Promise<void>(resolve => { startedBody = resolve })
  const body = new Promise<Blob>(resolve => { finishBody = resolve })
  const fake = (async () => ({ ok: true, blob: () => { startedBody(); return body } } as Response)) as typeof fetch
  const result = createMaterialClient(scope, () => active, fake).download('hash')
  const rejected = assert.rejects(result, /账号或空间已切换/)
  await started
  active = { ...scope, userId: 'other' }
  finishBody(new Blob(['private-old-account']))
  await rejected
})
