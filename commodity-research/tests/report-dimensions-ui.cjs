/* The actual report template and shipped editor, exercised without a running service. */
const {assert, render, launch, open, prefix} = require('./chart-ui-fixture.cjs');
const day = '2029-09-15';
const longName = '备注：合成数据中的原表说明与指标口径'.repeat(12);
const dimensions = [{id: 'custom-demand', label: '下游跟踪'}, {id: 'custom-stock', label: '库存观察'}, {id: 'custom-empty', label: '备选维度'}];
const figure = {key: 'indicator:series-one', kind: 'indicator', series_id: 'series-one', title: '合成证据图', unit: '吨', as_of: day, caption: '保存引用', conclusion: '原图结论'};
function report() {
  const panels = Object.fromEntries(['summary', 'research', 'dashboard', 'watch'].map(key => [key, {visible: true, text: key === 'summary' ? '旧草稿导语' : '', references: []}]));
  const draft = {format: 'research-report-draft.v1', title: '合成一页周报', date: day, period: 'weekly', references: [], figures: [{series_id: 'series-one', caption: '保存引用'}],
    sections: Object.fromEntries(Object.entries(panels).map(([key, panel]) => [key, {visible: true, text: panel.text}])),
    dimension_notes: {'custom-stock': {text: '研究员原句', impact: 'bearish'}}, figure_conclusions: {'indicator:series-one': '原图结论'}};
  return {report_title: draft.title, coverage: {}, panels, is_manual_report: true, manual_report: {id: 'saved-report', revision: 2},
    briefs: [], judgment: null, conclusion: null, judgment_replay: null, research_cards: [], strategies: [], strategy_events: [], changes: [], gaps: [], derived: [], observations: [], counts: {observations: 0, derived: 0},
    strategy_overview: {text: '暂无策略', triggered: []}, selected_figures: draft.figures, report_figures: [figure], automatic_figures: [], figure_options: [{series_id: 'series-one', name: figure.title}],
    dimension_overview: [
      {id: 'custom-demand', label: '下游跟踪', facts: [
        {name: '合成指标', text: '合成指标 12 吨（较 09-08 +2，近 4 年同期第 1 高）'},
        {name: longName, text: longName + ' 86 吨'}
      ], sentence: '', impact: 'undecided', support_count: 0, oppose_count: 1, evidence: [{side: 'bear', text: '下游原文依据'}]},
      {id: 'custom-stock', label: '库存观察', change_summary: '本期无新数据（最新 2029-03-10）', facts: [
        {text: '合成库存 42 吨（本期无新数据，最新 2029-03-10）', latest_text: '合成库存 42 吨'},
        {text: '合成周转 8 天（本期无新数据，最新 2029-03-09）', latest_text: '合成周转 8 天'}
      ], warning_summary: '2 项停更（最长 190 天）', warnings: ['合成库存：最新数据距今 189 天，可能已停更', '合成周转：最新数据距今 190 天，可能已停更', '历史观测达到读取上限，同期位置可能受限'],
      sentence: '研究员原句', impact: 'bearish', support_count: 2, oppose_count: 0, review_needed: true, evidence: [{side: 'bull', text: '库存原文依据'}]}
    ], editable_dimensions: dimensions,
    logic_strategy: {contradiction: '合成主要矛盾', tone: '观察 · 保存基调', conditions: [{dimension_label: '下游跟踪', name: '合成阈值', condition: '> 12', state: '未触发', current_value_text: '10'}], strategies: [{text: '观察；保存逻辑；触发：保存条件；应对：继续观察'}]},
    evidence_groups: [{id: 'other', label: '其他', figures: [figure]}], editable_draft: draft};
}
const html = (config = {}, value = report(), period = 'weekly') => render('reports.html', config, {report: value, live_report: true, files: [], selected: {date: day}, period_kind: period, report_start: '2029-09-10', preview: '{}', restricted_data: {briefs: []}});
(async () => {
  const browser = await launch();
  try {
    let saved, generated = 0, received;
    const fixture = await open(browser, prefix + '/sn/reports', html(), async ({path, body, reply}) => {
      if (path === '/research-references') return reply({items: []});
      if (path === '/judgment/models') return reply({models: [{id: 'synthetic', name: '合成模型'}]});
      if (path.startsWith('/reports/figures/')) return reply({figure, status: 'unavailable', notice: '隔离夹具未绘图'});
      if (path.endsWith('/draft-dimensions')) {
        received = body; generated++;
        if (generated === 2) return reply({dimensions: [], summary: {text: '', status: 'numbers_unverified', reason: '总体数字待核对', suggestion: '合成数字999。'}});
        if (generated === 3) return reply({detail: '合成模型调用失败'}, 503);
        return reply({dimensions: [
          {id: 'custom-demand', text: '合成指标变为12吨，较上期增加2吨，建议继续观察。', impact: 'neutral', status: 'ok'},
          {id: 'custom-stock', text: '', suggestion: '虚构数字999', impact: 'bullish', status: 'numbers_unverified', reason: '数字待核对'}
        ], figures: [{key: figure.key, text: '合成图结论草稿', status: 'ok'}],
        summary: {text: '本周维持观察。后续关注下游能否改善。', status: 'ok'}});
      }
      if (path === '/report-drafts/saved-report') { saved = body; return reply({detail: '已捕获保存请求'}, 409); }
      return false;
    });
    const p = fixture.page;
    assert.deepEqual(await p.locator('#report-readable > section').evaluateAll(nodes => nodes.map(node => node.id)), ['report-overview', 'report-logic-strategy', 'report-dashboard']);
    assert.deepEqual(await p.locator('[data-report-dimension]').evaluateAll(nodes => nodes.map(node => node.dataset.reportDimension)), ['custom-demand', 'custom-stock']);
    assert.equal(await p.locator('#report-overview').getByText('备选维度').count(), 0);
    assert.match(await p.locator('#report-overview').innerText(), /12 吨.*09-08.*近 4 年/);
    assert.deepEqual(await p.locator('.report-overview-table thead th').allTextContents(), ['维度', '本期变化', '本期结论', '影响', '依据计数']);
    const overview = p.locator('#report-overview');
    assert.match(await overview.innerText(), /本周结论.*旧草稿导语.*本期变化/s);
    const longFact = p.locator('.report-fact-name').filter({hasText: longName});
    assert.equal(await longFact.getAttribute('title'), longName);
    assert.match(await longFact.locator('..').innerText(), /86 吨$/);
    assert.equal(await longFact.evaluate(node => {
      const style = getComputedStyle(node);
      return style.webkitLineClamp === '2' && node.clientHeight <= 2 * parseFloat(style.lineHeight) + 1
        && node.scrollHeight > node.clientHeight;
    }), true, '长指标名截断在两行内，数值仍可见');
    const stock = p.locator('[data-report-dimension="custom-stock"]');
    assert.equal(await stock.locator('.report-change-summary').innerText(), '本期无新数据（最新 2029-03-10）');
    assert.deepEqual(await stock.locator('.report-latest-reference').allTextContents(), ['合成库存 42 吨', '合成周转 8 天']);
    assert.equal((await stock.innerText()).match(/本期无新数据/g).length, 1, '无更新说明在维度内只显示一次');
    assert.doesNotMatch(await stock.innerText(), /较.*[+＋]0|持平|最新数据距今|历史观测达到读取上限/);
    assert.equal(await stock.locator('.report-dimension-notices').evaluate(node => node.open), false);
    assert.match(await stock.locator('.report-dimension-notices > summary').innerText(), /2 项停更（最长 190 天）/);
    await stock.locator('.report-dimension-notices > summary').click();
    assert.match(await stock.innerText(), /最新数据距今 190 天.*历史观测达到读取上限/s);
    await stock.locator('.report-dimension-notices > summary').click();
    assert.equal(await stock.locator('.report-impact').innerText(), '偏空');
    assert.notEqual(await stock.locator('.report-impact').evaluate(node => getComputedStyle(node).backgroundColor), 'rgba(0, 0, 0, 0)');
    assert.equal(await p.locator('.report-overview-table').evaluate(node => node.getBoundingClientRect().width <= node.parentElement.clientWidth), true, '桌面综述表不横向溢出');
    assert.match(await p.locator('#report-logic-strategy').innerText(), /主要矛盾.*合成主要矛盾/s);
    assert.equal(await p.locator('#report-full-record').evaluate(node => node.open), false);
    assert.doesNotMatch(await p.locator('#report-full-record').textContent(), /旧草稿导语/);
    assert.equal(await p.locator('[data-evidence-dimension="other"] > h4').innerText(), '其他');
    assert.equal(await p.locator('.report-figure-conclusion').innerText(), '原图结论');
    assert.match(await p.locator('[data-report-dimension="custom-stock"] .report-review-warning').getAttribute('href'), /\/sn\/judgment$/);
    await stock.locator('.report-dimension-evidence > summary').click();
    assert.match(await p.locator('[data-report-dimension="custom-stock"]').innerText(), /库存原文依据/);
    await p.locator('#report-edit-open').click();
    const summaryInput = p.getByLabel('本周结论', {exact: true});
    assert.equal(await summaryInput.inputValue(), '旧草稿导语');
    assert.equal(await summaryInput.getAttribute('maxlength'), '4000');
    assert.deepEqual(await p.locator('[data-edit-dimension]').evaluateAll(nodes => nodes.map(node => node.dataset.editDimension)), dimensions.map(row => row.id));
    assert.equal(await p.getByLabel('库存观察的本期结论').inputValue(), '研究员原句');
    assert.equal(await p.getByLabel('库存观察的影响').inputValue(), 'bearish');
    assert.equal(await p.getByLabel('下游跟踪的本期结论').getAttribute('maxlength'), '200');
    await p.getByRole('button', {name: '让模型起草', exact: true}).click();
    await p.locator('#report-edit-status').filter({hasText: '已填入 3 条草稿'}).waitFor();
    assert.equal(received.model_id, 'synthetic');
    assert.deepEqual(received.dimension_notes['custom-stock'], {text: '研究员原句', impact: 'bearish'});
    assert.match(await p.getByLabel('下游跟踪的本期结论').inputValue(), /变为12吨/);
    assert.equal(await p.getByLabel('下游跟踪的影响').inputValue(), 'neutral');
    assert.equal(await p.getByLabel('库存观察的本期结论').inputValue(), '研究员原句', '待核对句不预填');
    assert.equal(await p.getByLabel('库存观察的影响').inputValue(), 'bearish');
    assert.match(await p.locator('[data-edit-dimension="custom-stock"] [data-draft-status]').innerText(), /数字待核对/);
    assert.equal(fixture.calls.filter(call => call.path.startsWith('/report-drafts')).length, 0, '模型结果不自动保存');
    assert.equal(await summaryInput.inputValue(), '本周维持观察。后续关注下游能否改善。');
    assert.match(await p.locator('#report-period-conclusion').textContent(), /旧草稿导语/);
    await p.locator('#report-editor details').filter({hasText: '选图与图说'}).locator('summary').click();
    assert.equal(await p.getByLabel('合成证据图的一句结论').inputValue(), '合成图结论草稿');
    await p.getByRole('button', {name: '让模型起草', exact: true}).click();
    await p.locator('#report-summary-draft-status').filter({hasText: '总体数字待核对'}).waitFor();
    assert.equal(await summaryInput.inputValue(), '本周维持观察。后续关注下游能否改善。');
    await p.getByRole('button', {name: '让模型起草', exact: true}).click();
    await p.locator('#report-edit-status').filter({hasText: '合成模型调用失败'}).waitFor();
    assert.match(await p.getByLabel('下游跟踪的本期结论').inputValue(), /变为12吨/, '失败保留当前编辑');
    await p.getByLabel('备选维度的本期结论').fill('研究员新增的一句话');
    await summaryInput.fill('研究员确认总体结论。');
    await p.getByLabel('备选维度的影响').selectOption('neutral');
    await p.getByLabel('合成证据图的一句结论').fill('研究员修订图结论');
    await p.locator('#report-edit-save').click();
    await p.locator('#report-edit-status').filter({hasText: '已捕获保存请求'}).waitFor();
    assert.equal(saved.revision, 2); assert.deepEqual(saved.draft.dimension_notes['custom-empty'], {text: '研究员新增的一句话', impact: 'neutral'});
    assert.equal(saved.draft.figure_conclusions[figure.key], '研究员修订图结论');
    assert.equal(saved.draft.sections.summary.text, '研究员确认总体结论。');
    await p.locator('#report-edit-cancel').click(); await p.locator('#report-edit-open').click();
    assert.equal(await p.getByLabel('备选维度的本期结论').inputValue(), '', '取消后重新打开恢复已保存内容');
    assert.equal(await summaryInput.inputValue(), '旧草稿导语');
    await fixture.close();
    const legacy = report();
    delete legacy.panels.summary.text;
    const readonly = await open(browser, prefix + '/sn/reports', html({can_write: false}, legacy, 'daily'), ({path, reply}) => path.startsWith('/reports/figures/') ? reply({status: 'unavailable', notice: '隔离夹具未绘图'}) : false);
    assert.match(await readonly.page.locator('#report-period-conclusion').innerText(), /本日结论\s+待研究员填写/);
    assert.equal(await readonly.page.locator('#report-edit-open, #report-editor-data').count(), 0);
    assert.equal(readonly.calls.some(call => call.path.includes('models') || call.path.includes('draft-dimensions')), false);
    await readonly.close();
    console.log('PASS: one-page structure, custom dimensions, evidence groups, explicit edit/save, unsaved model drafts, numeric rejection, failure and readonly');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
