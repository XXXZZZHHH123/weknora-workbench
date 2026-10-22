import assert from 'node:assert/strict'
import test from 'node:test'
import { createPptxPreviewCache } from './pptxPreviewCache'

test('closing a viewer does not discard blobs; a second consumer shares the pending source', async () => {
  const cache = createPptxPreviewCache({ getScope: () => 'user/space' })
  let finish!: (value: { value: Blob; bytes: number }) => void
  let calls = 0
  const loader = () => { calls++; return new Promise<{value: Blob; bytes: number}>(resolve => { finish = resolve }) }
  const first = cache.getOrLoad('user/space', 'png/source/page/part', loader)
  const second = cache.getOrLoad('user/space', 'png/source/page/part', loader)
  await Promise.resolve()
  const blob = new Blob(['synthetic raster'])
  finish({ value: blob, bytes: blob.size })
  assert.equal(await first, blob)
  assert.equal(await second, blob)
  assert.equal(await cache.getOrLoad('user/space', 'png/source/page/part', loader), blob)
  assert.equal(calls, 1)
})

test('credential/workspace changes abort old work and cannot publish or retrieve it in a new scope', async () => {
  let scope = 'alice/one'
  const cache = createPptxPreviewCache({ getScope: () => scope })
  let finish!: (value: {value: string; bytes: number}) => void
  let signal!: AbortSignal
  const old = cache.getOrLoad(scope, 'same-source', arg => { signal = arg; return new Promise<{value:string;bytes:number}>(resolve => { finish = resolve }) })
  await Promise.resolve()
  scope = 'bob/two'
  assert.equal(cache.read(scope, 'same-source'), undefined)
  assert.equal(signal.aborted, true)
  finish({value: 'private old bytes', bytes: 17})
  await assert.rejects(old, /access changed/)
  await assert.rejects(cache.getOrLoad('alice/one', 'same-source', async () => ({value:'wrong',bytes:5})), /access changed/)
  assert.equal(await cache.getOrLoad(scope, 'same-source', async () => ({value:'new',bytes:3})), 'new')
})

test('logout is detected before a late response even without another preview lookup', async () => {
  let scope = 'session'
  const cache = createPptxPreviewCache({ getScope: () => scope })
  let finish!: (value: {value: string; bytes: number}) => void
  const task = cache.getOrLoad(scope, 'original', () => new Promise<{value:string;bytes:number}>(resolve => { finish = resolve }))
  await Promise.resolve()
  scope = ''
  finish({value:'late',bytes:4})
  await assert.rejects(task, /access changed/)
  assert.equal(cache.read('session', 'original'), undefined)
})

test('source revisions have separate keys; failures retry and never become cache hits', async () => {
  const cache = createPptxPreviewCache({ getScope: () => 'session' })
  await cache.getOrLoad('session', 'prepared/old-source', async () => ({value:'old',bytes:3}))
  assert.equal(cache.read('session', 'prepared/new-source'), undefined)
  await assert.rejects(cache.getOrLoad('session', 'png', async () => { throw new Error('conversion failed') }))
  assert.equal(await cache.getOrLoad('session', 'png', async () => ({value:'ok',bytes:2})), 'ok')
  assert.equal(await cache.getOrLoad('session', 'degraded', async () => ({value:'renderable without a fingerprint',bytes:32,cacheable:false})), 'renderable without a fingerprint')
  assert.equal(cache.read('session', 'degraded'), undefined)
})

test('TTL, byte budget and entry count bound retained data; read promotes LRU only', async () => {
  let time = 0
  const cache = createPptxPreviewCache({ getScope: () => 's', now: () => time, maxBytes: 6, maxEntries: 2, ttlMs: 10 })
  const put = (key:string, bytes = 3) => cache.getOrLoad('s',key,async()=>({value:key,bytes}))
  await put('a'); await put('b')
  assert.equal(cache.read('s','a'),'a')
  await put('c')
  assert.equal(cache.read('s','b'),undefined)
  assert.equal(cache.read('s','a'),'a')
  await put('oversized',7)
  assert.equal(cache.read('s','oversized'),undefined)
  assert.equal(cache.read('s','a'),'a')
  time = 10
  assert.equal(cache.read('s','a'),undefined, 'LRU reads do not extend permission/data retention TTL')
  assert.equal(cache.read('s','c'),undefined)
  const count = createPptxPreviewCache({ getScope:()=> 's', maxEntries:1 })
  await count.getOrLoad('s','a',async()=>({value:'a',bytes:0}))
  await count.getOrLoad('s','b',async()=>({value:'b',bytes:0}))
  assert.equal(count.read('s','a'),undefined)
})

test('clear aborts a pending task without allowing its finally to remove the replacement', async () => {
  const cache = createPptxPreviewCache({ getScope:()=> 's' })
  let finish!: (value:{value:string;bytes:number})=>void
  const old = cache.getOrLoad('s','key',()=>new Promise<{value:string;bytes:number}>(resolve=>{finish=resolve}))
  await Promise.resolve()
  cache.clear()
  const fresh = cache.getOrLoad('s','key',async()=>({value:'fresh',bytes:5}))
  finish({value:'stale',bytes:5})
  await assert.rejects(old,/access changed/)
  assert.equal(await fresh,'fresh')
  assert.equal(cache.read('s','key'),'fresh')
})

test('a decoder failure can discard only the bad entry and fetch it again', async () => {
  const cache = createPptxPreviewCache({ getScope: () => 'session' })
  let calls = 0
  const load = async () => ({value: ++calls, bytes: 1})
  assert.equal(await cache.getOrLoad('session', 'png/bad', load), 1)
  await cache.getOrLoad('session', 'png/other', async () => ({value:'valid',bytes:5}))
  cache.invalidate('wrong-session', 'png/other')
  cache.invalidate('session', 'png/bad')
  assert.equal(cache.read('session', 'png/other'), 'valid')
  assert.equal(await cache.getOrLoad('session', 'png/bad', load), 2)
})
