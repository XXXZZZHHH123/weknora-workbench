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
const sourceError = '所选指标含未确认日期，无法确定均线观测顺序，请先核对原表日期 <img src=x onerror=window.__monitorXss=1>';
const draft = {draft_id: 'offered-fixture', title: '合成监测 <script>unsafe</script>', sentence: '合成周观测连续增加两期且合成月观测大于五十时提醒我',
  default_window: {start: '2028-01-20', end: '2030-01-20', window_reason: '按数据可用区间回测'},
  logic: 'all', rules: [rule, monthly], confirmations: ['请确认变化按相邻观测计算'], candidates: [
    {rule_index: 0, series_id: 'synthetic-a', name: '合成观测A', unit: '吨', frequency: '周',
      coverage: {start: '2029-11-01', end: '2030-01-20', frequency: '周', point_count: 12, stale: true}},
    {rule_index: 0, series_id: 'synthetic-b', name: '合成观测B', unit: '吨', frequency: '周',
      coverage: {start: '2025-01-01', end: '2030-01-20', frequency: '周', point_count: 260, stale: false}},
    {rule_index: 0, series_id: 'synthetic-unknown', name: '覆盖待核对观测', unit: '吨', frequency: '周',
      coverage: {start: null, end: null, frequency: '周', unknown: true, reason: sourceError}},
    {rule_index: 1, series_id: 'synthetic-monthly', name: '合成月度观测', unit: '吨', frequency: '月',
      coverage: {start: '2025-01-01', end: '2030-01-20', frequency: '月', point_count: 60, stale: false}}]};
const backtest = {start: '2030-01-01', end: '2030-01-20', current_state: '触发', trigger_count: 1,
  window_reason: '数据最新至 2030-01-20，按数据可用区间回测',
  overlap: {start: '2025-01-01', end: '2030-01-20', days: 1846, observation_periods: 60,
    observation_periods_limited: true, observation_periods_bound: 'lower', insufficient: false},
  window_overlap: {start: '2030-01-01', end: '2030-01-20', days: 20, observation_periods: null},
  rule_coverage: draft.candidates.filter(item => ['synthetic-b', 'synthetic-monthly'].includes(item.series_id)).map(item => ({...item.coverage, series_id: item.series_id, rule_index: item.rule_index})),
  last_trigger: '2030-01-15', trigger_dates: ['2030-01-15'], daily_states: [],
  intervals: [{start: '2030-01-15', end: '2030-01-20', duration_periods: 1, duration_days: 6, ongoing: true}],
  series: [{series_id: 'synthetic-b', name: '合成观测B', unit: '吨', frequency: '周', latest_date: '2030-01-15',
    rule: {...rule, series_id: 'synthetic-b', value: 7}, points: [{date: '2030-01-01', value: 100}, {date: '2030-01-08', value: 110}, {date: '2030-01-15', value: 120}],
    quantities: [{date: '2030-01-01', value: null}, {date: '2030-01-08', value: 10}, {date: '2030-01-15', value: 10}]},
    {series_id: monthly.series_id, name: monthly.label, unit: '吨', frequency: '月', latest_date: '2030-01-01', rule: monthly,
      points: [{date: '2030-01-01', value: 60}], quantities: [{date: '2030-01-01', value: 60}]}]};
const sparseDraft = {...draft, rules: [{...rule, series_id: 'synthetic-sparse'}, monthly],
  overlap: {start: '2025-01-01', end: '2030-01-20', days: 1846, observation_periods: 8, insufficient: true},
  candidates: [...draft.candidates,
    {rule_index: 0, series_id: 'synthetic-sparse', name: '长跨度稀疏观测', unit: '吨', frequency: '周',
      coverage: {start: '2025-01-01', end: '2030-01-20', frequency: '周', point_count: null}},
    {rule_index: 0, series_id: 'synthetic-five', name: '长跨度五个观测', unit: '吨', frequency: '周',
      coverage: {start: '2025-01-01', end: '2030-01-20', frequency: '周', point_count: 5}}]};
(async () => {
  const browser = await f.launch(); let env;
  let offeredDraft = draft;
  let returnedBacktest = backtest;
  let savedAlerts = [];
  let finishRevision;
  const revisionGate = new Promise(resolve => { finishRevision = resolve; });
  const saved = [], checks = [], tests = [], savedWindows = [];
  try {
    const html = f.render('home.html', {}, {affairs});
    env = await f.open(browser, f.prefix + '/', html, async ({path, method, body, url, reply}) => {
      if (path === '/strategies/models') return reply({models: [{id: 'fixture-model', name: '合成模型'}]});
      if (path === '/monitors' && method === 'GET') return reply({items: saved, can_write: true, alerts: savedAlerts});
      if (path === '/monitors/draft') { assert.equal(body.sentence, offeredDraft.sentence); assert.equal(body.model_id, 'fixture-model'); return reply(offeredDraft); }
      if (path === '/monitors/backtest') {
        tests.push(body); assert.equal(body.draft_id, draft.draft_id); assert.equal(body.rules[0].series_id, 'synthetic-b');
        assert.equal(body.rules[0].value, 7); return reply({...returnedBacktest, start: body.start || returnedBacktest.start, end: body.end || returnedBacktest.end});
      }
      if (path === '/monitors' && method === 'POST') {
        const {start, end, ...testedBody} = tests.at(-1);
        assert.deepEqual(body, testedBody); saved.push({...body, id: 'monitor-fixture', revision: 1, current_state: '未检查', last_trigger: null});
        return reply(saved[0]);
      }
      if (path === '/monitors/monitor-fixture/backtest') {
        const window = Object.fromEntries(url.searchParams); savedWindows.push(window);
        return reply({...backtest, start: window.start || backtest.start, end: window.end || backtest.end});
      }
      if (path === '/monitors/monitor-fixture' && method === 'PATCH') {
        assert.equal(body.revision, saved[0].revision);
        const {revision, ...update} = body;
        if (update.draft_id) {
          const {start, end, ...testedBody} = tests.at(-1);
          assert.deepEqual(update, testedBody);
          assert.equal(update.enabled, false, 'Revising does not re-enable a stopped monitor');
          await revisionGate;
        }
        Object.assign(saved[0], update, {revision: revision + 1}); return reply(saved[0]);
      }
      if (path === '/monitors/monitor-fixture' && method === 'DELETE') {
        saved.splice(0); savedAlerts = []; return reply({deleted: true});
      }
      if (path === '/affairs/check') {
        checks.push(true); Object.assign(saved[0], {current_state: '触发', last_trigger: '2030-01-15'});
        const alert = {kind: 'monitor', title: '合成监测', detail: '组合条件进入触发', href: '/?monitor=monitor-fixture#monitors'};
        savedAlerts = [alert];
        return reply({alerts: checks.length === 1 ? [alert] : [], monitor_alerts: savedAlerts,
          checked_monitors: 1, checked_rules: 0, checked_at: '2030-01-20T10:00:00', partial: false});
      }
      return false;
    });
    const {page} = env;
    await page.waitForFunction(() => !document.getElementById('monitor-model').disabled);
    assert.equal(await page.locator('#affairs-check').isDisabled(), true);
    assert.equal(await page.getByRole('heading', {name: '预警与触发', exact: true}).count(), 1);
    assert.deepEqual(await page.locator('main > section, main > .affairs-grid').evaluateAll(elements => elements.map(element => element.id || element.className)),
      ['affairs-alerts', 'affairs-grid']);
    assert.equal(await page.locator('.affairs-alerts dialog, .affairs-alerts #monitor-create-panel, .affairs-alerts #monitor-list-panel').count(), 0);
    assert.ok(!(await page.locator('main').textContent()).includes('一句话建监测'));
    const createToggle = page.locator('.affairs-alerts').getByRole('button', {name: '新建监测', exact: true});
    const listToggle = page.locator('#monitor-list-toggle');
    const dialog = page.locator('dialog#monitors');
    const close = dialog.getByRole('button', {name: '关闭', exact: true});
    assert.equal(await listToggle.innerText(), '监测 0');
    assert.equal(await dialog.isHidden(), true);
    const previousLayout = await page.locator('.affairs-grid').evaluate(element => ({html: element.innerHTML, width: element.getBoundingClientRect().width}));
    const previousPosition = await page.locator('.affairs-grid').boundingBox();
    await listToggle.click();
    await page.getByRole('dialog', {name: '我的监测'}).waitFor();
    assert.equal(await dialog.locator('#monitor-create-panel').isHidden(), true);
    await dialog.getByRole('button', {name: '新建监测', exact: true}).click();
    await page.getByRole('dialog', {name: '新建监测'}).waitFor();
    assert.equal(await dialog.locator('#monitor-list-panel').isHidden(), true);
    await page.waitForFunction(() => document.activeElement.id === 'monitor-sentence');
    await page.keyboard.press('Escape');
    await dialog.waitFor({state: 'hidden'});
    await page.waitForFunction(() => document.activeElement.id === 'monitor-list-toggle');
    assert.deepEqual(await page.locator('.affairs-grid').boundingBox(), previousPosition);
    await createToggle.click();
    await page.getByRole('dialog', {name: '新建监测'}).waitFor();
    await page.getByLabel('监测需求原话').fill(draft.sentence);
    await page.getByRole('button', {name: '生成草稿', exact: true}).click();
    await page.locator('#monitor-draft:not([hidden])').waitFor();
    assert.match(await page.locator('#monitor-rules').innerText(), /本期变化 > 0 · 连续 2 期/);
    assert.match(await page.locator('#monitor-confirmation-list').innerText(), /相邻观测/);
    assert.equal(await page.getByLabel('草稿回测起始日期', {exact: true}).inputValue(), draft.default_window.start);
    assert.equal(await page.getByLabel('草稿回测结束日期', {exact: true}).inputValue(), draft.default_window.end);
    const firstRule = page.locator('[data-monitor-rule="0"]');
    await firstRule.locator('.monitor-candidates summary').click();
    assert.match(await firstRule.locator('.monitor-candidates').innerText(), /合成观测A · 覆盖 2029-11-01 至 2030-01-20 · 周 · 12 个观测 · 数据停更/);
    assert.match(await firstRule.locator('.monitor-candidates').innerText(), /合成观测B · 覆盖 2025-01-01 至 2030-01-20/);
    assert.match(await page.getByLabel('第1条规则指标', {exact: true}).textContent(), /覆盖 2029-11-01 至 2030-01-20/);
    assert.equal(await firstRule.locator('[data-overlap-warning]').isVisible(), true);
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-unknown');
    assert.equal(await page.getByLabel('第1条规则指标', {exact: true}).inputValue(), 'synthetic-unknown',
      'A failed coverage read remains selectable');
    assert.equal(await firstRule.locator('[data-coverage]').innerText(), '覆盖未知：' + sourceError + ' · 周');
    assert.equal(await page.evaluate(() => window.__monitorXss), undefined);
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-b');
    assert.equal(await firstRule.locator('[data-overlap-warning]').isHidden(), true, 'Changing a candidate recalculates the overlap warning without replacing another choice');
    await page.getByLabel('第1条规则阈值', {exact: true}).fill('7');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').first().waitFor();
    assert.equal(await page.locator('#monitor-backtest-result [data-rule-boundary]').count(), 2);
    assert.equal(await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').count(), 2,
      'Both frequencies show the combination entry even after the monthly series latest observation');
    assert.equal(await page.locator('#monitor-backtest-result [data-axis-unit="right"]').textContent(), '吨');
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /触发次数.*1.*最近触发.*2030-01-15/s);
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /按数据可用区间回测/);
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /规则来源数据重叠 2025-01-01 至 2030-01-20 · 1846 天 · 至少60 个观测期/);
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /本次回测窗口重叠 2030-01-01 至 2030-01-20 · 20 天 · 观测期待核实/);
    assert.equal(await page.locator('#monitor-save').isDisabled(), true);
    await page.locator('#monitor-confirm').check();
    assert.equal(await page.locator('#monitor-save').isEnabled(), true);
    await page.locator('#monitor-logic').selectOption('any');
    assert.equal(await page.locator('#monitor-save').isDisabled(), true, 'Changing combination logic invalidates the previous backtest and confirmation');
    assert.equal(await page.locator('#monitor-backtest-result').isHidden(), true);
    await page.locator('#monitor-logic').selectOption('all');
    await page.getByLabel('草稿回测起始日期', {exact: true}).fill('2029-12-01');
    await page.getByLabel('草稿回测结束日期', {exact: true}).fill('2030-01-18');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').first().waitFor();
    assert.equal(tests.at(-1).start, '2029-12-01'); assert.equal(tests.at(-1).end, '2030-01-18');
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /回测范围 2029-12-01 至 2030-01-18/);
    await page.locator('#monitor-confirm').check();
    await page.getByRole('button', {name: '保存监测', exact: true}).click();
    await page.getByRole('dialog', {name: '我的监测'}).waitFor();
    await dialog.locator('[data-monitor-id="monitor-fixture"][aria-current="true"]').waitFor();
    assert.equal(await listToggle.innerText(), '监测 1');
    assert.match(await page.locator('#monitor-list').innerText(), /合成监测 <script>unsafe<\/script>.*未检查|未检查.*合成监测 <script>unsafe<\/script>/s);
    await close.click();
    await page.waitForFunction(() => document.activeElement.id === 'monitor-create-toggle');
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
    await page.evaluate(() => { window.location.hash = '#monitors'; });
    await page.getByRole('dialog', {name: '我的监测'}).waitFor();
    await page.waitForFunction(() => !document.querySelector('[data-monitor-id][aria-current="true"]'));
    assert.equal(await page.locator('[data-monitor-id][aria-current="true"]').count(), 0);
    await close.click();
    await page.locator('#affairs-alert-list a').click();
    await page.waitForURL('**/?monitor=monitor-fixture#monitors');
    await page.locator('#monitor-list-panel:not([hidden]) [data-monitor-id="monitor-fixture"][aria-current="true"]').waitFor();
    assert.notEqual(await page.locator('[data-monitor-id="monitor-fixture"]').evaluate(element => getComputedStyle(element).boxShadow), 'none');
    await close.click();
    await dialog.waitFor({state: 'hidden'});
    await page.locator('#affairs-alert-list a').click();
    await page.getByRole('dialog', {name: '我的监测'}).waitFor();
    await dialog.locator('[data-monitor-id="monitor-fixture"][aria-current="true"]').waitFor();
    Object.assign(saved[0], {current_state: '无法判断', rule_states: [
      {rule_index: 0, label: rule.label, state: '无法判断', notice: sourceError},
      {rule_index: 1, label: monthly.label, state: '触发', notice: ''}]});
    await page.getByRole('button', {name: '刷新列表', exact: true}).click();
    await page.waitForFunction(() => document.getElementById('monitor-list').textContent.includes('所选指标含未确认日期'));
    assert.match(await page.locator('#monitor-list').innerText(), /第1条规则.*无法判断.*所选指标含未确认日期/s);
    assert.match(await page.locator('#monitor-list').innerText(), /第2条规则.*触发/s);
    assert.equal(await page.evaluate(() => window.__monitorXss), undefined);
    await page.locator('[data-monitor-id="monitor-fixture"]').getByRole('button', {name: '查看', exact: true}).click();
    await page.locator('#monitor-view [data-event-marker="2030-01-15"]').first().waitFor();
    assert.equal(await page.getByLabel('已保存监测回测起始日期', {exact: true}).inputValue(), backtest.start);
    await page.getByLabel('已保存监测回测起始日期', {exact: true}).fill('2030-01-04');
    await page.getByLabel('已保存监测回测结束日期', {exact: true}).fill('2030-01-19');
    await page.getByRole('button', {name: '调整窗口后回测', exact: true}).click();
    await page.waitForFunction(() => document.getElementById('monitor-view-result').textContent.includes('回测范围 2030-01-04 至 2030-01-19'));
    assert.deepEqual(savedWindows.at(-1), {start: '2030-01-04', end: '2030-01-19'});
    assert.deepEqual(await page.locator('.affairs-grid').evaluate(element => ({html: element.innerHTML, width: element.getBoundingClientRect().width})), previousLayout,
      'Building, checking and viewing monitors preserve the existing groups and geometry');
    for (const width of [1280, 390]) {
      await page.setViewportSize({width, height: 900});
      await page.waitForFunction(() => [...document.querySelectorAll('#monitors .chart-kit svg')].every(element => Math.abs(element.viewBox.baseVal.width - element.getBoundingClientRect().width) < 2));
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Monitor cards and controls stay inside viewport ' + width);
    }
    await page.getByLabel('启用 ' + draft.title, {exact: true}).uncheck();
    await page.waitForFunction(() => document.getElementById('monitor-list').textContent.includes('v2'));
    await dialog.getByRole('button', {name: '修订', exact: true}).click();
    await page.getByRole('dialog', {name: '修订监测'}).waitFor();
    assert.equal(await page.getByLabel('监测需求原话').inputValue(), draft.sentence);
    await page.getByRole('button', {name: '生成草稿', exact: true}).click();
    await page.locator('#monitor-draft:not([hidden])').waitFor();
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-b');
    await page.getByLabel('第1条规则阈值', {exact: true}).fill('7');
    await dialog.getByLabel('标题', {exact: true}).fill('修订后的监测');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result [data-event-marker="2030-01-15"]').first().waitFor();
    await page.locator('#monitor-confirm').check();
    await dialog.getByRole('button', {name: '保存监测', exact: true}).click();
    await page.keyboard.press('Escape');
    await dialog.waitFor({state: 'hidden'});
    finishRevision();
    await page.waitForFunction(() => document.getElementById('monitor-list').textContent.includes('修订后的监测'));
    assert.equal(await dialog.isHidden(), true, 'A successful save does not reopen a dialog closed during the request');
    await listToggle.click();
    await dialog.getByText('修订后的监测', {exact: true}).waitFor();
    assert.match(await page.locator('#monitor-list').innerText(), /v3/);
    assert.equal(await dialog.getByLabel('启用 修订后的监测', {exact: true}).isChecked(), false);
    assert.equal(await dialog.locator('#monitor-view').isHidden(), true);
    await page.locator('[data-monitor-id="monitor-fixture"]').getByRole('button', {name: '删除', exact: true}).click();
    await page.getByText('还没有保存监测。', {exact: true}).waitFor();
    assert.equal(await listToggle.innerText(), '监测 0');
    assert.equal(await page.locator('#affairs-alert-list [data-kind="monitor"]').count(), 0);
    assert.equal(checks.length, 2, 'Removing a monitor refreshes saved alerts without running a new source check');
    assert.equal(await page.locator('#affairs-check').isDisabled(), true);
    offeredDraft = sparseDraft;
    await dialog.getByRole('button', {name: '新建监测', exact: true}).click();
    await page.getByLabel('监测需求原话').fill(draft.sentence);
    await page.getByRole('button', {name: '生成草稿', exact: true}).click();
    await page.locator('#monitor-draft:not([hidden])').waitFor();
    assert.equal(await page.getByLabel('第1条规则指标', {exact: true}).inputValue(), 'synthetic-sparse');
    assert.equal(await firstRule.locator('[data-overlap-warning]').isVisible(), true,
      'The initial server overlap warning is shown even with a long calendar span and unknown total count');
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-b');
    assert.equal(await firstRule.locator('[data-overlap-warning]').isHidden(), true);
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-five');
    assert.equal(await firstRule.locator('[data-overlap-warning]').isVisible(), true,
      'A candidate with fewer than ten total observations proves insufficient overlap despite its long date span');
    returnedBacktest = {...backtest, overlap: {...backtest.overlap, observation_periods: 8,
      observation_periods_bound: 'upper', insufficient: true}};
    await page.getByLabel('第1条规则指标', {exact: true}).selectOption('synthetic-b');
    await page.getByLabel('第1条规则阈值', {exact: true}).fill('7');
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.locator('#monitor-backtest-result:not([hidden])').waitFor();
    assert.match(await page.locator('#monitor-backtest-result').innerText(), /规则来源数据重叠.*至多8 个观测期/);
    returnedBacktest = {...backtest, current_state: '无法判断', condition_state: '无法判断',
      trigger_count: 0, last_trigger: null, trigger_dates: [], intervals: [],
      series: [{...backtest.series[0], points: [], quantities: [], state: '无法判断', notice: sourceError},
        {...backtest.series[1], state: '触发', notice: ''}]};
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.waitForFunction(() => document.getElementById('monitor-backtest-result').textContent.includes('规则状态 无法判断'));
    assert.match(await page.locator('#monitor-backtest-result .monitor-summary').innerText(), /当前状态.*无法判断/s);
    assert.match(await page.locator('[data-monitor-chart="0"] .monitor-chart-note').innerText(), /规则状态 无法判断.*所选指标含未确认日期/s);
    assert.match(await page.locator('[data-monitor-chart="1"] .monitor-chart-note').innerText(), /规则状态 触发/);
    await page.locator('#monitor-logic').selectOption('any');
    returnedBacktest = {...returnedBacktest, current_state: '触发', condition_state: '触发'};
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.waitForFunction(() => document.querySelector('#monitor-backtest-result .monitor-summary strong')?.textContent === '触发');
    assert.match(await page.locator('[data-monitor-chart="0"] .monitor-chart-note').innerText(), /规则状态 无法判断.*所选指标含未确认日期/s);
    returnedBacktest = {...returnedBacktest, current_state: '数据停更', condition_state: '触发'};
    await page.getByRole('button', {name: '回测', exact: true}).click();
    await page.waitForFunction(() => document.querySelector('#monitor-backtest-result .monitor-summary strong')?.textContent === '数据停更');
    assert.match(await page.locator('#monitor-backtest-result .monitor-summary').innerText(), /当前状态.*数据停更.*组合条件.*触发/s);
    assert.match(await page.locator('[data-monitor-chart="0"] .monitor-chart-note').innerText(), /规则状态 无法判断.*所选指标含未确认日期/s);
    assert.equal(await page.evaluate(() => window.__monitorXss), undefined);
    assert.deepEqual(env.errors, []);
    const restrictedHtml = f.render('home.html', {restricted: true}, {affairs});
    assert.ok(!restrictedHtml.includes('id="monitors"'), 'The restricted view does not expose monitoring controls');
    console.log('PASS: monitor sentence → draft → candidate coverage and overlap warning → adjusted draft/saved windows → actual ChartKit trigger marker → save → personal list → check/toggle/delete; existing layout preserved at 1280px and 390px.');
  } finally { await env?.context.close(); await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
