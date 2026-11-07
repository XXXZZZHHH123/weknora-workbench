import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const source = readFileSync(new URL('./Input-field.vue', import.meta.url), 'utf8')
const start = source.indexOf('const scopedKbIds = computed(')
const end = source.indexOf('let query = ref', start)
const snippet = source.slice(start, end)

const scoped = (knowledgeScope: unknown) => runInNewContext(`${snippet}; ({ scopedKbIds, isScopedMention })`, {
  props: { knowledgeScope }, computed: (fn: () => unknown) => ({ get value() { return fn() } }),
})

test('a multi-source research scope accepts every authorized KB and refuses others', () => {
  assert.ok(start > 0 && end > start)
  const { scopedKbIds, isScopedMention } = scoped({ kbId: '', kbIds: ['personal-kb', 'central-kb'] })
  assert.deepEqual(Array.from(scopedKbIds.value), ['personal-kb', 'central-kb'])
  assert.equal(isScopedMention({ id: 'central-kb', type: 'kb' }), true)
  assert.equal(isScopedMention({ id: 'file-1', type: 'file', kbId: 'personal-kb' }), true)
  assert.equal(isScopedMention({ id: 'file-2', type: 'file', kbId: 'foreign-kb' }), false)
  assert.equal(isScopedMention({ id: '', type: 'kb' }), false)
})

test('a single-KB scope keeps its original behaviour and the send check uses the scoped list', () => {
  const { scopedKbIds, isScopedMention } = scoped({ kbId: 'bound-kb' })
  assert.deepEqual(Array.from(scopedKbIds.value), ['bound-kb'])
  assert.equal(isScopedMention({ id: 'bound-kb', type: 'kb' }), true)
  assert.equal(isScopedMention({ id: 'other-kb', type: 'kb' }), false)
  assert.match(source, /if \(!scopedKbIds\.value\.length \|\| !props\.sessionId/)
})
