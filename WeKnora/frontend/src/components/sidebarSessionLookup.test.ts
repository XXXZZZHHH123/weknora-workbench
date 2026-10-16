import assert from 'node:assert/strict'
import test from 'node:test'
import { lookupSidebarSession } from './sidebarSessionLookup.ts'

test('newly created sessions outside the global composer are resolved from real session detail', async () => {
  const calls: string[] = []
  const row = await lookupSidebarSession('created-elsewhere', [], async (id) => {
    calls.push(id)
    return { success: true, data: { id, title: '真实标题', has_user_message: true, created_at: '2026-10-14T00:00:00Z' } }
  })
  assert.equal(row?.id, 'created-elsewhere')
  assert.equal(row?.title, '真实标题')
  assert.deepEqual(calls, ['created-elsewhere'])
})

test('locally created global chat keeps its immediate row without an extra detail request', async () => {
  const cached = { id: 'local-session', title: '已有标题' }
  const row = await lookupSidebarSession('local-session', [cached], async () => { throw new Error('not requested') })
  assert.equal(row, cached)
})

test('lookup never invents a session for missing, inaccessible or mismatched details', async () => {
  assert.equal(await lookupSidebarSession('', [], async () => { throw new Error('not requested') }), null)
  assert.equal(await lookupSidebarSession('missing', [], async () => ({ success: true })), null)
  assert.equal(await lookupSidebarSession('forbidden', [], async () => ({ success: false, data: { id: 'forbidden' } })), null)
  assert.equal(await lookupSidebarSession('requested', [], async () => ({ data: { id: 'different' } })), null)
  await assert.rejects(lookupSidebarSession('failed', [], async () => { throw new Error('network unavailable') }), /network unavailable/)
})


test('opening a draft never surfaces it as history, but a later persisted user message does', async () => {
  let hasUserMessage = false
  const fetch = async (id: string) => ({ success: true, data: { id, title: 'Pinned draft', is_pinned: true, has_user_message: hasUserMessage } })
  assert.equal(await lookupSidebarSession('draft', [], fetch), null)
  assert.equal(await lookupSidebarSession('draft', [], async id => ({ data: { id, title: 'Legacy detail without evidence' } })), null)
  hasUserMessage = true
  assert.equal((await lookupSidebarSession('draft', [], fetch))?.id, 'draft')
})
