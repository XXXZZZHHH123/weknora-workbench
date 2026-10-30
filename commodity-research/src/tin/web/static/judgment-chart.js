(() => {
  const svgNS = 'http://www.w3.org/2000/svg';
  const colors = ['#ED7D31', '#4472C4', '#8064A2', '#6B8E23'];
  const pending = new Map();
  const number = value => new Intl.NumberFormat('zh-CN', {maximumSignificantDigits: 15}).format(value);

  function movingAverage(timeline, window) {
    if (!Number.isInteger(window) || window < 2 || window > 120) return timeline.map(() => null);
    return timeline.map((_, index) => {
      if (index + 1 < window) return null;
      const samples = timeline.slice(index + 1 - window, index + 1);
      if (samples.some(point => typeof point.value !== 'number' || !Number.isFinite(point.value))) return null;
      // Display only: rule evaluation uses the server's exact decimal calculation.
      const value = samples.reduce((sum, point) => sum + point.value / window, 0);
      return Number.isFinite(value) ? value : null;
    });
  }

  function streakText(streak, semantics = 'change') {
    if (!streak) return '';
    const up = streak.direction === 'up';
    const label = semantics === 'inventory' ? (up ? '累库' : '去库') : (up ? '连升' : '连降');
    return `${up ? '↑' : '↓'} ${streak.at_least ? '至少' : ''}${label}${streak.count}${streak.period}`;
  }

  function boundaryText(boundary, unit = '') {
    const operator = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于', between: '区间边界'}[boundary.operator] || '';
    return `${boundary.name} ${operator} ${number(boundary.value)} ${unit}`.trim();
  }

  function shape(timeline, boundaries, selectedWindow) {
    const points = timeline.map(point => ({date: point.date, value: point.value, time: Date.parse(point.date)}));
    const configured = [...new Set(boundaries.filter(item => item.kind === 'sma').map(item => item.window))];
    if (selectedWindow && !configured.includes(selectedWindow)) configured.push(selectedWindow);
    const averages = configured.map(window => ({window, values: movingAverage(points, window)}));
    const values = points.filter(point => Number.isFinite(point.value) && Number.isFinite(point.time)).map(point => point.value);
    for (const boundary of boundaries) if (boundary.kind === 'fixed' && Number.isFinite(boundary.value)) values.push(boundary.value);
    for (const average of averages) for (const value of average.values) if (value !== null) values.push(value);
    if (!values.length) return {points: [], averages: [], fixed: []};
    const low = Math.min(...values), high = Math.max(...values);
    const denominator = Math.max(Math.abs(low), Math.abs(high), 1);
    const scaledLow = low / denominator, scaledHigh = high / denominator;
    const span = scaledHigh - scaledLow;
    const padding = span ? span * .08 : Math.max(Math.abs(scaledHigh) * .04, 1 / denominator);
    const minimum = scaledLow - padding, maximum = scaledHigh + padding;
    const start = points.find(point => Number.isFinite(point.time))?.time;
    const end = points.findLast(point => Number.isFinite(point.time))?.time;
    const xOf = time => start === end ? 240 : 12 + (time - start) / (end - start) * 456;
    // Scale before subtracting: two finite opposite-signed extremes may overflow their difference.
    const scaledSpan = maximum - minimum;
    const yOf = value => scaledSpan ? 10 + (maximum - value / denominator) / scaledSpan * 102 : 61;
    for (const point of points) {
      point.x = xOf(point.time);
      point.y = typeof point.value === 'number' && Number.isFinite(point.value) ? yOf(point.value) : null;
    }
    return {points, averages: averages.map(average => ({...average,
      points: average.values.map((value, index) => ({...points[index], value, y: value === null ? null : yOf(value)}))})),
      fixed: boundaries.filter(item => item.kind === 'fixed' && Number.isFinite(item.value)).map(item => ({...item, y: yOf(item.value)}))};
  }

  function element(tag, attributes = {}, text) {
    const node = document.createElementNS(svgNS, tag);
    for (const [name, value] of Object.entries(attributes)) node.setAttribute(name, String(value));
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function paths(svg, points, attributes) {
    let segment = [];
    function flush() {
      if (!segment.length) return;
      if (segment.length === 1) svg.append(element('circle', {...attributes, cx: segment[0].x, cy: segment[0].y, r: 2.5}));
      else svg.append(element('polyline', {...attributes, points: segment.map(point => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(' ')}));
      segment = [];
    }
    for (const point of points) {
      if (point.y === null || !Number.isFinite(point.y) || !Number.isFinite(point.x)) flush();
      else segment.push(point);
    }
    flush();
  }

  function storageKey(metric) {
    const context = window.RESEARCH_CONTEXT;
    if (!context?.tenant || !context?.user || !context?.workbench || !metric.series_id) return null;
    return 'tin.judgment.chart.v1:' + JSON.stringify([context.tenant, context.user, context.workbench, metric.series_id]);
  }

  function initialize(root) {
    if (root.dataset.chartReady) return;
    const source = root.querySelector('[data-chart-values]'), svg = root.querySelector('[data-metric-trend]');
    if (!source || !svg) return;
    let metric;
    try { metric = JSON.parse(source.textContent); } catch { return; }
    root.dataset.chartReady = 'true';
    const tooltip = root.querySelector('[data-chart-tooltip]');
    const select = root.querySelector('[data-chart-ma]'), custom = root.querySelector('[data-chart-ma-custom]');
    const semantic = root.querySelector('[data-chart-semantics]'), streak = root.querySelector('[data-chart-streak]');
    const status = root.querySelector('[data-chart-status]');
    let selectedWindow = 0, revision = 0, rendered, selectedIndex = null;
    const key = storageKey(metric);
    let preference = {};
    if (key) try { preference = JSON.parse(window.localStorage.getItem(key) || '{}') || {}; } catch { /* Presentation stays available without local storage. */ }
    if ((metric.boundaries || []).some(item => item.kind === 'sma')) select.options[0].textContent = '按规则';
    semantic.value = ['change', 'inventory'].includes(preference.semantics) ? preference.semantics : metric.trend_semantics === 'inventory' ? 'inventory' : 'change';
    function semantics() { if (streak) streak.textContent = streakText(metric.streak, semantic.value); }
    function savePreference() {
      if (key) try { window.localStorage.setItem(key, JSON.stringify({semantics: semantic.value})); } catch { /* Presentation preference is optional. */ }
      document.querySelectorAll('[data-judgment-chart]').forEach(other => {
        if (root.dataset.chartSeries && other !== root && other.dataset.chartSeries === root.dataset.chartSeries) {
          const control = other.querySelector('[data-chart-semantics]');
          const badge = other.querySelector('[data-chart-streak]');
          if (control) control.value = semantic.value;
          if (badge) badge.textContent = streakText(metric.streak, semantic.value);
        }
      });
    }
    semantic.addEventListener('change', () => { semantics(); savePreference(); });
    semantics();

    function message(text) { status.textContent = text; status.hidden = !text; }
    function render() {
      rendered = shape(metric.timeline || metric.points || [], metric.boundaries || [], selectedWindow);
      svg.replaceChildren(); svg.setAttribute('viewBox', '0 0 480 132');
      for (const y of [10, 61, 112]) svg.append(element('line', {x1: 12, x2: 468, y1: y, y2: y, class: 'judgment-chart-grid'}));
      for (const boundary of rendered.fixed) {
        const line = element('line', {x1: 12, x2: 468, y1: boundary.y, y2: boundary.y, class: 'judgment-chart-boundary', 'data-chart-boundary': '', 'data-boundary-value': boundary.value});
        line.append(element('title', {}, boundaryText(boundary, metric.unit || '')));
        svg.append(line);
        svg.append(element('text', {x: 465, y: Math.max(10, boundary.y - 4), 'text-anchor': 'end', class: 'judgment-chart-boundary-label'}, number(boundary.value)));
      }
      paths(svg, rendered.points, {class: 'judgment-chart-observation'});
      rendered.averages.forEach((average, index) => paths(svg, average.points, {'data-chart-average': average.window, style: `stroke:${colors[index % colors.length]}`, class: 'judgment-chart-average'}));
      const cross = element('g', {'data-chart-crosshair': '', visibility: 'hidden'});
      cross.append(element('line', {y1: 10, y2: 112, class: 'judgment-chart-cross-line'}));
      cross.append(element('circle', {r: 3.5, class: 'judgment-chart-cross-point'}));
      svg.append(cross);
      if (selectedWindow && !rendered.averages.find(item => item.window === selectedWindow)?.values.some(value => value !== null)) message(`MA${selectedWindow}：有效连续观测不足`);
      else message('');
    }
    function hide() {
      tooltip.hidden = true;
      svg.querySelector('[data-chart-crosshair]')?.setAttribute('visibility', 'hidden');
    }
    function show(index) {
      const point = rendered.points[index];
      if (!point || point.y === null || !Number.isFinite(point.y)) { hide(); return; }
      selectedIndex = index;
      const cross = svg.querySelector('[data-chart-crosshair]');
      cross.setAttribute('visibility', 'visible');
      const line = cross.querySelector('line'), dot = cross.querySelector('circle');
      line.setAttribute('x1', point.x); line.setAttribute('x2', point.x); dot.setAttribute('cx', point.x); dot.setAttribute('cy', point.y);
      tooltip.replaceChildren();
      const date = document.createElement('div'); date.textContent = point.date.replace('T', ' ').slice(0, 16); tooltip.append(date);
      const value = document.createElement('strong'); value.textContent = `${number(point.value)} ${metric.unit || ''}`; tooltip.append(value);
      const previous = rendered.points[index - 1];
      if (previous && Number.isFinite(previous.value)) {
        const change = document.createElement('span'); const difference = point.value - previous.value;
        if (Number.isFinite(difference)) { change.textContent = `${difference > 0 ? '+' : ''}${number(difference)} 较前次`; tooltip.append(change); }
      }
      for (const average of rendered.averages) {
        const value = average.values[index];
        if (value !== null) { const row = document.createElement('span'); row.textContent = `MA${average.window} ${number(value)}`; tooltip.append(row); }
      }
      for (const boundary of rendered.fixed) { const row = document.createElement('span'); row.textContent = boundaryText(boundary); tooltip.append(row); }
      tooltip.hidden = false;
      tooltip.style.left = '8px';
      const plotWidth = svg.parentElement.clientWidth;
      tooltip.style.left = `${Math.max(8, Math.min(plotWidth - tooltip.offsetWidth - 8, point.x / 480 * plotWidth))}px`;
    }
    svg.addEventListener('pointermove', event => {
      const bounds = svg.getBoundingClientRect(); const x = (event.clientX - bounds.left) / bounds.width * 480;
      let nearest = -1, distance = Infinity;
      rendered.points.forEach((point, index) => {
        if (point.y === null || !Number.isFinite(point.y)) return;
        const delta = Math.abs(point.x - x); if (delta < distance) { nearest = index; distance = delta; }
      });
      if (nearest >= 0) show(nearest);
    });
    svg.addEventListener('pointerleave', hide);
    svg.addEventListener('blur', hide);
    svg.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Escape'].includes(event.key)) return;
      event.preventDefault(); if (event.key === 'Escape') { hide(); return; }
      const valid = rendered.points.map((point, index) => point.y !== null && Number.isFinite(point.y) ? index : null).filter(index => index !== null);
      let index = valid.indexOf(selectedIndex);
      if (event.key === 'Home') index = 0;
      else if (event.key === 'End') index = valid.length - 1;
      else index = Math.max(0, Math.min(valid.length - 1, (index < 0 ? valid.length - 1 : index) + (event.key === 'ArrowLeft' ? -1 : 1)));
      if (valid.length) show(valid[index]);
    });
    async function chooseAverage() {
      const ownRevision = ++revision;
      custom.hidden = select.value !== 'custom';
      const period = Number(select.value === 'custom' ? custom.value : select.value);
      if (select.value === 'custom' && (!custom.value.trim() || period === 0) || period && (!Number.isInteger(period) || period < 2 || period > 120)) { selectedWindow = 0; render(); message('均线期数应为 2–120'); return; }
      selectedWindow = period;
      const need = period ? Math.min(121, period + 23) : 0;
      if ((need > (metric.timeline || metric.points || []).length || period && !metric.history_complete) && root.dataset.chartUrl) {
        message('加载中…');
        try {
          const url = new URL(root.dataset.chartUrl, location.href);
          for (const [name, value] of [['date', root.dataset.chartDate], ['judgment_id', root.dataset.chartJudgment], ['series_id', root.dataset.chartSeries], ['limit', need]]) url.searchParams.set(name, String(value));
          const cacheKey = url.href + ':' + JSON.stringify(window.RESEARCH_CONTEXT || {});
          if (!pending.has(cacheKey)) {
            if (pending.size >= 64) pending.delete(pending.keys().next().value);
            pending.set(cacheKey, (async () => {
            const response = await researchFetch(url.href); const result = await response.json();
            if (!response.ok || !result.metric || result.metric.series_id !== metric.series_id || !Array.isArray(result.metric.timeline) || result.metric.timeline.length > need) throw new Error('图表数据暂不可用');
            return result.metric;
          })().catch(error => { pending.delete(cacheKey); throw error; }));
          }
          const updated = await pending.get(cacheKey);
          if (ownRevision !== revision) return;
          metric = {...metric, ...updated};
          semantics();
        } catch { if (ownRevision === revision) { render(); message('图表数据暂不可用，请重试'); } return; }
      }
      if (ownRevision === revision) render();
    }
    select.addEventListener('change', chooseAverage); custom.addEventListener('change', chooseAverage);
    render();
  }

  const api = {movingAverage, shape, streakText, boundaryText};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (typeof document === 'undefined') return;
  function boot() { document.querySelectorAll('[data-judgment-chart]').forEach(initialize); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, {once: true});
  else boot();
})();
