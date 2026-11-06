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
      .map(part => ({label: part.label, current: !!part.current, point: nearestPoint(part.points, x, tolerance)}))
      .sort((a, b) => (b.point?.value ?? -Infinity) - (a.point?.value ?? -Infinity));
  }
  // Thresholds are explicit rule bindings, never labels or derived operands.
  // SMA values arrive from the server with their full, verified rule window.
  function ruleOverlays(series, values, transform = 'raw') {
    const result = [], fixed = new Map();
    for (const rule of Array.isArray(values) ? values : []) {
      if (!rule || typeof rule.series_id !== 'string' || !rule.series_id || !['fixed', 'sma', 'dynamic'].includes(rule.kind)) continue;
      for (const part of series) {
        if (part.derived || part.series_id !== rule.series_id) continue;
        const first = part.points.find(point => finite(point.value) && finite(point.raw));
        const convert = value => {
          if (!finite(value)) return null;
          const converted = transform === 'raw' ? value : !first ? null : transform === 'change'
            ? value - first.raw : first.raw > 0 ? value / first.raw * 100 : null;
          return finite(converted) ? converted : null;
        };
        if (rule.kind === 'fixed') {
          const value = convert(rule.value);
          if (value === null) continue;
          const key = JSON.stringify([rule.series_id, rule.rule_id, rule.name, rule.operator, part.axis, value]);
          if (fixed.has(key)) { fixed.get(key).keys.push(part.key); continue; }
          const overlay = {...rule, value, axis: part.axis, keys: [part.key], points: []};
          fixed.set(key, overlay); result.push(overlay);
        } else if (Array.isArray(rule.points)) {
          const dated = new Map(rule.points.filter(point => point && typeof point.date === 'string').map(point => [point.date, point.value]));
          const points = part.points.map(point => ({date: point.date, x: point.x, value: convert(dated.get(point.date))}));
          if (points.some(point => finite(point.value))) result.push({...rule, axis: part.axis, keys: [part.key], points});
        }
      }
    }
    return result;
  }
  function annualSegments(segments) {
    return !!segments?.length && segments.every(part => {
      const length = dayNumber(part.end) - dayNumber(part.start) + 1;
      return finite(length) && length >= 300 && length <= 400;
    });
  }
  function visibleCycleKeys(series, count) {
    const limit = Number.isInteger(count) && count >= 1 && count <= 40 ? count : 5;
    const current = series.find(part => part.current);
    const history = series.filter(part => part !== current && (!current || part.start <= current.start))
      .slice().sort((a, b) => b.start.localeCompare(a.start));
    return new Set((current ? [current, ...history] : history).slice(0, limit).map(part => part.key));
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
    if (template.kind === 'repeat' || (template.kind === 'segments' && annualSegments(template.segments))) {
      const start = template.start || '01-01';
      const origin = template.kind === 'segments' ? template.segments[0].start : anchorDate;
      const anchor = finite(dayNumber(origin)) ? dayNumber(origin) : dayNumber('2000-' + start);
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
    for (let x = Math.ceil(min / step) * step; x <= max; x += step) ticks.push({x, label: '第 ' + x + ' 天'});
    return ticks;
  }
  function dateTicks(min, max) {
    if (!finite(min) || !finite(max) || max < min) return [];
    const span = max - min, ticks = [], first = new Date(min * DAY);
    const year = first.getUTCFullYear(), month = first.getUTCMonth();
    let x, advance, calendar = false, years = false;
    if (span >= 730) {
      const step = Math.max(1, Math.ceil(span / 365.2425 / 6));
      x = Date.UTC(Math.floor(year / step) * step, 0, 1) / DAY;
      advance = value => Date.UTC(new Date(value * DAY).getUTCFullYear() + step, 0, 1) / DAY;
      calendar = true; years = true;
    } else if (span >= 60) {
      const step = span >= 180 ? 3 : 1;
      x = Date.UTC(year, Math.floor(month / step) * step, 1) / DAY;
      advance = value => { const d = new Date(value * DAY); return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + step, 1) / DAY; };
      calendar = true;
    } else if (span >= 14) {
      const step = 7 * Math.max(1, Math.ceil(span / 7 / 8));
      // 1970-01-05 was a Monday. Align every weekly tick to that weekday.
      x = Math.floor((min - 4) / step) * step + 4;
      advance = value => value + step;
    } else {
      const step = span > 7 ? 2 : 1;
      x = Math.floor(min / step) * step; advance = value => value + step;
    }
    for (; x <= max; x = advance(x)) {
      if (x < min) continue;
      const iso = new Date(x * DAY).toISOString();
      ticks.push({x, label: years ? iso.slice(0, 4) : calendar ? iso.slice(0, 7) : iso.slice(5, 10)});
    }
    return ticks;
  }
  function precisionForStep(step) {
    if (!finite(step) || step <= 0) return 3;
    let precision = Math.max(0, -Math.floor(Math.log10(step) + 1e-10));
    const limit = Math.min(12, precision + 3);
    while (precision < limit && Math.abs(step * 10 ** precision - Math.round(step * 10 ** precision)) > 1e-7) precision++;
    return Math.min(12, precision);
  }
  function formatNumber(value, step = null) {
    if (!finite(value)) return '—';
    const magnitude = Math.abs(value), divisor = magnitude >= 1e8 ? 1e8 : magnitude >= 1e4 ? 1e4 : 1;
    const precision = finite(step) && step > 0 ? precisionForStep(step / divisor) : Math.min(12, Math.max(3, magnitude && magnitude < .001 ? -Math.floor(Math.log10(magnitude)) + 1 : 0));
    const rounded = Number((value / divisor).toFixed(precision));
    const label = (rounded === 0 ? 0 : rounded).toLocaleString('zh-CN', {
      minimumFractionDigits: finite(step) && step > 0 ? precision : 0, maximumFractionDigits: precision
    });
    return label + (divisor === 1e8 ? '亿' : divisor === 1e4 ? '万' : '');
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
  function alignAxes(left, right) {
    if (!left.members.length || !right.members.length) return;
    const specs = [left, right], fixed = specs.find(spec => spec.fixedMin || spec.fixedMax);
    if (!fixed) {
      const intervals = Math.max(...specs.map(spec => spec.ticks.length - 1));
      for (const spec of specs) {
        spec.max = spec.min + spec.step * intervals;
        spec.ticks = Array.from({length: intervals + 1}, (_, index) => ({value: Number((spec.min + index * spec.step).toPrecision(15))}));
      }
    } else {
      const other = specs.find(spec => spec !== fixed);
      const fractions = fixed.ticks.map(tick => (tick.value - fixed.min) / (fixed.max - fixed.min));
      other.ticks = fractions.map(fraction => ({value: other.min + fraction * (other.max - other.min)}));
      if (other.ticks.length > 1) other.step = other.ticks[1].value - other.ticks[0].value;
    }
  }
  // These are 1x slot pixels; the 2x PNG rasterization must not halve the type.
  function exportStyle(width = 960) {
    if (!finite(width) || width <= 0) throw new Error('图位尺寸无效');
    const scale = Math.min(2, Math.max(1, width / 640));
    return {scale, axisFontSize: 11 * scale, legendFontSize: 11 * scale, titleFontSize: 13 * scale,
      tableFontSize: 12 * scale, currentLineWidth: 2.5 * scale, lineWidth: 1.5 * scale,
      text: '#30343b', background: '#ffffff', grid: '#e5e7eb', band: '#94a3b8',
      fontFamily: 'Arial, "Microsoft YaHei", sans-serif'};
  }
  function textMeasure(document, fontFamily) {
    const context = document.createElement('canvas').getContext?.('2d');
    return (value, size, bold = false) => {
      if (context?.measureText) {
        context.font = `${bold ? 700 : 400} ${size}px ${fontFamily}`;
        return context.measureText(String(value)).width;
      }
      return [...String(value)].reduce((sum, char) => sum + (char.charCodeAt(0) < 256 ? .62 : 1), 0) * size;
    };
  }
  function wrapText(value, maxWidth, measure) {
    const lines = []; let line = '';
    for (const char of String(value || '')) {
      if (char === '\n' || (line && measure(line + char) > maxWidth)) { lines.push(line); line = ''; }
      if (char !== '\n') line += char;
    }
    if (line) lines.push(line);
    return lines;
  }
  function fitText(value, maxWidth, measure) {
    let label = String(value);
    if (measure(label) <= maxWidth) return label;
    while (label && measure(label + '…') > maxWidth) label = label.slice(0, -1);
    return label + '…';
  }
  function svgElement(name, attrs = {}, content) {
    const el = root.document.createElementNS(NS, name);
    Object.entries(attrs).forEach(([key, value]) => { if (value !== undefined && value !== null) el.setAttribute(key, String(value)); });
    if (content !== undefined) el.textContent = content;
    return el;
  }
  // Both chart and table exports use their SVG viewBox at exactly 2x pixels.
  function imageMethods(getSVG, document) {
    function toSVG() {
      const svg = getSVG(), copy = svg.cloneNode(true), originals = [svg, ...svg.querySelectorAll('*')], clones = [copy, ...copy.querySelectorAll('*')];
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
        const svg = getSVG(), canvas = document.createElement('canvas');
        canvas.width = Math.round(svg.viewBox.baseVal.width * 2); canvas.height = Math.round(svg.viewBox.baseVal.height * 2);
        const ctx = canvas.getContext('2d'); if (!ctx) throw new Error('浏览器不支持图片转换，请下载 SVG');
        ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
        return await new Promise((resolve, reject) => canvas.toBlob(value => value ? resolve(value) : reject(new Error('图片转换失败')), 'image/png'));
      } finally { URL.revokeObjectURL(url); }
    }
    return {toSVG, toPNGBlob};
  }
  function renderTable(container, points, options = {}) {
    const width = options.width ?? 360, height = options.height ?? (points.length + 1) * 24;
    if (!finite(width) || !finite(height) || width <= 0 || height <= 0) throw new Error('表格尺寸无效');
    const rows = points.filter(point => finite(point.value) && typeof point.date === 'string');
    if (!rows.length) throw new Error('截止日前暂无有效观测');
    const wrapper = container.ownerDocument.createElement('div'); wrapper.className = 'chart-kit chart-kit-table';
    const svg = svgElement('svg', {viewBox: `0 0 ${width} ${height}`, width, height, role: 'img', 'aria-label': options.title || '日期与数值', xmlns: NS});
    const add = (name, attrs, text) => { const node = svgElement(name, attrs, text); svg.append(node); return node; };
    const rowHeight = height / (rows.length + 1), style = options.export ? exportStyle(width) : null;
    let split = width * .56;
    const dateLength = Math.max(4, ...rows.map(point => String(point.date).length)), valueLength = Math.max(4, ...rows.map(point => String(point.value).length));
    let fontSize = Math.min(16, rowHeight * .53, width / 15, split * .9 / (dateLength * .62), (width - split) * .9 / (valueLength * .62));
    if (style) {
      svg.style.fontFamily = style.fontFamily;
      const measure = textMeasure(container.ownerDocument, style.fontFamily), padding = 6 * style.scale;
      const dateWidth = Math.max(measure('日期', 1, true), ...rows.map(point => measure(point.date, 1)));
      const valueWidth = Math.max(measure('数值', 1, true), ...rows.map(point => measure(point.value, 1)));
      fontSize = Math.min(style.tableFontSize, rowHeight / 1.5, (width - 4 * padding) / (dateWidth + valueWidth));
      if (fontSize < 11) throw new Error('图位过小，无法以至少 11px 字号显示数据小表，请减少行数或增大图位');
      split = 2 * padding + (width - 4 * padding) * dateWidth / (dateWidth + valueWidth);
    }
    add('rect', {width, height, fill: style?.background || 'var(--surface)'});
    add('rect', {width, height: rowHeight, fill: style ? '#f3f4f6' : 'var(--surface-soft)'});
    const text = (x, y, value, header = false) => add('text', {x, y, fill: style?.text || 'var(--text)', 'font-size': fontSize,
      'font-weight': header ? 600 : 400, 'text-anchor': 'middle', 'dominant-baseline': 'central'}, value);
    text(split / 2, rowHeight / 2, '日期', true); text((split + width) / 2, rowHeight / 2, '数值', true);
    rows.forEach((point, index) => {
      const y = (index + 1.5) * rowHeight;
      text(split / 2, y, point.date); text((split + width) / 2, y, String(point.value));
    });
    for (let index = 1; index <= rows.length; index++) add('line', {x1: 0, x2: width, y1: index * rowHeight, y2: index * rowHeight, stroke: style?.grid || 'var(--line)', 'stroke-width': .6});
    add('line', {x1: split, x2: split, y1: 0, y2: height, stroke: style?.grid || 'var(--line)', 'stroke-width': .6});
    wrapper.append(svg); container.replaceChildren(wrapper);
    return {destroy() { wrapper.remove(); }, ...imageMethods(() => svg, container.ownerDocument)};
  }
  function render(container, payload, options = {}) {
    const document = container.ownerDocument, definition = options.definition || {};
    const multiCycle = payload.mode === 'cycle' && Array.isArray(payload.curves) && payload.curves.some(curve => curve.cycle);
    const cycle = multiCycle || Array.isArray(payload.segments);
    const transform = payload.transform || payload.template?.transform || definition.time?.transform || 'raw';
    const exporting = !!options.export, exportTheme = exporting ? exportStyle(options.width || 960) : null;
    const title = options.title || (exporting ? definition.title : '');
    const measure = exporting ? textMeasure(document, exportTheme.fontFamily) : null;
    const base = definition.curves?.[0] || {};
    const hasPoints = part => (part.points || []).some(p => finite(p.x) && finite(p.value));
    const parts = multiCycle ? payload.curves.flatMap((curve, curveIndex) => {
      const result = curve.cycle || {}, segments = result.segments || [], ownCurrent = segments.find(part => part.current);
      const currentStart = result.current_start || payload.common_current_start || ownCurrent?.start;
      const current = ownCurrent?.start === currentStart ? ownCurrent : null;
      const hasPreviousStart = Object.hasOwn(result, 'previous_start') || Object.hasOwn(payload, 'common_previous_start');
      const previousStart = result.previous_start ?? payload.common_previous_start;
      const previous = hasPreviousStart ? segments.find(part => part.start === previousStart) : currentStart
        ? segments.filter(part => part.start < currentStart).slice().sort((a, b) => b.start.localeCompare(a.start))[0] : null;
      const selection = curve.curve_cycles || definition.curves?.[curveIndex]?.curve_cycles || definition.time?.curve_cycles || 'current';
      return [current, ...(selection === 'current_and_previous' ? [previous] : [])].filter(part => part && hasPoints(part))
        .map(part => ({...part, current: part === current, label: `${curve.label} · ${part.label}`, kind: curve.kind || 'line', axis: curve.axis || 'left',
          series_id: curve.series_id, derived: curve.derived,
          unit: curve.unit, style: curve.style || {}, curveIndex, previous: part === previous,
          freq_days: result.freq_days, tolerance_days: result.tolerance_days}));
    }) : cycle ? payload.segments.filter(hasPoints) : (payload.curves || []);
    const current = cycle ? parts.find(p => p.current) : null;
    const previous = cycle ? parts.filter(p => !p.current && (!current || p.start <= current.start)).slice().sort((a, b) => b.start.localeCompare(a.start))[0] : null;
    const monthly = cycle && payload.template?.kind !== 'event' && (multiCycle
      ? payload.curves.length > 0 && payload.curves.every(curve => curve.cycle?.freq_days >= 28) : payload.freq_days >= 28);
    const anchorDate = (multiCycle ? payload.common_current_start : null) || current?.start || parts[0]?.start || '2000-' + (payload.template?.start || '01-01');
    const band = multiCycle ? [] : monthly ? monthlyBand(payload.band, anchorDate) : payload.band;
    const series = parts.map((part, index) => ({...part, key: index, label: part.label || part.series_id || '未命名曲线',
      series_id: cycle && !multiCycle ? base.series_id : part.series_id,
      derived: cycle && !multiCycle ? base.derived : part.derived,
      kind: cycle && !multiCycle ? (base.kind || 'line') : part.kind || 'line', axis: cycle && !multiCycle ? (base.axis || 'left') : part.axis || 'left',
      style: cycle && !multiCycle ? (definition.time?.segment_styles || []).find(style => style.label === part.label) || {} : (part.style || {}),
      points: (part.points || []).map(p => {
        const x = cycle ? p.x : dayNumber(p.date);
        const centered = monthly && finite(x) ? monthMidpoint(dayNumber(p.date), dayNumber(part.start)) : NaN;
        return {...p, x: finite(centered) ? centered : x};
      })}));
    const overlays = ruleOverlays(series, options.overlays || payload.overlays, transform);
    function appearance(s) {
      const isPrevious = multiCycle ? s.previous : cycle && parts[s.key] === previous;
      const colorIndex = multiCycle ? s.curveIndex : s.key;
      const palette = root.ChartPalette ? root.ChartPalette.color(colorIndex) : exporting ? ['#2563eb', '#d97706', '#059669', '#7c3aed'][colorIndex % 4] : `var(--chart-series-${colorIndex + 1})`;
      const color = s.style.color || (cycle && !multiCycle && s.current ? base.style?.color || (exporting ? '#2563eb' : 'var(--primary)') : palette);
      const width = s.style.width || (cycle ? s.current ? Math.max(3, multiCycle ? 3 : base.style?.width || 3) : isPrevious ? 2 : 1.2 : 2);
      const dash = multiCycle && isPrevious ? 'dash' : s.style.dash ?? (isPrevious ? 'dash' : null);
      return {color, width: exporting ? Math.max(width, s.current ? exportTheme.currentLineWidth : exportTheme.lineWidth) : width,
        dash: dash === 'dash' ? '7 4' : dash === 'dot' ? '2 4' : null,
        opacity: !exporting && cycle && !s.current && !s.style.color ? (isPrevious ? .85 : .5) : 1};
    }
    const defaultKeys = cycle && !multiCycle ? visibleCycleKeys(series, definition.time?.visible_cycles) : new Set(series.map(s => s.key));
    const hidden = new Set(series.filter(s => !defaultKeys.has(s.key)).map(s => s.key));
    const wrapper = document.createElement('div'), tip = document.createElement('div');
    wrapper.className = 'chart-kit'; tip.className = 'chart-kit-tooltip'; tip.hidden = true;
    tip.setAttribute('role', 'status'); wrapper.append(tip); container.replaceChildren(wrapper);
    let allYears;
    if (cycle && !multiCycle && !options.thumbnail && !exporting) {
      allYears = document.createElement('button'); allYears.type = 'button';
      allYears.textContent = '显示全部年份'; allYears.setAttribute('data-show-all-cycles', '');
      allYears.addEventListener('click', () => {
        if (hidden.size) hidden.clear();
        else series.forEach(s => { if (!defaultKeys.has(s.key)) hidden.add(s.key); });
        draw();
      });
      wrapper.append(allYears);
    }
    let svg, destroyed = false;
    function draw() {
      if (destroyed) return;
      tip.hidden = true;
      allYears?.setAttribute('aria-pressed', String(hidden.size === 0));
      const targetWidth = options.width || 960, targetHeight = options.height || 440;
      // Interactive exactSize callers retain their old design scale. PPT exports
      // lay out directly in slot pixels so small slots never shrink their text.
      const designScale = options.exactSize && !exporting ? Math.min(1, targetWidth / 960, targetHeight / (160 + Math.ceil(series.length / 4) * 24)) : 1;
      const width = targetWidth / designScale;
      const scale = exportTheme?.scale || 1, axisFont = exportTheme?.axisFontSize || 11;
      const legendFont = exportTheme?.legendFontSize || 12, legendRowHeight = exporting ? legendFont * 1.7 : 24;
      const titleLines = exporting ? wrapText(title, width - 16 * scale, value => measure(value, exportTheme.titleFontSize, true)) : [];
      const legendSeries = exporting ? series.filter(s => !hidden.has(s.key)) : series;
      const legendLayout = [];
      if (exporting && !options.thumbnail) {
        let x = 8 * scale, row = 0;
        for (const s of legendSeries) {
          const label = fitText(s.label + (s.current ? '（当前）' : ''), width - 40 * scale, value => measure(value, legendFont));
          const itemWidth = measure(label, legendFont) + 24 * scale;
          if (x > 8 * scale && x + itemWidth > width - 8 * scale) { row++; x = 8 * scale; }
          legendLayout.push({x, row, label, width: itemWidth}); x += itemWidth + 14 * scale;
        }
      }
      const legendRows = options.thumbnail ? 0 : exporting ? (legendLayout.at(-1)?.row ?? -1) + 1 : Math.ceil(series.length / 4);
      const margin = exporting ? {left: 72 * scale, right: 72 * scale,
        top: 8 * scale + titleLines.length * exportTheme.titleFontSize * 1.3 + axisFont * 1.7,
        bottom: axisFont * 2.6 + legendRows * legendRowHeight + 8 * scale}
        : {left: 72, right: 72, top: options.title ? 46 : 28, bottom: 54 + legendRows * 24};
      const height = exporting ? targetHeight : options.exactSize ? targetHeight / designScale : Math.max(options.height || (options.thumbnail ? 210 : 440), margin.top + margin.bottom + (options.thumbnail ? 80 : 200));
      if (exporting && height - margin.top - margin.bottom < axisFont * 3) throw new Error('图位过小，无法容纳清晰的标题、坐标轴与图例，请增大图位');
      let right = width - margin.right;
      const bottom = height - margin.bottom;
      const visible = series.filter(s => !hidden.has(s.key));
      const activeOverlays = overlays.filter(overlay => overlay.keys.some(key => !hidden.has(key)));
      const validBand = cycle ? filterBand(band) : [];
      const showBand = cycle && !multiCycle && validBand.length > 0;
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
        for (const overlay of activeOverlays.filter(item => item.axis === axis)) {
          values.push(...(overlay.kind === 'fixed' ? [overlay.value] : overlay.points.map(point => point.value).filter(finite)));
        }
        if (showBand && (base.axis || 'left') === axis) values.push(...validBand.flatMap(p => [p.min, p.max]));
        if (members.some(s => s.kind === 'bar' || s.kind === 'area')) values.push(0);
        let min = values.length ? Math.min(...values) : 0, max = values.length ? Math.max(...values) : 1;
        const pad = max === min ? Math.max(Math.abs(max) * .05, 1) : (max - min) * .08;
        const fixed = (definition.axes || payload.axes || {})[axis] || {};
        min = finite(fixed.min) ? fixed.min : min - pad; max = finite(fixed.max) ? fixed.max : max + pad;
        if (max <= min) max = min + 1;
        const nice = niceAxis(min, max, {thumbnail: !!options.thumbnail || (exporting && bottom - margin.top < axisFont * 8), fixedMin: finite(fixed.min), fixedMax: finite(fixed.max)});
        axes[axis] = {...nice, members, fixedMin: finite(fixed.min), fixedMax: finite(fixed.max)};
      }
      alignAxes(axes.left, axes.right);
      for (const spec of Object.values(axes)) {
        spec.scale = y => bottom - (y - spec.min) / (spec.max - spec.min) * (bottom - margin.top);
        spec.ticks = spec.ticks.map(tick => ({...tick, label: formatNumber(tick.value, spec.step)}));
      }
      const tickTemplate = payload.template?.kind === 'segments' ? {...payload.template, segments: payload.template.segments || parts} : payload.template || {};
      const ticks = cycle ? cycleTicks(tickTemplate, minX, maxX, monthly ? anchorDate : current?.start, monthly) : dateTicks(minX, maxX);
      if (exporting) {
        const halfLabel = Math.max(0, ...ticks.map(tick => measure(tick.label, axisFont))) / 2;
        for (const axis of ['left', 'right']) {
          const labelWidth = series.some(s => s.axis === axis) ? Math.max(0, ...axes[axis].ticks.map(tick => measure(tick.label, axisFont))) : 0;
          margin[axis] = Math.max(labelWidth, halfLabel) + 8 * scale;
        }
        right = width - margin.right;
        if (right - margin.left < axisFont * 4) throw new Error('图位过窄，无法容纳清晰的坐标轴，请增大图位');
      }
      const next = svgElement('svg', {viewBox: `0 0 ${width * designScale} ${height * designScale}`, width: width * designScale, height: height * designScale, role: 'img', 'aria-label': title || (cycle ? '周期对比图' : '走势图'), xmlns: NS});
      if (exporting) next.style.fontFamily = exportTheme.fontFamily;
      const scene = options.exactSize && !exporting ? svgElement('g', {transform: `scale(${designScale})`}) : next;
      if (scene !== next) next.append(scene);
      const add = (name, attrs, content, parent = scene) => { const el = svgElement(name, attrs, content); parent.append(el); return el; };
      add('rect', {width, height, fill: exportTheme?.background || 'var(--surface)'});
      if (exporting) titleLines.forEach((line, index) => add('text', {x: width / 2, y: 8 * scale + exportTheme.titleFontSize * (1 + index * 1.3),
        fill: exportTheme.text, 'font-size': exportTheme.titleFontSize, 'font-weight': 700, 'text-anchor': 'middle', 'data-chart-title': ''}, line));
      else if (options.title) add('text', {x: margin.left, y: 24, fill: 'var(--text)', 'font-size': 16, 'font-weight': 600}, options.title);
      const clipId = 'chart-kit-clip-' + (++render.sequence), defs = add('defs');
      const clip = add('clipPath', {id: clipId}, undefined, defs);
      add('rect', {x: margin.left, y: margin.top, width: right - margin.left, height: bottom - margin.top}, undefined, clip);
      for (const axis of ['left', 'right']) {
        const spec = axes[axis];
        if (!series.some(s => s.axis === axis)) continue;
        const labelColor = axis === 'right' && spec.members.length ? appearance(spec.members[0]).color : exportTheme?.text || 'var(--text-muted)';
        for (const tick of spec.ticks) {
          const y = spec.scale(tick.value);
          if (axis === 'left' || !series.some(s => s.axis === 'left')) add('line', {x1: margin.left, x2: right, y1: y, y2: y, stroke: exportTheme?.grid || 'var(--line)'});
          add('text', {x: axis === 'left' ? margin.left - 8 * scale : right + 8 * scale, y: y + 4 * scale, fill: labelColor, 'font-size': axisFont, 'text-anchor': axis === 'left' ? 'end' : 'start', 'data-axis-tick': axis}, tick.label);
        }
        const sourceUnit = [...new Set(spec.members.map(s => s.unit || payload.unit).filter(Boolean))].join(' / ');
        const unit = transform === 'rebase' ? '起点=100' : transform === 'change' ? '变化值' + (sourceUnit ? `（${sourceUnit}）` : '') : sourceUnit;
        if (unit) add('text', {x: axis === 'left' ? margin.left : right, y: margin.top - (exporting ? axisFont * .6 : 10), fill: labelColor, 'font-size': axisFont, 'text-anchor': axis === 'left' ? 'start' : 'end', 'data-axis-unit': axis}, unit);
      }
      const every = options.thumbnail ? Math.max(1, Math.ceil(ticks.length / 4)) : 1;
      let lastLabelRight = -Infinity;
      ticks.forEach((tick, index) => {
        if (index % every) return;
        const x = xScale(tick.x), labelX = tick.labelX ?? tick.x;
        if (tick.x >= minX && tick.x <= maxX) add('line', {x1: x, x2: x, y1: bottom, y2: bottom + 4 * scale, stroke: exportTheme?.grid || 'var(--line)'});
        if (labelX >= minX && labelX <= maxX) {
          if (exporting) {
            const half = measure(tick.label, axisFont) / 2;
            if (xScale(labelX) - half < lastLabelRight + 6 * scale) return;
            lastLabelRight = xScale(labelX) + half;
          }
          add('text', {x: xScale(labelX), y: bottom + (exporting ? axisFont * 1.7 : 20), 'text-anchor': 'middle', fill: exportTheme?.text || 'var(--text-muted)', 'font-size': exporting ? axisFont : options.thumbnail ? 13 : 11, 'data-date-tick': ''}, tick.label);
        }
      });
      const plot = add('g', {'clip-path': `url(#${clipId})`});
      const path = (run, y) => run.map((p, i) => `${i ? 'L' : 'M'}${xScale(p.x).toFixed(2)},${y(p.value).toFixed(2)}`).join(' ');
      if (showBand) {
        const y = axes[base.axis || 'left'].scale;
        // Keep low-n holes as explicit breaks; a band may extend beyond the current segment.
        bandRuns(band, payload.freq_days).forEach(run => {
          const upper = run.map(p => ({...p, value: p.max})), lower = run.slice().reverse().map(p => ({...p, value: p.min}));
          add('path', {d: path(upper, y) + ' ' + path(lower, y).replace(/^M/, 'L') + ' Z', fill: exportTheme?.band || 'var(--text-muted)', 'fill-opacity': exporting ? .12 : .13, 'data-band': 'range'}, undefined, plot);
          add('path', {d: path(run, y), fill: 'none', stroke: exportTheme?.band || 'var(--text-muted)', 'stroke-width': 1.5 * scale, 'stroke-dasharray': '6 4', 'data-band': 'mean'}, undefined, plot);
        });
      }
      const ordered = visible.slice().sort((a, b) => Number(!!a.current) - Number(!!b.current));
      for (const s of ordered) {
        const y = axes[s.axis].scale, {color, width: strokeWidth, dash, opacity} = appearance(s);
        const group = add('g', {'data-series': s.key, opacity}, undefined, plot);
        const runs = splitObservations(s.points, cycle, s.freq_days ?? payload.freq_days);
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
      for (const overlay of activeOverlays) {
        const y = axes[overlay.axis].scale, source = series.find(part => overlay.keys.includes(part.key));
        const color = overlay.color || appearance(source).color;
        const group = add('g', {'data-rule-overlay': overlay.kind, 'data-rule-id': overlay.rule_id,
          'data-rule-series': overlay.series_id}, undefined, plot);
        const label = `${overlay.name || '规则阈值'}${overlay.kind === 'sma' ? ` · MA${overlay.window}` : overlay.kind === 'fixed' ? ` · ${formatNumber(overlay.value)}` : ''}`;
        add('title', {}, label, group);
        const attributes = {fill: 'none', stroke: color, 'stroke-width': 1.5 * scale, 'stroke-dasharray': '5 4', 'data-rule-boundary': ''};
        if (overlay.kind === 'fixed') {
          add('line', {...attributes, x1: margin.left, x2: right, y1: y(overlay.value), y2: y(overlay.value)}, undefined, group);
          if (!options.thumbnail) add('text', {x: margin.left + 5 * scale, y: Math.max(margin.top + axisFont, y(overlay.value) - 4 * scale),
            fill: exportTheme?.text || color, 'font-size': axisFont, 'data-rule-label': ''}, label, group);
        } else {
          for (const run of splitObservations(overlay.points, cycle, source.freq_days ?? payload.freq_days)) {
            add('path', {...attributes, d: path(run, y)}, undefined, group);
            if (run.length === 1) add('circle', {cx: xScale(run[0].x), cy: y(run[0].value), r: 2 * scale, fill: color}, undefined, group);
          }
        }
      }
      // Values stay in source units; only their text uses 万/亿. Keep annotations
      // inside the plot so both the page and exported SVG retain the full label.
      const latest = (cycle ? visible.filter(s => s.current) : visible).map(s => {
        const point = s.points.filter(p => finite(p.x) && finite(p.value)).reduce((last, p) => {
          if (!last) return p;
          const date = dayNumber(p.date), previousDate = dayNumber(last.date);
          if (finite(date) && finite(previousDate) && date !== previousDate) return date > previousDate ? p : last;
          return p.x > last.x ? p : last;
        }, null);
        if (!point) return null;
        const y = axes[s.axis].scale(point.value);
        return y >= margin.top && y <= bottom ? {s, point, y, label: formatNumber(point.value, axes[s.axis].step)} : null;
      }).filter(Boolean);
      const annotationSize = axisFont, gap = annotationSize * 1.5;
      let labels = latest.slice().sort((a, b) => a.y - b.y), cursor = margin.top + annotationSize;
      for (const item of labels) { item.labelY = Math.max(item.y + annotationSize / 3, cursor); cursor = item.labelY + gap; }
      if (labels.length && labels.at(-1).labelY > bottom) {
        const shift = labels.at(-1).labelY - bottom;
        labels.forEach(item => { item.labelY -= shift; });
      }
      if (labels.some(item => item.labelY < margin.top + annotationSize || Math.abs(item.labelY - item.y) > gap * 2)) {
        const primary = latest.find(item => item.s.axis === 'left') || latest[0];
        labels = primary ? [{...primary, labelY: Math.min(bottom, Math.max(margin.top + annotationSize, primary.y + annotationSize / 3))}] : [];
      }
      for (const item of labels) {
        const {s, point, label, labelY} = item, color = appearance(s).color;
        const labelWidth = measure ? measure(label, annotationSize, !!s.current) : [...label].reduce((sum, char) => sum + (char.charCodeAt(0) < 256 ? .62 : 1), 0) * annotationSize;
        const pointX = xScale(point.x), after = pointX + labelWidth + 8 * scale < right;
        const labelX = after ? pointX + 6 * scale : Math.max(margin.left + labelWidth, pointX - 6 * scale);
        add('circle', {cx: pointX, cy: item.y, r: 2.5 * scale, fill: color, 'data-latest-point': s.key});
        if (Math.abs(labelY - item.y) > gap / 2) add('line', {x1: pointX, y1: item.y, x2: labelX, y2: labelY - annotationSize / 3, stroke: color, 'stroke-width': .7 * scale});
        add('text', {x: labelX, y: labelY, fill: exporting ? exportTheme.text : color, 'font-size': annotationSize,
          'font-weight': s.current ? 700 : 500, 'text-anchor': after ? 'start' : 'end',
          'paint-order': 'stroke', stroke: exportTheme?.background || 'var(--surface)', 'stroke-width': 3 * scale,
          'data-latest-value': s.key}, label);
      }
      if (!visible.some(s => s.points.some(p => finite(p.value)))) add('text', {x: width / 2, y: (margin.top + bottom) / 2, 'text-anchor': 'middle', fill: exportTheme?.text || 'var(--text-muted)', 'font-size': exporting ? axisFont : 14}, hidden.size ? '点击图例显示曲线' : '暂无有效观测；缺失值不会补为零');
      if (!options.thumbnail) {
        legendSeries.forEach((s, index) => {
          const layout = legendLayout[index];
          const x = exporting ? layout.x : margin.left + (index % 4) * ((right - margin.left) / 4);
          const y = exporting ? height - 8 * scale - (legendRows - 1 - layout.row) * legendRowHeight : bottom + 48 + Math.floor(index / 4) * 24;
          const legend = add('g', {role: 'button', tabindex: 0, 'aria-label': s.label, 'aria-pressed': String(!hidden.has(s.key)), 'data-legend': s.key, opacity: hidden.has(s.key) ? .4 : 1});
          add('rect', {x: x - 4 * scale, y: y - 16 * scale, width: exporting ? layout.width + 4 * scale : (right - margin.left) / 4 - 8, height: exporting ? legendRowHeight : 23, fill: exportTheme?.background || 'var(--surface)'}, undefined, legend);
          const {color, width, dash, opacity} = appearance(s);
          add('line', {x1: x, x2: x + 18 * scale, y1: y - 4 * scale, y2: y - 4 * scale, stroke: color, 'stroke-width': width, 'stroke-dasharray': dash, opacity}, undefined, legend);
          const label = s.label + (s.current ? '（当前）' : ''), short = label.length > 20 ? label.slice(0, 19) + '…' : label;
          const text = add('text', {x: x + 24 * scale, y, fill: exportTheme?.text || 'var(--text)', 'font-size': legendFont}, exporting ? layout.label : short, legend);
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
          const tolerance = s => cycle ? (s.tolerance_days ?? payload.tolerance_days ?? Math.max(1, Math.floor((s.freq_days || payload.freq_days || 1) / 2))) : Math.max(1, Math.floor((medianInterval(s.points) || 1) / 2));
          const rows = (cycle ? series : visible).map(s => ({s,
            point: nearestPoint(s.points, hoverX, tolerance(s))}))
            .sort((a, b) => (b.point?.value ?? -Infinity) - (a.point?.value ?? -Infinity));
          for (const {s, point} of rows) {
            const row = document.createElement('div');
            if (s.current) { row.style.fontWeight = '700'; row.setAttribute('data-tooltip-current', ''); }
            const unit = transform === 'rebase' ? '（起点=100）' : (s.unit || payload.unit ? ' ' + (s.unit || payload.unit) : '');
            row.textContent = `${s.label}${s.current ? '（当前）' : ''}：${point ? formatNumber(point.value, axes[s.axis].step) + unit + ' · ' + point.date : '此位置缺失'}`;
            tip.append(row);
          }
          for (const boundary of activeOverlays) {
            const point = boundary.kind === 'fixed' ? {value: boundary.value} : nearestPoint(boundary.points, hoverX, tolerance(series.find(s => boundary.keys.includes(s.key))));
            if (!point) continue;
            const row = document.createElement('div');
            row.textContent = `${boundary.name || '规则阈值'}${boundary.kind === 'sma' ? ` MA${boundary.window}` : ''}：${formatNumber(point.value, axes[boundary.axis].step)}`;
            row.setAttribute('data-rule-tooltip', boundary.rule_id || ''); tip.append(row);
          }
          tip.hidden = false; tip.style.left = Math.min(Math.max(0, event.clientX - rect.left + 12), Math.max(0, rect.width - 280)) + 'px'; tip.style.top = Math.max(0, (event.clientY - rect.top) - 30) + 'px';
        });
        overlay.addEventListener('pointerleave', () => { tip.hidden = true; crosshair.setAttribute('visibility', 'hidden'); });
      }
      if (svg) svg.replaceWith(next); else wrapper.prepend(next);
      svg = next;
    }
    draw();
    const observer = !exporting && typeof root.MutationObserver === 'function' ? new root.MutationObserver(draw) : null;
    observer?.observe(document.documentElement, {attributes: true, attributeFilter: ['class', 'style', 'theme-mode']});
    return {destroy() { destroyed = true; observer?.disconnect(); wrapper.remove(); }, ...imageMethods(() => svg, document)};
  }
  render.sequence = 0;
  const api = {render, renderTable, exportStyle, monthMidpoint, monthlyBand, medianInterval, shouldBreak, splitObservations, cycleTicks, dateTicks, niceAxis, formatNumber, annualSegments, visibleCycleKeys, alignAxes, filterBand, bandRuns, nearestPoint, tooltipValues, ruleOverlays};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.ChartKit = Object.freeze(api);
})(typeof window !== 'undefined' ? window : globalThis);
