/* Actual templates/scripts, synthetic account and source responses; no running service. */
const {assert, render, renderLibrary, launch, open, prefix, record, cycle} = require('./chart-ui-fixture.cjs');
const sid = index => 'kb-' + index.toString(16).padStart(48, '0');
// The actual preview card template, rendered once for every synthetic indicator.
const previewHTML = new Map(render('_indicator_cards.html', {date: '2029-09-15'}, {cards: Array.from({length: 30}, (_, index) => ({
  series_id: sid(index + 1), name: '合成指标 ' + (index + 1), status: 'ok', provider: '隔离来源', source_level: '知识库',
  value: '27', unit: '吨', change: '+7', points: '0,30 50,20 100,5', count: 3, min: '10', max: '27', as_of: '2029-09-15',
  frequency: '周', registered: true, retired: false, stale: false}))}).split('<div class="indicator-favorite"').slice(1)
  .map(part => ['<div class="indicator-favorite"' + part, part.match(/data-favorite-card="([^"]+)"/)[1]]).map(([html, id]) => [id, html]));
const rows = [1, 2].map(index => ({series_id: sid(index), name: '合成指标 ' + index, category: '合成分组',
  unit: '吨', frequency: '周', source: '隔离来源', caliber: {}, registered: true, macro: false, fetch_mode: 'readonly'}));
const html = renderLibrary({}, {rows, groups: [{name: '合成分组', rows}], catalog_total: rows.length});
const initialItem = (id, kind = 'indicator', status = 'active') => ({kind, id, status,
  title: status === 'invalid' ? '<已撤回的项目>' : '合成指标 ' + id.slice(-2), added_at: '2029-09-01T00:00:00Z'});
const key = 'tin.indicators.favorites.v1:17:fixture-user:fixture';
function source({path, url, reply}) {
  if (path === '/charts') return reply({items: [record()], total: 1, next_offset: null});
  if (path === '/charts/anomalies') return reply({as_of: '2029-09-15', items: [], total: 0, failed: []});
  if (path === '/charts/chart-1') return reply(record());
  if (path === '/indicators/previews') return reply({cards: [], html: url.searchParams.getAll('series_id').map(id => previewHTML.get(id) || '').join('')});
  if (path.endsWith('/cycle')) return reply(cycle());
  if (path.endsWith('/data')) return reply({...cycle(), revision: 1});
  if (path === '/indicators/catalog') return reply({indicators: rows, total: rows.length, offset: 0, next_offset: null});
  return false;
}
(async () => {
  const browser = await launch();
  try {
    const migration = await open(browser, '/sn/indicators?date=2029-09-15', html, source, {storage: {[key]: [sid(2), sid(1)]}});
    const p = migration.page;
    // This account's own earlier browser favorites join the account without a click.
    await p.waitForFunction(() => document.querySelectorAll('[data-favorite-card]').length === 2);
    assert.equal(await p.locator('#indicator-legacy-sync').isVisible(), false);
    assert.equal(migration.calls.filter(call => call.method === 'PUT').length, 1);
    assert.deepEqual(migration.favorites.items.map(item => item.id), [sid(2), sid(1)]);
    assert.deepEqual(await p.evaluate(key => JSON.parse(localStorage.getItem(key)), key), [sid(2), sid(1)]);
    assert.match(await p.locator('#indicator-storage-note').innerText(), /已把本浏览器原有的 2 个自选同步到账号/);
    assert.equal(await p.locator('#indicator-tab-favorites').getAttribute('aria-selected'), 'true');
    assert.equal(migration.calls.some(call => call.path === '/charts'), false, '图表栏在打开时才加载');
    // Indicator cards keep the earlier latest-value and sparkline card, from one batched preview read.
    const first = p.locator('[data-favorite-card]').first();
    await first.locator('.macro-trend-card svg polyline').waitFor();
    assert.match(await first.innerText(), /合成指标 2[\s\S]*27[\s\S]*较前值 \+7/);
    assert.equal(await first.locator('[data-series-chart]').count(), 1, '点击小图仍可展开历史走势');
    assert.equal(await first.locator('.favorite-meta a', {hasText: '口径与来源'}).count(), 1);
    assert.equal(await first.locator('[data-favorite]').count(), 1, '每张卡只有一个自选星标');
    const previewCalls = migration.calls.filter(call => call.path === '/indicators/previews');
    assert.equal(previewCalls.length, 1);
    assert.deepEqual(previewCalls[0].url.searchParams.getAll('series_id'), [sid(2), sid(1)]);
    assert.equal(previewCalls[0].url.searchParams.get('date'), '2029-09-15');
    assert.equal(migration.calls.some(call => call.path.endsWith('/cycle')), false);
    await p.reload(); await p.waitForFunction(() => document.querySelectorAll('[data-favorite-card]').length === 2);
    assert.equal(migration.calls.filter(call => call.method === 'PUT').length, 1, '同一浏览器只并入一次');
    await p.locator('#indicator-tab-catalog').click();
    const star = p.locator(`tr[data-series-id="${sid(1)}"] [data-favorite]`);
    assert.equal(await star.getAttribute('aria-pressed'), 'true');
    await star.press('Space');
    await p.waitForFunction(() => document.querySelector('#indicator-favorite-count').textContent === '1');
    assert.equal(await star.getAttribute('aria-pressed'), 'false');
    await star.press('Enter');
    await p.waitForFunction(() => document.querySelector('#indicator-favorite-count').textContent === '2');
    await p.locator('#indicator-tab-catalog').focus(); await p.keyboard.press('ArrowRight');
    assert.equal(await p.locator('#indicator-tab-charts').getAttribute('aria-selected'), 'true');
    await p.locator('[data-chart-id="chart-1"] [data-favorite]').press('Enter');
    await p.waitForFunction(() => document.querySelector('[data-chart-id="chart-1"] [data-favorite]').getAttribute('aria-pressed') === 'true');
    assert.equal(new URL(p.url()).searchParams.get('tab'), 'charts');
    assert.equal(new URL(p.url()).searchParams.get('date'), '2029-09-15');
    await p.locator('#indicator-tab-favorites').click();
    const chart = p.locator('[data-favorite-card="chart:chart-1"]');
    await chart.locator('svg').waitFor();
    await chart.getByRole('button', {name: /^上移 /}).click();
    await p.waitForFunction(() => document.querySelectorAll('[data-favorite-card]')[1]?.dataset.favoriteCard === 'chart:chart-1');
    assert.equal(await p.evaluate(() => document.activeElement?.dataset.move), '-1');
    assert.deepEqual(migration.favorites.items.map(item => item.kind), ['indicator', 'chart', 'indicator']);
    await migration.close();

    const invalid = await open(browser, '/sn/indicators?tab=favorites', html, source,
      {favorites: [initialItem('gone', 'chart', 'invalid'), initialItem(sid(1))]});
    const invalidCard = invalid.page.locator('[data-favorite-card="chart:gone"]');
    assert.match(await invalidCard.innerText(), /已失效/);
    assert.equal(await invalidCard.locator('a').count(), 0);
    await invalidCard.getByRole('button', {name: /^移除已失效自选 /}).press('Enter');
    await invalid.page.waitForFunction(() => !document.querySelector('[data-favorite-card="chart:gone"]'));
    assert.deepEqual(invalid.favorites.items.map(item => item.id), [sid(1)]);
    assert.equal(await invalid.page.locator('main img').count(), 0);
    assert.equal(await invalid.page.locator('#indicator-legacy-sync').isVisible(), false);
    await invalid.close();

    // Records saved before favorites were per account may belong to someone else: offered, never written.
    const unscoped = await open(browser, '/sn/indicators', html, source, {storage: {'tin.indicators.favorites.v1': [sid(1)]}});
    await unscoped.page.locator('#indicator-legacy-sync-button').waitFor();
    assert.equal(await unscoped.page.locator('#indicator-legacy-sync-button').innerText(), '把本浏览器的 1 个自选同步到账号');
    assert.equal(unscoped.calls.filter(call => call.method === 'PUT').length, 0);
    await unscoped.close();

    let remote = {format: 'favorites.v1', revision: 1, items: [initialItem(sid(1))]};
    const conflict = await open(browser, '/sn/indicators?tab=favorites', html, source, {
      handleFavorites: ({method, reply}) => {
        if (method === 'PUT') { remote = {...remote, revision: 2, items: [initialItem(sid(2))]}; return reply({detail: '账号自选已更新，请重新加载后再修改'}, 409); }
        return reply(remote);
      },
    });
    await conflict.page.locator('[data-favorite-card] [data-favorite]').click();
    await conflict.page.locator('#indicator-storage-note').filter({hasText: '账号自选已更新'}).waitFor();
    assert.equal(conflict.calls.filter(call => call.method === 'PUT').length, 1);
    assert.equal(await conflict.page.locator(`[data-favorite-card="indicator:${sid(2)}"]`).count(), 1);
    await conflict.close();

    let active = 0, maximum = 0;
    const lazy = await open(browser, '/sn/indicators?tab=favorites', html, async event => {
      if (event.path.endsWith('/data')) {
        active++; maximum = Math.max(maximum, active);
        await new Promise(resolve => setTimeout(resolve, 50)); active--;
      }
      return source(event);
    }, {favorites: [...Array.from({length: 30}, (_, index) => initialItem(sid(index + 1))), initialItem('chart-1', 'chart')]});
    await lazy.page.waitForFunction(() => document.querySelectorAll('[data-favorite-card] .macro-trend-card').length === 30);
    assert.equal(lazy.calls.filter(call => call.path === '/indicators/previews').length, 1, '指标小图一次批量读取');
    assert.equal(lazy.calls.some(call => call.path.endsWith('/data')), false, '图表缩略图在进入视野时才读取');
    await lazy.page.locator('[data-favorite-card="chart:chart-1"]').scrollIntoViewIfNeeded();
    await lazy.page.locator('[data-favorite-card="chart:chart-1"] svg').waitFor();
    assert.ok(maximum <= 4, '图表缩略图最多4个并发任务');
    await lazy.close();

    for (const status of [403, 409]) {
      const blocked = await open(browser, '/sn/indicators?tab=favorites', html, source,
        {handleFavorites: ({reply}) => reply({detail: status === 403 ? '受限视角暂不开放自选' : '请重新打开研究台更新来源授权'}, status)});
      assert.equal(await blocked.page.locator('[data-indicator-row] [data-favorite]:enabled').count(), 0);
      assert.match(await blocked.page.locator('#indicator-storage-note').innerText(), /受限视角|重新打开研究台/);
      assert.equal(blocked.calls.filter(call => call.method === 'PUT').length, 0);
      await blocked.close();
    }
    console.log('favorites-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
