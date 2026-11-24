(function () {
  'use strict';
  const node = document.getElementById('chart-page-config'); if (!node) return;
  const config = JSON.parse(node.textContent), U = window.ChartUI;
  if (!config.enabled) return;
  const $ = id => document.getElementById(id), sheets = new Map();
  const allButton = $('chart-import-all'), allDialog = $('chart-import-all-dialog');
  const allCancel = $('chart-import-all-cancel'), allResult = $('chart-import-all-result');
  const allDetails = $('chart-import-all-details'), allLibrary = $('chart-import-all-library');
  const pageLimit = 200;
  let next = 0, busy = false, complete = false, invalidated = false, loadedCount = 0, total = null;
  let previewStarted = false, allTotal = null, cancelRequested = false;
  const keyFor = sheet => JSON.stringify([sheet.knowledge_id, sheet.sheet]);
  const countSameIndicators = skipped => skipped.filter(item => item.reason === 'same_indicators').length;
  const sameIndicatorSummary = count => count ? `${count} 张${U.message('same_indicators')}。` : '';
  function controls() {
    const selected = [...sheets.values()].filter(s => s.selected);
    $('chart-import-apply').disabled = !config.can_write || !complete || invalidated || busy || !selected.length || selected.length > 200;
    $('chart-import-more').disabled = busy; $('chart-import-more').hidden = complete || invalidated;
    const count = allTotal ?? total;
    allButton.disabled = !config.can_write || !count || busy;
    allButton.textContent = count === null ? '全部导入' : `全部导入（${count} 张）`;
    $('chart-import-hint').textContent = !config.can_write ? config.write_reason : !complete ? '按工作表导入前，请加载剩余预览，避免导入尚未审阅的图表。' : selected.length > 200 ? '每次最多确认 200 个工作表，请分批选择。' : `已选 ${selected.length} 个工作表。起点覆盖仅用于年度重复周期，手工分段保留原始日期。`;
    $('chart-import-progress').textContent = total === null ? '' : `已预览 ${loadedCount} / ${total} 张${complete ? '，预览已全部加载' : '，下方汇总仅计已加载图表'}`;
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
      const data = await U.api('/charts/import/native/preview', {method: 'POST', body: JSON.stringify({offset: next, limit: pageLimit})});
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
      const summary = `导入完成：新建 ${result.created.length} 张，跳过 ${result.skipped.length} 张，无法转换 ${(result.unconvertible || []).length} 张。`;
      U.notice($('chart-import-result'), summary + sameIndicatorSummary(countSameIndicators(result.skipped)));
      const imported = new Set([...result.created.map(c => c.definition.origin.ref),
        ...result.skipped.filter(c => c.reason === 'already_imported').map(c => c.chart_key)]);
      for (const sheet of selected) {
        sheet.charts.forEach(chart => { if (imported.has(chart.chart_key)) chart.already_imported = true; });
        sheet.selected = false; sheet.element.querySelector('[data-sheet-select]').checked = false; renderSheet(sheet);
      }
      U.notice($('chart-import-status'), '');
    } catch (error) { if (error.status === 409) invalidated = true; U.notice($('chart-import-status'), error.message + (error.status === 409 ? '；请重新打开此页预览后确认。' : ''), 'error'); }
    finally { busy = false; controls(); }
  }

  function showRejections(rejected) {
    allDetails.open = false;
    allDetails.hidden = !rejected.length;
    const reasons = $('chart-import-all-reasons');
    reasons.replaceChildren();
    $('chart-import-all-details-summary').textContent = `${rejected.length} 张无法转换，展开查看原因`;
    if (!rejected.length) return;
    const items = document.createDocumentFragment();
    for (const chart of rejected) {
      const item = document.createElement('li');
      const source = [chart.source_file, chart.sheet, chart.title || chart.chart_key].filter(Boolean).join(' / ');
      const curves = (chart.missing_curves || []).map(curve => [curve.label, U.message(curve.reason)].filter(Boolean).join('：'));
      item.textContent = `${source}：${[U.message(chart.reason), ...curves].filter(Boolean).join('；')}`;
      items.append(item);
    }
    reasons.append(items);
  }

  async function applyAll() {
    if (allButton.disabled) return;
    allDialog.close();
    busy = true;
    cancelRequested = false;
    controls();
    $('chart-import-all-panel').hidden = false;
    allCancel.hidden = false;
    allCancel.disabled = false;
    allCancel.textContent = '取消全部导入';
    allCancel.focus();
    allLibrary.hidden = true;
    U.notice(allResult, '');
    showRejections([]);

    let offset = 0, version, created = 0, skipped = 0, sameIndicators = 0;
    const rejected = [];
    const summary = () => `新建 ${created}，跳过 ${skipped}，无法转换 ${rejected.length}`;
    const progress = () => {
      const processed = created + skipped + rejected.length;
      $('chart-import-all-progress').textContent = `已处理 ${processed} / ${allTotal ?? total} 张：${summary()}`;
    };
    progress();
    try {
      while (offset !== null && !cancelRequested) {
        const body = {offset, limit: pageLimit, ...(version ? {version} : {})};
        const page = await U.api('/charts/import/native/apply-page', {method: 'POST', body: JSON.stringify(body)});
        if (page.next_offset !== null && (!Number.isInteger(page.next_offset) || page.next_offset <= offset)) {
          throw new Error('原表图表分页未前进，请重新开始全部导入');
        }
        if (offset === 0) {
          version = page.version;
          allTotal = page.total;
          controls();
        }
        offset = page.next_offset;
        created += page.created_count;
        skipped += page.skipped_count;
        sameIndicators += countSameIndicators(page.skipped || []);
        rejected.push(...page.unconvertible);
        progress();
      }
      const finished = offset === null;
      const status = finished ? '全部导入完成：' : '已取消全部导入，已保存的图表会保留。';
      U.notice(allResult, `${status}${summary()}。${sameIndicatorSummary(sameIndicators)}`);
    } catch (error) {
      const reason = error instanceof TypeError ? '连接失败，请稍后重试' : error.message;
      U.notice(allResult, `全部导入失败：${reason}。已保存的图表会保留。${sameIndicatorSummary(sameIndicators)}`, 'error');
    } finally {
      const restoreFocus = document.activeElement === allCancel;
      allCancel.hidden = true;
      showRejections(rejected);
      allLibrary.hidden = false;
      busy = false;
      controls();
      if (restoreFocus) (allButton.disabled ? allLibrary : allButton).focus();
    }
  }

  $('chart-import-more').addEventListener('click', load); $('chart-import-apply').addEventListener('click', apply);
  allButton.addEventListener('click', () => { if (!allButton.disabled) allDialog.showModal(); });
  $('chart-import-all-dismiss').addEventListener('click', () => allDialog.close());
  $('chart-import-all-form').addEventListener('submit', event => { event.preventDefault(); void applyAll(); });
  allCancel.addEventListener('click', () => {
    cancelRequested = true;
    allCancel.disabled = true;
    allCancel.textContent = '当前页保存后停止';
  });
  document.addEventListener('chart-import:source', event => {
    if (event.detail.source !== 'native' || previewStarted) return;
    previewStarted = true;
    void load();
  });
})();
