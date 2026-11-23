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
  const cannotDraw = '这张图还不能生成。请为每条图例选择对应指标，检查图表类型，再点“预览”。';
  const cannotIdentify = '这部分报告暂时无法识别出可导入的图表。可以重新识别，或在图表库手动新建。';
  const reasonLabels = {
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
  const noticeText = values => [...new Set((values || []).map(U.message).filter(Boolean))].join('；');
  const missingMessage = '指标库中未找到，请手动选择或跳过';
  function controls() {
    $('file').disabled = !config.can_write || busy;
    $('search').disabled = !config.can_write || busy;
    $('model').disabled = !config.can_write || busy || !$('model').options.length;
    $('group').disabled = !config.can_write || busy;
    $('more').disabled = busy || sourcesBusy;
    $('recognize').disabled = !config.can_write || busy || !$('file').value || !$('model').value;
    const selected = [...states.values()].filter(state => state.selected && state.valid && !state.imported);
    $('apply').disabled = !config.can_write || busy || invalidated || !selected.length;
    $('selection').textContent = states.size ? `共 ${states.size} 张，已选 ${selected.length} 张。可能重复、缺少指标或暂不能绘制的图需要您确认后勾选。` : '';
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
    const fallback = error.status===409 ? '文件版本或访问权限可能已有变化，请重新识别。' :
      error.status===403 ? '当前账号无法读取这份周报或导入图表，请联系研究台负责人。' : cannotIdentify;
    U.notice($('status'),reason(error.detail??error.message,fallback), 'error');
    diagnostic(error.detail??error.message);
  }
  function clearCards() {
    for (const state of states.values()) { state.version++; state.renderer?.destroy(); }
    states.clear(); $('cards').replaceChildren();
  }
  function selection(state) {
    return {chart_id: state.item.id, mappings: [...state.element.querySelectorAll('[data-report-mapping]')].map(select => select.value),
      chart_type: state.element.querySelector('[data-report-type]').value};
  }
  function flags(state) {
    const missing = [...state.element.querySelectorAll('[data-report-mapping]')].some(select => !select.value);
    state.element.querySelectorAll('[data-report-missing]').forEach((node, index) => {
      node.hidden = !!state.element.querySelector(`[data-report-mapping="${index}"]`).value;
    });
    U.notice(state.element.querySelector('[data-report-flags]'), [
      state.duplicate ? '可能重复：图表库已有同指标、同图型的图。' : '',
      missing ? '未找到指标：' + missingMessage : '',
      state.definitionFailed && !missing ? cannotDraw : '',
      state.needsReview && state.valid ? '请核对后手动勾选。' : ''
    ].filter(Boolean).join('；'), 'warning');
    return missing;
  }
  async function preview(state) {
    if (busy || invalidated || state.imported || !config.can_write) return;
    const version = ++state.version, own = generation;
    state.valid = false; state.selected = false; state.element.querySelector('[data-report-select]').checked = false;
    state.renderer?.destroy(); state.renderer = null;
    const plot = state.element.querySelector('[data-report-plot]'), notice = state.element.querySelector('[data-report-notice]');
    if (flags(state)) {
      plot.textContent = ''; U.notice(notice, noticeText([...(state.item.warnings || []), ...(state.item.mapping_warnings || [])]), 'warning'); controls(); return;
    }
    plot.textContent = '正在读取所选指标的数据，准备预览…'; controls();
    try {
      const result = await post('/preview', {job_id: jobId, group: $('group').value, ...selection(state)});
      if (closed || own !== generation || version !== state.version) return;
      const errors = (result.errors || []).map(error=>reason(error,cannotDraw));
      if (errors.length || !result.definition || !result.payload) {
        state.definitionFailed = true; state.needsReview = true; flags(state);
        diagnostic(result.errors);
        plot.textContent = ''; U.notice(notice, [...new Set(errors)].join('；') || cannotDraw, 'error'); return;
      }
      state.renderer = K.render(plot, result.payload, {definition: result.definition, thumbnail: true, width: 680, height: 250});
      state.valid = true;
      state.definitionFailed = false;
      state.duplicate = !!result.possible_duplicate;
      state.needsReview = state.needsReview || state.duplicate;
      state.selected = !state.duplicate && !state.needsReview;
      state.element.querySelector('[data-report-select]').checked = state.selected;
      flags(state);
      U.notice(notice, noticeText([...(state.item.warnings || []), ...(result.warnings || [])]), 'warning');
    } catch (error) {
      if (closed || own !== generation || version !== state.version) return;
      state.definitionFailed = true; state.needsReview = true; flags(state);
      diagnostic(error.detail??error.message);
      plot.textContent = ''; U.notice(notice, reason(error.detail??error.message,cannotDraw), 'error');
      if ([403, 409].includes(error.status)) conflict(error);
    } finally { if (own === generation && version === state.version) controls(); }
  }
  function addCard(item, target) {
    const card = document.createElement('article'); card.className = 'charts-card'; card.dataset.reportChart = item.id;
    const pageLabel = item.page != null ? `第 ${item.page} 页` : '按原文顺序';
    card.innerHTML = `<h2><label><input type="checkbox" data-report-select disabled> ${E(pageLabel)} · ${E(item.title || '未命名图表')}</label></h2><p role="status" data-report-flags hidden></p>` +
      '<div class="charts-fields"><label class="charts-field">图型<select data-report-type><option value="trend">走势</option><option value="seasonal">同期对比（按年份分列）</option><option value="comparison">多指标对比</option></select></label></div>' +
      (item.series || []).map((series, index) => `<label class="charts-field">原文图例：${E(series.name)}${series.unit ? `（${E(series.unit)}）` : ''} → 指标<select data-report-mapping="${index}"><option value="">请选择指标</option>${(series.candidates || []).map(candidate => `<option value="${E(candidate.id)}">${E(candidate.name || candidate.metric || '未命名指标')}${candidate.unit ? ` · ${E(candidate.unit)}` : ''}${candidate.sheet ? ` · ${E(candidate.sheet)}` : ''}${candidate.source_file ? ` · ${E(candidate.source_file)}` : ''}</option>`).join('')}</select></label>` +
        (series.legends?.length ? `<p class="charts-muted">${item.legend_semantics === 'periods' ? '原文周期图例' : '原文图例'}：${E(series.legends.join('、'))}</p>` : '') +
        `<p class="charts-notice warning" data-report-missing="${index}" hidden>${missingMessage}</p>`).join('') +
      '<button type="button" class="charts-button" data-report-preview>预览</button><div class="charts-thumbnail" data-report-plot></div><p role="status" data-report-notice hidden></p>' +
      `<details><summary>原文图表依据</summary><p class="charts-muted">${E(Array.isArray(item.evidence) ? item.evidence.map(value => typeof value === 'string' ? value : value.text || value.quote || '').join('\n') : item.evidence)}</p></details>`;
    const missing = item.series?.some(series => !series.selected_id);
    const state = {item, element: card, valid: false, selected: false, version: 0, imported: false,
      duplicate: !!item.possible_duplicate, definitionFailed: !!item.definition_failed || !!item.errors?.length && !missing,
      needsReview: !!item.possible_duplicate || !!item.missing_indicators || missing || !!item.definition_failed || !!item.errors?.length}; states.set(item.id, state);
    const type = card.querySelector('[data-report-type]'); type.value = item.chart_type || '';
    card.querySelectorAll('[data-report-mapping]').forEach((select, index) => { select.value = item.series[index].selected_id || ''; });
    card.querySelector('[data-report-select]').addEventListener('change', event => { state.selected = event.target.checked; controls(); });
    card.querySelectorAll('select').forEach(select => select.addEventListener('change', () => preview(state)));
    card.querySelector('[data-report-preview]').addEventListener('click', () => preview(state));
    target.append(card); flags(state);
    U.notice(card.querySelector('[data-report-notice]'), noticeText([
      ...(item.errors || []).map(error=>reason(error,cannotDraw)), ...(item.warnings || []), ...(item.mapping_warnings || [])
    ]), 'warning');
    return state;
  }
  async function renderResult(data, own) {
    clearCards();
    if (data.group !== undefined) $('group').value = data.group;
    const groups = new Map(), queue = [];
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
        if (!failed) { queue.push(addCard(item, cards)); continue; }
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
    diagnostic({failures:data.failures, charts:(data.charts||[]).filter(item=>item.errors?.length).map(item=>({id:item.id,errors:item.errors}))});
    U.notice($('status'), queue.length ? `识别到 ${queue.length} 张图，请核对每条图例对应的指标和预览效果。` : '没有找到可导入的图。可重新识别，或在图表库手动新建。', queue.length ? '' : 'warning');
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
    diagnostic();
    U.notice($('result'), ''); U.notice($('failures'), ''); U.notice($('status'), '正在读取周报正文并识别图表…'); controls();
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
      for (const item of failed) {
        const failedIndex=selected.findIndex(state=>state.item.id===item.chart_id), original=selected[failedIndex]?.item;
        const title=item.title || original?.title || (original?.page != null ? `第 ${original.page} 页的图` : failedIndex>=0 ? `所选的第 ${failedIndex+1} 张图` : '未命名图表');
        const notice=document.createElement('p');
        notice.textContent=`${title}：${reason(item,cannotDraw)}`;
        $('result').append(notice);
      }
      diagnostic(failed.length ? failed : undefined);
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
