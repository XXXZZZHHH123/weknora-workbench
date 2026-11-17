import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import type { useChatMemoryRow as MemoryRow } from './useChatMemoryRow.ts'

const compiled = ts.transpileModule(readFileSync(new URL('./useChatMemoryRow.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function fixture(remove: (id: string) => Promise<void>) {
  const messages: string[] = []
  const modules: Record<string, unknown> = {
    vue, 'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    'tdesign-vue-next': { MessagePlugin: {
      success: (message: string) => messages.push(message), error: (message: string) => messages.push(message),
    } },
    '@/api/memory': { deleteMemoryItem: remove },
  }
  const exports: { useChatMemoryRow?: typeof MemoryRow } = {}
  runInNewContext(compiled, { exports, require: (id: string) => {
    assert.ok(id in modules, `unexpected import ${id}`)
    return modules[id]
  } })
  const used = vue.ref([
    { id: 'first', kind: 'fact', content: 'Read by this answer' },
    { id: 'second', kind: 'preference', content: 'Another recalled item' },
  ])
  return { used, messages, row: exports.useChatMemoryRow!(() => used.value) }
}

test('recalled memories can be inspected and forgotten only after the scoped API succeeds', async () => {
  let resolve!: () => void
  const deleted: string[] = []
  const f = fixture(id => { deleted.push(id); return new Promise<void>(done => { resolve = done }) })
  assert.equal(f.row.hasMemory.value, true)
  f.row.toggle()
  assert.equal(f.row.expanded.value, true)
  const pending = f.row.forget(f.used.value[0]!)
  assert.equal(f.row.forgettingId.value, 'first')
  assert.equal(f.row.memoryItems.value.length, 2)
  resolve()
  await pending
  assert.deepEqual(deleted, ['first'])
  assert.deepEqual(Array.from(f.row.memoryItems.value, item => item.id), ['second'])
  assert.equal(f.row.forgettingId.value, '')
  assert.deepEqual(f.messages, ['chat.memoryForgotten'])
  // The persisted historical statement about what this answer saw is intact.
  assert.deepEqual(f.used.value.map(item => item.id), ['first', 'second'])
})

test('failed forgetting preserves the recall list and exposes the failure', async () => {
  const f = fixture(async () => { throw new Error('Permission denied') })
  await f.row.forget(f.used.value[0]!)
  assert.equal(f.row.memoryItems.value.length, 2)
  assert.equal(f.row.forgettingId.value, '')
  assert.deepEqual(f.messages, ['Permission denied'])
})
