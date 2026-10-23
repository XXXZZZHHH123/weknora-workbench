import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { build, resolveConfig, type Plugin, type ResolvedConfig } from 'vite'
import type { OutputChunk, RollupOutput } from 'rollup'
import { serialEsbuildMinify } from './serialEsbuildMinify'

function renderHook(plugin: Plugin) {
  const hook = plugin.renderChunk
  assert.ok(hook)
  return typeof hook === 'function' ? hook : hook.handler
}

async function configure(plugins: Plugin[], options: Record<string, unknown> = {}) {
  const plugin = serialEsbuildMinify()
  const hook = plugin.configResolved!
  const handler = typeof hook === 'function' ? hook : hook.handler
  await handler.call({} as ThisParameterType<typeof handler>, {
    plugins,
    esbuild: {},
    build: { minify: 'esbuild' },
    ...options,
  } as unknown as ResolvedConfig)
  return plugin
}

const chunk = { fileName: 'chunk.js' } as Parameters<ReturnType<typeof renderHook>>[1]
const outputOptions = { format: 'es' } as Parameters<ReturnType<typeof renderHook>>[2]
const meta = { chunks: { 'chunk.js': chunk } }
const context = {} as ThisParameterType<ReturnType<typeof renderHook>>

test('only applies to builds and leaves other minifiers and disabled esbuild unchanged', async () => {
  assert.equal(serialEsbuildMinify().apply, 'build')
  for (const options of [
    { build: { minify: false } },
    { build: { minify: 'terser' } },
    { esbuild: false },
  ]) {
    const target: Plugin = { name: 'vite:esbuild-transpile', renderChunk: () => null }
    const original = target.renderChunk
    await configure([target], options)
    assert.equal(target.renderChunk, original)
  }
})

test('fails visibly if the expected Vite hook is missing or ambiguous', async () => {
  const target: Plugin = { name: 'vite:esbuild-transpile', renderChunk: () => null }
  for (const plugins of [[], [target, { ...target }], [{ name: target.name }]]) {
    await assert.rejects(configure(plugins), /requires one Vite esbuild renderChunk hook/)
  }
})

test('serializes concurrent chunks in arrival order', async () => {
  let active = 0
  let peak = 0
  const started: string[] = []
  const target: Plugin = {
    name: 'vite:esbuild-transpile',
    async renderChunk(code) {
      active++
      peak = Math.max(peak, active)
      started.push(code)
      await new Promise<void>((resolve) => setImmediate(resolve))
      active--
      return { code }
    },
  }
  await configure([target])
  const handler = renderHook(target)
  const results = await Promise.all(['first', 'second', 'third'].map((code) =>
    handler.call(context, code, chunk, outputOptions, meta),
  ))
  assert.equal(peak, 1)
  assert.deepEqual(started, ['first', 'second', 'third'])
  assert.deepEqual(results, [{ code: 'first' }, { code: 'second' }, { code: 'third' }])
})

test('preserves each context, argument, result object and object-hook metadata', async () => {
  const map = { version: 3, names: [], sources: ['input.js'], mappings: 'AAAA' }
  const result = { code: 'const value=1;', map }
  const seenContexts: unknown[] = []
  const contexts = [{ environment: { name: 'client' } }, { environment: { name: 'ssr' } }]
  const target: Plugin = {
    name: 'vite:esbuild-transpile',
    renderChunk: {
      order: 'post',
      handler(code, actualChunk, actualOptions, actualMeta) {
        seenContexts.push(this)
        assert.equal(code, 'const value = 1;')
        assert.equal(actualChunk, chunk)
        assert.equal(actualOptions, outputOptions)
        assert.equal(actualMeta, meta)
        return result
      },
    },
  }
  await configure([target])
  assert.equal(typeof target.renderChunk, 'object')
  assert.equal((target.renderChunk as { order: string }).order, 'post')
  const handler = renderHook(target)
  const results = await Promise.all(contexts.map((ctx) => handler.call(
    ctx as unknown as typeof context, 'const value = 1;', chunk, outputOptions, meta,
  )))
  assert.deepEqual(seenContexts, contexts)
  for (const actual of results) assert.equal(actual, result)
})

for (const synchronous of [false, true]) {
  test(`propagates ${synchronous ? 'synchronous' : 'asynchronous'} failures without blocking later chunks`, async () => {
    const failure = new Error('minify failed')
    const target: Plugin = {
      name: 'vite:esbuild-transpile',
      renderChunk(code) {
        if (code === 'broken') {
          if (synchronous) throw failure
          return Promise.reject(failure)
        }
        return null
      },
    }
    await configure([target])
    const handler = renderHook(target)
    const results = await Promise.allSettled(['broken', 'valid'].map((code) =>
      Promise.resolve().then(() => handler.call(context, code, chunk, outputOptions, meta)),
    ))
    assert.deepEqual(results, [
      { status: 'rejected', reason: failure },
      { status: 'fulfilled', value: null },
    ])
  })
}

test('preserves actual Vite esbuild minification and source maps', async () => {
  const results = []
  for (const plugins of [[], [serialEsbuildMinify()]]) {
    const config = await resolveConfig({
      configFile: false,
      logLevel: 'silent',
      plugins,
      build: { minify: 'esbuild', target: 'es2020', sourcemap: true },
    }, 'build')
    const target = config.plugins.find((plugin) => plugin.name === 'vite:esbuild-transpile')!
    const handler = renderHook(target)
    results.push(await handler.call(
      { environment: { config } } as unknown as typeof context,
      'export function calculate(input) { const doubled = input * 2; return doubled + 1; }',
      chunk, outputOptions, meta,
    ))
  }
  assert.deepEqual(results[1], results[0])
  const result = results[1] as { code: string; map: unknown }
  assert.match(result.code, /export/)
  assert.ok(result.code.length < 85)
  assert.ok(result.map)
})

test('real Vite builds emit identical minified chunks, CSS and source maps', { timeout: 20_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'weknora-serial-minify-'))
  try {
    await writeFile(join(root, 'main.js'), [
      'import "./style.css";',
      'export const label = "fixture";',
      'export async function calculate(input) {',
      '  const { double } = await import("./lazy.js");',
      '  return double(input) + label.length;',
      '}',
    ].join('\n'))
    await writeFile(join(root, 'lazy.js'), 'export function double(value) { return value * 2; }')
    await writeFile(join(root, 'style.css'), '.fixture { color: rgb(255, 0, 0); padding: 0px 0px; }')
    const results = []
    for (const plugins of [[], [serialEsbuildMinify()]]) {
      const result = await build({
        root,
        configFile: false,
        logLevel: 'silent',
        plugins,
        build: {
          write: false,
          minify: 'esbuild',
          target: 'es2020',
          sourcemap: true,
          rollupOptions: {
            input: join(root, 'main.js'),
            preserveEntrySignatures: 'strict',
          },
        },
      }) as RollupOutput
      results.push(result.output.map((item) => ({
        name: item.fileName,
        content: item.type === 'chunk' ? item.code : item.source,
      })).sort((a, b) => a.name.localeCompare(b.name)))
      const chunks = result.output.filter((item): item is OutputChunk => item.type === 'chunk')
      assert.equal(chunks.length, 2)
      assert.ok(chunks.every((item) => item.code.includes('export')))
      assert.ok(result.output.some((item) => item.fileName.endsWith('.css')))
      assert.equal(result.output.filter((item) => item.fileName.endsWith('.map')).length, 2)
    }
    assert.deepEqual(results[1], results[0])
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})
