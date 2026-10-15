import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { conversationPath } from './knowledgeChatNavigation'

test('conversation navigation preserves a concrete knowledge-base context and the global route', () => {
  assert.equal(conversationPath('session', 'library'), '/platform/knowledge-bases/library/chat/session')
  assert.equal(conversationPath('session'), '/platform/chat/session')
  assert.equal(conversationPath('session', ''), '/platform/chat/session')
  assert.equal(conversationPath('s?x', 'kb?x'), '/platform/knowledge-bases/kb%3Fx/chat/s%3Fx')
})

test('the real first-session callback carries the draft and navigates inside its knowledge base', async () => {
  const source = readFileSync(new URL('../creatChat/creatChat.vue', import.meta.url), 'utf8')
  const start = source.indexOf('const navigateToSession = async')
  const end = source.indexOf('const handleKBEditorSuccess', start)
  assert.ok(start >= 0 && end > start)
  const calls: unknown[][] = []
  const menu = { updataMenuChildren: (row: unknown) => calls.push(['menu', row]),
    changeIsFirstSession: (value: unknown) => calls.push(['first', value]),
    changeFirstQuery: (...args: unknown[]) => calls.push(['draft', ...args]) }
  const context = { route: { params: { kbId: 'library' } }, router: { push: (path: string) => calls.push(['route', path]) },
    usemenuStore: menu, t: () => 'title', conversationPath, Date }
  const compiled = ts.transpileModule(source.slice(start, end) + '\nglobalThis.navigate = navigateToSession', {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  runInNewContext(compiled, context)
  await (context as any).navigate('session', 'unsent fixture draft', 'selected-model', [], [], [])
  assert.deepEqual(calls.find(call => call[0] === 'route'), ['route', '/platform/knowledge-bases/library/chat/session'])
  assert.equal(calls.find(call => call[0] === 'draft')?.[1], 'unsent fixture draft')
  assert.equal(calls.find(call => call[0] === 'draft')?.[3], 'selected-model')
  assert.equal(calls.find(call => call[0] === 'menu'), undefined, 'a prepared draft is not persisted history')
})
