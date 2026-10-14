import assert from 'node:assert/strict'
import test from 'node:test'
import { createResearchChatSession } from './researchChatSession'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('opening is lazy, repeated clicks share one creation, and reopening retains the session', async () => {
  const request = deferred<string>()
  let calls = 0
  const chat = createResearchChatSession(() => { calls++; return request.promise })
  assert.equal(calls, 0)
  assert.equal(chat.state.visible, false)
  const first = chat.open()
  const second = chat.open()
  assert.equal(calls, 1)
  assert.equal(chat.state.loading, true)
  chat.close()
  assert.equal(chat.state.visible, false)
  request.resolve('session-a')
  await Promise.all([first, second])
  assert.equal(chat.state.visible, false, 'a late creation never reopens the panel')
  assert.equal(chat.state.sessionId, 'session-a')
  await chat.open()
  assert.equal(chat.state.visible, true)
  assert.equal(chat.state.sessionId, 'session-a')
  assert.equal(calls, 1)
})

test('creation failures and empty IDs stay visible and allow retry', async () => {
  let attempt = 0
  const chat = createResearchChatSession(async () => {
    attempt++
    if (attempt === 1) throw new Error('没有访问权限')
    return attempt === 2 ? '' : 'session-c'
  })
  await chat.open()
  assert.equal(chat.state.error, '没有访问权限')
  assert.equal(chat.state.loading, false)
  assert.equal(chat.state.sessionId, '')
  await chat.open()
  assert.match(chat.state.error, /未能创建对话/)
  await chat.open()
  assert.equal(chat.state.error, '')
  assert.equal(chat.state.sessionId, 'session-c')
})

test('leaving the account, space or workbench discards a late session response', async () => {
  const request = deferred<string>()
  let calls = 0
  const chat = createResearchChatSession(() => { calls++; return request.promise })
  const pending = chat.open()
  chat.dispose()
  request.resolve('old-session')
  await pending
  await chat.open()
  assert.equal(chat.state.sessionId, '')
  assert.equal(chat.state.visible, false)
  assert.equal(chat.state.loading, false)
  assert.equal(calls, 1)
})

test('a disposed request cannot display an old identity error', async () => {
  const request = deferred<string>()
  const chat = createResearchChatSession(() => request.promise)
  const pending = chat.open()
  chat.dispose()
  request.reject(new Error('previous account failure'))
  await pending
  assert.equal(chat.state.error, '')
  assert.equal(chat.state.loading, false)
})

test('a synchronous request failure does not prevent a later retry', async () => {
  let calls = 0
  const chat = createResearchChatSession(() => {
    if (++calls === 1) throw new Error('request preparation failed')
    return Promise.resolve('retried-session')
  })
  await chat.open()
  assert.equal(chat.state.error, 'request preparation failed')
  await chat.open()
  assert.equal(chat.state.sessionId, 'retried-session')
})
