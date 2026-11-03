import assert from 'node:assert/strict'
import test from 'node:test'
Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => null, removeItem() {} } })
Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/research', href: '' } } })
import { analysisJudgmentId, authenticateResearchFrame, researchFramePath } from './researchFrameSession'

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
    await assert.rejects(authenticateResearchFrame('expired',1,() => true),/log in again/)
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

test('analysis results reopen through the authorized native judgment page', () => {
  assert.equal(researchFramePath('workbench-a','judgment','analysis-job-1'),'/research/workbenches/workbench-a/sn/judgment?analysis_job=analysis-job-1')
  assert.equal(researchFramePath('workbench-a','home','analysis-job-1'),'/research/workbenches/workbench-a/')
  assert.equal(researchFramePath('workbench-a','judgment',['analysis-job-1']),'/research/workbenches/workbench-a/sn/judgment')
  assert.equal(researchFramePath('workbench-a','judgment','../secret'),'/research/workbenches/workbench-a/sn/judgment')
  assert.equal(researchFramePath('workbench-a','judgment',undefined,'42'),'/research/workbenches/workbench-a/sn/judgment?judgment_id=42')
  assert.equal(analysisJudgmentId('9223372036854775807'),'9223372036854775807')
  assert.equal(analysisJudgmentId('9223372036854775808'),'')
  assert.equal(analysisJudgmentId(9007199254740993),'')
})

test('diagram results reopen through their own authorized page and preserve only valid layout identifiers', () => {
  assert.equal(researchFramePath('workbench-a','diagram','diagram-job-1'),'/research/workbenches/workbench-a/sn/diagram?analysis_job=diagram-job-1')
  assert.equal(researchFramePath('workbench-a','diagram',undefined,undefined,'7'),'/research/workbenches/workbench-a/sn/diagram?template=7')
  assert.equal(researchFramePath('workbench-a','diagram','../invalid',undefined,'-1'),'/research/workbenches/workbench-a/sn/diagram')
})
