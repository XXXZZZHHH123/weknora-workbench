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
  let preview;
  const fixture = await open(browser, prefix + '/sn/charts/import?' + query('charts'), page, ({path, body, reply}) => {
    if (path === '/charts/import/report/files') return reply({items: [], next_offset: null});
    if (path === '/strategies/models') return reply({models: [{id: 'model-fixture', name: '合成模型'}]});
    if (path === resultPath) return reply(result('charts', {
      job_id: setupId, group: '合成周报图组', source, model_id: 'model-fixture', charts: [chart]}));
    if (path === '/charts/import/report/preview') {
      preview = body;
      return reply({errors: ['本次预览仅捕获请求']});
    }
    return false;
  });
  const p = fixture.page;
  await p.getByText('本次预览仅捕获请求').waitFor();
  assert.equal(await p.locator('#report-chart-group').inputValue(), '合成周报图组');
  assert.deepEqual(preview, {job_id: setupId, group: '合成周报图组', chart_id: chart.id,
    mappings: [item.series_id], chart_type: 'trend'});
  assert.deepEqual(fixture.calls.filter(call => call.method !== 'GET').map(call => call.path), ['/charts/import/report/preview']);
  await fixture.close();
}

(async () => {
  const browser = await launch();
  try {
    await dimensionReview(browser);
    await layoutReview(browser);
    await explicitDimensionReview(browser);
    await chartReview(browser);
  }
  finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
