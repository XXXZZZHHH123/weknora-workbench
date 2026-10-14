import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'
import { sameNativeUploadScope } from './excelSource.ts'

const source = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')

test('referenced documents navigate to their containing folder before opening details', () => {
  const resolveFolder = source.indexOf('await getKnowledgeDetails(targetId)')
  const selectFolder = source.indexOf('selectedFolderPath.value = detail.folder_path || ROOT_FOLDER_PATH')
  const openDetails = source.indexOf('openCardDetails(target)', selectFolder)

  assert.ok(resolveFolder >= 0, 'document details should be fetched to resolve folder_path')
  assert.ok(selectFolder > resolveFolder, 'the containing folder should be selected after details load')
  assert.ok(openDetails > selectFolder, 'the detail drawer should open after folder navigation')
})

test('a newer referenced-document navigation supersedes an older request', () => {
  assert.match(source, /const request = \+\+autoOpenRequest/)
  assert.match(source, /if \(request !== autoOpenRequest\) return;/)
})

async function openReference(options: { detail?: any; change?: string; reject?: boolean; changeAfterTick?: boolean } = {}) {
  const opened: any[] = []
  const scope = { userId: 'actor-a', tenantId: '10000', knowledgeBaseId: 'kb-a' }
  const folder = ref('')
  const snippet = source.slice(source.indexOf('const pendingKnowledgeId ='), source.indexOf('// React to later ?knowledge_id='))
  const code = ts.transpileModule(snippet, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const detail = options.detail || { id: 'file-a', knowledge_base_id: 'kb-a', folder_path: 'Outer/Inner', file_name: 'Notes.txt' }
  const change = () => {
    if (options.change === 'actor') scope.userId = 'actor-b'
    if (options.change === 'tenant') scope.tenantId = '10001'
    if (options.change === 'kb') scope.knowledgeBaseId = 'kb-b'
  }
  const context = {
    ref, watch: () => {}, route: { query: { knowledge_id: 'file-a' } }, kbId: ref('kb-a'),
    authStore: { user: { id: 'actor-a' }, effectiveTenantId: 10000 }, isCardDetails: ref(false), uiStore: {},
    cardList: ref([]), captureKnowledgeScope: () => ({ ...scope }), sameNativeUploadScope,
    selectedFolderPath: folder, ROOT_FOLDER_PATH: '', openCardDetails: (data: any) => opened.push(data),
    getKnowledgeDetails: async () => {
      if (!options.changeAfterTick) change()
      if (options.reject) throw new Error('Denied')
      return { data: detail }
    },
    nextTick: async () => { if (options.changeAfterTick) change() }, console: { error: () => {} },
  }
  await vm.runInNewContext(code + '\ntryAutoOpenDocument()', context)
  return { opened, folder: folder.value }
}

test('a validated native reference opens its actual file in its actual directory', async () => {
  const result = await openReference()
  assert.equal(result.opened[0].id, 'file-a')
  assert.equal(result.opened[0].knowledge_base_id, 'kb-a')
  assert.equal(result.folder, 'Outer/Inner')
})

for (const detail of [{ id: 'file-a', knowledge_base_id: 'kb-b' }, { id: 'file-b', knowledge_base_id: 'kb-a' }, { id: 'file-a' }]) {
  test('wrong-library, wrong-ID and missing-ownership references do not open native drawers: ' + JSON.stringify(detail), async () => {
    const result = await openReference({ detail })
    assert.deepEqual(result.opened, [])
    assert.equal(result.folder, '')
  })
}

test('a denied native reference has no ID-only fallback', async () => {
  assert.deepEqual((await openReference({ reject: true })).opened, [])
})

for (const change of ['actor', 'tenant', 'kb']) {
  test('a reference response cannot open under a changed ' + change, async () => {
    assert.deepEqual((await openReference({ change })).opened, [])
    assert.deepEqual((await openReference({ change, changeAfterTick: true })).opened, [])
  })
}
