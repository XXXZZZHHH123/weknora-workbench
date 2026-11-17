/* Browser coverage; all HTTP requests are fulfilled by synthetic fixtures. */
const {assert, render, renderLibrary, launch, open, prefix, record, cycle} = require('./chart-ui-fixture.cjs');
const html = renderLibrary();
const asOf = '2029-09-15', unsafe = '<img src=x onerror="window.__xss=1">';
const items = Array.from({length: 12}, (_, index) => ({id: 'chart-' + (index + 1), title: index ? '图表' + (index + 1) : unsafe,
  metric: index ? '指标' : unsafe, date: '2029-09-14', value: 35, rank: 1, n: 6, unit: '吨', transform: 'raw',
  previous_rank: index ? 3 : null, previous_n: index ? 6 : 2, previous_date: '2029-09-07',
  reasons: ['extreme_high', 'rank_change'], warnings: []}));
(async () => {
  const browser = await launch();
  try {
    const library = await open(browser, '/sn/indicators?tab=charts', html, ({path, url, reply}) => {
      if (path === '/charts/anomalies') {
        assert.equal(url.searchParams.get('date'), asOf);
        return reply({as_of: asOf, items, total: 12, failed: [{title: unsafe, reason: unsafe}]});
      }
      if (path === '/charts') return reply({items: [], total: 0, next_offset: null});
      return false;
    }, {documents: {[prefix + '/sn/charts/chart-1']: '<p>图表详情</p>'}});
    const p = library.page, panel = p.locator('#chart-anomalies'), rows = panel.locator('tbody tr');
    assert.equal(await panel.getAttribute('open'), ''); assert.equal(await rows.count(), 10);
    assert.equal(await rows.first().locator('a').innerText(), unsafe);
    assert.equal(await rows.first().locator('a').getAttribute('href'), prefix + '/sn/charts/chart-1?date=' + asOf);
    assert.match(await rows.first().innerText(), /暂无可用排名/);
    assert.match(await rows.nth(1).innerText(), /1\/7[\s\S]*3\/7[\s\S]*同期前两高[\s\S]*排名变化/);
    assert.equal(await panel.locator('img, script').count(), 0);
    assert.equal(await p.evaluate(() => window.__xss), undefined);
    await panel.locator(':scope > summary').click(); assert.equal(await rows.first().isVisible(), false);
    await panel.locator(':scope > summary').click(); assert.equal(await rows.first().isVisible(), true);
    await p.locator('#chart-anomalies-more').click(); assert.equal(await rows.count(), 12);
    assert.equal(await p.locator('#chart-anomalies-more').getAttribute('aria-expanded'), 'true');
    await p.locator('#chart-anomalies-more').click(); assert.equal(await rows.count(), 10);
    await p.locator('#chart-anomalies-failed-summary').click();
    assert.equal(await p.locator('#chart-anomalies-failed-list').innerText(), unsafe + '：' + unsafe);
    await rows.first().locator('a').click(); await p.waitForURL('**/sn/charts/chart-1?date=' + asOf);
    await library.close();
    const empty = await open(browser, '/sn/indicators?tab=charts', html, ({path, reply}) => {
      if (path === '/charts/anomalies') return reply({as_of: asOf, items: [], total: 0, failed: []});
      if (path === '/charts') return reply({items: [], total: 0, next_offset: null});
      return false;
    });
    assert.equal(await empty.page.locator('#chart-anomalies-status').innerText(), '本期没有同期前两高、前两低或排名明显变化的图表');
    assert.equal(await empty.page.locator('#chart-anomalies-more').isVisible(), false);
    assert.equal(await empty.page.locator('#chart-anomalies-table').isVisible(), false);
    await empty.close();

    // Hold the anomaly response until cards, pagination and search have all
    // completed. Exercise both the indicator tab and the library template.
    for (const [pathname, content] of [['/sn/indicators?tab=charts', html], ['/sn/charts', render('charts.html')]]) {
      let release;
      const pending = new Promise(resolve => { release = resolve; });
      const records = [record('chart-1'), record('chart-2')];
      records[1].definition.title = '第二张合成图';
      const independent = await open(browser, pathname, content, async ({path, url, reply}) => {
        if (path === '/charts/anomalies') {
          await pending;
          return reply({detail: '合成来源暂不可用'}, 503);
        }
        if (path === '/charts') {
          if (url.searchParams.get('q')) return reply({items: [records[1]], total: 1, next_offset: null});
          if (url.searchParams.get('limit') === '100') return reply({items: records, total: 2, next_offset: null});
          const offset = Number(url.searchParams.get('offset'));
          return reply({items: [records[offset]], total: 2, next_offset: offset ? null : 1});
        }
        if (path.endsWith('/data')) return reply(cycle());
        return false;
      }, {waitUntil: 'domcontentloaded'});
      const p = independent.page;
      try {
        await p.locator('.charts-card .charts-thumbnail svg').waitFor();
        assert.equal(await p.locator('.charts-card').count(), 1);
        assert.equal(await p.locator('#chart-anomalies-status').innerText(), '正在读取同期位置…');
        assert.equal(await p.locator('#chart-anomalies').getAttribute('aria-busy'), 'true');
        await p.locator('#charts-more').click();
        await p.waitForFunction(() => document.querySelectorAll('.charts-card').length === 2);
        await p.locator('#chart-search').fill('第二张');
        await p.waitForFunction(() => document.querySelectorAll('.charts-card').length === 1 && document.querySelector('.charts-card').dataset.chartId === 'chart-2');
        release();
        await p.locator('#chart-anomalies-status').filter({hasText: '同期异动读取失败：合成来源暂不可用'}).waitFor();
        assert.equal(await p.locator('#chart-anomalies').getAttribute('aria-busy'), 'false');
        assert.match(await p.locator('.charts-card').innerText(), /第二张合成图/);
        assert.equal(await p.locator('#charts-status').isVisible(), false);
      } finally { release(); await independent.close(); }
    }
    console.log('chart-anomalies-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
