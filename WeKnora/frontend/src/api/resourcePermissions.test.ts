import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

test('source sharing API sends explicit page grants and clears them on revoke', () => {
  const calls: Array<{ url: string; body: unknown }> = [], exports: Record<string, any> = {}
  const code = ts.transpileModule(readFileSync(new URL('./resourcePermissions.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  runInNewContext(code, { exports, require(name: string) {
    assert.equal(name, '@/utils/request')
    return { put: (url: string, body: unknown) => calls.push({ url, body: JSON.parse(JSON.stringify(body)) }) }
  } })
  exports.updateWorkbenchSourceSharing('workbench/a', true, ['market', 'diagram'])
  exports.updateWorkbenchSourceSharing('workbench/a', false, ['indicators'])
  assert.deepEqual(calls, [
    { url: '/api/v1/workspaces/research/workbench%2Fa/source-sharing', body: { approved: true, modules: ['market', 'diagram'] } },
    { url: '/api/v1/workspaces/research/workbench%2Fa/source-sharing', body: { approved: false, modules: [] } },
  ])
})
