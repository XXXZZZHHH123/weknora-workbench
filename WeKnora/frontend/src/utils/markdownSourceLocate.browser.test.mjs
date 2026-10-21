import assert from 'node:assert/strict'
import { before, after, test } from 'node:test'
import { createRequire } from 'node:module'

// Run against the isolated Vite preview with the project's browser test
// container. No account, API requests or real business documents are needed.
const url = process.env.FRONTEND_BROWSER_TEST_URL
let browser, page
before(async () => {
  if (!url) return
  const { chromium } = createRequire(import.meta.url)('playwright')
  browser = await chromium.launch({ args: ['--disable-dev-shm-usage'] })
  page = await browser.newPage()
  await page.route('**/api/**', route => route.fulfill({ status: 403, json: {} }))
  await page.goto(url)
})
after(async () => { await browser?.close() })

const header = '| Metric | Value |\n| --- | ---: |\n'
const earlier = '| Earlier row | 10 |\n'
const cited = '| Later unique measurement | 25 |\n'
const ending = '\nThe final observation was verified at the coastal station.'
const original = header + earlier + cited + ending
const chunk = header + cited + ending

async function locate(source, excerpt, sentence = '') {
  return page.evaluate(async ({ source, excerpt, sentence }) => {
    const { renderDocumentPreviewMarkdown } = await import('/src/utils/documentPreviewMarkdown.ts')
    const { findMarkdownSourceRange } = await import('/src/utils/markdownSourceLocate.ts')
    const root = document.createElement('div')
    root.innerHTML = renderDocumentPreviewMarkdown(source)
    document.body.appendChild(root)
    try {
      const match = findMarkdownSourceRange(root, excerpt, sentence)
      return match ? { text: match.range.toString(), exact: match.exact } : null
    } finally { root.remove() }
  }, { source, excerpt, sentence })
}

test('locates table continuation with a repeated header and keeps all cited body text', { skip: !url }, async () => {
  const match = await locate(original, chunk)
  assert.ok(match)
  assert.match(match.text, /Later unique measurement/)
  assert.match(match.text, /final observation/)
  assert.doesNotMatch(match.text, /Earlier row/)
})

test('requires the same original table header', { skip: !url }, async () => {
  assert.equal(await locate(original.replace('Metric', 'Another column'), chunk), null)
})

test('does not rescue a missing chunk body using a matching answer elsewhere', { skip: !url }, async () => {
  const source = original.replace('final observation', 'first observation') + '\n\nThe final observation was verified at the coastal station.'
  assert.equal(await locate(source, chunk, 'The final observation was verified at the coastal station.'), null)
})

test('rejects ambiguous repeated table bodies', { skip: !url }, async () => {
  assert.equal(await locate(original + '\n\n' + original, chunk), null)
})

test('preserves normal full-source matching and rejects changed numeric values', { skip: !url }, async () => {
  assert.ok(await locate(original, original))
  assert.equal(await locate(original, chunk.replace('25', '250')), null)
})
