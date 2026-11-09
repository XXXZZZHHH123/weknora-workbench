(function () {
  'use strict';
  const node = document.getElementById('indicator-cycle-config'); if (!node) return;
  const config = JSON.parse(node.textContent), U = window.ChartUI;
  const $ = id => document.getElementById(id), select = $('indicator-cycle-template'), save = $('indicator-cycle-save'), usage = $('indicator-chart-usage');
  if (!config.enabled) {
    U.notice($('indicator-cycle-status'), config.reason, 'warning'); U.notice($('indicator-related-status'), config.reason, 'warning');
    if (usage) usage.textContent = config.reason; return;
  }
  let items = [], chart, payload, token = 0, next = 0, relatedBusy = false, started = null, view = 'trend', table = 'time';
  U.exports($('indicator-cycle-exports'), () => chart, () => config.name + ' · 周期对比');
  async function loadCycle() {
    const own = ++token; save.disabled = true; payload = null; chart?.destroy(); chart = null;
    $('indicator-cycle-statistics').hidden = true; U.notice($('indicator-cycle-status'), '正在读取周期对比…');
    yearStatus('正在读取往年数据…');
    const query = new URLSearchParams({template: select.value}); if (config.date) query.set('date', config.date);
    try {
      const result = await U.api('/indicators/' + encodeURIComponent(config.series_id) + '/cycle?' + query);
      if (own !== token) return;
      payload = result; chart = window.ChartKit.render($('indicator-cycle-plot'), result, {title: config.name + ' · 周期对比'});
      U.stats($('indicator-cycle-statistics'), result);
      // Same-period ranks and ranges need at least three past cycles; say so instead of an empty table.
      U.notice($('indicator-cycle-status'), result.position?.status && result.position.status !== 'ok'
        ? '往年数据不足 3 个周期，同期排名和区间暂不计算，曲线仅供对照。' : '', 'warning');
      save.disabled = !config.can_write; renderYears();
    } catch (error) { if (own === token) { U.notice($('indicator-cycle-status'), error.message, 'error'); yearStatus(error.message); } }
  }
  async function templates() {
    try {
      const result = await U.api('/cycle-templates'); items = result.items;
      U.fillTemplates(select, items, items.find(item => item.builtin)?.id);
      select.disabled = !items.length;
      if (items.length) await loadCycle();
      else { U.notice($('indicator-cycle-status'), '当前没有可用的周期模板'); yearStatus('当前没有可用的周期模板'); }
    } catch (error) { U.notice($('indicator-cycle-status'), error.message, 'error'); yearStatus(error.message); }
  }
  // The cycle is read only when someone opens 同期对比 or 按年份分列.
  const ensureCycle = () => started || (started = templates());
  const remember = (key, value) => { try { localStorage.setItem('tin.indicator.' + key, value); } catch (_) { /* Per-browser preference only. */ } };
  const recall = key => { try { return localStorage.getItem('tin.indicator.' + key); } catch (_) { return null; } };
  function showView(value, persist = true) {
    view = value === 'cycle' ? 'cycle' : 'trend';
    document.querySelectorAll('[data-ind-view]').forEach(button => button.setAttribute('aria-selected', String(button.dataset.indView === view)));
    $('ind-trend-view').hidden = view !== 'trend'; $('indicator-cycle-panel').hidden = view !== 'cycle';
    const title = $('ind-view-title'), range = $('ind-trend-range');
    title.textContent = view === 'cycle' ? '同期对比' : title.dataset.trendTitle; if (range) range.hidden = view !== 'trend';
    if (persist) remember('view', view);
    if (view === 'cycle') ensureCycle();
  }
  function showTable(value, persist = true) {
    table = value === 'year' ? 'year' : 'time';
    document.querySelectorAll('[data-ind-table]').forEach(button => button.setAttribute('aria-selected', String(button.dataset.indTable === table)));
    if ($('ind-time-table')) $('ind-time-table').hidden = table !== 'time';
    if ($('ind-year-table')) $('ind-year-table').hidden = table !== 'year';
    if (persist) remember('table', table);
    if (table === 'year') { renderYears(); ensureCycle(); }
  }
  function yearStatus(text) { if ($('ind-year-status')) $('ind-year-status').textContent = text; }
  // Columns are the template's cycles (newest first); rows are the same position in each cycle.
  // Only real observations are shown; a missing cell stays empty and is never filled.
  function yearRows(data) {
    const freq = data.freq_days || 1, event = data.template?.kind === 'event';
    const segments = (data.segments || []).filter(part => part.points?.length).slice().sort((a, b) => b.start.localeCompare(a.start));
    const rows = new Map();
    const position = point => {
      if (event) {
        const step = freq >= 5 ? 7 : 1, index = Math.floor(point.x / step), unit = step === 7 ? '周' : '天';
        return [index, index < 0 ? `前${-index}${unit}` : index === 0 ? (step === 7 ? '当周' : '当天') : `后${index}${unit}`];
      }
      if (freq >= 28) return [point.date.slice(5, 7), `${Number(point.date.slice(5, 7))}月`];
      if (freq >= 5) { const week = Math.floor(point.x / 7) + 1; return [week, `第${week}周`]; }
      return [point.date.slice(5), point.date.slice(5)];
    };
    segments.forEach((part, column) => part.points.forEach(point => {
      const raw = Number.isFinite(point.raw) ? point.raw : point.value;
      if (!Number.isFinite(raw) || !Number.isFinite(point.x)) return;
      const [key, label] = position(point), row = rows.get(key) || {label, order: point.x, cells: []};
      row.order = Math.min(row.order, point.x);
      const cell = row.cells[column];
      if (!cell || cell.date < point.date) row.cells[column] = {date: point.date, value: raw};
      rows.set(key, row);
    }));
    return {columns: segments.map(part => part.label + (part.current ? '（当前）' : '')), rows: [...rows.values()].sort((a, b) => a.order - b.order)};
  }
  const number = value => new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 4}).format(value);
  function renderYears() {
    const box = $('ind-year-table'); if (!box || box.hidden) return;
    if (!payload) return;
    const {columns, rows} = yearRows(payload), head = box.querySelector('thead'), body = box.querySelector('tbody');
    const template = items.find(item => item.id === select.value)?.name || payload.template?.name || '周期模板';
    yearStatus(rows.length ? `按「${template}」分列${payload.unit ? '，单位：' + payload.unit : ''}；空格表示该期没有观测，不补值。` : '暂无可分列的观测。');
    const headRow = document.createElement('tr'), first = document.createElement('th'); first.className = 'py-1'; first.textContent = '时点'; headRow.append(first);
    columns.forEach(label => { const cell = document.createElement('th'); cell.className = 'text-right px-2'; cell.textContent = label; headRow.append(cell); });
    head.replaceChildren(headRow);
    body.replaceChildren(...rows.map(row => {
      const line = document.createElement('tr'), label = document.createElement('td'); line.className = 'border-t border-[var(--line)]';
      label.className = 'py-1'; label.textContent = row.label; line.append(label);
      columns.forEach((_, column) => {
        const cell = document.createElement('td'), value = row.cells[column]; cell.className = 'text-right px-2';
        cell.textContent = value ? number(value.value) : ''; if (value) cell.title = value.date; line.append(cell);
      });
      return line;
    }));
  }
  window.IndicatorYearTable = {csv() {
    if (table !== 'year' || !payload) return null;
    const {columns, rows} = yearRows(payload), quote = text => /[",\n]/.test(text) ? '"' + text.replace(/"/g, '""') + '"' : text;
    return {rows: rows.length, text: [['时点', ...columns], ...rows.map(row => [row.label, ...columns.map((_, column) => row.cells[column] ? String(row.cells[column].value) : '')])]
      .map(line => line.map(quote).join(',')).join('\n')};
  }};
  document.querySelectorAll('[data-ind-view]').forEach(button => button.addEventListener('click', () => showView(button.dataset.indView)));
  document.querySelectorAll('[data-ind-table]').forEach(button => button.addEventListener('click', () => showTable(button.dataset.indTable)));
  async function related() {
    if (next === null || relatedBusy) return;
    relatedBusy = true; $('indicator-related-more').disabled = true;
    try {
      const result = await U.api('/charts?' + new URLSearchParams({series_id: config.series_id, offset: next, limit: 20}));
      if (usage) usage.textContent = `图表库中有 ${result.total} 张图表引用这条指标`;
      for (const item of result.items) {
        const link = document.createElement('a'); link.className = 'charts-card'; link.href = U.pageURL('/sn/charts/' + encodeURIComponent(item.id));
        link.textContent = `${item.definition.title} · ${item.definition.group || '未分组'} · v${item.revision}`;
        $('indicator-related-charts').append(link);
      }
      U.notice($('indicator-related-status'), result.total ? '' : '暂时没有包含此指标的图表。');
      next = result.next_offset; $('indicator-related-more').hidden = next === null;
    } catch (error) { U.notice($('indicator-related-status'), error.message, 'error'); if (usage) usage.textContent = '图表引用数暂不可用：' + error.message; }
    finally { relatedBusy = false; $('indicator-related-more').disabled = false; }
  }
  save.addEventListener('click', async () => {
    if (!config.can_write || !payload || save.disabled) return;
    const template = payload.template || items.find(item => item.id === select.value)?.template;
    if (!template) return;
    save.disabled = true;
    const definition = {format: 'chart.v1', title: (config.name + ' · ' + template.name).slice(0, 120), group: '', note: '',
      curves: [{series_id: config.series_id, label: config.name.slice(0, 120), kind: 'line', axis: 'left', style: {}}],
      time: {mode: 'cycle', template, cycles: 8, band_cycles: 5, transform: template.transform || 'raw'}, origin: {kind: 'manual', ref: ''}};
    try {
      const result = await U.api('/charts', {method: 'POST', body: JSON.stringify(definition)});
      U.notice($('indicator-cycle-status'), '已保存为共享图表。');
      const link = document.createElement('a'); link.className = 'charts-button'; link.textContent = '打开已保存图表'; link.href = U.pageURL('/sn/charts/' + encodeURIComponent(result.id)); $('indicator-cycle-status').append(' ', link);
      next = 0; $('indicator-related-charts').replaceChildren(); await related();
    } catch (error) { U.notice($('indicator-cycle-status'), error.message, 'error'); }
    finally { save.disabled = !config.can_write || !payload; }
  });
  select.addEventListener('change', loadCycle); $('indicator-related-more').addEventListener('click', related);
  window.addEventListener('pagehide', () => { token++; chart?.destroy(); }, {once: true});
  // Default is the plain trend; the last choice in this browser is restored.
  showView(recall('view') === 'cycle' ? 'cycle' : 'trend', false);
  if ($('ind-year-table')) showTable(recall('table') === 'year' ? 'year' : 'time', false);
  related();
})();
