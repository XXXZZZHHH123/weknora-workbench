/* Actual template and client; synthetic source/account responses, no service. */
const {assert, render, renderLibrary, launch, open, prefix} = require('./chart-ui-fixture.cjs');
const sid = index => 'kb-' + index.toString(16).padStart(48, '0');
const field = (index, personal, changes = {}) => ({series_id: sid(index), metric: '任意字段 ' + index,
  unit: '合成单位', frequency: '周', knowledge_base_id: personal ? 'personal-fixture' : 'default-fixture',
  knowledge_base_name: personal ? '个人库夹具' : 'default 库夹具', source_file: personal ? '个人副本.xlsx' : '中心.xlsx',
  sheet: '工作表 A', ...changes});
const evidence = changes => ({matching_periods: 12, mismatching_periods: 0, latest_mismatch_date: null,
  unit_match: true, frequency_match: true, personal_as_of: '2029-09-15', default_as_of: '2029-09-15',
  overlap_periods: 12, checked_points: 500, limited: true, precision_complete: true, stale_default: false, ...changes});
const pair = (id, index, changes = {}) => ({id, personal: field(index, true), default: field(index + 10, false),
  state: 'pending', revision: 0, history: [], codes: ['wind:S0000042'], evidence: evidence(), batch_eligible: true, ...changes});
const initial = [pair('strong', 1, {personal: field(1, true, {metric: '<img src=x onerror=window.__dedupXss=1>'}),
  default: field(11, false, {metric: '另一种文字名称'})}),
pair('weak', 2, {codes: [], batch_eligible: false, evidence: evidence({matching_periods: 11,
  mismatching_periods: 1, latest_mismatch_date: '2029-09-08'})}),
pair('unchecked', 3, {personal: field(3, true, {sheet: '工作表 B'}), evidence: null, batch_eligible: false})];
const html = render('indicator_dedup.html');

function mock(items = initial, options = {}) {
  const state = structuredClone(items);
  const snapshot = () => ({items: state, can_confirm: options.canConfirm !== false,
    pending_count: state.filter(item => item.state === 'pending').length, sources_revision: 7});
  const handler = ({path, method, body, reply}) => {
    if (!path.startsWith('/indicator-dedup')) return false;
    if (path === '/indicator-dedup' && method === 'GET') return reply(snapshot());
    const verify = path.match(/^\/indicator-dedup\/([^/]+)\/verify$/);
    if (verify) {
      assert.deepEqual(body, {sources_revision: 7});
      const item = state.find(value => value.id === verify[1]);
      if (!item.evidence) { item.evidence = evidence(); item.batch_eligible = true; }
      return reply(item);
    }
    if (options.conflict) return reply({detail: '对应已被其他研究员修改，请刷新后核对'}, 409);
    assert.equal(options.canConfirm === false, false, '只读账号不能提交决定或撤销');
    assert.equal(body.sources_revision, 7);
    if (path === '/indicator-dedup/decisions') {
      for (const id of body.ids) {
        const item = state.find(value => value.id === id);
        assert.equal(body.revisions[id], item.revision);
        if (body.batch) assert.equal(item.batch_eligible, true);
        item.state = body.state; item.revision++;
        item.history.push({actor: '<合成确认人>', at: '2029-09-15T00:00:00Z', state: item.state});
      }
      return reply(snapshot());
    }
    const undo = path.match(/^\/indicator-dedup\/([^/]+)\/undo$/);
    if (undo) {
      const item = state.find(value => value.id === undo[1]);
      assert.equal(body.revision, item.revision);
      item.state = 'pending'; item.revision++;
      item.history.push({actor: '<合成确认人>', at: '2029-09-16T00:00:00Z', action: 'undo', state: 'pending'});
      return reply(item);
    }
    return false;
  };
  return {state, handler};
}

(async () => {
  const browser = await launch();
  try {
    const source = mock();
    const view = await open(browser, prefix + '/sn/indicator-dedup', html, source.handler);
    const p = view.page;
    assert.equal(await p.locator('[data-dedup-row]').count(), 3);
    assert.equal(await p.locator('[data-dedup-group]').count(), 2, '按个人来源和工作表分组');
    assert.equal(view.calls.filter(call => call.method === 'POST').length, 0, '读取候选不决定对应');
    assert.match(await p.locator('[data-dedup-row="strong"]').innerText(), /另一种文字名称/);
    assert.equal(await p.locator('main img').count(), 0, '字段元数据作为文本呈现');
    assert.equal(await p.evaluate(() => window.__dedupXss), undefined);
    const weak = p.locator('[data-dedup-row="weak"]');
    assert.match(await weak.innerText(), /一致 11 期[\s\S]*不一致 1 期[\s\S]*2029-09-08/);
    assert.equal(await weak.locator('[data-dedup-select]').isDisabled(), true);
    assert.equal(await p.locator('[data-dedup-row="unchecked"] [data-dedup-select]').isDisabled(), true);
    assert.equal(await p.locator('[data-dedup-row="unchecked"] [data-dedup-decision="confirmed_default"]').isDisabled(), true,
      '未经数值核实不能确认为default');
    assert.equal(await p.locator('[data-dedup-row="unchecked"] [data-dedup-decision="keep_both"]').isEnabled(), true);
    await p.locator('[data-dedup-verify="unchecked"]').click();
    await p.waitForFunction(() => !document.querySelector('[data-dedup-row="unchecked"] [data-dedup-select]').disabled);
    assert.equal(source.state[2].state, 'pending', '核实证据不代替研究员确认');
    await p.locator('[data-dedup-select="strong"]').check();
    await p.locator('[data-dedup-select="unchecked"]').check();
    await p.locator('#dedup-batch-confirm').click();
    await p.locator('[data-dedup-undo="strong"]').waitFor();
    assert.deepEqual(source.state.map(item => item.state), ['confirmed_default', 'pending', 'confirmed_default']);
    const batch = view.calls.find(call => call.path === '/indicator-dedup/decisions');
    assert.deepEqual(batch.body.ids, ['strong', 'unchecked']);
    assert.equal(batch.body.batch, true);
    const labels = {keep_both:'保留两者', rejected:'不是同一指标', confirmed_default:'以 default 库为准'};
    for (const decision of ['keep_both', 'rejected', 'confirmed_default']) {
      await p.locator('[data-dedup-row="weak"] [data-dedup-decision="' + decision + '"]').click();
      await p.waitForFunction(expected => document.querySelector('[data-dedup-row="weak"] header strong').textContent === expected, labels[decision]);
      assert.equal(source.state[1].state, decision);
      await p.locator('[data-dedup-undo="weak"]').click();
      await p.waitForFunction(() => !document.querySelector('[data-dedup-undo="weak"]'));
      assert.equal(source.state[1].state, 'pending');
    }
    await p.locator('[data-dedup-row="weak"] details summary').click();
    assert.match(await p.locator('[data-dedup-row="weak"] details').innerText(), /决定与撤销记录[\s\S]*<合成确认人>/);
    await view.close();

    const readOnly = mock([pair('readonly', 1, {state: 'confirmed_default'})], {canConfirm: false});
    const readonly = await open(browser, prefix + '/sn/indicator-dedup', html, readOnly.handler);
    assert.match(await readonly.page.locator('#dedup-permission').innerText(), /仅可查看和核实[\s\S]*中心来源维护权限/);
    assert.equal(await readonly.page.locator('[data-dedup-decision]:enabled, [data-dedup-undo]:enabled').count(), 0);
    assert.equal(await readonly.page.locator('#dedup-batch-confirm').isDisabled(), true);
    assert.equal(await readonly.page.locator('[data-dedup-verify]').isEnabled(), true);
    assert.equal(readonly.calls.filter(call => call.method === 'POST').length, 0);
    await readonly.close();

    const conflictSource = mock([pair('conflict', 1)], {conflict: true});
    const conflict = await open(browser, prefix + '/sn/indicator-dedup', html, conflictSource.handler);
    await conflict.page.locator('[data-dedup-decision="confirmed_default"]').click();
    await conflict.page.locator('#dedup-status').filter({hasText: '对应已被其他研究员修改'}).waitFor();
    assert.equal(conflictSource.state[0].state, 'pending');
    await conflict.close();

    const unknown = await open(browser, prefix + '/sn/indicator-dedup', html,
      mock([pair('unknown', 1, {batch_eligible:false, evidence:evidence({precision_complete:false})})]).handler);
    assert.match(await unknown.page.locator('.dedup-evidence').innerText(), /显示精度无法核实[\s\S]*不能批量确认/);
    assert.equal(await unknown.page.locator('[data-dedup-select]').isDisabled(), true);
    assert.equal(await unknown.page.locator('[data-dedup-decision="confirmed_default"]').isEnabled(), true,
      '不能批量确认时仍可逐条审阅决定');
    await unknown.close();

    const stale = pair('stale', 1, {state: 'confirmed_default', evidence: evidence({stale_default: true,
      default_as_of: '2029-09-01', personal_as_of: '2029-09-15'})});
    const detailHtml = html.replace(/<main>[\s\S]*<\/main>/, '<main><section data-dedup-sources="' + sid(11) + '" hidden></section></main>');
    const detail = await open(browser, prefix + '/sn/indicators/' + sid(11), detailHtml, mock([stale]).handler);
    await detail.page.locator('[data-dedup-sources] .indicator-warning').waitFor();
    assert.match(await detail.page.locator('[data-dedup-sources]').innerText(), /已与个人库对应[\s\S]*default 库序列截至日期早于个人库副本/);
    assert.equal(detail.calls.some(call => call.path.endsWith('/decisions') || call.path.endsWith('/undo')), false);
    await detail.close();

    const catalogHtml = renderLibrary({}, {dedup_enabled: true});
    const catalogSource = mock();
    const catalog = await open(browser, prefix + '/sn/indicators?tab=catalog', catalogHtml, event => {
      if (event.path === '/indicators/catalog') return event.reply({indicators: [], total: 0, offset: 0, next_offset: null});
      return catalogSource.handler(event);
    });
    await catalog.page.locator('[data-dedup-banner]').filter({hasText: '发现 3 组可能重复的指标'}).waitFor();
    assert.equal(await catalog.page.locator('[data-dedup-banner] a').getAttribute('href'), prefix + '/sn/indicator-dedup');
    await catalog.close();

    const widgetHtml = kind => html.replace('</head>', '<script defer src="/static/dedup-references.js"></script></head>')
      .replace(/<main>[\s\S]*<\/main>/, '<main><div data-dedup-references="' + kind + '" data-dedup-reference-id="artifact-fixture"></div></main>');
    for (const kind of ['favorites', 'core', 'chart', 'ppt', 'judgment', 'strategy']) {
      let saved = false;
      const canRewrite = !['judgment', 'strategy'].includes(kind);
      const reference = await open(browser, prefix + '/sn/reference-fixture', widgetHtml(kind), event => {
        if (event.path === '/indicator-dedup/references') {
          assert.equal(event.url.searchParams.get('kind'), kind);
          return event.reply({items: saved ? [] : [{personal_name: '<个人原指标>', default_name:'中心指标',
            personal_id:sid(1), default_id:sid(11),
            url:prefix + '/sn/indicators/' + sid(11)}], related_charts:[], can_rewrite:canRewrite,
            revision:saved ? 4 : 3, sources_revision:7,
            reason:canRewrite ? '' : '研判与策略仅提示，请进入原记录核对后编辑'});
        }
        if (event.path === '/indicator-dedup/references/rewrite') {
          assert.equal(canRewrite, true);
          assert.deepEqual(event.body, {kind, id:'artifact-fixture', revision:3, sources_revision:7, confirmed:true,
            replacements:{[sid(1)]:sid(11)}});
          saved = true; return event.reply({revision:4});
        }
        return false;
      });
      const widget = reference.page.locator('[data-dedup-references]');
      await widget.filter({hasText:'可改为引用 default 库指标'}).waitFor();
      assert.equal(reference.calls.filter(call => call.method === 'POST').length, 0,
        kind + '读取引用仅提示，未经研究员确认不生成修订');
      assert.equal(await widget.locator('a').getAttribute('href'), prefix + '/sn/indicators/' + sid(11));
      if (canRewrite) {
        await widget.getByRole('button', {name:'确认改为 default 库指标并保存新修订'}).click();
        await reference.page.waitForFunction(() => document.querySelector('[data-dedup-references]').hidden);
        assert.equal(saved, true);
        assert.equal(reference.calls.filter(call => call.method === 'POST').length, 1);
      } else {
        assert.equal(await widget.locator('button').count(), 0);
        assert.match(await widget.innerText(), /研判与策略仅提示/);
      }
      await reference.close();
    }

    const restricted = await open(browser, prefix + '/sn/indicators?tab=catalog',
      renderLibrary({restricted:true}, {dedup_enabled:false, restricted_data:{owner_unavailable:false}}), () => false);
    assert.equal(await restricted.page.locator('[data-dedup-banner], [data-dedup-sources], [data-dedup-references]').count(), 0);
    assert.equal(restricted.calls.some(call => call.path.startsWith('/indicator-dedup')), false);
    await restricted.close();
    console.log('indicator-dedup-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
