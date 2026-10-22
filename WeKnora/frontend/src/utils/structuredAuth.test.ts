import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
const source = readFileSync(new URL('../../../../structured-data/src/structured_data/index.html', import.meta.url), 'utf8')
function frame() {
  let receive: (event: any) => void
  const parent = { postMessage() {} }, location = { origin: 'http://localhost' }
  const context: any = { parent, location, window: { addEventListener: (_: string, listener: any) => { receive = listener } },
    authReceived: true, authEpoch: 5, authMessageSequence: 0, bearer: 'old', activeTenant: '7', activeKnowledgeBase: 'kb', activeUserId: '', requestedSourceKnowledge: '',
    reviewOnly: false, viewParams: new URLSearchParams(), nativeSourceBusy: false,
    $: () => ({ classList: { contains: () => true } }), authRefreshPending: null, clearTimeout, Error,
  }
  const start = source.indexOf(" window.addEventListener('message',async e=>{", source.indexOf("window.addEventListener('DOMContentLoaded'"))
  const end = source.indexOf(" parent.postMessage({type:'structured-ready'}", start)
  runInNewContext(source.slice(start, end), context)
  return { context, send: (data: any, origin = location.origin, sender = parent) => receive!({ data, origin, source: sender }) }
}
test('token rotation preserves structured request epoch and current editing state', () => {
  const { context, send } = frame()
  send({ type: 'structured-auth', token: 'new', tenant: '7', knowledgeBaseId: 'kb' })
  assert.equal(context.bearer, 'new'); assert.equal(context.authEpoch, 5)
})
test('structured authentication ignores messages outside its parent and origin', () => {
  const { context, send } = frame()
  send({ type: 'structured-auth', token: 'other' }, 'https://other.invalid')
  send({ type: 'structured-auth', token: 'other' }, undefined, {} as any)
  assert.equal(context.bearer, 'old'); assert.equal(context.authEpoch, 5)
})
test('refresh reply is correlated to the pending request and original scope', () => {
  const { context, send } = frame(); let resolved = 0, rejected = 0
  context.authRefreshPending = { requestId: 'r1', epoch: 5, resolve: () => resolved++, reject: () => rejected++ }
  send({ type: 'structured-auth-refreshed', requestId: 'other', token: 'other' }); assert.equal(context.bearer, 'old')
  send({ type: 'structured-auth-refreshed', requestId: 'r1', token: 'new' }); assert.equal(context.bearer, 'new'); assert.equal(resolved, 1)
  context.authRefreshPending = { requestId: 'r2', epoch: 4, resolve: () => resolved++, reject: () => rejected++ }
  send({ type: 'structured-auth-refreshed', requestId: 'r2', token: 'stale' }); assert.equal(context.bearer, 'new'); assert.equal(rejected, 1)
})
