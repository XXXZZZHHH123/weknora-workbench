/* Isolated browser review flow with a precomputed model result. */
const {assert, launch, open, prefix, render} = require('./chart-ui-fixture.cjs');

const setupId = 'setup-fixture-00001';
const resultPath = `/analysis-jobs/${setupId}/result`;
const query = part => `setup_job=${setupId}&setup_part=${part}`;
const script = html => html.replace('</head>', '<script src="/static/workbench-setup-review.js"></script></head>');
const day = '2029-09-15';
const proposed = {id: 'custom-stock', label: '合成库存维度', category: '库存', sections: [{id: 's-test', label: '合成环节', items: []}]};
const item = {series_id: 'kb-fixture-series', name: '合成指标', dimension: proposed.id,
  section: proposed.sections[0].label, section_id: proposed.sections[0].id, reason: '合成候选理由'};
const result = (part, data) => ({
  job: {id: setupId, purpose: 'workbench_setup', workbench_id: 'fixture', state: 'succeeded'},
  report: {name: '合成往期周报'},
  items: [{part, data, state: 'succeeded'}]
});

async function setupChecklist(browser) {
  const state = result('indicators', {items: [item]});
  state.items[0].counts = {bindings: 1, pending_sections: 0, ignored_count: 3};
  state.items.push(
    {part: 'focus', state: 'failed', message: '本步资料或模型输出未通过校验', detail: '模型返回的指标 ID 不存在于当前目录'},
    {part: 'diagram', state: 'skipped', message: '跳过', detail: '没有可用于产业图的指标'},
    {part: 'strategy', state: 'succeeded', data: {}, counts: {drafts: 1, ignored_count: 0}},
  );
  const html = render('home.html', {}, {affairs: {setup_enabled: true, sections: []}});
  const fixture = await open(browser, prefix + '/?analysis_job=' + setupId, html, ({path, reply}) => {
    if (path === `/analysis-jobs/${setupId}`) return reply(state.job);
    if (path === resultPath) return reply(state);
    if (path === '/monitors') return reply({items: []});
    if (path === '/strategies/models') return reply({models: []});
    return false;
  });
  const review = fixture.page.locator('#workbench-setup-review');
  await review.waitFor();
  const row = label => review.locator('li').filter({hasText: label});
  assert.match(await row('维度绑定指标').textContent(), /绑定指标 1 个，待确认环节 0 个，已忽略 3 项/);
  assert.match(await row('核心关注').textContent(), /本步资料或模型输出未通过校验：模型返回的指标 ID 不存在于当前目录/);
  assert.match(await row('产业图骨架').textContent(), /跳过：没有可用于产业图的指标/);
  assert.doesNotMatch(await row('策略草稿').textContent(), /已忽略/);
  assert.equal(await row('核心关注').getByRole('link').count(), 0);
  assert.equal(await row('产业图骨架').getByRole('link').count(), 0);
  assert.match(await row('维度绑定指标').getByRole('link').getAttribute('href'), new RegExp(query('indicators')));
  await fixture.close();
}

async function dimensionReview(browser) {
  const stored = {dimensions: [], revision: 0, can_write: true, categories: [{id: '库存', label: '库存'}]};
  const page = script(render('dimensions.html', {}, {dimension_page: {date: day}}));
  let attemptedSave;
  const fixture = await open(browser, prefix + '/sn/dimensions?' + query('indicators'), page, ({path, body, reply}) => {
    if (path === '/dimensions') return reply(stored);
    if (path === '/dimensions/data') return reply({dimensions: [], revision: 0});
    if (path === resultPath) return reply(result('indicators', {dimensions: [proposed], items: [item], sections: []}));
    if (path === '/dimensions/from-setup/' + setupId) { attemptedSave = body; return reply({detail: '本次保存仅捕获请求'}, 409); }
    return false;
  });
  const p = fixture.page;
  await p.locator('#dimensions-model-dialog[open]').waitFor();
  assert.equal(await p.locator('#dimensions-model-title').textContent(), '核对周报绑定指标');
  assert.equal(await p.getByLabel('建议指标的维度').locator('option:checked').textContent(), proposed.label);
  assert.equal(await p.getByLabel('建议指标的环节').inputValue(), item.section);
  assert.equal(await p.locator('#dimensions-model-run').isVisible(), false);
  await p.locator('#dimensions-model-adopt').click();
  assert.equal(await p.locator('.dimensions-edit-card').count(), 1);
  assert.ok(fixture.calls.every(call => call.method === 'GET'));
  await p.locator('#dimensions-save').click();
  await p.locator('#dimensions-status').filter({hasText: '本次保存仅捕获请求'}).waitFor();
  assert.equal(attemptedSave.dimensions[0].id, proposed.id);
  assert.equal(attemptedSave.dimensions[0].sections[0].items[0].id, item.series_id);
  await fixture.close();
}

async function layoutReview(browser) {
  const current = {id: 'custom-demand', label: '合成需求维度'};
  const pending = {id: 'custom-pending', label: '尚未保存的维度'};
  const title = '合成指标图';
  const panels = Object.fromEntries(['summary', 'research', 'dashboard', 'watch'].map(key => [key, {visible: true, text: ''}]));
  const draft = {title: '本期研究', period: 'weekly', date: day, sections: panels, figures: [], references: []};
  const report = {coverage: {}, counts: {observations: 0}, strategy_overview: {triggered: []}, panels, report_figures: [], automatic_figures: [],
    figure_options: [{series_id: item.series_id, name: title}], editable_dimensions: [current, proposed], editable_draft: draft};
  const page = script(render('reports.html', {}, {report, live_report: true, files: [], selected: {date: day}, period_kind: 'weekly'}));
  let saved;
  const fixture = await open(browser, prefix + '/sn/reports?period=weekly&' + query('layout'), page, ({path, body, reply}) => {
    if (path === resultPath) return reply(result('layout', {dimensions: [proposed, current, pending], groups: [
      {id: pending.id, label: pending.label, figures: [{title, series_id: item.series_id}]}]}));
    if (path === '/research-references' || path === '/judgment/models') return reply({items: [], models: []});
    if (path === '/report-drafts') { saved = body; return reply({detail: '本次保存仅捕获请求'}, 409); }
    return false;
  });
  const p = fixture.page, layout = p.locator('#report-layout-editor[open]');
  await layout.waitFor();
  assert.match(await layout.locator('ol > li').first().textContent(), /合成库存维度/);
  assert.match(await layout.textContent(), /待先保存研究维度/);
  assert.equal(await p.getByLabel(title + '的证据图分组').inputValue(), pending.id);
  assert.equal(await p.locator('#report-edit-summary').inputValue(), '');
  assert.equal(saved, undefined);
  await p.getByLabel(title + '的证据图分组').selectOption(current.id);
  await p.locator('#report-edit-save').click();
  await p.locator('#report-edit-status').filter({hasText: '本次保存仅捕获请求'}).waitFor();
  assert.deepEqual(saved.draft.layout, {dimension_order: [proposed.id, current.id], figure_groups: {[item.series_id]: current.id}});
  assert.deepEqual(saved.draft.figures, []);
  await fixture.close();
}

async function explicitDimensionReview(browser) {
  const job = {id: 'inference-job-00001', purpose: 'dimension_inference', workbench_id: 'fixture', state: 'succeeded'};
  const url = prefix + '/sn/dimensions?' + query('indicators') + '&analysis_job=' + job.id;
  const page = script(render('dimensions.html', {}, {dimension_page: {date: day}}));
  const fixture = await open(browser, url, page, ({path, reply}) => {
    if (path === '/dimensions') return reply({dimensions: [], revision: 0, can_write: true, categories: []});
    if (path === '/dimensions/data') return reply({dimensions: [], revision: 0});
    if (path === '/strategies/models' || path === '/dimensions/sources') return reply({models: [], items: []});
    if (path === '/analysis-jobs/' + job.id) return reply(job);
    if (path === '/analysis-jobs/' + job.id + '/result') return reply({job, dimensions: [proposed]});
    return false;
  });
  const dialog = fixture.page.locator('#dimensions-model-dialog');
  await dialog.getByText(proposed.label).waitFor();
  assert.equal(await dialog.locator('#dimensions-model-title').textContent(), '从往期周报归纳维度');
  assert.ok(fixture.calls.every(call => !call.path.includes(setupId)));
  await fixture.close();
}

async function chartReview(browser) {
  const page = script(render('chart_import.html'));
  const source = {kind: 'knowledge_file', knowledge_id: 'weekly', source_binding_id: 'fixture', file_version: 'a'.repeat(64)};
  const chart = {id: 'setup-chart', title: '合成周报图', chart_type: 'trend',
    series: [{name: item.name, selected_id: item.series_id, candidates: [{id: item.series_id, name: item.name}]}]};
  const alternate = {id: 'kb-alternate', name: '备选合成指标'};
  const missing = {id: 'unmatched-chart', title: '待匹配的周报图', chart_type: 'trend', missing_indicators: true,
    series: [{name: '待核对的原文图例', selected_id: null, candidates: [alternate]},
      {name: item.name, selected_id: null, candidates: []}]};
  const changed = {...missing, missing_indicators: false, series: [
    {...missing.series[0], selected_id: item.series_id, candidates: [...chart.series[0].candidates, alternate]}, chart.series[0]]};
  const rematchJob = (id, state) => ({id, purpose: 'report_chart_import', workbench_id: 'fixture', state});
  const failedId = 'rematch-failed-00001', successId = 'rematch-success-00001';
  let preview, rematchAttempts = 0;
  const fixture = await open(browser, prefix + '/sn/charts/import?' + query('charts'), page, ({path, body, reply}) => {
    if (path === '/charts/import/report/files') return reply({items: [], next_offset: null});
    if (path === '/strategies/models') return reply({models: [{id: 'model-fixture', name: '合成模型'}]});
    if (path === resultPath) return reply(result('charts', {
      job_id: setupId, group: '合成周报图组', source, model_id: 'model-fixture', charts: [chart, missing]}));
    if (path === '/charts/import/report/preview') {
      preview = body;
      return reply({errors: ['本次预览仅捕获请求']});
    }
    if (path === '/charts/import/report/rematch') {
      assert.deepEqual(body, {job_id: setupId, model_id: 'model-fixture'});
      return reply(rematchJob(++rematchAttempts === 1 ? failedId : successId, 'queued'), 202);
    }
    if (path === '/analysis-jobs/' + failedId) return reply({...rematchJob(failedId, 'failed'), message: '合成重新匹配失败'});
    if (path === '/analysis-jobs/' + successId) return reply(rematchJob(successId, 'succeeded'));
    if (path === '/analysis-jobs/' + successId + '/result') return reply({id: setupId, job: rematchJob(successId, 'succeeded'),
      request: {source, model_id: 'model-fixture', rematch_of: setupId}, group: '合成周报图组', charts: [chart, changed],
      progress: {message: '重新匹配完成'}});
    return false;
  });
  const p = fixture.page;
  await p.getByText('本次预览仅捕获请求').waitFor();
  assert.equal(await p.locator('#report-chart-group').inputValue(), '合成周报图组');
  assert.deepEqual(preview, {job_id: setupId, group: '合成周报图组', chart_id: chart.id,
    mappings: [item.series_id], chart_type: 'trend'});
  const rematched = p.locator(`[data-report-chart="${missing.id}"]`);
  const mappings = rematched.locator('[data-report-mapping]');
  await mappings.first().selectOption(alternate.id);
  await rematched.locator('[data-report-type]').selectOption('seasonal');
  const rematch = p.getByRole('button', {name: '重新匹配未对上的图（1 张）'});
  await rematch.click();
  await p.getByText('合成重新匹配失败').waitFor();
  assert.equal(await mappings.last().inputValue(), '');
  await rematch.click();
  await p.getByText('重新匹配完成').waitFor();
  await rematched.getByText('本次预览仅捕获请求').waitFor();
  assert.equal(await mappings.first().inputValue(), alternate.id);
  assert.equal(await mappings.last().inputValue(), item.series_id);
  assert.equal(await p.locator('#report-chart-rematch').isHidden(), true);
  assert.deepEqual(fixture.calls.filter(call => call.path.endsWith('/preview')).map(call => call.body.chart_id), [chart.id, missing.id]);
  assert.deepEqual(preview, {job_id: setupId, group: '合成周报图组', chart_id: missing.id,
    mappings: [alternate.id, item.series_id], chart_type: 'seasonal'});
  assert.deepEqual(fixture.calls.filter(call => call.method !== 'GET').map(call => call.path), [
    '/charts/import/report/preview', '/charts/import/report/rematch', '/charts/import/report/rematch', '/charts/import/report/preview']);
  await fixture.close();
}

(async () => {
  const browser = await launch();
  try {
    await setupChecklist(browser);
    await dimensionReview(browser);
    await layoutReview(browser);
    await explicitDimensionReview(browser);
    await chartReview(browser);
  }
  finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
