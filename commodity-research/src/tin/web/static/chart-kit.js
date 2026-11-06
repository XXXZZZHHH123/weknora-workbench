/* chart.v1 / compute_cycles renderer. No interpolation and no external assets. */
(function (root) {
  'use strict';
  const DAY = 86400000, NS = 'http://www.w3.org/2000/svg';
  const finite = value => typeof value === 'number' && Number.isFinite(value);
  const dayNumber = value => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) ? Date.parse(value + 'T00:00:00Z') / DAY : NaN;
  function monthMidpoint(day, anchor) {
    const date = new Date(day * DAY), year = date.getUTCFullYear(), month = date.getUTCMonth();
    return (Date.UTC(year, month, 1) + Date.UTC(year, month + 1, 1)) / (2 * DAY) - anchor;
  }
  function monthlyBand(points, anchorDate) {
    const anchor = dayNumber(anchorDate), months = new Map();
    if (!finite(anchor)) return points || [];
    for (const point of points || []) {
      if (!finite(point.x)) continue;
      const x = monthMidpoint(anchor + point.x, anchor), previous = months.get(x);
      if (finite(x) && (!previous || point.n > previous.n)) months.set(x, {...point, x});
    }
    return [...months.values()].sort((a, b) => a.x - b.x);
  }
  function median(values) {
    const sorted = values.filter(finite).slice().sort((a, b) => a - b), n = sorted.length;
    return n ? (sorted[Math.floor((n - 1) / 2)] + sorted[Math.floor(n / 2)]) / 2 : null;
  }
  function medianInterval(points, cycle = false) {
    const xs = points.filter(p => finite(p.value)).map(p => cycle ? p.x : dayNumber(p.date)).filter(finite).sort((a, b) => a - b);
    return median(xs.slice(1).map((x, i) => x - xs[i]).filter(x => x > 0));
  }
  function shouldBreak(a, b, interval) {
    return finite(interval) && interval > 0 && b - a > 2.5 * interval;
  }
  function splitObservations(points, cycle = false, fallback = null) {
    const interval = medianInterval(points, cycle) || fallback, runs = [];
    let run = [], previous = null;
    for (const point of points) {
      const x = cycle ? point.x : dayNumber(point.date);
      if (!finite(x) || !finite(point.value)) {
        if (run.length) runs.push(run);
        run = []; previous = null; continue;
      }
      if (previous !== null && shouldBreak(previous, x, interval)) {
        if (run.length) runs.push(run);
        run = [];
      }
      run.push({...point, x}); previous = x;
    }
    if (run.length) runs.push(run);
    return runs;
  }
  function filterBand(points) {
    return (points || []).filter(p => finite(p.x) && finite(p.min) && finite(p.max) && finite(p.mean) && p.n >= 3 && p.min <= p.max);
  }
  function bandRuns(points, freqDays) {
    const valid = new Set(filterBand(points)), step = Math.max(1, Math.round(freqDays || 1)), runs = [];
    let run = [];
    for (const point of points || []) {
      if (!valid.has(point) || (run.length && point.x - run[run.length - 1].x > step * 1.5)) {
        if (run.length) runs.push(run); run = [];
      }
      if (valid.has(point)) run.push({...point, value: point.mean});
    }
    if (run.length) runs.push(run);
    return runs;
  }
  function nearestPoint(points, x, tolerance) {
    let best = null, distance = Infinity;
    for (const point of points || []) {
      if (!finite(point.x) || !finite(point.value)) continue;
      const delta = Math.abs(point.x - x);
      if (delta < distance || (delta === distance && point.x < best.x)) { best = point; distance = delta; }
    }
    return distance <= tolerance ? best : null;
  }
  function tooltipValues(segments, x, tolerance) {
    return segments.filter(part => (part.points || []).some(p => finite(p.x) && finite(p.value)))
      .map(part => ({label: part.label, current: !!part.current, point: nearestPoint(part.points, x, tolerance)}));
  }
  function cycleTicks(template, min, max, anchorDate, centerMonths = false) {
    if (!finite(min) || !finite(max)) return [];
    if (template.kind === 'event') {
      const weeks = Math.max(1, Math.ceil((max - min) / 7 / 16)), ticks = [];
      for (let x = Math.ceil(min / (7 * weeks)) * 7 * weeks; x <= max; x += 7 * weeks) {
        const w = x / 7; ticks.push({x, label: w === 0 ? '事件日' : (w < 0 ? '−' : '+') + Math.abs(w) + '周'});
      }
      return ticks;
    }
    if (template.kind === 'repeat') {
      const start = template.start || '01-01', anchor = finite(dayNumber(anchorDate)) ? dayNumber(anchorDate) : dayNumber('2000-' + start);
      const first = new Date(anchor * DAY), boundary = Date.UTC(first.getUTCFullYear(), first.getUTCMonth(), 1) / DAY - anchor;
      const ticks = [{x: centerMonths ? boundary : 0, label: (first.getUTCMonth() + 1) + '月'}];
      const months = {year: 12, half: 6, quarter: 3, month: 1}[template.period] || 12;
      for (let i = 1; i < months; i++) {
        const stamp = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + i, 1)), x = stamp.getTime() / DAY - anchor;
        if (x > max) break;
        ticks.push({x, label: (stamp.getUTCMonth() + 1) + '月'});
      }
      return ticks.map(t => centerMonths ? {...t, labelX: monthMidpoint(anchor + t.x, anchor)} : t)
        .filter(t => (t.x >= min && t.x <= max) || (centerMonths && t.labelX >= min && t.labelX <= max));
    }
    const step = Math.max(1, Math.ceil((max - min) / 8));
    const ticks = [];
    for (let x = Math.ceil(min / step) * step; x <= max; x += step) ticks.push({x, label: x + '天'});
    return ticks;
  }
  function dateTicks(min, max) {
    const span = max - min, step = Math.max(1, Math.ceil(span / 6)), ticks = [];
    for (let x = min; x <= max; x += step) ticks.push({x, label: new Date(Math.round(x) * DAY).toISOString().slice(0, 10)});
    return ticks;
  }
  function niceAxis(min, max, {thumbnail = false, fixedMin = false, fixedMax = false} = {}) {
    const limit = thumbnail ? 4 : 7, desired = thumbnail ? 3 : 5;
    function niceStep(value) {
      const power = 10 ** Math.floor(Math.log10(value)), fraction = value / power;
      return ([1, 2, 5, 10].find(n => n >= fraction - 1e-10) || 10) * power;
    }
    let step = niceStep((max - min) / desired), first, last;
    function bounds() {
      first = fixedMin ? Math.ceil(min / step - 1e-10) : Math.floor(min / step + 1e-10);
      last = fixedMax ? Math.floor(max / step + 1e-10) : Math.ceil(max / step - 1e-10);
    }
    bounds();
    while (last - first + 1 > limit) { step = niceStep(step * 1.1); bounds(); }
    // Automatic ranges can expand to keep the full-size chart at 5–7 ticks.
    // Explicit user bounds stay exact, even when fewer ticks fit inside them.
    if (!thumbnail && last - first + 1 < 5) {
      if (!fixedMax) last = first + 4;
      else if (!fixedMin) first = last - 4;
    }
    const precision = Math.min(100, Math.max(0, -Math.floor(Math.log10(step) + 1e-10)));
    const clean = value => Math.abs(value) < step * 1e-10 ? 0 : Number(value.toPrecision(15));
    const ticks = Array.from({length: Math.max(0, last - first + 1)}, (_, i) => {
      const value = clean((first + i) * step);
      return {value, label: value.toFixed(precision)};
    });
    return {min: fixedMin ? min : clean(first * step), max: fixedMax ? max : clean(last * step), step, ticks};
  }
  function format(value) {
    return finite(value) ? value.toLocaleString('zh-CN', {maximumFractionDigits: 3}) : '—';
  }
  function svgElement(name, attrs = {}, content) {
    const el = root.document.createElementNS(NS, name);
    Object.entries(attrs).forEach(([key, value]) => { if (value !== undefined && value !== null) el.setAttribute(key, String(value)); });
    if (content !== undefined) el.textContent = content;
    return el;
  }
  function render(container, payload, options = {}) {
    const document = container.ownerDocument, cycle = Array.isArray(payload.segments), definition = options.definition || {};
    const base = definition.curves?.[0] || {};
    const parts = cycle ? payload.segments.filter(part => (part.points || []).some(p => finite(p.x) && finite(p.value))) : (payload.curves || []);
    const current = cycle ? parts.find(p => p.current) : null;
    const previous = cycle ? parts.filter(p => !p.current && (!current || p.start <= current.start)).slice().sort((a, b) => b.start.localeCompare(a.start))[0] : null;
    const monthly = cycle && payload.template?.kind !== 'event' && payload.freq_days >= 28;
    const anchorDate = current?.start || parts[0]?.start || '2000-' + (payload.template?.start || '01-01');
    const band = monthly ? monthlyBand(payload.band, anchorDate) : payload.band;
    const series = parts.map((part, index) => ({...part, key: index, label: part.label || part.series_id || '未命名曲线',
      kind: cycle ? (base.kind || 'line') : part.kind || 'line', axis: cycle ? (base.axis || 'left') : part.axis || 'left',
      style: cycle ? (definition.time?.segment_styles || []).find(style => style.label === part.label) || {} : (part.style || {}),
      points: (part.points || []).map(p => {
        const x = cycle ? p.x : dayNumber(p.date);
        const centered = monthly && finite(x) ? monthMidpoint(dayNumber(p.date), dayNumber(part.start)) : NaN;
        return {...p, x: finite(centered) ? centered : x};
      })}));
    function appearance(s) {
      const isPrevious = cycle && parts[s.key] === previous;
      const palette = root.ChartPalette ? root.ChartPalette.color(s.key) : `var(--chart-series-${s.key + 1})`;
      const color = s.style.color || (cycle && s.current ? base.style?.color || 'var(--primary)' : palette);
      const width = s.style.width || (cycle ? s.current ? Math.max(3, base.style?.width || 3) : isPrevious ? 2 : 1.2 : 2);
      const dash = s.style.dash ?? (isPrevious ? 'dash' : null);
      return {color, width, dash: dash === 'dash' ? '7 4' : dash === 'dot' ? '2 4' : null,
        opacity: cycle && !s.current && !s.style.color ? (isPrevious ? .85 : .5) : 1};
    }
    const hidden = new Set(), wrapper = document.createElement('div'), tip = document.createElement('div');
    wrapper.className = 'chart-kit'; tip.className = 'chart-kit-tooltip'; tip.hidden = true;
    tip.setAttribute('role', 'status'); wrapper.append(tip); container.replaceChildren(wrapper);
    let svg, destroyed = false;
    function draw() {
      if (destroyed) return;
      tip.hidden = true;
      const width = options.width || 960;
      const legendRows = options.thumbnail ? 0 : Math.ceil(series.length / 4);
      const margin = {left: 72, right: 72, top: options.title ? 46 : 28, bottom: 54 + legendRows * 24};
      const height = Math.max(options.height || (options.thumbnail ? 210 : 440), margin.top + margin.bottom + (options.thumbnail ? 80 : 200));
      const right = width - margin.right, bottom = height - margin.bottom;
      const visible = series.filter(s => !hidden.has(s.key));
      const validBand = cycle ? filterBand(band) : [];
      const showBand = cycle && visible.length > 0;
      const allX = series.flatMap(s => s.points.filter(p => finite(p.x)).map(p => p.x));
      if (showBand) allX.push(...validBand.map(p => p.x));
      let minX = allX.length ? Math.min(...allX) : 0, maxX = allX.length ? Math.max(...allX) : 1;
      if (cycle) {
        const template = payload.template || {};
        if (template.kind === 'event') { minX = Math.min(minX, -template.before_days); maxX = Math.max(maxX, template.after_days); }
        else { minX = Math.min(0, minX); maxX = Math.max(maxX, ...parts.map(p => dayNumber(p.end) - dayNumber(p.start)).filter(finite)); }
      }
      if (maxX === minX) { minX -= .5; maxX += .5; }
      const xScale = x => margin.left + (x - minX) / (maxX - minX) * (right - margin.left);
      const axes = {};
      for (const axis of ['left', 'right']) {
        const members = visible.filter(s => s.axis === axis), values = members.flatMap(s => s.points.map(p => p.value)).filter(finite);
        if (showBand && (base.axis || 'left') === axis) values.push(...validBand.flatMap(p => [p.min, p.max]));
        if (members.some(s => s.kind === 'bar' || s.kind === 'area')) values.push(0);
        let min = values.length ? Math.min(...values) : 0, max = values.length ? Math.max(...values) : 1;
        const pad = max === min ? Math.max(Math.abs(max) * .05, 1) : (max - min) * .08;
        const fixed = (definition.axes || payload.axes || {})[axis] || {};
        min = finite(fixed.min) ? fixed.min : min - pad; max = finite(fixed.max) ? fixed.max : max + pad;
        if (max <= min) max = min + 1;
        const nice = niceAxis(min, max, {thumbnail: !!options.thumbnail, fixedMin: finite(fixed.min), fixedMax: finite(fixed.max)});
        axes[axis] = {...nice, members, scale: y => bottom - (y - nice.min) / (nice.max - nice.min) * (bottom - margin.top)};
      }
      const next = svgElement('svg', {viewBox: `0 0 ${width} ${height}`, width, height, role: 'img', 'aria-label': options.title || (cycle ? '周期对比图' : '走势图'), xmlns: NS});
      const add = (name, attrs, content, parent = next) => { const el = svgElement(name, attrs, content); parent.append(el); return el; };
      add('rect', {width, height, fill: 'var(--surface)'});
      if (options.title) add('text', {x: margin.left, y: 24, fill: 'var(--text)', 'font-size': 16, 'font-weight': 600}, options.title);
      const clipId = 'chart-kit-clip-' + (++render.sequence), defs = add('defs');
      const clip = add('clipPath', {id: clipId}, undefined, defs);
      add('rect', {x: margin.left, y: margin.top, width: right - margin.left, height: bottom - margin.top}, undefined, clip);
      for (const axis of ['left', 'right']) {
        const spec = axes[axis];
        if (!series.some(s => s.axis === axis)) continue;
        for (const tick of spec.ticks) {
          const y = spec.scale(tick.value);
          if (axis === 'left' || !series.some(s => s.axis === 'left')) add('line', {x1: margin.left, x2: right, y1: y, y2: y, stroke: 'var(--line)'});
          add('text', {x: axis === 'left' ? margin.left - 8 : right + 8, y: y + 4, fill: 'var(--text-muted)', 'font-size': 11, 'text-anchor': axis === 'left' ? 'end' : 'start', 'data-axis-tick': axis}, tick.label);
        }
        const unit = cycle && (payload.template || {}).transform === 'rebase' ? '起点=100' : cycle && (payload.template || {}).transform === 'change' ? '变化值' + (payload.unit ? `（${payload.unit}）` : '') : [...new Set(spec.members.map(s => s.unit || payload.unit).filter(Boolean))].join(' / ');
        if (unit) add('text', {x: axis === 'left' ? margin.left : right, y: margin.top - 10, fill: 'var(--text-muted)', 'font-size': 11, 'text-anchor': axis === 'left' ? 'start' : 'end'}, unit);
      }
      const ticks = cycle ? cycleTicks(payload.template || {}, minX, maxX, monthly ? anchorDate : current?.start, monthly) : dateTicks(minX, maxX);
      const every = options.thumbnail ? Math.max(1, Math.ceil(ticks.length / 4)) : 1;
      ticks.forEach((tick, index) => {
        if (index % every) return;
        const x = xScale(tick.x), labelX = tick.labelX ?? tick.x;
        if (tick.x >= minX && tick.x <= maxX) add('line', {x1: x, x2: x, y1: bottom, y2: bottom + 4, stroke: 'var(--line)'});
        if (labelX >= minX && labelX <= maxX) add('text', {x: xScale(labelX), y: bottom + 20, 'text-anchor': 'middle', fill: 'var(--text-muted)', 'font-size': options.thumbnail ? 13 : 11}, tick.label);
      });
      const plot = add('g', {'clip-path': `url(#${clipId})`});
      const path = (run, y) => run.map((p, i) => `${i ? 'L' : 'M'}${xScale(p.x).toFixed(2)},${y(p.value).toFixed(2)}`).join(' ');
      if (showBand) {
        const y = axes[base.axis || 'left'].scale;
        // Keep low-n holes as explicit breaks; a band may extend beyond the current segment.
        bandRuns(band, payload.freq_days).forEach(run => {
          const upper = run.map(p => ({...p, value: p.max})), lower = run.slice().reverse().map(p => ({...p, value: p.min}));
          add('path', {d: path(upper, y) + ' ' + path(lower, y).replace(/^M/, 'L') + ' Z', fill: 'var(--text-muted)', 'fill-opacity': .13, 'data-band': 'range'}, undefined, plot);
          add('path', {d: path(run, y), fill: 'none', stroke: 'var(--text-muted)', 'stroke-width': 1.5, 'stroke-dasharray': '6 4', 'data-band': 'mean'}, undefined, plot);
        });
      }
      const ordered = visible.slice().sort((a, b) => Number(!!a.current) - Number(!!b.current));
      for (const s of ordered) {
        const y = axes[s.axis].scale, {color, width: strokeWidth, dash, opacity} = appearance(s);
        const group = add('g', {'data-series': s.key, opacity}, undefined, plot);
        const runs = splitObservations(s.points, cycle, payload.freq_days);
        const interval = medianInterval(s.points, cycle) || 1;
        const barMembers = visible.filter(other => other.kind === 'bar' && other.axis === s.axis), barIndex = barMembers.indexOf(s);
        const barWidth = Math.min(32, Math.max(1, Math.abs(xScale(minX + interval) - xScale(minX)) * .75 / Math.max(1, barMembers.length)));
        for (const run of runs) {
          if (s.kind === 'bar') {
            run.forEach(p => add('rect', {x: xScale(p.x) + (barIndex - (barMembers.length - 1) / 2) * barWidth - barWidth / 2,
              y: Math.min(y(p.value), y(0)), width: barWidth, height: Math.max(.8, Math.abs(y(p.value) - y(0))), fill: color}, undefined, group));
          } else {
            if (s.kind === 'area') add('path', {d: path(run, y) + ` L${xScale(run[run.length - 1].x)},${y(0)} L${xScale(run[0].x)},${y(0)} Z`, fill: color, 'fill-opacity': .16}, undefined, group);
            add('path', {d: path(run, y), fill: 'none', stroke: color, 'stroke-width': strokeWidth, 'stroke-dasharray': dash, 'stroke-linejoin': 'round', 'data-curve-run': ''}, undefined, group);
            if (run.length === 1) add('circle', {cx: xScale(run[0].x), cy: y(run[0].value), r: 2.5, fill: color}, undefined, group);
          }
        }
      }
      if (!visible.some(s => s.points.some(p => finite(p.value)))) add('text', {x: width / 2, y: (margin.top + bottom) / 2, 'text-anchor': 'middle', fill: 'var(--text-muted)', 'font-size': 14}, hidden.size ? '点击图例显示曲线' : '暂无有效观测；缺失值不会补为零');
      if (!options.thumbnail) {
        series.forEach((s, index) => {
          const x = margin.left + (index % 4) * ((right - margin.left) / 4), y = bottom + 48 + Math.floor(index / 4) * 24;
          const legend = add('g', {role: 'button', tabindex: 0, 'aria-label': s.label, 'aria-pressed': String(!hidden.has(s.key)), 'data-legend': s.key, opacity: hidden.has(s.key) ? .4 : 1});
          add('rect', {x: x - 4, y: y - 16, width: (right - margin.left) / 4 - 8, height: 23, fill: 'var(--surface)'}, undefined, legend);
          const {color, width, dash, opacity} = appearance(s);
          add('line', {x1: x, x2: x + 18, y1: y - 4, y2: y - 4, stroke: color, 'stroke-width': width, 'stroke-dasharray': dash, opacity}, undefined, legend);
          const label = s.label + (s.current ? '（当前）' : ''), short = label.length > 20 ? label.slice(0, 19) + '…' : label;
          const text = add('text', {x: x + 24, y, fill: 'var(--text)', 'font-size': 12}, short, legend);
          add('title', {}, label, text);
          const toggle = () => { hidden.has(s.key) ? hidden.delete(s.key) : hidden.add(s.key); draw(); };
          legend.addEventListener('click', toggle);
          legend.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); } });
        });
        const crosshair = add('line', {y1: margin.top, y2: bottom, stroke: 'var(--text-muted)', 'stroke-dasharray': '3 3', visibility: 'hidden', 'pointer-events': 'none'});
        const overlay = add('rect', {x: margin.left, y: margin.top, width: right - margin.left, height: bottom - margin.top, fill: 'transparent', 'data-chart-hover': ''});
        overlay.addEventListener('pointermove', event => {
          const rect = next.getBoundingClientRect(), px = (event.clientX - rect.left) / rect.width * width;
          const target = minX + (px - margin.left) / (right - margin.left) * (maxX - minX);
          const hoverX = Math.round(target);
          crosshair.setAttribute('x1', xScale(hoverX)); crosshair.setAttribute('x2', xScale(hoverX)); crosshair.setAttribute('visibility', 'visible');
          tip.replaceChildren();
          const heading = document.createElement('strong'); heading.textContent = cycle ? `第 ${hoverX} 天` : new Date(hoverX * DAY).toISOString().slice(0, 10); tip.append(heading);
          const tolerance = cycle ? (payload.tolerance_days ?? Math.max(1, Math.floor((payload.freq_days || 1) / 2))) : null;
          for (const s of visible) {
            const point = nearestPoint(s.points, hoverX, tolerance ?? Math.max(1, Math.floor((medianInterval(s.points) || 1) / 2)));
            const row = document.createElement('div');
            const unit = cycle && payload.template?.transform === 'rebase' ? '（起点=100）' : (payload.unit || s.unit ? ' ' + (payload.unit || s.unit) : '');
            row.textContent = `${s.label}${s.current ? '（当前）' : ''}：${point ? format(point.value) + unit + ' · ' + point.date : '此位置缺失'}`;
            tip.append(row);
          }
          tip.hidden = false; tip.style.left = Math.min(Math.max(0, event.clientX - rect.left + 12), Math.max(0, rect.width - 280)) + 'px'; tip.style.top = Math.max(0, (event.clientY - rect.top) - 30) + 'px';
        });
        overlay.addEventListener('pointerleave', () => { tip.hidden = true; crosshair.setAttribute('visibility', 'hidden'); });
      }
      if (svg) svg.replaceWith(next); else wrapper.prepend(next);
      svg = next;
    }
    function toSVG() {
      const copy = svg.cloneNode(true), originals = [svg, ...svg.querySelectorAll('*')], clones = [copy, ...copy.querySelectorAll('*')];
      originals.forEach((el, i) => {
        const computed = root.getComputedStyle(el);
        for (const name of ['fill', 'stroke', 'color', 'font-family', 'font-size', 'font-weight']) clones[i].style.setProperty(name, computed.getPropertyValue(name));
        clones[i].removeAttribute('tabindex');
      });
      copy.setAttribute('xmlns', NS);
      return new root.XMLSerializer().serializeToString(copy);
    }
    async function toPNGBlob() {
      const blob = new Blob([toSVG()], {type: 'image/svg+xml;charset=utf-8'}), url = URL.createObjectURL(blob);
      try {
        const img = new root.Image();
        await new Promise((resolve, reject) => { img.onload = resolve; img.onerror = () => reject(new Error('图片转换失败，请下载 SVG')); img.src = url; });
        const canvas = document.createElement('canvas'); canvas.width = svg.viewBox.baseVal.width * 2; canvas.height = svg.viewBox.baseVal.height * 2;
        const ctx = canvas.getContext('2d'); if (!ctx) throw new Error('浏览器不支持图片转换，请下载 SVG');
        ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
        return await new Promise((resolve, reject) => canvas.toBlob(value => value ? resolve(value) : reject(new Error('图片转换失败')), 'image/png'));
      } finally { URL.revokeObjectURL(url); }
    }
    draw();
    const observer = typeof root.MutationObserver === 'function' ? new root.MutationObserver(draw) : null;
    observer?.observe(document.documentElement, {attributes: true, attributeFilter: ['class', 'style', 'theme-mode']});
    return {destroy() { destroyed = true; observer?.disconnect(); wrapper.remove(); }, toSVG, toPNGBlob};
  }
  render.sequence = 0;
  const api = {render, monthMidpoint, monthlyBand, medianInterval, shouldBreak, splitObservations, cycleTicks, dateTicks, niceAxis, filterBand, bandRuns, nearestPoint, tooltipValues};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.ChartKit = Object.freeze(api);
})(typeof window !== 'undefined' ? window : globalThis);
