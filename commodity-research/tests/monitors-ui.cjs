/* Actual templates, scripts and ChartKit in Chromium; all API replies are isolated fixtures. */
const f = require('./chart-ui-fixture.cjs');
const {assert} = f;
const affairs = {shortcuts: [], alerts: [], can_check: false, partial: false,
  sections: ['progress', 'review', 'approvals', 'reports'], counts: {progress: 0, review: 0, approvals: 0, reports: 0},
  progress: [], review: [], approvals: [], observations: [], reports: [], report_links: []};
const rule = {series_id: 'synthetic-a', metric: 'change', operator: '>', value: 0,
  consecutive_periods: 2, label: '合成观测', definition: '本期变化连续两期大于零'};
const monthly = {series_id: 'synthetic-monthly', metric: 'value', operator: '>', value: 50,
  consecutive_periods: 1, label: '合成月度观测', definition: '月度观测值大于五十'};
const draft = {draft_id: 'offered-fixture', title: '合成监测 <script>unsafe</script>', sentence: '合成周观测连续增加两期且合成月观测大于五十时提醒我',
  logic: 'all', rules: [rule, monthly], confirmations: ['请确认变化按相邻观测计算'], candidates: [
    {rule_index: 0, series_id: 'synthetic-a', name: '合成观测A', unit: '吨', frequency: '周'},
    {rule_index: 0, series_id: 'synthetic-b', name: '合成观测B', unit: '吨', frequency: '周'},
    {rule_index: 1, series_id: 'synthetic-monthly', name: '合成月度观测', unit: '吨', frequency: '月'}]};
const backtest = {start: '2030-01-01', end: '2030-01-20', current_state: '触发', trigger_count: 1,
  last_trigger: '2030-01-15', trigger_dates: ['2030-01-15'], daily_states: [],
  intervals: [{start: '2030-01-15', end: '2030-01-20', duration_periods: 1, duration_days: 6, ongoing: true}],
  series: [{series_id: 'synthetic-b', name: '合成观测B', unit: '吨', frequency: '周', latest_date: '2030-01-15',
    rule: {...rule, series_id: 'synthetic-b', value: 7}, points: [{date: '2030-01-01', value: 100}, {date: '2030-01-08', value: 110}, {date: '2030-01-15', value: 120}],
    quantities: [{date: '2030-01-01', value: null}, {date: '2030-01-08', value: 10}, {date: '2030-01-15', value: 10}]},
    {series_id: monthly.series_id, name: monthly.label, unit: '吨', frequency: '月', latest_date: '2030-01-01', rule: monthly,
      points: [{date: '2030-01-01', value: 60}], quantities: [{date: '2030-01-01', value: 60}]}]};
(async () => {
  const browser = await f.launch(); let env;
  const saved = [], checks = [], tests = [];
  try {
    const html = f.render('home.html', {}, {affairs});
    env = await f.open(browser, f.prefix + '/', html, async ({path, method, body, reply}) => {
      if (path === '/strategies/models') return reply({models: [{id: 'fixture-model', name: '合成模型'}]});
      if (path === '/monitors' && method === 'GET') return reply({items: saved, can_write: true});
      if (path === '/monitors/draft') { assert.equal(body.sentence, draft.sentence); assert.equal(body.model_id, 'fixture-model'); return reply(draft); }
      if (path === '/monitors/backtest') {
        tests.push(body); assert.equal(body.draft_id, draft.draft_id); assert.equal(body.rules[0].series_id, 'synthetic-b');
        assert.equal(body.rules[0].value, 7); return reply(backtest);
      }
      if (path === '/monitors' && method === 'POST') {
        assert.deepEqual(body, tests.at(-1)); saved.push({...body, id: 'monitor-fixture', revision: 1, current_state: '未检查', last_trigger: null});
        return reply(saved[0]);
      }
      if (path === '/monitors/monitor-fixture/backtest') return reply(backtest);
      if (path === '/monitors/monitor-fixture' && method === 'PATCH') {
        assert.equal(body.revision, saved[0].revision); Object.assign(saved[0], {enabled: body.enabled, revision: saved[0].revision + 1}); return reply(saved[0]);
      }
      if (path === '/monitors/monitor-fixture' && method === 'DELETE') { saved.splice(0); return reply({deleted: true}); }
      if (path === '/affairs/check') {
        checks.push(true); Object.assign(saved[0], {current_state: '触发', last_trigger: '2030-01-15'});
        const alert = {kind: 'monitor', title: '合成监测', detail: '组合条件进入触发', href: '/?monitor=monitor-fixture#monitors'};
        return reply({alerts: checks.length === 1 ? [alert] : [], monitor_alerts: [alert],
          checked_monitors: 1, checked_rules: 0, checked_at: '2030-01-20T10:00:00', partial: false});
      }
      return false;
    });
    const {page} = env;
    await page.waitForFunction(() => !document.getElementById('monitor-model').disabled);
    assert.equal(await page.locator('#affairs-check').isDisabled(), true);
    assert.deepEqual(await page.locator('main > section, main > .affairs-grid').evaluateAll(elements => elements.map(element => element.id || element.className)),
      ['affairs-alerts', 'affairs-grid', 'monitors'], 'The monitor section is appended after all existing groups');
    const previousLayout = await page.locator('.affairs-grid').evaluate(element => ({html: element.innerHTML, width: element.getBoundingClientRect().width}));
    await page.getByLabel('监测需求原话').fill(draft.sentence);
    await page.getByRole('button', {name: '生成草稿', exact: true}).click();
    await page.locator('#monitor-draft:not([hidden])').waitFor();
    assert.match(await page.locator('#monitor-rules').innerText(), /本期变化 > 0 · 连续 2 期/);
    assert.match(await page.locator('#monitor-confirmation-list').innerText(), /相邻观测/);
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-b');
    await page.getByLabel('第1条规则阈值', {exact: true}).fill('7');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').first().waitFor();
    assert.equal(await page.locator('#monitor-backtest-result [data-rule-boundary]').count(), 2);
    assert.equal(await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').count(), 2,
      'Both frequencies show the combination entry even after the monthly series latest observation');
    assert.equal(await page.locator('#monitor-backtest-result [data-axis-unit="right"]').textContent(), '吨');
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /触发次数.*1.*最近触发.*2030-01-15/s);
    assert.equal(await page.locator('#monitor-save').isDisabled(), true);
    await page.locator('#monitor-confirm').check();
    assert.equal(await page.locator('#monitor-save').isEnabled(), true);
    await page.locator('#monitor-logic').selectOption('any');
    assert.equal(await page.locator('#monitor-save').isDisabled(), true, 'Changing combination logic invalidates the previous backtest and confirmation');
    assert.equal(await page.locator('#monitor-backtest-result').isHidden(), true);
    await page.locator('#monitor-logic').selectOption('all');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').first().waitFor();
    await page.locator('#monitor-confirm').check();
    await page.getByRole('button', {name: '保存监测', exact: true}).click();
    await page.locator('[data-monitor-id="monitor-fixture"]').waitFor();
    assert.match(await page.locator('#monitor-list').innerText(), /合成监测 <script>unsafe<\/script>.*未检查|未检查.*合成监测 <script>unsafe<\/script>/s);
    assert.equal(await page.locator('#affairs-check').isEnabled(), true, 'Saving the first enabled monitor makes the existing check button usable');
    await page.locator('#affairs-check').click();
    await page.locator('#affairs-alert-list .affairs-row-kind').filter({hasText: '监测触发'}).waitFor();
    assert.equal(await page.locator('#affairs-alert-list a').getAttribute('href'), f.prefix + '/?monitor=monitor-fixture#monitors');
    await page.waitForFunction(() => document.getElementById('monitor-list').textContent.includes('2030-01-15'));
    await page.locator('#affairs-check').click();
    await page.waitForFunction(() => document.getElementById('affairs-check').dataset.busy === 'false');
    assert.equal(await page.locator('#affairs-alert-list .affairs-row-kind').filter({hasText: '监测触发'}).count(), 1,
      'A repeated check retains exactly one persisted monitor reminder');
    assert.equal(checks.length, 2);
    await page.locator('[data-monitor-id="monitor-fixture"]').getByRole('button', {name: '查看', exact: true}).click();
    await page.locator('#monitor-view [data-event-marker="2030-01-15"]').first().waitFor();
    assert.deepEqual(await page.locator('.affairs-grid').evaluate(element => ({html: element.innerHTML, width: element.getBoundingClientRect().width})), previousLayout,
      'Building, checking and viewing monitors preserve the existing groups and geometry');
    for (const width of [1280, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.waitForFunction(() => [...document.querySelectorAll('#monitors .chart-kit svg')].every(element => Math.abs(element.viewBox.baseVal.width - element.getBoundingClientRect().width) < 2));
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Monitor cards and controls stay inside viewport ' + width);
    }
    await page.getByLabel('启用 ' + draft.title, {exact: true}).uncheck();
    await page.waitForFunction(() => document.getElementById('monitor-list').textContent.includes('v2'));
    await page.locator('[data-monitor-id="monitor-fixture"]').getByRole('button', {name: '删除', exact: true}).click();
    await page.getByText('还没有保存监测。先用一句话生成草稿。', {exact: true}).waitFor();
    assert.deepEqual(env.errors, []);
    const restrictedHtml = f.render('home.html', {restricted: true}, {affairs});
    assert.ok(!restrictedHtml.includes('id="monitors"'), 'The restricted view does not expose monitoring controls');
    console.log('PASS: monitor sentence → draft → candidate and threshold edits → actual ChartKit trigger marker → save → personal list → check/toggle/delete; existing layout preserved at 1280px and 390px.');
  } finally { await env?.context.close(); await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
