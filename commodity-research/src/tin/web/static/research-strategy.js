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
  const citationFields = {thesis: '适用范围与目标', watch_for: '执行前核对', trigger: '适用 / 触发',
    response_action: '应对措施', invalidation: '暂停 / 撤回', original_text: '原始研究文字'};
  const citationKinds = {indicator: '指标', judgment_condition: '研判条件', knowledge_file: '知识库原件'};
  const cache = new Map(), drafts = new Map(), metadata = new Map();
  for (const item of research.items || []) metadata.set('research:' + item.id, item);
  for (const item of state.plans || []) metadata.set(item.key, item);
  if (research.selected) cache.set('research:' + research.selected.id, research.selected);
  if (state.initial_plan) cache.set('plan:' + state.initial_plan.id, state.initial_plan);
  let selected = state.selected || null, activeEditor = null, selectSequence = 0, editSequence = 0;
  let selectedSources = new Set(), sourcesPage = 0, loadingSources = false, fileSequence = 0;
  let sourceNames = new Map();
  let cardSource = null, cardReference = null, judgmentReference = null, planSource = null, saving = false;
  let citations = [], citationContext = null, citationKind = 'indicator', citationRows = [], citationChoice = null;
  let citationNext = null, citationTimer = null, citationListToken = 0, citationPreviewToken = 0;
  let citationListController = null, citationPreviewController = null, citationImportController = null;
  let citationParent = null, citationParents = [], citationParentNext = 0, citationParentsLoaded = false;
  let citationParentController = null, citationParentToken = 0;
  const citationDialog = byId('strategy-citation-dialog');
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
  function definition(parent, entries, references = [], selectionKey) {
    const list = element('dl', undefined, 'research-preview-fields');
    for (const [name, value, slot] of entries) {
      const row = element('div'), content = element('dd', value || '尚未填写');
      if (slot) content.append(citationCards(references, slot, false, selectionKey));
      row.append(element('dt', name), content); list.append(row);
    }
    parent.append(list);
  }
  function disclosure(parent, title, text, references = [], slot, selectionKey) {
    if (!text && !references.some(item => item.field === slot)) return;
    const details = element('details'), summary = element('summary', title);
    details.append(summary, element('pre', text || '')); if (slot) details.append(citationCards(references, slot, false, selectionKey)); parent.append(details);
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
    if (citations.length) card.citations = structuredClone(citations);
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
    closeCitations();
    editSequence += 1; activeEditor = null;
    if (editor) editor.hidden = true;
    if (planEditor) planEditor.hidden = true;
    host.hidden = false;
  }
  function renderCard(item, key) {
    const article = element('article', undefined, 'strategy-preview-article');
    const header = element('header', undefined, 'strategy-preview-header'), title = element('div');
    const card = item.card;
    const stamp = localDay(item.updated_at || item.created_at);
    title.append(element('div', (item.direction_label || '研究记录') + ' · ' +
      (card?.response_action?.trim() ? '应对策略草稿' : '观察方案 · 待补应对') + (stamp ? ' · 更新于 ' + stamp : ''), 'strategy-card-top'), element('h2', item.title));
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
        line.append(element('span', '依据研判 · ' + (item.judgment_label || '所引版本已不可读，请重新选择')),
          button('预览依据', () => previewReference(ref, line, key)));
        article.append(line);
      }
      definition(article, [['适用 / 触发', card.trigger, 'trigger'], ['应对措施', card.response_action || '待补应对措施；当前仅作为观察方案保留。', 'response_action'],
        ['暂停 / 撤回', card.invalidation, 'invalidation'], ['复核日期', card.review_date]], card.citations || [], key);
      disclosure(article, '适用范围与目标', card.thesis, card.citations || [], 'thesis', key);
      disclosure(article, '执行前核对', card.watch_for, card.citations || [], 'watch_for', key);
      disclosure(article, '原始文字', card.original_text, card.citations || [], 'original_text', key);
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
    if (card && (card.citations || []).some(row => row.kind === 'indicator' || row.kind === 'judgment_condition'))
      loadMonitor(item, article.querySelector('.research-preview-fields'));
  }
  function localDay(value) {
    const stamp = value ? new Date(value) : null;
    if (!stamp || Number.isNaN(stamp.getTime())) return '';
    return [stamp.getFullYear(), String(stamp.getMonth() + 1).padStart(2, '0'), String(stamp.getDate()).padStart(2, '0')].join('-');
  }
  function formatNumber(value) {
    return typeof value === 'number' && Number.isFinite(value) ? value.toLocaleString('zh-CN', {maximumFractionDigits: 4}) : '—';
  }
  function knownUnit(unit) { return unit && unit !== '未注明' ? unit : ''; }
  function sparkline(chart, boundary) {
    const ns = 'http://www.w3.org/2000/svg', svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 240 48'); svg.setAttribute('class', 'strategy-monitor-spark'); svg.setAttribute('aria-hidden', 'true');
    for (const segment of chart?.segments || []) {
      const line = document.createElementNS(ns, 'polyline'); line.setAttribute('points', segment); svg.append(line);
    }
    const last = (chart?.points || []).at(-1);
    if (last) { const dot = document.createElementNS(ns, 'circle'); dot.setAttribute('cx', last.x); dot.setAttribute('cy', last.y); dot.setAttribute('r', '2.5'); svg.append(dot); }
    return svg;
  }
  const monitorStates = {'触发': '已触发', '未触发': '未触发', '数据缺失': '数据缺失', '无法核对': '无法核对'};
  function monitorItem(row, kind) {
    const node = element('article', undefined, 'strategy-monitor-item'), top = element('div', undefined, 'strategy-monitor-top');
    node.dataset.state = kind === 'condition' ? row.state : 'indicator';
    top.append(element('span', kind === 'condition' ? monitorStates[row.state] || row.state : '指标', 'strategy-monitor-state'),
      element('small', [row.field_label, kind === 'condition' ? (row.rule_kind === 'falsifier' ? '研判证伪条件' : '研判阈值') : '引用指标'].filter(Boolean).join(' · ')));
    node.append(top, element('strong', row.title));
    const chart = row.chart || {};
    const unit = knownUnit(kind === 'condition' ? row.unit || chart.unit : chart.unit);
    const facts = kind === 'condition'
      ? [row.value !== undefined && row.value !== null ? '当前 ' + formatNumber(row.value) + (unit ? ' ' + unit : '') : '', row.condition ? '条件 ' + row.condition : '', row.as_of ? '数据 ' + row.as_of : '']
      : [chart.latest !== undefined && chart.latest !== null ? '最新 ' + formatNumber(chart.latest) + (unit ? ' ' + unit : '') : '暂无可用观测',
        typeof chart.change === 'number' ? '较前值 ' + (chart.change > 0 ? '+' : '') + formatNumber(chart.change) : '', chart.date ? '数据 ' + chart.date.slice(0, 10) : ''];
    node.append(element('p', facts.filter(Boolean).join(' · ')));
    if ((chart.segments || []).length) node.append(sparkline(chart));
    const fresh = chart.freshness;
    const notes = [row.note, fresh?.stale ? '最新数据距今 ' + fresh.age_days + ' 天，超过该指标通常的更新间隔，可能已停更' : '',
      row.outdated ? '所引研判已有新版本（v' + row.judgment.current_revision + '），这里仍按引用时的 v' + row.judgment.revision + ' 核对' : ''].filter(Boolean);
    if (fresh?.stale) node.dataset.stale = 'true';
    if (notes.length) node.append(element('small', notes.join('；'), 'strategy-small-note'));
    return node;
  }
  async function loadMonitor(item, anchor) {
    const own = selectSequence, panel = element('section', undefined, 'strategy-monitor');
    panel.setAttribute('aria-label', '条件监测'); panel.setAttribute('aria-live', 'polite');
    const head = element('header'); head.append(element('strong', '条件监测'), element('small', '正在核对所引条件与指标…'));
    panel.append(head); (anchor || host.firstChild)?.after(panel);
    try {
      const query = new URLSearchParams(); const day = new URL(location.href).searchParams.get('date'); if (day) query.set('date', day);
      const data = await request('/research-strategies/' + encodeURIComponent(item.id) + '/monitor' + (query.size ? '?' + query : ''));
      if (own !== selectSequence || !panel.isConnected) return;
      const counts = data.counts || {};
      head.replaceChildren(element('strong', '条件监测' + (counts.conditions ? ' · 所引条件 ' + counts.conditions + ' 项，' + (counts.triggered ? counts.triggered + ' 项已触发' : '暂无触发') : '')), element('small', data.notice || ''));
      const grid = element('div', undefined, 'strategy-monitor-grid');
      for (const row of data.conditions || []) grid.append(monitorItem(row, 'condition'));
      for (const row of data.indicators || []) grid.append(monitorItem(row, 'indicator'));
      panel.append(grid);
    } catch (error) {
      if (own !== selectSequence || !panel.isConnected) return;
      head.replaceChildren(element('strong', '条件监测'), element('small', error.message || '暂时无法核对所引条件；方案内容不受影响。'));
    }
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
    citations = structuredClone(body.card.citations || []);
    field('reference-note').hidden = !cardReference;
    field('reference-note').textContent = cardReference ? '引用已保存策略；独立副本不随原记录更新。' : '';
    field('judgment-preview').hidden = true; field('judgment-preview').replaceChildren();
    renderSelectedSources();
    renderCitationFields();
  }
  function citationIdentity(value) {
    if (!value || typeof value !== 'object' || Array.isArray(value) || !Object.hasOwn(citationFields, value.field))
      throw new Error('引用所属文字项无效，请重新选择。');
    const keys = {indicator: ['series_id', 'source_version', 'source_binding_id'],
      judgment_condition: ['id', 'revision', 'rule_kind', 'rule_id'], knowledge_file: ['source_binding_id', 'knowledge_id', 'file_version']}[value.kind];
    if (!keys || Object.keys(value).some(key => !['kind', 'field', 'snapshot', ...keys].includes(key)))
      throw new Error('引用格式无效，请从当前工作台重新选择。');
    const result = {kind: value.kind, field: value.field};
    for (const key of keys) {
      if (value.kind === 'indicator' && key === 'source_binding_id' && value[key] === undefined) continue;
      if (key === 'revision') {
        if (!Number.isSafeInteger(value[key]) || value[key] < 1) throw new Error('研判引用版本无效。');
      } else if (typeof value[key] !== 'string' || !value[key] || value[key].length > 128) throw new Error('引用信息不完整，请重新选择。');
      result[key] = value[key];
    }
    if (value.kind === 'judgment_condition' && !['threshold', 'falsifier'].includes(value.rule_kind)) throw new Error('请选择真实的阈值或证伪条件。');
    for (const key of ['source_version', 'file_version']) if (key in result && !/^[a-f0-9]{64}$/.test(result[key])) throw new Error('引用来源版本无效。');
    return result;
  }
  const citationKey = value => JSON.stringify(citationIdentity(value));
  function citationCards(items, slot, editing, selectionKey) {
    const list = element('div', undefined, 'strategy-citation-chips');
    for (const item of items.filter(value => value.field === slot)) {
      const chip = element('span', undefined, 'strategy-citation-chip');
      const title = item.snapshot?.title || citationKinds[item.kind] || '待核对引用';
      const open = button(title, () => openCitationPreview(item, editing ? null : selectionKey), 'strategy-citation-open');
      open.title = [title, item.snapshot?.subtitle].filter(Boolean).join(' · '); chip.append(open);
      if (editing) {
        const remove = button('×', () => { citations = citations.filter(value => citationKey(value) !== citationKey(item)); renderCitationFields(); dirty(); }, 'strategy-citation-remove');
        remove.setAttribute('aria-label', '移除引用：' + title); chip.append(remove);
      }
      list.append(chip);
    }
    return list;
  }
  function renderCitationFields() {
    document.querySelectorAll('[data-citation-list]').forEach(node => node.replaceChildren(citationCards(citations, node.dataset.citationList, true)));
  }
  function cancelCitationRequests() {
    clearTimeout(citationTimer); citationListToken++; citationPreviewToken++;
    citationListController?.abort(); citationPreviewController?.abort(); citationImportController?.abort();
    citationParentController?.abort(); citationParentToken++;
  }
  function closeCitations() {
    cancelCitationRequests(); citationContext = null; citationChoice = null;
    if (citationDialog?.open) citationDialog.close();
  }
  function currentCitationContext(context) {
    return Boolean(context) && citationContext === context && (context.mode === 'preview'
      ? context.selection === selectSequence && context.selected === selected
      : context.editing === editSequence && context.key === activeEditor && !editor.hidden);
  }
  function citationStatus(message, error = false) { status(message, error, byId('strategy-citation-status')); }
  function showCitationDialog(context) {
    closeCitations(); citationContext = context;
    citationParents = []; citationParentNext = 0; citationParentsLoaded = false;
    citationParent = context.parent || (judgmentReference ? {...judgmentReference} : null);
    byId('strategy-citation-title').textContent = context.mode === 'preview' ? '引用预览' : '引用 · ' + citationFields[context.field];
    byId('strategy-citation-browser').hidden = context.mode === 'preview';
    byId('strategy-citation-clear').hidden = context.mode === 'preview';
    byId('strategy-citation-clear').disabled = !citations.some(item => item.field === context.field);
    byId('strategy-citation-preview').replaceChildren(element('p', '选择一项查看内容。', 'strategy-small-note'));
    citationStatus(''); if (!citationDialog.open) citationDialog.showModal();
  }
  function openCitationPicker(slot, importOriginal = false) {
    if (saving || !activeEditor || !Object.hasOwn(citationFields, slot)) return;
    const context = {mode: importOriginal ? 'import' : 'edit', field: slot, key: activeEditor, editing: editSequence};
    showCitationDialog(context); citationKind = importOriginal ? 'knowledge_file' : 'indicator';
    byId('strategy-citation-search').value = ''; loadCitationOptions();
  }
  function openCitationPreview(item, selectionKey) {
    const context = {mode: selectionKey ? 'preview' : 'edit', field: item.field, key: activeEditor,
      editing: editSequence, selection: selectSequence, selected,
      parent: item.kind === 'judgment_condition' ? {id: item.id, revision: item.revision, title: item.snapshot?.preview?.judgment_title} : null};
    showCitationDialog(context); if (!selectionKey) { citationKind = item.kind; byId('strategy-citation-search').value = ''; loadCitationOptions(); }
    previewCitation(item);
  }
  function drawCitationOptions() {
    const context = citationContext, list = byId('strategy-citation-options'); list.replaceChildren();
    for (const item of citationRows) {
      const choice = button('', () => { if (currentCitationContext(context)) previewCitation({...item.citation, field: context.field}); }, 'strategy-citation-option');
      choice.append(element('strong', item.title), element('small', item.subtitle || ''));
      choice.setAttribute('aria-pressed', String(citationChoice && citationKey(citationChoice.citation) === citationKey({...item.citation, field: citationContext.field})));
      list.append(choice);
    }
    if (!citationRows.length) list.append(element('p', citationNext === null ? '没有匹配条目。' : '本批暂无可选条目，可继续加载。', 'strategy-small-note'));
    byId('strategy-citation-more').hidden = citationNext === null;
    document.querySelectorAll('[data-citation-kind]').forEach(node => node.setAttribute('aria-selected', String(node.dataset.citationKind === citationKind)));
    byId('strategy-citation-parent').hidden = citationKind !== 'judgment_condition';
  }
  function renderCitationParents() {
    const selector = byId('strategy-citation-judgment'); selector.replaceChildren(element('option', '请选择研判版本')); selector.firstChild.value = '';
    const rows = citationParent && !citationParents.some(item => item.id === citationParent.id && item.revision === citationParent.revision)
      ? [citationParent, ...citationParents] : citationParents;
    for (const row of rows) {
      const title = row.title ? display.reference(row) : '当前依据研判';
      const option = element('option', title + ' · 第' + row.revision + '版'); option.value = row.id + ':' + row.revision;
      if (citationParent?.id === row.id && citationParent?.revision === row.revision) option.selected = true;
      selector.append(option);
    }
    byId('strategy-citation-more-judgments').hidden = citationParentNext === null;
  }
  async function loadCitationParents(append = false) {
    const context = citationContext; if (!context || citationKind !== 'judgment_condition') return;
    citationParentController?.abort(); citationParentController = new AbortController(); const token = ++citationParentToken;
    const offset = append ? citationParentNext : 0; if (offset === null) return;
    const more = byId('strategy-citation-more-judgments'); more.disabled = true; citationStatus('正在读取研判目录…'); renderCitationParents();
    try {
      const data = await request('/research-references?' + new URLSearchParams({kind: 'judgment', limit: '30', offset: String(offset)}), {signal: citationParentController.signal});
      if (!currentCitationContext(context) || token !== citationParentToken || citationKind !== 'judgment_condition') return;
      if (!Array.isArray(data.items) || data.items.length > 30 || !Number.isSafeInteger(data.total) || data.total < 0) throw new Error('研判目录暂时不完整，请重试。');
      const rows = data.items.filter(item => item.kind === 'judgment' && typeof item.id === 'string' && Number.isSafeInteger(item.revision) && item.revision >= 1);
      if (rows.length !== data.items.length) throw new Error('研判版本信息不完整，请重试。');
      citationParents = append ? [...citationParents, ...rows.filter(item => !citationParents.some(row => row.id === item.id && row.revision === item.revision))] : rows;
      const next = offset + data.items.length;
      if (data.truncated && next <= offset) throw new Error('研判目录分页暂时不可用，请重新打开。');
      citationParentNext = data.truncated ? next : null; citationParentsLoaded = true;
      if (!citationParent && rows.length) citationParent = rows[0]; renderCitationParents();
      if (!append) await loadCitationOptions(); else citationStatus('');
    } catch (error) { if (error.name !== 'AbortError' && currentCitationContext(context) && token === citationParentToken) citationStatus(error.message, true); }
    finally { if (currentCitationContext(context) && token === citationParentToken) more.disabled = false; }
  }
  async function loadCitationOptions(append = false) {
    const context = citationContext; if (!context || context.mode === 'preview') return;
    byId('strategy-citation-parent').hidden = citationKind !== 'judgment_condition';
    clearTimeout(citationTimer); citationListController?.abort(); citationListController = new AbortController();
    const token = ++citationListToken, offset = append ? citationNext : 0;
    if (offset === null) return;
    if (!append) {
      citationRows = []; citationNext = null; citationChoice = null; citationPreviewToken++;
      citationPreviewController?.abort(); citationImportController?.abort();
      byId('strategy-citation-preview').replaceChildren(element('p', '选择一项查看内容。', 'strategy-small-note'));
      drawCitationOptions();
    }
    if (citationKind === 'judgment_condition' && !citationParentsLoaded) { await loadCitationParents(); return; }
    if (citationKind === 'judgment_condition' && !citationParent) { citationStatus('先选择研判版本，再查看具体条件。'); return; }
    byId('strategy-citation-more').disabled = true; citationStatus('正在搜索…');
    try {
      const query = new URLSearchParams({kind: citationKind, search: byId('strategy-citation-search').value.trim(), offset: String(offset), limit: '30'});
      if (citationKind === 'judgment_condition') { query.set('judgment_id', citationParent.id); query.set('revision', String(citationParent.revision)); }
      const data = await request('/research-strategies/citation-options?' + query, {signal: citationListController.signal});
      if (!currentCitationContext(context) || token !== citationListToken) return;
      if (!Array.isArray(data.items) || data.items.length > 30 || typeof data.has_more !== 'boolean') throw new Error('引用目录暂时不完整，请重试。');
      const rows = data.items.map(item => ({...item, citation: citationIdentity({...item.citation, field: context.field})}));
      const next = data.has_more ? (data.next_offset ?? offset + (data.limit || 30)) : null;
      if (next !== null && (!Number.isSafeInteger(next) || next <= offset)) throw new Error('引用目录分页暂时不可用，请重新搜索。');
      citationRows = append ? [...citationRows, ...rows.filter(item => !citationRows.some(row => citationKey(row.citation) === citationKey(item.citation)))] : rows;
      citationNext = next; drawCitationOptions(); citationStatus('');
    } catch (error) {
      if (error.name !== 'AbortError' && currentCitationContext(context) && token === citationListToken) {
        drawCitationOptions(); citationStatus(error.message, true);
      }
    } finally { if (currentCitationContext(context) && token === citationListToken) byId('strategy-citation-more').disabled = false; }
  }
  function citationLink(url, title) {
    try {
      const resolved = new URL(url, location.href); if (resolved.origin !== location.origin || !/^\/(?:platform\/knowledge-bases\/|research\/|sn\/)/.test(resolved.pathname)) return null;
      if (resolved.pathname.startsWith('/sn/') && prefix) resolved.pathname = prefix + resolved.pathname;
      const link = element('a', title, 'strategy-detail-link'); link.href = resolved.href; link.target = '_blank'; link.rel = 'noopener'; return link;
    } catch (_) { return null; }
  }
  function drawCitationPreview(data, context) {
    const panel = byId('strategy-citation-preview'), preview = data.preview || {};
    panel.replaceChildren(element('h3', data.title), element('p', data.subtitle || '', 'strategy-small-note'));
    if (preview.type === 'condition') panel.append(element('p', preview.text, 'strategy-citation-condition'));
    if (preview.notice) panel.append(element('p', preview.notice, 'strategy-small-note'));
    const link = citationLink(preview.url || preview.detail_url || preview.origin_url, preview.type === 'file' ? '预览知识库原件 ↗' : preview.type === 'condition' ? '查看所引研判 ↗' : '查看指标详情 ↗');
    if (link) panel.append(link);
    if (preview.type === 'indicator') { const values = element('div', '正在读取所选指标…', 'strategy-small-note'); panel.append(values); loadCitationIndicator(data, context, values); }
    const controls = element('div', undefined, 'strategy-citation-actions');
    if (context.mode !== 'preview') {
      const used = citations.some(item => citationKey(item) === citationKey(data.citation));
      const add = button(used ? '已引用' : '引用此项', () => {
        if (!currentCitationContext(context) || saving) return;
        if (citations.length >= 32) { citationStatus('每份策略最多引用32项，请先移除不需要的引用。', true); return; }
        citations.push(structuredClone(data.citation)); renderCitationFields(); dirty();
        byId('strategy-citation-clear').disabled = false; add.disabled = true; add.textContent = '已引用'; citationStatus('已加入' + citationFields[context.field] + '。');
      }, 'button-primary'); add.disabled = used || preview.version_status === 'updated'; controls.append(add);
      if (preview.type === 'file' && preview.content_supported && preview.version_status !== 'updated') controls.append(button('导入正文', () => importCitationFile(data, context)));
      else if (preview.type === 'file' && preview.version_status === 'updated') panel.append(element('p', '原件已有更新，请在知识库原件中重新选择当前版本。', 'strategy-small-note'));
      else if (preview.type === 'file') panel.append(element('p', '此格式可预览和引用，暂不支持导入正文。', 'strategy-small-note'));
    }
    panel.append(controls);
  }
  async function previewCitation(item) {
    const context = citationContext; if (!context) return;
    citationPreviewController?.abort(); citationPreviewController = new AbortController(); const token = ++citationPreviewToken;
    const panel = byId('strategy-citation-preview'); panel.replaceChildren(element('p', '正在核对引用…', 'strategy-small-note')); citationStatus('');
    try {
      const data = await request('/research-strategies/citation-preview', {method: 'POST', headers: {'Content-Type': 'application/json'}, signal: citationPreviewController.signal,
        body: JSON.stringify({citation: citationIdentity(item)})});
      if (!currentCitationContext(context) || token !== citationPreviewToken) return;
      citationIdentity(data.citation); citationChoice = data; drawCitationOptions(); drawCitationPreview(data, context);
    } catch (error) {
      if (error.name !== 'AbortError' && currentCitationContext(context) && token === citationPreviewToken) {
        citationChoice = null; panel.replaceChildren(button('重试预览', () => previewCitation(item))); citationStatus(error.message, true);
      }
    }
  }
  async function loadCitationIndicator(data, context, target) {
    const token = citationPreviewToken;
    try {
      const query = new URLSearchParams({series_id: data.preview.series_id});
      const date = new URL(location.href).searchParams.get('date'); if (date) query.set('date', date);
      const result = await request('/indicators/previews?' + query, {signal: citationPreviewController.signal});
      if (!currentCitationContext(context) || token !== citationPreviewToken || !target.isConnected) return;
      const card = result.cards?.[0]; if (!card) throw new Error('该指标暂无可用观测。');
      target.replaceChildren(element('strong', card.status === 'ok' ? observationText(card) : '暂无可用观测'),
        element('p', card.as_of ? '观测日期 ' + card.as_of : card.note || ''));
      if (card.change) target.append(element('p', '较前值 ' + card.change));
    } catch (error) { if (error.name !== 'AbortError' && currentCitationContext(context) && token === citationPreviewToken) target.textContent = '观测暂不可读，指标引用仍可保留。'; }
  }
  function observationText(card) { return [card.value, card.unit].filter(value => value !== null && value !== undefined && value !== '').join(' '); }
  function parseImport(text, filename) {
    if (!/\.json$/i.test(filename)) {
      if (text.length > 24000) throw new Error('原文超过24,000字，请先摘录所需部分。');
      return {text};
    }
    const input = JSON.parse(text), original = input?.card || input, optional = new Set(['format', 'reference', 'judgment_reference', 'citations']);
    if (!original || typeof original !== 'object' || Array.isArray(original) || original.format !== 'research-strategy-card.v1'
        || Object.keys(original).some(key => !optional.has(key) && !Object.hasOwn(fields, key))
        || Object.keys(fields).some(key => original[key] !== undefined && original[key] !== null && typeof original[key] !== 'string')) throw new Error('该 JSON 不是策略方案格式，请选择策略导出的 JSON，或用 TXT/MD 导入原文。');
    const card = structuredClone(original);
    for (const name of ['reference', 'judgment_reference']) if (card[name] && (typeof card[name].id !== 'string' || !Number.isSafeInteger(card[name].revision) || card[name].revision < 1 || card[name].kind !== (name === 'reference' ? 'strategy' : 'judgment'))) throw new Error('引用版本格式无效。');
    if (card.citations !== undefined) {
      if (!Array.isArray(card.citations) || card.citations.length > 32) throw new Error('每份策略最多引用32项。');
      card.citations = card.citations.map(citationIdentity);
    }
    if (input.source_ids !== undefined && (!Array.isArray(input.source_ids) || input.source_ids.length > 100 || input.source_ids.some(id => typeof id !== 'string'))) throw new Error('关联资料格式无效。');
    return {body: {card, source_ids: input.source_ids || [...selectedSources], acknowledge_observation: input.acknowledge_observation === true}};
  }
  function applyImport(text, filename, originalCitation, content = {}) {
    const fragments = content.content_kind === 'parsed_fragments';
    const parsed = parseImport(text, fragments ? 'fragments.txt' : filename), body = parsed.body || readCardForm();
    if (!parsed.body) { body.card.original_text = parsed.text; if (!body.card.title) body.card.title = filename.replace(/\.(txt|md)$/i, '').slice(0, 120); }
    if (originalCitation) {
      const source = {...originalCitation, field: 'original_text'}, entries = body.card.citations || [];
      if (!entries.some(item => citationKey(item) === citationKey(source))) entries.push(source);
      if (entries.length > 32) throw new Error('导入后引用将超过32项，请先移除不需要的引用。'); body.card.citations = entries;
    }
    const occupied = parsed.body ? Object.keys(citationFields).some(name => field(fields[name]).value.trim()) || field('title').value.trim() || citations.length : field('original').value.trim();
    const notice = fragments ? content.notice || '知识库已有解析片段，不等同原件全文。' : '';
    const confirmation = parsed.body ? '导入将替换当前策略表单及引用，未保存的内容会被覆盖。继续导入？' :
      occupied ? '导入将替换当前原始研究文字，其他策略文字保留。继续导入？' : '将这些片段载入原始研究文字，供继续编辑。是否导入？';
    if ((occupied || fragments) && !window.confirm([notice, confirmation].filter(Boolean).join('\n\n'))) return false;
    fillCard(body); loadJudgments(); field('sources').querySelectorAll('input').forEach(box => box.checked = selectedSources.has(box.value));
    field('original').closest('details').open = true; field('filename').textContent = filename + (fragments ? ' · 已解析文本片段' : '');
    const draft = drafts.get(activeEditor); if (draft) draft.importLabel = field('filename').textContent; dirty();
    status('原文已载入，可继续编辑后保存。', false, field('form-status')); return true;
  }
  async function importCitationFile(data, context) {
    citationImportController?.abort(); citationImportController = new AbortController(); const sequence = ++fileSequence;
    citationStatus('正在读取原件正文…');
    try {
      const item = citationIdentity(data.citation), query = new URLSearchParams({source_binding_id: item.source_binding_id, knowledge_id: item.knowledge_id, file_version: item.file_version});
      const result = await request('/research-strategies/citation-file-content?' + query, {signal: citationImportController.signal});
      if (!currentCitationContext(context) || sequence !== fileSequence) return;
      if (result.knowledge_id !== item.knowledge_id || result.file_version !== item.file_version) throw new Error('原件版本与所选引用不一致，请重新选择。');
      if (!result.supported || typeof result.text !== 'string') throw new Error(result.notice || '此格式暂不支持导入正文，请预览原件。');
      if (result.content_kind === 'parsed_fragments') citationStatus(result.notice || '知识库已有解析片段，不等同原件全文。');
      if (applyImport(result.text, result.filename || data.preview.file_name, data.citation, result)) closeCitations();
      else citationStatus('已取消导入，原有输入保留。');
    } catch (error) { if (error.name !== 'AbortError' && currentCitationContext(context) && sequence === fileSequence) citationStatus(error.message + ' 输入内容已保留。', true); }
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
    fillCard(body); field('file').value = ''; field('filename').textContent = draft?.importLabel || ''; field('sources').replaceChildren();
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
      // A copy must be distinguishable from its original in the list.
      card.title = (String(card.title || '').slice(0, 114) + '（副本）').slice(0, 120);
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
    closeCitations(); saving = true; const unlock = lock(form); status('正在保存…', false, field('form-status'));
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
        applyImport(text, file.name);
      } catch (error) { if (own === editSequence) status(error.message, true, field('form-status')); }
    });
  }
  if (citationDialog) {
    document.querySelectorAll('[data-citation-field]').forEach(node => node.addEventListener('click', () => openCitationPicker(node.dataset.citationField)));
    document.querySelectorAll('[data-citation-kind]').forEach(node => node.addEventListener('click', () => {
      citationKind = node.dataset.citationKind; byId('strategy-citation-search').value = ''; loadCitationOptions();
    }));
    document.querySelectorAll('[data-citation-close]').forEach(node => node.addEventListener('click', closeCitations));
    citationDialog.addEventListener('close', closeCitations);
    byId('strategy-citation-search').addEventListener('input', () => {
      clearTimeout(citationTimer); citationListToken++; citationListController?.abort();
      citationPreviewToken++; citationPreviewController?.abort(); citationImportController?.abort(); citationChoice = null;
      citationRows = []; citationNext = null; drawCitationOptions();
      byId('strategy-citation-preview').replaceChildren(element('p', '选择一项查看内容。', 'strategy-small-note'));
      citationTimer = setTimeout(() => loadCitationOptions(), 250);
    });
    byId('strategy-citation-more').addEventListener('click', () => loadCitationOptions(true));
    byId('strategy-citation-more-judgments').addEventListener('click', () => loadCitationParents(true));
    byId('strategy-citation-judgment').addEventListener('change', event => {
      citationParent = citationParents.find(row => row.id + ':' + row.revision === event.target.value) || null;
      byId('strategy-citation-search').value = ''; loadCitationOptions();
    });
    byId('strategy-citation-clear').addEventListener('click', () => {
      const context = citationContext; if (!context || !currentCitationContext(context) || context.mode === 'preview') return;
      citations = citations.filter(item => item.field !== context.field); renderCitationFields(); dirty(); closeCitations();
    });
    field('library-import')?.addEventListener('click', () => openCitationPicker('original_text', true));
    field('export')?.addEventListener('click', () => {
      const body = readCardForm(); delete body.revision;
      const url = URL.createObjectURL(new Blob([JSON.stringify(body, null, 2)], {type: 'application/json;charset=utf-8'}));
      const link = document.createElement('a'); link.href = url; link.download = (body.card.title || '策略方案').replace(/[\\/:*?"<>|]/g, '_') + '.json';
      link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
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
