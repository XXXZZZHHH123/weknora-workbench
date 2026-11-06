(function () {
  'use strict';
  const node = document.getElementById('chart-page-config'); if (!node) return;
  const config = JSON.parse(node.textContent), U = window.ChartUI;
  if (!config.enabled) return;
  const $ = id => document.getElementById(id), sheets = new Map();
  let next = 0, busy = false, complete = false, invalidated = false, loadedCount = 0, total = 0;
  const keyFor = sheet => JSON.stringify([sheet.knowledge_id, sheet.sheet]);
  function controls() {
    const selected = [...sheets.values()].filter(s => s.selected);
    $('chart-import-apply').disabled = !config.can_write || !complete || invalidated || busy || !selected.length || selected.length > 200;
    $('chart-import-more').disabled = busy; $('chart-import-more').hidden = complete || invalidated;
    $('chart-import-hint').textContent = !config.can_write ? config.write_reason : !complete ? '请加载剩余预览后确认导入，避免导入尚未审阅的图表。' : selected.length > 200 ? '每次最多确认 200 个工作表，请分批选择。' : `已选 ${selected.length} 个工作表。起点覆盖仅用于年度重复周期，手工分段保留原始日期。`;
    $('chart-import-progress').textContent = `已预览 ${loadedCount} / ${total} 张${complete ? '，预览已全部加载' : '，下方汇总仅计已加载图表'}`;
  }
  function renderSheet(state) {
    if (!state.element) {
      state.element = document.createElement('section'); state.element.className = 'charts-panel charts-sheet';
      state.element.dataset.sheetKey = keyFor(state);
      state.element.innerHTML = '<header class="charts-sheet-header"><label><input type="checkbox" data-sheet-select> <span data-sheet-title></span></label><label class="charts-field">可选起点覆盖（MM-DD）<input data-sheet-start placeholder="不覆盖" maxlength="5" pattern="[0-9]{2}-[0-9]{2}"></label></header><p class="charts-muted" data-sheet-summary></p><div data-sheet-charts></div>';
      state.element.querySelector('[data-sheet-title]').textContent = `${state.source_file} / ${state.sheet}`;
      const select = state.element.querySelector('[data-sheet-select]'), start = state.element.querySelector('[data-sheet-start]');
      select.disabled = !config.can_write; start.disabled = !config.can_write;
      select.addEventListener('change', () => { state.selected = select.checked; controls(); });
      start.addEventListener('input', () => { state.start = start.value.trim(); start.setCustomValidity(''); });
      $('chart-import-sheets').append(state.element);
    }
    const charts = [...state.charts.values()], list = state.element.querySelector('[data-sheet-charts]');
    const expanded = list.querySelector('details')?.open;
    state.element.querySelector('[data-sheet-summary]').textContent = U.importSummary(charts);
    list.innerHTML = U.importMarkup(charts);
    if (expanded && list.querySelector('details')) list.querySelector('details').open = true;
  }
  async function load() {
    if (busy || complete || invalidated) return;
    busy = true; controls(); U.notice($('chart-import-status'), '正在读取原表图表预览…');
    try {
      const data = await U.api('/charts/import/native/preview', {method: 'POST', body: JSON.stringify({offset: next, limit: 200})});
      if (next && data.total !== total) throw new Error('原表图表数量已变化，请重新打开此页预览');
      total = data.total;
      for (const sheet of data.sheets) {
        const key = keyFor(sheet);
        if (!sheets.has(key)) sheets.set(key, {...sheet, charts: new Map(), selected: false, start: ''});
        const state = sheets.get(key);
        for (const chart of sheet.charts) state.charts.set(chart.chart_key, chart);
        renderSheet(state);
      }
      loadedCount = [...sheets.values()].reduce((count, sheet) => count + sheet.charts.size, 0);
      if (data.next_offset !== null && data.next_offset <= next) throw new Error('预览分页未前进，请重新打开此页');
      next = data.next_offset; complete = next === null;
      U.notice($('chart-import-status'), total ? '' : '当前来源没有可预览的原表图表。');
    } catch (error) { if (error.status === 409) invalidated = true; U.notice($('chart-import-status'), error.message, 'error'); }
    finally { busy = false; controls(); }
  }
  async function apply() {
    if (!config.can_write || !complete || invalidated || busy) return;
    const selected = [...sheets.values()].filter(sheet => sheet.selected);
    if (!selected.length || selected.length > 200) return;
    for (const sheet of selected) {
      if (sheet.start && !U.validMonthDay(sheet.start)) {
        const input = sheet.element.querySelector('[data-sheet-start]'); input.setCustomValidity('请填写有效的 MM-DD'); input.reportValidity(); input.focus(); return;
      }
    }
    const body = {sheets: selected.map(sheet => ({knowledge_id: sheet.knowledge_id, sheet: sheet.sheet,
      chart_keys: [...sheet.charts.keys()], ...(sheet.start ? {start: sheet.start} : {})}))};
    busy = true; controls(); U.notice($('chart-import-status'), '正在导入已确认的工作表…');
    try {
      const result = await U.api('/charts/import/native/apply', {method: 'POST', body: JSON.stringify(body)});
      U.notice($('chart-import-result'), `导入完成：新建 ${result.created.length} 张，跳过 ${result.skipped.length} 张，无法转换 ${(result.unconvertible || []).length} 张。`);
      const imported = new Set([...result.created.map(c => c.definition.origin.ref), ...result.skipped.map(c => c.chart_key)]);
      for (const sheet of selected) {
        sheet.charts.forEach(chart => { if (imported.has(chart.chart_key)) chart.already_imported = true; });
        sheet.selected = false; sheet.element.querySelector('[data-sheet-select]').checked = false; renderSheet(sheet);
      }
      U.notice($('chart-import-status'), '');
    } catch (error) { if (error.status === 409) invalidated = true; U.notice($('chart-import-status'), error.message + (error.status === 409 ? '；请重新打开此页预览后确认。' : ''), 'error'); }
    finally { busy = false; controls(); }
  }
  $('chart-import-more').addEventListener('click', load); $('chart-import-apply').addEventListener('click', apply);
  load();
})();
