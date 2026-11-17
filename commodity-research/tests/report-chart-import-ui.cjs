const {assert, render, launch, open, definition, cycle, prefix} = require('./chart-ui-fixture.cjs');
const source = {kind: 'knowledge_file', source_binding_id: 'binding-fixture', knowledge_id: 'report-fixture', file_version: 3};
const candidates = [{id: 'kb-fixture-series', name: '合成指标', unit: '吨', sheet: '数据表'}, {id: 'kb-alternate', name: '另一个真实候选', unit: '吨'}];
const charts = [
  {id: 'report-1', page: 2, source_index: 1, title: '<img src=x onerror=window.__reportXss=1>', chart_type: 'seasonal', legend_semantics: 'periods', evidence: '原文图例：合成指标', warnings: ['模型页码与依据不一致，已按依据位置修正'], series: [{name: '合成指标', legends: ['2019', '2020'], unit: '吨', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-2', page: 3, source_index: 5, title: '另一张合成图', chart_type: 'trend', evidence: '另一张图的图例', possible_duplicate: true, series: [{name: '图例二', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-3', page: 4, source_index: 6, title: '缺少指标的图', chart_type: 'comparison', evidence: '待核对的图例片段', missing_indicators: true, series: [{name: '待核对图例', candidates, selected_id: null}]},
  {id: 'report-4', page: 2, source_index: 1, title: '定义失败的图', chart_type: 'trend', evidence: '原文图例', definition_failed: true, errors: ['定义校验不通过'], series: [{name: '原文图例', candidates, selected_id: 'kb-fixture-series'}]},
  {id: 'report-5', page: null, page_label: '片段9', source_index: 8, title: '无页码图', chart_type: 'trend', evidence: '无页码片段', series: [{name: '原文图例', candidates, selected_id: 'kb-fixture-series'}]}
];
const html = render('chart_import.html');
const base = ({path, reply}) => {
  if (path === '/charts/import/native/preview') return reply({total: 0, next_offset: null, sheets: []});
  if (path === '/strategies/models') return reply({models: [{id: 'model-fixture', name: '测试模型'}]});
  return false;
};
(async () => {
  const browser = await launch();
  try {
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
      if (path === '/charts/import/report/recognize') { assert.deepEqual(body, {source, model_id: 'model-fixture'}); return reply({job_id: 'job-fixture'}); }
      if (path === '/charts/import/report/jobs/job-fixture') {
        jobPolls++;
        return reply(jobPolls === 1 ? {status: 'running', progress: {completed: 1, total: 2, message: '正在识别'}} :
          {status: 'complete', progress: {completed: 2, total: 2, message: '识别完成'}, group: '合成周报.pptx', charts: [charts[1], charts[4], charts[3], charts[2], charts[0]],
            failures: [{page: 5, source_index: 10, reason: '本页识别超时'}, {batch: 3, reason: '本批处理超时'}, {title: '缺少依据的图', reason: '缺少依据片段'}]});
      }
      if (path === '/charts/import/report/preview') {
        previews.push(body);
        if (body.chart_id === 'report-4' && body.mappings[0] !== 'kb-alternate') return reply({errors: ['定义校验不通过']});
        const def = {...definition, title: charts.find(item => item.id === body.chart_id).title, curves: [{...definition.curves[0], series_id: body.mappings[0]}]};
        const payload = body.chart_type === 'seasonal' ? cycle() : {mode: 'recent', curves: [{series_id: body.mappings[0], label: '合成指标', unit: '吨', axis: 'left', points: [{date: '2029-09-01', value: 10}, {date: '2029-09-02', value: 12}]}]};
        return reply({definition: def, payload, possible_duplicate: body.chart_id === 'report-2', errors: []});
      }
      if (path === '/charts/import/report/apply') {
        applied = body;
        return reply({created: [{id: 'new-chart-fixture', title: '新建合成图'}], failed: [{chart_id: 'report-2', title: '另一张合成图', reason: '指标已变化，请重新确认'}]});
      }
      return false;
    });
    const p = ui.page;
    assert.equal(await p.locator('#report-chart-import').getAttribute('open'), null);
    assert.equal(ui.calls.filter(call => call.path.startsWith('/charts/import/report')).length, 0, '原表导入不主动读取周报');
    await p.locator('#report-chart-import > summary').click();
    await p.locator('#report-chart-file').selectOption(JSON.stringify(source));
    assert.equal(await p.locator('#report-chart-group').inputValue(), '合成周报.pptx');
    await p.locator('#report-chart-recognize').click();
    await p.locator('#report-chart-progress').filter({hasText: '正在识别'}).waitFor();
    await p.locator('[data-report-chart="report-1"] [data-report-plot] svg').waitFor();
    assert.equal(await p.locator('[data-report-chart]').count(), 5);
    assert.equal(await p.locator('#report-chart-cards img').count(), 0);
    assert.equal(await p.evaluate(() => window.__reportXss), undefined);
    const first = p.locator('[data-report-chart="report-1"]'), duplicate = p.locator('[data-report-chart="report-2"]');
    assert.match(await first.innerText(), /第 2 页[\s\S]*原文图例：合成指标/);
    assert.match(await first.innerText(), /原文周期图例：2019、2020/);
    assert.match(await first.innerText(), /已按依据位置修正/);
    assert.equal(await first.locator('[data-report-select]').isChecked(), true);
    assert.equal(await duplicate.locator('[data-report-select]').isChecked(), false);
    assert.match(await duplicate.innerText(), /可能重复/);
    const missing = p.locator('[data-report-chart="report-3"]'), failedDefinition = p.locator('[data-report-chart="report-4"]');
    assert.equal(await missing.locator('[data-report-select]').isDisabled(), true);
    assert.equal(await missing.locator('[data-report-select]').isChecked(), false);
    assert.match(await missing.innerText(), /未找到指标：指标库中未找到，请手动选择或跳过/);
    assert.doesNotMatch(await missing.locator('[data-report-flags]').innerText(), /定义失败/);
    assert.equal(previews.some(body => body.chart_id === 'report-3'), false, '缺失指标不发起无效数据预览');
    await failedDefinition.locator('[data-report-flags]').filter({hasText: '定义失败'}).waitFor();
    assert.equal(await failedDefinition.locator('[data-report-select]').isDisabled(), true);
    assert.equal(await failedDefinition.locator('[data-report-select]').isChecked(), false);
    assert.deepEqual(await p.locator('[data-report-page] > h2').allTextContents(), ['第 2 页', '第 3 页', '第 4 页', '片段9', '第 5 页', '第 3 批', '未定位依据']);
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
    await p.locator('[data-report-chart="report-5"] [data-report-select]').uncheck();
    await first.locator('[data-report-mapping]').selectOption('kb-alternate');
    await p.waitForFunction(() => document.querySelector('[data-report-chart="report-1"] [data-report-select]').checked);
    assert.equal(previews.at(-1).mappings[0], 'kb-alternate');
    await first.locator('[data-report-type]').selectOption('trend');
    await p.waitForFunction(() => document.querySelector('[data-report-chart="report-1"] [data-report-select]').checked);
    assert.equal(previews.at(-1).chart_type, 'trend');
    await duplicate.locator('[data-report-select]').check();
    await p.locator('#report-chart-group').fill('研究员的常用图');
    await p.locator('#report-chart-apply').click();
    await p.locator('#report-chart-result').filter({hasText: '新建 1 张，失败 1 张'}).waitFor();
    assert.deepEqual(applied, {job_id: 'job-fixture', group: '研究员的常用图', charts: [
      {chart_id: 'report-1', mappings: ['kb-alternate'], chart_type: 'trend'},
      {chart_id: 'report-2', mappings: ['kb-fixture-series'], chart_type: 'trend'}]});
    assert.equal(await p.locator('#report-chart-result a').getAttribute('href'), prefix + '/sn/charts/new-chart-fixture');
    assert.match(await p.locator('#report-chart-result').innerText(), /指标已变化，请重新确认/);
    assert.equal(await first.locator('[data-report-select]').isDisabled(), true);
    await ui.close();

    const readonly = await open(browser, '/sn/charts/import', render('chart_import.html', {can_write: false, write_reason: '没有编辑权限'}), base);
    await readonly.page.locator('#report-chart-import > summary').click();
    await readonly.page.locator('#report-chart-status').filter({hasText: '没有编辑权限'}).waitFor();
    assert.equal(await readonly.page.locator('#report-chart-recognize').isDisabled(), true);
    assert.equal(await readonly.page.locator('#report-chart-apply').isDisabled(), true);
    assert.match(await readonly.page.locator('#report-chart-status').innerText(), /没有编辑权限/);
    assert.equal(readonly.calls.filter(call => call.path.startsWith('/charts/import/report')).length, 0);
    await readonly.close();

    const conflict = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path.endsWith('/recognize')) return args.reply({job_id: 'job-conflict'});
      if (args.path.endsWith('/jobs/job-conflict')) return args.reply({status: 'complete', charts: charts.slice(0, 1)});
      if (args.path.endsWith('/preview')) return args.reply({detail: '文件版本已变化，请重新识别'}, 409);
      return false;
    });
    await conflict.page.locator('#report-chart-import > summary').click();
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
      if (args.path.endsWith('/recognize')) return args.reply({job_id: 'job-retry'});
      if (args.path.endsWith('/jobs/job-retry')) return args.reply({status: 'complete', charts: charts.slice(0, 1)});
      if (args.path.endsWith('/preview')) {
        previewAttempt++;
        if (previewAttempt === 1) return new Promise(resolve => setTimeout(() => resolve(args.reply({definition, payload: cycle(), errors: []})), 500));
        return args.reply({definition, payload: cycle(), errors: [], possible_duplicate: false});
      }
      return false;
    });
    // Shorten only the request deadline to exercise an actual aborted preview.
    await retry.page.evaluate(() => { const delay = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...args) => delay(fn, ms === 60000 ? 100 : ms, ...args); });
    await retry.page.locator('#report-chart-import > summary').click();
    await retry.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await retry.page.locator('#report-chart-recognize').click();
    await retry.page.locator('[data-report-notice]').filter({hasText: '请求超时'}).waitFor();
    assert.equal(await retry.page.locator('[data-report-select]').isDisabled(), true);
    assert.equal(await retry.page.locator('[data-report-preview]').isDisabled(), false);
    await retry.page.locator('[data-report-preview]').click();
    await retry.page.locator('[data-report-plot] svg').waitFor();
    assert.equal(await retry.page.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await retry.page.locator('[data-report-select]').isChecked(), false, '预览失败后重试成功仍需手动勾选');
    await retry.page.locator('[data-report-select]').check();
    assert.equal(await retry.page.locator('[data-report-select]').isChecked(), true);
    await retry.close();

    let invalidPreview = true;
    const repair = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path.endsWith('/recognize')) return args.reply({job_id: 'job-repair'});
      if (args.path.endsWith('/jobs/job-repair')) return args.reply({status: 'complete', charts: charts.slice(0, 1)});
      if (args.path.endsWith('/preview')) return args.reply(invalidPreview ? {errors: ['图表定义校验失败']} : {definition, payload: cycle(), errors: []});
      return false;
    });
    await repair.page.locator('#report-chart-import > summary').click();
    await repair.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await repair.page.locator('#report-chart-recognize').click();
    await repair.page.locator('[data-report-notice]').filter({hasText: '图表定义校验失败'}).waitFor();
    assert.equal(await repair.page.locator('[data-report-select]').isChecked(), false);
    assert.equal(await repair.page.locator('[data-report-select]').isDisabled(), true);
    invalidPreview = false;
    await repair.page.locator('[data-report-preview]').click();
    await repair.page.locator('[data-report-plot] svg').waitFor();
    assert.equal(await repair.page.locator('[data-report-select]').isDisabled(), false);
    assert.equal(await repair.page.locator('[data-report-select]').isChecked(), false, '运行时定义校验失败后修复也不自动勾选');
    await repair.close();

    const duplicateMappingWarning = '重复映射已在定义中去重';
    const mappingChart = {...charts[0], mapping_warnings: [duplicateMappingWarning], series: [charts[0].series[0], {...charts[0].series[0], name: '另一个原文指标'}]};
    const mappingRepair = await open(browser, '/sn/charts/import', html, async args => {
      if (await base(args) !== false) return;
      if (args.path.endsWith('/files')) return args.reply({items: [{citation: source, title: '合成周报.pdf'}], next_offset: null});
      if (args.path.endsWith('/recognize')) return args.reply({job_id: 'job-mapping'});
      if (args.path.endsWith('/jobs/job-mapping')) return args.reply({status: 'complete', charts: [mappingChart]});
      if (args.path.endsWith('/preview')) return args.reply({definition, payload: cycle(), errors: [],
        warnings: new Set(args.body.mappings).size < args.body.mappings.length ? [duplicateMappingWarning] : []});
      return false;
    });
    await mappingRepair.page.locator('#report-chart-import > summary').click();
    await mappingRepair.page.locator('#report-chart-file').selectOption(JSON.stringify(source));
    await mappingRepair.page.locator('#report-chart-recognize').click();
    await mappingRepair.page.locator('[data-report-plot] svg').waitFor();
    assert.match(await mappingRepair.page.locator('[data-report-notice]').innerText(), /重复映射已在定义中去重/);
    await mappingRepair.page.locator('[data-report-mapping]').first().selectOption('kb-alternate');
    await mappingRepair.page.locator('[data-report-plot] svg').waitFor();
    assert.doesNotMatch(await mappingRepair.page.locator('[data-report-notice]').innerText(), /重复映射/);
    assert.match(await mappingRepair.page.locator('[data-report-notice]').innerText(), /已按依据位置修正/, '映射修正保留静态依据提示');
    await mappingRepair.close();
    console.log('report-chart-import-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
