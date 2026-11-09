const {assert, render, launch, open, templates, record, cycle} = require('./chart-ui-fixture.cjs');
const detail = {series_id: 'kb-fixture-series', name: '合成指标 <安全>', unit: '吨', frequency: '周', fetch_mode: 'manual',
  history: [{as_of: '2029-09-08', value: 20}, {as_of: '2029-09-15', value: 27}], caliber: [], revisions: []};
const extra = {detail, usage: {judgment: [], nodes: [], crosscheck: []}};
const html = render('indicator.html', {}, extra);
(async () => {
  const browser = await launch();
  try {
    let saved, selected;
    const ui = await open(browser, '/sn/indicators/kb-fixture-series', html, ({path, method, body, url, reply}) => {
      if (path === '/cycle-templates') return reply(templates);
      if (path.endsWith('/cycle')) { selected = url.searchParams.get('template'); assert.equal(url.searchParams.get('date'), '2029-09-15'); return reply({...cycle(), template: templates.items.find(t => t.id === selected).template}); }
      if (path === '/charts' && method === 'GET') { assert.equal(url.searchParams.get('series_id'), detail.series_id); return reply({items: saved ? [record('saved-chart')] : [], total: saved ? 1 : 0, next_offset: null}); }
      if (path === '/charts' && method === 'POST') { saved = body; return reply(record('saved-chart'), 201); }
      return false;
    });
    const p = ui.page;
    // Trend first; the cycle is read only when 同期对比 is opened, in the same card.
    await p.locator('#ind-chart svg').waitFor();
    assert.equal(await p.locator('#ind-view-trend').getAttribute('aria-selected'), 'true');
    assert.equal(await p.locator('#indicator-cycle-panel').isVisible(), false);
    assert.equal(ui.calls.some(call => call.path.endsWith('/cycle') || call.path === '/cycle-templates'), false);
    await p.locator('#ind-view-cycle').click();
    await p.locator('#indicator-cycle-plot svg').waitFor();
    assert.equal(await p.locator('#ind-trend-view').isVisible(), false);
    assert.equal(await p.locator('#ind-view-title').innerText(), '同期对比');
    assert.equal(await p.locator('#ind-trend-view + #indicator-cycle-panel, #ind-trend-view ~ #indicator-cycle-panel').count(), 1, '同期对比与走势在同一张卡片');
    await p.locator('[data-favorite-kind="indicator"]').press('Enter');
    await p.waitForFunction(() => document.querySelector('[data-favorite-kind="indicator"]').getAttribute('aria-pressed') === 'true');
    assert.equal(ui.favorites.items[0].id, detail.series_id);
    assert.equal(await p.locator('#ind-chart svg').count(), 1, '原走势仍保留'); assert.equal(await p.locator('#ind-rows tr').count(), 2);
    // The observation table can be laid out by year, from the same cycle and template.
    await p.locator('#ind-table-year').click();
    assert.equal(await p.locator('#ind-time-table').isVisible(), false);
    const header = await p.locator('#ind-year-table thead th').allInnerTexts();
    assert.deepEqual(header, ['时点', '2029（当前）', '2028', '2027', '<历史段>']);
    const firstRow = await p.locator('#ind-year-table tbody tr').first().locator('td').allInnerTexts();
    assert.deepEqual(firstRow, ['第1周', '13', '12', '11', '10']);
    assert.deepEqual(await p.locator('#ind-year-table tbody tr').nth(3).locator('td').allInnerTexts(), ['第6周', '', '47', '46', '45'], '缺失观测留空，不补值');
    assert.match(await p.locator('#ind-year-status').innerText(), /空格表示该期没有观测/);
    const csv = await p.evaluate(() => window.IndicatorYearTable.csv());
    assert.equal(csv.rows, 5); assert.match(csv.text, /^时点,2029（当前）,2028,2027,<历史段>\n第1周,13,12,11,10/);
    await p.reload(); await p.locator('#indicator-cycle-plot svg').waitFor();
    assert.equal(await p.locator('#ind-view-cycle').getAttribute('aria-selected'), 'true', '本浏览器记住上次的视图');
    assert.equal(await p.locator('#ind-table-year').getAttribute('aria-selected'), 'true');
    await p.locator('#ind-table-time').click(); await p.locator('#ind-view-trend').click();
    assert.equal(await p.locator('#ind-chart svg').isVisible(), true);
    await p.locator('#ind-view-cycle').click();
    assert.equal(await p.locator('#indicator-cycle-template option').count(), 2);
    assert.match(await p.locator('#indicator-cycle-statistics').innerText(), /同期排名[\s\S]*1\/4/);
    assert.match(await p.locator('#indicator-cycle-statistics').innerText(), /较近 3 期均值/);
    assert.match(await p.locator('#indicator-cycle-statistics').innerText(), /上涨 1 段\/共 2 段/);
    await p.locator('#indicator-cycle-template').selectOption('saved-template');
    await p.waitForFunction(() => !document.querySelector('#indicator-cycle-save').disabled);
    await p.locator('#indicator-cycle-save').click(); await p.locator('#indicator-cycle-status').filter({hasText: '已保存为共享图表'}).waitFor();
    assert.equal(selected, 'saved-template'); assert.equal(saved.curves[0].series_id, detail.series_id); assert.equal(saved.time.mode, 'cycle'); assert.equal(saved.time.template.start, '09-01');
    await p.locator('#indicator-chart-usage').filter({hasText: '1 张图表引用'}).waitFor(); assert.equal(await p.locator('#indicator-related-charts a').count(), 1); await ui.close();
    const readonly = await open(browser, '/sn/indicators/kb-fixture-series', render('indicator.html', {can_write: false}, extra), ({path, reply}) => {
      if (path === '/cycle-templates') return reply(templates);
      if (path.endsWith('/cycle')) return reply({...cycle(), position: {...cycle().position, status: 'insufficient_cycles', n: 2}});
      return reply({items: [], total: 0, next_offset: null});
    });
    await readonly.page.locator('#ind-view-cycle').click();
    await readonly.page.locator('#indicator-cycle-statistics').filter({hasText: '有效往期不足'}).waitFor();
    assert.match(await readonly.page.locator('#indicator-cycle-status').innerText(), /往年数据不足 3 个周期/);
    assert.equal(await readonly.page.locator('#indicator-cycle-save').isDisabled(), true);
    assert.match(await readonly.page.locator('#indicator-cycle-write-reason').innerText(), /没有研究工作台编辑权限/);
    assert.match(await readonly.page.locator('#indicator-cycle-statistics').innerText(), /有效往期不足 3 期/); await readonly.close();
    const blocked = await open(browser, '/sn/indicators/kb-fixture-series', html, ({reply}) => reply({detail: '图表库需要来源版本2，请重新打开研究台更新来源授权'}, 409));
    await blocked.page.locator('#ind-view-cycle').click();
    await blocked.page.locator('#indicator-cycle-status').filter({hasText: '重新打开研究台'}).waitFor();
    assert.match(await blocked.page.locator('#indicator-cycle-status').innerText(), /重新打开研究台/); assert.equal(await blocked.page.locator('#indicator-cycle-save').isDisabled(), true); await blocked.close();
    console.log('indicator-cycle-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
