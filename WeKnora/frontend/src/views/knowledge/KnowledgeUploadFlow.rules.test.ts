import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { computed } from 'vue'
import { isExcelFilename, sameNativeUploadScope } from './excelSource.ts'
import { shouldRejectKnowledgeFileType } from '../../utils/fileTypeVerification.ts'

const component = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')
function actualCode(from: string, to: string) {
  const start = component.indexOf(from), end = component.indexOf(to, start)
  assert.ok(start >= 0 && end > start)
  return ts.transpileModule(component.slice(start, end), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
}
const types = actualCode('const supportedFileTypes =', 'const acceptFileTypes =')
const confirmations = actualCode('const handleUploadConfirmResult =', 'const handleUploadSourceFiles =')

test('unknown engines preserve the actual default file whitelist for Excel and documents', () => {
  const supported = vm.runInNewContext(types + '\nsupportedFileTypes.value', {
    computed, parserEngines: { value: [] }, kbInfo: { value: {} },
  }) as Set<string>
  for (const filename of ['source.xlsx', 'legacy.XLS', 'report.pdf', 'notes.docx', 'notes.md']) {
    assert.equal(shouldRejectKnowledgeFileType(filename, supported), false, filename)
  }
  assert.equal(shouldRejectKnowledgeFileType('payload.exe', supported), true)
})

test('an unavailable Excel parser does not block originals while other formats stay filtered', () => {
  const supported = vm.runInNewContext(types + '\nsupportedFileTypes.value', {
    computed, parserEngines: { value: [
      { Name: 'builtin', Available: true, FileTypes: ['pdf', 'xlsx', 'xls'] },
      { Name: 'unavailable', Available: false, FileTypes: ['xlsx', 'pptx'] },
    ] }, kbInfo: { value: { chunking_config: { parser_engine_rules: [
      { file_types: ['xlsx'], engine: 'unavailable' }, { file_types: ['pdf'], engine: 'unavailable' },
    ] } } },
  }) as Set<string>
  assert.equal(shouldRejectKnowledgeFileType('source.xlsx', supported), false)
  assert.equal(shouldRejectKnowledgeFileType('legacy.xls', supported), false)
  assert.equal(shouldRejectKnowledgeFileType('report.pdf', supported), true)
  assert.equal(shouldRejectKnowledgeFileType('slides.pptx', supported), true)
})

async function confirm(patch: Record<string, unknown> = {}, finalNames = ['source.xlsx'], modelsReady = false, urls: string[] = []) {
  const queued: unknown[][] = [], notices: string[] = [], opens: any[] = []
  let initialized = 0
  const identity = { user: { id: 'user-a' }, effectiveTenantId: 10000 }
  const library = { value: 'kb-a' }
  const activeRoute = { value: { params: { kbId: 'kb-a' } } }
  const context = {
    kbInfo: { value: { id: 'kb-a', name: 'Library' } }, kbId: library, authStore: identity,
    router: { currentRoute: activeRoute },
    canEdit: { value: true }, isExcelFilename, sameNativeUploadScope,
    ROOT_FOLDER_PATH: '', selectedTagIds: { value: [] }, selectedFolderPath: { value: 'initial' },
    folderOptions: { value: [] }, acceptFileTypes: { value: '.xlsx,.pdf' }, supportedFileTypes: { value: new Set(['xlsx', 'pdf']) },
    uploadConfirmStore: { open: async (options: unknown) => {
      opens.push(options)
      if (patch.userId) identity.user.id = String(patch.userId)
      if (patch.tenantId) identity.effectiveTenantId = Number(patch.tenantId)
      if (patch.kbId) {
        activeRoute.value.params.kbId = String(patch.kbId)
        if (!patch.keepUnmountedComponentId) library.value = String(patch.kbId)
      }
      return { mode: 'file', files: finalNames.map(name => ({ name, size: 10 })), urls,
        processConfig: {}, tagIds: [], targetFolder: 'chosen/subfolder' }
    } },
    ensureDocumentKbReady: () => { initialized++; return modelsReady },
    enqueueUploads: (...arguments_: unknown[]) => queued.push(arguments_),
    executeUrlImport: () => { throw new Error('file confirmation must not import URLs') },
    MessagePlugin: { warning: (message: string) => notices.push(message) }, t: (key: string) => key,
    initial: [{ name: 'source.xlsx', size: 10 }],
  }
  await vm.runInNewContext(confirmations + '\nopenUploadConfirmDialog(initial)', context)
  return { queued: JSON.parse(JSON.stringify(queued)), notices, opens: JSON.parse(JSON.stringify(opens)), initialized }
}

test('an unmounted component cannot confirm against its stale local knowledge-base ref', async () => {
  const result = await confirm({ kbId: 'kb-b', keepUnmountedComponentId: true })
  assert.deepEqual(result.queued, [])
  assert.equal(result.notices[0], 'knowledgeBase.uploadScopeChanged')
})

test('the actual file confirmation stores Excel without models and keeps the chosen folder', async () => {
  const result = await confirm()
  assert.equal(result.initialized, 0)
  assert.equal(result.queued[0][1].targetFolder, 'chosen/subfolder')
  assert.equal(result.opens[0].allowURL, false)
})

for (const patch of [{ userId: 'user-b' }, { tenantId: 10001 }, { kbId: 'kb-b' }]) {
  test('an obsolete file confirmation cannot upload into a changed scope: ' + Object.keys(patch)[0], async () => {
    const result = await confirm(patch)
    assert.deepEqual(result.queued, [])
    assert.equal(result.notices[0], 'knowledgeBase.uploadScopeChanged')
  })
}

test('adding PDF during Excel confirmation rechecks the final batch initialization', async () => {
  const rejected = await confirm({}, ['source.xlsx', 'added.pdf'])
  assert.equal(rejected.initialized, 1)
  assert.deepEqual(rejected.queued, [])
  const allowed = await confirm({}, ['source.xlsx', 'added.pdf'], true)
  assert.equal(allowed.queued[0][0].length, 2)
})

test('the Native file-flow rejects URL sources even if a result is injected', async () => {
  const result = await confirm({}, ['source.xlsx'], true, ['https://example.test/source.xlsx'])
  assert.deepEqual(result.queued, [])
  assert.equal(result.notices[0], 'knowledgeBase.nativeFileSourcesOnly')
})
