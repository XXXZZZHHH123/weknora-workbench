/* Report cards, automatic chart citations and manual dedup use the actual report template. */
const {assert, render, launch, open, prefix, definition, cycle} = require('./chart-ui-fixture.cjs');
const automatic = Array.from({length: 8}, (_, index) => ({key: `chart:chart-${index}`, kind: 'chart', title: `引用图 ${index + 1}`, caption: '引用于：研判（已保存依据）', unit: '吨', as_of: '2029-09-15', single_series_id: index === 0 ? 'manual-duplicate' : null,
  reference: {id: `chart-${index}`, revision: 2}, references: [{kind: 'judgment', text: '研判（已保存依据）'}]}));
const manual = {key: 'indicator:manual-extra', kind: 'indicator', title: '额外指标', caption: '研究员图说', unit: '元', as_of: '2029-09-15', series_id: 'manual-extra'};
function report({visible = true} = {}) {
  const panels = Object.fromEntries(['summary','research','dashboard','watch'].map(key => [key, {visible: key === 'dashboard' ? visible : true, text: key === 'dashboard' ? '已保存图表按语' : '', references: []}]));
  return {report_title: '合成报告', coverage: {}, panels, is_manual_report: false, manual_report: null, briefs: [], judgment: null, conclusion: null, judgment_replay: null,
    strategy_overview: {text: '暂无策略', triggered: []}, research_cards: [], strategies: [], strategy_events: [], changes: [], gaps: [], derived: [], observations: [], counts: {observations: 0, derived: 0},
    selected_figures: [{series_id: 'manual-duplicate', caption: '旧草稿重复手选'}, {series_id: 'manual-extra', caption: '研究员图说'}], report_figures: [...automatic, manual], automatic_figures: automatic,
    figure_options: [{series_id: 'manual-duplicate', name: '重复指标', unit: '吨'}, {series_id: 'manual-extra', name: '额外指标', unit: '元'}],
    editable_draft: {format: 'research-report-draft.v1', title: '合成报告', date: '2029-09-15', period: 'weekly', references: [],
      figures: [{series_id: 'manual-duplicate', caption: '旧草稿重复手选'}, {series_id: 'manual-extra', caption: '研究员图说'}],
      sections: Object.fromEntries(Object.entries(panels).map(([key,panel]) => [key,{visible: panel.visible,text: panel.text}]))}};
}
const html = (live = true, visible = true, value = null) => render('reports.html', {}, {report: value || report({visible}), live_report: live, files: [], selected: {date: '2029-09-15'}, period_kind: 'weekly', report_start: '2029-09-10', preview: '{}'}).replace('</head>', '<style>#report-research{min-height:1100px}</style></head>');
(async () => {
  const browser = await launch();
  try {
    let active = 0, maximum = 0, saved;
    const fixture = await open(browser, prefix + '/sn/reports', html(), async ({path, url, body, reply}) => {
      if (path === '/research-references') return reply({items: []});
      if (path === '/report-drafts') { saved = body; return reply({detail: '合成保存完成，保留页面供断言'}, 409); }
      if (path.startsWith('/reports/figures/')) {
        active++; maximum = Math.max(maximum, active);
        await new Promise(resolve => setTimeout(resolve, 20));
        active--;
        assert.equal(url.searchParams.get('date'), '2029-09-15');
        const key = decodeURIComponent(path.split('/').at(-1)), figure = [...automatic, manual].find(item => item.key === key);
        if (key === 'chart:chart-1') return reply({figure, status: 'unavailable', notice: '合成来源已撤回，请核对权限'});
        if (figure.kind === 'chart') return reply({figure, rendered: {payload: cycle(), definition, notice: '已有新修订'}});
        return reply({figure, series: {name: '额外指标', unit: '元', source: '保存来源', min: 2, max: 8, path: 'M0 0L1 1', points: [{x:52,y:128,date:'2029-09-01',value:2},{x:484,y:16,date:'2029-09-15',value:8}]}});
      }
      return false;
    });
    const p = fixture.page;
    assert.deepEqual(await p.locator('#report-readable > section').evaluateAll(nodes => nodes.map(node => node.id)), ['report-summary','report-research','report-dashboard','report-strategy']);
    assert.equal(await p.locator('#report-figures > figure').count(), 9, '引用图不受6张手选上限影响');
    assert.match(await p.locator('#report-dashboard').innerText(), /已保存图表按语/);
    assert.equal(fixture.calls.filter(call => call.path.startsWith('/reports/figures/')).length, 0, '图表未入视口时不读数据');
    await p.locator('[data-report-figure="chart:chart-1"]').scrollIntoViewIfNeeded();
    await p.getByText('合成来源已撤回，请核对权限', {exact: false}).waitFor();
    assert.equal(await p.locator('[data-report-figure="chart:chart-1"] h4').innerText(), '引用图 2', '失败图保留标题与引用处');
    await p.locator('[data-report-figure="indicator:manual-extra"]').scrollIntoViewIfNeeded();
    await p.locator('[data-report-figure="indicator:manual-extra"] svg').waitFor();
    assert.ok(maximum <= 1, '逐图串行读取，避免多图同时放大观测预算');
    await p.locator('#report-edit-open').click();
    await p.locator('#report-editor details').filter({hasText: '选图与图说'}).locator('summary').click();
    assert.match(await p.locator('#report-editor').innerText(), /研判和策略引用的图会自动放进报告/);
    assert.equal(await p.locator('#report-automatic-figures .report-automatic-figure').count(), 8);
    assert.equal(await p.locator('#report-automatic-figures input').count(), 0, '引用图只读');
    assert.equal(await p.locator('#report-figure-options .report-figure-option').count(), 1, '单曲线与手选重复指标已去重');
    assert.match(await p.locator('#report-figure-options').innerText(), /额外指标/);
    assert.equal(await p.getByLabel('额外指标的图说').inputValue(), '研究员图说');
    assert.match(await p.locator('#report-figure-count').innerText(), /已选 2 条，其中 1 条与引用图重复并合并展示/);
    await p.locator('[data-report-section="research"]').click();
    await p.locator('#report-edit-visible').uncheck();
    assert.equal(await p.locator('#report-automatic-figures .report-automatic-figure').count(), 0, '隐藏研判后去掉该节引用图');
    assert.equal(await p.locator('#report-figure-options .report-figure-option').count(), 2, '隐藏引用后恢复原手选候选');
    assert.equal(await p.getByLabel('重复指标的图说').inputValue(), '旧草稿重复手选');
    assert.match(await p.locator('#report-figure-count').innerText(), /已选 2 条，其中 0 条/);
    await p.locator('#report-edit-visible').check();
    assert.equal(await p.locator('#report-automatic-figures .report-automatic-figure').count(), 8);
    assert.equal(await p.locator('#report-figure-options .report-figure-option').count(), 1);
    await p.locator('#report-edit-save').click();
    await p.locator('#report-edit-status').filter({hasText: '合成保存完成'}).waitFor();
    assert.deepEqual(saved.draft.figures, [{series_id: 'manual-duplicate', caption: '旧草稿重复手选'}, {series_id: 'manual-extra', caption: '研究员图说'}], '报告展示去重，原手选和图说继续保留');
    await fixture.close();
    const frozen = await open(browser, prefix + '/sn/reports', html(false), () => { throw new Error('冻结归档不应读数据'); });
    assert.equal(await frozen.page.locator('[data-figure-plot]').count(), 0);
    assert.equal(await frozen.page.locator('#report-figures > figure').count(), 9);
    assert.match(await frozen.page.locator('#report-dashboard').innerText(), /引用于：研判/);
    assert.equal(frozen.calls.length, 0); await frozen.close();
    const hidden = await open(browser, prefix + '/sn/reports', html(true, false), ({path,reply}) => path === '/research-references' ? reply({items: []}) : false);
    assert.equal(await hidden.page.locator('#report-dashboard').count(), 0); assert.equal(hidden.calls.length, 0); await hidden.close();
    const cappedReport = report();
    for (let index = 0; index < 4; index++) {
      const series_id = 'saved-extra-' + index;
      cappedReport.editable_draft.figures.push({series_id, caption: '保留的第' + index + '条图说'});
      cappedReport.figure_options.push({series_id, name: '保留指标' + index});
    }
    cappedReport.figure_options.push({series_id: 'cap-candidate', name: '新增候选'});
    const capped = await open(browser, prefix + '/sn/reports', html(true, false, cappedReport), ({path,reply}) => path === '/research-references' ? reply({items: []}) : false);
    await capped.page.locator('#report-edit-open').click();
    await capped.page.locator('#report-editor details').filter({hasText: '选图与图说'}).locator('summary').click();
    assert.match(await capped.page.locator('#report-figure-count').innerText(), /已选 6 条，其中 1 条与引用图重复并合并展示/);
    assert.equal(await capped.page.locator('#report-figure-options input[type="checkbox"]:checked').count(), 5);
    await capped.page.getByLabel('新增候选', {exact: true}).click();
    assert.equal(await capped.page.getByLabel('新增候选', {exact: true}).isChecked(), false);
    assert.match(await capped.page.locator('#report-edit-status').innerText(), /手选已达 6 条上限.*其中 1 条与引用图重复并合并展示/);
    await capped.close();
    const revisions = report();
    const versionedFigure = {key: 'chart:versioned', kind: 'chart', title: '策略较新图', reference: {id: 'versioned', revision: 2}, single_series_id: 'new-direct',
      caption: '引用于：研判、策略「保存方案」', references: [
        {kind: 'judgment', text: '研判', chart_revision: 1, chart_title: '研判原图', single_series_id: 'manual-duplicate'},
        {kind: 'strategy', text: '策略「保存方案」', chart_revision: 2, chart_title: '策略较新图', single_series_id: 'new-direct'}]};
    revisions.automatic_figures = [versionedFigure]; revisions.all_automatic_figures = [versionedFigure];
    revisions.report_figures = [];
    revisions.figure_options.push({series_id: 'new-direct', name: '新版曲线指标'});
    revisions.all_figure_options = revisions.figure_options;
    const versioned = await open(browser, prefix + '/sn/reports', html(true, false, revisions), ({path,reply}) => path === '/research-references' ? reply({items: []}) : false);
    await versioned.page.locator('#report-edit-open').click();
    await versioned.page.locator('#report-editor details').filter({hasText: '选图与图说'}).locator('summary').click();
    assert.equal(await versioned.page.locator('#report-automatic-figures strong').innerText(), '策略较新图');
    assert.match(await versioned.page.locator('#report-figure-options').innerText(), /重复指标/);
    assert.equal(await versioned.page.getByLabel('新版曲线指标', {exact:true}).count(), 0);
    await versioned.page.locator('[data-report-section="watch"]').click();
    await versioned.page.locator('#report-edit-visible').uncheck();
    assert.equal(await versioned.page.locator('#report-automatic-figures strong').innerText(), '研判原图', '隐藏策略后引用图回退到仍显示的研判修订');
    assert.equal(await versioned.page.getByLabel('重复指标 · 吨', {exact:true}).count(), 0);
    assert.equal(await versioned.page.getByLabel('新版曲线指标', {exact:true}).count(), 1, '旧修订单曲线去重，不继续隐藏新版曲线指标');
    await versioned.page.locator('#report-edit-visible').check();
    assert.equal(await versioned.page.locator('#report-automatic-figures strong').innerText(), '策略较新图');
    assert.equal(await versioned.page.getByLabel('新版曲线指标', {exact:true}).count(), 0);
    await versioned.close();
    console.log('PASS: report chart section, lazy serial reads, visible failure, frozen zero reads, automatic readonly and manual dedup');
  } finally { await browser.close(); }
})().catch(error => {console.error(error);process.exitCode=1;});
