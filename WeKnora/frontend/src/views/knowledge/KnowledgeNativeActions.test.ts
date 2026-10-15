import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { computed, ref } from 'vue'
import { isExcelFilename, sameNativeUploadScope } from './excelSource.ts'
import { newKnowledgeFolderPath } from '../workspaces/knowledgeDirectory.ts'

const knowledge = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')
const dropdown = readFileSync(new URL('./components/KbUploadSourceDropdown.vue', import.meta.url), 'utf8')
function code(source: string, from: string, to: string) {
  const start = source.indexOf(from), end = source.indexOf(to, start)
  assert.ok(start >= 0 && end > start, `actual component must provide ${from}`)
  return ts.transpileModule(source.slice(start, end), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
}

test('the native knowledge toolbar provides webpage import and its existing editor', () => {
  const toolbar = knowledge.slice(knowledge.indexOf('<div v-if="canEdit" class="doc-filter-actions">'), knowledge.indexOf('<div v-if="visibleChildFolders.length"'))
  assert.match(toolbar, /<KbUploadSourceDropdown\b[^>]*include-manual/)
  assert.doesNotMatch(toolbar, /<KbUploadSourceDropdown\b[^>]*\bunified\b/)
  assert.match(toolbar, /@url="handleUploadSourceUrl"/)
  assert.match(toolbar, /@manual="handleManualCreate"/)
})

test('the exposed webpage action opens the native URL dialog without materials navigation', () => {
  const events: unknown[] = []
  const context = { props: { unified: false }, urlInputValue: ref('old'), urlDialogVisible: ref(false), emit: (...args: unknown[]) => events.push(args) }
  vm.runInNewContext(code(dropdown, 'const openUrlDialog =', 'defineExpose') + '\nopenUrlDialog()', context)
  assert.equal(context.urlDialogVisible.value, true)
  assert.equal(context.urlInputValue.value, '')
  assert.deepEqual(events, [])
  assert.doesNotMatch(dropdown, /emit\('intake'/)
})

async function urlConfirmation(patch: Partial<Record<'actor' | 'tenant' | 'kb', string>> = {}, ready = true) {
  const imports: unknown[][] = [], notices: string[] = [], opens: any[] = []
  const authStore = { user: { id: 'user-a' }, effectiveTenantId: 10000 }
  const router = { currentRoute: ref({ params: { kbId: 'kb-a' } }) }
  const context = {
    kbInfo: ref({ id: 'kb-a' }), kbId: ref('kb-a'), authStore, router,
    canEdit: ref(true), isExcelFilename, sameNativeUploadScope, ROOT_FOLDER_PATH: '',
    selectedTagIds: ref(['selected']), selectedFolderPath: ref('chosen'), folderOptions: ref([]),
    acceptFileTypes: ref('.pdf,.xlsx'), supportedFileTypes: ref(new Set(['pdf', 'xlsx'])),
    uploadConfirmStore: { open: async (options: unknown) => {
      opens.push(options)
      if (patch.actor) authStore.user.id = patch.actor
      if (patch.tenant) authStore.effectiveTenantId = Number(patch.tenant)
      if (patch.kb) router.currentRoute.value.params.kbId = patch.kb
      return { mode: 'file', files: [], urls: ['https://example.test/page'], processConfig: { summary: true }, tagIds: ['confirmed'] }
    } },
    ensureDocumentKbReady: () => ready,
    enqueueUploads: () => { throw new Error('a webpage source must use the native URL endpoint') },
    executeUrlImport: (...args: unknown[]) => imports.push(args),
    MessagePlugin: { warning: (value: string) => notices.push(value) }, t: (key: string) => key,
  }
  await vm.runInNewContext(code(knowledge, 'const handleUploadConfirmResult =', 'const handleUploadSourceFiles =') + "\nopenUploadConfirmDialog([], ['https://example.test/page'])", context)
  return { imports: JSON.parse(JSON.stringify(imports)), opens, notices }
}

test('webpage confirmation retains native processing options, tags and URL import', async () => {
  const result = await urlConfirmation()
  assert.equal(result.opens[0].allowURL, true)
  assert.deepEqual(result.imports, [['https://example.test/page', { summary: true }, ['confirmed']]])
})

for (const patch of [{ actor: 'user-b' }, { tenant: '10001' }, { kb: 'kb-b' }]) {
  test('an obsolete webpage confirmation cannot submit after ' + Object.keys(patch)[0] + ' changes', async () => {
    const result = await urlConfirmation(patch)
    assert.deepEqual(result.imports, [])
    assert.deepEqual(result.notices, ['knowledgeBase.uploadScopeChanged'])
  })
}

test('webpages retain the native non-Excel initialization requirement', async () => {
  assert.deepEqual((await urlConfirmation({}, false)).imports, [])
})

async function createFolder(options: { parent?: string; name?: string; existing?: string[]; editable?: boolean; switchBeforeConfirm?: boolean; switchDuringRequest?: boolean; reject?: boolean } = {}) {
  const posted: unknown[][] = [], events: any[] = [], notices: string[] = [], watchers: any[] = []
  const router = { currentRoute: ref({ params: { kbId: 'kb-a' } }) }
  const context: any = {
    ref, watch: (...args: unknown[]) => watchers.push(args), computed,
    kbId: ref('kb-a'), kbInfo: ref({ name: 'Library' }), authStore: { user: { id: 'user-a' }, effectiveTenantId: 10000 }, router,
    canEdit: ref(options.editable !== false), selectedFolderPath: ref(options.parent || ''), ROOT_FOLDER_PATH: '',
    folderOptions: ref((options.existing || []).map(path => ({ path }))), newKnowledgeFolderPath, sameNativeUploadScope,
    KNOWLEDGE_DIRECTORY_EVENT: 'directory-change', CustomEvent: class { detail: unknown; constructor(public type: string, options: { detail: unknown }) { this.detail = options.detail } },
    window: { dispatchEvent: (event: unknown) => events.push(event) },
    MessagePlugin: { success: (key: string) => notices.push(key), warning: (key: string) => notices.push(key) }, t: (key: string) => key,
    workspaceErrorMessage: (error: Error) => error.message,
    createKnowledgeFolder: async (...args: unknown[]) => {
      posted.push(args)
      if (options.switchDuringRequest) router.currentRoute.value.params.kbId = 'kb-b'
      if (options.reject) throw new Error('server refused creation')
    },
  }
  const setup = code(knowledge, 'const createFolderOpen =', 'const moveKnowledgeIntoFolder =')
  const executable = vm.createContext({ ...context, inputName: options.name ?? 'New folder' })
  vm.runInContext(setup + '\nopenCreateFolder(); createFolderName.value = inputName;', executable)
  if (options.switchBeforeConfirm) router.currentRoute.value.params.kbId = 'kb-b'
  await vm.runInContext('submitCreateFolder()', executable)
  return { posted: JSON.parse(JSON.stringify(posted)), events, notices, error: vm.runInContext('createFolderError.value', executable), visible: vm.runInContext('createFolderOpen.value', executable) }
}

test('the knowledge toolbar exposes persistent empty-folder creation', () => {
  assert.match(knowledge, /data-guide="kb-detail-new-folder"/)
  assert.match(knowledge, /@click="openCreateFolder"/)
  assert.match(knowledge, /@confirm="submitCreateFolder"/)
})

for (const parent of ['', 'Outer/Inner']) {
  test('a real folder is created under the currently selected parent: ' + (parent || 'root'), async () => {
    const result = await createFolder({ parent, name: '  Notes  ' })
    assert.deepEqual(result.posted, [['kb-a', parent ? `${parent}/Notes` : 'Notes']])
    assert.equal(result.visible, false)
    assert.equal(result.events[0].type, 'directory-change')
    assert.equal(result.events[0].detail.kbId, 'kb-a')
  })
}

for (const name of ['', '..', 'bad/name', 'bad\\name', 'ends.', 'x'.repeat(129)]) {
  test('an invalid empty-folder name is rejected before any write: ' + JSON.stringify(name.slice(0, 20)), async () => {
    const result = await createFolder({ name })
    assert.deepEqual(result.posted, [])
    assert.ok(result.error)
  })
}

test('duplicate, viewer, stale confirmation and failed folder writes do not fake success', async () => {
  assert.deepEqual((await createFolder({ parent: 'Outer', name: 'Notes', existing: ['Outer/Notes'] })).posted, [])
  assert.deepEqual((await createFolder({ editable: false })).posted, [])
  assert.deepEqual((await createFolder({ switchBeforeConfirm: true })).posted, [])
  const failed = await createFolder({ reject: true })
  assert.equal(failed.error, 'server refused creation')
  assert.equal(failed.visible, true)
  assert.deepEqual(failed.events, [])
  const changed = await createFolder({ switchDuringRequest: true })
  assert.deepEqual(changed.events, [])
  assert.deepEqual(changed.notices, [])
})

async function folderMutation(action: 'move' | 'rename', change = '') {
  const posts: any[] = [], events: any[] = [], selected: string[] = []
  const scope = { userId: 'actor-a', tenantId: '10000', knowledgeBaseId: 'kb-a' }
  const api = async (...args: unknown[]) => {
    posts.push(args)
    if (change === 'actor') scope.userId = 'actor-b'
    if (change === 'tenant') scope.tenantId = '10001'
    if (change === 'kb') scope.knowledgeBaseId = 'kb-b'
  }
  const context = {
    kbId: ref('kb-a'), canEdit: ref(true), captureKnowledgeScope: () => ({ ...scope }), sameNativeUploadScope,
    moveKnowledgeToFolder: api, renameKnowledgeFolder: api, canMoveFolderTo: () => true,
    selectedFolderPath: ref('Old/Inner'), handleFolderSelect: (path: string) => selected.push(path),
    clearSelection: () => {}, batchMode: ref(true), MessagePlugin: { success: () => {}, error: () => {}, warning: () => {} }, t: (key: string) => key,
    KNOWLEDGE_DIRECTORY_EVENT: 'directory-change', CustomEvent: class { detail: unknown; constructor(public type: string, options: { detail: unknown }) { this.detail = options.detail } },
    window: { dispatchEvent: (event: unknown) => events.push(event) },
    resetPage: () => {}, loadKnowledgeFiles: async () => {}, loadFolderTree: async () => {},
  }
  const script = code(knowledge, 'const moveKnowledgeIntoFolder =', 'const handleFolderTreeCollapsedChange =')
  const invocation = action === 'move' ? "moveKnowledgeIntoFolder(['file-a'], 'Other')" : "handleFolderRename({ from: 'Old', to: 'Renamed' })"
  await vm.runInNewContext(script + '\n' + invocation, context)
  return { posts: JSON.parse(JSON.stringify(posts)), events, selected }
}

for (const action of ['move', 'rename'] as const) {
  test('native ' + action + ' refreshes both the directory sidebar and file list', async () => {
    const result = await folderMutation(action)
    assert.equal(result.events.length, 1)
    assert.equal(result.events[0].detail.kbId, 'kb-a')
    if (action === 'rename') assert.deepEqual(result.selected, ['Renamed/Inner'])
  })
  for (const change of ['actor', 'tenant', 'kb']) {
    test('a completed native ' + action + ' cannot refresh or navigate a changed ' + change, async () => {
      const result = await folderMutation(action, change)
      assert.equal(result.posts.length, 1)
      assert.deepEqual(result.events, [])
      assert.deepEqual(result.selected, [])
    })
  }
}

test('successful native editor writes refresh the real directory without replaying stale callbacks', () => {
  const events: any[] = []
  const scope = { userId: 'actor-a', tenantId: '10000', knowledgeBaseId: 'kb-a' }
  const original = { ...scope }
  const context = {
    captureKnowledgeScope: () => ({ ...scope }), sameNativeUploadScope, isFAQ: ref(false),
    KNOWLEDGE_DIRECTORY_EVENT: 'directory-change', CustomEvent: class { detail: unknown; constructor(public type: string, options: { detail: unknown }) { this.detail = options.detail } },
    window: { dispatchEvent: (event: unknown) => events.push(event) }, original,
  }
  const sandbox = vm.createContext(context)
  vm.runInContext(code(knowledge, 'const manualEditorSuccess =', 'const ensureDocumentKbReady ='), sandbox)
  vm.runInContext("manualEditorSuccess({ kbId: 'kb-a' }, original)", sandbox)
  assert.equal(events.length, 1)
  assert.equal(events[0].detail.kbId, 'kb-a')
  vm.runInContext("manualEditorSuccess({ kbId: 'kb-b' }, original)", sandbox)
  scope.userId = 'actor-b'
  vm.runInContext("manualEditorSuccess({ kbId: 'kb-a' }, original)", sandbox)
  assert.equal(events.length, 1)
})

async function deletion(change = '', phase = 'submit') {
  const events: any[] = []
  const scope = { userId: 'actor-a', tenantId: '10000', knowledgeBaseId: 'kb-a' }
  const changeScope = (at: string) => {
    if (at !== phase) return
    if (change === 'actor') scope.userId = 'actor-b'
    if (change === 'tenant') scope.tenantId = '10001'
    if (change === 'kb') scope.knowledgeBaseId = 'kb-b'
  }
  const context = {
    deleteGeneration: 0, kbId: ref('kb-a'), isCurrentKb: (id: string) => id === scope.knowledgeBaseId,
    captureKnowledgeScope: () => ({ ...scope }), sameNativeUploadScope,
    batchDeleting: ref(false), batchReparsing: ref(false), batchDownloading: ref(false), batchMode: ref(false),
    clearSelection: () => {}, MessagePlugin: { success: () => {}, error: () => {}, warning: () => {}, info: () => {} }, t: (key: string) => key,
    waitForKnowledgeDeletion: async () => { changeScope('poll'); return 'completed' },
    submit: async () => { changeScope('submit'); return { success: true } },
    loadTags: async () => {}, KNOWLEDGE_DIRECTORY_EVENT: 'directory-change',
    CustomEvent: class { detail: unknown; constructor(public type: string, options: { detail: unknown }) { this.detail = options.detail } },
    window: { dispatchEvent: (event: unknown) => events.push(event) },
  }
  await vm.runInNewContext(code(knowledge, 'const deleteKnowledgeDocuments =', 'const confirmBatchDelete =') + "\ndeleteKnowledgeDocuments(['file-a'], submit, false)", context)
  return events
}

test('native file deletion refreshes both directory views after confirmation', async () => {
  const events = await deletion()
  assert.equal(events.length, 1)
  assert.equal(events[0].detail.kbId, 'kb-a')
})

for (const change of ['actor', 'tenant', 'kb']) {
  test('late deletion status cannot refresh a changed ' + change, async () => {
    assert.deepEqual(await deletion(change, 'submit'), [])
    assert.deepEqual(await deletion(change, 'poll'), [])
  })
}
