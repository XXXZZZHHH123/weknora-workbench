/* Researcher-owned dimension configuration; observations and calculations come from the API. */
document.addEventListener('DOMContentLoaded', () => {
  'use strict';
  const bootstrap = document.getElementById('dimensions-page');
  if (!bootstrap) return;
  const initial = JSON.parse(bootstrap.textContent), get = id => document.getElementById('dimensions-' + id);
  const prefix = window.RESEARCH_PREFIX || '', key = item => item.kind + ':' + item.id;
  let config, current, draft, editing = false, saving = false, deleted = new Set(), generation = 0;
  let pickerTarget, pickerNext = null, pickerToken = 0, pickerTimer, drag, modelMode = 'suggest', running = false;
  let suggestions = [], inferred = [], selectedSources = new Map(), sourceNext = null, sourceToken = 0, sourceTimer;
  const renderers = new Set(), modelDialog = get('model-dialog'), picker = get('picker');
  const clone = value => JSON.parse(JSON.stringify(value));
  const itemName = item => item.name || item.title || item.id;
  const warningText = value => ({history_limited:'历史观测已达到读取上限，同期统计仅覆盖已读取窗口。',observation_window_limited:'观测已达到读取上限，统计仅覆盖已读取窗口。',baseline_unavailable:'缺少可比的往期观测。',insufficient_cycles:'有效同期样本不足，暂不提供排名。',empty_segments_omitted:'没有观测的历史周期已省略。'}[value] || '部分数据存在读取或覆盖提示，请核对条目。');
  function node(tag, css, text) { const el = document.createElement(tag); if (css) el.className = css; if (text !== undefined) el.textContent = text; return el; }
  function button(label, action, disabled = false) { const el = node('button', '', label); el.type = 'button'; el.disabled = disabled; el.addEventListener('click', action); return el; }
  function status(message, error = false) { get('status').textContent = message || ''; get('status').classList.toggle('error', error); }
  async function request(path, body, method = 'GET') {
    const response = await window.researchFetch(prefix + '/api/sn' + path, body === undefined ? {} : {method, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const result = await response.json();
    if (!response.ok) {
      const detail = result.detail, error = new Error(typeof detail === 'string' ? detail : detail?.message || '研究维度暂时不可用，请重试。');
      error.status = response.status; error.detail = detail; throw error;
    }
    return result;
  }
  function query() { const q = new URLSearchParams({period: get('period').value}); if (get('date').value) q.set('date', get('date').value); return q; }
  function freshId() { const bytes = new Uint8Array(4); crypto.getRandomValues(bytes); return 's-' + [...bytes].map(v => v.toString(16).padStart(2, '0')).join(''); }
  const editable = () => config?.can_write === true;
  const dims = () => editing ? draft : config.dimensions;
  function clearRenderers() { renderers.forEach(value => value.destroy?.()); renderers.clear(); window.ChartHover?.close(); }
  function allItems() { return (draft || config?.dimensions || []).flatMap(d => (d.sections || []).flatMap(s => s.items || [])); }
  function countItems() { return allItems().length; }
  function serialized() {
    return draft.map(d => ({...(d.id ? {id: d.id} : {}),label: d.label,category: d.category,description: d.description || '',
      sections: (d.sections || []).map(s => ({id: s.id, label: s.label,items: (s.items || []).map(i => ({kind: i.kind,id: i.id,...(i.kind === 'chart' ? {revision: i.revision} : {})}))}))}));
  }
  async function load() {
    const own = ++generation; status('正在读取研究维度…');
    try {
      if (initial.config && initial.data) {
        config = initial.config; current = initial.data;
        get('period').value = current.period || 'daily';
        if (!get('date').value) get('date').value = current.latest_data_date || current.as_of || '';
        get('date').disabled = true; get('period').disabled = true;
        render(); status('受限视角 · 仅显示已授权最新观测。'); return;
      }
      const loaded = await request('/dimensions');
      if (own !== generation) return;
      config = loaded; get('edit').hidden = !editable(); get('suggest').hidden = !editable();
      render(); await loadData(own);
    } catch (error) { if (own === generation) status(error.message, true); }
  }
  async function loadData(own = ++generation) {
    status('正在计算本期变化…');
    try {
      const data = await request('/dimensions/data?' + query());
      if (own !== generation) return;
      if (data.revision !== undefined && config.revision !== data.revision) throw new Error('研究维度配置已更新，请刷新页面后重试。');
      current = data;
      if (!get('date').value) get('date').value = data.latest_data_date || data.as_of || '';
      if (!editing) render();
      status([...new Set((data.warnings || []).map(warningText))].join('；'));
    } catch (error) { if (own === generation) status(error.message, true); }
  }
  function startEdit() {
    if (!editable() || editing) return;
    draft = clone(config.dimensions); deleted = new Set(); editing = true; render();
  }
  function evidence(d) {
    const refs = d.references || {}, support = refs.support ?? refs.supporting ?? 0, oppose = refs.oppose ?? refs.opposing ?? 0;
    return {support, oppose, total: refs.total ?? support + oppose};
  }
  function render() {
    if (!config) return;
    clearRenderers(); const root = get('sections'); root.replaceChildren();
    get('edit-actions').hidden = !editing; get('edit').hidden = !editable() || editing;
    get('save').disabled = saving; get('cancel').disabled = saving;
    get('date').disabled = editing || initial.restricted; get('period').disabled = editing || initial.restricted;
    const dataById = new Map((current?.dimensions || []).map(d => [d.id, d]));
    for (const [index, dim] of dims().entries()) {
      const section = node('section', 'dimensions-section'); section.dataset.dimensionId = dim.id || ''; section.dataset.dimensionIndex = index;
      if (editing) renderEditor(section, dim, index);
      else {
        const observed = dataById.get(dim.id), heading = node('header', 'dimensions-heading'), title = node('div');
        title.append(node('h2', '', dim.label));
        const summary = observed?.summary;
        title.append(node('p', 'dimensions-summary', summary ? `↑${summary.up} ↓${summary.down} · 持平 ${summary.flat} · 同期极端 ${summary.extreme}` : '本期数据读取中…'));
        heading.append(title);
        if (config.evidence_available === false) heading.append(node('p','dimensions-muted','研判依据统计未授权'));
        else {
          const refs = evidence(dim), link = node('a', 'dimensions-evidence-link', `研判依据：支持 ${refs.support} / 反对 ${refs.oppose} / 未标注 ${config.evidence?.unassigned?.total ?? config.evidence?.unassigned ?? 0}`);
          link.href = prefix + '/sn/judgment?dimension=' + encodeURIComponent(dim.id); heading.append(link);
        }
        section.append(heading);
        if (dim.description) section.append(node('p', 'dimensions-muted dimensions-description', dim.description));
        const stages = observed?.sections || dim.sections || [];
        for (const stage of stages) {
          const row = node('section', 'dimensions-stage'); row.append(node('h3', '', stage.label)); const grid = node('div', 'dimensions-grid');
          for (const item of stage.items || []) grid.append(renderCard(item)); row.append(grid); section.append(row);
        }
        if (!(dim.sections || []).some(s => s.items?.length)) {
          const empty = node('div', 'dimensions-empty', '还没有绑定指标');
          if (editable()) empty.append(button('添加', () => { startEdit(); ensureStage(index); openPicker(index, 0); }), button('让模型建议', () => openModels('suggest')));
          section.append(empty);
        }
      }
      root.append(section);
    }
    get('empty').hidden = allItems().length > 0 || editing;
    const unassigned = config.evidence?.unassigned, removed = config.evidence?.deleted;
    get('evidence').textContent = config.evidence_available === false ? '' : [unassigned ? `未标注维度的研判依据 ${unassigned.total ?? unassigned} 条` : '', removed ? `已删除维度的研判依据 ${removed.total ?? removed} 条，请在研究判断中重新指派。` : ''].filter(Boolean).join('；');
    const failed = current?.failed || []; get('failed').hidden = !failed.length;
    get('failed').querySelector('ul').replaceChildren(...failed.map(item => node('li', '', `${item.name || item.title || item.id}：${item.reason}`)));
    if (saving) {
      root.querySelectorAll('input,textarea,select,button').forEach(control => control.disabled = true);
      get('add').disabled = true; get('infer').disabled = true; get('suggest').disabled = true;
    } else { get('add').disabled = false; get('infer').disabled = false; get('suggest').disabled = false; }
  }
  function renderCard(item) {
    const card = node(initial.restricted ? 'article' : 'a', 'dimensions-card'); if (!initial.restricted) card.href = prefix + (item.kind === 'chart' ? '/sn/charts/' : '/sn/indicators/') + encodeURIComponent(item.id) + (get('date').value ? '?date=' + get('date').value : '');
    card.dataset.itemId = item.id; card.append(node('h4', '', itemName(item)));
    if (item.status === 'unavailable' || item.status === 'missing' || item.reason) { card.append(node('p', 'dimensions-error', item.reason || item.notice || '条目暂不可用')); return card; }
    if (item.kind === 'chart') {
      const plot = node('div', 'dimensions-chart-plot'); card.append(plot);
      if (item.payload && window.ChartKit) {
        try { renderers.add(window.ChartKit.render(plot, item.payload, {definition: item.definition, thumbnail: true, width: 500, height: 155})); } catch (error) { plot.textContent = error.message; }
      } else plot.textContent = '图表数据读取中…';
      if (item.notice) card.append(node('p', 'dimensions-muted', item.notice));
      return card;
    }
    const display = item.display || {}, value = node('p', 'dimensions-value', display.latest ?? '—'); value.append(node('small', '', item.unit || ''));
    card.append(value, node('p', 'dimensions-muted', [item.date, item.frequency].filter(Boolean).join(' · ')));
    const baselineLabel = item.baseline?.label === '较上期' ? '较上期 ' + (item.baseline.date || item.previous_date || '').slice(5) : item.baseline?.label || ('较 ' + (item.previous_date || '').slice(5));
    const change = item.change === null || item.change === undefined ? '暂无可比观测' : `${baselineLabel} ${display.change ?? '—'} ${item.unit || ''}${display.change_pct ? '（' + display.change_pct + '）' : ''}`;
    card.append(node('p', 'dimensions-change ' + (item.direction || ''), change));
    card.append(node('p', 'dimensions-position', item.position?.text || item.position?.notice || '同期位置读取中…'));
    if (item.freshness?.stale) card.append(node('p', 'dimensions-stale', `观测已过旧，距截至日 ${item.freshness.age_days} 天`));
    if (item.source || item.source_file) { const source = node('p', 'dimensions-muted', item.source || item.source_file); source.title = source.textContent; card.append(source); }
    for (const warning of [...new Set((item.warnings || []).map(warningText))]) card.append(node('p','dimensions-muted',warning));
    if (item.points?.length) card.append(spark(item.points));
    if (item.status === 'ok' && !initial.restricted) window.ChartHover?.attach(card, {key: 'dimension:' + item.id + ':' + get('date').value,title: itemName(item),load: async () => ({title: itemName(item),data: await request('/indicators/' + encodeURIComponent(item.id) + '/cycle?' + new URLSearchParams({date: get('date').value})),definition: {title: itemName(item),time: {mode: 'cycle'}}})});
    return card;
  }
  function spark(points) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); svg.setAttribute('viewBox', '0 0 240 40'); svg.setAttribute('preserveAspectRatio', 'none'); svg.classList.add('dimensions-spark'); svg.setAttribute('aria-label', '最近 ' + points.length + ' 个观测点走势');
    const values = points.map(p => p.value), lo = Math.min(...values), hi = Math.max(...values), dates = points.map(p => Date.parse(p.date)), first = dates[0], span = dates.at(-1) - first;
    const line = document.createElementNS(svg.namespaceURI, 'polyline'); line.setAttribute('points', points.map((p, i) => `${span ? (dates[i] - first) / span * 236 + 2 : 120},${hi === lo ? 20 : 37 - (p.value - lo) / (hi - lo) * 34}`).join(' ')); line.setAttribute('fill', 'none'); line.setAttribute('stroke', 'currentColor'); line.setAttribute('stroke-width', '1.7'); svg.append(line); return svg;
  }
  function input(value, label, action, tag = 'input') {
    const el = node(tag); el.value = value || ''; el.setAttribute('aria-label', label); if (tag === 'input') el.type = 'text'; el.addEventListener('input', () => action(el.value)); return el;
  }
  function reorder(values, from, to) { if (to < 0 || to >= values.length) return; values.splice(to, 0, values.splice(from, 1)[0]); render(); }
  function renderEditor(root, dim, index) {
    const fields = node('div', 'dimensions-edit-fields'), label = input(dim.label, '维度名称', value => dim.label = value); label.dataset.field = 'label'; label.maxLength = 24;
    const category = node('select'); category.setAttribute('aria-label', '通用类别');
    for (const entry of config.categories || []) category.append(new Option(entry.label || entry, entry.id || entry.value || entry));
    category.value = dim.category; category.addEventListener('change', () => dim.category = category.value);
    fields.append(label, category, button('上移', () => reorder(draft, index, index - 1), index === 0), button('下移', () => reorder(draft, index, index + 1), index === draft.length - 1), button('删除维度', () => {
      const refs = evidence(dim);
      if (!window.confirm(refs.total ? `该维度仍被 ${refs.total} 条研判依据引用。删除后将显示“已删除维度”，需要重新指派。确认删除？` : `确认删除维度“${dim.label}”？`)) return;
      if (dim.id && refs.total) deleted.add(dim.id); draft.splice(index, 1); render();
    }, draft.length <= 1));
    const description = input(dim.description, '维度说明', value => dim.description = value, 'textarea'); description.maxLength = 200; description.placeholder = '写一句说明'; fields.append(description); root.append(fields);
    (dim.sections || []).forEach((stage, si) => {
      const row = node('section', 'dimensions-stage-editor'); row.dataset.stageIndex = si;
      const controls = node('div', 'dimensions-actions'); const name = input(stage.label, '环节名称', value => stage.label = value); name.maxLength = 40;
      controls.append(name, button('上移环节', () => reorder(dim.sections, si, si - 1), si === 0), button('下移环节', () => reorder(dim.sections, si, si + 1), si === dim.sections.length - 1), button('删除环节', () => {
        if (stage.items.length && !window.confirm(`删除“${stage.label}”及其中 ${stage.items.length} 个条目？`)) return;
        dim.sections.splice(si, 1); render();
      }), button('添加条目', () => openPicker(index, si), stage.items.length >= 12 || countItems() >= 96)); row.append(controls);
      const grid = node('div', 'dimensions-grid'); grid.dataset.dimensionIndex = index; grid.dataset.stageIndex = si;
      grid.addEventListener('dragover', event => { if (drag) { event.preventDefault(); grid.classList.add('dimensions-drop-active'); } });
      grid.addEventListener('dragleave', event => { if (!grid.contains(event.relatedTarget)) grid.classList.remove('dimensions-drop-active'); });
      grid.addEventListener('drop', event => { event.preventDefault(); grid.classList.remove('dimensions-drop-active'); if (!drag) return;
        const {di, si: fromStage, ii} = drag, source = draft[di].sections[fromStage].items;
        if (source !== stage.items && stage.items.length >= 12) return status('每个环节最多 12 个条目。', true);
        const targetCard = event.target.closest('[data-item-index]'); let target = targetCard ? Number(targetCard.dataset.itemIndex) : stage.items.length;
        if (source === stage.items && ii < target) target--;
        const [item] = source.splice(ii, 1); stage.items.splice(target, 0, item); drag = null; render();
      });
      stage.items.forEach((item, ii) => {
        const card = node('div', 'dimensions-card dimensions-edit-card'); card.draggable = true; card.dataset.itemIndex = ii; card.dataset.itemId = item.id;
        card.addEventListener('dragstart', event => { drag = {di: index,si,ii}; event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', key(item)); });
        card.addEventListener('dragend', () => { drag = null; document.querySelectorAll('.dimensions-drop-active').forEach(n => n.classList.remove('dimensions-drop-active')); });
        card.append(node('span', '', itemName(item) + (item.kind === 'chart' ? '（图表）' : '')));
        const actions = node('span'); actions.append(button('↑', () => reorder(stage.items, ii, ii - 1), ii === 0), button('↓', () => reorder(stage.items, ii, ii + 1), ii === stage.items.length - 1), button('移除', () => { stage.items.splice(ii, 1); render(); })); card.append(actions); grid.append(card);
      }); row.append(grid); root.append(row);
    });
    root.append(button('增加环节', () => { dim.sections ||= []; dim.sections.push({id: freshId(),label: '新环节',items: []}); render(); }, (dim.sections || []).length >= 8));
  }
  function ensureStage(di) { draft[di].sections ||= []; if (!draft[di].sections.length) draft[di].sections.push({id: freshId(),label: '重点跟踪',items: []}); }
  async function save() {
    if (saving || !editing) return;
    for (const old of config.dimensions) {
      const refs = evidence(old);
      if (old.id && !draft.some(d => d.id === old.id) && refs.total && !deleted.has(old.id)) {
        if (!window.confirm(`“${old.label}”仍被 ${refs.total} 条研判依据引用。保存后这些依据将显示“已删除维度”。确认删除？`)) return;
        deleted.add(old.id);
      }
    }
    saving = true; render(); status('正在保存…');
    try {
      config = await request('/dimensions', {revision: config.revision,dimensions: serialized(),confirm_delete: [...deleted]}, 'PUT');
      editing = false; draft = undefined; current = undefined; render(); status('已保存研究维度。'); await loadData();
    } catch (error) { status(error.status === 409 ? `${error.message} 若配置已被其他人更新，请刷新页面后再修改；当前编辑尚未保存。` : error.message, true); }
    finally { saving = false; render(); }
  }
  function openPicker(di, si) { pickerTarget = {di, si}; get('picker-search').value = ''; picker.showModal(); void loadChoices(); }
  async function loadChoices(append = false) {
    const own = ++pickerToken, kind = get('picker-kind').value, search = get('picker-search').value;
    const list = get('picker-list'); if (!append) list.replaceChildren(); get('picker-status').textContent = '正在读取已授权目录…'; get('picker-more').hidden = true;
    try {
      let data, rows;
      if (kind === 'indicator') {
        data = await window.ResearchCatalog.read({search,offset: append ? pickerNext : 0,date: get('date').value});
        rows = data.indicators.map(r => ({kind,id: r.series_id,name: r.name,unit: r.unit,frequency: r.frequency,source: r.source || r.source_file,sheet: r.sheet}));
      } else {
        data = await request('/charts?' + new URLSearchParams({q: search,offset: append ? pickerNext : 0,limit: 100}));
        rows = data.items.map(r => ({kind,id: r.id,revision: r.revision,name: r.definition.title,source: r.definition.group}));
      }
      if (own !== pickerToken) return; pickerNext = data.next_offset;
      for (const row of rows) {
        const choice = node('label', 'dimensions-choice'), check = node('input'); check.type = 'checkbox'; check.checked = allItems().some(i => key(i) === key(row)); check.disabled = check.checked;
        const body = node('span', 'dimensions-choice-body'); body.append(node('strong', '', row.name),node('small', '', [row.unit,row.frequency,row.source,row.sheet].filter(Boolean).join(' · ')));
        check.addEventListener('change', () => {
          const stage = draft[pickerTarget.di].sections[pickerTarget.si];
          if (allItems().some(i => key(i) === key(row))) return;
          if (stage.items.length >= 12 || countItems() >= 96) { check.checked = false; get('picker-status').textContent = '每环节最多12条，整份配置最多96条。'; return; }
          stage.items.push(row); check.disabled = true; render();
        }); choice.append(check,body); list.append(choice);
      }
      get('picker-more').hidden = pickerNext === null || pickerNext === undefined;
      get('picker-status').textContent = list.children.length ? `已显示 ${list.children.length} / ${data.total ?? list.children.length} 个条目；已绑定条目不可重复添加。` : '没有匹配的条目。';
    } catch (error) { if (own === pickerToken) get('picker-status').textContent = error.message; }
  }
  async function openModels(mode) {
    if (!editable()) return; modelMode = mode; suggestions = []; inferred = []; selectedSources = new Map(); sourceNext = null;
    get('model-title').textContent = mode === 'infer' ? '从往期周报归纳维度' : '让模型建议指标';
    get('source-controls').hidden = mode !== 'infer'; get('suggestions').replaceChildren(); get('model-status').textContent = '';
    get('model-adopt').textContent = mode === 'infer' ? '将这套维度作为编辑草稿' : '采用所选到编辑草稿'; modelDialog.showModal(); syncModel();
    try {
      if (!get('model').dataset.loaded) {
        const result = await request('/strategies/models'); get('model').replaceChildren();
        for (const m of result.models || []) get('model').append(new Option(m.name,m.id));
        if (!get('model').options.length) get('model').append(new Option('暂无可用模型',''));
        get('model').disabled = false; get('model').dataset.loaded = 'true';
      }
      if (mode === 'infer') { get('source-search').value = ''; await loadSources(); }
    } catch (error) { get('model-status').textContent = error.message; }
    syncModel();
  }
  function syncModel() {
    const chosen = [...get('suggestions').querySelectorAll('input[type="checkbox"]:checked')];
    const validTargets = chosen.every(check => check.closest('.dimensions-choice').querySelector('select')?.value !== '');
    get('model-run').disabled = running || !get('model').value || (modelMode === 'infer' && (selectedSources.size < 1 || selectedSources.size > 3));
    get('model-adopt').disabled = running || (modelMode === 'infer' ? !inferred.length : !chosen.length || !validTargets); get('model-close').disabled = running;
  }
  async function loadSources(append = false) {
    const own = ++sourceToken; if (!append) get('source-list').replaceChildren();
    try {
      const result = await request('/dimensions/sources?' + new URLSearchParams({search: get('source-search').value,offset: append ? sourceNext : 0,limit: 20}));
      if (own !== sourceToken) return; sourceNext = result.next_offset;
      for (const row of result.items || []) {
        const choice = node('label', 'dimensions-choice'), check = node('input'); check.type = 'checkbox'; const id = JSON.stringify(row.citation); check.checked = selectedSources.has(id);
        check.addEventListener('change', () => { if (check.checked) { if (selectedSources.size >= 3) { check.checked = false; get('model-status').textContent = '最多选择3份资料。'; return; } selectedSources.set(id,row.citation); } else selectedSources.delete(id); syncModel(); });
        choice.append(check,node('span', '', row.title)); get('source-list').append(choice);
      }
      get('source-more').hidden = sourceNext === null || sourceNext === undefined;
      if (!get('source-list').children.length) get('source-list').append(node('p','dimensions-muted','没有匹配的已授权原文。'));
    } catch (error) { get('model-status').textContent = error.message; } syncModel();
  }
  async function generate() {
    if (running || get('model-run').disabled) return; running = true; suggestions = []; inferred = []; get('suggestions').replaceChildren(); syncModel();
    get('model-status').textContent = modelMode === 'infer' ? '正在读取所选原文并归纳维度…' : '正在核对工作表与真实指标…';
    try {
      const result = await request('/dimensions/' + (modelMode === 'infer' ? 'infer' : 'suggestions'), {model_id: get('model').value,...(modelMode === 'infer' ? {sources: [...selectedSources.values()]} : {})}, 'POST');
      if (modelMode === 'infer') {
        inferred = result.dimensions || [];
        for (const d of inferred) {
          const category = config.categories.find(c => (c.id || c) === d.category);
          const card = node('div','dimensions-choice-body dimensions-choice'); const body = node('div'); body.append(node('strong','',d.label),node('small','',[category?.label || d.category,d.description].filter(Boolean).join(' · ')),node('small','',(d.sections || []).map(s => s.label).join(' / '))); card.append(body); get('suggestions').append(card);
        }
      } else {
        suggestions = result.items || [];
        const available = dims(), groups = new Map();
        for (const d of available) {
          const group = node('section','dimensions-suggestion-group'); group.append(node('h3','',d.label)); groups.set(d.id,group);
          if (suggestions.some(row => row.dimension === d.id)) get('suggestions').append(group);
        }
        const unresolved = node('section','dimensions-suggestion-group'); unresolved.append(node('h3','','待指定维度'));
        for (const [index, row] of suggestions.entries()) {
          const choice = node('label','dimensions-choice'), check = node('input'); check.type = 'checkbox'; check.checked = true; check.dataset.index = index;
          const body = node('span','dimensions-choice-body'); body.append(node('strong','',row.name || row.series_id),node('small','',row.reason));
          const target = node('select'); target.setAttribute('aria-label','建议指标的维度');
          target.append(new Option('请选择维度','')); available.forEach((d, i) => target.append(new Option(d.label,String(i))));
          const matched = available.findIndex(d => d.id === row.dimension); target.value = matched < 0 ? '' : String(matched); target.addEventListener('change',syncModel);
          const stage = input(row.section || '', '建议指标的环节', () => {}); stage.placeholder = '环节名（可改放）'; stage.maxLength = 40;
          body.append(target,stage); choice.append(check,body);
          const group = groups.get(row.dimension); (group || unresolved).append(choice); check.addEventListener('change', syncModel);
        }
        if (unresolved.querySelector('.dimensions-choice')) get('suggestions').append(unresolved);
      }
      get('model-status').textContent = result.notice || (modelMode === 'infer' ? '这是模型草稿。采用后可以增删改；只有点击页面“保存”才会覆盖配置。' : '勾选条目并核对环节；采用后还需要保存研究维度。');
    } catch (error) { get('model-status').textContent = error.message; }
    finally { running = false; syncModel(); }
  }
  function adopt() {
    if (running || get('model-adopt').disabled) return;
    if (modelMode === 'infer') {
      if (!window.confirm('将这套维度放入编辑草稿？当前未保存的编辑会被替换，现有配置要点击“保存”后才会覆盖。')) return;
      startEdit(); draft = clone(inferred); render(); modelDialog.close(); status('已放入编辑草稿，可增删改后保存。'); return;
    }
    const candidate = clone(editing ? draft : config.dimensions); let merged = 0;
    for (const check of get('suggestions').querySelectorAll('input[type="checkbox"]:checked')) {
      const row = suggestions[Number(check.dataset.index)], choice = check.closest('.dimensions-choice');
      const selectedDimension = choice.querySelector('select').value;
      if (selectedDimension === '' || !candidate[Number(selectedDimension)]) { get('model-status').textContent = '请为每个所选指标指定当前草稿中存在的维度。'; return; }
      if (candidate.flatMap(d => (d.sections || []).flatMap(s => s.items)).some(i => i.kind === 'indicator' && i.id === row.series_id)) continue;
      const dim = candidate[Number(selectedDimension)], label = choice.querySelector('input[type="text"]').value.trim() || '重点跟踪'; dim.sections ||= [];
      let stage = dim.sections.find(s => s.label === label);
      if (!stage) { if (dim.sections.length >= 8) { get('model-status').textContent = '某维度的环节已满，请改放到已有环节。'; return; } stage = {id: freshId(),label,items: []}; dim.sections.push(stage); }
      if (stage.items.length >= 12 || candidate.flatMap(d => (d.sections || []).flatMap(s => s.items)).length >= 96) { get('model-status').textContent = '采用后将超过每环节12条或总96条上限，请减少所选或调整环节。'; return; }
      stage.items.push({kind:'indicator',id:row.series_id,name:row.name,unit:row.unit,frequency:row.frequency}); merged++;
    }
    startEdit(); draft = candidate; render(); modelDialog.close(); status(`已合并 ${merged} 个指标到编辑草稿，请核对并保存。`);
  }
  get('edit').addEventListener('click', startEdit); get('save').addEventListener('click', save);
  get('cancel').addEventListener('click', () => { if (saving) return; editing = false; draft = undefined; deleted = new Set(); render(); status('已取消未保存的编辑。'); });
  get('add').addEventListener('click', () => { if (draft.length >= 12) return status('最多12个维度。',true); draft.push({label:'新维度',category:config.categories?.[0]?.id || config.categories?.[0] || '其他',description:'',sections:[]}); render(); });
  get('date').addEventListener('change', () => void loadData()); get('period').addEventListener('change', () => void loadData());
  get('picker-kind').addEventListener('change', () => void loadChoices()); get('picker-more').addEventListener('click', () => void loadChoices(true)); get('picker-close').addEventListener('click', () => picker.close());
  get('picker-search').addEventListener('input', () => { pickerToken++; clearTimeout(pickerTimer); pickerTimer = setTimeout(() => void loadChoices(),180); });
  get('suggest').addEventListener('click', () => void openModels('suggest')); get('infer').addEventListener('click', () => void openModels('infer'));
  get('model').addEventListener('change', syncModel); get('model-run').addEventListener('click', () => void generate()); get('model-adopt').addEventListener('click', adopt); get('model-close').addEventListener('click', () => modelDialog.close());
  get('source-search').addEventListener('input', () => { sourceToken++; clearTimeout(sourceTimer); sourceTimer = setTimeout(() => void loadSources(),180); }); get('source-more').addEventListener('click', () => void loadSources(true));
  modelDialog.addEventListener('cancel', event => { if (running) event.preventDefault(); });
  const params = new URLSearchParams(location.search); if (params.get('period') === 'daily') get('period').value = 'daily';
  if (params.get('date')) get('date').value = params.get('date'); else if (initial.date) get('date').value = initial.date;
  window.addEventListener('pagehide', clearRenderers); void load();
});
