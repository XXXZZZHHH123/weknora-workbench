import assert from 'node:assert/strict'
import test from 'node:test'
import { authenticateResearchFrame, researchFramePath } from './researchFrameSession'

test('cookie exchanges finish in order across account changes; stale calls never mount', async () => {
  const original = globalThis.fetch
  const calls: Array<{headers: Headers; finish: (value: Response) => void}> = []
  globalThis.fetch = async (_url, options) => new Promise(resolve => {
    calls.push({headers:new Headers(options?.headers),finish:resolve})
  })
  try {
    let active = 'a'
    const first = authenticateResearchFrame('secret-a',1,() => active === 'a')
    await new Promise(setImmediate)
    active = 'b'
    const second = authenticateResearchFrame('secret-b',2,() => active === 'b')
    await new Promise(setImmediate)
    assert.equal(calls.length,1)
    calls[0]!.finish(new Response('{}'))
    assert.equal(await first,false)
    await new Promise(setImmediate)
    assert.equal(calls[1]!.headers.get('authorization'),'Bearer secret-b')
    assert.equal(calls[1]!.headers.get('x-tenant-id'),'2')
    calls[1]!.finish(new Response('{}'))
    assert.equal(await second,true)
    assert.equal(await authenticateResearchFrame('stale',1,() => false),false)
    assert.equal(calls.length,2)
  } finally { globalThis.fetch = original }
})

test('a rejected exchange cannot block the next verified login', async () => {
  const original = globalThis.fetch
  let calls = 0
  globalThis.fetch = async () => new Response('{}',{status:++calls === 1 ? 401 : 200})
  try {
    await assert.rejects(authenticateResearchFrame('expired',1,() => true),/登录核验失败/)
    assert.equal(await authenticateResearchFrame('current',2,() => true),true)
  } finally { globalThis.fetch = original }
})

test('navigation is a direct scoped path containing no auth or bootstrap hop', () => {
  assert.equal(researchFramePath('workbench-a','diagram'),'/research/workbenches/workbench-a/sn/diagram')
  assert.equal(researchFramePath('workbench-a','home'),'/research/workbenches/workbench-a/')
  assert.equal(researchFramePath('','home'),'')
  assert.equal(researchFramePath('workbench-a','../other'),'')
  assert.equal(researchFramePath('workbench-a','toString'),'')
})
