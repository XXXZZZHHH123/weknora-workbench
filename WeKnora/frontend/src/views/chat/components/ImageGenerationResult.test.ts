import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, h, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import en from '../../../i18n/locales/en-US'

let server: ViteDevServer
let Renderer: Component
before(async () => {
  server = await createServer({
    configFile: false, optimizeDeps: { noDiscovery: true, entries: [] }, plugins: [
      { name: 'isolate-generated-image-renderer', enforce: 'pre', load(id) {
        if (id.endsWith('.vue') && !['ToolResultRenderer.vue', 'ImageGenerationResult.vue'].some(name => id.endsWith(`/${name}`))) {
          return '<template><div /></template>'
        }
      } }, vue(),
    ],
    resolve: { alias: { '@': fileURLToPath(new URL('../../../', import.meta.url)) } },
    server: { middlewareMode: true }, appType: 'custom',
  })
  Renderer = (await server.ssrLoadModule('/src/views/chat/components/ToolResultRenderer.vue')).default
})
after(async () => { await server?.close() })

async function render(data: Record<string, unknown>, success = true, output = '') {
  const app = createSSRApp({ render: () => h(Renderer, { displayType: 'image_generation', toolData: data, success, output }) })
  app.use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': en } }))
  app.component('t-dialog', { render: () => h('div') })
  return renderToString(app)
}
const saved = {
  status: 'completed', session_id: 'session-1', message_id: 'message-1', width: 1024, height: 768,
  artifacts: [{ index: 2, file_name: 'saved-<script>.png', file_type: '.png', file_size: 1200, handle: 'resource://real-handle' }],
}

test('saved generated images use a dedicated result card on live or historical replay', async () => {
  for (const data of [saved, JSON.parse(JSON.stringify(saved))]) {
    const html = await render(data)
    assert.match(html, /Image generated and saved/)
    assert.match(html, /1024 × 768/)
    assert.match(html, /saved-&lt;script&gt;.png/)
    assert.match(html, /Download image/)
    assert.doesNotMatch(html, /fallback-output|<script>/)
  }
})

test('failure and missing persisted metadata never present an image as delivered', async () => {
  const failed = await render(saved, false, 'Upstream generation failed')
  assert.match(failed, /role="alert"/)
  assert.match(failed, /Upstream generation failed/)
  assert.doesNotMatch(failed, /Image generated and saved|Download image/)
  const missing = await render({ status: 'completed', image_url: 'https://vendor.invalid/temporary.png' })
  assert.match(missing, /unavailable or has been deleted/)
  assert.doesNotMatch(missing, /Image generated and saved|vendor.invalid|Download image/)
})
