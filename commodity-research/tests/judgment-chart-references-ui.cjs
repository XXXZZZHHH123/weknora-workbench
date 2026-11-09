/* Real judgment manager template and scripts; all API reads/writes intercepted. */
const {assert, render, launch, open, prefix, definition, record, cycle} = require('./chart-ui-fixture.cjs');
const partial = render('_judgment_manager.html', {}, {logic_input: '', selected_judgment_id: 42, j: null});
const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<link rel="stylesheet" href="/static/app.css"><link rel="stylesheet" href="/static/judgment-manager.css"><link rel="stylesheet" href="/static/charts.css">
<style>[hidden]{display:none!important}body{padding:20px}</style>
<script>window.RESEARCH_PREFIX=${JSON.stringify(prefix)};window.RESEARCH_CONTEXT={tenant:17,user:'fixture-user',workbench:'fixture',scope_version:2};window.researchFetch=(url,options)=>fetch(url,options);</script>
<script defer src="/static/chart-kit.js"></script><script defer src="/static/chart-hover.js"></script><script defer src="/static/chart-references.js"></script>
<script defer src="/static/judgment-chart.js"></script><script defer src="/static/judgment-manager.js"></script></head>
<body><button data-open-logic>管理研究逻辑</button>
<section data-judgment-library="42" data-chart-date="2029-09-15"><p data-library-status></p><div data-judgment-library-target="judgment"></div></section>
<div data-judgment-library-target="threshold:r-one"></div><div data-chart-related-rule="r-one"></div>
<div data-judgment-library-target="evidence:bull:0"></div>${partial}</body></html>`;
const choice = {id: 'chart-selected', revision: 3, title: '可插入的图表 <安全>', group: '研究分组', definition_summary: '周期对比 · 数据曲线'};
const oldReference = {kind: 'chart', id: 'chart-existing', revision: 2, title: '已保存的图表', definition_summary: '周期对比 · 数据曲线',
  as_of: '2028-09-15', source_version: 'historical-facts', target: {kind: 'judgment'}};
const rules = [{id: 'r-one', series_id: 'kb-fixture-series', operator: '>', value: 23, name: '真实ID阈值'}];
const targets = [{target: {kind: 'judgment'}, title: '整条研判的图表依据'},
  {target: {kind: 'threshold', id: 'r-one'}, title: '规则：真实ID阈值'},
  {target: {kind: 'evidence', side: 'bull', index: 0}, title: '多方证据 1'}];
const current = {id: 42, version: 4, status: '草稿', variety: 'ZZ', written_at: '2029-09-15', markdown: '# 研判当前版本',
  source_text: '原始研究依据', chart_references: [oldReference], chart_rules: rules, chart_targets: targets, delete_revision: 1};
const historical = {...current, id: 41, version: 2, written_at: '2028-09-15', markdown: '# 历史研判', diff: {markdown: '-旧\n+新'}};

(async () => {
  const browser = await launch();
  try {
    const saves = [], renders = [], fixture = await open(browser, '/sn/judgment?judgment_id=42', html,
      async ({path, method, body, reply}) => {
        if (path === '/judgment/library-references' && method === 'GET') return reply({references: [oldReference], rules,
          as_of: current.written_at, related: {'r-one': [{id: 'chart-related', title: '相同真实指标ID的图表'}]}});
        if (path === '/judgment/library-references' && method === 'POST') {
          saves.push(body); return reply({id: 43, version: 6, status: '草稿'});
        }
        if (path === '/charts/chart-related') return reply(record('chart-related', {revision: 4, title: '相同真实指标ID的图表'}));
        if (path === '/charts/chart-related/data') return reply({...cycle(), revision: 4});
        if (path === '/judgment/manage') return reply({current, latest: {id: 44, version: 5}, variety: 'ZZ', can_write: true,
          versions: [{id: 41, version: 2, variety: 'ZZ', status: '已归档'}]});
        if (path === '/judgment/models') return reply({models: [{id: 'fixture-model', name: '测试模型'}]});
        if (path === '/judgment/panels') return reply({items: [], total: 0, has_more: false});
        if (path === '/judgment/jobs/latest') return reply({job: null});
        if (path === '/judgment/versions/2') return reply(historical);
        if (path === '/chart-references/options') return reply({items: [choice], groups: ['研究分组'], total: 1, next_offset: null});
        if (path === '/chart-references/render') {
          renders.push(body);
          return reply({reference: body.reference, title: body.reference.title, definition, payload: cycle(),
            overlays: [{kind: 'fixed', series_id: 'kb-fixture-series', rule_id: 'r-one', name: '真实ID阈值', value: 23}],
            current: body.current, as_of: body.current ? '2030-01-20' : body.reference.as_of, notice: '已有新修订', status: 'updated'});
        }
        return false;
      });
    const p = fixture.page;
    const embedded = p.locator('[data-judgment-library-target="judgment"]');
    await embedded.locator('svg').waitFor();
    assert.match(await p.locator('[data-chart-related-rule="r-one"]').innerText(), /相关图表：相同真实指标ID的图表/);
    const related = p.locator('[data-chart-related-rule="r-one"] a');
    assert.equal(await related.getAttribute('href'), prefix + '/sn/charts/chart-related');
    assert.equal(fixture.calls.filter(call => call.path.startsWith('/charts/chart-related')).length, 0, '相关图表仅在悬停时读取');
    await related.hover();
    const hover = p.getByRole('dialog', {name: '相同真实指标ID的图表'});
    await hover.locator('svg').waitFor();
    const dataCall = fixture.calls.find(call => call.path === '/charts/chart-related/data');
    assert.equal(dataCall.url.searchParams.get('date'), '2029-09-15', '相关图表采用页面日期，保持与研判写作日期独立');
    await p.keyboard.press('Escape');
    await hover.waitFor({state: 'hidden'});
    assert.equal(await related.evaluate(link => document.activeElement === link), true);
    assert.equal(await embedded.locator('[data-chart-reference-mode]').inputValue(), 'historical');
    assert.equal(renders[0].reference.revision, 2); assert.equal(renders[0].reference.as_of, '2028-09-15');
    await embedded.locator('[data-chart-reference-mode]').selectOption('current');
    await embedded.locator('[data-chart-reference-status]').filter({hasText: '当前数据 · 截止 2030-01-20'}).waitFor();
    assert.equal(renders.at(-1).reference.revision, 2);

    await p.locator('[data-open-logic]').click();
    const manager = p.locator('#judgment-manager');
    await manager.locator('#logic-chart-editor').waitFor();
    await manager.locator('#logic-chart-insert:not(:disabled)').waitFor();
    for (const target of targets.map(item => item.target)) {
      await manager.locator('#logic-chart-target').selectOption(JSON.stringify(target));
      await manager.locator('#logic-chart-insert').click();
      await p.getByRole('dialog', {name: '插入图表'}).locator('[data-chart-picker-id="chart-selected"]').click();
    }
    assert.equal(await manager.locator('#logic-chart-selected > p').count(), 4);
    assert.match(await manager.locator('#logic-chart-selected').innerText(), /整条研判的图表依据/);
    assert.match(await manager.locator('#logic-chart-selected').innerText(), /规则：真实ID阈值/);
    assert.match(await manager.locator('#logic-chart-selected').innerText(), /多方证据 1/);
    assert.equal(await manager.locator('#logic-current-charts [data-chart-reference]').count(), 4);
    assert.ok(renders.filter(call => call.reference.id === choice.id).every(call => !call.current && call.reference.as_of === current.written_at));

    await manager.locator('[data-logic-tab="versions"]').click();
    await manager.locator('#logic-history-charts svg').waitFor();
    const history = manager.locator('#logic-history-charts');
    assert.equal(await history.locator('[data-chart-reference-mode]').inputValue(), 'historical');
    assert.match(await history.locator('[data-chart-reference-status]').innerText(), /当时.*2028-09-15.*修订 2/);
    await history.locator('[data-chart-reference-mode]').selectOption('current');
    await history.locator('[data-chart-reference-status]').filter({hasText: '当前数据 · 截止 2030-01-20'}).waitFor();
    assert.equal(renders.at(-1).reference.revision, 2);
    await history.locator('[data-chart-reference-mode]').selectOption('historical');
    await history.locator('[data-chart-reference-status]').filter({hasText: '当时 · 截止 2028-09-15'}).waitFor();
    await p.setViewportSize({width: 390, height: 900});
    const beforeTheme = await history.locator('clipPath').getAttribute('id');
    await p.evaluate(() => document.documentElement.setAttribute('theme-mode', 'dark'));
    await p.waitForFunction(previous => document.querySelector('#logic-history-charts clipPath')?.id !== previous, beforeTheme);
    const historyBounds = await history.locator('svg').evaluate(svg => {
      const rect = svg.getBoundingClientRect(), plot = svg.closest('[data-chart-reference-plot]').getBoundingClientRect();
      return {left: rect.left, right: rect.right, width: rect.width, plotWidth: plot.width, viewport: innerWidth};
    });
    assert.ok(historyBounds.left >= 0 && historyBounds.right <= historyBounds.viewport + 1 && historyBounds.width <= historyBounds.plotWidth + 1,
      '390px管理历史页主题重绘后，冻结图表仍在图位与视口内');
    await p.setViewportSize({width: 1280, height: 900});
    await manager.locator('[data-logic-tab="current"]').click();
    await manager.locator('#logic-chart-save').click();
    await p.waitForURL('**/sn/judgment?judgment_id=43');
    assert.equal(saves.length, 1);
    assert.equal(saves[0].judgment_id, 42); assert.equal(saves[0].base_version, 5);
    assert.deepEqual(saves[0].references.map(reference => reference.target), [{kind: 'judgment'}, ...targets.map(item => item.target)]);
    assert.ok(saves[0].references.every(reference => Object.keys(reference).sort().join(',') === 'id,kind,revision,target'), '保存仅发送明确图表身份与挂载位置，冻结信息由服务端重建');
    await fixture.close();
    console.log('judgment-chart-references-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
