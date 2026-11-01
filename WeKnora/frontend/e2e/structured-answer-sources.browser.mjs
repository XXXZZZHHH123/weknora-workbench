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
const fixturePdf = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../internal/infrastructure/docparser/anydoc/testdata/upstream/pdf/text.pdf'))
const fixturePdfBase64 = fixturePdf.toString('base64')
const fixturePdfHash = createHash('sha256').update(fixturePdf).digest('hex')
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  page.on('console', message => { if (message.type() === 'error') console.error('[browser console]', message.text()) })
  page.on('pageerror', error => console.error('[browser pageerror]', error.message))
  await page.addInitScript(({ pdfBase64, pdfHash }) => { window.fixturePdfBase64 = pdfBase64; window.fixturePdfHash = pdfHash }, { pdfBase64: fixturePdfBase64, pdfHash: fixturePdfHash })
  await page.goto(process.env.STRUCTURED_FIXTURE_URL || 'http://127.0.0.1:18291/e2e/structured-answer-sources.fixture.html')
  await page.waitForFunction(() => window.fixtureReady === true)
  await page.getByText('查看分析取数与计算依据').click()
  await page.getByRole('button', { name: '查看原始表格单元格' }).click()
  await page.locator('[data-source-cell="B2"]').waitFor()
  assert.equal(await page.locator('[data-source-cell="B2"]').evaluate(el => el.classList.contains('highlighted')), true)
  assert.match(await page.locator('.original-sheet').innerText(), /已核验来源 hash/)
  await page.getByRole('button', { name: '查看 PDF 原页与表格单元格' }).click()
  try { await page.locator('.pdf-source-overlay').waitFor({ timeout: 10000 }) } catch (error) {
    console.error('[fixture error text]', await page.locator('.source-card').allInnerTexts())
    await page.screenshot({ path: '/tmp/structured-answer-sources-failed.png', fullPage: true })
    throw error
  }
  await page.getByText(/fixture\.pdf · 第 1 页原件/).waitFor()
  await page.locator('.pdf-source-page__canvas').first().waitFor()
  assert.ok(await page.locator('.pdf-source-mark').count() > 0, 'PDF source coordinate should be highlighted')
  await page.screenshot({ path: process.env.STRUCTURED_FIXTURE_SCREENSHOT || '/tmp/structured-answer-sources.png', fullPage: true })
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
  console.log('Excel/PDF scoped source rendering, workspace cleanup and hash mismatch checks passed')
} finally {
  await browser.close()
}
