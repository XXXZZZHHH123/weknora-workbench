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
  const cannotDraw = '这张图还不能生成。请为每条图例选择对应指标，检查图表类型，再点“预览”。';
  const cannotIdentify = '这部分报告暂时无法识别出可导入的图表。可以重新识别，或在图表库手动新建。';
  const reasonLabels = {
    '本页识别超时':'这一页识别超时，请重新识别。',
    '本批处理超时':'这部分识别超时，请重新识别。',
    '缺少依据片段':'正文里缺少能核对这张图的内容，可查看原文或在图表库手动新建。',
    '文件版本已变化，请重新识别':'周报已有新版本，请重新选择后识别。',
    '任务超时或中断，请重新识别':'识别超时或中断，请重新识别。',
    '指标已变化，请重新确认':'所选指标已变化，请重新确认各图例使用的指标。',
    '图型只能选择走势、同期对比或多指标对比':'请选择走势、同期对比或多指标对比。',
    '请为每条原文图例确认一个候选指标':'请为每条图例选择对应指标。',
    '指标库中未找到，请手动选择或跳过':'指标库中未找到，请手动选择或跳过',
    '只能选择该图例本次展示过的候选指标，请重新确认映射':'请重新为这条图例选择列表中可用的指标。',
    '映射指标已不在当前研究台来源目录，请重新识别':'所选指标已不在当前研究台，请重新识别并选择。',
    '候选图超过两种真实单位，请调整映射；不会换算单位':'一张图最多可以比较两种单位，请重新选择指标。',
    '请选择已有解析正文的PPTX或PDF周报':'请选择正文已经可以搜索的 PPTX 或 PDF 周报。',
    '所选周报尚未完成解析，请先在知识库完成解析':'所选周报还不能搜索正文，请在知识库查看文件处理进度。',
    '所选周报没有可读解析片段':'暂时无法读取所选周报的正文，请先在知识库核对文件。',
    '周报文件版本已变化，请重新选择':'周报文件版本已变化，请重新选择后识别。',
    '请求超时，请重试或重新识别':'预览等待时间过长，请点“预览”重试。',
    '周报解析正文超过200万字，请拆分资料后重试':'周报正文超过 200 万字，请拆分资料后重试。',
    '整体识别超时，保留已完成并复核的图；其余页面未处理':'识别超时。这里已有完成的图表，其余页面请重新识别。',
    '写入图表库失败，请重试':'这张图未能保存，请核对后重试。'
  };
  function reason(value, fallback = cannotIdentify) {
    const raw = typeof value === 'string' ? value : value?.reason || value?.message;
    if (Object.hasOwn(reasonLabels,raw)) return reasonLabels[raw];
    if (/^本批处理超过\d+秒$/.test(raw)) return '这部分识别超时，请重新识别。';
    return fallback;
  }
  function diagnostic(detail) {
    const node = $('diagnostics');
    if (!node) return;
    node.hidden = detail == null;
    node.querySelector('pre').textContent = detail == null ? '' :
      typeof detail === 'string' ? detail : JSON.stringify(detail,null,2);
  }
  const noticeText = values => [...new Set((values || []).map(U.message).filter(Boolean))].join('\n');
  const missingMessage = '指标库中未找到，请手动选择或跳过';
  const editedMessage = '已更改指标或图表类型，请重新预览。';
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
    const fallback = error.status===409 ? '文件版本或访问权限可能已有变化，请重新识别。' :
      error.status===403 ? '当前账号无法读取这份周报或导入图表，请联系研究台负责人。' : cannotIdentify;
    U.notice($('status'),reason(error.detail??error.message,fallback), 'error');
    diagnostic(error.detail??error.message);
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
      duplicate ? `与${duplicate.page != null ? `第 ${duplicate.page} 页` : '原文'}《${duplicate.title}》指标和图型相同` : '',
      possibleDuplicate ? '可能重复：图表库已有同指标、同图型的图。' : '',
      missing ? '未找到指标：' + missingMessage : '',
      definitionFailed && !missing ? cannotDraw : '',
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
    plot.textContent = '正在读取所选指标的数据，准备预览…';
    try {
      const result = await post('/preview', {job_id: jobId, group: $('group').value, ...selection(state)});
      if (!isCurrent()) return;
      const errors = (result.errors || []).map(error => reason(error, cannotDraw));
      const reportedDuplicate = result.duplicate_of === undefined ? duplicateOf(state) : result.duplicate_of;
      const duplicate = duplicateVersion === state.duplicateVersion ? reportedDuplicate : duplicateOf(state);
      if (duplicateVersion === state.duplicateVersion) state.duplicate = !!result.possible_duplicate;
      if (errors.length || !result.definition || !result.payload) {
        state.validation = {valid: false, definitionFailed: !result.definition, duplicateOf: duplicate,
          reviewReason: errors.join('；') || reason(result.ready_reason, cannotDraw)};
        diagnostic(result.errors);
        plot.textContent = ''; U.notice(notice, errors.join('；') || cannotDraw, 'error'); return;
      }
      state.validation = {valid: true, duplicateOf: duplicate,
        reviewReason: result.range_mismatch || (result.ready === false && !reportedDuplicate && !result.possible_duplicate)
          ? result.ready_reason || (result.range_mismatch ? '数值范围与原图刻度不符，请核对指标。' : '请核对预览结果。') : ''};
      state.renderer = K.render(plot, result.payload, {definition: result.definition, thumbnail: true, width: 680, height: 250});
      U.notice(notice, noticeText(chartWarnings(state.item, result)), 'warning');
    } catch (error) {
      if (!isCurrent()) return;
      if (error.status === 422) state.validation = {valid: false, definitionFailed: true, duplicateOf: duplicateOf(state)};
      diagnostic(error.detail ?? error.message);
      const fallback = error.status === 422 ? cannotDraw : '暂时读不到图表数据，请点“预览”重试。';
      plot.textContent = ''; U.notice(notice, reason(error.detail ?? error.message, fallback), 'error');
      if ([403, 409].includes(error.status)) conflict(error);
    } finally { if (isCurrent()) { flags(state); controls(); } }
  }
  function addCard(item, imported = false) {
    const card = document.createElement('article'); card.className = 'charts-card'; card.dataset.reportChart = item.id;
    const pageLabel = item.page != null ? `第 ${item.page} 页` : '按原文顺序';
    const shape = item.shape || {}, originalShape = [
      shape.season_start_month ? `${shape.season_start_month}月起` : '',
      shape.period_labels?.join('、'),
      shape.y_ticks ? `刻度 ${shape.y_ticks.min}–${shape.y_ticks.max}` : '',
    ].filter(Boolean).join(' · ');
    card.innerHTML = `<h2><label><input type="checkbox" data-report-select disabled> ${E(pageLabel)} · ${E(item.title || '未命名图表')}</label></h2><p role="status" data-report-flags hidden></p>` +
      (originalShape ? `<p class="charts-muted" data-report-shape>原图：${E(originalShape)}</p>` : '') +
      '<div class="charts-fields"><label class="charts-field">图型<select data-report-type><option value="trend">走势</option><option value="seasonal">同期对比（按年份分列）</option><option value="comparison">多指标对比</option></select></label></div>' +
      (item.series || []).map((series, index) => `<label class="charts-field">原文图例：${E(series.name)}${series.unit ? `（${E(series.unit)}）` : ''} → 指标<select data-report-mapping="${index}"><option value="">请选择指标</option>${(series.candidates || []).map(candidate => `<option value="${E(candidate.id)}">${E([candidate.name, candidate.metric].find(value => value && value !== candidate.id) || '未命名指标')}${candidate.unit ? ` · ${E(candidate.unit)}` : ''}${candidate.sheet ? ` · ${E(candidate.sheet)}` : ''}${candidate.source_file ? ` · ${E(candidate.source_file)}` : ''}</option>`).join('')}</select></label>` +
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
      ...(item.errors || []).map(error => reason(error, cannotDraw)), ...chartWarnings(item)
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
      // Cards without a page number share a group ordered by their position in the source.
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
        message.textContent = reason(item); card.append(title, message); cards.append(card);
      }
    }
    U.notice($('failures'), (data.failures || []).map(item => {
      const location = item.page != null ? `第 ${item.page} 页` : '';
      return [location, item.title, reason(item)].filter(Boolean).join('：');
    }).join('\n'), 'warning');
    const chartErrors = (data.charts || []).filter(item => item.errors?.length).map(item => ({id: item.id, errors: item.errors}));
    diagnostic(data.failures?.length || chartErrors.length ? {failures: data.failures, charts: chartErrors} : undefined);
    U.notice($('status'), states.size ? `识别到 ${states.size} 张图，请核对每条图例对应的指标和预览效果。` :
      '没有找到可导入的图。可重新识别，或在图表库手动新建。', states.size ? '' : 'warning');
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
          ...(item.errors || []).map(error => reason(error, cannotDraw)), ...chartWarnings(item)
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
      ResearchAnalysisJobs.message($('progress'), '识别已完成。请先处理当前图表和所选指标，再查看新结果。');
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
      for (const item of failed) {
        const failedIndex=selected.findIndex(state=>state.item.id===item.chart_id), original=selected[failedIndex]?.item;
        const title=item.title || original?.title || (original?.page != null ? `第 ${original.page} 页的图` : failedIndex>=0 ? `所选的第 ${failedIndex+1} 张图` : '未命名图表');
        const notice=document.createElement('p');
        notice.textContent=`${title}：${reason(item,cannotDraw)}`;
        $('result').append(notice);
      }
      diagnostic(failed.length ? failed : undefined);
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
        U.notice($('status'), '当前图表或所选指标已修改。请先处理当前修改，再查看后台结果。', 'warning');
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
