import assert from 'node:assert/strict'
import test from 'node:test'
import { createIsolatedResearchClient, type IsolatedResearchIdentity } from './isolatedResearch'

function identity(): IsolatedResearchIdentity { return { token: 'isolated-test-token', tenantId: 'test-tenant', userId: 'test-user', workbenchId: 'new-workbench', knowledgeBaseId: 'new-kb' } }

test('isolated requests include the exact workbench, user, tenant and knowledge-base scope', async () => {
  const scope = identity()
  const calls: Array<{ url: string; options?: RequestInit }> = []
  const api = createIsolatedResearchClient(scope, () => scope, async (url, options) => {
    calls.push({ url: String(url), options })
    return Response.json({ indicators: [] })
  })
  await api.get('/indicators')
  assert.equal(calls[0]!.url, '/research/api/workbenches/new-workbench/indicators')
  const headers = new Headers(calls[0]!.options!.headers)
  assert.equal(headers.get('Authorization'), 'Bearer isolated-test-token')
  assert.equal(headers.get('X-Tenant-ID'), 'test-tenant')
  assert.equal(headers.get('X-Research-User'), 'test-user')
  assert.equal(headers.get('X-Knowledge-Base-ID'), 'new-kb')
  assert.equal(calls[0]!.options!.credentials, 'same-origin')
})

test('a changed user, tenant, knowledge base or workbench rejects a delayed response', async () => {
  for (const key of ['tenantId', 'userId', 'workbenchId', 'knowledgeBaseId'] as const) {
    let active = identity()
    let resolve!: (response: Response) => void
    const api = createIsolatedResearchClient(active, () => active, () => new Promise(done => { resolve = done }))
    const result = api.get('/overview')
    active = { ...active, [key]: 'changed-scope' }
    resolve(Response.json({ workbench: { id: 'old-workbench' } }))
    await assert.rejects(result, /已切换/)
  }
})

test('a changed identity is rejected before a request and cannot mutate the captured scope object', async () => {
  const supplied = identity()
  let calls = 0
  const api = createIsolatedResearchClient(supplied, () => supplied, async () => { calls++; return Response.json({}) })
  supplied.knowledgeBaseId = 'different-kb'
  await assert.rejects(api.post('/notes', { title: 'test-only draft' }), /已切换/)
  assert.equal(calls, 0)
})

test('legacy, global and path traversal endpoints cannot be called by the isolated client', async () => {
  const scope = identity()
  let calls = 0
  const api = createIsolatedResearchClient(scope, () => scope, async () => { calls++; return Response.json({}) })
  for (const path of ['/api/sn/indicators', '/auth/bootstrap', '/indicators/../../sn', '//other.example/path', 'https://other.example/overview']) await assert.rejects(api.get(path), /无效/)
  assert.equal(calls, 0)
})

test('local source preparation routes remain under the same isolated endpoint and preserve the payload', async () => {
  const scope = identity()
  const calls: Array<{ url: string; options?: RequestInit }> = []
  const api = createIsolatedResearchClient(scope, () => scope, async (url, options) => { calls.push({ url: String(url), options }); return Response.json({ status: 'processing', id: 'job' }) })
  await api.put('/sources/new-source/role', { source_role: 'acquisition-plan' })
  await api.post('/source-imports/new-import/preview', { mapping: { sheet: 'test sheet', blocks: [] }, source_role: 'observations' })
  await api.get('/scan-jobs/job')
  assert.ok(calls.every(call => call.url.startsWith('/research/api/workbenches/new-workbench/')))
  assert.equal(calls[0]!.options!.method, 'PUT')
  assert.deepEqual(JSON.parse(calls[0]!.options!.body as string), { source_role: 'acquisition-plan' })
  assert.equal(JSON.parse(calls[1]!.options!.body as string).source_role, 'observations')
})

test('API denial preserves the server explanation instead of creating a false ready state', async () => {
  const scope = identity()
  const api = createIsolatedResearchClient(scope, () => scope, async () => Response.json({ detail: '资料不属于当前知识库' }, { status: 403 }))
  await assert.rejects(api.get('/sources'), /资料不属于当前知识库/)
})

test('credential rotation keeps a client in the same resource scope and uses the latest token', async () => {
  let active = identity(), release!: (value: Response) => void
  const calls: string[] = []
  const client = createIsolatedResearchClient(active, () => active, async (_, options) => {
    calls.push(new Headers(options?.headers).get('Authorization')!)
    if (calls.length === 1) return new Promise(resolve => { release = resolve })
    return Response.json({ ok: true })
  })
  const pending = client.get('/overview')
  active = { ...active, token: 'rotated' }; release(Response.json({ ok: true }))
  assert.deepEqual(await pending, { ok: true }); await client.get('/overview')
  assert.deepEqual(calls, ['Bearer isolated-test-token', 'Bearer rotated'])
})

test('logout rejects pending research responses even if the resource IDs remain the same', async () => {
  let active = identity(), release!: (value: Response) => void
  const client = createIsolatedResearchClient(active, () => active, async () => new Promise(resolve => { release = resolve }))
  const pending = client.get('/overview'); active = { ...active, token: '' }; release(Response.json({ ok: true }))
  await assert.rejects(pending, /已切换/)
})

test('publishing uses the same bound workbench while generic diagram paths remain unavailable', async () => {
  const scope = identity(), calls: string[] = []
  const api = createIsolatedResearchClient(scope, () => scope, async url => { calls.push(String(url)); return Response.json({ published: true }) })
  await api.post('/diagram/publish', {})
  assert.deepEqual(calls, ['/research/api/workbenches/new-workbench/diagram/publish'])
  for (const path of ['/diagram/catalog', '/diagram/templates', '/diagram/publish-other', '/diagram/publish/../../sources']) await assert.rejects(api.get(path), /无效/)
})
