/* Real dialog and Chromium; every request is fulfilled with isolated fixtures. */
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const web = path.join(__dirname, '../src/tin/web');
const rendered = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', `
import sys
from jinja2 import Environment, FileSystemLoader, select_autoescape
env = Environment(loader=FileSystemLoader(sys.argv[1]), autoescape=select_autoescape(['html']))
print(env.get_template('_judgment_manager.html').render(logic_input='', selected_judgment_id=42, j=None))
`, path.join(web, 'templates')], {encoding: 'utf8'});
assert.equal(rendered.status, 0, rendered.stderr);
const html = '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>:root{--surface:#fff;--text:#222;--line:#ddd;--primary:#a63f25;--text-muted:#777;--alert:#b60019}body{font-family:Arial,sans-serif}</style><link rel="stylesheet" href="/static/judgment-manager.css"></head><body><button data-open-logic>管理研究逻辑</button>' + rendered.stdout + '<script>window.RESEARCH_PREFIX="/research/workbenches/synthetic";window.RESEARCH_CONTEXT={workbench:"synthetic"};window.researchFetch=(url,options)=>fetch(url,options)</script><script src="/static/judgment-manager.js"></script></body></html>';
const dimensions = [{id: 'custom_cost', label: '成本约束', category: '其他', description: '合成研究台的自定义维度'}, {id: 'demand', label: '终端消费', category: '需求', description: ''}];
const dimensionItems = [
  {section: 'supporting', index: 0, title: '支持依据：合成文字', dimension: 'removed_id'},
  {section: 'opposing', index: 0, title: '反对依据：合成文字'},
  {section: 'thresholds', index: 0, title: '阈值：合成条件', dimension: 'demand'},
  {section: 'falsifiers', index: 0, title: '证伪：合成条件'}];
const clone = value => JSON.parse(JSON.stringify(value));
let viewer = false, modelsAvailable = true, failModel = false, failReview = false, revision = 1, holdModel = true, releaseModel;
let review = {draft_id: 'synthetic-dimension-preview', revision: 1, total: 2, research_dimensions: dimensions, sections: [
  {id: 'contradiction', title: '主矛盾', items: [{id: 'evidence', kind: 'binding', title: '合成支持依据', description: '保持原始论证文字', status: 'ready', can_bind: false, can_dimension: true, dimension: 'demand', dimension_suggestion: {dimension: 'custom_cost', reason: ''}, warnings: []}]},
  {id: 'thresholds', title: '阈值条件', items: [{id: 'threshold', kind: 'thresholds', title: '合成阈值', description: '保持原始规则文字', status: 'ready', can_bind: false, can_dimension: true, rule: {operator: '>', value_literal: '12', type: '观察位', horizon: '短期'}, warnings: []}]}]};
const preview = () => ({draft_id: review.draft_id, base_version: 3, source_text: '合成研究原文', variety: 'ZZ', model_id: 'synthetic-model', markdown: '# 合成研究', diff: {markdown: '', fields: []}, bindings: [], unresolved: [], research_dimensions: dimensions, review_available: true, review: clone(review)});
(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox', '--disable-gpu', '--renderer-process-limit=1'], ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {})});
  const requests = [], errors = [];
  try {
    const page = await browser.newPage({viewport: {width: 1280, height: 1000}});
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()), p = url.pathname;
      requests.push({path: p, method: request.method(), body: request.postDataJSON?.()});
      let data;
      if (p === '/') return route.fulfill({contentType: 'text/html; charset=utf-8', body: html});
      if (p.startsWith('/static/')) return route.fulfill({path: path.join(web, 'static', p.slice(8))});
      if (p.endsWith('/manage')) data = {variety: 'ZZ', current: {id: 42, version: 2, status: '草稿', markdown: '# 原始研判', source_text: '原始分析', dimension_items: clone(dimensionItems)}, latest: {version: 3}, versions: [], can_write: !viewer, research_dimensions: dimensions};
      else if (p.endsWith('/models')) data = {models: modelsAvailable ? [{id: 'synthetic-model', name: '合成模型'}, {id: 'synthetic-model-2', name: '合成补标模型二'}] : []};
      else if (p.endsWith('/panels')) data = {items: [], total: 0, has_more: false};
      else if (p.endsWith('/jobs/latest')) data = {job: null};
      else if (p.endsWith('/dimension-suggestions')) {
        if (failModel) return route.fulfill({status: 502, json: {detail: '合成模型调用失败'}});
        if (holdModel) await new Promise(resolve => { releaseModel = resolve; });
        data = {research_dimensions: dimensions, suggestions: dimensionItems.map(item => ({section: item.section, index: item.index, dimension: 'custom_cost', reason: '合成模型依据原文提出的建议'}))};
      } else if (p.endsWith('/dimensions')) data = {id: 44, version: 4};
      else if (p.endsWith('/jobs') && request.method() === 'POST') data = {id: 'synthetic-dimension-job-001', state: 'queued', purpose: 'judgments', workbench_id: 'synthetic'};
      else if (p.endsWith('/jobs/synthetic-dimension-job-001')) data = {id: 'synthetic-dimension-job-001', state: 'succeeded', purpose: 'judgments', workbench_id: 'synthetic', result_available: true};
      else if (p.endsWith('/jobs/synthetic-dimension-job-001/result')) data = preview();
      else if (p.endsWith('/review/update')) {
        const body = request.postDataJSON(), item = review.sections.flatMap(section => section.items).find(item => item.id === body.item_id);
        assert.equal(body.action, 'dimension');
        assert.deepEqual(Object.keys(body).sort(), ['action', 'dimension', 'draft_id', 'item_id', 'revision']);
        assert.equal(Object.hasOwn(body, 'series_id'), false);
        assert.equal(Object.hasOwn(body, 'source_quote'), false);
        if (failReview) return route.fulfill({status: 422, json: {detail: '合成维度暂存失败'}});
        item.dimension = body.dimension; review.revision = ++revision; data = preview();
      } else throw new Error('Unexpected synthetic request ' + request.method() + ' ' + p);
      return route.fulfill({json: data});
    });
    await page.goto('http://dimensions.fixture/'); await page.locator('[data-open-logic]').click();
    const editor = page.locator('#logic-dimension-editor'); await editor.waitFor({state: 'visible'});
    const first = editor.locator('.logic-dimension-select').first();
    assert.equal(await first.inputValue(), 'removed_id');
    assert.match(await first.locator('option:checked').innerText(), /已删除维度/);
    assert.deepEqual(await first.locator('option').allTextContents(), ['未标注', '成本约束', '终端消费', '已删除维度（请重新指派）']);
    const modelSelect = page.getByRole('combobox', {name: '为内容推荐研究维度所用的模型'}); await modelSelect.waitFor({state: 'visible'});
    await modelSelect.selectOption('synthetic-model-2');
    assert.equal(await page.locator('#logic-model').inputValue(), 'synthetic-model-2', 'The visible tagging model and analysis model share the same selection');
    assert.equal(requests.filter(row => row.path.endsWith('/models')).length, 1, 'Both model selectors reuse one model request');
    await page.locator('#logic-dimension-suggest').click();
    assert.equal(await first.isDisabled(), false, 'The researcher can edit while the model is working');
    await first.selectOption('demand'); holdModel = false; releaseModel();
    await editor.locator('[data-dimension-adopt]').first().waitFor({state: 'visible'});
    assert.equal(requests.filter(row => row.path.endsWith('/dimension-suggestions')).at(-1).body.model_id, 'synthetic-model-2');
    assert.equal(await first.inputValue(), 'demand', 'Completed suggestions preserve edits made during the model request');
    assert.equal(requests.filter(row => row.path.endsWith('/dimensions')).length, 0);
    await editor.locator('[data-dimension-adopt]').first().click();
    assert.equal(await first.inputValue(), 'custom_cost');
    await editor.locator('.logic-dimension-select').nth(1).selectOption('demand');
    await editor.locator('.logic-dimension-select').nth(2).selectOption('');
    await page.setViewportSize({width: 390, height: 900});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    assert.equal(await editor.evaluate(node => node.getBoundingClientRect().right <= innerWidth), true);
    failModel = true; await page.locator('#logic-dimension-suggest').click();
    await page.locator('#logic-status').filter({hasText: '合成模型调用失败'}).waitFor({state: 'visible'});
    assert.equal(await first.inputValue(), 'custom_cost', 'A model error retains the researcher edits');
    failModel = false; await page.locator('#logic-dimension-save').click();
    await page.waitForFunction(() => location.search.includes('judgment_id=44'));
    const saved = requests.filter(row => row.path.endsWith('/dimensions')).at(-1).body;
    assert.equal(saved.judgment_id, 42); assert.equal(saved.base_version, 3);
    assert.deepEqual(saved.items.map(item => item.dimension), ['custom_cost', 'demand', null, null]);
    assert.deepEqual(Object.keys(saved).sort(), ['base_version', 'items', 'judgment_id']);
    await page.locator('[data-open-logic]').click();
    await page.locator('[data-logic-tab="analyze"]').click(); await page.locator('#logic-input').fill('合成研究原文');
    await page.locator('#logic-model').selectOption('synthetic-model'); await page.locator('#logic-generate').click();
    assert.equal(await page.locator('#logic-dimension-model').inputValue(), 'synthetic-model', 'Model changes in the analysis tab also update the tagging selector');
    const card = page.locator('article[data-review-item="evidence"]'); await card.waitFor({state: 'visible'});
    assert.equal(await card.locator('[data-review-control="dimension"]').inputValue(), 'demand');
    assert.match(await card.locator('.logic-dimension-suggestion').innerText(), /模型建议：成本约束/);
    await card.locator('[data-review-control="dimension-adopt"]').click();
    await page.waitForFunction(() => document.querySelector('article[data-review-item="evidence"] [data-review-control="dimension"]')?.value === 'custom_cost' && !document.querySelector('#logic-apply').disabled);
    assert.equal(await card.getAttribute('data-review-status'), 'ready', 'Changing a tag retains the existing review state');
    failReview = true;
    await card.locator('[data-review-control="dimension"]').selectOption('demand');
    await page.locator('#logic-status').filter({hasText: '合成维度暂存失败'}).waitFor({state: 'visible'});
    assert.equal(await page.locator('#logic-apply').isDisabled(), true);
    failReview = false;
    await card.locator('[data-review-control="dimension"]').selectOption('custom_cost');
    await page.waitForFunction(() => document.querySelector('article[data-review-item="evidence"] [data-review-control="dimension"]')?.value === 'custom_cost' && !document.querySelector('#logic-apply').disabled);
    assert.equal(await card.getAttribute('data-review-status'), 'ready', 'Retrying a failed dimension edit still changes only the tag');
    await card.locator('[data-review-control="dimension"]').selectOption('demand');
    await page.waitForFunction(() => document.querySelector('article[data-review-item="evidence"] [data-review-control="dimension"]')?.value === 'demand' && !document.querySelector('#logic-apply').disabled);
    await page.locator('#logic-review-nav [data-review-section="thresholds"]').click();
    const threshold = page.locator('article[data-review-item="threshold"]');
    await threshold.locator('[data-review-control="dimension"]').selectOption('demand');
    await page.waitForFunction(() => document.querySelector('article[data-review-item="threshold"] [data-review-control="dimension"]')?.value === 'demand' && !document.querySelector('#logic-apply').disabled);
    assert.match(await threshold.locator('.logic-item-condition').innerText(), /12/);
    assert.equal(requests.filter(row => row.path.endsWith('/apply')).length, 0);
    modelsAvailable = false; await page.goto('http://dimensions.fixture/'); await page.locator('[data-open-logic]').click();
    await editor.waitFor({state: 'visible'}); await page.locator('#logic-dimension-model option').filter({hasText: '暂无可用对话模型'}).waitFor({state: 'attached'});
    const beforeNoModel = requests.filter(row => row.path.endsWith('/dimension-suggestions')).length;
    await page.locator('#logic-dimension-suggest').click();
    await page.locator('#logic-status').filter({hasText: '请选择推荐研究维度所用的模型'}).waitFor({state: 'visible'});
    assert.equal(requests.filter(row => row.path.endsWith('/dimension-suggestions')).length, beforeNoModel, 'An empty model selection never sends an inference request');
    viewer = true; await page.goto('http://dimensions.fixture/'); await page.locator('[data-open-logic]').click();
    await editor.waitFor({state: 'visible'});
    assert.equal(await page.locator('#logic-dimension-suggest').isDisabled(), true);
    assert.equal(await editor.locator('.logic-dimension-select').first().isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log('PASS real Chromium: custom/deleted dimensions, explicit suggestion adoption, manual reassignment, model error, version save, review dropdowns, read-only controls and 390px layout; no service or real writes.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
