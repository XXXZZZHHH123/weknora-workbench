import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { isExcelFilename } from '../excelSource.ts'

const component = readFileSync(new URL('./UploadConfirmDialog.vue', import.meta.url), 'utf8')
const start = component.indexOf('const validateBeforeConfirm =')
const end = component.indexOf('</script>', start)
assert.ok(start > 0 && end > start)
// Execute the component's real confirmation handlers. Browser APIs and the
// model-settings UI are incidental here; replacing the validator would hide
// the regression where disabled controls still blocked storing an original.
const handlers = ts.transpileModule(component.slice(start, end), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

function confirm(names: string[], missingModel = true, urls: string[] = []) {
  const files = names.map(name => ({ name, size: 10 }))
  const events: unknown[][] = [], notices: string[] = [], sections: string[] = []
  let configurations = 0
  const context = {
    props: { mode: 'file', allowURL: false }, batchItemCount: { value: files.length + urls.length },
    sourceOnlyBatch: { value: files.length > 0 && files.every(file => isExcelFilename(file.name)) },
    hasImages: { value: names.some(name => name.endsWith('.png')) }, hasAudio: { value: false },
    showMultimodalModelError: { value: missingModel }, showAsrModelError: { value: missingModel },
    uiState: { value: { multimodalConfig: { enabled: true, vllmModelId: missingModel ? '' : 'model' },
                       asrConfig: { enabled: true, modelId: missingModel ? '' : 'model' } } },
    localFiles: { value: files }, localUrls: { value: urls }, selectedTagIds: { value: [] }, localTargetFolder: { value: 'folder' },
    MessagePlugin: { warning: (message: string) => notices.push(message) }, t: (key: string) => key,
    goToSection: (section: string) => sections.push(section),
    buildProcessOverrides: () => { configurations++; return { summary_enabled: true } },
    emit: (...event: unknown[]) => events.push(event),
  }
  vm.runInNewContext(handlers + '\nhandleConfirm()', context)
  return { events: JSON.parse(JSON.stringify(events)), notices, sections, configurations }
}

test('storing Excel originals is not blocked by missing hidden VLM or ASR models', () => {
  const result = confirm(['source.xlsx', 'archive.xls'])
  assert.equal(result.events[0]?.[0], 'confirm')
  assert.deepEqual(result.events[0][1].files.map((file: { name: string }) => file.name), ['source.xlsx', 'archive.xls'])
  assert.deepEqual(result.events[0][1].processConfig, {})
  assert.equal(result.configurations, 0)
  assert.deepEqual(result.notices, [])
})

test('a file-only confirmation refuses injected URL sources', () => {
  const result = confirm(['source.xlsx'], false, ['https://example.test/source.xlsx'])
  assert.deepEqual(result.events, [])
  assert.equal(result.notices[0], 'knowledgeBase.nativeFileSourcesOnly')
  assert.equal(result.configurations, 0)
})

test('appending a non-Excel file restores existing model validators', () => {
  const blocked = confirm(['source.xlsx', 'append.pdf'])
  assert.deepEqual(blocked.events, [])
  assert.equal(blocked.sections[0], 'multimodal')
  const allowed = confirm(['source.xlsx', 'append.pdf'], false)
  assert.equal(allowed.events[0]?.[0], 'confirm')
  assert.equal(allowed.configurations, 1)
})
