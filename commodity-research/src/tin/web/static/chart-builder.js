/* Researcher-selected chart definitions; directory text is only search evidence. */
(function () {
  'use strict';
  const dialog = document.getElementById('chart-builder');
  if (!dialog) return;
  const U = window.ChartUI, K = window.ChartKit, E = U.esc, $ = id => document.getElementById('builder-' + id);
  const config = JSON.parse((document.getElementById('indicator-cycle-config') || document.getElementById('chart-page-config')).textContent);
  const metadata = new Map();
  let curves = [], templates = [], defaults = null, defaultSource = '', defaultId = '', renderers = [], previewTimer, searchTimer;
  let previewToken = 0, searchToken = 0, openToken = 0, searchController, previewController, nextOffset = null, offset = 0;
  let favoritesOnly = false, savedSignature = '', saving = false, initializing = false;
  const rawIds = () => [...new Set(curves.flatMap(c => c.derived ? [c.derived.a, c.derived.b] : [c.series_id]))];
  const unit = spec => !spec?.unit || spec.unit === '—' ? '' : spec.unit.trim();
  const curveUnit = c => c.derived ? c.derived.op === 'div' ? '倍' : unit(metadata.get(c.derived.a)) || unit(metadata.get(c.derived.b)) : unit(metadata.get(c.series_id));
  function cleanPreview() { renderers.forEach(r => r.destroy()); renderers = []; $('preview').replaceChildren(); $('statistics').hidden = true; }
  function status(text, kind = '') { U.notice($('status'), text, kind); }
  function remember(data) { for (const row of data.indicators || []) metadata.set(row.series_id, row); return data.indicators || []; }
  async function catalog(ids, search = '', pageOffset = 0, signal) {
    const query = new URLSearchParams({format: 'metadata', search, offset: pageOffset, limit: 40});
    ids?.forEach(id => query.append('series_id', id));
    return U.api('/indicators/catalog?' + query, {signal});
  }
  function addIndicator(spec) {
    if (curves.some(c => c.series_id === spec.series_id)) return;
    if (new Set([...rawIds(), spec.series_id]).size > 16 || curves.length >= 16) { status('最多选择 16 个指标或曲线。', 'warning'); return; }
    metadata.set(spec.series_id, spec);
    curves.push({series_id: spec.series_id, label: (spec.name || spec.series_id).slice(0, 120), kind: 'line', axis: 'left', style: {}, curve_cycles: 'current'});
    if (!$('title').value) $('title').value = spec.name?.slice(0, 120) || '';
    renderCurves(); void updateDefault(); schedule();
  }
  async function search(pageOffset = 0) {
    const own = ++searchToken; searchController?.abort(); searchController = new AbortController();
    $('prev').disabled = true; $('next').disabled = true;
    U.notice($('catalog-status'), '正在读取指标目录…');
    try {
      let ids;
      if (favoritesOnly) {
        await window.AccountFavorites.ready();
        ids = window.AccountFavorites.items.filter(i => i.kind === 'indicator' && i.status === 'active').map(i => i.id);
      }
      const data = ids?.length === 0 ? {indicators: [], total: 0, offset: 0, next_offset: null} :
        await catalog(ids, $('search').value.trim(), pageOffset, searchController.signal);
      if (own !== searchToken || !dialog.open) return;
      const items = remember(data); offset = pageOffset; nextOffset = data.next_offset;
      $('catalog-count').textContent = `${items.length ? offset + 1 : 0}–${offset + items.length} / ${data.total ?? items.length}`;
      $('catalog-items').replaceChildren();
      for (const item of items) {
        const row = document.createElement('label'); row.className = 'builder-catalog-item';
        const box = document.createElement('input'); box.type = 'checkbox'; box.dataset.builderIndicator = item.series_id;
        box.checked = curves.some(c => c.series_id === item.series_id);
        const text = document.createElement('span'); text.textContent = `${item.name} · ${unit(item) || '单位未注明'}`;
        box.addEventListener('change', () => {
          if (box.checked) addIndicator(item);
          else { curves = curves.filter(c => c.series_id !== item.series_id); renderCurves(); void updateDefault(); schedule(); }
          box.checked = curves.some(c => c.series_id === item.series_id);
        }); row.append(box, text); $('catalog-items').append(row);
      }
      U.notice($('catalog-status'), items.length ? '' : favoritesOnly ? '自选中没有匹配的有效指标。' : '没有匹配指标。');
      $('prev').disabled = !offset; $('next').disabled = nextOffset == null;
    } catch (error) { if (own === searchToken && error.name !== 'AbortError') U.notice($('catalog-status'), error.message, 'error'); }
  }
  function renderCurves() {
    $('curve-count').textContent = `${curves.length}/16`;
    $('curves').innerHTML = curves.map((c, i) => `<div class="builder-curve" data-builder-curve="${i}"><label class="charts-field">图例名称<input data-builder-label maxlength="120" value="${E(c.label)}"></label><label class="charts-field">${$('mode').value === 'trend' ? '图型' : '周期显示'}${$('mode').value === 'trend' ? '<select data-builder-kind><option value="line">线</option><option value="bar">柱</option><option value="area">面积</option></select>' : '<select data-builder-cycle><option value="current">当前期</option><option value="current_and_previous">当前期 + 上一期（虚线）</option></select>'}</label><span class="charts-muted">${E(curveUnit(c) || '单位未注明')}${c.derived ? '<br>计算序列' : ''}</span><button type="button" data-builder-remove class="charts-button">移除</button></div>`).join('');
    $('curves').querySelectorAll('[data-builder-curve]').forEach((row, i) => {
      const cycle = row.querySelector('[data-builder-cycle]'), kind = row.querySelector('[data-builder-kind]');
      if (cycle) {
        cycle.value = curves[i].curve_cycles || 'current'; cycle.disabled = $('mode').value !== 'cycle' || curves.length < 2;
        cycle.addEventListener('change', e => { curves[i].curve_cycles = e.target.value; schedule(); });
      }
      if (kind) { kind.value = curves[i].kind; kind.addEventListener('change', e => { curves[i].kind = e.target.value; schedule(); }); }
      row.querySelector('[data-builder-label]').addEventListener('input', e => { curves[i].label = e.target.value; schedule(); });
      row.querySelector('[data-builder-remove]').addEventListener('click', () => { curves.splice(i, 1); renderCurves(); void updateDefault(); schedule(); });
    });
    $('catalog-items').querySelectorAll('[data-builder-indicator]').forEach(box => { box.checked = curves.some(c => c.series_id === box.dataset.builderIndicator); });
    $('add-derived').disabled = $('mode').value === 'batch' || rawIds().length < 2 || curves.length >= 16;
  }
  function templateNote() {
    const trend = $('mode').value === 'trend';
    $('custom-field').hidden = $('template').value !== '__custom';
    $('cycle-start').disabled = trend || $('custom-field').hidden;
    $('cycle-fields').hidden = trend;
    $('cycles').disabled = trend; $('template').disabled = trend;
    $('title').required = $('mode').value !== 'batch'; $('group').required = $('mode').value === 'batch';
    $('template-note').textContent = $('mode').value === 'trend' ? '按真实日期展示最近最多 2000 个观测；计算序列按双方共同日期计算。' :
      $('template').value === '__auto' ? $('mode').value === 'batch' ? '每张图分别采用其工作表已导入周期图中最多的明确模板起点；没有时使用自然年。' :
        `默认：${defaults?.name || '自然年'}${defaults?.start ? ' · 起点 ' + defaults.start : ''}（${defaultSource === 'native_sheet' ? '同工作表已导入周期图的明确元数据' : '自然年'}）。可自行改选模板。` : '所选曲线共用本模板；数值与统计只使用真实观测。';
  }
  async function updateDefault() {
    const id = rawIds()[0];
    if (id === defaultId) { templateNote(); return; }
    defaultId = id || ''; defaults = null; defaultSource = '';
    if (!id) { templateNote(); return; }
    const own = openToken;
    try {
      const data = await U.api('/charts/builder-options?' + new URLSearchParams({series_id: id}));
      if (own !== openToken || id !== defaultId) return;
      defaults = data.template; defaultSource = data.template_source;
      $('groups').innerHTML = (data.groups || []).map(group => `<option value="${E(group)}"></option>`).join('');
      templateNote(); schedule();
    } catch (error) { if (own === openToken && id === defaultId) status(error.message, 'error'); }
  }
  function selectedTemplate() {
    if ($('template').value === '__auto') {
      if ($('mode').value === 'batch') return undefined;
      if (rawIds().length && !defaults) throw new Error('正在核对工作表默认模板，请稍候。');
      return defaults || templates.find(t => t.builtin && t.template.kind === 'repeat' && t.template.start === '01-01')?.template;
    }
    if ($('template').value === '__custom') {
      const start = $('cycle-start').value.trim();
      if (!U.validMonthDay(start)) throw new Error('请填写有效的年度起点 MM-DD。');
      return {kind: 'repeat', name: '自定义年度周期', start, period: 'year', align: 'days', transform: 'raw'};
    }
    return templates.find(t => t.id === $('template').value)?.template;
  }
  function definition() {
    if (!curves.length) throw new Error('请先选择指标。');
    if (curves.length > 8 && $('mode').value === 'cycle') throw new Error('单张周期图最多 8 条曲线，请减少曲线或选择批量周期图。');
    const transform = $('transform').value, axisUnits = [];
    const members = curves.map(c => {
      const key = transform === 'rebase' ? '起点=100' : curveUnit(c);
      if (key && !axisUnits.includes(key)) axisUnits.push(key);
      return {...c, kind: $('mode').value === 'trend' ? c.kind : 'line', axis: axisUnits.indexOf(key) === 1 ? 'right' : 'left'};
    });
    if ($('mode').value !== 'batch' && axisUnits.length > 2) throw new Error('超过两种单位，请改用 change 或 rebase 变换；change 保留原单位，起点 = 100 可共用纵轴。不会自动换算单位。');
    if ($('mode').value === 'batch') {
      if (curves.some(c => c.derived)) throw new Error('批量图集只接受单指标；计算序列请选择走势或周期对比。');
      if (!$('group').value.trim()) throw new Error('请为图集选择或新建一个分组。');
      const template = selectedTemplate();
      return {series_ids: rawIds(), group: $('group').value.trim(), cycles: Number($('cycles').value), transform, ...(template ? {template} : {})};
    }
    if (!$('title').value.trim()) throw new Error('请填写图表标题。');
    const time = $('mode').value === 'trend' ? {mode: 'recent', points: 2000, transform} :
      {mode: 'cycle', template: selectedTemplate(), cycles: Number($('cycles').value), band_cycles: 5, transform, curve_cycles: 'current'};
    return {format: 'chart.v1', title: $('title').value.trim(), group: $('group').value.trim(), curves: members, time, origin: {kind: 'manual', ref: ''}};
  }
  function schedule() {
    clearTimeout(previewTimer); previewController?.abort(); previewToken++; savedSignature = ''; $('save').disabled = true; $('saved').replaceChildren();
    templateNote();
    if (!dialog.open || initializing || saving) return;
    previewTimer = setTimeout(preview, 350);
  }
  async function preview() {
    const own = ++previewToken, batch = $('mode').value === 'batch';
    let body;
    try { body = definition(); } catch (error) { cleanPreview(); status(error.message); return; }
    const signature = JSON.stringify(body); previewController = new AbortController();
    status('正在生成预览…');
    try {
      const query = config.date ? '?date=' + encodeURIComponent(config.date) : '';
      const data = await U.api('/charts/' + (batch ? 'batch/preview' : 'preview') + query, {method: 'POST', body: signature, signal: previewController.signal});
      if (own !== previewToken || !dialog.open) return;
      cleanPreview(); $('preview').classList.toggle('batch', batch);
      if (batch) {
        for (const item of data.items) {
          const card = document.createElement('article'); card.className = 'charts-card';
          const heading = document.createElement('h3'); heading.textContent = item.definition.title;
          const plot = document.createElement('div'); card.append(heading, plot); $('preview').append(card);
          renderers.push(K.render(plot, item.payload, {definition: item.definition, thumbnail: true, width: 640, height: 240}));
        }
      } else {
        renderers.push(K.render($('preview'), data, {definition: body, title: body.title, height: 420}));
        U.stats($('statistics'), data);
      }
      U.notices($('status'), [...(data.notices || []), ...(data.warnings || [])]);
      if (!config.can_write) status(config.write_reason || '当前账号没有研究工作台编辑权限，可预览。');
      savedSignature = signature; $('save').disabled = !config.can_write;
    } catch (error) { if (own === previewToken && error.name !== 'AbortError') { cleanPreview(); status(error.message, 'error'); } }
  }
  async function open(ids = [], favoriteShortcut = false) {
    if (dialog.open) return;
    const own = ++openToken; initializing = true; saving = false; $('controls').disabled = false; curves = []; metadata.clear(); defaults = null; defaultId = ''; savedSignature = '';
    $('form').reset(); $('saved').replaceChildren(); $('derived').hidden = true; $('save').disabled = true; cleanPreview();
    favoritesOnly = favoriteShortcut; dialog.showModal(); renderCurves(); status('正在读取图表设置…');
    try {
      ids = [...new Set(ids)];
      if (ids.length > 16) throw new Error('最多选择 16 个指标，请减少选择后重新打开作图。');
      const results = await Promise.all([U.api('/cycle-templates'), U.api('/charts/builder-options' + (ids[0] ? '?' + new URLSearchParams({series_id: ids[0]}) : '')),
        ids.length ? catalog(ids) : Promise.resolve({indicators: []})]);
      if (own !== openToken || !dialog.open) return;
      templates = results[0].items; const options = results[1];
      defaults = options.template; defaultSource = options.template_source; defaultId = ids[0] || '';
      $('groups').innerHTML = (options.groups || []).map(group => `<option value="${E(group)}"></option>`).join('');
      $('template').innerHTML = '<option value="__auto">按工作表导入图表的默认模板</option>' + templates.map(t => `<option value="${E(t.id)}">${E(t.template.name)}</option>`).join('') + '<option value="__custom">自定义年度起点</option>';
      remember(results[2]);
      ids.forEach(id => { if (metadata.has(id)) addIndicator(metadata.get(id)); });
      if (ids.some(id => !metadata.has(id))) status('部分所选指标已不在当前来源目录，请重新选择。', 'warning');
      else status('');
    } catch (error) { if (own === openToken) status(error.message, 'error'); }
    finally { if (own === openToken && dialog.open) { initializing = false; renderCurves(); void search(); schedule(); } }
  }
  function derivedUnitNotice() {
    const missing = $('derived-op').value === 'sub' && (!unit(metadata.get($('derived-a').value)) || !unit(metadata.get($('derived-b').value)));
    $('unit-confirm-field').hidden = !missing; $('unit-confirm').checked = false;
    $('derived-unit').textContent = $('derived-op').value === 'div' ? '结果单位：倍' : `A 单位：${unit(metadata.get($('derived-a').value)) || '未注明'}；B 单位：${unit(metadata.get($('derived-b').value)) || '未注明'}。相减要求单位相同。`;
  }
  $('add-derived').addEventListener('click', () => {
    const ids = rawIds();
    for (const name of ['derived-a', 'derived-b']) $('' + name).innerHTML = ids.map(id => `<option value="${E(id)}">${E(metadata.get(id)?.name || id)}</option>`).join('');
    $('derived-b').selectedIndex = 1; $('derived-label').value = ''; $('derived').hidden = false; derivedUnitNotice();
  });
  ['derived-a', 'derived-b', 'derived-op'].forEach(name => $(name).addEventListener('change', derivedUnitNotice));
  $('derived-cancel').addEventListener('click', () => { $('derived').hidden = true; });
  $('derived-apply').addEventListener('click', () => {
    const a = $('derived-a').value, b = $('derived-b').value, op = $('derived-op').value, label = $('derived-label').value.trim();
    if (!a || !b || !label) { status('请选择两个指标并填写计算序列名称。', 'warning'); return; }
    if (curves.length >= 16) { status('最多 16 条曲线。', 'warning'); return; }
    const ua = unit(metadata.get(a)), ub = unit(metadata.get(b));
    if (op === 'sub' && ua && ub && ua !== ub) { status('相减要求两者单位相同；不会换算单位。', 'error'); return; }
    if (op === 'sub' && (!ua || !ub) && !$('unit-confirm').checked) { status('单位缺失，请先核对并勾选确认。', 'warning'); return; }
    curves.push({derived: {op, a, b, ...(op === 'sub' && (!ua || !ub) ? {unit_confirmed: true} : {})}, label, kind: 'line', axis: 'left', style: {}, curve_cycles: 'current'});
    $('derived').hidden = true; renderCurves(); schedule();
  });
  $('mode').addEventListener('change', () => { $('derived').hidden = true; renderCurves(); schedule(); });
  ['transform', 'template', 'cycle-start', 'cycles', 'title', 'group'].forEach(name => $(name).addEventListener('input', schedule));
  $('search').addEventListener('input', () => { clearTimeout(searchTimer); searchController?.abort(); searchToken++; searchTimer = setTimeout(() => search(), 250); });
  $('favorites').addEventListener('click', () => { favoritesOnly = true; void search(); });
  $('catalog').addEventListener('click', () => { favoritesOnly = false; void search(); });
  $('prev').addEventListener('click', () => search(Math.max(0, offset - 40))); $('next').addEventListener('click', () => { if (nextOffset != null) void search(nextOffset); });
  $('form').addEventListener('submit', async event => {
    event.preventDefault(); if (saving || !savedSignature || !config.can_write) return;
    let body; try { body = definition(); } catch (error) { status(error.message, 'error'); return; }
    if (JSON.stringify(body) !== savedSignature) { schedule(); return; }
    const own = openToken, batch = $('mode').value === 'batch', favorite = $('save-favorite').checked;
    saving = true; $('controls').disabled = true; $('save').disabled = true; status('正在保存图表…');
    try {
      const result = await U.api('/charts' + (batch ? '/batch' : ''), {method: 'POST', body: JSON.stringify(body)});
      const items = result.created || result.items || [result];
      if (own === openToken && dialog.open) {
        status(`已保存 ${items.length} 张图表到图表库。`); $('saved').replaceChildren();
        for (const item of items) {
          const link = document.createElement('a'); link.className = 'charts-button'; link.textContent = item.definition.title;
          link.href = U.pageURL('/sn/charts/' + encodeURIComponent(item.id)); $('saved').append(link);
        }
        savedSignature = '';
      }
      document.dispatchEvent(new CustomEvent('chart:saved', {detail: {items}}));
      if (favorite) {
        try {
          const client = window.AccountFavorites; await client.ready();
          const next = [...client.items];
          items.forEach(item => { if (!next.some(i => i.kind === 'chart' && i.id === item.id)) next.push({kind: 'chart', id: item.id}); });
          await client.replace(next);
          if (own === openToken && dialog.open) status(`已保存 ${items.length} 张图表并加入自选。`);
        } catch (error) { if (own === openToken && dialog.open) status('图表已保存；加入自选失败：' + error.message, 'warning'); }
      }
    } catch (error) { if (own === openToken && dialog.open) { status(error.message, 'error'); $('save').disabled = false; } }
    finally { if (own === openToken) { saving = false; $('controls').disabled = false; } }
  });
  document.getElementById('chart-builder-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { openToken++; previewToken++; searchToken++; clearTimeout(previewTimer); clearTimeout(searchTimer); previewController?.abort(); searchController?.abort(); cleanPreview(); });
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-chart-builder]'); if (!button) return;
    if (button.dataset.chartBuilder === 'single') void open([button.dataset.builderSeries]);
    else if (button.dataset.chartBuilder === 'favorites') void open([...document.querySelectorAll('[data-builder-favorite]:checked')].map(box => box.dataset.builderFavorite), true);
    else void open();
  });
  window.ChartBuilder = Object.freeze({open});
})();
