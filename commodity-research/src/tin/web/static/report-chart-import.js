/* Switch import sources and review report charts with the shared renderer. */
(function () {
  'use strict';
  const panel = document.getElementById('report-chart-import'), configNode = document.getElementById('chart-page-config');
  if (!panel || !configNode) return;
  const config = JSON.parse(configNode.textContent), U = window.ChartUI, K = window.ChartKit;
  if (!config.enabled) return;
  const $ = id => document.getElementById('report-chart-' + id), E = U.esc;
  const path = '/charts/import/report', states = new Map(), sources = new Map(), controllers = new Set();
  const previewQueue = new Map();
  let loaded = false, busy = false, invalidated = false, jobId, generation = 0, closed = false;
  let reviewEdits = 0, pendingSheets = 0, activePreviews = 0;
  let sourceNext = null, sourceToken = 0, sourceTimer, sourcesBusy = false, modelEdits = 0;
  async function api(url, options = {}) {
    const controller = new AbortController(); controllers.add(controller);
    const timeout = setTimeout(() => controller.abort(), 60000);
    try { return await U.api(path + url, {...options, signal: controller.signal}); }
    catch (error) { if (error.name === 'AbortError' && !closed) throw new Error('请求超时，请重试或重新识别'); throw error; }
    finally { clearTimeout(timeout); controllers.delete(controller); }
  }
  const post = (url, body) => api(url, {method: 'POST', body: JSON.stringify(body)});
  const reason = value => typeof value === 'string' ? value : value?.reason || value?.message || '处理失败';
  const noticeText = values => [...new Set(values.map(U.message).filter(Boolean))].join('\n');
  const missingMessage = '指标库中未找到，请手动选择或跳过';
  const editedMessage = '映射或图型已修改，请重新预览。';
  const duplicateOf = state => state.validation ? state.validation.duplicateOf : !state.edited && state.item.duplicate_of;
  const canSelect = state => !invalidated && !state.imported &&
    (state.validation?.valid ?? (!state.edited && state.item.ready === true));
  const autoReady = (state, valid = canSelect(state)) => valid && !state.duplicate && !duplicateOf(state) &&
    !state.confirmAfterEdit && !state.validation?.reviewReason;
  const isChecked = (state, ready = autoReady(state)) => !state.imported && (state.manualChoice ?? ready);
  const selectedCards = () => [...states.values()].filter(state => canSelect(state) && isChecked(state));
  const observer = typeof IntersectionObserver === 'function' ? new IntersectionObserver(entries => {
    for (const entry of entries) {
      const state = states.get(entry.target.dataset.reportChart);
      if (state?.element !== entry.target) continue;
      if (entry.isIntersecting) queuePreview(state);
      else if (previewQueue.get(state) === false) previewQueue.delete(state);
    }
    drainPreviews();
  }, {rootMargin: '240px 0px'}) : null;
  function controls(jobBusy = chartJob.busy) {
    $('file').disabled = !config.can_write || busy;
    $('search').disabled = !config.can_write || busy;
    $('model').disabled = !config.can_write || busy || !$('model').options.length;
    $('group').disabled = !config.can_write || busy;
    $('more').disabled = busy || sourcesBusy;
    $('recognize').disabled = jobBusy || recognitionDisabled();
    let selected = 0, ready = 0, missing = 0;
    for (const state of states.values()) {
      const valid = canSelect(state), directlyImportable = autoReady(state, valid);
      const checked = isChecked(state, directlyImportable);
      if (valid && checked) selected++;
      if (directlyImportable) ready++;
      if (state.item.missing_indicators) missing++;
      state.checkbox.checked = checked;
      state.checkbox.disabled = !config.can_write || busy || !valid;
      state.controls.forEach(control => { control.disabled = !config.can_write || busy || invalidated || state.imported; });
    }
    $('rematch').hidden = !missing;
    $('rematch').disabled = !config.can_write || busy || jobBusy || invalidated || !jobId || !$('model').value;
    $('rematch').textContent = `重新匹配未对上的图（${missing} 张）`;
    $('apply').disabled = !config.can_write || busy || !selected;
    const count = states.size ? `已选 ${selected} 张 · 可直接导入 ${ready} 张` : '';
    if ($('selection').textContent !== count) $('selection').textContent = count;
  }
  function conflict(error) {
    if ([403, 409].includes(error.status)) {
      invalidated = true;
      controls();
    }
    U.notice($('status'), error.message, 'error');
  }
  function clearCards() {
    observer?.disconnect(); previewQueue.clear();
    for (const state of states.values()) state.renderer?.destroy();
    states.clear(); pendingSheets = 0; $('cards').replaceChildren();
  }
  function selection(state) {
    return {chart_id: state.item.id, mappings: [...state.element.querySelectorAll('[data-report-mapping]')].map(select => select.value),
      chart_type: state.element.querySelector('[data-report-type]').value};
  }
  function flags(state) {
    const missing = [...state.element.querySelectorAll('[data-report-mapping]')].some(select => !select.value);
    state.element.querySelectorAll('[data-report-missing]').forEach((node, index) => {
      node.hidden = !!state.element.querySelector(`[data-report-mapping="${index}"]`).value;
      node.textContent = missingMessage + (pendingSheets ? `。知识库还有 ${pendingSheets} 张工作表待确认，确认后可重新匹配` : '');
    });
    const duplicate = duplicateOf(state);
    const possibleDuplicate = (!state.edited || state.validation?.valid) && state.duplicate;
    const definitionFailed = state.validation ? state.validation.definitionFailed : !state.edited && state.item.definition_failed;
    const readyReason = state.validation?.reviewReason || (!state.validation &&
      (state.edited ? editedMessage : state.item.ready_reason));
    U.notice(state.element.querySelector('[data-report-flags]'), [
      state.imported ? '已导入' : '',
      duplicate ? `与${duplicate.page != null ? `第 ${duplicate.page} 页` : duplicate.page_label || '原文'}《${duplicate.title}》指标和图型相同` : '',
      possibleDuplicate ? '可能重复：图表库已有同指标、同图型的图。' : '',
      missing ? '未找到指标：' + missingMessage : '',
      definitionFailed && !missing ? '定义失败：请修改映射或图型后重新预览。' : '',
      !state.imported && !duplicate && !possibleDuplicate && !missing && !definitionFailed ? readyReason : '',
      canSelect(state) && !autoReady(state) ? '请核对后手动勾选。' : ''
    ].filter(Boolean).join('\n'), state.imported ? '' : 'warning');
    return missing;
  }
  function chartWarnings(item, result) {
    return [...(item.warnings || []), ...(result?.shape_warnings ?? item.shape_warnings ?? []),
      ...(result?.warnings ?? item.mapping_warnings ?? [])];
  }
  function queuePreview(state, manual = false) {
    if (!config.can_write || invalidated || state.imported || state.previewVersion === state.version ||
        (!manual && state.previewStarted)) return;
    if (manual || !previewQueue.has(state)) previewQueue.set(state, manual);
  }
  function drainPreviews() {
    if (busy || closed || invalidated || panel.hidden) return;
    while (activePreviews < 3 && previewQueue.size) {
      const available = [...previewQueue].filter(([state]) => state.previewVersion === undefined);
      const entry = available.find(([, manual]) => manual) || available[0];
      if (!entry) return;
      const [state] = entry;
      previewQueue.delete(state);
      if (state.imported) continue;
      observer?.unobserve(state.element);
      state.previewStarted = true;
      state.previewVersion = state.version;
      activePreviews++;
      void preview(state).finally(() => {
        activePreviews--;
        state.previewVersion = undefined;
        drainPreviews();
      });
    }
  }
  async function preview(state) {
    const version = state.version, duplicateVersion = state.duplicateVersion;
    const isCurrent = () => !closed && !invalidated && !state.imported &&
      states.get(state.item.id) === state && version === state.version;
    state.renderer?.destroy(); state.renderer = null;
    const plot = state.element.querySelector('[data-report-plot]'), notice = state.element.querySelector('[data-report-notice]');
    if (flags(state)) {
      plot.textContent = ''; U.notice(notice, noticeText(chartWarnings(state.item)), 'warning'); return;
    }
    plot.textContent = '正在读取真实数据预览…';
    try {
      const result = await post('/preview', {job_id: jobId, group: $('group').value, ...selection(state)});
      if (!isCurrent()) return;
      const errors = (result.errors || []).map(reason);
      const reportedDuplicate = result.duplicate_of === undefined ? duplicateOf(state) : result.duplicate_of;
      const duplicate = duplicateVersion === state.duplicateVersion ? reportedDuplicate : duplicateOf(state);
      if (duplicateVersion === state.duplicateVersion) state.duplicate = !!result.possible_duplicate;
      if (errors.length || !result.definition || !result.payload) {
        state.validation = {valid: false, definitionFailed: !result.definition, duplicateOf: duplicate,
          reviewReason: result.ready_reason || errors.join('；') || '暂无有效图表预览'};
        plot.textContent = ''; U.notice(notice, errors.join('；') || '暂无有效图表预览', 'error'); return;
      }
      state.validation = {valid: true, duplicateOf: duplicate,
        reviewReason: result.range_mismatch || (result.ready === false && !reportedDuplicate && !result.possible_duplicate)
          ? result.ready_reason || (result.range_mismatch ? '数值范围与原图刻度不符，请核对指标。' : '请核对预览结果。') : ''};
      state.renderer = K.render(plot, result.payload, {definition: result.definition, thumbnail: true, width: 680, height: 250});
      U.notice(notice, noticeText(chartWarnings(state.item, result)), 'warning');
    } catch (error) {
      if (!isCurrent()) return;
      if (error.status === 422) state.validation = {valid: false, definitionFailed: true, duplicateOf: duplicateOf(state)};
      plot.textContent = ''; U.notice(notice, error.message + (!error.status ? '；可点击“预览”重试。' : ''), 'error');
      if ([403, 409].includes(error.status)) conflict(error);
    } finally { if (isCurrent()) { flags(state); controls(); } }
  }
  function addCard(item, imported = false) {
    const card = document.createElement('article'); card.className = 'charts-card'; card.dataset.reportChart = item.id;
    const pageLabel = item.page != null ? `第 ${item.page} 页` : item.page_label || '片段顺序';
    const shape = item.shape || {}, originalShape = [
      shape.season_start_month ? `${shape.season_start_month}月起` : '',
      shape.period_labels?.join('、'),
      shape.y_ticks ? `刻度 ${shape.y_ticks.min}–${shape.y_ticks.max}` : '',
    ].filter(Boolean).join(' · ');
    card.innerHTML = `<h2><label><input type="checkbox" data-report-select disabled> ${E(pageLabel)} · ${E(item.title)}</label></h2><p role="status" data-report-flags hidden></p>` +
      (originalShape ? `<p class="charts-muted" data-report-shape>原图：${E(originalShape)}</p>` : '') +
      '<div class="charts-fields"><label class="charts-field">图型<select data-report-type><option value="trend">走势</option><option value="seasonal">同期对比（按年份分列）</option><option value="comparison">多指标对比</option></select></label></div>' +
      (item.series || []).map((series, index) => `<label class="charts-field">原文图例：${E(series.name)}${series.unit ? `（${E(series.unit)}）` : ''} → 指标<select data-report-mapping="${index}"><option value="">请选择指标</option>${(series.candidates || []).map(candidate => `<option value="${E(candidate.id)}">${E(candidate.name || candidate.metric || candidate.id)}${candidate.unit ? ` · ${E(candidate.unit)}` : ''}${candidate.sheet ? ` · ${E(candidate.sheet)}` : ''}${candidate.source_file ? ` · ${E(candidate.source_file)}` : ''}</option>`).join('')}</select></label>` +
        (series.legends?.length ? `<p class="charts-muted">${item.legend_semantics === 'periods' ? '原文周期图例' : '原文图例'}：${E(series.legends.join('、'))}</p>` : '') +
        `<p class="charts-notice warning" data-report-missing="${index}" hidden></p>`).join('') +
      '<button type="button" class="charts-button" data-report-preview>预览</button><div class="charts-thumbnail" data-report-plot></div><p role="status" data-report-notice hidden></p>' +
      `<details><summary>原文图表依据</summary><p class="charts-muted">${E(Array.isArray(item.evidence) ? item.evidence.map(value => typeof value === 'string' ? value : value.text || value.quote || '').join('\n') : item.evidence)}</p></details>`;
    const checkbox = card.querySelector('[data-report-select]');
    const state = {item, element: card, checkbox, controls: card.querySelectorAll('select,button'), version: 0,
      imported, duplicateVersion: 0, duplicate: !!item.possible_duplicate};
    states.set(item.id, state);
    const type = card.querySelector('[data-report-type]'); type.value = item.chart_type || '';
    card.querySelectorAll('[data-report-mapping]').forEach((select, index) => { select.value = item.series[index].selected_id || ''; });
    checkbox.addEventListener('change', event => {
      reviewEdits++; state.manualChoice = event.target.checked; controls();
    });
    card.querySelectorAll('select').forEach(select => select.addEventListener('change', () => {
      reviewEdits++;
      if (!state.edited || state.validation) state.confirmAfterEdit ||= !autoReady(state);
      state.edited = true;
      state.validation = undefined;
      state.version++;
      state.renderer?.destroy(); state.renderer = null;
      const missing = flags(state);
      card.querySelector('[data-report-plot]').textContent = missing ? '' : '等待预览…';
      controls();
      queuePreview(state, true); drainPreviews();
    }));
    card.querySelector('[data-report-preview]').addEventListener('click', () => {
      reviewEdits++; queuePreview(state, true); drainPreviews();
    });
    flags(state);
    U.notice(card.querySelector('[data-report-notice]'), noticeText([
      ...(item.errors || []).map(reason), ...chartWarnings(item)
    ]), 'warning');
    return state;
  }
  function renderResult(data) {
    clearCards();
    pendingSheets = data.source_status_hint?.pending || 0;
    if (data.group !== undefined) $('group').value = data.group;
    const groups = new Map();
    function addToGroup(item, failed = false) {
      const index = Number.isInteger(item.source_index) ? item.source_index : null;
      // Real pages group cards; without page numbers, one grid in source order
      // (each card title keeps its fragment label) instead of one group per fragment.
      const key = item.page != null ? `page:${item.page}` : 'source-order';
      if (!groups.has(key)) groups.set(key, {label: item.page != null ? `第 ${item.page} 页` : '按原文顺序（该文件未提供可靠页码）',
        order: item.page ?? Number.MAX_SAFE_INTEGER, entries: []});
      const group = groups.get(key);
      group.order = Math.min(group.order, index ?? item.page ?? Number.MAX_SAFE_INTEGER);
      group.entries.push({item, failed});
    }
    (data.charts || []).forEach(item => addToGroup(item));
    (data.failures || []).forEach(item => addToGroup(item, true));
    for (const [key, group] of [...groups.entries()].sort((a, b) => a[1].order - b[1].order)) {
      const section = document.createElement('section'); section.dataset.reportPage = key;
      const heading = document.createElement('h2'); heading.textContent = group.label;
      const cards = document.createElement('div'); cards.className = 'charts-grid';
      section.append(heading, cards); $('cards').append(section);
      for (const {item, failed} of group.entries.sort((a, b) => (a.item.source_index ?? 0) - (b.item.source_index ?? 0))) {
        if (!failed) {
          const state = addCard(item, !!data.applied?.[item.id]);
          cards.append(state.element);
          if (!state.imported) observer?.observe(state.element);
          continue;
        }
        const card = document.createElement('article'); card.className = 'charts-card'; card.dataset.reportFailure = '';
        const title = document.createElement('h2'); title.textContent = item.title || '未完成的图表识别';
        const message = document.createElement('p'); message.className = 'charts-notice warning';
        message.textContent = '定义失败：' + reason(item); card.append(title, message); cards.append(card);
      }
    }
    U.notice($('failures'), (data.failures || []).map(item => {
      const location = item.page != null ? `第 ${item.page} 页` : item.page_label || (item.batch != null ? `第 ${item.batch} 批` : '');
      return [location, item.title, reason(item)].filter(Boolean).join('：');
    }).join('\n'), 'warning');
    U.notice($('status'), states.size ? `识别到 ${states.size} 张候选图，请核对指标映射，按需预览真实数据。` : '没有可导入的图表结构。', states.size ? '' : 'warning');
    controls();
  }
  function preserveEdits(previous, updated) {
    const previousSelection = selection(previous);
    if (previousSelection.chart_type !== previous.item.chart_type) {
      updated.element.querySelector('[data-report-type]').value = previousSelection.chart_type;
      updated.edited = true;
    }
    updated.element.querySelectorAll('[data-report-mapping]').forEach((select, index) => {
      const old = previousSelection.mappings[index];
      if (old !== (previous.item.series[index]?.selected_id || '') && [...select.options].some(option => option.value === old)) {
        select.value = old;
        updated.edited = true;
      }
    });
    if (updated.edited) updated.confirmAfterEdit = previous.confirmAfterEdit;
  }
  function refreshCards(data) {
    pendingSheets = data.source_status_hint?.pending || 0;
    for (const item of data.charts || []) {
      const state = states.get(item.id);
      if (!state) continue;
      state.imported ||= !!data.applied?.[item.id];
      if (state.item.missing_indicators && !state.imported) {
        observer?.unobserve(state.element); previewQueue.delete(state); state.renderer?.destroy();
        const updated = addCard(item);
        updated.manualChoice = state.manualChoice;
        if (state.edited) preserveEdits(state, updated);
        state.element.replaceWith(updated.element);
        flags(updated); observer?.observe(updated.element);
        continue;
      }
      state.item = item;
      if (!state.edited) {
        state.duplicate = !!item.possible_duplicate;
        state.duplicateVersion++;
        if (state.validation) state.validation.duplicateOf = item.duplicate_of;
        else U.notice(state.element.querySelector('[data-report-notice]'), noticeText([
          ...(item.errors || []).map(reason), ...chartWarnings(item)
        ]), 'warning');
      }
      flags(state);
    }
    controls(); drainPreviews();
  }
  function recognitionDisabled() {
    return !config.can_write || busy || !$('file').value || !$('model').value;
  }
  const reviewSnapshot = () => [generation, $('file').value, $('group').value, reviewEdits];
  const chartJob = ResearchAnalysisJobs.create({
    purpose: 'report_chart_import',
    button: $('recognize'),
    status: $('progress'),
    restoreButton: $('restore'),
    disabled: recognitionDisabled,
    onSync: controls,
    isOpen: () => !closed && !panel.hidden,
    snapshot: reviewSnapshot,
    present: presentChartResult,
  });
  function presentChartResult(data, {restored}) {
    const rematchOf = data.request.rematch_of;
    const draftId = rematchOf || data.job?.id || data.job_id || data.id;
    const sameDraft = states.size && jobId === draftId;
    if (restored && states.size && !sameDraft) {
      ResearchAnalysisJobs.message($('progress'), '识别已完成，保留研究员当前的候选图和映射。');
      return;
    }
    const source = data.request.source;
    const selected = sources.get($('file').value);
    if (restored && !sameDraft && $('file').value) {
      if (!selected || ['source_binding_id', 'knowledge_id', 'file_version'].some(key => selected.citation[key] !== source[key])) {
        ResearchAnalysisJobs.message($('progress'), '识别已完成，保留研究员当前选择的周报。');
        return;
      }
    }
    if (restored && !sameDraft && $('group').value && $('group').value !== (selected?.title || '').slice(0, 80)
        && $('group').value !== data.group) {
      ResearchAnalysisJobs.message($('progress'), '识别已完成，保留研究员当前的图表分组。');
      return;
    }
    if (restored && !$('file').value) {
      const key = JSON.stringify(source);
      if (!sources.has(key)) {
        const title = data.setupReport?.name || data.group || '已识别的周报';
        sources.set(key, {citation: source, title});
        $('file').append(new Option(title, key));
      }
      $('file').value = key;
    }
    if (data.setupReport && [...$('model').options].some(option => option.value === data.model_id)) {
      $('model').value = data.model_id;
    }
    jobId = draftId; invalidated = false;
    const progress = data.progress;
    ResearchAnalysisJobs.message($('progress'), [progress?.message,
      progress?.total ? `${progress.completed || 0} / ${progress.total}` : ''].filter(Boolean).join(' · '));
    if (sameDraft) return refreshCards(data);
    U.notice($('result'), '');
    generation++; renderResult(data);
  }
  $('recognize').addEventListener('click', () => {
    if (recognitionDisabled() || chartJob.busy) return;
    void chartJob.submit('/api/sn' + path + '/recognize', {
      source: sources.get($('file').value).citation, model_id: $('model').value,
    });
  });
  $('rematch').addEventListener('click', () => {
    if ($('rematch').disabled) return;
    void chartJob.submit('/api/sn' + path + '/rematch', {job_id: jobId, model_id: $('model').value});
  });
  $('apply').addEventListener('click', async () => {
    if (!config.can_write || busy || invalidated) return;
    const selected = selectedCards(), own = generation;
    if (!selected.length) return;
    busy = true; controls(); U.notice($('status'), '正在导入已确认的图表…');
    try {
      const data = await post('/apply', {job_id: jobId, group: $('group').value, charts: selected.map(selection)});
      if (closed || own !== generation) return;
      const created = data.created || [], failed = data.failed || [];
      $('result').replaceChildren(); $('result').hidden = false; $('result').className = 'charts-notice';
      const summary = document.createElement('p'); summary.textContent = `导入完成：新建 ${created.length} 张，失败 ${failed.length} 张。`; $('result').append(summary);
      for (const item of created) {
        const link = document.createElement('a'); link.className = 'charts-button';
        link.href = U.pageURL('/sn/charts/' + encodeURIComponent(item.id)); link.textContent = item.title || item.definition?.title || '打开新建图表'; $('result').append(link);
      }
      for (const item of failed) { const p = document.createElement('p'); p.textContent = `${item.title || item.chart_id || ''}：${reason(item)}`; $('result').append(p); }
      const failedIds = new Set(failed.map(item => item.chart_id));
      reviewEdits++;
      for (const state of selected) if (!failedIds.has(state.item.id)) {
        state.imported = true;
        observer?.unobserve(state.element); previewQueue.delete(state);
      }
      for (const state of states.values()) {
        const duplicate = data.possible_duplicates?.[state.item.id];
        const readiness = data.readiness?.[state.item.id];
        if (!state.edited && duplicate !== undefined) {
          state.duplicate = duplicate;
          state.duplicateVersion++;
          if (readiness) state.item = {...state.item, ...readiness};
        }
        flags(state);
      }
      U.notice($('status'), '');
    } catch (error) { if (!closed && own === generation) conflict(error); }
    finally { busy = false; controls(); drainPreviews(); }
  });
  $('file').addEventListener('change', () => {
    generation++; clearCards(); jobId = null; invalidated = false;
    $('group').value = (sources.get($('file').value)?.title || '').slice(0, 80);
    $('progress').textContent = ''; U.notice($('status'), ''); U.notice($('result'), ''); U.notice($('failures'), ''); controls();
  });
  $('model').addEventListener('change', () => { modelEdits++; controls(); });
  async function loadFiles(reset = false) {
    const own = ++sourceToken;
    if (reset) {
      sources.clear(); $('file').replaceChildren(new Option('请选择周报', '')); sourceNext = 0;
      generation++; clearCards(); jobId = null; invalidated = false;
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
  async function loadReportSources() {
    if (loaded) return;
    if (!config.can_write) { U.notice($('status'), config.write_reason); return; }
    loaded = true;
    await Promise.all([loadFiles(), (async () => {
      try {
        const data = await U.api('/strategies/models'); $('model').replaceChildren();
        for (const model of data.models || []) $('model').append(new Option(model.name, model.id));
        if (!$('model').options.length) $('model').append(new Option('暂无可用模型', ''));
      } catch (error) { if (!closed) { loaded = false; conflict(error); } }
      controls();
    })()]);
  }

  const sourceTabs = [...document.querySelectorAll('[data-chart-import-source]')];
  const reportHash = '#' + panel.id;
  let activeSource;
  function chooseSource(source, updateURL = false, discover = true) {
    for (const tab of sourceTabs) {
      const selected = tab.dataset.chartImportSource === source;
      tab.setAttribute('aria-selected', String(selected));
      tab.tabIndex = selected ? 0 : -1;
      document.getElementById(tab.getAttribute('aria-controls')).hidden = !selected;
      document.getElementById(`chart-import-${tab.dataset.chartImportSource}-intro`).hidden = !selected;
    }
    if (updateURL) {
      const url = new URL(window.location.href);
      url.hash = source === 'report' ? reportHash : '';
      if (url.href !== window.location.href) history.replaceState(history.state, '', url);
    }
    if (activeSource !== source) {
      activeSource = source;
      document.dispatchEvent(new CustomEvent('chart-import:source', {detail: {source}}));
    }
    if (source === 'report') {
      drainPreviews();
      if (config.can_write && discover) void chartJob.discover();
      return loadReportSources();
    }
  }
  sourceTabs.forEach((tab, index) => {
    tab.addEventListener('click', () => chooseSource(tab.dataset.chartImportSource, true));
    tab.addEventListener('keydown', event => {
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % sourceTabs.length;
      if (event.key === 'ArrowLeft') next = (index + sourceTabs.length - 1) % sourceTabs.length;
      if (event.key === 'Home') next = 0;
      if (event.key === 'End') next = sourceTabs.length - 1;
      if (next === undefined) return;
      event.preventDefault();
      sourceTabs[next].focus();
      sourceTabs[next].click();
    });
  });
  function chooseLinkedSource() {
    chooseSource(window.location.hash === reportHash ? 'report' : 'native');
  }
  async function reviewSetup(setup) {
    const snapshot = () => JSON.stringify([...reviewSnapshot(), modelEdits, $('search').value]);
    const before = snapshot();
    await chooseSource('report', true, false);
    if (closed || busy || panel.hidden) return;
    U.notice($('status'), '正在载入周报图表候选…');
    try {
      const result = await setup.load();
      if (closed || busy || panel.hidden) return;
      if (chartJob.busy || before !== snapshot()) {
        U.notice($('status'), '编辑内容已改动，保留当前候选图和映射。', 'warning');
        return;
      }
      await presentChartResult({...result, request: {source: result.source}}, {restored: true});
    } catch (error) {
      if (!closed && !panel.hidden && before === snapshot()) U.notice($('status'), error.message, 'error');
    }
  }
  window.addEventListener('hashchange', chooseLinkedSource);
  const setup = window.WorkbenchSetupReview;
  const explicit = new URLSearchParams(location.search).has('analysis_job');
  if (config.can_write && (explicit || setup?.part === 'charts')) {
    void ResearchAnalysisJobs.requested('report_chart_import').then(async job => {
      if (closed || activeSource !== undefined) return;
      if (job) {
        void chooseSource('report', true, false);
        return chartJob.restore(job.id, {explicit: true});
      }
      if (await setup?.appliesTo('charts')) return reviewSetup(setup);
      chooseLinkedSource();
    });
  } else chooseLinkedSource();
  window.addEventListener('pagehide', () => {
    closed = true; generation++; chartJob.pause(); clearTimeout(sourceTimer);
    controllers.forEach(controller => controller.abort()); clearCards();
  }, {once: true});
  controls();
})();
