(function () {
  'use strict';
  const root = document.getElementById('monitors');
  if (!root) return;
  const get = id => document.getElementById('monitor-' + id);
  const api = (window.RESEARCH_PREFIX || '') + '/api/sn';
  let draft = null, busy = false, canWrite = false, tested = false, listToken = 0;
  let viewedItem = null, customDraftWindow = false;
  const overlapWarning = '规则之间重叠数据不足，建议更换覆盖更长的指标';
  const selected = new URLSearchParams(window.location.search).get('monitor');
  let selectedOpened = false;
  const displays = new Map();
  function showPanel(name, visible) {
    get(name + '-panel').hidden = !visible;
    get(name + '-toggle').setAttribute('aria-expanded', String(visible));
    get('panels').hidden = get('create-panel').hidden && get('list-panel').hidden;
  }
  function openDeepLink() {
    if (window.location.hash !== '#monitors' && !selected) return;
    showPanel('list', true);
    root.scrollIntoView({block: 'start'});
  }
  function node(tag, text, className) {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = String(text);
    if (className) element.className = className;
    return element;
  }
  function status(message, error = false) {
    get('status').textContent = message; get('status').dataset.error = String(error);
  }
  async function request(path, body, method = 'GET') {
    const response = await researchFetch(api + path, {method, ...(body === undefined ? {} : {
      headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)})});
    let result;
    try { result = await response.json(); } catch (_) { throw new Error('服务返回异常，编辑内容已保留，请重试。'); }
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '操作未完成，请核对后重试。');
    return result;
  }
  function sync() {
    get('generate').disabled = busy || !canWrite || !get('model').value || !get('sentence').value.trim();
    get('backtest').disabled = busy || !draft || !draft.rules.length;
    get('save').disabled = busy || !canWrite || !tested || !get('confirm').checked;
    get('refresh').disabled = busy;
    for (const control of get('draft').querySelectorAll('input,select,button')) {
      if (!['monitor-save', 'monitor-backtest'].includes(control.id)) control.disabled = busy;
    }
    for (const control of get('list').querySelectorAll('input,button')) {
      control.disabled = busy || (control.dataset.write === 'true' && !canWrite);
    }
    for (const control of get('view').querySelectorAll('input,button')) control.disabled = busy;
  }
  function clearDisplay(target) {
    for (const view of displays.get(target) || []) { view.observer?.disconnect(); view.renderer?.destroy(); }
    displays.delete(target); target.replaceChildren();
  }
  function invalidate() {
    tested = false; get('confirm').checked = false;
    clearDisplay(get('backtest-result')); get('backtest-result').hidden = true; sync();
  }
  function body() {
    return {draft_id: draft.draft_id, title: get('title').value.trim(), sentence: draft.sentence,
      logic: get('logic').value, enabled: true, rules: draft.rules.map(rule => {
        const result = {};
        for (const key of ['series_id', 'metric', 'operator', 'value', 'consecutive_periods', 'label', 'definition']) result[key] = rule[key];
        return result;
      })};
  }
  function field(label, control) {
    const wrapper = node('label'); wrapper.append(node('span', label), control); return wrapper;
  }
  function select(options, value, name) {
    const control = node('select'); control.dataset.field = name;
    for (const [key, text] of options) control.append(new Option(text, key));
    control.value = value; return control;
  }
  function coverageText(candidate) {
    const coverage = candidate.coverage || candidate;
    const range = coverage.unknown ? `覆盖未知：${coverage.reason || '数据来源暂时不可读'}` :
      !coverage.end ? '暂无有效观测' : coverage.start ? `覆盖 ${coverage.start} 至 ${coverage.end}` :
      `首个观测日未核实${coverage.observed_start ? `，已读取 ${coverage.observed_start} 至 ${coverage.end}` : `，最新观测 ${coverage.end}`}`;
    return [range, coverage.limited ? '覆盖信息受读取上限限制' : '',
      coverage.frequency || candidate.frequency,
      coverage.point_count != null ? `${coverage.point_count} 个观测` : '',
      coverage.stale ? `数据停更（最新 ${coverage.end || '未知'}）` : ''].filter(Boolean).join(' · ');
  }
  function overlapText(label, overlap) {
    const bound = overlap.observation_periods_limited ? (overlap.observation_periods_bound === 'upper' ? '至多' : '至少') : '';
    const periods = overlap.observation_periods == null ? '观测期待核实' : `${bound}${overlap.observation_periods} 个观测期`;
    return `${label} ${overlap.start || '无'} 至 ${overlap.end || '无'} · ${overlap.days ?? '待核实'} 天 · ${periods}`;
  }
  function selectedCoverage() {
    return draft.rules.map(rule => rule._candidates.find(item => item.series_id === rule.series_id)?.coverage || {});
  }
  function showRuleCoverage(serverOverlap) {
    const coverages = selectedCoverage();
    const complete = coverages.every(item => item.start && item.end);
    const start = complete ? coverages.map(item => item.start).sort().at(-1) : null;
    const end = complete ? coverages.map(item => item.end).sort()[0] : null;
    const days = start && end ? Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 86400000) + 1) : null;
    const sparse = coverages.some(item => Number.isInteger(item.point_count) && item.point_count < 10);
    const insufficient = draft.rules.length > 1 && (serverOverlap?.insufficient || sparse || (days !== null && days < 180));
    for (const [index, row] of [...get('rules').children].entries()) {
      row.querySelector('[data-coverage]').textContent = coverageText(coverages[index]);
      const warning = row.querySelector('[data-overlap-warning]');
      warning.textContent = insufficient ? overlapWarning : '';
      warning.hidden = !insufficient;
    }
  }
  function setWindow(prefix, result) {
    get(prefix + 'start').value = result.start || '';
    get(prefix + 'end').value = result.end || '';
  }
  function windowParams(prefix) {
    const values = {};
    for (const key of ['start', 'end']) {
      const input = get(prefix + key);
      if (!input.checkValidity()) throw new Error('请填写有效回测日期。');
      if (input.value) values[key] = input.value;
    }
    return values;
  }
  function resetDraftWindow() {
    if (customDraftWindow) return;
    const coverages = selectedCoverage();
    if (!coverages.length || coverages.some(item => !item.end)) { setWindow('', {}); return; }
    const today = new Intl.DateTimeFormat('en-CA', {timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit'}).format(new Date());
    const end = [...coverages.map(item => item.end), today].sort()[0];
    const years = Math.min(...coverages.map(item => item.default_years || 2));
    const date = new Date(end + 'T00:00:00Z');
    const year = date.getUTCFullYear() - years, month = date.getUTCMonth();
    const day = Math.min(date.getUTCDate(), new Date(Date.UTC(year, month + 1, 0)).getUTCDate());
    setWindow('', {start: new Date(Date.UTC(year, month, day)).toISOString().slice(0, 10), end});
  }
  function renderRules(overlap) {
    const rows = draft.rules.map((rule, index) => {
      const row = node('div', undefined, 'affairs-row monitor-rule'); row.dataset.monitorRule = String(index);
      const summary = node('div', undefined, 'affairs-row-title');
      function summarize() {
        const candidate = rule._candidates.find(item => item.series_id === rule.series_id);
        summary.textContent = `${candidate?.name || rule.label} · ${rule.metric === 'change' ? '本期变化' : '本期值'} ${rule.operator} ${rule.value} · 连续 ${rule.consecutive_periods} 期`;
      }
      summarize(); row.append(summary);
      const controls = node('div', undefined, 'monitor-fields');
      const choices = select(rule._candidates.map(item => [item.series_id,
        [item.name, item.unit, coverageText(item)].filter(Boolean).join(' · ')]), rule.series_id, 'series_id');
      choices.setAttribute('aria-label', `第${index + 1}条规则指标`);
      choices.addEventListener('change', () => { rule.series_id = choices.value; summarize(); resetDraftWindow(); showRuleCoverage(); invalidate(); });
      controls.append(field('指标', choices));
      const metric = select([['value', '本期值'], ['change', '本期变化']], rule.metric, 'metric');
      const operator = select(['>', '>=', '<', '<='].map(value => [value, value]), rule.operator, 'operator');
      for (const [label, control, key] of [['口径', metric, 'metric'], ['比较', operator, 'operator']]) {
        control.setAttribute('aria-label', `第${index + 1}条规则${label}`);
        control.addEventListener('change', () => { rule[key] = control.value; summarize(); invalidate(); });
        controls.append(field(label, control));
      }
      for (const [label, key] of [['阈值', 'value'], ['连续期数', 'consecutive_periods']]) {
        const input = node('input'); input.type = 'number'; input.value = rule[key]; input.dataset.field = key;
        input.step = key === 'value' ? 'any' : '1';
        if (key === 'consecutive_periods') { input.min = '1'; input.max = '120'; }
        input.required = true; input.setAttribute('aria-label', `第${index + 1}条规则${label}`);
        input.addEventListener('input', () => {
          rule[key] = input.value === '' ? null : Number(input.value); summarize(); invalidate();
        }); controls.append(field(label, input));
      }
      const remove = node('button', '删除规则', 'affairs-check'); remove.type = 'button';
      remove.addEventListener('click', () => { draft.rules.splice(index, 1); resetDraftWindow(); invalidate(); renderRules(); });
      controls.append(remove); row.append(controls);
      const definition = node('input'); definition.type = 'text'; definition.maxLength = 1000;
      definition.value = rule.definition; definition.className = 'monitor-rule-definition';
      definition.setAttribute('aria-label', `第${index + 1}条规则口径说明`);
      definition.addEventListener('input', () => { rule.definition = definition.value; invalidate(); });
      row.append(field('口径说明', definition));
      const coverage = node('p', '', 'affairs-row-detail'); coverage.dataset.coverage = 'true'; row.append(coverage);
      const warning = node('p', '', 'monitor-warning'); warning.dataset.overlapWarning = 'true'; warning.hidden = true; row.append(warning);
      const candidates = node('details', undefined, 'monitor-candidates'); candidates.append(node('summary', '候选指标与数据覆盖'));
      const list = node('ul', undefined, 'affairs-row-detail');
      for (const candidate of rule._candidates) list.append(node('li', `${candidate.name || candidate.series_id} · ${coverageText(candidate)}`));
      candidates.append(list); row.append(candidates); return row;
    });
    get('rules').replaceChildren(...rows); showRuleCoverage(overlap); sync();
  }
  function setDraft(result) {
    draft = {...result, rules: result.rules.map((rule, index) => ({...rule,
      _candidates: result.candidates.filter(item => item.rule_index === index)}))};
    get('title').value = result.title; get('logic').value = result.logic;
    customDraftWindow = false; setWindow('', result.default_window || {});
    get('confirmation-list').replaceChildren(...(result.confirmations || []).map(text => node('li', text)));
    if (!(result.confirmations || []).length) get('confirmation-list').append(node('li', '请核对所选指标、比较方式和连续期数是否符合原意。'));
    get('draft').hidden = false; invalidate(); renderRules(result.overlap);
  }
  function display(target, result) {
    clearDisplay(target); target.hidden = false;
    const views = []; displays.set(target, views);
    const summary = node('div', undefined, 'monitor-summary');
    for (const [label, value] of [['当前状态', result.current_state], ['触发次数', result.trigger_count], ['最近触发', result.last_trigger || '无']]) {
      const card = node('div', undefined, 'research-stat'); card.append(node('small', label), node('strong', value ?? '—')); summary.append(card);
    }
    if (result.condition_state && result.condition_state !== result.current_state) {
      const card = node('div', undefined, 'research-stat');
      card.append(node('small', '组合条件'), node('strong', result.condition_state)); summary.append(card);
    }
    target.append(summary);
    if (result.start || result.end) target.append(node('p', `回测范围 ${result.start || '—'} 至 ${result.end || '—'}，按各指标最近已知状态组合，不插值、不填零。`, 'affairs-row-detail'));
    if (result.window_reason) target.append(node('p', result.window_reason, 'affairs-row-detail'));
    if (result.overlap) {
      const overlap = result.overlap;
      target.append(node('p', overlapText('规则来源数据重叠', overlap), 'affairs-row-detail'));
      if (overlap.insufficient) target.append(node('p', overlap.warning || overlapWarning, 'monitor-warning'));
    }
    if (result.window_overlap) target.append(node('p', overlapText('本次回测窗口重叠', result.window_overlap), 'affairs-row-detail'));
    for (const item of result.rule_coverage || []) target.append(node('p', `第${item.rule_index + 1}条规则 · ${coverageText(item)}`, 'affairs-row-detail'));
    const triggerDetails = node('details'); triggerDetails.append(node('summary', '触发日期与持续期数'));
    const triggers = node('ul', undefined, 'monitor-trigger-list');
    const intervals = result.intervals || [];
    if (intervals.length) for (const interval of intervals) triggers.append(node('li',
      `${interval.start} 至 ${interval.end || '当前'} · 持续 ${interval.periods ?? interval.duration_periods ?? interval.duration_days ?? '—'} 期`));
    else triggers.append(node('li', '回测范围内没有触发记录。'));
    triggerDetails.append(triggers); target.append(triggerDetails);
    const markers = (result.trigger_dates || []).map(date => ({date, label: '组合条件进入触发'}));
    for (const [index, item] of (result.series || []).entries()) {
      const rule = item.rule || draft?.rules[index] || {};
      const section = node('div', undefined, 'monitor-chart'); section.dataset.monitorChart = String(index);
      section.append(node('div', rule.label || item.name || '指标走势', 'affairs-row-title'));
      section.append(node('p', [item.state ? '规则状态 ' + item.state : '', item.name, item.unit, item.frequency,
        item.latest_date ? '最近观测 ' + item.latest_date : '', item.notice].filter(Boolean).join(' · '), 'affairs-row-detail monitor-chart-note'));
      const plot = node('div'); section.append(plot); target.append(section);
      const points = (item.points || []).map(point => ({...point, raw: point.value}));
      if (!points.some(point => Number.isFinite(point.value))) {
        plot.append(node('p', item.state === '无法判断' ? '指标暂不可读，无法展示观测。' : '该指标在回测范围内无有效观测。', 'affairs-empty'));
        continue;
      }
      const source = item.series_id, changeId = source + '::monitor-change';
      const curves = [{series_id: source, label: item.name || rule.label || '本期值', unit: item.unit || '', points, axis: 'left', kind: 'line'}];
      if (rule.metric === 'change') curves.push({series_id: changeId, label: '本期变化', unit: item.unit || '',
        points: (item.quantities || []).map(point => ({...point, raw: point.value})), axis: 'right', kind: 'line'});
      const thresholdSeries = rule.metric === 'change' ? changeId : source;
      const overlays = [{kind: 'fixed', series_id: thresholdSeries, rule_id: 'monitor-' + index,
        name: `${rule.metric === 'change' ? '本期变化' : '本期值'} ${rule.operator} ${rule.value}`, value: rule.value}];
      const view = {renderer: null, observer: null, width: null}; views.push(view);
      const draw = () => {
        const width = Math.max(280, Math.round(plot.clientWidth || 600));
        if (view.width === width) return; view.width = width; view.renderer?.destroy();
        try { view.renderer = window.ChartKit.render(plot, {curves, unit: item.unit || ''}, {
          width, height: 280, markers, overlays, dateRange: {start: result.start, end: result.end}}); }
        catch (error) { plot.textContent = '图表未显示：' + error.message; }
      };
      draw();
      if (typeof ResizeObserver === 'function') { view.observer = new ResizeObserver(draw); view.observer.observe(plot); }
    }
  }
  async function viewItem(item, {adjust = false, scroll = true} = {}) {
    const query = adjust ? new URLSearchParams(windowParams('view-')).toString() : '';
    const result = await request('/monitors/' + encodeURIComponent(item.id) + '/backtest' + (query ? '?' + query : ''));
    viewedItem = item; setWindow('view-', result);
    get('view').hidden = false; get('view-title').textContent = item.title + ' · 历史回测';
    display(get('view-result'), result);
    if (scroll) get('view').scrollIntoView({block: 'nearest'});
    status('历史回测已更新。');
  }
  function renderList(items) {
    const rows = items.map(item => {
      const row = node('div', undefined, 'affairs-row monitor-list-row'); row.dataset.monitorId = item.id;
      if (item.id === selected) row.setAttribute('aria-current', 'true');
      const copy = node('div', undefined, 'monitor-list-copy');
      const title = node('div', undefined, 'affairs-row-title'); title.append(node('span', item.current_state || '未检查', 'affairs-row-kind'), node('span', item.title));
      copy.append(title, node('div', item.sentence, 'affairs-row-detail'), node('div',
        `最近触发 ${item.last_trigger || '无'} · v${item.revision}` + (item.checked_at ? ' · 检查于 ' + item.checked_at.slice(0, 16).replace('T', ' ') : ''), 'affairs-row-detail'));
      for (const rule of item.rule_states || []) copy.append(node('p',
        [`第${rule.rule_index + 1}条规则`, rule.label, rule.state, rule.notice].filter(Boolean).join(' · '), 'affairs-row-detail'));
      const actions = node('div', undefined, 'monitor-actions');
      const enabled = node('input'); enabled.type = 'checkbox'; enabled.checked = item.enabled; enabled.dataset.write = 'true';
      enabled.setAttribute('aria-label', '启用 ' + item.title);
      enabled.addEventListener('change', () => void act(async () => {
        await request('/monitors/' + encodeURIComponent(item.id), {revision: item.revision, enabled: enabled.checked}, 'PATCH');
        await loadList(); status(enabled.checked ? '监测已启用，下一次检查将核对条件。' : '监测已停用。');
      }, () => { enabled.checked = item.enabled; }));
      actions.append(field('启用', enabled));
      const view = node('button', '查看', 'affairs-check'); view.type = 'button';
      view.addEventListener('click', () => void act(() => viewItem(item)));
      const remove = node('button', '删除', 'affairs-check'); remove.type = 'button'; remove.dataset.write = 'true';
      remove.addEventListener('click', () => void act(async () => {
        await request('/monitors/' + encodeURIComponent(item.id) + '?revision=' + item.revision, undefined, 'DELETE');
        get('view').hidden = true; clearDisplay(get('view-result')); await loadList(); status('监测已删除，版本记录已保留。');
      })); actions.append(view, remove); row.append(copy, actions); return row;
    });
    get('list').replaceChildren(...(rows.length ? rows : [node('div', '还没有保存监测。先用一句话生成草稿。', 'affairs-empty')]));
    sync();
  }
  async function loadList() {
    const token = ++listToken, result = await request('/monitors');
    if (token !== listToken) return;
    const items = result.items || [];
    canWrite = result.can_write === true; renderList(items);
    get('list-toggle').textContent = '我的监测 ' + items.length;
    window.dispatchEvent(new CustomEvent('monitors-changed', {detail: {
      enabled: items.some(item => item.enabled), alerts: result.alerts}}));
    if (selected && !selectedOpened && !busy) {
      const item = items.find(item => item.id === selected);
      if (item) { selectedOpened = true; void act(() => viewItem(item, {scroll: false})); }
    }
  }
  async function act(action, failure) {
    if (busy) return; busy = true; sync();
    try { await action(); } catch (error) { failure?.(); status(error.message, true); }
    finally { busy = false; sync(); }
  }
  for (const name of ['create', 'list']) {
    get(name + '-toggle').addEventListener('click', () => showPanel(name, get(name + '-panel').hidden));
  }
  window.addEventListener('hashchange', openDeepLink);
  openDeepLink();
  get('sentence').addEventListener('input', sync); get('model').addEventListener('change', sync);
  get('title').addEventListener('input', invalidate); get('logic').addEventListener('change', invalidate);
  for (const key of ['start', 'end']) get(key).addEventListener('input', () => { customDraftWindow = true; invalidate(); });
  get('confirm').addEventListener('change', sync);
  get('generate').addEventListener('click', () => void act(async () => {
    status('正在理解原话、召回并核对授权指标…');
    const result = await request('/monitors/draft', {sentence: get('sentence').value.trim(), model_id: get('model').value}, 'POST');
    setDraft(result); status('草稿已生成。请逐条核对指标和口径，再回测。');
  }));
  get('backtest').addEventListener('click', () => void act(async () => {
    for (const input of get('draft').querySelectorAll('input[type=number]')) if (!input.checkValidity()) throw new Error('请填写有效阈值和连续期数。');
    status('正在读取历史观测并回测…');
    const result = await request('/monitors/backtest', {...body(), ...(customDraftWindow ? windowParams('') : {})}, 'POST');
    setWindow('', result); showRuleCoverage(result.overlap);
    display(get('backtest-result'), result); tested = true; status('回测完成。核对规则与口径后可保存监测。');
  }));
  get('save').addEventListener('click', () => void act(async () => {
    if (!tested || !get('confirm').checked) return;
    await request('/monitors', body(), 'POST'); await loadList(); get('draft').hidden = true;
    draft = null; tested = false; clearDisplay(get('backtest-result')); status('监测已保存并启用。重新检查时会判断是否触发。');
  }));
  get('cancel').addEventListener('click', () => { get('draft').hidden = true; clearDisplay(get('backtest-result')); draft = null; tested = false; sync(); });
  get('view-close').addEventListener('click', () => { get('view').hidden = true; clearDisplay(get('view-result')); });
  get('view-backtest').addEventListener('click', () => { if (viewedItem) void act(() => viewItem(viewedItem, {adjust: true})); });
  get('refresh').addEventListener('click', () => void act(loadList));
  window.addEventListener('affairs-checked', () => { void loadList().catch(error => status(error.message, true)); });
  void loadList().catch(error => { get('list').replaceChildren(node('div', '监测读取失败，请刷新列表重试。', 'affairs-empty')); status(error.message, true); });
  void request('/strategies/models').then(result => {
    get('model').replaceChildren(...(result.models || []).map(model => new Option(model.name, model.id)));
    if (!get('model').options.length) get('model').append(new Option('暂无可用模型', ''));
    get('model').disabled = false; sync();
  }).catch(error => { get('model').replaceChildren(new Option('模型读取失败', '')); status(error.message, true); });
})();
