import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { computed, ref } from 'vue'
import { buildListKnowledgeFilesQuery } from '../../api/knowledge-base/knowledgeFileListQuery'
import { buildSourceFileUploads, findSourceFileVersionChoices, sourceFileVersionConflict } from './sourceFileVersion'

const original = { id: 'original', file_name: '数据.xlsx', folder_path: '归档' }
const source = new File(['updated data'], original.file_name)
const unique = new File(['new data'], '其他.xlsx')

test('same-name lookup queries the full knowledge base and prefers an exact match in the destination folder', async () => {
  const target = { ...original, id: 'target', folder_path: '研究' }
  const matches = await findSourceFileVersionChoices([source, unique], '研究', async params => {
    const query = new URLSearchParams(buildListKnowledgeFilesQuery(params))
    assert.deepEqual(query.getAll('file_names'), ['数据.xlsx', '其他.xlsx'])
    assert.equal(query.has('folder_path'), false)
    return { success: true, total: 3, data: [original, target, { id: 'partial', file_name: '更多数据.xlsx' }] }
  })
  assert.deepEqual(matches.map(choice => [choice.index, choice.knowledgeId]), [[0, target.id]])
  assert.deepEqual(matches[0].candidates.map(candidate => candidate.id), [target.id, original.id])
})

test('same-name lookup uses subsequent pages and distinguishes repeated targets in a batch', async () => {
  const second = { ...original, id: 'other' }
  const pages: number[] = []
  const choices = await findSourceFileVersionChoices([source, source], '', async params => {
    pages.push(params.page)
    return { success: true, total: 101, data: params.page === 1 ? [original] : [second] }
  })
  assert.deepEqual(pages, [1, 2])
  assert.deepEqual(choices.map(choice => choice.knowledgeId), ['original', 'other'])
})

const page = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')
const flow = page.slice(page.indexOf('const versionChoices ='), page.indexOf('// Hands the batch to the global upload queue'))
const executable = ts.transpileModule(flow, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

async function confirmUpload(choose: 'default' | 'new' | 'cancel' | 'scope' | 'lookup-error') {
  let scopeCurrent = true
  const errors: string[] = []
  const dialog = ref(false)
  const context = vm.createContext({
    ref, computed, watch: () => {}, onUnmounted: () => {}, captureKnowledgeScope: () => ({}),
    source, canEdit: ref(true), kbId: ref('library-a'), findSourceFileVersionChoices, sourceFileVersionConflict,
    listKnowledgeFiles: async (kbId: string) => {
      assert.equal(kbId, 'library-a')
      if (choose === 'lookup-error') throw new Error('lookup failed')
      if (choose === 'scope') scopeCurrent = false
      return { success: true, total: 1, data: [original] }
    },
    MessagePlugin: { error: (value: string) => errors.push(value) }, t: (key: string) => key,
    isScopeCurrent: () => scopeCurrent,
  })
  vm.runInContext(executable, context)
  const pending = vm.runInContext("chooseUploadVersions([source], '研究', isScopeCurrent)", context)
  await new Promise(resolve => setImmediate(resolve))
  dialog.value = vm.runInContext('versionChoiceDialogOpen.value', context)
  if (dialog.value) {
    assert.equal(vm.runInContext('versionChoices.value[0].knowledgeId', context), original.id)
    if (choose === 'cancel') vm.runInContext('dismissVersionChoices()', context)
    else {
      if (choose === 'new') vm.runInContext("versionChoices.value[0].knowledgeId = ''", context)
      vm.runInContext('submitVersionChoices()', context)
    }
  }
  const result = await pending
  assert.equal(vm.runInContext('versionChoiceDialogOpen.value', context), false)
  return { result, errors, opened: dialog.value }
}

test('ordinary upload prompts whether to replace the source and defaults to its existing knowledge ID', async () => {
  const confirmed = await confirmUpload('default')
  assert.equal(confirmed.opened, true)
  assert.deepEqual(buildSourceFileUploads([source, unique], '研究', confirmed.result), [
    { file: source, replaceKnowledgeId: original.id },
    { file: unique, fileName: '研究/其他.xlsx' },
  ])
  const options = readFileSync(new URL('./components/SourceFileVersionOptions.vue', import.meta.url), 'utf8')
  assert.match(page, /<SourceFileVersionOptions.*v-model:knowledge-id="choice.knowledgeId"/)
  assert.match(options, /\$t\('knowledgeBase.uploadAsFileVersion', \{ fileName: choice.fileName \}\)/)
  assert.match(options, /<option value="">\{\{ \$t\('knowledgeBase.saveFileAsNew'\) \}\}<\/option>/)
})

test('explicit save as new keeps the chosen folder and requests a separate file even for identical bytes', async () => {
  const confirmed = await confirmUpload('new')
  assert.deepEqual(buildSourceFileUploads([source], '研究', confirmed.result), [
    { file: source, fileName: '研究/数据.xlsx', saveAsNew: true },
  ])
})

test('cancel, scope changes and failed duplicate checks do not produce an upload plan', async () => {
  for (const action of ['cancel', 'scope', 'lookup-error'] as const) {
    const confirmed = await confirmUpload(action)
    assert.equal(confirmed.result, null)
    assert.equal(confirmed.opened, action === 'cancel')
    assert.deepEqual(confirmed.errors, action === 'lookup-error' ? ['knowledgeBase.fileVersionLookupFailed'] : [])
  }
})

test('list and card views expose explicit replacement for writable file rows', () => {
  for (const view of ['DocumentListView', 'DocumentCardView']) {
    const component = readFileSync(new URL(`./components/${view}.vue`, import.meta.url), 'utf8')
    assert.match(component, /<DocumentActionMenu[\s\S]*?:can-edit="canEdit"[\s\S]*?@upload-version="handleAction\('upload-version', item\)"/)
  }
  const menu = readFileSync(new URL('./components/DocumentActionMenu.vue', import.meta.url), 'utf8')
  assert.match(menu, /<button v-if="canEdit && item.type === 'file'"[^>]*emit\('upload-version'\)/)
  assert.match(page, /uploads: \[\{ file, replaceKnowledgeId: target.id \}\]/)
})
