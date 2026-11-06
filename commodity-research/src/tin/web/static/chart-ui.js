/* Shared presentation for saved charts and the indicator cycle panel. */
(function (root) {
  'use strict';
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
  const fmt = value => typeof value === 'number' && Number.isFinite(value) ? value.toLocaleString('zh-CN', {maximumFractionDigits: 3}) : '—';
  const pct = value => typeof value === 'number' && Number.isFinite(value) ? (value * 100).toFixed(2) + '%' : '—';
  const messages = {
    insufficient_cycles: '有效往期不足 3 期，暂不计算同期统计', unusable_current: '当前段覆盖不足、缺少起点或基准值不可用，暂不计算同期统计',
    no_observations: '暂无有效观测', frequency_unavailable: '观测不足，无法确定频率', segments_overlap: '手工分段有重叠，请注意样本可能重复',
    empty_segments_omitted: '已省略没有观测的周期',
    observation_window_limited: '观测读取已达上限，当前图表和统计仅覆盖已读取的窗口', sparse: '观测覆盖不足', start_missing: '缺少周期起点观测',
    invalid_base: '起点值不大于零，不能换算为起点=100', incomplete: '当前周期尚未结束', missing: '指标已不在当前来源目录',
    unit_missing: '曲线缺少单位，请核对口径', axis_unit_mismatch: '同一纵轴的单位不一致，请修改图表',
    already_imported: '已导入，跳过重复图表', pending_source: '曲线来源尚未接入', unsupported: '暂不支持该图型', no_data: '曲线没有可用数据', unconfirmed_sheet: '工作表尚未确认'
  };
  function message(value) {
    if (typeof value === 'string') return messages[value] || value;
    if (!value) return '';
    return value.message || [value.label || value.axis, messages[value.code] || value.code, value.units?.join(' / ')].filter(Boolean).join('：');
  }
  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    const fetcher = typeof root.researchFetch === 'function' ? root.researchFetch : root.fetch.bind(root);
    const response = await fetcher((root.RESEARCH_PREFIX || '') + '/api/sn' + path, {...options, headers});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      const detail = body.detail;
      const error = new Error(typeof detail === 'string' ? detail : Array.isArray(detail) ? detail.map(x => x.msg || '输入无效').join('；') : '请求失败，请稍后重试');
      error.status = response.status; throw error;
    }
    return body;
  }
  const pageURL = path => (root.RESEARCH_PREFIX || '') + path;
  function notice(element, text, kind = '') {
    element.textContent = text || ''; element.hidden = !text; element.className = 'charts-notice' + (kind ? ' ' + kind : '');
  }
  function noticeText(values) {
    return [...new Set((values || []).map(message).filter(Boolean))].join('；');
  }
  function notices(element, values, kind = 'warning') {
    const text = noticeText(values);
    notice(element, text, text ? kind : '');
    if (kind === 'subtle') element.className = 'charts-footnote';
  }
  function noticeMarker(element, values) {
    const text = noticeText(values);
    element.textContent = text ? '!' : ''; element.hidden = !text;
    element.title = text; element.className = 'charts-warning-marker';
    element.setAttribute('role', 'img'); element.setAttribute('aria-label', text);
  }
  function importSummary(charts) {
    let cycles = 0, trends = 0, unconvertible = 0; const periods = new Map();
    for (const chart of charts) {
      if (!chart.convertible) unconvertible++;
      else if (chart.mode === 'cycle') {
        cycles++; const start = chart.definition.time.template.start;
        const label = start ? '起点 ' + start : '原表分段';
        periods.set(label, (periods.get(label) || 0) + 1);
      } else trends++;
    }
    const details = [...periods].map(([label, count]) => `${label} ${count} 张`).join('、');
    return `本表 ${charts.length} 张：${cycles} 张周期图${details ? '（' + details + '）' : ''}，${trends} 张走势图，${unconvertible} 张无法转换`;
  }
  function importMarkup(charts) {
    function item(chart) {
      const type = !chart.convertible ? '无法转换' : chart.mode === 'cycle' ? '周期对比' : '走势';
      let html = `<article class="charts-import-item" data-import-chart="${esc(chart.chart_key)}"><h3>${esc(chart.title || '未命名图表')} · ${type}${chart.already_imported ? ' · 已导入（将跳过）' : ''}</h3>`;
      if (chart.reason) html += `<p class="charts-notice warning">${esc(message(chart.reason))}</p>`;
      if (chart.definition?.time.mode === 'cycle') {
        const time = chart.definition.time, template = time.template;
        html += `<p class="charts-muted">${esc(template.name)} · ${esc(template.start || (template.kind === 'segments' ? '按原始日期分段' : '事件窗口'))} · 近 ${esc(time.cycles)} 期</p>`;
      }
      if (chart.warnings?.length) html += '<ul>' + chart.warnings.map(w => `<li class="charts-muted">${esc(message(w))}</li>`).join('') + '</ul>';
      if (chart.missing_curves?.length) html += '<p>缺失曲线：</p><ul>' + chart.missing_curves.map(c => `<li>${esc(c.label)}：${esc(message(c.reason))}</li>`).join('') + '</ul>';
      return html + '</article>';
    }
    const unavailable = charts.filter(chart => !chart.convertible);
    return charts.filter(chart => chart.convertible).map(item).join('') + (unavailable.length
      ? `<details class="charts-import-unavailable"><summary>${unavailable.length} 张无法转换，展开查看原因</summary>${unavailable.map(item).join('')}</details>` : '');
  }
  function stats(element, payload, {showWarnings = true} = {}) {
    if (!Array.isArray(payload.segments)) { element.replaceChildren(); element.hidden = true; return; }
    element.hidden = false;
    const p = payload.position, n = p?.n || 0, status = p?.status || payload.status || (p?.rank != null ? 'ok' : 'no_observations');
    const transform = payload.template?.transform || 'raw', valueUnit = transform === 'rebase' ? '（起点=100）' : payload.unit ? ' ' + payload.unit : '';
    const warnings = (payload.warnings || []).map(message);
    let html = '<h2>周期统计</h2>';
    if (status !== 'ok') html += `<p class="charts-footnote" role="status">${esc(payload.reason || p?.reason || message(status))}${p ? `；当前位置 ${esc(p.date)}，有效往期 ${esc(n)} 期` : ''}</p>`;
    if (status === 'ok') {
      const recent = p.vs_recent_mean, recentCount = recent?.cycles || 0;
      const recentValues = (p.peers || []).slice(0, recentCount).map(item => item.value).filter(value => typeof value === 'number' && Number.isFinite(value));
      const recentMean = recentValues.length ? recentValues.reduce((a, b) => a + b, 0) / recentValues.length : null;
      const cells = [['同期排名（从高到低）', `${p.rank}/${n + 1}`], ['往期区间', `${fmt(p.min)} – ${fmt(p.max)}`], ['往期中位数', fmt(p.median)], ['往期均值', fmt(p.mean)]];
      if (recent) {
        cells.push([`较近 ${recentCount} 期均值`, fmt(recentMean)]);
        cells.push([`当前较近 ${recentCount} 期均值${recent.kind === 'pct' ? '变化率' : '差值'}`, recent.kind === 'pct' ? pct(recent.value) : fmt(recent.value)]);
      }
      html += `<p class="charts-muted">当前位置 ${esc(p.date)} · 第 ${esc(p.x)} 天 · 当前值 ${esc(fmt(p.value))}${esc(valueUnit)}</p>`;
      html += `<p class="charts-muted">数值口径：${transform === 'rebase' ? '起点=100（无量纲）' : transform === 'change' ? '相对起点变化' : '原值'}${transform !== 'rebase' && payload.unit ? '（' + esc(payload.unit) + '）' : ''}</p>`;
      html += '<dl class="charts-stats-grid">' + cells.map(([label, value]) => `<div class="charts-stat"><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join('') + '</dl>';
      html += '<h3>往期后续（逐期列示）</h3>';
      html += `<p class="charts-muted">后续变化按原始观测计算${payload.unit ? '；变化值单位：' + esc(payload.unit) : '；原始单位未注明'}。</p>`;
      for (const forward of payload.forward || []) {
        html += `<div class="charts-panel"><p><strong>后续 ${esc(forward.h)} 期（${esc(fmt(forward.days))} 天）</strong> · 上涨 ${esc(forward.up)} 段/共 ${esc(forward.n)} 段</p>`;
        html += `<p class="charts-muted">变化中位数 ${esc(fmt(forward.median))} · 区间 ${esc(fmt(forward.min))} – ${esc(fmt(forward.max))}</p>`;
        html += '<div class="charts-table-scroll"><table class="charts-table"><thead><tr><th>历史段</th><th>匹配日 → 后续日</th><th>变化值</th><th>变化率</th></tr></thead><tbody>';
        html += (forward.items || []).map(item => `<tr><td>${esc(item.label)}</td><td>${esc(item.date || '—')} → ${esc(item.end_date || '—')}</td><td>${esc(fmt(item.change))}</td><td>${esc(pct(item.pct))}</td></tr>`).join('');
        if (!(forward.items || []).length) html += '<tr><td colspan="4">此窗口没有可匹配的后续观测</td></tr>';
        html += '</tbody></table></div></div>';
      }
    }
    if (showWarnings && warnings.length) html += `<p class="charts-footnote">${esc([...new Set(warnings)].join('；'))}</p>`;
    html += `<p class="charts-muted">历史参照，不是预测；样本 ${esc(n)} 期。后续窗口分别标注实际有效样本数；缺失观测不补点。</p>`;
    element.innerHTML = html;
  }
  function download(blob, filename) {
    const url = URL.createObjectURL(blob), link = root.document.createElement('a');
    link.href = url; link.download = filename; link.click(); setTimeout(() => URL.revokeObjectURL(url), 10000);
  }
  function exports(element, getChart, getTitle) {
    element.classList.add('charts-export');
    element.innerHTML = '<button type="button" class="charts-button" data-export-copy>复制为图片</button><button type="button" class="charts-button" data-export-svg>下载 SVG</button><a class="charts-button" data-export-png hidden>下载 PNG</a><span role="status"></span>';
    const status = element.querySelector('[role=status]'), pngLink = element.querySelector('[data-export-png]');
    let pngURL;
    const title = () => (getTitle() || '图表').replace(/[\\/:*?"<>|]/g, '_');
    element.querySelector('[data-export-svg]').addEventListener('click', () => {
      const chart = getChart(); if (!chart) { status.textContent = '请等待图表加载完成'; return; }
      download(new Blob([chart.toSVG()], {type: 'image/svg+xml;charset=utf-8'}), title() + '.svg');
    });
    element.querySelector('[data-export-copy]').addEventListener('click', async event => {
      const chart = getChart(); if (!chart) { status.textContent = '请等待图表加载完成'; return; }
      event.currentTarget.disabled = true; status.textContent = '正在生成图片…';
      try {
        const png = chart.toPNGBlob();
        let copyError;
        try {
          if (!root.ClipboardItem || !root.navigator.clipboard?.write) throw new Error('clipboard_unavailable');
          // Start clipboard.write in the click activation, before rasterisation completes.
          await root.navigator.clipboard.write([new root.ClipboardItem({'image/png': png})]);
        } catch (error) { copyError = error; }
        const blob = await png;
        if (pngURL) URL.revokeObjectURL(pngURL);
        pngURL = URL.createObjectURL(blob); pngLink.href = pngURL; pngLink.download = title() + '.png';
        pngLink.hidden = !copyError;
        status.textContent = copyError ? '复制失败或浏览器不支持，请点击下载 PNG' : '图片已复制';
      } catch (error) { status.textContent = error.message + '；可下载 SVG'; }
      finally { element.querySelector('[data-export-copy]').disabled = false; }
    });
    root.addEventListener('pagehide', () => { if (pngURL) URL.revokeObjectURL(pngURL); }, {once: true});
  }
  function fillTemplates(select, items, selected) {
    select.replaceChildren();
    for (const item of items) {
      const option = root.document.createElement('option'); option.value = item.id;
      option.textContent = (item.builtin ? '内置 · ' : '已保存 · ') + item.template.name;
      option.selected = item.id === selected; select.append(option);
    }
  }
  function validMonthDay(value) {
    if (!/^\d{2}-\d{2}$/.test(value)) return false;
    const date = new Date('2000-' + value + 'T00:00:00Z');
    return Number.isFinite(date.getTime()) && date.toISOString().slice(5, 10) === value;
  }
  root.ChartUI = Object.freeze({esc, fmt, pct, message, api, pageURL, notice, notices, noticeMarker, importSummary, importMarkup, stats, exports, fillTemplates, validMonthDay});
})(window);
