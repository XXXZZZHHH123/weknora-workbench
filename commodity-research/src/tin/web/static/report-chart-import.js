/* Review report chart structures using the existing chart cards and renderer. */
(function () {
  'use strict';
  const panel = document.getElementById('report-chart-import'), configNode = document.getElementById('chart-page-config');
  if (!panel || !configNode) return;
  const config = JSON.parse(configNode.textContent), U = window.ChartUI, K = window.ChartKit;
  if (!config.enabled) return;
  const $ = id => document.getElementById('report-chart-' + id), E = U.esc;
  const path = '/charts/import/report', states = new Map(), sources = new Map(), controllers = new Set();
  let loaded = false, busy = false, invalidated = false, jobId, generation = 0, timer, closed = false;
  let sourceNext = null, sourceToken = 0, sourceTimer, sourcesBusy = false;
  async function api(url, options = {}) {
    const controller = new AbortController(); controllers.add(controller);
    const timeout = setTimeout(() => controller.abort(), 60000);
    try { return await U.api(path + url, {...options, signal: controller.signal}); }
    catch (error) { if (error.name === 'AbortError' && !closed) throw new Error('请求超时，请重试或重新识别'); throw error; }
    finally { clearTimeout(timeout); controllers.delete(controller); }
  }
  const post = (url, body) => api(url, {method: 'POST', body: JSON.stringify(body)});
  const reason = value => typeof value === 'string' ? value : value?.reason || value?.message || '处理失败';
  function controls() {
    $('file').disabled = !config.can_write || busy;
    $('search').disabled = !config.can_write || busy;
    $('model').disabled = !config.can_write || busy || !$('model').options.length;
    $('group').disabled = !config.can_write || busy;
    $('more').disabled = busy || sourcesBusy;
    $('recognize').disabled = !config.can_write || busy || !$('file').value || !$('model').value;
    const selected = [...states.values()].filter(state => state.selected && state.valid && !state.imported);
    $('apply').disabled = !config.can_write || busy || invalidated || !selected.length;
    $('selection').textContent = states.size ? `已选 ${selected.length} / ${states.size} 张；可能重复的图默认不勾选。` : '';
    for (const state of states.values()) {
      state.element.querySelector('[data-report-select]').disabled = !config.can_write || busy || invalidated || !state.valid || state.imported;
      state.element.querySelectorAll('select,button').forEach(control => { control.disabled = !config.can_write || busy || invalidated || state.imported; });
    }
  }
  function conflict(error) {
    if ([403, 409].includes(error.status)) {
      invalidated = true;
      for (const state of states.values()) { state.valid = false; state.selected = false; state.element.querySelector('[data-report-select]').checked = false; }
      controls();
    }
    U.notice($('status'), error.message, 'error');
  }
  function clearCards() {
    for (const state of states.values()) { state.version++; state.renderer?.destroy(); }
    states.clear(); $('cards').replaceChildren();
  }
  function selection(state) {
    return {chart_id: state.item.id, mappings: [...state.element.querySelectorAll('[data-report-mapping]')].map(select => select.value),
      chart_type: state.element.querySelector('[data-report-type]').value};
  }
  async function preview(state) {
    if (busy || invalidated || state.imported || !config.can_write) return;
    const version = ++state.version, own = generation;
    state.valid = false; state.selected = false; state.element.querySelector('[data-report-select]').checked = false;
    state.renderer?.destroy(); state.renderer = null;
    const plot = state.element.querySelector('[data-report-plot]'), notice = state.element.querySelector('[data-report-notice]');
    plot.textContent = '正在读取真实数据预览…'; controls();
    try {
      const result = await post('/preview', {job_id: jobId, group: $('group').value, ...selection(state)});
      if (closed || own !== generation || version !== state.version) return;
      const errors = (result.errors || []).map(reason);
      if (errors.length || !result.definition || !result.payload) {
        plot.textContent = ''; U.notice(notice, errors.join('；') || '暂无有效图表预览', 'error'); return;
      }
      state.renderer = K.render(plot, result.payload, {definition: result.definition, thumbnail: true, width: 680, height: 250});
      state.valid = true;
      const duplicate = result.possible_duplicate;
      state.selected = !duplicate;
      state.element.querySelector('[data-report-select]').checked = state.selected;
      U.notice(notice, [duplicate ? '可能重复：图表库已有同指标、同图型的图。' : '',
        U.noticeText ? U.noticeText(result.warnings || []) : (result.warnings || []).map(U.message).join('；')].filter(Boolean).join('；'), 'warning');
    } catch (error) {
      if (closed || own !== generation || version !== state.version) return;
      plot.textContent = ''; U.notice(notice, error.message + (!error.status ? '；可点击“预览”重试。' : ''), 'error');
      if ([403, 409].includes(error.status)) conflict(error);
    } finally { if (own === generation && version === state.version) controls(); }
  }
  function addCard(item) {
    const card = document.createElement('article'); card.className = 'charts-card'; card.dataset.reportChart = item.id;
    const pageLabel = item.page != null ? `第 ${item.page} 页` : item.page_label || '片段顺序';
    card.innerHTML = `<h2><label><input type="checkbox" data-report-select disabled> ${E(pageLabel)} · ${E(item.title)}</label></h2>` +
      '<div class="charts-fields"><label class="charts-field">图型<select data-report-type><option value="trend">走势</option><option value="seasonal">同期对比（按年份分列）</option><option value="comparison">多指标对比</option></select></label></div>' +
      (item.series || []).map((series, index) => `<label class="charts-field">原文图例：${E(series.name)}${series.unit ? `（${E(series.unit)}）` : ''} → 指标<select data-report-mapping="${index}"><option value="">请选择指标</option>${(series.candidates || []).map(candidate => `<option value="${E(candidate.id)}">${E(candidate.name || candidate.metric || candidate.id)}${candidate.unit ? ` · ${E(candidate.unit)}` : ''}${candidate.sheet ? ` · ${E(candidate.sheet)}` : ''}${candidate.source_file ? ` · ${E(candidate.source_file)}` : ''}</option>`).join('')}</select></label>`).join('') +
      '<button type="button" class="charts-button" data-report-preview>预览</button><div class="charts-thumbnail" data-report-plot></div><p role="status" data-report-notice hidden></p>' +
      `<details><summary>原文图表依据</summary><p class="charts-muted">${E(Array.isArray(item.evidence) ? item.evidence.map(value => typeof value === 'string' ? value : value.text || value.quote || '').join('\n') : item.evidence)}</p></details>`;
    const state = {item, element: card, valid: false, selected: false, version: 0, imported: false}; states.set(item.id, state);
    const type = card.querySelector('[data-report-type]'); type.value = item.chart_type || '';
    card.querySelectorAll('[data-report-mapping]').forEach((select, index) => { select.value = item.series[index].selected_id || ''; });
    card.querySelector('[data-report-select]').addEventListener('change', event => { state.selected = event.target.checked; controls(); });
    card.querySelectorAll('select').forEach(select => select.addEventListener('change', () => preview(state)));
    card.querySelector('[data-report-preview]').addEventListener('click', () => preview(state));
    $('cards').append(card);
    if (item.errors?.length) U.notice(card.querySelector('[data-report-notice]'), item.errors.map(reason).join('；'), 'warning');
    return state;
  }
  async function renderResult(data, own) {
    clearCards();
    if (data.group !== undefined) $('group').value = data.group;
    const queue = (data.charts || []).map(addCard);
    U.notice($('failures'), (data.failures || []).map(item => {
      const location = item.page != null ? `第 ${item.page} 页` : item.page_label || (item.batch != null ? `第 ${item.batch} 批` : '');
      return [location, item.title, reason(item)].filter(Boolean).join('：');
    }).join('\n'), 'warning');
    U.notice($('status'), queue.length ? `识别到 ${queue.length} 张候选图，请核对指标映射与真实数据预览。` : '没有可导入的图表结构。', queue.length ? '' : 'warning');
    controls();
    // Bounded previews keep a long report from starting every data read at once.
    await Promise.all(Array.from({length: Math.min(3, queue.length)}, async () => {
      while (queue.length && own === generation && !closed && !invalidated) await preview(queue.shift());
    }));
  }
  async function poll(own) {
    try {
      const data = await api('/jobs/' + encodeURIComponent(jobId));
      if (closed || own !== generation) return;
      const progress = data.progress;
      $('progress').textContent = typeof progress === 'string' ? progress : [progress?.message, progress?.total ? `${progress.completed || 0} / ${progress.total}` : ''].filter(Boolean).join(' · ');
      if (['complete', 'completed', 'done', 'success'].includes(data.status)) {
        busy = false; await renderResult(data, own); return;
      }
      if (['failed', 'error'].includes(data.status)) throw new Error(data.reason || data.error || '识别失败，请重试');
      timer = setTimeout(() => poll(own), 1000);
    } catch (error) { if (!closed && own === generation) { busy = false; conflict(error); controls(); } }
  }
  $('recognize').addEventListener('click', async () => {
    if (!config.can_write || busy || !$('file').value) return;
    const own = ++generation; clearTimeout(timer); clearCards(); invalidated = false; busy = true;
    U.notice($('result'), ''); U.notice($('failures'), ''); U.notice($('status'), '正在读取已解析片段并识别图表…'); controls();
    try {
      const data = await post('/recognize', {source: sources.get($('file').value).citation, model_id: $('model').value});
      if (own !== generation || closed) return;
      jobId = data.job_id; await poll(own);
    } catch (error) { if (!closed && own === generation) { busy = false; conflict(error); controls(); } }
  });
  $('apply').addEventListener('click', async () => {
    if (!config.can_write || busy || invalidated) return;
    const selected = [...states.values()].filter(state => state.selected && state.valid && !state.imported);
    if (!selected.length) return;
    busy = true; controls(); U.notice($('status'), '正在导入已确认的图表…');
    try {
      const data = await post('/apply', {job_id: jobId, group: $('group').value, charts: selected.map(selection)});
      const created = data.created || [], failed = data.failed || [];
      $('result').replaceChildren(); $('result').hidden = false; $('result').className = 'charts-notice';
      const summary = document.createElement('p'); summary.textContent = `导入完成：新建 ${created.length} 张，失败 ${failed.length} 张。`; $('result').append(summary);
      for (const item of created) {
        const link = document.createElement('a'); link.className = 'charts-button';
        link.href = U.pageURL('/sn/charts/' + encodeURIComponent(item.id)); link.textContent = item.title || item.definition?.title || '打开新建图表'; $('result').append(link);
      }
      for (const item of failed) { const p = document.createElement('p'); p.textContent = `${item.title || item.chart_id || ''}：${reason(item)}`; $('result').append(p); }
      const failedIds = new Set(failed.map(item => item.chart_id));
      for (const state of selected) if (!failedIds.has(state.item.id)) { state.imported = true; state.selected = false; state.element.querySelector('[data-report-select]').checked = false; }
      U.notice($('status'), '');
    } catch (error) { conflict(error); }
    finally { busy = false; controls(); }
  });
  $('file').addEventListener('change', () => {
    generation++; clearTimeout(timer); clearCards(); jobId = null; invalidated = false;
    $('group').value = (sources.get($('file').value)?.title || '').slice(0, 80);
    $('progress').textContent = ''; U.notice($('status'), ''); U.notice($('result'), ''); U.notice($('failures'), ''); controls();
  });
  $('model').addEventListener('change', controls);
  async function loadFiles(reset = false) {
    const own = ++sourceToken;
    if (reset) {
      sources.clear(); $('file').replaceChildren(new Option('请选择周报', '')); sourceNext = 0;
      generation++; clearTimeout(timer); clearCards(); jobId = null; invalidated = false;
      $('group').value = ''; $('progress').textContent = ''; U.notice($('result'), ''); U.notice($('failures'), '');
    }
    sourcesBusy = true; controls(); U.notice($('status'), '正在读取已授权周报…');
    try {
      const query = new URLSearchParams({search: $('search').value.trim(), offset: sourceNext || 0, limit: 50});
      const data = await api('/files?' + query);
      if (closed || own !== sourceToken) return;
      for (const item of data.items || []) {
        const key = JSON.stringify(item.citation);
        if (sources.has(key)) continue;
        sources.set(key, item);
        const option = new Option([item.title, item.subtitle].filter(Boolean).join(' · '), key); $('file').append(option);
      }
      sourceNext = data.next_offset; $('more').hidden = sourceNext == null;
      U.notice($('status'), sources.size ? '' : '当前研究台没有匹配的已授权 PPTX / PDF 周报。');
    } catch (error) { if (!closed && own === sourceToken) { loaded = false; conflict(error); } }
    finally { if (own === sourceToken) { sourcesBusy = false; controls(); } }
  }
  $('search').addEventListener('input', () => { clearTimeout(sourceTimer); sourceTimer = setTimeout(() => loadFiles(true), 300); });
  $('more').addEventListener('click', () => { if (!sourcesBusy && !busy && sourceNext != null) loadFiles(); });
  panel.addEventListener('toggle', async () => {
    if (!panel.open || loaded) return;
    if (!config.can_write) { U.notice($('status'), config.write_reason); return; }
    loaded = true;
    await Promise.all([loadFiles(true), (async () => {
      try {
        const data = await U.api('/strategies/models'); $('model').replaceChildren();
        for (const model of data.models || []) $('model').append(new Option(model.name, model.id));
        if (!$('model').options.length) $('model').append(new Option('暂无可用模型', ''));
      } catch (error) { if (!closed) { loaded = false; conflict(error); } }
      controls();
    })()]);
  });
  window.addEventListener('pagehide', () => { closed = true; generation++; clearTimeout(timer); clearTimeout(sourceTimer); controllers.forEach(controller => controller.abort()); clearCards(); }, {once: true});
  controls();
})();
