import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const { vendorChunk } = await import(process.env.MANUAL_CHUNK_TEST_MODULE || './manualChunks.ts')
const nodeModules = fileURLToPath(new URL('../node_modules/', import.meta.url))

test('installed heavy packages keep their intended chunk', () => {
  for (const [relative, expected] of [
    ['mermaid/dist/mermaid.core.mjs', 'vendor-mermaid'],
    ['dagre-d3-es/src/index.js', 'vendor-mermaid'],
    ['cytoscape/dist/cytoscape.esm.mjs', 'vendor-mermaid'],
    ['marked/lib/marked.esm.js', 'vendor-markdown'],
    ['katex/dist/katex.mjs', 'vendor-markdown'],
    ['highlight.js/es/index.js', 'vendor-highlight'],
  ]) {
    assert.ok(existsSync(nodeModules + relative!.split('/')[0] + '/package.json'), relative)
    assert.equal(vendorChunk(nodeModules + relative), expected, relative)
  }
})

test('installed similarly named icons do not turn the global icon registry into a heavy dependency', () => {
  for (const name of ['highlight', 'mermaid', 'mermaid-filled', 'user-marked', 'task-marked']) {
    const id = nodeModules + 'tdesign-icons-vue-next/esm/components/' + name + '.js'
    assert.ok(existsSync(id), id)
    assert.equal(vendorChunk(id), undefined, id)
  }
})

test('nested pnpm and Windows paths identify the actual package rather than ancestors or file names', () => {
  assert.equal(vendorChunk('/app/node_modules/.pnpm/mermaid@11/node_modules/mermaid/dist/mermaid.core.mjs'), 'vendor-mermaid')
  assert.equal(vendorChunk('C:\\app\\node_modules\\.pnpm\\highlight.js@11\\node_modules\\highlight.js\\lib\\core.js?commonjs-proxy'), 'vendor-highlight')
  assert.equal(vendorChunk('/app/node_modules/.pnpm/mermaid@11/node_modules/dompurify/dist/purify.es.mjs'), undefined)
  assert.equal(vendorChunk('/app/node_modules/.pnpm/highlight.js@11/node_modules/vue/dist/vue.runtime.esm-bundler.js'), undefined)
  assert.equal(vendorChunk('C:\\app\\node_modules\\tdesign-icons-vue-next\\esm\\components\\highlight.js'), undefined)
  assert.equal(vendorChunk('/app/node_modules/@dagrejs/graphlib/dist/graphlib.js'), 'vendor-mermaid')
})

test('application modules, common helpers and similarly named packages are left to automatic shared chunks', () => {
  for (const id of ['/app/src/mermaid.ts', '/app/src/highlight.js', '\0vite/preload-helper.js',
    '/app/node_modules/dompurify/dist/purify.es.mjs', '/app/node_modules/vue/dist/vue.runtime.esm-bundler.js',
    '/app/node_modules/marked-tools/index.js', '/app/node_modules/my-mermaid/index.js']) {
    assert.equal(vendorChunk(id), undefined, id)
  }
})
