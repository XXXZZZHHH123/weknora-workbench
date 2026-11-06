/* Actual templates/scripts, synthetic account and source responses; no running service. */
const {assert, renderLibrary, launch, open, prefix, record, cycle} = require('./chart-ui-fixture.cjs');
const sid = index => 'kb-' + index.toString(16).padStart(48, '0');
const rows = [1, 2].map(index => ({series_id: sid(index), name: '合成指标 ' + index, category: '合成分组',
  unit: '吨', frequency: '周', source: '隔离来源', caliber: {}, registered: true, macro: false, fetch_mode: 'readonly'}));
const html = renderLibrary({}, {rows, groups: [{name: '合成分组', rows}], catalog_total: rows.length});
const initialItem = (id, kind = 'indicator', status = 'active') => ({kind, id, status,
  title: status === 'invalid' ? '<已撤回的项目>' : '合成指标 ' + id.slice(-2), added_at: '2029-09-01T00:00:00Z'});
const key = 'tin.indicators.favorites.v1:17:fixture-user:fixture';
function source({path, reply}) {
  if (path === '/charts') return reply({items: [record()], total: 1, next_offset: null});
  if (path === '/charts/anomalies') return reply({as_of: '2029-09-15', items: [], total: 0, failed: []});
  if (path === '/charts/chart-1') return reply(record());
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
    assert.equal(await p.locator('#indicator-tab-catalog').getAttribute('aria-selected'), 'true');
    assert.equal(await p.locator('#indicator-legacy-sync-button').innerText(), '把本浏览器的 2 个自选同步到账号');
    assert.equal(migration.calls.filter(call => call.method === 'PUT').length, 0, '旧浏览器自选只提示，读取不写入');
    assert.equal(migration.calls.some(call => call.path === '/charts'), false, '图表栏在打开时才加载');
    await p.locator('#indicator-legacy-sync-button').click();
    await p.waitForFunction(() => document.querySelectorAll('[data-favorite-card]').length === 2);
    assert.deepEqual(migration.favorites.items.map(item => item.id), [sid(2), sid(1)]);
    assert.deepEqual(await p.evaluate(key => JSON.parse(localStorage.getItem(key)), key), [sid(2), sid(1)]);
    assert.equal(await p.locator('#indicator-tab-favorites').getAttribute('aria-selected'), 'true');
    await p.locator('.favorite-value').first().waitFor();
    assert.match(await p.locator('[data-favorite-card]').first().innerText(), /27[\s\S]*较上期：[+]7[\s\S]*同期第 1\/4/);
    assert.ok(migration.calls.filter(call => call.path.endsWith('/cycle')).every(call => call.url.searchParams.get('date') === '2029-09-15' && !call.url.searchParams.has('template')));
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
      if (event.path.endsWith('/cycle') || event.path.endsWith('/data')) {
        active++; maximum = Math.max(maximum, active);
        await new Promise(resolve => setTimeout(resolve, 50)); active--;
      }
      return source(event);
    }, {favorites: [...Array.from({length: 30}, (_, index) => initialItem(sid(index + 1))), initialItem('chart-1', 'chart')]});
    const firstReads = lazy.calls.filter(call => call.path.endsWith('/cycle')).length;
    assert.ok(firstReads > 0 && firstReads < 30, '周期摘要只读取视野附近的自选');
    await lazy.page.locator('[data-favorite-card="chart:chart-1"]').scrollIntoViewIfNeeded();
    await lazy.page.locator('[data-favorite-card="chart:chart-1"] svg').waitFor();
    assert.ok(maximum <= 4, '周期摘要和图表缩略图合用最多4个并发任务');
    assert.ok(lazy.calls.filter(call => call.path.endsWith('/cycle')).length > firstReads);
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
