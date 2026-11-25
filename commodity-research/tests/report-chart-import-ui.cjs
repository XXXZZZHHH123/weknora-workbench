const {assert, render, launch, open, definition, cycle, prefix} = require('./chart-ui-fixture.cjs');
const source = {kind: 'knowledge_file', source_binding_id: 'binding-fixture', knowledge_id: 'report-fixture', file_version: 3};
const candidates = [{id: 'kb-fixture-series', name: '合成指标', unit: '吨', sheet: '数据表'}, {id: 'kb-alternate', name: '另一个真实候选', unit: '吨'}];
const charts = [
  {id: 'report-1', page: 2, source_index: 1, title: '<img src=x onerror=window.__reportXss=1>', chart_type: 'seasonal', ready: true, legend_semantics: 'periods', evidence: '原文图例：合成指标', warnings: ['模型页码与依据不一致，已按依据位置修正'], shape: {season_start_month: 9, period_labels: ['19/20', '20/21'], period_kind: 'crop_year', y_ticks: {min: '20', max: '100'}}, series: [{name: '合成指标', legends: ['2019', '2020'], unit: '吨', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-2', page: 3, source_index: 5, title: '另一张合成图', chart_type: 'trend', ready: false, ready_reason: '图表库已有同类图表', evidence: '另一张图的图例', possible_duplicate: true, duplicate_of: {id: 'report-4', page: 2, title: '定义失败的图'}, series: [{name: '图例二', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-3', page: 4, source_index: 6, title: '缺少指标的图', chart_type: 'comparison', ready: false, ready_reason: '未找到指标', evidence: '待核对的图例片段', missing_indicators: true, series: [{name: '待核对图例', candidates, selected_id: null}]},
  {id: 'report-4', page: 2, source_index: 1, title: '定义失败的图', chart_type: 'trend', ready: false, ready_reason: '图表定义校验失败', evidence: '原文图例', definition_failed: true, errors: ['定义校验不通过'], series: [{name: '原文图例', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-5', page: null, page_label: '片段9', source_index: 8, title: '无页码图', chart_type: 'trend', ready: false, ready_reason: '数值范围与原图刻度不符', evidence: '无页码片段', range_mismatch: true, shape_warnings: ['数值范围与原图刻度不符（原图 1–5，当前数据 100–120），请核对指标口径或单位'], series: [{name: '原文图例', candidates, selected_id: 'kb-fixture-series'}]}
];
const jobID = slug => 'report-chart-job-' + slug;
const meta = (slug, state) => ({id: jobID(slug), purpose: 'report_chart_import', workbench_id: 'fixture',
  state, phase_label: '正在识别图表'});
const result = (slug, fields) => ({...fields, request: {source, model_id: 'model-fixture'}, job: meta(slug, 'succeeded')});
function jobRoutes(args, slug, fields) {
  if (args.path.endsWith('/recognize')) return args.reply(meta(slug, 'queued'), 202);
  if (args.path === '/analysis-jobs/' + jobID(slug)) return args.reply(meta(slug, 'succeeded'));
  if (args.path === '/analysis-jobs/' + jobID(slug) + '/result') return args.reply(result(slug, fields));
  return false;
}
const html = render('chart_import.html');
const nativeTab = page => page.getByRole('tab', {name: '原表（Excel）'});
const reportTab = page => page.getByRole('tab', {name: '往期周报（PPT / PDF）'});
const base = ({path, reply}) => {
  if (path === '/charts/import/native/preview') return reply({total: 0, next_offset: null, sheets: []});
  if (path === '/strategies/models') return reply({models: [{id: 'model-fixture', name: '测试模型'}]});
  return false;
};
const previewPath = '/charts/import/report/preview';
const card = (page, id) => page.locator(`[data-report-chart="${id}"]`);
const checkbox = (page, id) => card(page, id).locator('[data-report-select]');
const count = (page, selected, ready) => page.locator('#report-chart-selection')
  .filter({hasText: `已选 ${selected} 张 · 可直接导入 ${ready} 张`}).waitFor();
const frames = page => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
const belowViewport = html.replace('</head>', `<style>
  #report-chart-cards { margin-top: 1500px; }
  #report-chart-cards .charts-grid { grid-template-columns: repeat(5, minmax(0, 1fr)); }
  #report-chart-cards > section + section { margin-top: 1600px; }
</style></head>`);
async function recognize(page) {
  await reportTab(page).click();
  await page.locator('#report-chart-file').selectOption(JSON.stringify(source));
  await page.locator('#report-chart-recognize').click();
}
(async () => {
  const browser = await launch();
  try {
    const linked = await open(browser, '/sn/charts/import?date=2029-09-15#report-chart-import', html, args => {
      if (args.path === '/charts/import/report/files') return args.reply({items: [], next_offset: null});
      return base(args);
    });
    assert.equal(await reportTab(linked.page).getAttribute('aria-selected'), 'true');
    assert.equal(await nativeTab(linked.page).getAttribute('aria-selected'), 'false');
    await linked.page.getByRole('heading', {name: '从往期周报导入常用图'}).waitFor();
    assert.equal(await linked.page.locator('#native-chart-import').isHidden(), true);
    assert.equal(await linked.page.locator('#chart-import-report-intro').isVisible(), true);
    assert.equal(linked.calls.filter(call => call.path === '/charts/import/report/files').length, 1);
    assert.equal(linked.calls.filter(call => call.path === '/charts/import/native/preview').length, 0);
    await reportTab(linked.page).press('ArrowLeft');
    await linked.page.locator('#chart-import-progress').filter({hasText: '已预览 0 / 0'}).waitFor();
    assert.equal(await nativeTab(linked.page).getAttribute('aria-selected'), 'true');
    assert.equal(await nativeTab(linked.page).evaluate(tab => document.activeElement === tab), true);
    assert.equal(await linked.page.locator('#report-chart-import').isHidden(), true);
    assert.equal(await linked.page.locator('#chart-import-native-intro').isVisible(), true);
    assert.match(linked.page.url(), /\?date=2029-09-15$/);
    await nativeTab(linked.page).press('ArrowRight');
    assert.equal(await reportTab(linked.page).getAttribute('aria-selected'), 'true');
    assert.match(linked.page.url(), /\?date=2029-09-15#report-chart-import$/);
    assert.equal(linked.calls.filter(call => call.path === '/charts/import/report/files').length, 1);
    await linked.page.evaluate(() => { window.location.hash = ''; });
    await linked.page.locator('#native-chart-import').waitFor();
    await linked.page.evaluate(() => { window.location.hash = '#report-chart-import'; });
    await linked.page.locator('#report-chart-import').waitFor();
    assert.equal(linked.calls.filter(call => call.path === '/charts/import/native/preview').length, 1);
    await linked.close();

    const immediate = await open(browser, '/sn/charts/import', belowViewport, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path.endsWith('/apply')) return args.reply({created: [{id: 'direct-chart', title: '直接导入'}], failed: []});
      return jobRoutes(args, 'immediate', {charts: [charts[0], charts[2]]});
    });
    await recognize(immediate.page);
    await count(immediate.page, 1, 1);
    await frames(immediate.page);
    assert.equal(await checkbox(immediate.page, 'report-1').isChecked(), true);
    assert.equal(await checkbox(immediate.page, 'report-1').isEnabled(), true);
    assert.equal(await checkbox(immediate.page, 'report-3').isChecked(), false);
    await immediate.page.locator('#report-chart-apply').click();
    await immediate.page.locator('#report-chart-result').filter({hasText: '新建 1 张，失败 0 张'}).waitFor();
    assert.equal(immediate.calls.filter(call => call.path === previewPath).length, 0, '直接导入全程无需预览');
    assert.deepEqual(immediate.calls.find(call => call.path.endsWith('/apply')).body.charts,
      [{chart_id: 'report-1', mappings: ['kb-fixture-series'], chart_type: 'seasonal'}]);
    await count(immediate.page, 0, 0);
    await immediate.close();

    const nearIds = ['range-changed', 'data-arrived', 'manual-unchecked', 'manual-checked', 'mapping-edited'];
    const farId = 'offscreen';
    const lazyCharts = [...nearIds, farId].map((id, i) => ({...charts[0], id, source_index: i, page: i < nearIds.length ? 2 : 3,
      title: id, ready: id !== 'data-arrived', ready_reason: id === 'data-arrived' ? '重画窗口内无观测' : ''}));
    let releasePreviews, releaseEdit, activePreviews = 0, maxActivePreviews = 0;
    const previewGate = new Promise(resolve => { releasePreviews = resolve; });
    const editGate = new Promise(resolve => { releaseEdit = resolve; });
    const lazy = await open(browser, '/sn/charts/import', belowViewport, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path !== previewPath) return jobRoutes(args, 'lazy', {charts: lazyCharts});
      const {chart_id: id, mappings} = args.body;
      const mismatch = id === 'range-changed' || id === 'manual-checked';
      activePreviews++;
      maxActivePreviews = Math.max(maxActivePreviews, activePreviews);
      try {
        await (mappings[0] === 'kb-alternate' ? editGate : previewGate);
        const readyReason = mismatch ? '数值范围与原图刻度不符' : '';
        return await args.reply({definition, payload: cycle(), errors: [], ready: !mismatch, ready_reason: readyReason,
          range_mismatch: mismatch, shape_warnings: mismatch ? [readyReason] : []});
      } finally { activePreviews--; }
    });
    const lazyPage = lazy.page;
    await recognize(lazyPage);
    await count(lazyPage, 5, 5);
    await frames(lazyPage);
    assert.deepEqual(lazy.calls.filter(call => call.path === previewPath), []);
    let started = 0;
    const firstThree = lazyPage.waitForEvent('request', request => request.url().endsWith(previewPath) && ++started === 3);
    await card(lazyPage, nearIds[0]).evaluate(node => node.scrollIntoView({block: 'center'}));
    await firstThree;
    await frames(lazyPage);
    assert.equal(lazy.calls.filter(call => call.path === previewPath).length, 3, '其他可见卡片等待并发槽位');
    await count(lazyPage, 5, 5);
    await checkbox(lazyPage, 'manual-unchecked').uncheck();
    await checkbox(lazyPage, 'manual-checked').uncheck();
    await checkbox(lazyPage, 'manual-checked').check();
    releasePreviews();
    await Promise.all(nearIds.map(id => card(lazyPage, id).locator('[data-report-plot] svg').waitFor()));
    assert.equal(maxActivePreviews, 3);
    assert.deepEqual(lazy.calls.filter(call => call.path === previewPath).map(call => call.body.chart_id).sort(), [...nearIds].sort());
    assert.equal(await checkbox(lazyPage, 'range-changed').isChecked(), false);
    assert.match(await card(lazyPage, 'range-changed').innerText(), /数值范围与原图刻度不符/);
    assert.equal(await checkbox(lazyPage, 'data-arrived').isChecked(), true);
    assert.doesNotMatch(await card(lazyPage, 'data-arrived').innerText(), /重画窗口内无观测/);
    assert.equal(await checkbox(lazyPage, 'manual-unchecked').isChecked(), false);
    assert.equal(await checkbox(lazyPage, 'manual-checked').isChecked(), true);
    await count(lazyPage, 4, 4);

    const edited = card(lazyPage, 'mapping-edited');
    const editRequest = lazyPage.waitForRequest(request => request.url().endsWith(previewPath)
      && request.postDataJSON().mappings[0] === 'kb-alternate');
    await edited.locator('[data-report-mapping]').selectOption('kb-alternate');
    await editRequest;
    assert.equal(await checkbox(lazyPage, 'mapping-edited').isDisabled(), true);
    await count(lazyPage, 3, 3);
    releaseEdit();
    await edited.locator('[data-report-plot] svg').waitFor();
    assert.equal(await checkbox(lazyPage, 'mapping-edited').isEnabled(), true);
    await count(lazyPage, 4, 4);
    assert.equal(lazy.calls.some(call => call.path === previewPath && call.body.chart_id === farId), false);
    await card(lazyPage, farId).locator('[data-report-preview]').evaluate(button => button.click());
    await card(lazyPage, farId).locator('[data-report-plot] svg').waitFor();
    assert.equal(lazy.calls.filter(call => call.path === previewPath && call.body.chart_id === farId).length, 1);
    await lazy.close();

    let applied, jobPolls = 0;
    const previews = [];
    const ui = await open(browser, '/sn/charts/import', html, async args => {
      const {path, body, reply, url} = args;
      if (await base(args) !== false) return;
      if (path === '/charts/import/report/files') {
        assert.equal(url.searchParams.get('limit'), '50');
        assert.equal(url.searchParams.get('search'), '');
        return reply({items: [{citation: source, title: '合成周报.pptx', subtitle: '已解析'}], next_offset: null});
      }
      if (path === '/charts/import/report/recognize') {
        assert.deepEqual(body, {source, model_id: 'model-fixture'}); return reply(meta('fixture', 'queued'), 202);
      }
      if (path === '/analysis-jobs/' + jobID('fixture')) return reply(meta('fixture', ++jobPolls === 1 ? 'running' : 'succeeded'));
      if (path === '/analysis-jobs/' + jobID('fixture') + '/result') return reply(result('fixture', {
        progress: {completed: 2, total: 2, message: '识别完成'}, group: '合成周报.pptx',
        charts: [charts[1], charts[4], charts[3], charts[2], charts[0]],
        source_status_hint: {pending: 7},
        failures: [{page: 5, source_index: 10, reason: '本页识别超时'}, {batch: 3, reason: '本批处理超时'}, {title: '缺少依据的图', reason: '缺少依据片段'}],
      }));
      if (path === '/charts/import/report/preview') {
        previews.push(body);
        if (body.chart_id === 'report-4' && body.mappings[0] !== 'kb-alternate') return reply({errors: ['定义校验不通过']});
        const def = {...definition, title: charts.find(item => item.id === body.chart_id).title, curves: [{...definition.curves[0], series_id: body.mappings[0]}]};
        const payload = body.chart_type === 'seasonal' ? cycle() : {mode: 'recent', curves: [{series_id: body.mappings[0], label: '合成指标', unit: '吨', axis: 'left', points: [{date: '2029-09-01', value: 10}, {date: '2029-09-02', value: 12}]}]};
        const rangeMismatch = body.chart_id === 'report-5';
        return reply({definition: def, payload, possible_duplicate: body.chart_id === 'report-2',
          range_mismatch: rangeMismatch, shape_warnings: rangeMismatch ? charts[4].shape_warnings : [], errors: []});
      }
      if (path === '/charts/import/report/apply') {
        applied = body;
        return reply({created: [{id: 'new-chart-fixture', title: '新建合成图'}], failed: [{chart_id: 'report-2', title: '另一张合成图', reason: '指标已变化，请重新确认'}],
          possible_duplicates: {'report-5': true}});
      }
      return false;
    });
    const p = ui.page;
    assert.equal(await nativeTab(p).getAttribute('aria-selected'), 'true');
    assert.equal(await p.locator('#report-chart-import').isHidden(), true);
    assert.equal(ui.calls.filter(call => call.path.startsWith('/charts/import/report')).length, 0, '原表导入不主动读取周报');
    await reportTab(p).click();
    await p.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await nativeTab(p).click();
    assert.equal(await p.locator('#report-chart-file').isHidden(), true);
    await reportTab(p).click();
    assert.equal(await p.locator('#report-chart-file').inputValue(), JSON.stringify(source));
    assert.equal(ui.calls.filter(call => call.path === '/charts/import/native/preview').length, 1);
    assert.equal(ui.calls.filter(call => call.path === '/charts/import/report/files').length, 1);
    assert.equal(await p.locator('#report-chart-group').inputValue(), '合成周报.pptx');
    await p.locator('#report-chart-recognize').click();
    await p.locator('#report-chart-progress').filter({hasText: '正在识别'}).waitFor();
    await p.locator('[data-report-chart="report-1"] [data-report-plot] svg').waitFor();
    assert.equal(await p.locator('[data-report-chart]').count(), 5);
    const toolbar = p.locator('#report-chart-toolbar'), apply = p.locator('#report-chart-apply');
    assert.equal(await apply.count(), 1);
    assert.equal(await toolbar.evaluate(node => getComputedStyle(node).position), 'sticky');
    assert.equal(await toolbar.evaluate(node => node.nextElementSibling.id), 'report-chart-cards');
    assert.equal(await p.locator('#report-chart-result').evaluate(node => node.previousElementSibling.contains(document.querySelector('#report-chart-apply'))), true);
    await toolbar.evaluate(node => window.scrollTo({top: node.offsetTop + 140, behavior: 'instant'}));
    await p.waitForFunction(() => window.scrollY > 100 && Math.abs(document.querySelector('#report-chart-toolbar').getBoundingClientRect().top) < 2);
    assert.equal(await apply.isVisible(), true);
    assert.equal(await p.locator('#report-chart-cards img').count(), 0);
    assert.equal(await p.evaluate(() => window.__reportXss), undefined);
    const first = p.locator('[data-report-chart="report-1"]'), duplicate = p.locator('[data-report-chart="report-2"]');
    assert.match(await first.innerText(), /第 2 页[\s\S]*原文图例：合成指标/);
    assert.match(await first.innerText(), /原文周期图例：2019、2020/);
    assert.equal(await first.locator('[data-report-shape]').innerText(), '原图：9月起 · 19/20、20/21 · 刻度 20–100');
    assert.match(await first.innerText(), /已按依据位置修正/);
    assert.equal(await first.locator('[data-report-select]').isChecked(), true);
    assert.equal(await duplicate.locator('[data-report-select]').isChecked(), false);
    assert.match(await duplicate.innerText(), /可能重复/);
    assert.match(await duplicate.innerText(), /与第 2 页《定义失败的图》指标和图型相同/);
    const missing = p.locator('[data-report-chart="report-3"]'), failedDefinition = p.locator('[data-report-chart="report-4"]');
    assert.equal(await missing.locator('[data-report-select]').isDisabled(), true);
    assert.equal(await missing.locator('[data-report-select]').isChecked(), false);
    assert.match(await missing.innerText(), /未找到指标：指标库中未找到，请手动选择或跳过/);
    assert.match(await missing.locator('[data-report-missing]').innerText(), /知识库还有 7 张工作表待确认，确认后可重新匹配/);
    assert.equal(await p.locator('#report-chart-rematch').innerText(), '重新匹配未对上的图（1 张）');
    assert.doesNotMatch(await missing.locator('[data-report-flags]').innerText(), /定义失败/);
    assert.equal(previews.some(body => body.chart_id === 'report-3'), false, '缺失指标不发起无效数据预览');
    await failedDefinition.locator('[data-report-flags]').filter({hasText: '定义失败'}).waitFor();
    assert.equal(await failedDefinition.locator('[data-report-select]').isDisabled(), true);
    assert.equal(await failedDefinition.locator('[data-report-select]').isChecked(), false);
    assert.deepEqual(await p.locator('[data-report-page] > h2').allTextContents(), ['第 2 页', '第 3 页', '第 4 页', '第 5 页', '按原文顺序（该文件未提供可靠页码）']);
    assert.equal(await p.locator('[data-report-page="page:2"] [data-report-chart]').count(), 2);
    assert.equal(await p.locator('[data-report-failure]').count(), 3);
    assert.match(await p.locator('#report-chart-failures').innerText(), /第 5 页：本页识别超时/);
    assert.match(await p.locator('#report-chart-failures').innerText(), /第 3 批：本批处理超时/);
    assert.match(await p.locator('#report-chart-failures').innerText(), /缺少依据的图：缺少依据片段/);
    assert.equal(applied, undefined, '识别及预览不自动导入');
    await missing.locator('[data-report-mapping]').selectOption('kb-alternate');
    await missing.locator('[data-report-plot] svg').waitFor();
    assert.equal(await missing.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await missing.locator('[data-report-select]').isChecked(), false, '手动补全指标后仍待研究员勾选');
    assert.equal(await missing.locator('[data-report-missing]').isVisible(), false);
    await failedDefinition.locator('[data-report-mapping]').selectOption('kb-alternate');
    await failedDefinition.locator('[data-report-plot] svg').waitFor();
    assert.equal(await failedDefinition.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await failedDefinition.locator('[data-report-select]').isChecked(), false, '修复初始定义失败仍不自动勾选');
    const rangeMismatch = p.locator('[data-report-chart="report-5"]');
    await rangeMismatch.scrollIntoViewIfNeeded();
    await rangeMismatch.locator('[data-report-plot] svg').waitFor();
    assert.equal(await rangeMismatch.locator('[data-report-select]').isChecked(), false);
    assert.match(await rangeMismatch.locator('[data-report-notice]').innerText(), /数值范围与原图刻度不符/);
    await first.locator('[data-report-mapping]').selectOption('kb-alternate');
    await first.locator('[data-report-plot] svg').waitFor();
    assert.equal(previews.filter(body => body.chart_id === 'report-1').at(-1).mappings[0], 'kb-alternate');
    await first.locator('[data-report-type]').selectOption('trend');
    await first.locator('[data-report-plot] svg').waitFor();
    assert.equal(previews.filter(body => body.chart_id === 'report-1').at(-1).chart_type, 'trend');
    await duplicate.scrollIntoViewIfNeeded();
    await duplicate.locator('[data-report-plot] svg').waitFor();
    assert.equal(await first.locator('[data-report-select]').isChecked(), true);
    await duplicate.locator('[data-report-select]').check();
    await p.locator('#report-chart-group').fill('研究员的常用图');
    await p.locator('#report-chart-apply').click();
    await p.locator('#report-chart-result').filter({hasText: '新建 1 张，失败 1 张'}).waitFor();
    assert.deepEqual(applied, {job_id: jobID('fixture'), group: '研究员的常用图', charts: [
      {chart_id: 'report-1', mappings: ['kb-alternate'], chart_type: 'trend'},
      {chart_id: 'report-2', mappings: ['kb-fixture-series'], chart_type: 'trend'}]});
    assert.equal(await p.locator('#report-chart-result a').getAttribute('href'), prefix + '/sn/charts/new-chart-fixture');
    assert.match(await p.locator('#report-chart-result').innerText(), /指标已变化，请重新确认/);
    assert.equal(await first.locator('[data-report-select]').isDisabled(), true);
    assert.match(await rangeMismatch.locator('[data-report-flags]').innerText(), /可能重复/);
    await ui.close();

    const readonly = await open(browser, '/sn/charts/import', render('chart_import.html', {can_write: false, write_reason: '没有编辑权限'}), base);
    await reportTab(readonly.page).click();
    await readonly.page.locator('#report-chart-status').filter({hasText: '没有编辑权限'}).waitFor();
    assert.equal(await readonly.page.locator('#report-chart-recognize').isDisabled(), true);
    assert.equal(await readonly.page.locator('#report-chart-apply').isDisabled(), true);
    assert.match(await readonly.page.locator('#report-chart-status').innerText(), /没有编辑权限/);
    assert.equal(readonly.calls.filter(call => call.path.startsWith('/charts/import/report')).length, 0);
    await readonly.close();

    const conflict = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      const jobReply = jobRoutes(args, 'conflict', {charts: charts.slice(0, 1)});
      if (jobReply !== false) return jobReply;
      if (args.path.endsWith('/preview')) return args.reply({detail: '文件版本已变化，请重新识别'}, 409);
      return false;
    });
    await reportTab(conflict.page).click();
    await conflict.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await conflict.page.locator('#report-chart-recognize').click();
    await conflict.page.locator('#report-chart-status').filter({hasText: '文件版本已变化'}).waitFor();
    assert.equal(await conflict.page.locator('#report-chart-apply').isDisabled(), true);
    assert.equal(await conflict.page.locator('[data-report-select]').isDisabled(), true);
    await conflict.close();

    let previewAttempt = 0;
    const retry = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      const jobReply = jobRoutes(args, 'retry', {charts: charts.slice(0, 1)});
      if (jobReply !== false) return jobReply;
      if (args.path.endsWith('/preview')) {
        previewAttempt++;
        if (previewAttempt === 1) return new Promise(resolve => setTimeout(() => resolve(args.reply({definition, payload: cycle(), errors: []})), 500));
        return args.reply({definition, payload: cycle(), errors: [], possible_duplicate: false});
      }
      return false;
    });
    // Shorten only the request deadline to exercise an actual aborted preview.
    await retry.page.evaluate(() => { const delay = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...args) => delay(fn, ms === 60000 ? 100 : ms, ...args); });
    await reportTab(retry.page).click();
    await retry.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await retry.page.locator('#report-chart-recognize').click();
    await retry.page.locator('[data-report-notice]').filter({hasText: '请求超时'}).waitFor();
    assert.equal(await retry.page.locator('[data-report-select]').isEnabled(), true);
    assert.equal(await retry.page.locator('[data-report-select]').isChecked(), true);
    assert.equal(await retry.page.locator('[data-report-preview]').isDisabled(), false);
    await retry.page.locator('[data-report-preview]').click();
    await retry.page.locator('[data-report-plot] svg').waitFor();
    assert.equal(await retry.page.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await retry.page.locator('[data-report-select]').isChecked(), true, '预览超时不会取消已就绪的勾选');
    await retry.close();

    let invalidPreview = true;
    const repair = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      const jobReply = jobRoutes(args, 'repair', {charts: charts.slice(0, 1)});
      if (jobReply !== false) return jobReply;
      if (args.path.endsWith('/preview')) return args.reply(invalidPreview ? {errors: ['图表定义校验失败']} : {definition, payload: cycle(), errors: []});
      return false;
    });
    await reportTab(repair.page).click();
    await repair.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await repair.page.locator('#report-chart-recognize').click();
    await repair.page.locator('[data-report-notice]').filter({hasText: '图表定义校验失败'}).waitFor();
    assert.equal(await repair.page.locator('[data-report-select]').isChecked(), false);
    assert.equal(await repair.page.locator('[data-report-select]').isDisabled(), true);
    invalidPreview = false;
    await repair.page.locator('[data-report-preview]').click();
    await repair.page.locator('[data-report-plot] svg').waitFor();
    assert.equal(await repair.page.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await repair.page.locator('[data-report-select]').isChecked(), true, '新预览恢复就绪后重新勾选未手动操作的图');
    await repair.close();

    const duplicateMappingWarning = '重复映射已在定义中去重';
    const mappingChart = {...charts[0], mapping_warnings: [duplicateMappingWarning], series: [charts[0].series[0], {...charts[0].series[0], name: '另一个原文指标'}]};
    const mappingRepair = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      const jobReply = jobRoutes(args, 'mapping', {charts: [mappingChart]});
      if (jobReply !== false) return jobReply;
      if (args.path.endsWith('/preview')) return args.reply({definition, payload: cycle(), errors: [],
        warnings: new Set(args.body.mappings).size < args.body.mappings.length ? [duplicateMappingWarning] : []});
      return false;
    });
    await reportTab(mappingRepair.page).click();
    await mappingRepair.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await mappingRepair.page.locator('#report-chart-recognize').click();
    await mappingRepair.page.locator('[data-report-plot] svg').waitFor();
    assert.match(await mappingRepair.page.locator('[data-report-notice]').innerText(), /重复映射已在定义中去重/);
    await mappingRepair.page.locator('[data-report-mapping]').first().selectOption('kb-alternate');
    await mappingRepair.page.locator('[data-report-plot] svg').waitFor();
    assert.doesNotMatch(await mappingRepair.page.locator('[data-report-notice]').innerText(), /重复映射/);
    assert.match(await mappingRepair.page.locator('[data-report-notice]').innerText(), /已按依据位置修正/, '映射修正保留静态依据提示');
    await mappingRepair.close();
    let returned = false;
    const linkedJob = await open(browser, '/sn/charts/import?analysis_job=' + jobID('deep-link'), html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path === '/analysis-jobs/' + jobID('deep-link')) return args.reply(meta('deep-link', returned ? 'succeeded' : 'running'));
      if (args.path === '/analysis-jobs/' + jobID('deep-link') + '/result') {
        return args.reply(result('deep-link', {charts: [charts[0]], group: '后台识别分组'}));
      }
      return false;
    });
    const restored = linkedJob.page;
    await restored.locator('#report-chart-progress[data-analysis-busy=true]').filter({hasText: '可关闭窗口'}).waitFor();
    await restored.locator('#report-chart-group').fill('研究员先修改分组');
    returned = true;
    await restored.locator('#report-chart-progress').filter({hasText: '编辑内容已改动'}).waitFor();
    assert.equal(await restored.locator('#report-chart-group').inputValue(), '研究员先修改分组');
    assert.equal(await restored.locator('[data-report-chart]').count(), 0);
    assert.equal(linkedJob.calls.some(call => /\/charts\/import\/report\/(recognize|apply)$/.test(call.path)), false);
    await linkedJob.close();

    const rematched = {...charts[2], ready: true, ready_reason: '', missing_indicators: false,
      series: [{...charts[2].series[0], selected_id: 'kb-alternate'}]};
    const detachedRematch = await open(browser, '/sn/charts/import?analysis_job=' + jobID('rematch'), html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [], next_offset: null});
      if (args.path === '/analysis-jobs/' + jobID('rematch')) return args.reply(meta('rematch', 'succeeded'));
      if (args.path === '/analysis-jobs/' + jobID('rematch') + '/result') return args.reply({
        ...result('rematch', {id: jobID('original'), group: '合成周报图组', charts: [charts[0], rematched],
          applied: {'report-1': {id: 'applied-fixture'}}, progress: {message: '重新匹配完成'}}),
        request: {source, model_id: 'model-fixture', rematch_of: jobID('original')},
      });
      if (args.path.endsWith('/preview')) {
        assert.equal(args.body.job_id, jobID('original'));
        assert.equal(args.body.chart_id, rematched.id);
        assert.deepEqual(args.body.mappings, ['kb-alternate']);
        return args.reply({definition, payload: cycle(), errors: []});
      }
      return false;
    });
    const detachedPage = detachedRematch.page;
    await card(detachedPage, rematched.id).scrollIntoViewIfNeeded();
    await detachedPage.locator(`[data-report-chart="${rematched.id}"] [data-report-plot] svg`).waitFor();
    const imported = detachedPage.locator('[data-report-chart="report-1"]');
    assert.equal(await imported.locator('[data-report-select]').isDisabled(), true);
    assert.match(await imported.locator('[data-report-flags]').innerText(), /已导入/);
    assert.equal(await detachedPage.locator('#report-chart-rematch').isHidden(), true);
    assert.deepEqual(detachedRematch.calls.filter(call => call.method === 'POST').map(call => call.path), ['/charts/import/report/preview']);
    await detachedRematch.close();
    console.log('report-chart-import-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
