import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { isTrustedStructuredFrameMessage, structuredFrameAuthPayload } from './structuredFrameHost'

test('structured iframe auth carries current tenant, knowledge base, and exact source type', () => {
  assert.deepEqual(structuredFrameAuthPayload({
    token: 'refreshed-token', tenant: 12, knowledgeBaseId: 'kb-current',
    sourceKnowledgeId: 'pdf-current', sourceFileType: 'pdf',
  }), {
    type: 'structured-auth', token: 'refreshed-token', tenant: 12,
    knowledgeBaseId: 'kb-current', sourceKnowledgeId: 'pdf-current', sourceFileType: 'pdf',
  })
})

test('structured iframe messages require both the same origin and the current iframe window', () => {
  const contentWindow = {} as Window
  const frame = { contentWindow } as HTMLIFrameElement
  assert.equal(isTrustedStructuredFrameMessage({ origin: 'https://app.test', source: contentWindow }, frame, 'https://app.test'), true)
  assert.equal(isTrustedStructuredFrameMessage({ origin: 'https://other.test', source: contentWindow }, frame, 'https://app.test'), false)
  assert.equal(isTrustedStructuredFrameMessage({ origin: 'https://app.test', source: {} as Window }, frame, 'https://app.test'), false)
  assert.equal(isTrustedStructuredFrameMessage({ origin: 'https://app.test', source: contentWindow }, undefined, 'https://app.test'), false)
})

test('PDF review entry is restricted to a PDF preview and mounts the shared authenticated host with the selected source', () => {
  const docContent = readFileSync(new URL('../components/doc-content.vue', import.meta.url), 'utf8')
  assert.match(docContent, /viewMode === 'preview' && canReviewPdfTables/)
  assert.match(docContent, /resolveFilePreviewExt\(props\.details\?\.title \|\| props\.details\?\.file_name \|\| '', props\.details\?\.file_type\) === 'pdf'/, 'use authoritative file_type when the visible title has no PDF extension')
  assert.ok(docContent.indexOf('const props = defineProps(') < docContent.indexOf('const pdfReviewKey = computed('), 'initialize props before registering watchers that synchronously read the PDF review key')
  assert.match(docContent, /核对 PDF 表格/)
  assert.match(docContent, /:key="pdfReviewKey"/)
  assert.match(docContent, /:knowledge-base-id="String\(kbId \|\| ''\)"/)
  assert.match(docContent, /:source-knowledge-id="String\(details\?\.id \|\| ''\)" source-file-type="pdf"/)
  assert.match(docContent, /@navigate="navigateFromPdfReview"/)
  assert.match(docContent, /@click="pdfReviewOpen = true"/)
  assert.match(docContent, /v-model:visible="pdfReviewOpen"/)
  assert.match(docContent, /\.pdf-review-dialog__frame\s*\{[^}]*display:\s*flex/, 'give the iframe host a flex height from the fixed-height dialog frame')
  assert.ok(docContent.includes('watch(pdfReviewKey, () => { pdfReviewOpen.value = false; });'))
  assert.ok(docContent.includes('if (!visible) pdfReviewOpen.value = false;'))
  assert.match(docContent, /预览通过不代表 OCR 或公式识别已经验证/)
})

test('the knowledge structured route and preview drawer share the same iframe auth and theme host', () => {
  const routePage = readFileSync(new URL('../views/workspaces/KnowledgeStructuredData.vue', import.meta.url), 'utf8')
  assert.match(routePage, /<KnowledgeStructuredFrame :knowledge-base-id="kbId"/)
  assert.match(routePage, /:source-knowledge-id="sourceKnowledgeId" :source-file-type="sourceFileType"/)
  assert.match(routePage, /:mode="mode" :view="String\(route\.query\.view \|\| ''\)"/)
  const host = readFileSync(new URL('../components/KnowledgeStructuredFrame.vue', import.meta.url), 'utf8')
  assert.ok(host.includes('if (!library.value || !auth.token || !tenant.value) return'))
  assert.ok(host.includes('if (own !== generation) return'))
  assert.ok(host.includes("onBeforeUnmount(() => { generation++; window.removeEventListener('message', receive) })"))
  assert.match(host, /refreshStructuredFrame\(event, \(\) => frame\.value\?\.contentWindow === event\.source\)/)
  assert.match(host, /isTrustedStructuredFrameMessage\(event, frame\.value, window\.location\.origin\)/)
  assert.ok(routePage.includes("route.query.source_file_type === 'pdf' ? 'pdf' : 'xlsx'"))
})
