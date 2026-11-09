/* Isolated browser checks: all data and API writes are fulfilled in-process. */
const {assert, render, renderLibrary, launch, open, templates, template, record, cycle} = require('./chart-ui-fixture.cjs');
const fields = Array.from({length: 17}, (_, i) => ({series_id: `kb-builder-${i}`, name: `目录指标 ${i} <安全>`,
  unit: i === 2 ? '元' : i === 3 ? '' : '吨', category: '合成目录', source: '测试来源', frequency: '周',
  macro: false, caliber: {}, registered: true, fetch_mode: 'readonly', status: '可用'}));
const rows = fields.slice(0, 3), extra = {rows, groups: [{name: '合成目录', rows}], catalog_remote: false, catalog_total: rows.length};
function curveData(definition) {
  if (definition.time.mode !== 'cycle') return {mode: 'trend', transform: definition.time.transform, curves: definition.curves.map(c => ({...c,
    unit: c.derived?.op === 'div' ? '倍' : fields.find(f => f.series_id === (c.series_id || c.derived?.a))?.unit,
    points: [{date: '2029-09-01', value: 10}, {date: '2029-09-08', value: 12}], freq_days: 7})), notices: []};
  if (definition.curves.length === 1) return {...cycle(), template: definition.time.template};
  return {mode: 'cycle', template: definition.time.template, common_current_start: '2029-09-01', common_previous_start: '2028-09-01', band: [], warnings: [],
    curves: definition.curves.map(c => ({...c, unit: c.derived?.op === 'div' ? '倍' : fields.find(f => f.series_id === (c.series_id || c.derived?.a))?.unit,
      cycle: {...cycle(), position: {...cycle().position, recent_mean: 25}}}))};
}
function backend(capture = {}) {
  return async ({path, url, method, body, reply}) => {
    if (path === '/charts/anomalies') return reply({as_of: '2029-09-15', items: [], total: 0, failed: []});
    if (path === '/cycle-templates') return reply(templates);
    if (path === '/charts/builder-options') return reply({template, template_source: 'native_sheet', groups: ['已有组']});
    if (path === '/indicators/catalog') {
      const ids = url.searchParams.getAll('series_id'), search = url.searchParams.get('search') || '';
      const selected = fields.filter(f => (!ids.length || ids.includes(f.series_id)) && f.name.includes(search));
      return reply({indicators: selected, total: selected.length, offset: 0, next_offset: null});
    }
    if (path === '/charts/preview') { (capture.previews ||= []).push(body); return reply(curveData(body)); }
    if (path === '/charts/batch/preview') {
      capture.batchPreview = body;
      return reply({items: body.series_ids.map(id => {
        const field = fields.find(f => f.series_id === id), definition = {format: 'chart.v1', title: field.name,
          group: body.group, curves: [{series_id: id, label: field.name}], time: {mode: 'cycle', template: body.template || template}};
        return {definition, payload: cycle()};
      })});
    }
    if (path === '/charts/batch' && method === 'POST') {
      capture.batch = body;
      return reply({created: body.series_ids.map((id, i) => record(`batch-${i}`, {definition: {title: fields.find(f => f.series_id === id).name}}))}, 201);
    }
    if (path === '/charts' && method === 'POST') { capture.saved = body; return reply(record('builder-saved', {definition: body}), 201); }
    if (path === '/charts') return reply({items: [], total: 0, next_offset: null});
    if (path === '/indicators/previews') return reply({cards: [], html: ''});
    if (path.endsWith('/cycle')) return reply(cycle());
    return false;
  };
}
async function ready(p) {
  try { await p.locator('#builder-save:not(:disabled)').waitFor(); }
  catch (error) { throw new Error(error.message + '\n面板：' + await p.locator('#chart-builder').innerText()); }
}
(async () => {
  const browser = await launch();
  try {
    const capture = {}, lib = await open(browser, '/sn/indicators?tab=catalog', renderLibrary({}, extra), backend(capture));
    const p = lib.page;
    await p.locator('[data-compare="kb-builder-0"]').check(); await p.locator('[data-compare="kb-builder-2"]').check();
    await p.locator('#indicator-chart-open').click(); await ready(p);
    assert.equal(await p.locator('#chart-builder').isVisible(), true);
    assert.match(await p.locator('#builder-template-note').innerText(), /09-01.*明确元数据/);
    assert.deepEqual(capture.previews.at(-1).curves.map(c => c.axis), ['left', 'right']);
    assert.equal(await p.locator('#builder-preview [data-series]').count(), 2);
    assert.equal(await p.locator('#builder-preview [data-band]').count(), 0);
    assert.match(await p.locator('#builder-statistics').innerText(), /同期排名[\s\S]*较近 5 期均值[\s\S]*目录指标 0 <安全>[\s\S]*目录指标 2 <安全>/);
    await p.locator('[data-builder-cycle]').first().selectOption('current_and_previous'); await ready(p);
    assert.equal(await p.locator('#builder-preview [data-series]').count(), 3);
    assert.ok(await p.locator('#builder-preview [data-axis-tick="right"]').count());
    const dashed = await p.locator('#builder-preview [data-curve-run]').evaluateAll(nodes => nodes.map(n => n.getAttribute('stroke-dasharray')));
    assert.ok(dashed.includes('7 4'));
    assert.equal(await p.locator('#chart-builder img').count(), 0);
    await p.locator('#builder-mode').selectOption('trend'); await ready(p);
    await p.locator('[data-builder-kind]').first().selectOption('bar'); await ready(p);
    assert.equal(capture.previews.at(-1).curves[0].kind, 'bar');
    await p.locator('#builder-add-derived').click(); await p.locator('#builder-derived-label').fill('价差 <研究员>');
    await p.locator('#builder-derived-apply').click();
    assert.match(await p.locator('#builder-status').innerText(), /单位相同/);
    await p.locator('#builder-derived-op').selectOption('div'); await p.locator('#builder-derived-label').fill('比值 <研究员>');
    await p.locator('#builder-derived-apply').click();
    await p.locator('#builder-status').filter({hasText: '超过两种单位'}).waitFor();
    await p.locator('#builder-transform').selectOption('rebase'); await ready(p);
    const formula = capture.previews.at(-1).curves[2];
    assert.deepEqual(formula.derived, {op: 'div', a: 'kb-builder-0', b: 'kb-builder-2'});
    assert.equal(formula.label, '比值 <研究员>');
    await p.locator('#builder-save-favorite').check(); await p.locator('#builder-save').click();
    await p.locator('#builder-status').filter({hasText: '并加入自选'}).waitFor();
    assert.equal(capture.saved.curves[2].points, undefined); assert.equal(capture.saved.curves[2].derived.op, 'div');
    assert.equal(lib.favorites.items[0].id, 'builder-saved');
    await p.locator('#chart-builder-close').click(); await lib.close();

    const batchCapture = {}, batch = await open(browser, '/sn/indicators?tab=charts', renderLibrary(), backend(batchCapture));
    await batch.page.locator('[data-chart-builder="new"]').click();
    await batch.page.locator('[data-builder-indicator="kb-builder-0"]').check();
    await batch.page.locator('[data-builder-indicator="kb-builder-1"]').check();
    await batch.page.locator('#builder-mode').selectOption('batch'); await batch.page.locator('#builder-group').fill('新建图集');
    await ready(batch.page); assert.equal(await batch.page.locator('#builder-preview .charts-card').count(), 2);
    assert.equal(batchCapture.batchPreview.template, undefined, '默认模板由每个指标的来源表单独核对');
    await batch.page.locator('#builder-save').click(); await batch.page.locator('#builder-status').filter({hasText: '已保存 2 张'}).waitFor();
    assert.equal(batchCapture.batch.group, '新建图集'); assert.equal(await batch.page.locator('#builder-saved a').count(), 2);
    await batch.page.locator('#chart-builder-close').click(); await batch.close();

    const favorite = await open(browser, '/sn/indicators?tab=favorites', renderLibrary(), backend(), {favorites: [
      {kind: 'indicator', id: 'kb-builder-0', title: fields[0].name, status: 'active'},
      {kind: 'indicator', id: 'kb-builder-1', title: fields[1].name, status: 'active'}]});
    await favorite.page.locator('[data-builder-favorite="kb-builder-0"]').check();
    await favorite.page.locator('#favorites-chart-open').click(); await ready(favorite.page);
    assert.equal(await favorite.page.locator('[data-builder-curve]').count(), 1);
    await favorite.page.locator('[data-builder-indicator="kb-builder-1"]').check(); await ready(favorite.page);
    await favorite.page.locator('#builder-add-derived').click();
    await favorite.page.locator('#builder-derived-label').fill('差值'); await favorite.page.locator('#builder-derived-apply').click(); await ready(favorite.page);
    assert.equal(await favorite.page.locator('[data-builder-curve]').count(), 3);
    await favorite.page.locator('#chart-builder-close').click(); await favorite.close();

    const single = await open(browser, '/sn/indicators/kb-builder-0', render('indicator.html', {}, {detail: {...fields[0], history: [], revisions: []}, usage: {}}), backend());
    await single.page.locator('[data-chart-builder="single"]').click(); await ready(single.page);
    assert.equal(await single.page.locator('#builder-mode').inputValue(), 'cycle');
    assert.equal(await single.page.locator('[data-builder-curve]').count(), 1);
    await single.page.locator('#chart-builder-close').click(); await single.close();

    const limits = await open(browser, '/sn/indicators?tab=charts', renderLibrary(), backend());
    const lp = limits.page;
    await lp.locator('[data-chart-builder="new"]').click();
    for (let i = 0; i < 16; i++) await lp.locator(`[data-builder-indicator="kb-builder-${i}"]`).check();
    await lp.locator('[data-builder-indicator="kb-builder-16"]').click();
    assert.equal(await lp.locator('[data-builder-curve]').count(), 16);
    assert.equal(await lp.locator('[data-builder-indicator="kb-builder-16"]').isChecked(), false);
    await lp.locator('#builder-status').filter({hasText: '最多 8 条曲线'}).waitFor();
    assert.equal(await lp.locator('#builder-save').isDisabled(), true);
    await lp.locator('#builder-mode').selectOption('trend'); await lp.locator('#builder-transform').selectOption('rebase'); await ready(lp);
    // Missing-unit subtraction requires a separate, explicit researcher confirmation.
    for (let i = 15; i >= 4; i--) await lp.locator(`[data-builder-indicator="kb-builder-${i}"]`).uncheck();
    await lp.locator('#builder-transform').selectOption('raw'); await ready(lp);
    assert.equal(await lp.locator('[data-builder-curve]').count(), 4, '未知单位只提示，不冒充第三种已知单位');
    await lp.locator('#builder-add-derived').click(); await lp.locator('#builder-derived-a').selectOption('kb-builder-0');
    await lp.locator('#builder-derived-b').selectOption('kb-builder-3'); await lp.locator('#builder-derived-label').fill('已核对口径');
    await lp.locator('#builder-derived-apply').click(); assert.match(await lp.locator('#builder-status').innerText(), /核对并勾选确认/);
    await lp.locator('#builder-unit-confirm').check(); await lp.locator('#builder-derived-apply').click(); await ready(lp);
    // Hidden cycle fields must not block a valid trend submission.
    await lp.locator('#builder-mode').selectOption('cycle'); await lp.locator('#builder-cycles').fill('0');
    await lp.locator('#builder-mode').selectOption('trend'); await ready(lp);
    assert.equal(await lp.locator('#builder-cycles').isDisabled(), true);
    await lp.locator('#builder-save').click(); await lp.locator('#builder-status').filter({hasText: '已保存 1 张'}).waitFor();
    await lp.locator('#chart-builder-close').click(); await limits.close();

    const roCapture = {}, readonly = await open(browser, '/sn/indicators?tab=charts', renderLibrary({can_write: false}), backend(roCapture));
    await readonly.page.locator('[data-chart-builder="new"]').click(); await readonly.page.locator('[data-builder-indicator="kb-builder-0"]').check();
    await readonly.page.locator('#builder-preview svg').waitFor(); assert.equal(await readonly.page.locator('#builder-save').isDisabled(), true);
    assert.equal(roCapture.saved, undefined); await readonly.page.locator('#chart-builder-close').click(); await readonly.close();

    let releaseSave, started = false;
    const saveGate = new Promise(resolve => { releaseSave = resolve; }), normal = backend();
    const reopen = await open(browser, '/sn/indicators?tab=charts', renderLibrary(), async entry => {
      if (entry.path === '/charts' && entry.method === 'POST') { started = true; await saveGate; return entry.reply(record('older-save', {definition: entry.body}), 201); }
      return normal(entry);
    });
    await reopen.page.locator('[data-chart-builder="new"]').click(); await reopen.page.locator('[data-builder-indicator="kb-builder-0"]').check(); await ready(reopen.page);
    await reopen.page.locator('#builder-save').click(); await reopen.page.waitForFunction(() => document.getElementById('builder-controls').disabled);
    assert.equal(started, true);
    await reopen.page.locator('#chart-builder-close').click(); await reopen.page.locator('[data-chart-builder="new"]').click();
    await reopen.page.locator('[data-builder-indicator="kb-builder-1"]').check(); releaseSave(); await ready(reopen.page);
    assert.equal(await reopen.page.locator('#builder-saved a').count(), 0, '已关闭轮次的保存响应不覆盖新面板');
    assert.equal(await reopen.page.locator('[data-builder-label]').inputValue(), fields[1].name);
    await reopen.page.locator('#chart-builder-close').click(); await reopen.close();
    console.log('chart-builder-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
