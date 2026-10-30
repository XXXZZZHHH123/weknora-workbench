import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const recovery = source.slice(source.indexOf('// Quietly recover an in-flight reply'), source.indexOf('// Watch for stream errors'))
const errorWatch = source.slice(source.indexOf('watch(error, (newError) => {'), source.indexOf('\nonChunk((data) => {'))
const stop = source.slice(source.indexOf('const handleStopGeneration = () => {'), source.indexOf('const handleStopConfirmed'))
const finished = { id: 'assistant', role: 'assistant', is_completed: true, content: 'saved answer', created_at: 'cursor' }

function fixture(read: () => Promise<unknown>) {
  const timers = new Map<number, () => Promise<void>>()
  const messages: any[] = [{ id: 'assistant', is_completed: false }]
  const state: any = {
    session_id: { value: 'session' }, currentAssistantMessageId: { value: 'assistant' },
    isReplying: { value: true }, isStreamRecovering: { value: false }, limit: { value: 20 },
    isAttachingHistoryStream: { value: false }, loading: { value: false },
    stopStream() {}, markInFlightAssistantStopped() {},
    created_at: { value: '' }, hasMoreHistory: { value: true }, messagesList: messages,
    MessagePlugin: { error: () => { errors++ } }, t: (s: string) => s,
    handleMsgList: async (data: any[]) => { messages.push(...data) },
    getMessageList: () => { reads++; return read() }, console,
    setTimeout: (fn: () => Promise<void>) => { const id = ++timerId; timers.set(id, fn); return id },
    clearTimeout: (id: number) => timers.delete(id),
  }
  let reads = 0, errors = 0, timerId = 0
  const context = runInNewContext(`let recoverPollTimer = null; let recoverPollGeneration = 0;\n${recovery}\n${stop}\n({
    recover: recoverIncompleteMessage,
    stop: handleStopGeneration,
    cancel: () => { recoverPollGeneration++; if (recoverPollTimer) clearTimeout(recoverPollTimer); },
  })`, state)
  return { ...context, state, messages, timers, reads: () => reads, errors: () => errors,
    tick: async () => { const [id, fn] = timers.entries().next().value!; timers.delete(id); await fn() },
  }
}

test('a persisted web reply appears without clearing it for another history request', async () => {
  const f = fixture(async () => ({ data: [finished] }))
  f.recover()
  assert.equal(f.state.isStreamRecovering.value, true)
  await f.tick()
  assert.equal(f.reads(), 1)
  assert.equal(f.messages[0].content, 'saved answer')
  assert.equal(f.state.isReplying.value, false)
  assert.equal(f.state.isStreamRecovering.value, false)
  assert.equal(f.state.created_at.value, 'cursor')
  assert.equal(f.state.hasMoreHistory.value, false)
  assert.equal(f.timers.size, 0)
})

test('recovery keeps waiting for an unfinished reply and then applies its completion', async () => {
  let done = false
  const f = fixture(async () => ({ data: [{ ...finished, is_completed: done }] }))
  f.recover()
  await f.tick()
  assert.equal(f.state.isStreamRecovering.value, true)
  assert.equal(f.timers.size, 1)
  done = true
  await f.tick()
  assert.equal(f.reads(), 2)
  assert.equal(f.messages[0].content, 'saved answer')
})

test('leaving during a history request cannot replace another session or restart polling', async () => {
  let complete!: (data: unknown) => void
  const f = fixture(() => new Promise(resolve => { complete = resolve }))
  f.recover()
  const pending = f.tick()
  f.cancel()
  f.state.session_id.value = 'another-session'
  f.messages.splice(0, 1, { content: 'other session' })
  complete({ data: [finished] })
  await pending
  assert.equal(f.messages[0].content, 'other session')
  assert.equal(f.timers.size, 0)
})

test('a stale request stays cancelled even if the user returns to the same session', async () => {
  let complete!: (data: unknown) => void
  const f = fixture(() => new Promise(resolve => { complete = resolve }))
  f.recover()
  const pending = f.tick()
  f.cancel()
  f.messages.splice(0, 1, { content: 'new view of same session' })
  complete({ data: [finished] })
  await pending
  assert.equal(f.messages[0].content, 'new view of same session')
  assert.equal(f.timers.size, 0)
})

test('transient history failures retry without losing the visible conversation', async () => {
  let fail = true
  const f = fixture(async () => { if (fail) throw new Error('temporary read error'); return { data: [finished] } })
  f.recover()
  await f.tick()
  assert.equal(f.messages[0].id, 'assistant')
  assert.equal(f.errors(), 0)
  fail = false
  await f.tick()
  assert.equal(f.messages[0].content, 'saved answer')
})

test('pressing Stop invalidates a recovery request already in flight', async () => {
  let complete!: (data: unknown) => void
  const f = fixture(() => new Promise(resolve => { complete = resolve }))
  f.recover()
  const pending = f.tick()
  f.stop()
  complete({ data: [finished] })
  await pending
  assert.equal(f.messages[0].is_completed, false, 'stale history cannot replace the stopped view')
  assert.equal(f.state.isStreamRecovering.value, false)
  assert.equal(f.timers.size, 0)
})

test('an expired recovery is bounded and surfaces failure without deleting history', async () => {
  const f = fixture(async () => ({ data: [] }))
  f.recover()
  for (let i = 0; i < 48; i++) await f.tick()
  assert.equal(f.errors(), 1)
  assert.equal(f.timers.size, 0)
  assert.equal(f.messages[0].id, 'assistant')
  assert.equal(f.state.isStreamRecovering.value, false)
})

test('failed history attaches recover while fresh outgoing request errors remain visible', () => {
  for (const attaching of [true, false]) {
    let listener!: (error: string) => void
    let recoveries = 0, errors = 0
    const state = {
      error: {}, attachingResumedRun: false, watch: (_: unknown, fn: typeof listener) => { listener = fn },
      isAttachingHistoryStream: { value: attaching }, recoverIncompleteMessage: () => { recoveries++ },
      MessagePlugin: { error: () => { errors++ } }, isReplying: { value: true },
      loading: { value: true }, currentAssistantMessageId: { value: 'assistant' },
    }
    runInNewContext(errorWatch, state)
    listener('HTTP 404')
    assert.equal(recoveries, attaching ? 1 : 0)
    assert.equal(errors, attaching ? 0 : 1)
    assert.equal(state.currentAssistantMessageId.value, attaching ? 'assistant' : '')
  }
})
