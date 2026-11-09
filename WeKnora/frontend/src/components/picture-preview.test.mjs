import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

// The viewer is opened programmatically from message thumbnails; its own default trigger would
// render an empty image box ("图片无法显示 / 预览") inside every message that includes it.
test('the shared picture preview never renders TDesign\'s default thumbnail trigger', () => {
  const source = readFileSync(new URL('./picture-preview.vue', import.meta.url), 'utf8')
  const viewer = source.slice(source.indexOf('<t-image-viewer'), source.indexOf('</t-image-viewer>'))
  assert.match(viewer, /<template #trigger><\/template>/)
})
