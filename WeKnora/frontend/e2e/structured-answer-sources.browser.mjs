import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { createRequire } from 'node:module'

if (!process.env.PLAYWRIGHT_PACKAGE) throw new Error('Set PLAYWRIGHT_PACKAGE to the installed Playwright package path')
const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PACKAGE)
const launchOptions = { headless: true, args: ['--disable-dev-shm-usage', '--renderer-process-limit=1'] }
if (process.env.PLAYWRIGHT_EXECUTABLE_PATH) launchOptions.executablePath = process.env.PLAYWRIGHT_EXECUTABLE_PATH
const browser = await chromium.launch(launchOptions)
const fixturePdf = readFileSync(process.env.STRUCTURED_FIXTURE_PDF || resolve(dirname(fileURLToPath(import.meta.url)), '../../internal/infrastructure/docparser/anydoc/testdata/upstream/pdf/text.pdf'))
const fixturePdfBase64 = fixturePdf.toString('base64')
const fixturePdfHash = createHash('sha256').update(fixturePdf).digest('hex')
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 850 } })
  const pageErrors = []
  page.on('pageerror', error => pageErrors.push(error.message))
  page.on('console', message => { if (message.type() === 'error') console.error('[browser console]', message.text()) })
  page.on('pageerror', error => console.error('[browser pageerror]', error.message))
  await page.addInitScript(({ pdfBase64, pdfHash }) => { window.fixturePdfBase64 = pdfBase64; window.fixturePdfHash = pdfHash }, { pdfBase64: fixturePdfBase64, pdfHash: fixturePdfHash })
  await page.goto(process.env.STRUCTURED_FIXTURE_URL || 'http://127.0.0.1:18291/e2e/structured-answer-sources.fixture.html')
  await page.waitForFunction(() => window.fixtureReady === true)
  assert.equal(await page.locator('.source-row').count(), 2, 'one Excel row and one grouped PDF row')
  assert.equal(await page.locator('.source-name').filter({ hasText: 'fixture.pdf' }).count(), 1)
  assert.equal(await page.locator('.pdf-locations').getAttribute('open'), null)
  assert.equal(await page.getByText('candidate-only.pdf', { exact: true }).isVisible(), false)
  assert.equal(await page.locator('.analysis-evidence').innerText().then(text => text.includes('pdft_')), false)
  assert.equal(await page.locator('.source-pages').innerText(), '第 1 页', 'unread directory page 11 is not answer-page evidence')
  assert.equal(await page.locator('.analysis-evidence').evaluate(el => getComputedStyle(el).borderTopWidth), '0px')
  assert.equal(await page.locator('.source-row').first().evaluate(el => getComputedStyle(el).borderLeftWidth), '0px')
  if (process.env.STRUCTURED_FIXTURE_SCREENSHOT) await page.screenshot({ path: process.env.STRUCTURED_FIXTURE_SCREENSHOT.replace('.png', '-desktop.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true)
  if (process.env.STRUCTURED_FIXTURE_SCREENSHOT) await page.screenshot({ path: process.env.STRUCTURED_FIXTURE_SCREENSHOT.replace('.png', '-mobile.png'), fullPage: true })
  await page.getByText('查看分析取数与计算依据').click()
  await page.getByRole('button', { name: '查看原始表格单元格' }).click()
  await page.locator('[data-source-cell="B2"]').waitFor()
  assert.equal(await page.locator('[data-source-cell="B2"]').evaluate(el => el.classList.contains('highlighted')), true)
  assert.match(await page.locator('.original-sheet').innerText(), /已核验来源 hash/)
  await page.locator('.pdf-locations > summary').click()
  assert.equal(await page.locator('.pdf-table-location').count(), 1, 'only inspected table appears as an actual source location')
  await page.locator('.pdf-cell-locations > summary').click()
  assert.equal(await page.locator('.pdf-cell-list button').count(), 2, 'both read pages retain their source cells')
  await page.locator('.pdf-cell-list button').filter({ hasText: '第 2 行' }).click()
  try { await page.locator('.pdf-source-overlay').waitFor({ timeout: 10000 }) } catch (error) {
    console.error('[fixture error text]', await page.locator('.source-row').allInnerTexts())
    await page.screenshot({ path: '/tmp/structured-answer-sources-failed.png', fullPage: true })
    throw error
  }
  await page.getByText(/fixture\.pdf · 第 1 页原件/).waitFor()
  await page.locator('.pdf-source-page__canvas').first().waitFor()
  assert.ok(await page.locator('.pdf-source-mark').count() > 0, 'PDF source coordinate should be highlighted')
  await page.screenshot({ path: process.env.STRUCTURED_FIXTURE_SCREENSHOT || '/tmp/structured-answer-sources.png', fullPage: true })
  assert.equal(await page.evaluate(() => window.fixtureRequests.some(url => url.includes('pdft_other_table'))), false, 'opening source details does not read directory candidates')
  await page.getByRole('button', { name: '关闭', exact: true }).click()
  await page.locator('.catalog-sources > summary').click()
  assert.equal(await page.getByText('candidate-only.pdf', { exact: true }).isVisible(), true)
  assert.equal(await page.getByRole('button', { name: '仅检索到的其他表', exact: true }).isVisible(), true)
  await page.locator('.catalog-sources > summary').click()
  await page.getByRole('button', { name: '查看原件', exact: true }).click()
  await page.locator('.pdf-source-overlay').waitFor()
  // A workspace switch must discard already opened source material.
  await page.evaluate(() => { window.fixtureAuth.selectedTenantId = 8 })
  await page.locator('.pdf-source-overlay').waitFor({ state: 'detached' })
  assert.equal(await page.locator('.original-sheet').count(), 0)
  await page.evaluate(() => {
    window.fixtureAuth.selectedTenantId = 7
    const previousFetch = window.fetch
    window.fetch = async (input, init) => {
      const response = await previousFetch(input, init)
      if (!String(input).includes('/report')) return response
      const data = await response.json()
      data.manifest.sha256 = 'c'.repeat(64)
      return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
    }
  })
  await page.getByRole('button', { name: '查看原始表格单元格' }).click()
  await page.getByRole('alert').filter({ hasText: '原件版本 hash 与回答中的证据不一致' }).waitFor()
  assert.equal(await page.locator('.original-sheet').count(), 0, 'mismatched source must not render')
  assert.deepEqual(pageErrors, [])
  console.log('Grouped PDF source rows, preserved pagination/cell navigation, narrow layout, Excel scope cleanup and hash guards passed')
} finally {
  await browser.close()
}
