import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const batchBar = readFileSync(new URL('./DocumentBatchBar.vue', import.meta.url), 'utf8')
const cardView = readFileSync(new URL('./DocumentCardView.vue', import.meta.url), 'utf8')
const listView = readFileSync(new URL('./DocumentListView.vue', import.meta.url), 'utf8')

// Root exclusion and rename/delete state are exercised through the actual
// compiled action component in KnowledgeResourceActions.behavior.test.ts.
// Picking a folder is a small, reversible action, so it stays a popup: in the row
// menu it is another level of the menu that is already open, and in the batch bar
// it hangs off the button. Neither should escalate to a modal dialog.
test('the folder picker is a popup rather than a modal', () => {
  assert.match(cardView, /folderPickerItemId === item\.id/)
  assert.match(listView, /folderPickerItemId === item\.id/)
  assert.match(batchBar, /<t-popup[^>]*v-model:visible="folderPickerVisible"/)
  for (const source of [cardView, listView, batchBar]) {
    assert.doesNotMatch(source, /MoveToFolderDialog/)
  }
})

// The folder picker must render ahead of the normal action menu; otherwise
// moveMenuMode === 'normal' keeps the menu visible and clicks look dead.
test('the folder picker wins over the normal action menu', () => {
  for (const source of [cardView, listView]) {
    const pickerIdx = source.indexOf('v-if="folderPickerItemId === item.id"')
    const normalIdx = source.indexOf('v-else-if="moveMenuMode === \'normal\'"')
    assert.ok(pickerIdx >= 0)
    assert.ok(normalIdx >= 0)
    assert.ok(pickerIdx < normalIdx)
  }
})

test('folder navigation cancels stale card hover popovers', () => {
  assert.match(cardView, /if \(!cardElement\.isConnected/)
  assert.match(cardView, /watch\(\(\) => props\.items, dismissCardPopover\)/)
  assert.match(cardView, /const onOpenFolder = \(path: string\)[\s\S]*?dismissCardPopover\(\)[\s\S]*?emit\('open-folder', path\)/)
  assert.match(cardView, /@click\.stop="onOpenFolder\(item\.folder_path\)"/)
})
