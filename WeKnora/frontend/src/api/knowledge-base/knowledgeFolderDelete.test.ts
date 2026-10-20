import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

test('directory deletion preserves the original default and explicitly opts into recursive deletion', () => {
  const requests: Array<{url: string; config?: {timeout?: number}}> = [], exports: Record<string, any> = {}
  const source = ts.transpileModule(readFileSync(new URL('./index.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  runInNewContext(source, { exports, URLSearchParams, require(name: string) {
    if (name === '../../utils/request') return { del: (url: string, _data?: unknown, config?: {timeout?: number}) => requests.push({url, config}) }
    if (name === './knowledgeFileListQuery') return {}
    throw new Error('Unexpected runtime dependency ' + name)
  } })
  exports.deleteKnowledgeFolder('kb/a', 'Parent/A&B')
  exports.deleteKnowledgeFolder('kb/a', 'Parent/A&B', { recursive: false })
  exports.deleteKnowledgeFolder('kb/a', 'Parent/A&B', { recursive: true })
  assert.equal(requests[0]?.url, requests[1]?.url)
  assert.equal(requests[0]?.config, undefined)
  assert.equal(requests[1]?.config, undefined)
  assert.equal(requests[2]?.config?.timeout, 60000)
  for (const [index, request] of requests.entries()) {
    const url = new URL(request.url, 'http://fixture.invalid')
    assert.equal(url.pathname, '/api/v1/knowledge-bases/kb%2Fa/knowledge/folders')
    assert.equal(url.searchParams.get('path'), 'Parent/A&B')
    assert.equal(url.searchParams.get('recursive'), index === 2 ? 'true' : null)
    assert.equal([...url.searchParams.keys()].length, index === 2 ? 2 : 1)
  }
})
