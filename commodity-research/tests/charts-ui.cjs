const {assert, render, renderLibrary, launch, open, prefix, templates, record, cycle, origin} = require('./chart-ui-fixture.cjs');
const libraryHTML = renderLibrary(), detailHTML = render('chart_detail.html');
(async () => {
  const browser = await launch();
  try {
    const records = Array.from({length: 30}, (_, index) => record('chart-' + (index + 1)));
    records[0].warnings = [{code: 'unit_missing', series_id: 'kb-fixture-series', label: '合成指标'}];
    records.forEach((r, i) => { if (i) r.definition.title = '合成图' + (i + 1); r.definition.group = i % 2 ? '乙组' : '甲组'; });
    let active = 0, maximum = 0, dataCalls = 0;
    const lib = await open(browser, '/sn/indicators?tab=charts', libraryHTML, async ({path, url, reply}) => {
      if (path === '/charts/anomalies') return reply({as_of: '2029-09-15', items: [], total: 0, failed: []});
      if (path === '/charts') {
        const q = url.searchParams.get('q') || '', group = url.searchParams.get('group'), offset = Number(url.searchParams.get('offset') || 0), limit = Number(url.searchParams.get('limit'));
        const filtered = records.filter(r => r.definition.title.includes(q) && (!group || r.definition.group === group));
        return reply({items: filtered.slice(offset, offset + limit), total: filtered.length, offset, next_offset: offset + limit < filtered.length ? offset + limit : null});
      }
      if (/^\/charts\/chart-\d+\/data$/.test(path)) {
        dataCalls++; active++; maximum = Math.max(maximum, active);
        await new Promise(resolve => setTimeout(resolve, 50)); active--;
        return reply(cycle());
      }
      return false;
    });
    assert.equal(await lib.page.locator('.charts-card').count(), 24);
    assert.ok(dataCalls > 0 && dataCalls < 24, '只读取进入视野附近的缩略图');
    assert.ok(maximum <= 4, '图表数据并发不超过4');
    const first = lib.page.locator('[data-chart-id="chart-1"]');
    const marker = first.locator('h2 .charts-warning-marker');
    assert.equal(await marker.isVisible(), true);
    assert.match(await marker.getAttribute('title'), /合成指标：曲线缺少单位/);
    assert.doesNotMatch(await marker.getAttribute('title'), /kb-/);
    assert.equal(await first.locator('.charts-notice.warning').count(), 0);
    assert.ok(await first.locator('[data-axis-tick=left]').count() <= 4);
    assert.equal(await lib.page.locator('.charts-card img').count(), 0); assert.equal(await lib.page.evaluate(() => window.__xss), undefined);
    await lib.page.locator('#charts-more').click();
    await lib.page.waitForFunction(() => document.querySelectorAll('.charts-card').length === 30);
    await lib.page.locator('#chart-groups button').filter({hasText: '乙组'}).click();
    await lib.page.waitForFunction(() => document.querySelector('#chart-count').textContent.includes('/ 15'));
    await lib.page.locator('#chart-search').fill('合成图30');
    await lib.page.waitForFunction(() => document.querySelectorAll('.charts-card').length === 1);
    assert.match(await lib.page.locator('.charts-card').innerText(), /合成图30/);
    await lib.close();

    const galleryRecords = records.map(item => ({...item, definition: {...item.definition, group: '整组图集'}}));
    const gallery = await open(browser, '/sn/indicators?tab=charts', libraryHTML, ({path, url, reply}) => {
      if (path === '/charts/anomalies') return reply({items: [], total: 0, failed: []});
      if (path === '/charts') {
        const offset = Number(url.searchParams.get('offset') || 0), limit = Number(url.searchParams.get('limit') || 24);
        return reply({items: galleryRecords.slice(offset, offset + limit), total: 30, next_offset: offset + limit < 30 ? offset + limit : null});
      }
      if (path.endsWith('/data')) return reply(cycle());
      return false;
    });
    await gallery.page.locator('#chart-groups button').filter({hasText: '整组图集'}).click();
    await gallery.page.waitForFunction(() => document.querySelectorAll('.charts-card').length === 30);
    assert.equal(await gallery.page.locator('#charts-more').isVisible(), false, '选择分组后自动平铺整组，继续复用懒加载');
    await gallery.close();

    let saved = record(), conflict = true, patch, deletedRevision;
    saved.definition.time.segment_styles = [{label: '2027', color: '#AA5500', width: 1.25, dash: 'dot'}];
    saved.definition.time.visible_cycles = 4;
    saved.warnings = [{code: 'unit_missing', label: '合成指标'}];
    const detail = await open(browser, '/sn/charts/chart-1', detailHTML, async ({path, method, body, url, reply}) => {
      if (path === '/cycle-templates') return reply(templates);
      if (path === '/charts/chart-1' && method === 'GET') return reply(saved);
      if (path === '/charts/chart-1/data') return reply({...cycle(), revision: saved.revision, warnings: ['empty_segments_omitted']});
      if (path === '/charts/chart-1/revisions') return reply({items: [{revision: saved.revision, definition: saved.definition, recorded_at: '2030-01-01', edited_by: '<编辑者>'}], next_offset: null});
      if (path === '/charts/chart-1' && method === 'PATCH') {
        patch = body;
        if (conflict) { conflict = false; saved = {...saved, revision: 2, definition: {...saved.definition, title: '另一成员的新版本'}}; return reply({detail: '图表已更新，请刷新后继续修改'}, 409); }
        saved = {...saved, revision: 3, definition: {...saved.definition, ...body}}; delete saved.definition.revision; return reply(saved);
      }
      if (path === '/charts/chart-1' && method === 'DELETE') { deletedRevision = Number(url.searchParams.get('revision')); return reply({status: 'deleted', revision: 4}); }
      return false;
    }, {documents: {[prefix + '/sn/indicators']: '<p>图表已删除</p>'}});
    const p = detail.page;
    await p.locator('#chart-plot svg').waitFor();
    assert.equal(await p.locator('[name=visible_cycles]').inputValue(), '4');
    assert.equal(await p.locator('#chart-reconvert-panel').isVisible(), false);
    const detailStar = p.locator('[data-favorite-kind="chart"]');
    assert.equal(await detailStar.getAttribute('aria-pressed'), 'false');
    // The bookmark follows the title on the same line instead of wrapping below it.
    assert.equal(await p.locator('#chart-title + .favorites-bookmark').count(), 1);
    const [titleBox, starBox] = [await p.locator('#chart-title').boundingBox(), await detailStar.boundingBox()];
    assert.ok(starBox.x > titleBox.x + titleBox.width - 1 && Math.abs((starBox.y + starBox.height / 2) - (titleBox.y + titleBox.height / 2)) < 12);
    assert.equal((await detailStar.innerText()).trim(), '');
    await detailStar.press('Space');
    await p.waitForFunction(() => document.querySelector('[data-favorite-kind="chart"]').getAttribute('aria-pressed') === 'true');
    assert.equal(detail.favorites.items[0].id, 'chart-1');
    assert.equal(await p.locator('a').filter({hasText: '返回图表库'}).getAttribute('href'), prefix + '/sn/indicators?tab=charts&date=2029-09-15');
    assert.match(await p.locator('#chart-notices').innerText(), /合成指标：曲线缺少单位[\s\S]*已省略没有观测的周期/);
    assert.equal(await p.locator('#chart-notices').getAttribute('class'), 'charts-footnote');
    assert.equal(await p.locator('#chart-plot + #chart-notices').count(), 1);
    assert.equal(await p.locator('#chart-statistics .charts-notice.warning').count(), 0);
    const yTicks = await p.locator('#chart-plot [data-axis-tick=left]').count();
    assert.ok(yTicks >= 5 && yTicks <= 7);
    assert.equal(await p.locator('[data-series="1"] [data-curve-run]').first().getAttribute('stroke'), '#AA5500');
    assert.equal(await p.locator('[data-legend="1"] line').getAttribute('stroke'), '#AA5500');
    assert.match(await p.locator('#chart-statistics').innerText(), /1\/4/);
    assert.match(await p.locator('#chart-statistics').innerText(), /上涨 1 段\/共 2 段/);
    assert.match(await p.locator('#chart-statistics').innerText(), /历史参照，不是预测；样本 3 期/);
    assert.equal(await p.locator('#chart-statistics img').count(), 0);
    assert.ok(await p.locator('[data-band=range]').count());
    assert.ok(await p.locator('[data-series="3"] path').first().getAttribute('stroke-width') >= 3);
    assert.equal(await p.locator('[data-series="2"] path').first().getAttribute('stroke-dasharray'), '7 4');
    assert.ok(await p.locator('[data-series="0"] [data-curve-run]').count() >= 2, '中位间隔以上的长缺口断线');
    const area = await p.locator('[data-chart-hover]').boundingBox();
    await p.mouse.move(area.x + area.width * 7 / 365, area.y + area.height / 2);
    assert.match(await p.locator('.chart-kit-tooltip').innerText(), /2029-09-08/);
    await p.mouse.move(area.x + area.width * 200 / 365, area.y + area.height / 2);
    assert.match(await p.locator('.chart-kit-tooltip').innerText(), /第 200 天[\s\S]*此位置缺失/);
    await p.locator('[data-legend="3"]').click(); assert.equal(await p.locator('[data-series="3"]').count(), 0);
    await p.locator('[data-legend="3"]').press('Enter'); assert.equal(await p.locator('[data-series="3"]').count(), 1);
    await p.evaluate(() => document.documentElement.classList.add('dark'));
    const download = p.waitForEvent('download'); await p.locator('[data-export-svg]').click(); assert.match((await download).suggestedFilename(), /\.svg$/);
    await p.locator('[data-export-copy]').click();
    await p.locator('#chart-exports [role=status]').filter({hasText: /图片已复制|复制失败/}).waitFor();
    if ((await p.locator('#chart-exports [role=status]').innerText()).includes('复制失败')) assert.equal(await p.locator('[data-export-png]').isVisible(), true);
    await p.locator('[name=title]').fill('我的修改'); await p.locator('#chart-editor button[type=submit]').click();
    await p.locator('#chart-conflict').waitFor({state: 'visible'}); assert.equal(patch.revision, 1); assert.equal(await p.locator('[name=title]').isDisabled(), true);
    await p.locator('#chart-reload').click();
    await p.waitForFunction(() => document.querySelector('[name=title]').value === '另一成员的新版本');
    await p.locator('[name=title]').fill('修订后的图');
    await p.locator('[name=template]').selectOption('__custom'); await p.locator('[name=cycle_start]').fill('08-01');
    await p.locator('[name=cycles]').fill('6'); await p.locator('[name=band_cycles]').fill('4'); await p.locator('[name=transform]').selectOption('change');
    await p.locator('[name=visible_cycles]').fill('2');
    await p.locator('[data-curve-color]').fill('#abcdef'); await p.locator('#chart-editor button[type=submit]').click();
    await p.locator('#chart-detail-status').filter({hasText: '已保存图表定义'}).waitFor();
    assert.equal(patch.revision, 2); assert.equal(patch.time.template.start, '08-01'); assert.equal(patch.time.cycles, 6); assert.equal(patch.curves[0].style.color, '#abcdef');
    assert.equal(patch.time.transform, 'change'); assert.match(await p.locator('#chart-revisions').innerText(), /v3/);
    assert.equal(patch.time.visible_cycles, 2);
    assert.deepEqual(patch.time.segment_styles, [{label: '2027', color: '#AA5500', width: 1.25, dash: 'dot'}]);
    assert.equal(await p.locator('#chart-plot [data-series]').count(), 2);
    assert.equal(await p.locator('#chart-plot [data-legend]').count(), 4, '隐藏年份仍可从图例打开');
    assert.equal(await p.locator('#chart-plot [data-latest-value]').count(), 1);
    const bandPath = await p.locator('#chart-plot [data-band=range]').first().getAttribute('d');
    await p.locator('#chart-plot [data-show-all-cycles]').click();
    assert.equal(await p.locator('#chart-plot [data-series]').count(), 4);
    assert.equal(await p.locator('#chart-plot [data-show-all-cycles]').getAttribute('aria-pressed'), 'true');
    assert.equal(await p.locator('#chart-plot [data-band=range]').first().getAttribute('d'), bandPath);
    await p.locator('#chart-plot [data-show-all-cycles]').press('Enter');
    assert.equal(await p.locator('#chart-plot [data-series]').count(), 2);
    p.once('dialog', dialog => dialog.accept()); await p.locator('#chart-delete').click(); await p.waitForURL(origin + prefix + '/sn/indicators?tab=charts&date=2029-09-15'); assert.equal(deletedRevision, 3);
    await detail.close();

    let nativeSaved = record(), reconvertPatch;
    nativeSaved.definition.origin = {kind: 'native', ref: 'nc-' + '1'.repeat(32)};
    const suggestedDefinition = structuredClone(nativeSaved.definition);
    suggestedDefinition.title = '原表新建议 <安全文本>';
    suggestedDefinition.time.template.start = '08-01';
    const reconvert = await open(browser, '/sn/charts/chart-1', detailHTML, ({path, method, body, reply}) => {
      if (path === '/cycle-templates') return reply(templates);
      if (path === '/charts/chart-1' && method === 'GET') return reply(nativeSaved);
      if (path.endsWith('/data')) return reply({...cycle(), revision: nativeSaved.revision});
      if (path.endsWith('/revisions')) return reply({items: [{revision: nativeSaved.revision, definition: nativeSaved.definition}], next_offset: null});
      if (path.endsWith('/reconvert')) return reply({id: nativeSaved.id, revision: nativeSaved.revision, suggestion: {
        chart_key: nativeSaved.definition.origin.ref, title: suggestedDefinition.title, convertible: true, mode: 'cycle',
        definition: suggestedDefinition, warnings: [], missing_curves: []}});
      if (path === '/charts/chart-1' && method === 'PATCH') {
        reconvertPatch = body;
        nativeSaved = {...nativeSaved, revision: nativeSaved.revision + 1, definition: {...body}};
        delete nativeSaved.definition.revision; return reply(nativeSaved);
      }
      return false;
    });
    const r = reconvert.page;
    await r.locator('#chart-reconvert').click();
    await r.locator('#chart-reconvert-preview').waitFor({state: 'visible'});
    assert.equal(reconvertPatch, undefined, '建议展示时不保存');
    assert.match(await r.locator('#chart-reconvert-summary').innerText(), /08-01/);
    assert.equal(await r.locator('#chart-reconvert-summary img').count(), 0);
    await r.locator('#chart-reconvert-cancel').click();
    assert.equal(reconvertPatch, undefined);
    assert.equal(await r.locator('#chart-reconvert-preview').isVisible(), false);
    await r.locator('#chart-reconvert').click();
    await r.locator('#chart-reconvert-confirm').click();
    await r.locator('#chart-detail-status').filter({hasText: '已按原表重新转换并保存新修订'}).waitFor();
    assert.equal(reconvertPatch.revision, 1);
    assert.equal(reconvertPatch.time.template.start, '08-01');
    assert.equal(nativeSaved.revision, 2);
    assert.match(await r.locator('#chart-revisions').innerText(), /v2/);
    await reconvert.close();

    const readonly = await open(browser, '/sn/charts/chart-1', render('chart_detail.html', {can_write: false, write_reason: '当前账号没有研究工作台编辑权限'}), ({path, reply}) => {
      if (path === '/cycle-templates') return reply(templates);
      if (path.endsWith('/data')) return reply({...cycle(), revision: 1});
      if (path.endsWith('/revisions')) return reply({items: [], next_offset: null});
      return reply(record());
    });
    assert.equal(await readonly.page.locator('[name=title]').isDisabled(), true); assert.match(await readonly.page.locator('#chart-write-reason').innerText(), /没有/);
    assert.equal(await readonly.page.locator('#chart-reconvert').count(), 0); await readonly.close();
    for (const status of [403, 409]) {
      const blocked = await open(browser, '/sn/indicators?tab=charts', libraryHTML, ({reply}) => reply({detail: status === 403 ? '受限视角暂不开放图表库和周期对比' : '请重新打开研究台更新来源授权'}, status));
      assert.match(await blocked.page.locator('#charts-status').innerText(), /受限视角|重新打开研究台/); assert.equal(await blocked.page.locator('.charts-card').count(), 0); await blocked.close();
    }
    console.log('charts-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
