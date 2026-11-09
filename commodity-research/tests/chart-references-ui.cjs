/* Explicit chart pins: isolated browser acceptance without a listening service. */
const {assert, launch, open, prefix, cycle, definition} = require('./chart-ui-fixture.cjs');
const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<link rel="stylesheet" href="/static/app.css"><link rel="stylesheet" href="/static/charts.css"><style>[hidden]{display:none!important}body{padding:20px}main{max-width:1000px}</style>
<script>window.RESEARCH_PREFIX=${JSON.stringify(prefix)};window.researchFetch=(url,options)=>fetch(url,options);</script>
<script src="/static/chart-kit.js"></script><script src="/static/chart-references.js"></script></head>
<body><main><button id="insert">插入图表</button><div id="judgment"></div><div id="strategy"></div><div id="deleted"></div><div id="restricted"></div></main>
<script>document.getElementById('insert').onclick=()=>{window.ChartReferences.pick({asOf:'2029-09-15'}).then(value=>{window.picked=value;});};</script></body></html>`;
const chart = {id: 'chart-explicit', title: '当时标题 <img src=x onerror=window.__xss=1>', revision: 3,
  group: '历史分组', definition_summary: '周期 · 1 条曲线 <定义>'};
const second = {id: 'chart-second', title: '另一张图', revision: 1, group: '另一组', definition_summary: '走势 · 1 条曲线'};
const rules = [{series_id: 'kb-fixture-series', rule_id: 'r-fixed', kind: 'fixed', value: 23, name: '固定边界'},
  {series_id: 'kb-fixture-series', rule_id: 'r-sma', kind: 'sma', window: 2, name: '均线边界'},
  {series_id: '同名非ID', rule_id: 'unbound', kind: 'fixed', value: 999999, name: '不得关联'}];

(async () => {
  const browser = await launch();
  try {
    const calls = [], fixture = await open(browser, '/reference-fixture', html, async ({path, url, method, body, reply}) => {
      if (path === '/chart-references/options') {
        calls.push({path, query: Object.fromEntries(url.searchParams)});
        const q = url.searchParams.get('q') || '', group = url.searchParams.get('group');
        const items = [chart, second].filter(item => (!q || item.title.includes(q)) && (!group || item.group === group)
          && (url.searchParams.get('favorites') !== 'true' || item.id === chart.id));
        return reply({items, groups: ['历史分组', '另一组'], total: items.length, next_offset: null});
      }
      if (path === '/chart-references/render' && method === 'POST') {
        calls.push({path, body});
        if (body.reference.id === 'chart-denied') return reply({detail: '图表引用暂不对受限视角开放'}, 403);
        if (body.reference.id === 'chart-deleted') return reply({reference: body.reference, status: 'deleted', current: body.current,
          title: body.reference.title, notice: '图表已删除', as_of: body.reference.as_of, payload: null});
        const payload = cycle();
        const overlays = rules.map(rule => rule.kind === 'sma' ? {...rule,
          points: payload.segments.flatMap(segment => segment.points.map(point => ({date: point.date, value: point.value - 2})))} : rule);
        return reply({reference: body.reference, status: 'new_revision', notice: '已有新修订', current: body.current,
          as_of: body.current ? '2030-01-20' : body.reference.as_of, definition, payload, overlays,
          dedup_notice: '可改为引用 default 库指标'});
      }
      return false;
    });
    const p = fixture.page;
    await p.locator('#insert').click();
    const picker = p.getByRole('dialog', {name: '插入图表'});
    await picker.locator('[data-chart-picker-id="chart-explicit"]').waitFor();
    assert.equal(await picker.locator('img').count(), 0, '标题通过文本显示');
    assert.equal(await p.evaluate(() => window.__xss), undefined);
    await picker.locator('[data-chart-picker-group]').selectOption('历史分组');
    await picker.locator('[data-chart-picker-favorites]').check();
    await picker.locator('[data-chart-picker-search]').fill('当时标题');
    await p.waitForFunction(() => document.querySelector('[data-chart-picker-status]')?.textContent === '共 1 张图表');
    await p.waitForTimeout(220);
    await picker.locator('[data-chart-picker-id="chart-explicit"]').click();
    await p.waitForFunction(() => window.picked?.id === 'chart-explicit');
    const picked = await p.evaluate(() => window.picked);
    assert.equal(picked.revision, 3); assert.equal(picked.as_of, '2029-09-15');
    assert.ok(calls.some(call => call.query?.q === '当时标题' && call.query.group === '历史分组' && call.query.favorites === 'true'));

    await p.evaluate(rules => {
      window.judgmentHandle = ChartReferences.mount(document.getElementById('judgment'), {references: [window.picked], rules});
      window.strategyHandle = ChartReferences.mount(document.getElementById('strategy'), {references: [window.picked], thumbnail: true});
    }, rules);
    const judgment = p.locator('#judgment [data-chart-reference="chart-explicit"]'), strategy = p.locator('#strategy [data-chart-reference="chart-explicit"]');
    await judgment.locator('svg').waitFor(); await strategy.locator('svg').waitFor();
    assert.equal(await judgment.locator('[data-chart-reference-mode]').inputValue(), 'historical');
    const historical = calls.filter(call => call.path.endsWith('/render'));
    assert.ok(historical.every(call => call.body.current === false && call.body.reference.revision === 3 && call.body.reference.as_of === '2029-09-15'));
    assert.deepEqual(historical[0].body.rules, rules);
    assert.match(await judgment.locator('[data-chart-reference-status]').innerText(), /已有新修订.*当时.*2029-09-15.*修订 3/);
    assert.match(await judgment.locator('[data-chart-reference-notice]').innerText(), /可改为引用 default 库指标/);
    assert.equal(await judgment.locator('[data-rule-overlay="fixed"]').count(), 1);
    assert.equal(await judgment.locator('[data-rule-overlay="sma"]').count(), 4);
    assert.equal(await judgment.locator('[data-rule-id="unbound"]').count(), 0);
    assert.equal(await strategy.locator('[data-legend]').count(), 0, '策略使用缩略图');
    assert.match(await strategy.locator('[data-chart-reference-position]').innerText(), /周期第 14 天.*同期排名 1\/4/);
    assert.equal(await judgment.locator('[data-chart-reference-title]').innerText(), chart.title);
    assert.equal(await judgment.locator('img').count(), 0);
    await judgment.locator('[data-chart-reference-mode]').selectOption('current');
    await judgment.locator('[data-chart-reference-status]').filter({hasText: '当前数据 · 截止 2030-01-20'}).waitFor();
    assert.equal(calls.at(-1).body.current, true); assert.equal(calls.at(-1).body.reference.revision, 3);
    await judgment.locator('[data-chart-reference-mode]').selectOption('historical');
    await judgment.locator('[data-chart-reference-status]').filter({hasText: '当时 · 截止 2029-09-15'}).waitFor();

    await p.evaluate(reference => ChartReferences.mount(document.getElementById('deleted'), {references: [{...reference, id: 'chart-deleted'}]}), picked);
    await p.locator('#deleted [data-chart-reference-status]').filter({hasText: '图表已删除'}).waitFor();
    assert.equal(await p.locator('#deleted [data-chart-reference-title]').innerText(), chart.title);
    assert.equal(await p.locator('#deleted [data-chart-reference-definition]').innerText(), chart.definition_summary);
    assert.equal(await p.locator('#deleted svg').count(), 0);
    const beforeRestricted = calls.length;
    await p.evaluate(reference => ChartReferences.mount(document.getElementById('restricted'), {references: [reference], restricted: true}), picked);
    assert.equal(await p.locator('#restricted').innerText(), '图表引用暂不对受限视角开放');
    assert.equal(calls.length, beforeRestricted, '受限视角不请求引用');
    await p.evaluate(reference => ChartReferences.mount(document.getElementById('restricted'), {references: [{...reference, id: 'chart-denied'}]}), picked);
    await p.locator('#restricted').filter({hasText: '图表引用暂不对受限视角开放'}).waitFor();
    assert.equal(await p.locator('#restricted [data-chart-reference]').count(), 0);
    assert.equal(await p.evaluate(() => window.__xss), undefined);
    await p.setViewportSize({width: 390, height: 900});
    const beforeTheme = await judgment.locator('clipPath').getAttribute('id');
    await p.evaluate(() => document.documentElement.setAttribute('theme-mode', 'dark'));
    await p.waitForFunction(previous => document.querySelector('#judgment clipPath')?.id !== previous, beforeTheme);
    const bounds = await p.locator('#judgment svg,#strategy svg').evaluateAll(nodes => nodes.map(svg => {
      const rect = svg.getBoundingClientRect(), plot = svg.closest('[data-chart-reference-plot]').getBoundingClientRect();
      return {left: rect.left, right: rect.right, width: rect.width, plotWidth: plot.width, viewport: innerWidth};
    }));
    assert.equal(bounds.length, 2);
    assert.ok(bounds.every(box => box.left >= 0 && box.right <= box.viewport + 1 && box.width <= box.plotWidth + 1), '390px与主题重绘后，研判和策略缩略图SVG保持在各自图位与视口内');
    await fixture.close();
    console.log('chart-references-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
