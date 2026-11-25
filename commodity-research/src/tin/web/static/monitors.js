(function () {
  'use strict';
  const root = document.getElementById('monitors');
  if (!root) return;
  const get = id => document.getElementById('monitor-' + id);
  const api = (window.RESEARCH_PREFIX || '') + '/api/sn';
  let draft = null, busy = false, canWrite = false, tested = false, listToken = 0;
  let viewedItem = null, customDraftWindow = false;
  let monitorItems = [];
  let opener = null, editingItem = null, highlightedId = null, deepLinkId = null;
  const indicatorName = item => item.name && item.name !== item.series_id ? item.name : '未命名指标';
  const overlapWarning = '规则之间重叠数据不足，建议更换覆盖更长的指标';
  const displays = new Map();
  const setupDrafts = new Map();
  const savedSetupCandidates = new Set();
  function showMode(mode, trigger) {
    const create = mode === 'create';
    get('create-panel').hidden = !create;
    get('list-panel').hidden = create;
    get('dialog-create').hidden = create;
    get('refresh').hidden = create;
    get('dialog-list').hidden = !create;
    get('mode-title').textContent = create ? (editingItem ? '修订监测' : '新建监测') : '我的监测';
    if (trigger && !root.open) {
      opener = trigger;
      root.showModal();
    }
    if (root.open) {
      root.scrollTop = 0;
      (create ? get('sentence') : get('mode-title')).focus({preventScroll: true});
    }
  }
  function openDeepLink() {
    const id = new URLSearchParams(window.location.search).get('monitor');
    if (window.location.hash !== '#monitors' && !id) return false;
    highlightedId = deepLinkId = id;
    showMode('list', get('list-toggle'));
    return true;
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
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? (window.researchCopy?.(result.detail) ?? result.detail) : '操作未完成，请核对后重试。');
    return result;
  }
  function sync() {
    get('generate').disabled = draftJob.busy || generationDisabled();
    get('backtest').disabled = busy || !draft?.rules.length || draft.rules.some(rule => !rule.series_id);
    get('save').disabled = busy || !canWrite || !tested || !get('confirm').checked;
    get('setup-choice').disabled = busy;
    get('refresh').disabled = busy;
    get('dialog-create').disabled = busy;
    get('dialog-list').disabled = busy;
    for (const control of get('draft').querySelectorAll('input,select,button')) {
      if (!['monitor-save', 'monitor-backtest'].includes(control.id)) control.disabled = busy || control.dataset.unavailable === 'true';
    }
    for (const control of get('list').querySelectorAll('input,button')) {
      control.disabled = busy || (control.dataset.write === 'true' && !canWrite);
    }
    for (const control of get('view').querySelectorAll('input,button')) control.disabled = busy;
  }
  function generationDisabled() {
    return busy || !canWrite || !get('model').value || !get('sentence').value.trim();
  }
  function clearDisplay(target) {
    for (const view of displays.get(target) || []) { view.observer?.disconnect(); view.renderer?.destroy(); }
    displays.delete(target); target.replaceChildren();
  }
  function invalidate() {
    tested = false; get('confirm').checked = false;
    clearDisplay(get('backtest-result')); get('backtest-result').hidden = true; sync();
  }
  function clearDraft() {
    get('draft').hidden = true;
    get('setup-choice').value = '';
    draft = null;
    invalidate();
  }
  function openCreate(trigger, item = null, {discover = true} = {}) {
    if (!busy && item !== editingItem) {
      editingItem = item;
      get('sentence').value = item?.sentence || '';
      clearDraft();
    }
    showMode('create', trigger);
    if (discover) void draftJob.discover();
  }
  function body() {
    return {draft_id: draft.draft_id, title: get('title').value.trim(), sentence: draft.sentence,
      summary: get('summary').value.trim(),
      logic: get('logic').value, enabled: editingItem?.enabled ?? true,
      ...(draft.report_candidate ? {report_rule_indexes: draft.rules.map(rule => rule._originalIndex)} : {}),
      rules: draft.rules.map(rule => {
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
      const rule = draft.rules[index];
      row.querySelector('[data-coverage]').textContent = !rule.series_id ? '需研究员指定指标' :
        draft.fromSetup && !Object.keys(coverages[index]).length ? '回测时核对数据覆盖' : coverageText(coverages[index]);
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
  function indicatorSearch(index, choose) {
    const details = node('details', undefined, 'monitor-candidates');
    details.append(node('summary', '在授权指标目录中选取'));
    const fields = node('div', undefined, 'monitor-fields');
    const search = node('input'); search.type = 'search'; search.maxLength = 200;
    const lookup = node('button', '搜索', 'affairs-check'); lookup.type = 'button';
    const result = node('select'); result.setAttribute('aria-label', `第${index + 1}条规则目录搜索结果`);
    const previous = node('button', '上一页', 'affairs-check'); previous.type = 'button';
    const next = node('button', '下一页', 'affairs-check'); next.type = 'button';
    const message = node('p', '', 'affairs-row-detail'); message.setAttribute('role', 'status');
    let rows = [], offset = 0, nextOffset = null, query = '', token = 0;
    function updatePage() {
      previous.dataset.unavailable = String(offset === 0);
      next.dataset.unavailable = String(nextOffset == null);
      sync();
    }
    async function read(pageOffset) {
      const own = ++token;
      message.textContent = '正在读取指标目录…';
      try {
        const page = await window.ResearchCatalog.read({search: query, offset: pageOffset});
        if (token !== own || !details.isConnected) return;
        rows = page.indicators; offset = page.offset; nextOffset = page.next_offset;
        result.replaceChildren(new Option('请选择指标', ''), ...rows.map(item => new Option(
          [indicatorName(item), item.unit, item.sheet].filter(Boolean).join(' · '), item.series_id)));
        message.textContent = rows.length ? '' : '没有匹配指标。';
        updatePage();
      } catch (error) {
        if (token === own && details.isConnected) message.textContent = error.message;
      }
    }
    function startSearch() { query = search.value.trim(); void read(0); }
    lookup.addEventListener('click', startSearch);
    search.addEventListener('keydown', event => {
      if (event.key === 'Enter') { event.preventDefault(); startSearch(); }
    });
    details.addEventListener('toggle', () => { if (details.open && !token) startSearch(); });
    previous.addEventListener('click', () => void read(Math.max(0, offset - 200)));
    next.addEventListener('click', () => { if (nextOffset !== null) void read(nextOffset); });
    result.addEventListener('change', () => {
      const selected = rows.find(item => item.series_id === result.value);
      if (selected) choose(selected);
    });
    result.append(new Option('请选择指标', ''));
    previous.dataset.unavailable = next.dataset.unavailable = 'true';
    fields.append(field(`搜索第${index + 1}条规则指标`, search), lookup, result, previous, next);
    details.append(fields, message);
    return details;
  }
  function renderRules(overlap) {
    const rows = draft.rules.map((rule, index) => {
      const row = node('div', undefined, 'affairs-row monitor-rule'); row.dataset.monitorRule = String(index);
      const summary = node('div', undefined, 'affairs-row-title');
      function summarize() {
        const candidate = rule._candidates.find(item => item.series_id === rule.series_id);
        summary.textContent = `${candidate ? indicatorName(candidate) : rule.label} · ${rule.metric === 'change' ? '本期变化' : '本期值'} ${rule.operator} ${rule.value} · 连续 ${rule.consecutive_periods} 期`;
      }
      summarize(); row.append(summary);
      const controls = node('div', undefined, 'monitor-fields');
      const options = rule._candidates.map(item => [item.series_id,
        [indicatorName(item), item.unit, item.coverage ? coverageText(item) : ''].filter(Boolean).join(' · ')]);
      if (rule.needs_indicator) options.unshift(['', '需研究员指定指标']);
      const choices = select(options, rule.series_id || '', 'series_id');
      choices.setAttribute('aria-label', `第${index + 1}条规则指标`);
      function chooseIndicator() {
        rule.series_id = choices.value;
        summarize(); resetDraftWindow(); showRuleCoverage(); invalidate();
      }
      choices.addEventListener('change', chooseIndicator);
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
      remove.hidden = rule.needs_indicator === true;
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
      for (const candidate of rule._candidates) list.append(node('li',
        [indicatorName(candidate),
          candidate.source_file, candidate.sheet, candidate.unit,
          candidate.coverage ? coverageText(candidate) : '用历史数据试算时会检查可用日期'].filter(Boolean).join(' · ')));
      candidates.append(list);
      if (rule._candidates.length) row.append(candidates);
      if (rule.needs_indicator) row.append(indicatorSearch(index, selected => {
        if (!rule._candidates.some(item => item.series_id === selected.series_id)) {
          rule._candidates.push(selected);
          choices.append(new Option([indicatorName(selected), selected.unit, selected.sheet].filter(Boolean).join(' · '), selected.series_id));
        }
        choices.value = selected.series_id;
        chooseIndicator();
      }));
      return row;
    });
    get('rules').replaceChildren(...rows); showRuleCoverage(overlap); sync();
  }
  function setDraft(result, edits) {
    draft = {...result, rules: result.rules.map((rule, index) => ({...rule, _originalIndex: rule._originalIndex ?? index,
        _candidates: rule._candidates || result.candidates.filter(item => item.rule_index === index)}))};
    get('setup-candidates').hidden = !result.fromSetup;
    get('title').value = edits?.title ?? (editingItem?.title || result.title);
    get('summary').value = edits?.summary ?? editingItem?.summary ?? result.summary ?? '';
    get('logic').value = edits?.logic ?? result.logic;
    customDraftWindow = edits?.customWindow || false;
    setWindow('', edits?.window || result.default_window || {});
    get('confirmation-list').replaceChildren(...(result.confirmations || []).map(text => node('li', text)));
    if (!(result.confirmations || []).length) get('confirmation-list').append(node('li', '请核对所选指标、比较方式和连续期数是否符合原意。'));
    get('draft').hidden = false; invalidate(); renderRules(result.overlap);
  }
  function showSetupDraft(result, edits) {
    const setup = window.WorkbenchSetupReview;
    const candidate = result.setup_candidate ?? 0;
    get('setup-choice').replaceChildren(new Option('请选择监测候选', ''),
      ...(result.setup_titles || [result.title]).map((title, index) => {
        const saved = savedSetupCandidates.has(index);
        const option = new Option(title + (saved ? '（已保存）' : ''), index);
        option.disabled = saved;
        return option;
      }));
    get('setup-choice').value = String(candidate);
    const failures = result.setup_failures || [];
    get('setup-failures').replaceChildren(...failures.map(item => node('li', `第 ${item.index} 个候选未保留：${item.reason}`)));
    get('setup-failures').hidden = !failures.length;
    get('sentence').value = edits?.sentence ?? result.sentence;
    const confirmations = result.fromSetup ? result.confirmations : [setup.historicalNotice, ...(result.confirmations || [])];
    setDraft({...result, fromSetup: true, setup_candidate: candidate, confirmations}, edits);
    status('监测候选已载入，请逐条调整并回测。');
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
        if (item.state === '无法按日期判断') continue;
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
    if (viewedItem?.id !== item.id) get('view-source').open = false;
    get('view-sentence').textContent = item.sentence;
    viewedItem = item; setWindow('view-', result);
    get('view').hidden = false; get('view-title').textContent = item.title + ' · 历史回测';
    display(get('view-result'), result);
    if (scroll) get('view').scrollIntoView({block: 'nearest'});
    status('历史回测已更新。');
  }
  function closeView() {
    viewedItem = null;
    get('view').hidden = true;
    clearDisplay(get('view-result'));
  }
  function renderList(items) {
    const rows = items.map(item => {
      const row = node('div', undefined, 'affairs-row monitor-list-row'); row.dataset.monitorId = item.id;
      if (item.id === highlightedId) row.setAttribute('aria-current', 'true');
      const copy = node('div', undefined, 'monitor-list-copy');
      const title = node('div', undefined, 'affairs-row-title'); title.append(node('span', item.current_state || '未检查', 'affairs-row-kind'), node('span', item.title));
      const description = item.summary || item.rules.map(rule => rule.label).join(item.logic === 'any' ? ' 或 ' : ' 且 ');
      const detail = node('div', description, 'affairs-row-detail monitor-description'); detail.title = description;
      copy.append(title, detail, node('div',
        `最近触发 ${item.last_trigger || '无'} · 第 ${item.revision} 版` + (item.checked_at ? ' · 检查于 ' + item.checked_at.slice(0, 16).replace('T', ' ') : ''), 'affairs-row-detail'));
      for (const rule of item.rule_states || []) copy.append(node('p',
        [`第${rule.rule_index + 1}条规则`, rule.label, rule.state, rule.notice].filter(Boolean).join(' · '), 'affairs-row-detail'));
      const actions = node('div', undefined, 'monitor-actions');
      const enabled = node('input'); enabled.type = 'checkbox'; enabled.checked = item.enabled; enabled.dataset.write = 'true';
      enabled.setAttribute('aria-label', '启用 ' + item.title);
      enabled.addEventListener('change', () => void act(async () => {
        await request('/monitors/' + encodeURIComponent(item.id), {revision: item.revision, enabled: enabled.checked}, 'PATCH');
        await loadList({changed: true}); status(enabled.checked ? '监测已启用，下一次检查将核对条件。' : '监测已停用。');
      }, () => { enabled.checked = item.enabled; }));
      actions.append(field('启用', enabled));
      const view = node('button', '查看', 'affairs-check'); view.type = 'button';
      view.addEventListener('click', () => void act(() => viewItem(item)));
      const revise = node('button', '修订', 'affairs-check'); revise.type = 'button'; revise.dataset.write = 'true';
      revise.addEventListener('click', () => openCreate(null, item));
      const remove = node('button', '删除', 'affairs-check'); remove.type = 'button'; remove.dataset.write = 'true';
      remove.addEventListener('click', () => void act(async () => {
        await request('/monitors/' + encodeURIComponent(item.id) + '?revision=' + item.revision, undefined, 'DELETE');
        if (viewedItem?.id === item.id) closeView();
        await loadList({changed: true}); status('监测已删除，版本记录已保留。');
      })); actions.append(view, revise, remove); row.append(copy, actions); return row;
    });
    get('list').replaceChildren(...(rows.length ? rows : [node('div', '还没有保存监测。', 'affairs-empty')]));
    sync();
  }
  let pendingChange = false;
  async function loadList({reveal = false, changed = false} = {}) {
    pendingChange ||= changed;
    const token = ++listToken, result = await request('/monitors');
    if (token !== listToken) return;
    const items = result.items || [];
    monitorItems = items;
    canWrite = result.can_write === true; renderList(items);
    window.dispatchEvent(new CustomEvent('monitors-changed', {detail: {
      count: items.length, enabled: items.some(item => item.enabled), alerts: result.alerts, items, changed: pendingChange}}));
    pendingChange = false;
    if (root.open && !get('list-panel').hidden) {
      if (reveal || deepLinkId) get('list').querySelector('[aria-current=true]')?.scrollIntoView({block: 'nearest'});
      if (deepLinkId && !busy) {
        const item = items.find(item => item.id === deepLinkId);
        deepLinkId = null;
        if (item) void act(() => viewItem(item, {scroll: false}));
      }
    }
  }
  async function act(action, failure) {
    if (busy) return; busy = true; sync();
    try { await action(); } catch (error) { failure?.(); status(error.message, true); }
    finally { busy = false; sync(); }
  }
  const draftJob = ResearchAnalysisJobs.create({
    purpose: 'monitor_draft',
    button: get('generate'),
    status: get('status'),
    restoreButton: get('restore'),
    disabled: generationDisabled,
    isOpen: () => root.open && !get('create-panel').hidden,
    snapshot: () => [editingItem?.id, editingItem?.revision, get('sentence').value,
      draft && body(), get('start').value, get('end').value, get('confirm').checked],
    async present(result, {restored, current}) {
      const request = result.request;
      if (restored) {
        await initialList;
        if (!current()) return;
        const target = request.monitor_id && monitorItems.find(item => item.id === request.monitor_id);
        const sentence = get('sentence').value.trim();
        if (draft || (editingItem && editingItem.id !== request.monitor_id) || (sentence && sentence !== request.sentence)) {
          status('草稿已生成，保留研究员当前编辑内容。'); return;
        }
        if (request.monitor_id && (!target || target.revision !== request.revision)) {
          status('监测已更新，请刷新后重新生成草稿。', true); return;
        }
        editingItem = target || null;
        get('sentence').value = request.sentence;
        showMode('create');
      }
      setDraft(result); status('草稿已生成。请逐条核对指标和口径，再回测。');
    },
  });
  get('create-toggle').addEventListener('click', () => openCreate(get('create-toggle')));
  get('list-toggle').addEventListener('click', () => showMode('list', get('list-toggle')));
  get('dialog-create').addEventListener('click', () => openCreate());
  get('dialog-list').addEventListener('click', () => showMode('list'));
  get('close').addEventListener('click', () => root.close());
  root.addEventListener('close', () => {
    if (root.open) return;
    deepLinkId = null;
    opener?.focus({preventScroll: true});
  });
  function followDeepLink() {
    if (openDeepLink()) void loadList().catch(error => status(error.message, true));
  }
  window.addEventListener('hashchange', followDeepLink);
  document.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const link = event.target.closest('a[data-kind="monitor-resident"], a[data-monitor-toast]');
    if (!link) return;
    event.preventDefault();
    if (link.href !== window.location.href) window.history.pushState(null, '', link.href);
    followDeepLink();
  });
  openDeepLink();
  get('sentence').addEventListener('input', sync); get('model').addEventListener('change', sync);
  get('setup-choice').addEventListener('change', () => {
    const selected = get('setup-choice').value;
    const previous = draft?.setup_candidate;
    const restoreChoice = () => { get('setup-choice').value = previous ?? ''; };
    if (!selected) { restoreChoice(); return; }
    void act(async () => {
      const sentence = get('sentence').value;
      const cached = setupDrafts.get(Number(selected));
      const result = cached?.draft || await window.WorkbenchSetupReview.load({candidate: Number(selected)});
      if (!root.open || get('create-panel').hidden || get('sentence').value !== sentence) {
        restoreChoice(); return;
      }
      if (draft?.fromSetup) setupDrafts.set(previous, {draft, sentence, title: get('title').value, logic: get('logic').value,
        summary: get('summary').value,
        customWindow: customDraftWindow, window: {start: get('start').value, end: get('end').value}});
      showSetupDraft(result, cached);
    }, restoreChoice);
  });
  get('title').addEventListener('input', invalidate); get('logic').addEventListener('change', invalidate);
  for (const key of ['start', 'end']) get(key).addEventListener('input', () => { customDraftWindow = true; invalidate(); });
  get('confirm').addEventListener('change', sync);
  get('generate').addEventListener('click', () => draftJob.submit('/api/sn/monitors/draft', {
    sentence: get('sentence').value.trim(), model_id: get('model').value,
    ...(editingItem ? {monitor_id: editingItem.id, revision: editingItem.revision} : {}),
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
    if (draft.fromSetup && !window.confirm('请确认监测规则已经按当前判断调整，再保存并启用。')) return;
    const saved = editingItem
      ? await request('/monitors/' + encodeURIComponent(editingItem.id), {...body(), revision: editingItem.revision}, 'PATCH')
      : await request('/monitors', body(), 'POST');
    const message = editingItem ? '监测修订已保存。' : '监测已保存并启用。重新检查时会判断是否触发。';
    highlightedId = saved.id;
    if (viewedItem?.id === saved.id) closeView();
    const remaining = draft.fromSetup && (draft.setup_titles || []).length > savedSetupCandidates.size + 1;
    if (draft.fromSetup) {
      savedSetupCandidates.add(draft.setup_candidate);
      setupDrafts.delete(draft.setup_candidate);
      const option = get('setup-choice').selectedOptions[0];
      option.disabled = true;
      option.textContent += '（已保存）';
    }
    editingItem = null; get('sentence').value = ''; clearDraft();
    if (!remaining) showMode('list');
    else if (root.open) get('setup-choice').focus({preventScroll: true});
    await loadList({reveal: true, changed: true});
    status(remaining ? `${message} 可继续选择其他周报监测候选。` : message);
  }));
  get('cancel').addEventListener('click', clearDraft);
  get('view-close').addEventListener('click', closeView);
  get('view-backtest').addEventListener('click', () => { if (viewedItem) void act(() => viewItem(viewedItem, {adjust: true})); });
  get('refresh').addEventListener('click', () => void act(loadList));
  window.addEventListener('affairs-checked', () => { void loadList().catch(error => status(error.message, true)); });
  const initialList = loadList().catch(error => {
    get('list').replaceChildren(node('div', '监测读取失败，请刷新列表重试。', 'affairs-empty')); status(error.message, true);
  });
  void ResearchAnalysisJobs.requested('monitor_draft').then(async job => {
    const setup = window.WorkbenchSetupReview;
    if (!job && !await setup?.appliesTo('monitors')) return;
    await initialList;
    if (!canWrite || root.open || draft || editingItem || get('sentence').value) return;
    if (job) {
      openCreate(get('create-toggle'), null, {discover: false});
      return draftJob.restore(job.id, {explicit: true});
    }
    try {
      const result = await setup.load();
      if (root.open || draft || editingItem || get('sentence').value) return;
      openCreate(get('create-toggle'), null, {discover: false});
      showSetupDraft(result);
    } catch (error) { status(error.message, true); }
  });
  void request('/strategies/models').then(result => {
    get('model').replaceChildren(...(result.models || []).map(model => new Option(model.name, model.id)));
    if (!get('model').options.length) get('model').append(new Option('暂无可用模型', ''));
    get('model').disabled = false; sync();
  }).catch(error => { get('model').replaceChildren(new Option('模型读取失败', '')); status(error.message, true); });
})();
