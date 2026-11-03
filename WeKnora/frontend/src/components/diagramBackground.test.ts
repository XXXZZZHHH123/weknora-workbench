import assert from 'node:assert/strict'
import { webcrypto } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const script = readFileSync(process.env.DIAGRAM_GENERATION_TEST_SOURCE || new URL('../../../../commodity-research/src/tin/web/static/diagram-generation.js', import.meta.url), 'utf8')
const taskId = 'd'.repeat(32)
function fixture(handler: (url: string, options: any) => Promise<any>) {
  let open = true
  const nodes: Record<string, any> = {
    'dg-md-drawer': { classList: { contains: () => !open } },
    'dg-md-text': { value: '# 已有编辑' }, 'dg-ai-instructions': { value: '合成说明' },
    'dg-ai-model': { value: 'model-a', selectedOptions: [{ dataset: { vision: 'true' } }] },
    'dg-ai-source': { value: 'source-a', selectedOptions: [{ dataset: { kind: 'text' } }] },
    'dg-ai-status': { textContent: '' }, 'dg-ai-run': { disabled: false },
  }
  const messages: any[] = [], calls: any[] = []
  const context: any = {
    crypto: webcrypto, AbortController, Uint8Array, URL, location: { origin: 'https://test.local', href: 'https://test.local/' },
    document: { readyState: 'loading', getElementById: (id: string) => nodes[id], addEventListener() {} },
    MutationObserver: class { observe() {} }, addEventListener() {}, clearTimeout() {}, setTimeout() {},
    RESEARCH_CONTEXT: { workbench: 'wb-a' }, RESEARCH_PREFIX: '/research/workbenches/wb-a',
    parent: { postMessage: (...args: unknown[]) => messages.push(args) },
    researchFetch: async (url: string, options: any) => {
      calls.push({ url, options }); const data = await handler(url, options)
      return { ok: true, json: async () => data }
    },
  }
  context.window = context
  runInNewContext(script, context)
  return { context, nodes, messages, calls, close() { open = false; context.dgCancelGeneration() } }
}
const metadata = { id: taskId, purpose: 'diagram', workbench_id: 'wb-a', state: 'running', phase: 'generating' }

test('closing while creation is pending keeps accepted work and announces it without reading or overwriting hidden UI', async () => {
  let accept!: (value: unknown) => void
  const f = fixture(async () => new Promise(resolve => { accept = resolve }))
  const running = f.context.dgGenerate()
  assert.equal(f.calls.length, 1)
  assert.equal(f.calls[0].options.method, 'POST')
  assert.equal(f.calls[0].options.signal, undefined, 'Drawer cancellation does not abort accepted creation')
  assert.match(f.calls[0].options.headers['Idempotency-Key'], /^[a-f0-9]{32}$/)
  await f.context.dgGenerate(); assert.equal(f.calls.length, 1, 'A second click while creation is pending cannot create another task')
  f.close(); accept(metadata); await running
  assert.equal(f.calls.length, 1, 'No status/result polling while the drawer is closed')
  assert.equal(f.nodes['dg-md-text'].value, '# 已有编辑')
  assert.equal(f.messages.length, 1)
  assert.deepEqual(JSON.parse(JSON.stringify(f.messages[0])), [{ type: 'research-analysis-job-started', purpose: 'diagram', workbenchId: 'wb-a', jobId: taskId }, 'https://test.local'])
})

test('a transport retry reuses the same creation key and payload while an accepted task has no duplicate POST', async () => {
  let attempts = 0
  const failed = { ...metadata, state: 'failed', phase: 'failed', message: '合成生成未完成' }
  const f = fixture(async (_url, options) => {
    if (options.method === 'POST' && ++attempts === 1) throw new Error('合成连接中断')
    return failed
  })
  await f.context.dgGenerate()
  assert.equal(f.nodes['dg-ai-run'].disabled, false)
  await f.context.dgGenerate()
  const creates = f.calls.filter(call => call.options.method === 'POST')
  assert.equal(creates.length, 2)
  assert.equal(creates[0].options.headers['Idempotency-Key'], creates[1].options.headers['Idempotency-Key'])
  assert.equal(creates[0].options.body, creates[1].options.body)
  assert.equal(f.nodes['dg-md-text'].value, '# 已有编辑')
  assert.match(f.nodes['dg-ai-status'].textContent, /合成生成未完成/)
})

test('metadata from another workbench or purpose cannot announce or open an unrelated task', async () => {
  for (const other of [{ ...metadata, workbench_id: 'wb-b' }, { ...metadata, purpose: 'judgments' }]) {
    const f = fixture(async () => other)
    await f.context.dgGenerate()
    assert.equal(f.messages.length, 0)
    assert.equal(f.calls.length, 1)
    assert.match(f.nodes['dg-ai-status'].textContent, /任务范围已改变/)
    assert.equal(f.nodes['dg-md-text'].value, '# 已有编辑')
  }
})
