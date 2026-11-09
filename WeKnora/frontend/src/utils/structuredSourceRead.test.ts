import assert from 'node:assert/strict'
import test from 'node:test'
import { createStructuredSourceReadQueue, structuredSourceReadError } from './structuredSourceRead'

function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('original reads from multiple source cards share a serial queue without overlapping HTTP work', async () => {
  const queue = createStructuredSourceReadQueue()
  const firstRead = deferred<string>(), secondRead = deferred<string>()
  let active = 0, maximum = 0
  const started: number[] = []
  const read = async (number: number, pending: Promise<string>) => {
    started.push(number); active++; maximum = Math.max(maximum, active)
    try { return await pending } finally { active-- }
  }
  const first = queue(() => true, () => read(1, firstRead.promise))
  const second = queue(() => true, () => read(2, secondRead.promise))
  await Promise.resolve()
  assert.deepEqual(started, [1])
  firstRead.resolve('first original')
  assert.equal(await first, 'first original')
  await Promise.resolve()
  assert.deepEqual(started, [1, 2])
  secondRead.resolve('second original')
  assert.equal(await second, 'second original')
  assert.equal(maximum, 1)
})

test('queued superseded targets never start and a stale running result is discarded', async () => {
  const queue = createStructuredSourceReadQueue()
  const firstRead = deferred<string>()
  let target = 1
  const started: number[] = []
  const first = queue(() => target === 1, () => { started.push(1); return firstRead.promise })
  await Promise.resolve()
  target = 2
  const superseded = queue(() => target === 2, async () => { started.push(2); return 'old target' })
  target = 3
  const latest = queue(() => target === 3, async () => { started.push(3); return 'current target' })
  firstRead.resolve('stale result')
  assert.equal(await first, undefined)
  assert.equal(await superseded, undefined)
  assert.equal(await latest, 'current target')
  assert.deepEqual(started, [1, 3])
})

test('source, account, tenant and original hash changes invalidate a queued read before sending it', async () => {
  for (const field of ['source', 'account', 'tenant', 'hash'] as const) {
    const queue = createStructuredSourceReadQueue()
    const blocker = deferred<void>()
    const state = { source: 'source-a', account: 'user-a', tenant: '7', hash: 'original-hash' }
    const snapshot = { ...state }
    const first = queue(() => true, () => blocker.promise)
    await Promise.resolve()
    let calls = 0
    const queued = queue(() => Object.keys(snapshot).every(key => state[key as keyof typeof state] === snapshot[key as keyof typeof state]),
      async () => { calls++; return 'old scoped data' })
    state[field] = 'changed'
    blocker.resolve()
    await first
    assert.equal(await queued, undefined)
    assert.equal(calls, 0)
  }
})

test('an older read failure releases the queue and the next current read runs once without retries', async () => {
  const queue = createStructuredSourceReadQueue()
  const pending = deferred<string>()
  const first = queue(() => true, () => pending.promise)
  const failed = assert.rejects(first, /old conflict/)
  let calls = 0
  const next = queue(() => true, async () => { calls++; return 'new result' })
  await Promise.resolve()
  pending.reject(new Error('old conflict'))
  await failed
  assert.equal(await next, 'new result')
  assert.equal(calls, 1)
})

test('real JSON conflicts show their bounded plain detail or typed message', async () => {
  const conflict = (detail: unknown) => new Response(JSON.stringify({ detail }), { status: 409, headers: { 'Content-Type': 'application/json' } })
  assert.equal((await structuredSourceReadError(conflict('正在读取原表，请稍后再查看其他区域。'))).message, '正在读取原表，请稍后再查看其他区域。')
  assert.equal((await structuredSourceReadError(conflict({ code: 'source_changed', message: '原件版本已变化，请重新读取。' }))).message, '原件版本已变化，请重新读取。')
  for (const detail of ['<script>unsafe</script>', 'too long '.repeat(100), 'stack\ninternal', { message: 'untyped internal exception' }, { code: '<invalid>', message: 'unsafe code' }]) {
    const message = (await structuredSourceReadError(conflict(detail))).message
    assert.match(message, /HTTP 409/)
    assert.doesNotMatch(message, /unsafe|internal|too long/)
  }
})

test('HTML, oversized conflict bodies and unknown failures never expose raw server exceptions', async () => {
  for (const response of [
    new Response('<html>private upstream exception</html>', { status: 409, headers: { 'Content-Type': 'text/html' } }),
    new Response(JSON.stringify({ detail: 'x'.repeat(5000) }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    new Response(JSON.stringify({ detail: 'private upstream exception' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
  ]) {
    const message = (await structuredSourceReadError(response)).message
    assert.match(message, /HTTP/)
    assert.doesNotMatch(message, /private|upstream|xxxxx/)
  }
  assert.match((await structuredSourceReadError(new Response(null, { status: 403 }))).message, /无权/)
  assert.match((await structuredSourceReadError(new Response(null, { status: 404 }))).message, /不存在/)
})
