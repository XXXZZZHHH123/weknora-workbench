const {assert, render, launch, open, record, definition} = require('./chart-ui-fixture.cjs');
const base = {knowledge_id: 'knowledge-fixture', source_file: '合成原表.xlsx', sheet: '合成工作表'};
const charts = [{...base, chart_key: 'native-1', title: '<img src=x onerror=window.__xss=1>', convertible: true, mode: 'cycle', definition,
  warnings: [{message: '原图未包含 29/30，转换后会显示'}], missing_curves: [{label: '<缺失曲线>', reason: '周期图一期只保留一个指标'}]},
  {...base, chart_key: 'native-2', title: '无法转换的图', convertible: false, mode: null, reason: '没有可用曲线', warnings: [], missing_curves: [{label: '未知源', reason: 'unconfirmed_sheet'}]},
  {...base, chart_key: 'native-3', title: '走势', convertible: true, mode: 'trend', definition: {...definition, time: {mode: 'since', start: '2020-01-01'}}, warnings: [], missing_curves: [], already_imported: true}];
const html = render('chart_import.html');
(async () => {
  const browser = await launch();
  try {
    let applied;
    const ui = await open(browser, '/sn/charts/import', html, ({path, body, reply}) => {
      if (path.endsWith('/preview')) {
        assert.equal(body.limit, 200);
        return reply({total: 3, offset: body.offset, next_offset: body.offset === 0 ? 2 : null,
          sheets: [{...base, charts: body.offset === 0 ? charts.slice(0, 2) : charts.slice(2)}]});
      }
      if (path.endsWith('/apply')) { applied = body; return reply({created: [record('created-1', {definition: {...definition, origin: {kind: 'native', ref: 'native-1'}}})], skipped: [{chart_key: 'native-3'}], unconvertible: [{chart_key: 'native-2', reason: '没有可用曲线'}]}); }
      return false;
    });
    const p = ui.page;
    assert.match(await p.locator('#chart-import-sheets').innerText(), /原图未包含 29\/30，转换后会显示/);
    assert.match(await p.locator('#chart-import-sheets').innerText(), /缺失曲线[\s\S]*周期图一期只保留一个指标/);
    assert.equal(await p.locator('#chart-import-sheets img').count(), 0); assert.equal(await p.evaluate(() => window.__xss), undefined);
    await p.locator('[data-sheet-select]').check(); assert.equal(await p.locator('#chart-import-apply').isDisabled(), true, '分页未读完不能导入未预览图表');
    await p.locator('#chart-import-more').click(); await p.locator('#chart-import-progress').filter({hasText: '预览已全部加载'}).waitFor();
    assert.equal(await p.locator('.charts-sheet').count(), 1, '同一工作表跨页合并');
    assert.match(await p.locator('[data-sheet-summary]').innerText(), /本表 3 张：1 张周期图（起点 09-01 1 张），1 张走势图，1 张无法转换/);
    const unavailable = p.locator('.charts-import-unavailable');
    assert.equal(await unavailable.getAttribute('open'), null);
    assert.equal(await unavailable.locator('summary').innerText(), '1 张无法转换，展开查看原因');
    assert.equal(await p.locator('[data-import-chart="native-2"]').isVisible(), false);
    assert.deepEqual(await p.locator('[data-sheet-charts] > article').evaluateAll(nodes => nodes.map(n => n.dataset.importChart)), ['native-1', 'native-3']);
    await unavailable.locator('summary').click();
    assert.match(await p.locator('[data-import-chart="native-2"]').innerText(), /没有可用曲线[\s\S]*工作表尚未确认/);
    await p.locator('[data-sheet-start]').fill('02-30'); await p.locator('#chart-import-apply').click(); assert.equal(applied, undefined);
    await p.locator('[data-sheet-start]').fill('09-01'); await p.locator('#chart-import-apply').click();
    await p.locator('#chart-import-result').filter({hasText: '新建 1 张，跳过 1 张，无法转换 1 张'}).waitFor();
    assert.deepEqual(applied, {sheets: [{knowledge_id: base.knowledge_id, sheet: base.sheet, chart_keys: ['native-1', 'native-2', 'native-3'], start: '09-01'}]});
    assert.equal(await p.locator('#chart-import-apply').isDisabled(), true); await ui.close();
    const readonly = await open(browser, '/sn/charts/import', render('chart_import.html', {can_write: false, write_reason: '当前账号没有研究工作台编辑权限'}), ({reply}) => reply({total: 1, next_offset: null, sheets: [{...base, charts: charts.slice(0, 1)}]}));
    assert.equal(await readonly.page.locator('[data-sheet-select]').isDisabled(), true); assert.equal(await readonly.page.locator('#chart-import-apply').isDisabled(), true); await readonly.close();
    const blocked = await open(browser, '/sn/charts/import', html, ({reply}) => reply({detail: '来源授权已变化，请重新打开研究台'}, 409));
    assert.match(await blocked.page.locator('#chart-import-status').innerText(), /来源授权已变化/); assert.equal(await blocked.page.locator('#chart-import-apply').isDisabled(), true); await blocked.close();
    console.log('chart-import-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
