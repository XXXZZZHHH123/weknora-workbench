const assert = require('node:assert/strict');
const path = require('node:path');
const {spawnSync} = require('node:child_process');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');

const web = path.join(__dirname, '../src/tin/web');
const rendered = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', `
import sys
from jinja2 import Environment, FileSystemLoader, select_autoescape
env = Environment(loader=FileSystemLoader(sys.argv[1]), autoescape=select_autoescape(['html']))
print(env.get_template('_judgment_manager.html').render(logic_input='', selected_judgment_id=None, j=None))
`, path.join(web, 'templates')], {encoding: 'utf8'});
assert.equal(rendered.status, 0, rendered.stderr);
const html = `<!doctype html><html lang="zh-CN"><meta charset="utf-8">
<link rel="stylesheet" href="/static/judgment-manager.css">
<button data-open-logic>管理研究逻辑</button>${rendered.stdout}
<script>window.researchFetch = (url, options) => fetch(url, options)</script>
<script src="/static/analysis-jobs.js"></script><script src="/static/judgment-manager.js"></script></html>`;

const original = {series_id: 'source-original', name: '原绑定', unit: 'u', frequency: '日'};
const candidate = {series_id: 'source-candidate', name: '合成指标', unit: 'v', frequency: '日'};
const draftId = 'saved-item-edit-draft';
function preview(changed = false) {
  const items = [
    {id: 'limit', kind: 'thresholds', title: '合成阈值', description: '按照原文核对数值。', can_bind: true,
      status: 'confirmed', binding: changed ? candidate : original,
      caliber: changed ? {catalog_unit: 'v'} : {catalog_unit: 'u', catalog_frequency: '日',
        basis: '旧指标的核对依据', scope_note: '旧指标的适用范围', confirmed: true},
      rule: {operator: '>', value_literal: changed ? '1200' : '120', source_value: '12',
        quantity_review_available: true, conversion_text: changed ? '12 ×100 = 1200' : '12 ×10 = 120'}},
    {id: 'evidence', kind: 'binding', title: '未绑定依据', status: 'pending', can_bind: true, binding: null},
    {id: 'note', kind: 'description', title: '补充说明', status: 'kept_text', can_bind: false},
  ];
  return {draft_id: draftId, base_judgment_id: 42, base_version: 4, markdown: '# 已保存研判', source_text: '合成阈值：高于12。',
    diff: {fields: [], markdown: ''}, bindings: [changed ? candidate : original], item_edit: true,
    review: {item_edit: true, has_changes: changed, revision: changed ? 2 : 1, auto_indicator_recommendations: true,
      total: items.length, sections: [{id: 'test', title: '逐项核对', items}]}};
}

(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox', '--disable-gpu'],
    ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {})});
  let releaseEdit;
  try {
    const page = await browser.newPage({viewport: {width: 1280, height: 900}});
    const errors = [], posts = [];
    let canWrite = false, hasCurrent = true, editAvailable = true;
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()), pathname = url.pathname;
      let data;
      if (pathname === '/') return route.fulfill({contentType: 'text/html; charset=utf-8', body: html});
      if (pathname.startsWith('/static/')) return route.fulfill({path: path.join(web, 'static', pathname.slice(8))});
      if (request.method() === 'POST') posts.push([pathname, request.postDataJSON()]);
      if (pathname.endsWith('/manage')) {
        data = {current: hasCurrent ?
          {id: 42, version: 4, status: '生效', markdown: '# 已保存研判', item_edit_available: editAvailable} : null,
          variety: 'ZZ', latest: hasCurrent ? {version: 4} : null, can_write: canWrite,
          versions: hasCurrent ? [
            {id: 42, version: 4, status: '生效', item_edit_available: editAvailable},
            {id: 41, version: 3, status: '草稿', item_edit_available: false},
          ] : []};
      } else if (pathname.endsWith('/models')) data = {models: []};
      else if (pathname.endsWith('/panels')) {
        const items = hasCurrent ? [{id: 'saved-panel', judgment_id: 42, version: 4, variety: 'ZZ',
          title: '当前面板', status: '生效'}] : [];
        data = {items, total: items.length, has_more: false};
      } else if (pathname.endsWith('/versions/3')) {
        assert.equal(url.searchParams.get('judgment_id'), '42');
        data = {id: 41, version: 3, status: '草稿', markdown: '# 历史研判', diff: {markdown: '- 旧阈值\n+ 当前阈值'}};
      } else if (pathname.endsWith('/versions/4')) data = {id: 42, version: 4, status: '生效', markdown: '# 已保存研判'};
      else if (pathname.endsWith('/latest')) data = {job: null};
      else if (pathname.endsWith('/review/edit')) {
        await new Promise(resolve => { releaseEdit = resolve; });
        data = preview();
      } else if (pathname.endsWith('/review/indicators')) {
        data = {items: [candidate], total: 1, next_offset: null,
          ...(url.searchParams.get('item_id') === 'limit' ? {recommendations: [candidate]} : {})};
      } else if (pathname.endsWith('/review/quantity')) {
        data = {latest: {value: '1210', date: '2023-09-22'}, range: {min: '900', max: '1250'},
          range_start: '2020-09-22', range_end: '2023-09-22',
          warning: '数值超出观测范围。', candidates: [{exponent: 2}],
          ...(url.searchParams.has('exponent') ? {converted_text: '1200', converted_overlaps: true} : {})};
      } else if (pathname.endsWith('/review/update')) data = preview(true);
      else if (pathname.endsWith('/apply')) data = {id: 44, version: 5, status: '草稿'};
      else if (pathname === '/favicon.ico') return route.fulfill({status: 204});
      else {
        errors.push('未预期的请求 ' + pathname);
        return route.fulfill({status: 500, json: {detail: '未预期的请求'}});
      }
      return route.fulfill({json: data});
    });
    const openManager = async () => {
      await page.goto('http://judgment-edit.fixture/');
      await page.locator('[data-open-logic]').click();
      await page.locator('#logic-current-state').filter({hasText: hasCurrent ? '生效' : '尚无研究逻辑'}).waitFor();
      await page.locator('#logic-status').filter({hasText: /^$/}).waitFor({state: 'attached'});
    };
    const edit = page.getByRole('button', {name: '逐条修改', exact: true});
    await openManager();
    assert.equal(await edit.isVisible(), false);
    canWrite = true; hasCurrent = false;
    await openManager();
    assert.equal(await edit.isVisible(), false);

    hasCurrent = true; editAvailable = false;
    await openManager();
    assert.equal(await edit.isVisible(), false);

    editAvailable = true;
    await openManager();
    await edit.waitFor();
    const toolbar = page.locator('#logic-reuse').locator('..');
    assert.deepEqual(await toolbar.getByRole('button').allTextContents(),
      ['修改当前研判', '逐条修改', '复用原文', '导出 MD', '删除当前研判']);
    assert.equal(await page.locator('#logic-panel-switch option').count(), 1);
    await page.getByRole('button', {name: '历史研判', exact: true}).click();
    await page.locator('#logic-version').selectOption('record:41');
    await page.locator('#logic-history-md').filter({hasText: '# 历史研判'}).waitFor();
    await page.getByText('与当前展示版本的 MD 差异', {exact: true}).click();
    await page.locator('#logic-history-diff').filter({hasText: '+ 当前阈值'}).waitFor();
    await page.getByRole('button', {name: '当前研判', exact: true}).click();
    await edit.click();
    const status = page.locator('#logic-status');
    assert.equal(await status.getAttribute('data-busy'), 'false');
    await status.filter({hasText: '正在读取已保存研判'}).waitFor();
    assert.equal(await status.getAttribute('data-busy'), 'true');
    releaseEdit();

    const review = page.getByRole('region', {name: '逐条修改'});
    await review.waitFor();
    await page.getByRole('button', {name: '当前研判', exact: true}).click();
    await edit.click();
    await review.waitFor();
    assert.equal(posts.length, 1);
    const save = page.getByRole('button', {name: '保存为新版本'});
    await page.getByText('没有修改', {exact: true}).waitFor();
    assert.equal(await save.isDisabled(), true);
    assert.equal(await review.locator('.logic-review-card[data-review-item="limit"]').getByText('已确认').isVisible(), true);
    assert.equal(await review.getByText('待确认', {exact: true}).isVisible(), true);
    assert.equal(await review.getByText('已保留为文字', {exact: true}).isVisible(), true);
    const limit = review.locator('.logic-review-card[data-review-item="limit"]');
    await limit.getByRole('button', {name: '推荐指标'}).click();
    await limit.getByRole('group', {name: '目录推荐'}).getByRole('option', {name: /合成指标/}).click();
    const confirm = page.locator('#logic-confirm-item-dialog');
    await confirm.getByText('当前版本：指标值高于 120 u', {exact: false}).waitFor();
    await confirm.getByText('采用指标单位：v').waitFor();
    await confirm.getByText('原文数字写法：12').waitFor();
    await confirm.locator('small').filter({hasText: '参考区间：2020-09-22 至 2023-09-22'}).waitFor();
    assert.equal(await confirm.locator('[data-review-control="confirm-basis"]').inputValue(), '用户在本条确认窗口核对口径');
    const mode = confirm.locator('[data-review-control="confirm-quantity-mode"]');
    await mode.selectOption('convert');
    const factor = confirm.locator('[data-review-control="confirm-quantity-exponent"]');
    assert.deepEqual(await factor.locator('option').allTextContents(), ['请选择倍率', '×100']);
    await factor.selectOption('2');
    await confirm.getByText('1200 · 与近 3 年数据范围有交集').waitFor();
    await confirm.getByRole('button', {name: '确认整条'}).click();
    await confirm.waitFor({state: 'hidden'});
    assert.equal(await save.isEnabled(), true);
    await save.click();
    await page.waitForURL('**/?judgment_id=44');
    assert.deepEqual(posts.map(([route]) => route), [
      '/api/sn/judgment/review/edit', '/api/sn/judgment/review/update', '/api/sn/judgment/apply',
    ]);
    assert.deepEqual(posts[0][1], {judgment_id: 42, expected_base_version: 4});
    assert.equal(posts[1][1].series_id, candidate.series_id);
    assert.deepEqual(posts[1][1].quantity_alignment, {mode: 'convert', exponent: 2});
    assert.deepEqual(posts[1][1].caliber, {mode: 'catalog', basis: '用户在本条确认窗口核对口径', scope_note: ''});
    assert.deepEqual(posts[2][1], {draft_id: draftId, base_version: 4, review_revision: 2});
    assert.deepEqual(errors, []);
  } finally {
    releaseEdit?.();
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
