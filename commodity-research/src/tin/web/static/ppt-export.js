/* Render bound slots locally, then submit PNGs and explicit data provenance. */
(function (root) {
  'use strict';
  const P = root.PptUI || (typeof require === 'function' ? require('./ppt.js') : null);
  const finite = value => typeof value === 'number' && Number.isFinite(value);
  function validDate(value) {
    if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
    const stamp = Date.parse(value + 'T00:00:00Z');
    return Number.isFinite(stamp) && new Date(stamp).toISOString().slice(0, 10) === value;
  }
  function observations(points, cutoff) {
    const result = (points || []).filter(point => finite(point.value) && validDate(point.date));
    if (result.some(point => point.date > cutoff)) throw new Error('来源返回了截止日之后的观测，请刷新核对');
    if (!result.length) throw new Error('截止日前暂无有效观测');
    return result;
  }
  function latestRows(payload, rows, cutoff) {
    if (!Number.isInteger(rows) || rows < 1 || rows > 60) throw new Error('数据小表行数须为 1 至 60 的整数');
    const byDate = new Map(observations(payload.points, cutoff).map(point => [point.date, point]));
    return [...byDate.values()].sort((a, b) => a.date.localeCompare(b.date)).slice(-rows);
  }
  function lastObservation(points, cutoff) { return observations(points, cutoff).map(point => point.date).sort().at(-1); }
  async function renderSlot(slot, binding, cutoff, dependencies = {}) {
    const request = dependencies.api || P.api, kit = dependencies.kit || root.ChartKit;
    if (!slot.replaceable || !binding || binding.kind === 'keep') throw new Error('此图位保留原图');
    if (!validDate(cutoff)) throw new Error('请选择有效的数据截止日');
    const size = P.slotSize(slot);
    if (size.width * size.height * 4 > 32000000 || Math.max(size.width, size.height) * 2 > 16384) throw new Error('图位尺寸超过浏览器图片生成上限');
    let data, definition, points, title, warnings = [], dataAsOf, target, renderer;
    try {
      if (binding.kind === 'chart') {
        const chartPath = '/charts/' + encodeURIComponent(binding.chart_id), chart = await request(chartPath);
        data = await request(chartPath + '/data?' + new URLSearchParams({date: cutoff}));
        if (chart.revision !== data.revision) throw new Error('图表定义在读取期间已更新，请重新生成');
        definition = chart.definition; title = definition.title;
        warnings = [...(chart.warnings || []), ...(data.notices || []), ...(data.warnings || [])];
        if (warnings.some(value => ['missing', 'axis_unit_mismatch'].includes(value.code)) || data.curves?.some(curve => curve.status === 'missing')) throw new Error('绑定图表的指标失效或单位不一致');
        points = (data.segments || data.curves || []).flatMap(part => part.points || []);
      } else if (binding.kind === 'series') {
        // The existing history endpoint accepts limit >= 2. Trim locally for one row.
        if (!Number.isInteger(binding.rows) || binding.rows < 1 || binding.rows > 60) throw new Error('数据小表行数须为 1 至 60 的整数');
        data = await request('/series/' + encodeURIComponent(binding.series_id) + '/history?' + new URLSearchParams({date: cutoff, limit: Math.max(2, binding.rows)}));
        points = latestRows(data, binding.rows, cutoff); title = data.name || '数据小表';
      } else throw new Error('无法识别的绑定类型');
      dataAsOf = lastObservation(points, cutoff);
      target = dependencies.createTarget ? dependencies.createTarget(size) : root.document.createElement('div');
      target.className = 'ppt-render-target'; target.style.width = size.width + 'px'; target.style.height = size.height + 'px';
      if (!dependencies.createTarget) root.document.body.append(target);
      renderer = binding.kind === 'chart' ? kit.render(target, data, {...size, exactSize: true, export: true, definition, title}) : kit.renderTable(target, points, {...size, export: true, title});
      const blob = await renderer.toPNGBlob();
      if (blob.type !== 'image/png' || blob.size === 0) throw new Error('未能生成有效 PNG 图片');
      if (blob.size > 4 * 1024 * 1024) throw new Error('生成的 PNG 超过 4 MB，此图位保留原图');
      return {blob, dataAsOf, title, warnings, ...size};
    } catch (error) { if (dataAsOf) error.dataAsOf = dataAsOf; throw error; }
    finally { renderer?.destroy(); target?.remove(); }
  }
  function buildFormData(date, markHeadlines, images, meta) {
    const form = new root.FormData(); form.append('date', date); form.append('mark_headlines', String(!!markHeadlines));
    form.append('meta', JSON.stringify(meta));
    for (const [id, blob] of images) form.append(id, blob, 'slot.png');
    return form;
  }
  function downloadURL(report, id) {
    const path = '/api/sn/ppt-templates/' + encodeURIComponent(id) + '/exports/' + encodeURIComponent(report.export_id);
    return report.download_url === path ? P.pageURL(path) : null;
  }
  const failureReasons = new Set(['missing_media', 'chart_missing', 'chart_invalid', 'series_missing',
    'chart_unit_mismatch', 'source_unavailable', 'data_read_failed', 'png_not_supplied']);
  function summarizeReport(report) {
    const pages = new Map(), reasons = new Map();
    for (const item of report.updated_slots || []) {
      if (!pages.has(item.slide)) pages.set(item.slide, {slide: item.slide, items: []});
      pages.get(item.slide).items.push(item);
    }
    for (const item of report.kept_slots || []) {
      if (!reasons.has(item.reason)) reasons.set(item.reason, {reason: item.reason, label: P.reason(item.reason) || '原因未提供',
        failed: failureReasons.has(item.reason), count: 0, items: []});
      const group = reasons.get(item.reason); group.count++; group.items.push(item);
    }
    const keptReasons = [...reasons.values()].sort((a, b) => b.count - a.count);
    return {updatedPages: [...pages.values()].sort((a, b) => a.slide - b.slide), keptReasons,
      keptSummary: keptReasons.map(group => `${group.label} ${group.count} 个`).join('、')};
  }
  function reportSections(report) {
    const E = P.esc, groups = summarizeReport(report);
    const row = item => `${E(item.slot_id)} · 数据截至 ${E(item.data_as_of || '未提供')}`;
    return `<h3>已更新图位（按页）与数据截至日</h3>${groups.updatedPages.length ? groups.updatedPages.map(page =>
      `<section class="ppt-report-page" data-report-page="${E(page.slide)}"><h4>第 ${E(page.slide)} 页 · ${page.items.length} 个图位</h4><ul class="ppt-report-list">${page.items.map(item => `<li>${row(item)}</li>`).join('')}</ul></section>`).join('') : '<p class="charts-muted">没有更新图位。</p>'}
      <h3>保留原图及原因</h3>${groups.keptReasons.length ? `<p class="ppt-kept-summary" data-report-kept-summary>${E(groups.keptSummary)}</p>` + groups.keptReasons.map(group =>
      `<details class="ppt-report-kept" data-report-reason="${E(group.reason)}"${group.failed ? ' open' : ''}><summary>${E(group.label)} ${group.count} 个</summary><ul class="ppt-report-list">${group.items.map(item => `<li>第 ${E(item.slide)} 页 · ${row(item)}${item.detail ? '；' + E(item.detail) : ''}</li>`).join('')}</ul></details>`).join('') : '<p class="charts-muted">无</p>'}`;
  }
  const helpers = {validDate, latestRows, lastObservation, renderSlot, buildFormData, downloadURL, summarizeReport, reportSections};
  if (typeof module !== 'undefined' && module.exports) module.exports = helpers;
  root.PptExport = Object.freeze(helpers);
  if (!root.document) return;
  const page = root.document.querySelector('[data-ppt-page="export"]'), configNode = root.document.getElementById('ppt-page-config');
  if (!page || !configNode) return;
  const config = JSON.parse(configNode.textContent); if (!config.enabled) return;
  const $ = id => root.document.getElementById(id), E = P.esc, path = '/ppt-templates/' + encodeURIComponent(config.id);
  const status = $('ppt-export-status'), fields = $('ppt-export-fields'), controller = new AbortController(), urls = new Set();
  const cards = new Map(), conflictedExports = new Set(); let record, busy = false, loading = false, conflict = false, closed = false;
  $('ppt-export-date').value = validDate(config.date) ? config.date : P.today();
  function controls() { fields.disabled = !config.can_write || !record || busy || loading || conflict; }
  function disposePreviews() { urls.forEach(url => root.URL.revokeObjectURL(url)); urls.clear(); cards.clear(); $('ppt-export-previews').replaceChildren(); }
  function previews() {
    disposePreviews();
    for (const slot of P.allSlots(record)) {
      const binding = record.bindings[slot.slot_id], kept = !slot.replaceable || !binding || binding.kind === 'keep';
      const card = root.document.createElement('article'); card.className = 'charts-card ppt-preview'; card.dataset.previewSlot = slot.slot_id;
      card.innerHTML = `<h3>第 ${E(slot.slide)} 页 · ${E(slot.name || slot.slot_id)}</h3><p class="charts-muted">${E(P.types[slot.kind] || '图片')}</p><div class="ppt-preview-viewport"></div><p data-preview-status role="status"></p><p class="charts-footnote" data-preview-date></p>`;
      card.querySelector('[data-preview-status]').textContent = kept ? '保留原图：' + P.reason(slot.preserve_reason || (binding?.kind === 'keep' ? 'keep' : 'unbound')) : '等待生成新图';
      cards.set(slot.slot_id, card); $('ppt-export-previews').append(card);
    }
    if (!cards.size) $('ppt-export-previews').textContent = '此模板没有图片图位；仍可导出并检查含数字的标题。';
  }
  function reportMarkup(report, changed = false) {
    const container = $('ppt-export-report'), updated = report.updated_slots || [], kept = report.kept_slots || [], titles = report.headlines_to_check || [];
    if (changed) conflictedExports.add(report.export_id);
    const suspect = conflictedExports.has(report.export_id);
    const href = downloadURL(report, config.id);
    container.hidden = false;
    container.innerHTML = `<h2>导出报告</h2><p>${E(report.date)} · 已更新 ${updated.length} 个图位 / 保留原图 ${kept.length} 个图位 · 修订 v${E(report.revision)}</p>
      ${suspect ? '<p class="charts-notice warning">生成期间模板版本发生变化，请重新加载并生成。</p>' : href ? `<p><a class="charts-button primary" id="ppt-download" href="${E(href)}" download>下载 PPTX</a></p>` : '<p class="charts-notice error">下载地址无效，请重新生成。</p>'}
      ${reportSections(report)}
      <h3>需核对标题${report.mark_headlines ? '（已标黄）' : ''}</h3>${titles.length ? '<ul class="ppt-report-list">' + titles.map(item => `<li>第 ${E(item.slide)} 页：${E(item.title)}</li>`).join('') + '</ul>' : '<p class="charts-muted">没有含数字的标题。</p>'}`;
  }
  async function load() {
    if (busy || loading) return; loading = true; controls(); P.notice(status, '正在读取模板…');
    try {
      record = await P.api(path, {signal: controller.signal});
      conflict = false; $('ppt-export-conflict').hidden = true;
      $('ppt-title').textContent = '导出 · ' + (record.title || record.filename);
      $('ppt-breadcrumb-title').textContent = record.title || record.filename;
      $('ppt-meta').textContent = `${record.slide_count} 页 · 修订 v${record.revision}`;
      $('ppt-export-summary').textContent = P.summary(P.counts(record)); previews();
      const previous = record.exports?.at(-1); if (previous) reportMarkup(previous); else $('ppt-export-report').hidden = true;
      P.notice(status, '');
    } catch (error) { if (error.name !== 'AbortError') P.notice(status, error.message, 'error'); }
    finally { loading = false; controls(); }
  }
  $('ppt-export-reload').addEventListener('click', load);
  $('ppt-export-form').addEventListener('submit', async event => {
    event.preventDefault(); if (!config.can_write || !record || busy || loading || conflict) return;
    const date = $('ppt-export-date').value, mark = $('ppt-mark-headlines').checked;
    if (!validDate(date)) { P.notice(status, '请选择有效的数据截止日', 'error'); return; }
    busy = true; controls(); previews(); $('ppt-export-report').hidden = true;
    const progress = $('ppt-render-progress'); progress.hidden = false; progress.value = 0;
    const images = new Map(), meta = Object.create(null), slots = P.allSlots(record);
    slots.forEach(slot => { meta[slot.slot_id] = {data_as_of: null}; });
    const selected = slots.filter(slot => slot.replaceable && record.bindings[slot.slot_id] && record.bindings[slot.slot_id].kind !== 'keep');
    progress.max = Math.max(1, selected.length); let completed = 0, failed = 0;
    try {
      P.notice(status, `正在生成 ${selected.length} 个已绑定图位…`);
      const results = await P.mapLimit(selected, 4, async (slot, index) => {
        const card = cards.get(slot.slot_id), output = card.querySelector('[data-preview-status]');
        output.textContent = '正在读取数据并生成图片…';
        try {
          if (controller.signal.aborted) throw new DOMException('生成已取消', 'AbortError');
          if (index >= 400) throw new Error('单次导出最多 400 张新图片，此图位保留原图');
          const result = await renderSlot(slot, record.bindings[slot.slot_id], date, {api: route => P.api(route, {signal: controller.signal})});
          if (closed) return;
          images.set(slot.slot_id, result.blob); meta[slot.slot_id] = {data_as_of: result.dataAsOf};
          const url = root.URL.createObjectURL(result.blob); urls.add(url);
          const img = root.document.createElement('img'); img.src = url; img.alt = result.title; card.querySelector('.ppt-preview-viewport').append(img);
          output.textContent = result.title + ' · 新图已生成';
          const warnings = [...new Set(result.warnings.map(value => root.ChartUI.message(value)).filter(Boolean))].join('；');
          card.querySelector('[data-preview-date]').textContent = `数据截至 ${result.dataAsOf}` + (warnings ? '；' + warnings : '');
        } catch (error) {
          if (closed) return;
          failed++; meta[slot.slot_id] = {data_as_of: error.dataAsOf || null, error: String(error.message).slice(0, 1000)};
          output.textContent = '保留原图：' + error.message; output.className = 'charts-notice warning';
        } finally {
          completed++; progress.value = completed;
          if (!closed) P.notice(status, `已完成 ${completed}/${selected.length} 个图位，${failed} 个失败将保留原图。`);
        }
      });
      const unexpected = results.find(result => result.status === 'rejected'); if (unexpected) throw unexpected.reason;
      if (closed) return;
      // Export currently has no revision input. Catch changes before sending pixels,
      // and check the report revision as well; the API must provide an atomic fence
      // to completely close the remaining race between this read and assembly.
      const latest = await P.api(path, {signal: controller.signal});
      if (latest.revision !== record.revision) throw Object.assign(new Error('模板在生成期间已更新，请重新加载并生成'), {status: 409});
      P.notice(status, `图位生成完成（${failed} 个失败保留原图），正在上传图片并组装 PPT…`);
      const report = await P.api(path + '/export', {method: 'POST', body: buildFormData(date, mark, images, meta), signal: controller.signal});
      const changed = report.revision !== record.revision + 1;
      reportMarkup(report, changed);
      if (changed) { conflict = true; $('ppt-export-conflict').hidden = false; P.notice(status, '生成期间模板版本发生变化，请重新加载并生成。', 'warning'); }
      else {
        record.revision = report.revision; $('ppt-meta').textContent = `${record.slide_count} 页 · 修订 v${record.revision}`;
        P.notice(status, `PPT 已生成：已更新 ${report.updated_slots.length} 个图位，保留原图 ${report.kept_slots.length} 个图位。`);
      }
    } catch (error) {
      if (error.status === 409) { conflict = true; $('ppt-export-conflict').hidden = false; }
      if (error.name !== 'AbortError') P.notice(status, error.message, 'error');
    } finally { busy = false; progress.hidden = true; images.clear(); controls(); }
  });
  root.addEventListener('pagehide', () => { closed = true; controller.abort(); urls.forEach(url => root.URL.revokeObjectURL(url)); urls.clear(); }, {once: true});
  load();
})(typeof window !== 'undefined' ? window : globalThis);
