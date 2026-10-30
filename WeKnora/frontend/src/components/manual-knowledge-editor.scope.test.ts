import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'
import { resolvePreferredKnowledgeBaseId } from '../utils/manualKnowledgeDraft'

const source = readFileSync(process.env.MANUAL_EDITOR_TEST_SOURCE || new URL('./manual-knowledge-editor.vue', import.meta.url), 'utf8')
function code(from: string, to: string) {
  const start = source.indexOf(from), end = source.indexOf(to, start)
  assert.ok(start >= 0 && end > start)
  return ts.transpileModule(source.slice(start, end), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
}

async function save(options: { target?: string; change?: string; phase?: string; wrongKB?: boolean; editing?: boolean } = {}) {
  const writes: any[] = [], notifications: any[] = [], messages: string[] = []
  const form = { kbId: 'kb-a', title: '  Draft title  ', content: 'Draft content' }
  const identity = { user: { id: 'actor-a' }, effectiveTenantId: 10000 }
  const route = { params: { kbId: 'kb-a' } }
  const visible = ref(true)
  const knowledge = ref(options.editing ? 'file-a' : null)
  let sandbox: vm.Context
  const context: any = {
    form, authStore: identity, route, visible, ref,
    watch: () => {},
    saving: ref(false), savingAction: ref('draft'), validateForm: () => true, t: (key: string) => key,
    manualTagIds: ref(['tag-a']), currentKnowledgeId: knowledge, mode: ref(options.editing ? 'edit' : 'create'),
    markPristine: () => {}, uiStore: { notifyManualEditorSuccess: (payload: any) => notifications.push(payload), closeManualEditor: () => { visible.value = false } },
    MessagePlugin: { success: (value: string) => messages.push(value), error: (value: string) => messages.push(value) },
    getKnowledgeBaseById: async () => { change('metadata'); return { data: { id: options.wrongKB ? 'kb-b' : 'kb-a' } } },
    uploadConfirmStore: { open: async () => { change('confirmation'); return { processConfig: {}, tagIds: ['tag-confirmed'] } } },
    createManualKnowledge: async (kbId: string, payload: unknown) => { writes.push(['create', kbId, payload]); change('write'); return { success: true, data: { id: 'new-file' } } },
    updateManualKnowledge: async (id: string, payload: unknown) => { writes.push(['update', id, payload]); change('write'); return { success: true, data: { id } } },
  }
  function change(phase: string) {
    if ((options.phase || 'confirmation') !== phase) return
    if (options.change === 'actor') identity.user.id = 'actor-b'
    if (options.change === 'tenant') identity.effectiveTenantId = 10001
    if (options.change === 'route') route.params.kbId = 'kb-b'
    if (options.change === 'destination') form.kbId = 'kb-b'
    if (options.change === 'closed') visible.value = false
    if (options.change === 'new-session' && source.includes('let editorGeneration =')) vm.runInContext('editorGeneration++', sandbox)
  }
  sandbox = vm.createContext(context)
  const setup = source.includes('let editorGeneration =') ? code('let editorGeneration =', '// ---------- view mode') : ''
  await vm.runInContext(setup + code('const handleSave =', '// ---------- unsaved-changes guard') + `\nhandleSave(${JSON.stringify(options.target || 'publish')})`, sandbox)
  return { writes: JSON.parse(JSON.stringify(writes)), notifications, messages, knowledge: knowledge.value }
}

test('the existing native editor saves and publishes using the original form and processing options', async () => {
  const result = await save()
  assert.deepEqual(result.writes, [['create', 'kb-a', { title: 'Draft title', content: 'Draft content', status: 'publish', tag_ids: ['tag-confirmed'], process_config: {} }]])
  assert.equal(result.notifications[0].kbId, 'kb-a')
  assert.equal(result.knowledge, 'new-file')
  assert.equal((await save({ target: 'draft', editing: true })).writes[0][0], 'update')
})

for (const change of ['actor', 'tenant', 'route', 'destination', 'closed', 'new-session']) {
  for (const phase of ['metadata', 'confirmation']) {
    test('an obsolete native publish cannot write after ' + change + ' changes during ' + phase, async () => {
      const result = await save({ change, phase })
      assert.deepEqual(result.writes, [])
      assert.deepEqual(result.notifications, [])
      assert.deepEqual(result.messages, [])
    })
  }
  test('a completed write cannot change another editor session after ' + change + ' changes', async () => {
    const result = await save({ change, phase: 'write' })
    assert.equal(result.writes.length, 1)
    assert.equal(result.knowledge, null)
    assert.deepEqual(result.notifications, [])
    assert.deepEqual(result.messages, [])
  })
}

test('the native editor refuses a mismatched knowledge-base metadata response', async () => {
  const result = await save({ wrongKB: true })
  assert.deepEqual(result.writes, [])
  assert.deepEqual(result.notifications, [])
})

function deferred() {
  let resolve!: (value: unknown) => void
  const promise = new Promise(value => { resolve = value })
  return { promise, resolve }
}
const tick = () => new Promise(resolve => setImmediate(resolve))
function readSession(editing = false, preset = 'kb-a') {
  const list = deferred(), metadata = deferred(), messages: string[] = []
  const sharedKnowledgeBases: any[] = []
  const chatResources = {
    rawKnowledgeBases: [] as any[],
    ensureKnowledgeBases: async () => {
      const response: any = await list.promise
      chatResources.rawKnowledgeBases = response?.data || []
    },
  }
  const form = { kbId: '', title: '', content: '', status: 'draft' }
  const auth = { user: { id: 'actor-a' }, effectiveTenantId: 10000 }
  const route = { params: { kbId: preset } }
  const ui = { manualEditorKBId: preset, manualEditorInitialTitle: '', manualEditorInitialContent: '', manualEditorInitialStatus: 'draft', selectedTagIds: [] }
  const visible = ref(true), options = ref<any[]>([]), knowledge = ref(editing ? 'file-a' : null), loaded = ref(false)
  const context: any = {
    ref, resolvePreferredKnowledgeBaseId, watch: () => {}, form, uiStore: ui, authStore: auth, route, visible, mode: ref(editing ? 'edit' : 'create'),
    knowledgeId: knowledge, currentKnowledgeId: ref(editing ? 'file-a' : null), kbOptions: options, initialLoaded: loaded,
    kbLoading: ref(false), contentLoading: ref(false), lastUpdatedAt: ref(''), manualTagIds: ref([]), selectionRange: { start: 0, end: 0 },
    t: (key: string) => key, markPristine: () => {},
    chatResources, organizationStore: { sharedKnowledgeBases },
    getKnowledgeDetails: () => metadata.promise, MessagePlugin: { error: (value: string) => messages.push(value) },
    console: { error: () => {}, warn: () => {} },
  }
  const sandbox = vm.createContext(context)
  const setup = source.includes('let editorGeneration =') ? code('let editorGeneration =', '// ---------- view mode') : ''
  vm.runInContext(setup + code('const resolveManualKnowledgeStatus =', 'const uiStore =') + code('const loadKnowledgeBases =', 'const validateForm ='), sandbox)
  return { sandbox, form, auth, route, ui, visible, options, knowledge, loaded, list, metadata, messages, context, sharedKnowledgeBases }
}
const listResponse = { data: [{ id: 'kb-a', name: 'Library A', type: 'document' }, { id: 'kb-b', name: 'Library B', type: 'document' }] }
function editResponse(id = 'file-a', kb = 'kb-a') {
  return { data: { id, knowledge_base_id: kb, title: 'Existing note', metadata: { content: 'Existing body', status: 'draft' }, tags: [{ id: 'tag-existing' }] } }
}
function changeReader(current: ReturnType<typeof readSession>, change: string) {
  if (change === 'actor') current.auth.user.id = 'actor-b'
  if (change === 'tenant') current.auth.effectiveTenantId = 10001
  if (change === 'route') current.route.params.kbId = 'kb-b'
  if (change === 'closed') current.visible.value = false
  if (change === 'new-session' && source.includes('let editorGeneration =')) vm.runInContext('editorGeneration++', current.sandbox)
}

test('native editor initialization loads its original list and existing document', async () => {
  const current = readSession(true)
  const initializing = vm.runInContext('initialize()', current.sandbox)
  current.list.resolve(listResponse)
  await tick()
  current.metadata.resolve(editResponse())
  await initializing
  assert.equal(current.loaded.value, true)
  assert.equal(current.form.title, 'Existing note')
  assert.equal(current.form.content, 'Existing body')
})

test('a global native editor without a preset library can resolve the actual document library', async () => {
  const current = readSession(true, '')
  const initializing = vm.runInContext('initialize()', current.sandbox)
  current.list.resolve(listResponse)
  await tick()
  current.metadata.resolve(editResponse('file-a', 'kb-b'))
  await initializing
  assert.equal(current.form.kbId, 'kb-b')
  assert.equal(current.loaded.value, true)
})

test('loading native editor options does not overwrite a destination chosen by the user', async () => {
  const current = readSession()
  const initializing = vm.runInContext('initialize()', current.sandbox)
  current.form.kbId = 'kb-b'
  current.list.resolve(listResponse)
  await initializing
  assert.equal(current.form.kbId, 'kb-b')
  assert.equal(current.options.value.length, 2)
})

test('the cached native picker includes writable shared document libraries without duplicate or read-only choices', async () => {
  const current = readSession(false, '')
  current.sharedKnowledgeBases.push(
    { permission: 'editor', knowledge_base: { id: 'shared-editor', name: 'Shared editor', type: 'document' } },
    { permission: 'admin', knowledge_base: { id: 'shared-admin', name: 'Shared admin' } },
    { permission: 'viewer', knowledge_base: { id: 'shared-viewer', name: 'Read only', type: 'document' } },
    { permission: 'editor', knowledge_base: { id: 'shared-faq', name: 'FAQ', type: 'faq' } },
    { permission: 'admin', knowledge_base: { id: 'kb-a', name: 'Duplicate', type: 'document' } },
  )
  const initializing = vm.runInContext('initialize()', current.sandbox)
  current.list.resolve({ data: [...listResponse.data, { id: 'own-faq', name: 'Own FAQ', type: 'faq' }] })
  await initializing
  assert.deepEqual(Array.from(current.options.value, item => item.value), ['kb-a', 'kb-b', 'shared-editor', 'shared-admin'])
  assert.equal(current.form.kbId, 'kb-a')
})

for (const change of ['actor', 'tenant', 'route', 'closed', 'new-session']) {
  test('late native knowledge-base choices cannot overwrite a changed ' + change, async () => {
    const current = readSession()
    const initializing = vm.runInContext('initialize()', current.sandbox)
    changeReader(current, change)
    current.form.title = 'New session title'
    current.options.value = [{ label: 'New session library', value: 'new-library' }]
    current.list.resolve(listResponse)
    await initializing
    assert.equal(current.form.title, 'New session title')
    assert.equal(current.options.value[0].value, 'new-library')
    assert.equal(current.loaded.value, false)
    assert.deepEqual(current.messages, [])
  })
  test('late native document metadata cannot overwrite a changed ' + change, async () => {
    const current = readSession(true)
    const initializing = vm.runInContext('initialize()', current.sandbox)
    current.list.resolve(listResponse)
    await tick()
    changeReader(current, change)
    current.form.title = 'New session title'
    current.form.content = 'New session content'
    current.metadata.resolve(editResponse())
    await initializing
    assert.equal(current.form.title, 'New session title')
    assert.equal(current.form.content, 'New session content')
    assert.equal(current.loaded.value, false)
    assert.deepEqual(current.messages, [])
  })
}

for (const response of [editResponse('other-file'), editResponse('file-a', 'kb-b'), { data: null }]) {
  test('an invalid existing-document response cannot initialize the editor: ' + JSON.stringify(response), async () => {
    const current = readSession(true)
    const initializing = vm.runInContext('initialize()', current.sandbox)
    current.list.resolve(listResponse)
    await tick()
    current.metadata.resolve(response)
    await initializing
    assert.equal(current.form.title, '')
    assert.equal(current.form.content, '')
    assert.equal(current.loaded.value, false)
  })
}
