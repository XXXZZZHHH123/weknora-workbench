import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, effectScope, nextTick, reactive, ref, watch } from 'vue'
import { createIsolatedResearchClient } from '../../api/isolatedResearch'

const source = readFileSync(new URL('./IsolatedResearchWorkbench.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(`${script}\n;({ overview, loading, busy, guideSaving, guideError, sources, sourceTotal, sourcePage, drawerOpen, drawer, drawerError, error, pendingFiles, guide, selectFiles, enqueueFiles, sourceStatus, isWorkbook, openSources, openReview, openKnowledgeBase, openStructured, showSource, selectedSource, sourceContent, sourceContentLoading, saveVisualOriginals, moreSources, filesChanged, load })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText
const tick = () => new Promise(setImmediate)
const ready = async () => { await nextTick(); await tick() }
const plain = (value: unknown) => JSON.parse(JSON.stringify(value))
type Extra = (path: string, options?: RequestInit) => Response | Promise<Response> | undefined
function fixture(extra?: Extra, mode: 'legacy' | 'isolated' = 'isolated', save?: (id: string, input: any) => Promise<any>) {
  const props = reactive({ workbench: { id: 'fresh-workbench', knowledge_base_id: 'fresh-kb', workspace_mode: mode, template_id: 'industrial', name: 'Test-only workspace', onboarding: { status: 'active', tasks: { sources: false, review: false, research: false } } } })
  const auth = reactive({ token: 'test-token', selectedTenantId: 'test-tenant', user: { id: 'test-user' } })
  const calls: Array<{ url: string; method: string; headers: Headers }> = [], guides: any[] = [], batches: any[] = [], emits: any[] = [], routes: any[] = []
  const cleanups: Array<() => void> = []
  const network = async (url: RequestInfo | URL, options?: RequestInit) => {
    const path = String(url)
    calls.push({ url: path, method: options?.method || 'GET', headers: new Headers(options?.headers) })
    const customized = extra?.(path, options); if (customized) return customized
    if (path.endsWith('/overview')) return Response.json({ workbench: { id: props.workbench.id, knowledge_base_id: props.workbench.knowledge_base_id, workspace_mode: props.workbench.workspace_mode }, counts: { sources: 0, documents_ready: 0, excel_pending: 0, indicators: 0, observations: 0 }, readiness: { status: 'empty', tasks: [] }, modules: [] })
    if (path.includes('/sources?')) return Response.json({ knowledge_base_id: props.workbench.knowledge_base_id, sources: [], total: 0 })
    if (path.endsWith('/content')) return Response.json({ text: 'Real fixture source', supported: true })
    throw new Error(`unexpected preparation request: ${options?.method || 'GET'} ${path}`)
  }
  const scope = effectScope()
  const view: any = scope.run(() => runInNewContext(code, {
    computed, nextTick, ref, watch, defineProps: () => props, defineEmits: () => ((...args: any[]) => emits.push(args)), defineExpose() {},
    useRouter: () => ({ push: (location: unknown) => { routes.push(location); return Promise.resolve() } }),
    useAuthStore: () => auth, useUploadTasksStore: () => ({ enqueue: (input: any) => batches.push(input) }),
    createIsolatedResearchClient: (captured: any, current: any) => createIsolatedResearchClient(captured, current, network),
    updateResearchWorkbenchOnboarding: async (id: string, input: any) => { guides.push({ id, input }); if (save) return save(id,input); return { data: { ...props.workbench, onboarding: { ...props.workbench.onboarding, ...input, tasks: { ...props.workbench.onboarding.tasks, ...input.tasks } } } } },
    MessagePlugin: { info() {} }, workspaceErrorMessage: (error: any) => error?.message || 'failed', buildUploadFileName: (file: any) => file.webkitRelativePath || undefined,
    window: { addEventListener() {}, removeEventListener() {} }, setTimeout, clearTimeout,
    onMounted() {}, onBeforeUnmount: (fn: () => void) => cleanups.push(fn),
  }))
  return { props, auth, view, calls, guides, batches, emits, routes, close() { cleanups.forEach(fn => fn()); scope.stop() } }
}
const workbook = { id: 'real-source', file_name: 'Fixture original.xlsx', parse_status: 'stored', knowledge_base_id: 'fresh-kb', folder_path: 'Original folder' }
const textFile = { id: 'real-text', file_name: 'Source note.txt', parse_status: 'completed', knowledge_base_id: 'fresh-kb' }
const ownSources: Extra = path => path.includes('/sources?') ? Response.json({ knowledge_base_id: 'fresh-kb', sources: [workbook, textFile], total: 2 }) : undefined

for (const mode of ['isolated', 'legacy'] as const) {
  test(`${mode} reads only scoped inventory and never confirms a second copy of KB research facts`, async () => {
    const f = fixture(ownSources, mode)
    try {
      await ready()
      assert.equal(f.view.overview.value.workbench.workspace_mode, mode)
      assert.equal(f.view.sources.value.length, 2)
      assert.equal(f.calls.length, 2)
      assert.ok(f.calls.every(call => call.url.startsWith('/research/api/workbenches/fresh-workbench/') && call.method === 'GET'))
      assert.ok(f.calls.every(call => call.headers.get('X-Knowledge-Base-ID') === 'fresh-kb'))
      f.view.openStructured(f.view.sources.value[0]); f.view.openReview(); await f.view.load()
      assert.ok(f.calls.every(call => call.method === 'GET'))
      assert.equal(f.calls.some(call => /\/(imports|source-imports|indicators)(\/|\?|$)/.test(call.url)), false)
      assert.deepEqual(plain(f.routes), [
        { path: '/platform/knowledge-bases/fresh-kb/structured-data', query: { mode: 'import', source_knowledge_id: 'real-source' } },
        { path: '/platform/knowledge-bases/fresh-kb/structured-data', query: { mode: 'import' } },
      ])
    } finally { f.close() }
  })
}

test('personal preparation acknowledgements persist without claiming data readiness', async () => {
  const f = fixture()
  try {
    await ready(); await f.view.guide('dismissed')
    assert.equal(f.guides[0].id, 'fresh-workbench')
    Object.assign(f.props.workbench, f.emits[0][1]); assert.equal(f.props.workbench.onboarding.status, 'dismissed')
    await f.view.guide('active'); await tick()
    Object.assign(f.props.workbench, f.emits[1][1]); assert.equal(f.props.workbench.onboarding.status, 'active')
    assert.equal(f.view.overview.value.counts.indicators, 0)
  } finally { f.close() }
})

test('legacy guide and upload never call unsupported onboarding APIs', async () => {
  const f = fixture(ownSources, 'legacy')
  try {
    await ready(); f.view.openSources()
    assert.equal(f.view.drawer.value, 'sources'); assert.equal(f.view.drawerOpen.value, true)
    await f.view.guide('active', 'review')
    f.view.selectFiles([{ name: 'Original.xlsx', size: 10 }]); f.view.enqueueFiles()
    assert.equal(f.batches.length, 1); assert.deepEqual(f.guides, []); assert.deepEqual(f.emits, [])
  } finally { f.close() }
})

test('native upload preserves folder paths, KB binding and non-Excel parsing', async () => {
  const f = fixture()
  try {
    await ready()
    f.view.selectFiles([{ name: 'fixture.txt' }, { name: 'fixture.xlsx', webkitRelativePath: 'folder/fixture.xlsx' }, { name: 'fixture.png' }, { name: 'fixture.zip' }])
    assert.equal(f.batches.length, 0); f.view.enqueueFiles()
    assert.equal(f.batches.length, 2)
    const ordinary = f.batches.find(batch => !batch.processConfig), originals = f.batches.find(batch => batch.processConfig?.source_only)
    assert.deepEqual(Array.from(ordinary.uploads, (item: any) => item.file.name), ['fixture.txt', 'fixture.xlsx'])
    assert.equal(ordinary.uploads[1].fileName, 'folder/fixture.xlsx')
    assert.deepEqual(Array.from(originals.uploads, (item: any) => item.file.name), ['fixture.png', 'fixture.zip'])
    assert.ok(f.batches.every(batch => batch.kbId === 'fresh-kb'))
    assert.equal(f.view.pendingFiles.value.length, 0)
    await tick()
    f.view.saveVisualOriginals.value = false
    f.view.selectFiles([{ name: 'native.png' }, { name: 'native.docx' }]); f.view.enqueueFiles()
    assert.equal(f.batches.at(-1).processConfig, undefined)
    assert.ok(f.calls.every(call => call.method === 'GET'))
  } finally { f.close() }
})

test('stored originals do not falsely claim parsed or confirmed facts', () => {
  const f = fixture()
  try {
    assert.equal(f.view.sourceStatus(workbook), '原件已保存')
    assert.equal(f.view.sourceStatus({ file_name: 'fixture.png', parse_status: 'stored' }), '原件已保存')
    assert.equal(f.view.sourceStatus(textFile), '文档已解析')
    assert.equal(f.view.isWorkbook({ file_name: 'fixture.png' }), false); assert.equal(f.view.isWorkbook(workbook), true)
  } finally { f.close() }
})

test('review and workbook actions route to one KB confirmation entry with the real source ID', async () => {
  const f = fixture(ownSources)
  try {
    await ready(); f.view.openStructured(workbook); f.view.openReview(); f.view.openKnowledgeBase()
    assert.deepEqual(plain(f.routes), [
      { path: '/platform/knowledge-bases/fresh-kb/structured-data', query: { mode: 'import', source_knowledge_id: workbook.id } },
      { path: '/platform/knowledge-bases/fresh-kb/structured-data', query: { mode: 'import' } },
      '/platform/knowledge-bases/fresh-kb',
    ])
    assert.ok(f.calls.every(call => call.method === 'GET')); assert.deepEqual(f.guides, [])
  } finally { f.close() }
})

test('external or unlisted sources cannot navigate or request content', async () => {
  const f = fixture(ownSources)
  try {
    await ready(); const before = f.calls.length
    f.view.openStructured({ ...workbook, id: 'external-source', knowledge_base_id: 'external-kb' })
    await f.view.showSource({ ...workbook, id: 'external-source', knowledge_base_id: 'external-kb' })
    f.view.openStructured(textFile)
    assert.deepEqual(f.routes, []); assert.equal(f.calls.length, before); assert.ok(f.view.drawerError.value)
  } finally { f.close() }
})

test('scoped inventory is authoritative when rows omit redundant KB metadata', async () => {
  const f = fixture(path => path.includes('/sources?') ? Response.json({ knowledge_base_id: 'fresh-kb', sources: [{ id: workbook.id, file_name: workbook.file_name }], total: 1 }) : undefined)
  try {
    await ready(); f.view.openStructured(f.view.sources.value[0])
    assert.equal(f.routes[0].query.source_knowledge_id, workbook.id)
    assert.equal(f.routes[0].path, '/platform/knowledge-bases/fresh-kb/structured-data')
  } finally { f.close() }
})

test('a conflicting inventory or mode never becomes visible', async () => {
  for (const changed of ['container-kb', 'row-kb', 'mode']) {
    const f = fixture(path => {
      if (changed === 'mode' && path.endsWith('/overview')) return Response.json({ workbench: { id: 'fresh-workbench', knowledge_base_id: 'fresh-kb', workspace_mode: 'legacy' } })
      if (path.includes('/sources?')) return Response.json({ knowledge_base_id: changed === 'container-kb' ? 'external-kb' : 'fresh-kb', sources: [{ ...workbook, knowledge_base_id: changed === 'row-kb' ? 'external-kb' : 'fresh-kb' }], total: 1 })
    })
    try { await ready(); assert.equal(f.view.sources.value.length, 0); assert.equal(f.view.overview.value, null); assert.ok(f.view.error.value) }
    finally { f.close() }
  }
})

test('source preview ignores late content from a previously selected file', async () => {
  let first!: (response: Response) => void
  const f = fixture(path => path.endsWith('/sources/real-source/content') ? new Promise(resolve => { first = resolve }) : ownSources(path))
  try {
    await ready(); const a = f.view.showSource(workbook); await f.view.showSource(textFile)
    first(Response.json({ text: 'Obsolete selected content', supported: true })); await a
    assert.equal(f.view.selectedSource.value.id, textFile.id)
    assert.equal(f.view.sourceContent.value.text, 'Real fixture source'); assert.equal(f.view.sourceContentLoading.value, false)
  } finally { f.close() }
})

test('identity changes synchronously clear files, content and late source responses', async () => {
  let first!: (response: Response) => void
  const f = fixture(path => path.endsWith('/sources/real-source/content') ? new Promise(resolve => { first = resolve }) : ownSources(path))
  try {
    await ready(); const preview = f.view.showSource(workbook)
    f.view.selectFiles([{ name: 'private-fixture.xlsx' }]); f.auth.user.id = 'other-test-user'
    assert.equal(f.view.overview.value, null); assert.equal(f.view.sources.value.length, 0)
    assert.equal(f.view.pendingFiles.value.length, 0); assert.equal(f.view.drawer.value, null)
    first(Response.json({ text: 'Previous account content', supported: true })); await preview
    assert.equal(f.view.sourceContent.value, null)
  } finally { f.close() }
})

test('refresh displays canonical KB counts without writing a research confirmation', async () => {
  let confirmed = false
  const f = fixture(path => {
    if (path.endsWith('/overview')) return Response.json({ workbench: { id: 'fresh-workbench', knowledge_base_id: 'fresh-kb', workspace_mode: 'isolated' }, counts: { sources: 1, documents_ready: 0, excel_pending: confirmed ? 0 : 1, indicators: confirmed ? 3 : 0, observations: confirmed ? 9 : 0 }, readiness: { status: confirmed ? 'ready' : 'preparing', tasks: [] }, modules: [] })
    return ownSources(path)
  })
  try {
    await ready(); assert.equal(f.view.overview.value.counts.observations, 0)
    f.view.openStructured(workbook); confirmed = true; await f.view.load()
    assert.equal(f.view.overview.value.counts.indicators, 3); assert.equal(f.view.overview.value.counts.observations, 9)
    assert.ok(f.calls.every(call => call.method === 'GET'))
  } finally { f.close() }
})

test('pagination rejects a foreign continuation and can retry the same source page', async () => {
  let rejectPage = true
  const f = fixture(path => path.includes('/sources?') ? Response.json(path.includes('page=2') ? { knowledge_base_id: rejectPage ? 'external-kb' : 'fresh-kb', sources: [{ ...textFile, id: 'next-source' }], total: 2 } : { knowledge_base_id: 'fresh-kb', sources: [workbook], total: 2 }) : undefined)
  try {
    await ready(); await f.view.moreSources()
    assert.deepEqual(Array.from(f.view.sources.value, (item: any) => item.id), [workbook.id])
    assert.equal(f.view.sourcePage.value, 1); assert.ok(f.view.drawerError.value)
    rejectPage = false; await f.view.moreSources()
    assert.deepEqual(Array.from(f.view.sources.value, (item: any) => item.id), [workbook.id, 'next-source'])
    assert.equal(f.view.sourcePage.value, 2)
  } finally { f.close() }
})

test('leaving before the scheduled load performs no orphaned API request', async () => {
  const f = fixture(); f.close(); await ready(); assert.deepEqual(f.calls, [])
})

test('the overview inventory avoids a second load and continues after its 200 rows', async () => {
  const rows = Array.from({length:200},(_,index) => ({...textFile,id:`source-${index}`}))
  const f = fixture(path => {
    if (path.endsWith('/overview')) return Response.json({
      workbench:{id:'fresh-workbench',knowledge_base_id:'fresh-kb',workspace_mode:'isolated'},
      counts:{sources:201,documents_ready:0,excel_pending:0,indicators:0,observations:0},readiness:{status:'preparing',tasks:[]},
      source_inventory:{knowledge_base_id:'fresh-kb',sources:rows,total:201},modules:[],
    })
    if (path.includes('/sources?page=3&page_size=100')) return Response.json({
      knowledge_base_id:'fresh-kb',sources:[{...textFile,id:'last-source'}],total:201,
    })
    return undefined
  })
  try {
    await ready()
    assert.equal(f.calls.length,1)
    assert.equal(f.view.sourcePage.value,2)
    await f.view.moreSources()
    assert.equal(f.view.sources.value.length,201)
    assert.equal(f.view.sources.value[200].id,'last-source')
  } finally { f.close() }
})

test('source preparation waits for verified preparation progress', async () => {
  let resolveOverview!: (response: Response) => void
  const f = fixture(path => path.endsWith('/overview') ? new Promise<Response>(resolve => { resolveOverview = resolve }) : undefined)
  try {
    await ready()
    assert.equal(f.view.loading.value, true)
    assert.equal(f.view.overview.value, null)
    resolveOverview(Response.json({ workbench: { id: 'fresh-workbench', knowledge_base_id: 'fresh-kb', workspace_mode: 'isolated' }, counts: { sources: 0, documents_ready: 0, excel_pending: 0, indicators: 0, observations: 0 }, readiness: { status: 'empty', tasks: [] }, modules: [] }))
    await ready()
    assert.equal(f.view.overview.value.workbench.id, 'fresh-workbench')
  } finally { f.close() }
})

test('failed preparation verification never starts onboarding with guessed progress', async () => {
  const f = fixture(path => path.endsWith('/overview') ? Response.json({ detail: 'Scope unavailable' }, { status: 503 }) : undefined)
  try {
    await ready()
    assert.equal(f.view.overview.value, null)
    assert.equal(f.view.drawerOpen.value, false)
    assert.match(f.view.error.value, /Scope unavailable/)
  } finally { f.close() }
})

test('source preparation exposes every drawer to suppress unrelated feature guides', async () => {
  const f = fixture(ownSources)
  try {
    await ready()
    f.view.openSources()
    assert.equal(f.view.drawerOpen.value, true)
    f.view.selectFiles([{ name: 'fixture.txt' }])
    assert.equal(f.view.drawerOpen.value, true)
    await f.view.showSource(f.view.sources.value[1])
    assert.equal(f.view.drawerOpen.value, true)
    f.view.drawer.value = null
    assert.equal(f.view.drawerOpen.value, false)
  } finally { f.close() }
})

test('guide saving survives a concurrent scoped source refresh', async () => {
  let resolveSave!: (value: unknown) => void
  const f = fixture(undefined,'isolated',() => new Promise(resolve => { resolveSave=resolve }))
  try {
    await ready()
    const saving=f.view.guide('completed')
    await f.view.load()
    resolveSave({data:{...f.props.workbench,onboarding:{...f.props.workbench.onboarding,status:'completed'}}})
    await saving
    assert.equal(f.emits.length,1)
    assert.equal(f.emits[0][1].onboarding.status,'completed')
    assert.deepEqual(plain(f.guides[0].input),{status:'completed'})
    Object.assign(f.props.workbench,f.emits[0][1])
    assert.equal(f.props.workbench.onboarding.status,'completed')
  } finally { f.close() }
})

test('guide saving is independent of source preparation busy state', async () => {
  const f=fixture()
  try {
    await ready()
    f.view.busy.value=true
    await f.view.guide('skipped')
    assert.equal(f.guides.length,1)
    assert.equal(f.emits[0][1].onboarding.status,'skipped')
    assert.equal(f.view.busy.value,true)
  } finally { f.close() }
})

test('failed guide saving remains retryable after a source refresh and can change the outcome', async () => {
  let rejectSave!: (error: Error) => void
  let attempts=0
  const f=fixture(undefined,'isolated',async (_id,input) => {
    if (++attempts===1) return new Promise((_resolve,reject) => { rejectSave=reject })
    return {data:{...f.props.workbench,onboarding:{...f.props.workbench.onboarding,...input}}}
  })
  try {
    await ready()
    const saving=f.view.guide('skipped')
    await f.view.load()
    rejectSave(new Error('Save unavailable'))
    await saving
    assert.equal(f.view.guideSaving.value,false)
    assert.equal(f.props.workbench.onboarding.status,'active')
    assert.equal(f.view.guideError.value,'Save unavailable')
    assert.equal(f.emits.length,0)
    await f.view.guide('completed')
    assert.deepEqual(f.guides.map(request => request.input.status),['skipped','completed'])
    assert.equal(f.emits[0][1].onboarding.status,'completed')
    assert.equal(f.view.guideError.value,'')
    assert.equal(f.view.overview.value.counts.indicators,0)
  } finally { f.close() }
})

for (const changed of ['user','tenant','workbench','knowledge-base','token'] as const) {
  for (const outcome of ['success','failure'] as const) {
    test(`late guide ${outcome} cannot cross a changed ${changed} scope`, async () => {
      let resolveSave!: (value: unknown) => void, rejectSave!: (error: Error) => void
      const f=fixture(undefined,'isolated',() => new Promise((resolve,reject) => { resolveSave=resolve;rejectSave=reject }))
      try {
        await ready()
        const original=structuredClone(JSON.parse(JSON.stringify(f.props.workbench)))
        const saving=f.view.guide('completed')
        if(changed==='user')f.auth.user.id='another-user'
        if(changed==='tenant')f.auth.selectedTenantId='another-tenant'
        if(changed==='workbench')f.props.workbench.id='another-workbench'
        if(changed==='knowledge-base')f.props.workbench.knowledge_base_id='another-library'
        if(changed==='token')f.auth.token='refreshed-token'
        await ready()
        if(outcome==='success')resolveSave({data:{...original,onboarding:{...original.onboarding,status:'completed'}}})
        else rejectSave(new Error('Old scope failure'))
        await saving
        assert.equal(f.emits.length,0)
        assert.equal(f.view.guideError.value,'')
        assert.equal(f.view.guideSaving.value,false)
      } finally { f.close() }
    })
  }
}

test('a guide request has its own duplicate-save lock', async () => {
  let resolveSave!: (value: unknown) => void
  const f=fixture(undefined,'isolated',() => new Promise(resolve => { resolveSave=resolve }))
  try {
    await ready()
    const saving=f.view.guide('completed')
    await f.view.guide('skipped')
    assert.equal(f.guides.length,1)
    assert.equal(f.view.guideSaving.value,true)
    resolveSave({data:{...f.props.workbench,onboarding:{...f.props.workbench.onboarding,status:'completed'}}})
    await saving
    assert.equal(f.view.guideSaving.value,false)
    assert.equal(f.emits.length,1)
  } finally { f.close() }
})
