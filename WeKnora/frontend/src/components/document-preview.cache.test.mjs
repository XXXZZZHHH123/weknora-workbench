import assert from 'node:assert/strict'
import test from 'node:test'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { reactive, watch, effectScope, nextTick } from 'vue'
import { createPptxPreviewCache } from '../utils/pptxPreviewCache.ts'
import { knowledgePptxSourceKey } from '../utils/pptxPreviewSource.ts'
const source = readFileSync(new URL('./document-preview.vue', import.meta.url), 'utf8')
const code = ts.transpile(source.slice(source.indexOf('async function loadPreview() {'), source.indexOf('\nfunction cleanup() {')))
const ref = value => ({ value })
const metadata = { id: 'document-a', file_path: 'resource://immutable-original', file_hash: 'source-hash', file_size: 4, updated_at: '2026-01-01T00:00:00Z' }
function environment() {
  const ctx = { scope: 'tenant-a:token-a', meta: { ...metadata }, denied: false, gets: 0, originals: 0, prepares: 0, paints: 0 }
  ctx.cache = createPptxPreviewCache({ getScope: () => ctx.scope })
  ctx.view = () => {
    const r = { props: { active: true, knowledgeId: metadata.id, fileName: 'synthetic.pptx', fileType: 'pptx' },
      previewExt: '', pptxSourceSha256: '', pptxSourceHashError: '', pptxSlideCount: 0,
      previewLoadVersion: 0, loadedForId: '', loadedPptxAuthority: '', pptxMediaCacheContext: undefined,
      loading: ref(false), error: ref(''), htmlViewMode: ref('render'), previewType: ref('pptx'), pptxPermissionChecking: ref(false), pptxData: ref(null), excelBlob: ref(null),
      getPreviewSourceKey: () => 'knowledge:' + r.props.knowledgeId,
      resolveFilePreviewExt: () => 'pptx', resolvePreviewKind: () => 'pptx', getPptxPreviewScope: () => ctx.scope,
      getKnowledgeDetails: async () => { ctx.gets++; if (ctx.denied) throw new Error('HTTP 403'); await ctx.metadataWait; return { data: { ...ctx.meta } } },
      knowledgePptxSourceKey, pptxPreviewCache: ctx.cache,
      clearPptxPageMedia: () => {}, watchPptxRender: () => {}, loadPptxPageMedia: () => {},
      cleanup: () => { r.loadedForId = ''; r.loadedPptxAuthority = ''; r.pptxData.value = null; r.pptxMediaCacheContext = undefined },
      allowsHtmlScriptPreview: () => false, previewKnowledgeFile: async () => { ctx.originals++; return new Blob(['PPTX']) },
      fetchPreviewBlob: () => assert.fail('Knowledge cache should use its captured authorized ID'),
      ensureBlobType: blob => blob, nextTick: async () => {},
      sha256Hex: async () => { if (ctx.hashFails) throw new Error('WebCrypto unavailable'); return 'a'.repeat(64) },
      preparePptxPreview: async data => { ctx.prepares++; await ctx.prepareWait; return { data, slideCount: 3 } },
      preparePptxOlePreview: async data => data,
      setPptxRendererData: async () => { ctx.paints++ },
      scheduleLocate: () => assert.fail('PPTX location is renderer-driven'),
      t: key => key,
    }
    const funcs = vm.runInNewContext(code + '; ({loadPreview})', r)
    return Object.assign(r, funcs)
  }
  return ctx
}

test('new preview instances reauthorize but reuse the original blob and prepared PPTX', async () => {
  const env = environment(), first = env.view()
  await first.loadPreview(); await env.view().loadPreview()
  assert.equal(env.gets, 2); assert.equal(env.originals, 1); assert.equal(env.prepares, 1); assert.equal(env.paints, 2)
  assert.equal(first.error.value, '')
})
test('retained preview reactivation also checks permission before reusing content', async () => {
  const env = environment(), view = env.view()
  await view.loadPreview(); await view.loadPreview()
  assert.equal(env.gets, 2); assert.equal(env.originals, 1); assert.equal(env.prepares, 1); assert.equal(env.paints, 1)
  env.denied = true; await view.loadPreview()
  assert.equal(view.error.value, 'HTTP 403'); assert.equal(view.pptxData.value, null); assert.equal(env.paints, 1)
})
test('metadata source changes select a fresh original and prepared version', async () => {
  const env = environment(), view = env.view()
  await view.loadPreview(); env.meta.file_path = 'resource://new-original'; env.meta.updated_at = '2026-02-01T00:00:00Z'; await view.loadPreview()
  assert.equal(env.gets, 2); assert.equal(env.originals, 2); assert.equal(env.prepares, 2); assert.equal(env.paints, 2)
})
test('permission failure on a warm remount does not read cached original or prepare', async () => {
  const env = environment(); await env.view().loadPreview(); env.denied = true
  const denied = env.view(); await denied.loadPreview()
  assert.equal(denied.error.value, 'HTTP 403'); assert.equal(denied.pptxData.value, null); assert.equal(env.originals, 1); assert.equal(env.prepares, 1); assert.equal(env.paints, 1)
})
test('closing while preparing permits shared warm completion without painting the retired view', async () => {
  const env = environment(); let release
  env.prepareWait = new Promise(resolve => { release = resolve })
  const old = env.view(), a = old.loadPreview()
  for (let wait = 0; !env.prepares; wait++) { assert.ok(wait < 100, old.error.value || 'prepare did not start'); await new Promise(resolve => setImmediate(resolve)) }
  old.props.active = false; old.previewLoadVersion++
  const next = env.view(), b = next.loadPreview(); release(); await Promise.all([a, b])
  assert.equal(env.originals, 1); assert.equal(env.prepares, 1); assert.equal(env.paints, 1); assert.equal(old.pptxData.value, null); assert.ok(next.pptxData.value)
})
test('changing credentials while metadata is in flight cannot reuse old authority', async () => {
  const env = environment(); let release
  env.metadataWait = new Promise(resolve => { release = resolve })
  const view = env.view(), task = view.loadPreview(); env.scope = 'tenant-b:token-b'; release(); await task
  assert.equal(env.originals, 0); assert.equal(env.prepares, 0); assert.equal(env.paints, 0)
})
test('changing spreadsheet sources removes the old Blob before awaiting the new source ACL', async () => {
  const env = environment(), view = env.view(); let release
  view.previewType.value = 'excel'
  view.resolveFilePreviewExt = () => 'xlsx'; view.resolvePreviewKind = () => 'excel'
  view.loadedForId = 'knowledge:previous-document'
  view.excelBlob.value = new Blob(['previous workbook'])
  env.metadataWait = new Promise(resolve => { release = resolve })
  const task = view.loadPreview()
  assert.equal(env.gets, 1)
  assert.equal(view.excelBlob.value, null, 'previous viewer unmounts while permission is pending')
  assert.equal(env.originals, 0)
  view.props.active = false; view.previewLoadVersion++; release(); await task
  assert.equal(env.originals, 0, 'closing during ACL validation cannot fetch the next original')
})
test('incomplete source metadata disables shared reuse rather than guessing source identity', async () => {
  const env = environment(); delete env.meta.file_path
  // The actual authorized file fetch stays allowed, but this test only checks
  // identity selection here because the no-cache branch uses the normal fetch.
  assert.equal(knowledgePptxSourceKey(metadata.id, env.meta), null)
  for (const [key, value] of [['file_hash', ''], ['file_size', -1], ['updated_at', null], ['id', 'other']]) {
    assert.equal(knowledgePptxSourceKey(metadata.id, { ...metadata, [key]: value }), null)
  }
  assert.notEqual(knowledgePptxSourceKey(metadata.id, metadata), knowledgePptxSourceKey(metadata.id, { ...metadata, file_hash: 'changed' }))
})

test('fingerprint failure can render this time but cannot become a prepared cache hit', async () => {
  const env = environment(); env.hashFails = true
  const first = env.view(); await first.loadPreview()
  assert.equal(first.error.value, ''); assert.ok(first.pptxData.value); assert.equal(first.pptxSourceSha256, '')
  env.hashFails = false; const next = env.view(); await next.loadPreview()
  assert.equal(env.originals, 1); assert.equal(env.prepares, 2); assert.equal(next.pptxSourceSha256, 'a'.repeat(64))
})


test('changing authentication clears retained PPT content before the next permission response', async t => {
  const env = environment(), view = env.view(), scope = effectScope()
  t.after(() => scope.stop())
  view.props = reactive(view.props)
  view.previewAuth = reactive({ token: 'token-a', selectedTenantId: 'tenant-a' })
  view.previewSecurityScope = env.scope
  view.watch = watch
  const start = source.lastIndexOf('\nwatch(')
  const end = source.indexOf('\nonUnmounted(', start)
  assert.ok(start > 0 && end > start)
  scope.run(() => vm.runInNewContext(ts.transpile(source.slice(start, end)), view))
  for (let wait = 0; env.paints < 1; wait++) { assert.ok(wait < 100, view.error.value); await new Promise(resolve => setImmediate(resolve)) }
  assert.ok(view.pptxData.value)
  let release
  env.metadataWait = new Promise(resolve => { release = resolve })
  env.scope = 'tenant-b:token-b'; view.previewAuth.token = 'token-b'; view.previewAuth.selectedTenantId = 'tenant-b'
  await nextTick()
  assert.equal(view.pptxData.value, null, 'old document data is removed while fresh ACL is pending')
  assert.equal(env.gets, 2); assert.equal(env.paints, 1)
  release()
  for (let wait = 0; env.paints < 2; wait++) { assert.ok(wait < 100, view.error.value); await new Promise(resolve => setImmediate(resolve)) }
  assert.equal(env.originals, 2)
})

test('retained locator callbacks cannot start raster loading while the reopen permission check is pending', async () => {
  const start = source.indexOf('async function loadPptxPageMedia(force = false) {')
  const end = source.indexOf('\nfunction onPptxMediaImageLoad', start)
  const script = ts.transpile(source.slice(start, end))
  let cleared = 0
  const run = vm.runInNewContext(script + '; loadPptxPageMedia', {
    loadedForId: 'knowledge:synthetic-ppt', props: { active: true, knowledgeId: 'synthetic-ppt' },
    pptxPermissionChecking: ref(true), clearPptxPageMedia: () => { cleared++ },
    createPptxViewportMedia: () => assert.fail('Pending ACL must not create a raster controller'),
  })
  await run()
  assert.equal(cleared, 1)
})
