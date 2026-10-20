import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { computed, ref } from 'vue'

const source = readFileSync(new URL('./KnowledgeBase.vue', import.meta.url), 'utf8')
const begin = source.indexOf('const showKbDetailContextualGuide = computed(')
const end = source.indexOf('\nconst onVisibleChange', begin)
assert.ok(begin >= 0 && end > begin)
const code = source.slice(begin, end) + '\nshowKbDetailContextualGuide'

function guide(overrides: Record<string, unknown> = {}) {
  const state = { kbId: 'library', isFAQ: false, canEdit: true, docListLoading: false,
    cardList: [], folderTreeLoading: false, folderTree: { total_document_count: 0 }, ...overrides }
  return runInNewContext(code, { computed, ...Object.fromEntries(Object.entries(state).map(([k, v]) => [k, ref(v)])) }).value
}

test('an empty root or selected folder is not an empty knowledge base', () => {
  assert.equal(guide({ folderTree: { root_document_count: 0, total_document_count: 7 } }), false)
})
test('the first-document guide waits for the complete directory count', () => {
  assert.equal(guide({ folderTree: null }), false)
  assert.equal(guide({ folderTreeLoading: true }), false)
  assert.equal(guide({ docListLoading: true }), false)
})
test('a confirmed empty editable document library still receives its first-document guide', () => {
  assert.equal(guide(), true)
  for (const changes of [{ canEdit: false }, { isFAQ: true }, { kbId: '' }, { cardList: [{ id: 'document' }] }]) {
    assert.equal(guide(changes), false)
  }
})
