/* Browser coverage; all HTTP requests are fulfilled by synthetic fixtures. */
const {assert, render, launch, open, prefix} = require('./chart-ui-fixture.cjs');
const html = render('charts.html');
const asOf = '2029-09-15', unsafe = '<img src=x onerror="window.__xss=1">';
const items = Array.from({length: 12}, (_, index) => ({id: 'chart-' + (index + 1), title: index ? '图表' + (index + 1) : unsafe,
  metric: index ? '指标' : unsafe, date: '2029-09-14', value: 35, rank: 1, n: 6, unit: '吨', transform: 'raw',
  previous_rank: index ? 3 : null, previous_n: index ? 6 : 2, previous_date: '2029-09-07',
  reasons: ['extreme_high', 'rank_change'], warnings: []}));
(async () => {
  const browser = await launch();
  try {
    const library = await open(browser, '/sn/charts', html, ({path, url, reply}) => {
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
    const empty = await open(browser, '/sn/charts', html, ({path, reply}) => {
      if (path === '/charts/anomalies') return reply({as_of: asOf, items: [], total: 0, failed: []});
      if (path === '/charts') return reply({items: [], total: 0, next_offset: null});
      return false;
    });
    assert.equal(await empty.page.locator('#chart-anomalies-status').innerText(), '本期没有同期前两高、前两低或排名明显变化的图表');
    assert.equal(await empty.page.locator('#chart-anomalies-more').isVisible(), false);
    assert.equal(await empty.page.locator('#chart-anomalies-table').isVisible(), false);
    await empty.close();
    console.log('chart-anomalies-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
