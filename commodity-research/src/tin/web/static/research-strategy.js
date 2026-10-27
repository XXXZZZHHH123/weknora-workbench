(() => {
  'use strict';
  const dataNode = document.getElementById('strategy-workspace-data');
  if (!dataNode) return;
  const state = JSON.parse(dataNode.textContent), sourceNode = document.getElementById('research-card-data');
  const research = sourceNode ? JSON.parse(sourceNode.textContent) : {items: [], enabled: false};
  const prefix = window.RESEARCH_PREFIX || '', api = prefix + '/api/sn';
  const byId = id => document.getElementById(id), field = id => byId('research-card-' + id);
  const display = window.ResearchDisplay || {text: value => String(value ?? ''), indicator: () => '指标名称待核对', reference: row => row?.title || '已保存研究判断'};
  const form = field('form'), editor = field('dialog'), planEditor = byId('strategy-plan-editor');
  const fields = {title: 'title', direction: 'direction', thesis: 'thesis', watch_for: 'watch',
    trigger: 'trigger', response_action: 'response-action', invalidation: 'invalidation',
    review_date: 'review', original_text: 'original'};
  const cache = new Map(), drafts = new Map(), metadata = new Map();
  for (const item of research.items || []) metadata.set('research:' + item.id, item);
  for (const item of state.plans || []) metadata.set(item.key, item);
  if (research.selected) cache.set('research:' + research.selected.id, research.selected);
  if (state.initial_plan) cache.set('plan:' + state.initial_plan.id, state.initial_plan);
  let selected = state.selected || null, activeEditor = null, selectSequence = 0, editSequence = 0;
  let selectedSources = new Set(), sourcesPage = 0, loadingSources = false, fileSequence = 0;
  let sourceNames = new Map();
  let cardSource = null, cardReference = null, judgmentReference = null, planSource = null, saving = false;
  const host = document.createElement('div'); host.id = 'strategy-dynamic-preview';
  byId('strategy-preview-content').prepend(host);
  byId('research-card-panels')?.setAttribute('hidden', '');
  byId('strategy-preview-content').querySelectorAll(':scope > article, :scope > #strategy-workspace-empty').forEach(node => node.hidden = true);
  function element(tag, text, className) {
    const node = document.createElement(tag); if (text !== undefined) node.textContent = display.text(text);
    if (className) node.className = className; return node;
  }
  function button(text, action, className = 'button-muted') {
    const node = element('button', text, className); node.type = 'button'; node.addEventListener('click', action); return node;
  }
  function status(message, error = false, target = byId('strategy-workspace-status')) {
    if (!target) return;
    target.textContent = message; target.setAttribute('role', error ? 'alert' : 'status');
  }
  async function request(path, options) {
    const response = await researchFetch(api + path, options);
    let body; try { body = await response.json(); } catch (_) { throw new Error('服务返回异常，请重试；编辑内容仍保留。'); }
    if (!response.ok) {
      const error = new Error(typeof body.detail === 'string' ? body.detail : '操作未完成，请核对字段后重试。');
      error.status = response.status; throw error;
    }
    return body;
  }
  function definition(parent, entries) {
    const list = element('dl', undefined, 'research-preview-fields');
    for (const [name, value] of entries) {
      const row = element('div'); row.append(element('dt', name), element('dd', value || '尚未填写')); list.append(row);
    }
    parent.append(list);
  }
  function disclosure(parent, title, text) {
    if (!text) return;
    const details = element('details'), summary = element('summary', title);
    details.append(summary, element('pre', text)); parent.append(details);
  }
  function referenceText(data) {
    if (data.markdown) return data.markdown;
    const body = data.payload || {};
    if (body.format === 'research-text.v1') return body.text;
    const labels = [['thesis', '适用范围与目标'], ['watch_for', '执行前核对'], ['trigger', '适用 / 触发'],
      ['response_action', '应对措施'], ['entry_condition', '入场条件'], ['invalidation', '暂停 / 撤回'],
      ['review_date', '复核日期'], ['review_at', '复核时间'], ['original_text', '原始文字']];
    const parts = labels.filter(([key]) => body[key]).map(([key, label]) => label + '\n' + body[key]);
    for (const [key, label] of [['entry', '入场'], ['target', '目标'], ['stop', '止损']]) {
      if (body[key]) parts.push(label + '\n' + body[key].low + ' — ' + body[key].high + '\n' + body[key].reason);
    }
    return parts.join('\n\n') || data.body_text || '此记录没有正文。';
  }
  function editable(key) { return state.can_write && (metadata.get(key)?.scope_status || cache.get(key)?.scope_status) !== 'unassigned'; }
  function markDraft(key, dirty) {
    document.querySelectorAll('[data-strategy-select]').forEach(node => {
      if (node.dataset.strategySelect === key) {
        const mark = node.querySelector('[data-draft-mark]'); if (mark) mark.hidden = !dirty;
      }
    });
  }
  function readCardForm() {
    const card = {format: 'research-strategy-card.v1'};
    for (const [name, id] of Object.entries(fields)) card[name] = field(id).value;
    if (cardReference) card.reference = {...cardReference};
    if (judgmentReference) card.judgment_reference = {...judgmentReference};
    return {card, source_ids: [...selectedSources], acknowledge_observation: field('observation-only').checked,
      ...(cardSource ? {revision: cardSource.revision} : {})};
  }
  function readPlanForm() {
    const plan = structuredClone(planSource.plan);
    for (const name of ['title', 'thesis', 'invalidation']) plan[name] = byId('plan-edit-' + name).value;
    plan.entry_condition = byId('plan-edit-entry-condition').value;
    for (const name of ['entry', 'target', 'stop']) plan[name] = {low: Number(byId('plan-edit-' + name + '-low').value),
      high: Number(byId('plan-edit-' + name + '-high').value), reason: byId('plan-edit-' + name + '-reason').value};
    const review = byId('plan-edit-review').value;
    plan.review_at = review ? new Date(review).toISOString() : '';
    plan.risk_budget = Number(byId('plan-edit-risk').value);
    return {base_version: planSource.version, plan};
  }
  function stash() {
    if (!activeEditor || saving) return;
    const draft = drafts.get(activeEditor);
    if (!draft) return;
    try { draft.body = activeEditor.startsWith('plan:') ? readPlanForm() : readCardForm(); } catch (_) { /* Invalid date is retained by the live form until corrected. */ }
  }
  function dirty() {
    if (!activeEditor) return;
    const draft = drafts.get(activeEditor); if (draft) { draft.dirty = true; stash(); markDraft(activeEditor, true); }
  }
  function tempEntry(key, title) {
    if (metadata.has(key)) return;
    metadata.set(key, {scope_status: 'current', title: title || '未保存方案'});
    const entry = button('', () => select(key), 'strategy-entry'); entry.dataset.strategySelect = key;
    const mark = element('small', '未保存草稿'); mark.dataset.draftMark = '';
    entry.append(element('strong', title || '未保存方案'), mark);
    const list = byId('research-sidebar-list'); list?.querySelector('.strategy-sidebar-empty')?.remove(); list?.prepend(entry);
    refreshCounts();
  }
  function refreshCounts() {
    for (const id of ['research-sidebar-list', 'plan-sidebar-list']) {
      const list = byId(id), count = list?.previousElementSibling?.querySelector('span');
      if (count) count.textContent = list.querySelectorAll('[data-strategy-select]').length;
    }
  }
  function removeTemp(key) {
    if (!key.startsWith('new:')) return;
    metadata.delete(key);
    document.querySelectorAll('[data-strategy-select]').forEach(node => { if (node.dataset.strategySelect === key) node.remove(); });
    const list = byId('research-sidebar-list');
    if (list && !list.querySelector('[data-strategy-select]')) list.append(element('p', '暂无策略方案', 'strategy-sidebar-empty'));
    refreshCounts();
  }
  function lock(formNode) {
    const wasInert = formNode.inert;
    formNode.inert = true;
    const controls = [...formNode.querySelectorAll('input, textarea, select, button')].map(node => [node, node.disabled]);
    controls.forEach(([node]) => node.disabled = true);
    return () => { formNode.inert = wasInert; controls.forEach(([node, disabled]) => node.disabled = disabled); };
  }
  function highlight() {
    document.querySelectorAll('[data-strategy-select]').forEach(node => {
      const active = node.dataset.strategySelect === selected;
      node.classList.toggle('selected', active); node.setAttribute('aria-pressed', String(active));
    });
  }
  function hideEditors() {
    editSequence += 1; activeEditor = null;
    if (editor) editor.hidden = true;
    if (planEditor) planEditor.hidden = true;
    host.hidden = false;
  }
  function renderCard(item, key) {
    const article = element('article', undefined, 'strategy-preview-article');
    const header = element('header', undefined, 'strategy-preview-header'), title = element('div');
    const card = item.card;
    title.append(element('div', (item.direction_label || '研究记录') + ' · ' +
      (card?.response_action?.trim() ? '应对策略草稿' : '观察方案 · 待补应对') + (item.created_at ? ' · ' + item.created_at.slice(0, 10) : ''), 'strategy-card-top'), element('h2', item.title));
    const actions = element('div', undefined, 'strategy-preview-actions');
    if (item.can_edit !== false && editable(key) && card) actions.append(button(drafts.get(key)?.dirty ? '继续编辑草稿' : '编辑',
      () => openCard(key, drafts.get(key)?.source || item, drafts.get(key))));
    if (item.can_apply !== false && item.scope_status !== 'unassigned' && state.can_write && card)
      actions.append(button('引用为新草稿', () => applyCard(key, item)));
    header.append(title, actions); article.append(header);
    if (item.scope_status === 'unassigned') article.append(element('p', '归属待核对 · 历史原记录仅供只读预览。', 'strategy-small-note'));
    if (card) {
      if (card.judgment_reference) {
        const line = element('div', undefined, 'strategy-basis-line');
        const ref = card.judgment_reference;
        line.append(element('span', '依据研判 · 已保存记录'),
          button('预览依据', () => previewReference(ref, line, key)));
        article.append(line);
      }
      definition(article, [['适用 / 触发', card.trigger], ['应对措施', card.response_action || '待补应对措施；当前仅作为观察方案保留。'],
        ['暂停 / 撤回', card.invalidation], ['复核日期', card.review_date]]);
      disclosure(article, '适用范围与目标', card.thesis);
      disclosure(article, '执行前核对', card.watch_for);
      disclosure(article, '原始文字', card.original_text);
      if (card.reference) {
        const line = element('div', undefined, 'strategy-basis-line');
        line.append(element('span', '引用策略 · 独立副本'),
          button('预览原策略', () => previewReference(card.reference, line, key))); article.append(line);
      }
      const sources = item.source_snapshots || [];
      if (sources.length) disclosure(article, '关联资料（' + sources.length + '）', sources.map(row => row.title || row.file_name || '未命名关联资料').join('\n'));
    } else disclosure(article, '历史研究记录全文', item.legacy_body);
    article.append(element('small', item.evidence_notice || '', 'strategy-small-note'));
    host.replaceChildren(article);
  }
  function renderPlan(item, key) {
    const article = element('article', undefined, 'strategy-preview-article'), plan = item.plan;
    const header = element('header', undefined, 'strategy-preview-header'), title = element('div');
    const statusLabel = metadata.get(key)?.status_label || item.status;
    title.append(element('div', '交易计划 · ' + statusLabel, 'strategy-card-top'), element('h2', item.title));
    header.append(title);
    if (editable(key)) header.append(button(drafts.get(key)?.dirty ? '继续编辑草稿' : '编辑为独立草稿',
      () => openPlan(key, drafts.get(key)?.source || item, drafts.get(key))));
    article.append(header);
    if (metadata.get(key)?.scope_status === 'unassigned') article.append(element('p', '归属待核对 · 历史原记录仅供只读预览。', 'strategy-small-note'));
    definition(article, [['研究逻辑', plan.thesis], ['入场条件', plan.entry_condition], ['失效 / 撤回', plan.invalidation], ['复核时间', plan.review_at]]);
    const bands = element('div', undefined, 'strategy-plan-preview-bands');
    for (const [name, label] of [['entry', '入场'], ['target', '目标'], ['stop', '止损']]) {
      const group = element('div'); group.append(element('small', label), element('strong', plan[name].low + ' — ' + plan[name].high), element('p', plan[name].reason)); bands.append(group);
    }
    article.append(bands);
    definition(article, [['合约与方向', plan.legs.map(row => row.contract + ' · ' + (row.side === 'buy' ? '买入' : '卖出') + ' × ' + row.ratio).join('\n')],
      ['计划风险预算', String(plan.risk_budget)], ['替代解释', plan.alternative_explanation], ['市场预期', plan.market_expectation], ['催化与期限', plan.catalyst + ' · ' + plan.horizon_days + '天']]);
    disclosure(article, '证据与数据口径（' + plan.evidence.length + '）', plan.evidence.map(row =>
      [row.statement, row.series_id ? display.indicator(row.series_id) : null, row.value, row.source, row.as_of].filter(value => value !== undefined && value !== null).join(' · ')).join('\n\n'));
    const link = element('a', '完整计划与成交记录 ↗', 'strategy-detail-link'); link.href = prefix + '/sn/strategies/' + item.id; article.append(link);
    host.replaceChildren(article);
  }
  async function select(key, restoreDraft = true) {
    if (saving) { status('正在保存，请稍候。'); return; }
    stash(); hideEditors(); selected = key; highlight();
    const own = ++selectSequence; status('');
    if (restoreDraft && drafts.has(key)) {
      const draft = drafts.get(key);
      if (draft.type === 'plan') openPlan(key, draft.source, draft); else openCard(key, draft.source, draft);
      return;
    }
    if (!key) { host.replaceChildren(element('p', '从左侧选择策略，或新建策略方案。', 'strategy-workspace-empty')); return; }
    try {
      let item = cache.get(key);
      if (!item) {
        host.replaceChildren(element('div', '正在读取策略…', 'strategy-loading')); byId('strategy-preview').setAttribute('aria-busy', 'true');
        item = await request(key.startsWith('research:') ? '/research-strategies/' + encodeURIComponent(key.slice(9)) : '/strategies/' + key.slice(5));
        cache.set(key, item);
      }
      if (own !== selectSequence) return;
      key.startsWith('research:') ? renderCard(item, key) : renderPlan(item, key);
      const url = new URL(location.href); url.searchParams.set('selected', key); history.replaceState(null, '', url);
    } catch (error) {
      if (own !== selectSequence) return;
      status(error.message, true); host.replaceChildren(button('重试读取', () => select(key, false)));
    } finally { if (own === selectSequence) byId('strategy-preview').removeAttribute('aria-busy'); }
  }
  function fillCard(body) {
    for (const [name, id] of Object.entries(fields)) field(id).value = body.card[name] || (name === 'direction' ? 'observe' : '');
    selectedSources = new Set(body.source_ids || []); cardReference = body.card.reference || null;
    judgmentReference = body.card.judgment_reference || null; field('observation-only').checked = body.acknowledge_observation === true;
    field('reference-note').hidden = !cardReference;
    field('reference-note').textContent = cardReference ? '引用已保存策略；独立副本不随原记录更新。' : '';
    field('judgment-preview').hidden = true; field('judgment-preview').replaceChildren();
    renderSelectedSources();
  }
  function renderSelectedSources() {
    const container = field('selected-sources'); if (!container) return;
    container.replaceChildren();
    for (const id of selectedSources) {
      const chip = element('span');
      chip.append(element('span', sourceNames.get(id) || '已关联资料'), button('×', () => {
        selectedSources.delete(id); field('sources').querySelectorAll('input').forEach(box => { if (box.value === id) box.checked = false; });
        renderSelectedSources(); dirty();
      }));
      chip.lastChild.setAttribute('aria-label', '移除关联资料：' + (sourceNames.get(id) || '已关联资料'));
      container.append(chip);
    }
  }
  async function loadSources() {
    if (loadingSources || !editor || editor.hidden) return;
    loadingSources = true; const own = editSequence, page = sourcesPage + 1;
    field('more-sources').disabled = true;
    try {
      const data = await request('/research-strategies/sources?page=' + page);
      if (own !== editSequence || editor.hidden) return;
      for (const item of data.items || []) {
        sourceNames.set(item.id, item.file_name || item.title || '未命名关联资料');
        const label = element('label'), box = element('input'); box.type = 'checkbox'; box.value = item.id;
        box.checked = selectedSources.has(item.id);
        box.addEventListener('change', () => { box.checked ? selectedSources.add(item.id) : selectedSources.delete(item.id); renderSelectedSources(); dirty(); });
        label.append(box, element('span', item.file_name || item.title || '未命名关联资料')); field('sources').append(label);
      }
      renderSelectedSources();
      sourcesPage = page; field('more-sources').hidden = !data.has_more;
    } catch (error) { if (own === editSequence) status(error.message, true, field('form-status')); }
    finally { if (own === editSequence) { loadingSources = false; field('more-sources').disabled = false; } }
  }
  function showJudgmentOption(ref, title) {
    const option = element('option', title || '已引用研究判断'); option.value = ref.id + ':' + ref.revision;
    option.dataset.reference = JSON.stringify(ref); field('judgment').append(option); return option;
  }
  async function loadJudgments() {
    const own = editSequence, choice = field('judgment'); choice.replaceChildren(element('option', '不引用研判 · 临时观察方案'));
    choice.firstChild.value = '';
    if (judgmentReference) { showJudgmentOption(judgmentReference).selected = true; }
    field('preview-judgment').disabled = !judgmentReference;
    try {
      const result = await request('/research-references?kind=judgment&limit=100');
      if (own !== editSequence || editor.hidden) return;
      for (const item of result.items || []) {
        const ref = {kind: 'judgment', id: item.id, revision: item.revision};
        const existing = [...choice.options].find(option => option.value === item.id + ':' + item.revision);
        if (existing) existing.textContent = display.reference(item);
        else showJudgmentOption(ref, display.reference(item));
      }
    } catch (error) { if (own === editSequence) status('研判目录未读取：' + error.message + ' 当前编辑内容已保留。', true, field('form-status')); }
  }
  function openCard(key, item = null, draft = null) {
    if (saving) return;
    if (!form || !editable(key)) return;
    if (key.startsWith('new:')) tempEntry(key, draft?.body?.card?.title);
    stash(); hideEditors(); selected = key; activeEditor = key; ++selectSequence; highlight(); host.hidden = true; editor.hidden = false;
    cardSource = draft?.source ?? item;
    sourceNames = new Map((cardSource?.source_snapshots || []).map(row => [row.knowledge_id, row.title || row.file_name]));
    const body = draft?.body || {card: item?.card || {}, source_ids: item?.source_ids || []};
    fillCard(body); field('file').value = ''; field('filename').textContent = ''; field('sources').replaceChildren();
    field('thesis').closest('details').open = !field('thesis').value || !field('watch').value;
    sourcesPage = 0; loadingSources = false; field('more-sources').hidden = true; field('save').disabled = false;
    field('save-copy').hidden = true; field('judgment-preview').hidden = true; field('judgment-preview').replaceChildren();
    field('dialog-title').textContent = cardSource ? '编辑策略方案' : '新建策略方案';
    status('', false, field('form-status'));
    if (!draft) drafts.set(key, {type: 'card', source: item, body, dirty: false});
    if (draft?.dirty) markDraft(key, true);
    loadSources(); loadJudgments();
  }
  async function previewReference(ref, target, selectionKey) {
    const own = selectSequence, editing = editSequence;
    try {
      const data = await request('/research-references/' + ref.kind + '/' + encodeURIComponent(ref.id) + '?revision=' + ref.revision);
      if ((selectionKey && (own !== selectSequence || selected !== selectionKey)) || (!selectionKey && editing !== editSequence)) return;
      if (!selectionKey && (judgmentReference?.id !== ref.id || judgmentReference?.revision !== ref.revision)) return;
      target.querySelector('[data-reference-text]')?.remove();
      const details = element('details'); details.dataset.referenceText = ''; details.open = true;
      details.append(element('summary', display.reference(data, ref.kind === 'judgment' ? '研究判断' : '策略方案')), element('pre', referenceText(data)));
      target.append(details); target.hidden = false;
    } catch (error) { status(error.message, true, editor && !editor.hidden ? field('form-status') : byId('strategy-workspace-status')); }
  }
  async function applyCard(key, item) {
    const own = selectSequence; status('正在核对引用版本…');
    try {
      const result = await request('/research-references/strategy/' + encodeURIComponent(key) + '?revision=' + item.revision);
      if (own !== selectSequence || selected !== key) return;
      if (result.can_apply === false || result.resource_type !== 'research-card') throw new Error('该记录可预览，不能直接作为结构化方案应用。');
      const newKey = 'new:' + crypto.randomUUID(), card = structuredClone(result.payload);
      card.reference = {kind: 'strategy', id: result.id, revision: result.revision};
      const draft = {type: 'card', source: null, dirty: true, body: {card, source_ids: result.source_ids || [], acknowledge_observation: false}};
      drafts.set(newKey, draft); openCard(newKey, null, draft); status('');
    } catch (error) { if (own === selectSequence) status(error.message, true); }
  }
  function localDateTime(value) {
    const parsed = new Date(value); if (!Number.isFinite(parsed.getTime())) return '';
    return new Date(parsed.getTime() - parsed.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  }
  function openPlan(key, item, draft = null) {
    if (saving) return;
    if (!editable(key)) return;
    stash(); hideEditors(); ++selectSequence; selected = key; activeEditor = key; highlight(); host.hidden = true; planEditor.hidden = false;
    planSource = draft?.source || item; const plan = draft?.body?.plan || item.plan;
    for (const name of ['title', 'thesis', 'invalidation']) byId('plan-edit-' + name).value = plan[name] || '';
    byId('plan-edit-entry-condition').value = plan.entry_condition;
    for (const name of ['entry', 'target', 'stop']) for (const value of ['low', 'high', 'reason']) byId('plan-edit-' + name + '-' + value).value = plan[name][value];
    byId('plan-edit-review').value = localDateTime(plan.review_at); byId('plan-edit-risk').value = plan.risk_budget;
    byId('strategy-plan-save').disabled = false; status('', false, byId('strategy-plan-edit-status'));
    if (!draft) drafts.set(key, {type: 'plan', source: item, body: {base_version: item.version, plan}, dirty: false});
  }
  function updateEntry(key, item, type) {
    const isCard = type === 'card';
    const meta = isCard ? {id: item.id, title: item.title, status: item.status, revision: item.revision,
      scope_status: 'current', can_edit: true, can_apply: true, direction_label: item.direction_label, review_date: item.card?.review_date} :
      {id: item.id, title: item.title, status: item.status, version: item.version, scope_status: 'current', status_label: '草稿', kind_label: item.kind === 'calendar_spread' ? '跨期价差' : '方向策略'};
    metadata.set(key, meta); cache.set(key, {...item, scope_status: 'current', can_edit: true, can_apply: true});
    const list = byId(isCard ? 'research-sidebar-list' : 'plan-sidebar-list'); if (!list) return;
    list.querySelector('.strategy-sidebar-empty')?.remove();
    let entry = [...list.querySelectorAll('[data-strategy-select]')].find(node => node.dataset.strategySelect === key);
    if (!entry) { entry = button('', () => select(key), 'strategy-entry'); entry.dataset.strategySelect = key; list.prepend(entry); }
    const mark = element('small', '未保存草稿'); mark.dataset.draftMark = ''; mark.hidden = true;
    entry.replaceChildren(element('strong', item.title), element('span', isCard ? item.direction_label + ' · 草稿 · ' + (item.card?.review_date || '') : '草稿 · ' + meta.kind_label), mark);
    refreshCounts();
  }
  async function saveCard(copy = false) {
    if (saving) return;
    const key = activeEditor, own = editSequence, body = readCardForm();
    if (!body.card.response_action.trim() && !body.acknowledge_observation) {
      status('请填写应对措施，或勾选保存为观察方案。', true, field('form-status')); field('response-action').focus(); return;
    }
    if (copy) {
      delete body.revision;
      if (cardSource) body.card.reference = {kind: 'strategy', id: 'research:' + cardSource.id, revision: cardSource.revision};
    }
    saving = true; const unlock = lock(form); status('正在保存…', false, field('form-status'));
    try {
      const item = await request('/research-strategies' + (!copy && cardSource ? '/' + encodeURIComponent(cardSource.id) : ''),
        {method: !copy && cardSource ? 'PATCH' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
      drafts.delete(key); markDraft(key, false); removeTemp(key); const savedKey = 'research:' + item.id; updateEntry(savedKey, item, 'card');
      saving = false;
      if (own === editSequence && activeEditor === key) { activeEditor = null; hideEditors(); await select(savedKey, false); status('草稿已保存。'); }
      else status('草稿已保存：' + item.title);
    } catch (error) {
      if (error.status === 409) cache.delete(key);
      if (own === editSequence) { status(error.message + ' 输入内容已保留。', true, field('form-status')); field('save-copy').hidden = error.status !== 409; }
    } finally { saving = false; unlock(); }
  }
  if (form) {
    form.addEventListener('input', dirty); form.addEventListener('change', dirty);
    form.addEventListener('submit', event => { event.preventDefault(); saveCard(); });
    field('add')?.addEventListener('click', () => openCard('new:' + crypto.randomUUID()));
    field('more-sources').addEventListener('click', loadSources);
    field('save-copy').addEventListener('click', () => saveCard(true));
    field('judgment').addEventListener('change', event => {
      judgmentReference = event.target.selectedOptions[0]?.dataset.reference ? JSON.parse(event.target.selectedOptions[0].dataset.reference) : null;
      field('preview-judgment').disabled = !judgmentReference; field('judgment-preview').hidden = true; field('judgment-preview').replaceChildren(); dirty();
    });
    field('preview-judgment').addEventListener('click', () => judgmentReference && previewReference(judgmentReference, field('judgment-preview')));
    field('file').addEventListener('change', async () => {
      const file = field('file').files?.[0], own = editSequence, sequence = ++fileSequence; if (!file) return;
      try {
        if (!/\.(txt|md|json)$/i.test(file.name) || file.size > 96000) throw new Error('请选择不超过96 KB的 UTF-8 .txt、.md 或研究策略卡 .json 文件');
        const text = new TextDecoder('utf-8', {fatal: true}).decode(await file.arrayBuffer());
        if (own !== editSequence || sequence !== fileSequence || editor.hidden) return;
        if (/\.json$/i.test(file.name)) {
          const input = JSON.parse(text), card = input.card || input;
          const optional = new Set(['format', 'reference', 'judgment_reference']);
          if (!card || typeof card !== 'object' || Array.isArray(card) || card.format !== 'research-strategy-card.v1'
              || Object.keys(card).some(key => !optional.has(key) && !Object.hasOwn(fields, key))
              || Object.keys(fields).some(key => card[key] !== undefined && card[key] !== null && typeof card[key] !== 'string')) throw new Error('该 JSON 不是策略方案格式；可改用 TXT/MD 保留原文。');
          for (const name of ['reference', 'judgment_reference']) if (card[name] && (typeof card[name].id !== 'string' || !Number.isInteger(card[name].revision) || card[name].revision < 1 || card[name].kind !== (name === 'reference' ? 'strategy' : 'judgment'))) throw new Error('引用版本格式无效。');
          if (input.source_ids !== undefined && (!Array.isArray(input.source_ids) || input.source_ids.some(id => typeof id !== 'string'))) throw new Error('关联资料编号格式无效。');
          fillCard({card, source_ids: input.source_ids || [...selectedSources]}); loadJudgments();
          field('sources').querySelectorAll('input').forEach(box => box.checked = selectedSources.has(box.value));
        } else {
          if (text.length > 24000) throw new Error('原文超过24,000字，请先缩短。');
          field('original').value = text; if (!field('title').value) field('title').value = file.name.replace(/\.(txt|md)$/i, '').slice(0, 120);
          field('original').closest('details').open = true;
        }
        field('filename').textContent = file.name; dirty(); status('原文已载入，请补充适用条件、应对与撤回条件。', false, field('form-status'));
      } catch (error) { if (own === editSequence) status(error.message, true, field('form-status')); }
    });
  }
  byId('strategy-plan-form')?.addEventListener('input', dirty);
  byId('strategy-plan-form')?.addEventListener('submit', async event => {
    event.preventDefault(); if (saving) return;
    const key = activeEditor, own = editSequence; let body;
    try { body = readPlanForm(); } catch (_) { status('请填写有效的复核时间。', true, byId('strategy-plan-edit-status')); return; }
    saving = true; const unlock = lock(byId('strategy-plan-form')); status('正在保存独立草稿…', false, byId('strategy-plan-edit-status'));
    try {
      const item = await request('/strategies/' + planSource.id + '/draft', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
      drafts.delete(key); markDraft(key, false); const savedKey = 'plan:' + item.id; updateEntry(savedKey, item, 'plan');
      saving = false;
      if (own === editSequence) { activeEditor = null; hideEditors(); await select(savedKey, false); status('独立草稿已保存；原计划未改动。'); }
    } catch (error) { if (own === editSequence) status(error.message + ' 输入内容已保留。', true, byId('strategy-plan-edit-status')); }
    finally { saving = false; unlock(); }
  });
  document.querySelectorAll('[data-research-card-close], [data-plan-edit-close]').forEach(node => node.addEventListener('click', () => {
    if (saving) return;
    stash();
    const leaving = activeEditor, draft = drafts.get(leaving);
    if (leaving?.startsWith('new:') && !draft?.dirty) { drafts.delete(leaving); removeTemp(leaving); }
    activeEditor = null; hideEditors();
    select(selected && !selected.startsWith('new:') && metadata.has(selected) ? selected : state.selected, false);
  }));
  document.querySelectorAll('[data-strategy-select]').forEach(node => node.addEventListener('click', () => select(node.dataset.strategySelect)));
  byId('strategy-search')?.addEventListener('input', event => {
    const query = event.target.value.trim().toLocaleLowerCase(); let count = 0;
    document.querySelectorAll('[data-strategy-select]').forEach(node => { node.hidden = !node.textContent.toLocaleLowerCase().includes(query); if (!node.hidden) count++; });
    byId('strategy-search-empty').hidden = count > 0;
  });
  window.addEventListener('beforeunload', event => {
    if ([...drafts.values()].some(draft => draft.dirty)) { event.preventDefault(); event.returnValue = ''; }
  });
  select(selected);
})();
