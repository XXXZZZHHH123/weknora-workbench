(function () {
  const dialog = document.getElementById('judgment-manager');
  if (!dialog) return;
  const byId = name => document.getElementById('logic-' + name);
  const display = window.ResearchDisplay || {text: value => String(value ?? ''), reference: row => row?.title || (row?.created_at ? '研究判断 · ' + row.created_at.slice(0, 16).replace('T', ' ') : '已保存研究判断')};
  const input = byId('input'), status = byId('status');
  const endpoint = (window.RESEARCH_PREFIX || '') + '/api/sn/judgment';
  let managed, draft, displayed, busy = false, controller, panels = [], review = null, reviewEpoch = 0;
  const reviewEdits = new Map();
  const rememberedChoices = new Set();
  let reviewControls = [], searchSessions = [], selectedSection = '', reviewView = 'all';
  const selectedId = () => dialog.dataset.judgmentId || '';
  const managePath = () => '/manage' + (selectedId() ? '?judgment_id=' + encodeURIComponent(selectedId()) : '');
  function inform(message, error = false) {
    status.textContent = message || '';
    status.dataset.error = String(error);
    status.dataset.busy = String(busy);
  }
  function stopSearches() {
    reviewEpoch++;
    for (const session of searchSessions) { clearTimeout(session.timer); session.controller?.abort(); }
    searchSessions = [];
  }
  function invalidate() {
    stopSearches(); draft = null; review = null; reviewEdits.clear(); rememberedChoices.clear();
    byId('preview').hidden = true;
    if (byId('import')) byId('import').hidden = false;
  }
  function tab(name) {
    dialog.querySelectorAll('[data-logic-tab]').forEach(button => button.setAttribute('aria-selected', String(button.dataset.logicTab === name)));
    dialog.querySelectorAll('[data-logic-panel]').forEach(panel => { panel.hidden = panel.dataset.logicPanel !== name; });
  }
  function setBusy(value) {
    busy = value; status.dataset.busy = String(value); input.readOnly = value;
    for (const name of ['generate', 'apply', 'file', 'skeleton', 'structured', 'reuse', 'reuse-original', 'model', 'variety']) {
      byId(name).disabled = value || managed?.can_write === false;
    }
    for (const name of ['panel-name', 'panel-save', 'new-panel-title']) if (byId(name)) byId(name).disabled = value || managed?.can_write === false;
    for (const name of ['panel-switch', 'panel-open', 'panel-more']) if (byId(name)) byId(name).disabled = value;
    syncReviewControls();
    syncApply();
  }
  async function api(path, body, method = 'POST', signal = controller?.signal) {
    const response = await researchFetch(endpoint + path, body === undefined ? {signal} : {
      method, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body), signal
    });
    let value;
    try { value = await response.json(); } catch { throw new Error('研究逻辑服务暂时不可用，输入已保留。'); }
    if (!response.ok) { const detail = value.detail; const message = typeof detail === 'string' ? detail :
      detail?.message ? detail.message + (detail.issues?.length ? '：' + detail.issues.slice(0, 4).map(item => item.message).join('；') : '') : '请求未完成，输入已保留。';
      const error = new Error(message); error.status = response.status; error.fields = detail?.attention_fields; throw error; }
    return value;
  }
  function panelTitle() { const title = byId('new-panel-title')?.value.trim(); return title ? {panel_title: title} : {}; }
  function navigateToJudgment(identifier) {
    const url = new URL(location.href || endpoint.replace('/api/sn/judgment', '/sn/judgment'), location.origin);
    url.searchParams.set('judgment_id', String(identifier));
    if (typeof location.assign === 'function') location.assign(url.href); else location.href = url.href;
  }
  function syncPanelName() {
    const row = panels.find(item => String(item.judgment_id) === byId('panel-switch')?.value);
    if (byId('panel-name')) byId('panel-name').value = row?.title || '';
    if (byId('panel-save')) byId('panel-save').textContent = row?.named ? '保存名称' : '引用为面板';
  }
  async function loadPanels(append = false) {
    if (!byId('panel-switch')) return;
    const result = await api('/panels' + (append ? '?offset=' + panels.length : ''));
    const previous = byId('panel-switch').value;
    panels = append ? [...panels, ...result.items] : result.items;
    const selector = byId('panel-switch'); selector.replaceChildren();
    for (const row of panels) {
      const option = document.createElement('option'); option.value = String(row.judgment_id);
      option.textContent = `${display.reference(row)} · ${row.status || '草稿'}`; selector.append(option);
    }
    selector.value = previous || selectedId() || String(managed.current?.id || panels[0]?.judgment_id || '');
    byId('panel-range').textContent = `${panels.length}/${result.total} 个已保存研判`;
    byId('panel-more').hidden = !result.has_more;
    syncPanelName();
  }
  function text(id, value) { byId(id).textContent = display.text(value || ''); }
  function showVersion(row) {
    displayed = row;
    text('current-md', row?.markdown || '尚无研究逻辑。可在“导入与分析”添加原文。');
    text('original-source', row?.source_text || '尚无原始输入');
    text('current-state', row ? `${display.reference(row)} · ${row.status} · ${row.author}` : '尚无研究逻辑');
  }
  function summarize(value) {
    if (value === null || value === undefined || value === '') return '未设置';
    if (Array.isArray(value)) return value.length + ' 项研究内容';
    if (typeof value === 'object') return Object.keys(value).length + ' 项研究信息';
    return display.text(String(value));
  }
  function presentDraft(value, preserveEdits = false) {
    draft = value; byId('preview').hidden = false;
    text('preview-md', value.markdown); text('generated-diff', value.diff?.markdown);
    text('coverage', `${(value.bindings || []).length} 个已绑定指标`);
    const bindings = byId('bindings'); bindings.replaceChildren();
    for (const binding of value.bindings || []) {
      const chip = document.createElement('span'); chip.textContent = `${display.text(binding.name)} · ${binding.unit || '单位待确认，仅描述'}`; bindings.append(chip);
    }
    const gaps = byId('unresolved'); gaps.replaceChildren();
    for (const item of value.unresolved || []) {
      const line = document.createElement('li'); line.textContent = display.text(`${item.section}：${item.text} — ${item.reason}`); gaps.append(line);
    }
    const fields = byId('field-diff'); fields.replaceChildren();
    for (const change of value.diff?.fields || []) {
      const section = document.createElement('section'), title = document.createElement('strong'); title.textContent = change.label;
      const before = document.createElement('p'), after = document.createElement('p');
      before.textContent = '原：' + summarize(change.before);
      after.textContent = '新：' + summarize(change.after);
      section.append(title, before, after); fields.append(section);
    }
    if (!preserveEdits) reviewEdits.clear();
    else for (const [id, edit] of reviewEdits) if (!edit.dirty) reviewEdits.delete(id);
    review = value.review || null;
    if (!preserveEdits) reviewView = 'all';
    renderReview();
  }
  function element(tag, className, value) {
    const node = document.createElement(tag); if (className) node.className = className;
    if (value !== undefined) node.textContent = value;
    return node;
  }
  function pendingCount() { return review?.needs_attention_count ?? review?.pending_count ?? 0; }
  function hasReviewEdits() { return [...reviewEdits.values()].some(edit => edit.dirty); }
  function syncApply() {
    const unsynced = hasReviewEdits();
    byId('apply').disabled = busy || !draft || managed?.can_write === false || unsynced;
    const note = byId('review-save-note');
    if (note) note.textContent = !review ? '' : unsynced ? '修改尚未同步，请处理红框位置后再保存。' :
      pendingCount() ? `有 ${pendingCount()} 项需处理，也可先保存草稿；未通过校验的规则不会参与执行。` : '内容已解析，可以保存草稿；保存后不会自动生效。';
    if (byId('review-reuse')) byId('review-reuse').disabled = busy || !review || unsynced || !managed?.can_write;
    if (byId('review-edit')) byId('review-edit').disabled = busy;
  }
  function syncReviewControls() {
    for (const control of reviewControls) control.node.disabled = busy || !managed?.can_write || Boolean(control.disabled?.());
    for (const session of searchSessions) for (const option of session.list?.children || []) option.disabled = busy || !managed?.can_write;
  }
  function registerControl(node, item, name, disabled) {
    node.dataset.reviewItem = item.id; node.dataset.reviewControl = name;
    reviewControls.push({node, disabled}); return node;
  }
  function shortFile(value) { return String(value || '').split(/[\\/]/).pop(); }
  function bindingMeta(row) {
    return [row?.unit || '单位待核对', shortFile(row?.source_file), row?.sheet].filter(Boolean).join(' · ');
  }
  function changed(edit) { edit.dirty = true; edit.renderCondition?.(); edit.renderErrors?.(); syncReviewVisibility(); syncReviewControls(); syncApply(); }
  const ruleNumber = '[+-]?(?:[0-9]+(?:\\.[0-9]+)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?';
  const singleRuleNumber = new RegExp('^' + ruleNumber + '$');
  const rangeRuleNumbers = new RegExp('^\\[\\s*(' + ruleNumber + ')\\s*,\\s*(' + ruleNumber + ')\\s*\\]$');
  const comparisonExpression = new RegExp('^(value|change)\\s*(>=|<=|>|<)\\s*(' + ruleNumber + ')$');
  const comparisonWords = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于'};
  function ruleCondition(item, edit) {
    const rule = item.rule, unit = edit.selected?.unit ? ' ' + display.text(edit.selected.unit) : '（单位待确认）';
    const details = [rule.type, rule.horizon].filter(value => typeof value === 'string' && value).map(value => display.text(value));
    if (item.kind === 'thresholds') {
      const literal = typeof rule.value_literal === 'string' ? rule.value_literal.trim() : '';
      if (rule.operator === 'between') {
        const range = rangeRuleNumbers.exec(literal);
        if (range) return [`指标值不低于 ${range[1]}${unit}，且不高于 ${range[2]}${unit}（含上下界）`, ...details].join('；');
      } else if (Object.hasOwn(comparisonWords, rule.operator) && singleRuleNumber.test(literal)) {
        return [`指标值${comparisonWords[rule.operator]} ${literal}${unit}`, ...details].join('；');
      }
    } else if (item.kind === 'falsifiers') {
      const match = typeof rule.expression === 'string' ? comparisonExpression.exec(rule.expression.trim()) : null;
      if (match) {
        const periods = Number.isSafeInteger(rule.consecutive_periods) && rule.consecutive_periods > 0 ? `连续 ${rule.consecutive_periods} 期成立` : '连续期数待核对';
        const side = rule.side === 'bull' ? '多方原假设失效' : rule.side === 'bear' ? '空方原假设失效' : '原假设方向待核对';
        return [`${match[1] === 'change' ? '较上一数据点的变化值' : '指标值'}${comparisonWords[match[2]]} ${match[3]}${unit}`, periods, side, ...details].join('；');
      }
    }
    return ['需核对条件，请结合原文和下方提示确认。', ...details].join('；');
  }
  function selectSection(identifier) {
    selectedSection = identifier;
    syncReviewVisibility();
  }
  function needsAttention(item) { return item.status === 'pending' || item.requires_attention === true || reviewEdits.get(item.id)?.dirty === true; }
  function syncReviewVisibility() {
    if (!review?.sections || !byId('review-items')) return;
    const allItems = review.sections.flatMap(section => section.items || []), count = allItems.filter(needsAttention).length;
    if (!count) reviewView = 'all';
    byId('review-count').textContent = `已解析 ${review.total ?? allItems.length} 项 · 需处理 ${count} 项`;
    byId('review-attention')?.setAttribute('aria-pressed', String(reviewView === 'attention'));
    byId('review-all')?.setAttribute('aria-pressed', String(reviewView === 'all'));
    const visibleSections = review.sections.filter(section => reviewView === 'all' || section.items?.some(needsAttention));
    if (!visibleSections.some(section => section.id === selectedSection)) selectedSection = visibleSections[0]?.id || '';
    for (const node of byId('review-nav').children) {
      const section = review.sections.find(section => section.id === node.dataset.reviewSection);
      node.hidden = !visibleSections.includes(section); node.setAttribute('aria-current', String(section?.id === selectedSection));
    }
    for (const group of byId('review-items').children) {
      group.hidden = group.dataset.reviewSection !== selectedSection;
      for (const card of group.children) {
        if (!card.dataset.reviewItem) continue;
        const item = allItems.find(row => row.id === card.dataset.reviewItem), dirty = reviewEdits.get(item.id)?.dirty;
        card.hidden = reviewView === 'attention' && !needsAttention(item); card.dataset.reviewDirty = String(Boolean(dirty));
      }
    }
    if (byId('review-empty')) byId('review-empty').hidden = visibleSections.length > 0;
  }
  function renderBinding(item, edit) {
    const field = element('div', 'logic-binding-field');
    const label = element('label', 'logic-item-label', '关联指标');
    const search = registerControl(element('input', 'logic-binding-search'), item, 'search');
    search.type = 'text'; search.id = 'logic-binding-' + reviewControls.length;
    label.setAttribute('for', search.id); search.value = edit.selected ? display.text(edit.selected.name) : edit.query || '';
    search.placeholder = '搜索全部授权指标'; search.autocomplete = 'off';
    search.setAttribute('role', 'combobox'); search.setAttribute('aria-autocomplete', 'list');
    search.setAttribute('aria-expanded', 'false'); search.setAttribute('aria-label', item.title + '：搜索全部授权指标');
    const menu = element('div', 'logic-binding-menu'); menu.hidden = true;
    const list = element('div', 'logic-binding-options'); list.id = search.id + '-options'; list.setAttribute('role', 'listbox');
    search.setAttribute('aria-controls', list.id);
    const feedback = element('div', 'logic-binding-feedback'); feedback.setAttribute('aria-live', 'polite');
    const more = registerControl(element('button', 'logic-binding-more', '更多指标'), item, 'more'); more.type = 'button'; more.hidden = true;
    menu.append(list, feedback, more);
    const meta = element('p', 'logic-binding-meta', edit.selected ? bindingMeta(edit.selected) : '单位待确认');
    meta.title = meta.textContent; meta.textContent = edit.selected?.unit || '单位待确认';
    search.title = meta.title;
    const session = {timer: null, controller: null, token: 0, rows: [], active: -1, next: null, epoch: reviewEpoch, list}; searchSessions.push(session);
    function closeMenu() { menu.hidden = true; search.setAttribute('aria-expanded', 'false'); search.removeAttribute?.('aria-activedescendant'); }
    function showMenu() { menu.hidden = false; search.setAttribute('aria-expanded', 'true'); }
    function activate(index) {
      session.active = index;
      [...list.children].forEach((option, position) => option.setAttribute('aria-selected', String(position === index)));
      if (index >= 0) { search.setAttribute('aria-activedescendant', list.children[index].id); list.children[index].scrollIntoView?.({block: 'nearest'}); }
    }
    function choose(row) {
      if (busy || !managed?.can_write) return;
      edit.selected = row; edit.query = ''; edit.bindingChanged = true; search.value = display.text(row.name); meta.textContent = bindingMeta(row); meta.title = meta.textContent;
      meta.textContent = row.unit || '单位待确认'; search.title = bindingMeta(row);
      changed(edit); closeMenu(); return updateReview(item, edit, 'confirm');
    }
    function drawOptions() {
      list.replaceChildren();
      for (const [index, row] of session.rows.entries()) {
        const option = element('button', 'logic-binding-option'); option.type = 'button'; option.id = list.id + '-' + index;
        option.setAttribute('role', 'option'); option.setAttribute('aria-selected', 'false'); option.tabIndex = -1;
        const name = element('span', '', display.text(row.name)), detail = element('small', '', bindingMeta(row));
        option.title = bindingMeta(row); option.append(name, detail);
        option.addEventListener('mousedown', event => event.preventDefault());
        option.addEventListener('click', () => choose(row)); list.append(option);
      }
      session.active = -1; more.hidden = session.next === null || session.next === undefined;
    }
    async function load(append = false) {
      clearTimeout(session.timer); session.controller?.abort(); session.controller = new AbortController();
      const token = ++session.token, draftId = draft?.draft_id;
      if (!draftId) return;
      showMenu(); feedback.textContent = '正在搜索授权目录…'; more.disabled = true;
      try {
        const query = new URLSearchParams({draft_id: draftId, search: edit.query || '', offset: String(append ? session.next : 0), limit: '20'});
        const result = await api('/review/indicators?' + query, undefined, 'GET', session.controller.signal);
        if (token !== session.token || session.epoch !== reviewEpoch || draft?.draft_id !== draftId) return;
        const rows = result.items || [];
        session.rows = append ? [...session.rows, ...rows.filter(row => !session.rows.some(existing => existing.series_id === row.series_id))] : rows;
        session.next = result.next_offset; drawOptions();
        feedback.textContent = session.rows.length ? `已显示 ${session.rows.length} / ${result.total} 个匹配指标` : '没有匹配指标，可调整搜索或保留文字。';
      } catch (error) {
        if (error.name !== 'AbortError' && token === session.token && session.epoch === reviewEpoch) feedback.textContent = '指标目录暂时不可用，请稍后重试。';
      } finally { if (token === session.token) more.disabled = false; }
    }
    search.addEventListener('focus', () => load());
    search.addEventListener('input', () => {
      edit.query = search.value; edit.selected = null; edit.errorFields = [...new Set([...(edit.errorFields || item.attention_fields || []), 'binding'])]; edit.errorMessage = '请选择搜索结果中的指标。'; changed(edit);
      session.token++; session.controller?.abort(); clearTimeout(session.timer);
      session.rows = []; list.replaceChildren(); more.hidden = true;
      meta.textContent = '单位待确认'; showMenu(); feedback.textContent = '等待搜索…';
      session.timer = setTimeout(() => load(), 180);
    });
    search.addEventListener('keydown', event => {
      if (event.key === 'Escape') { closeMenu(); event.preventDefault(); }
      else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault(); showMenu();
        if (session.rows.length) activate(session.active < 0 ? (event.key === 'ArrowDown' ? 0 : session.rows.length - 1) :
          (session.active + (event.key === 'ArrowDown' ? 1 : -1) + session.rows.length) % session.rows.length);
      } else if (event.key === 'Enter' && !menu.hidden) {
        event.preventDefault(); if (session.active >= 0) return choose(session.rows[session.active]);
      } else if (event.key === 'Tab') closeMenu();
    });
    search.addEventListener('blur', () => { session.timer = setTimeout(closeMenu, 120); });
    more.addEventListener('click', () => { if (session.next !== null && session.next !== undefined) return load(true); });
    const picker = element('div', 'logic-binding-picker'); picker.append(search, menu);
    field.append(label, picker, meta); return field;
  }
  function renderReview() {
    if (!byId('review')) return;
    stopSearches(); reviewControls = [];
    const available = Boolean(review?.sections); byId('review').hidden = !available;
    for (const name of ['coverage', 'bindings', 'unresolved']) byId(name).hidden = available;
    if (!available) { syncApply(); return; }
    byId('import').hidden = true;
    const rawSource = review.source_text ?? draft.source_text ?? input.value;
    byId('review-source').textContent = rawSource; byId('review-reuse').checked = review.reuse_enabled === true;
    const nav = byId('review-nav'), items = byId('review-items'); nav.replaceChildren(); items.replaceChildren();
    for (const section of review.sections) {
      const sectionItems = section.items || [], remaining = sectionItems.filter(needsAttention).length;
      const navButton = element('button', 'logic-review-section-link'); navButton.type = 'button'; navButton.dataset.reviewSection = section.id;
      navButton.append(element('span', '', section.title), element('small', '', remaining ? `${remaining} 需处理` : `${sectionItems.length} 项`));
      navButton.addEventListener('click', () => selectSection(section.id)); nav.append(navButton);
      const group = element('section', 'logic-review-group'); group.dataset.reviewSection = section.id;
      group.append(element('h3', '', section.title));
      for (const item of sectionItems) {
        let edit = reviewEdits.get(item.id);
        if (!edit) { edit = {selected: item.binding || item.ai_binding || null, remember: rememberedChoices.has(item.id), dirty: false, query: '', sourceQuote: null, bindingChanged: false}; reviewEdits.set(item.id, edit); }
        const card = element('article', 'logic-review-card'); card.dataset.reviewItem = item.id; card.dataset.reviewStatus = item.status;
        card.append(element('strong', 'logic-item-title', display.text(item.title)));
        const fields = new Map();
        function field(node, name) {
          node.dataset.reviewField = name; node.className += ' logic-review-field';
          const error = element('p', 'logic-field-error'); error.setAttribute('role', 'status'); error.hidden = true;
          node.append(error); fields.set(name, {node, error}); return node;
        }
        const note = element('div', 'logic-item-description');
        note.append(element('p', '', display.text(item.description || ''))); field(note, 'content'); card.append(note);
        if (item.rule) {
          const condition = element('div', 'logic-item-condition'); condition.dataset.reviewItem = item.id; condition.dataset.reviewControl = 'condition';
          const value = element('span'); condition.append(value); field(condition, 'condition');
          edit.renderCondition = () => { value.textContent = ruleCondition(item, edit); condition.dataset.conditionText = value.textContent; };
          edit.renderCondition(); card.append(condition);
        }
        const columns = element('div', 'logic-item-columns'), original = element('div', 'logic-item-original');
        const excerpt = item.original_text || item.source_quote;
        const sourceParagraph = element('p', 'logic-item-source', excerpt || ''); sourceParagraph.hidden = !excerpt; original.append(sourceParagraph);
        const localSource = element('pre', 'logic-source-text'); localSource.tabIndex = 0;
        const fullSource = element('details', 'logic-item-full-source'); fullSource.append(element('summary', '', '原文'), localSource);
        fullSource.addEventListener('toggle', () => { if (fullSource.open && !localSource.textContent) localSource.textContent = rawSource; });
        const quote = element('p', 'logic-item-quote', edit.sourceQuote || ''); quote.hidden = !quote.textContent;
        const quoteButton = registerControl(element('button', 'logic-quote-button', '采用所选原文'), item, 'quote'); quoteButton.type = 'button';
        quoteButton.addEventListener('mousedown', event => event.preventDefault());
        quoteButton.addEventListener('click', () => {
          const selection = window.getSelection?.(), selected = selection?.toString() || '';
          if (!selected || ![byId('review-source'), localSource, sourceParagraph].some(node => node.contains(selection.anchorNode) && node.contains(selection.focusNode)) || !rawSource.includes(selected)) return inform('请在原文中选择对应片段。', true);
          if (selected.length > 4000) return inform('请选择较短的原文依据。', true);
          edit.sourceQuote = selected; quote.textContent = selected; quote.hidden = false; changed(edit); return updateReview(item, edit, 'confirm');
        });
        fullSource.append(quoteButton); original.append(quote, fullSource); field(original, 'source_quote');
        if (item.can_bind) {
          columns.append(original);
          const binding = renderBinding(item, edit); field(binding, 'binding');
          const rememberLabel = element('label', 'logic-remember-label');
          const remember = registerControl(element('input'), item, 'remember', () => !edit.selected); remember.type = 'checkbox'; remember.checked = edit.remember;
          remember.addEventListener('change', () => { edit.remember = remember.checked; changed(edit); return updateReview(item, edit, 'confirm'); });
          rememberLabel.append(remember, element('span', '', '记住选择')); binding.append(rememberLabel); columns.append(binding); card.append(columns);
        }
        const actions = element('div', 'logic-item-actions');
        for (const [action, label] of [['keep_text', '仅保留文字'], ['exclude', '排除']]) {
          const button = registerControl(element('button', '', label), item, action); button.type = 'button'; button.addEventListener('click', () => updateReview(item, edit, action)); actions.append(button);
        }
        const adjustments = element('details', 'logic-item-adjustments'); adjustments.dataset.reviewItem = item.id; adjustments.dataset.reviewControl = 'adjust'; adjustments.open = Boolean(edit.adjusting);
        adjustments.append(element('summary', '', '调整'));
        if (!item.can_bind) adjustments.append(original);
        adjustments.append(actions); adjustments.addEventListener('toggle', () => { edit.adjusting = adjustments.open; }); card.append(adjustments);
        edit.renderErrors = () => {
          const marked = edit.errorFields || item.attention_fields || (item.status === 'pending' ? [item.can_bind ? 'binding' : 'content'] : []);
          const reasons = item.kind === 'unresolved_binding' ? item.attention_reasons || [] : item.warnings?.length ? item.warnings : item.attention_reasons || [];
          const reason = display.text(edit.errorMessage || (typeof reasons[0] === 'string' ? reasons[0] : reasons[0]?.message) || '请修改红框位置。');
          for (const [name, entry] of fields) {
            const invalid = marked.includes(name); entry.node.dataset.invalid = String(invalid); entry.node.setAttribute('aria-invalid', String(invalid)); entry.error.hidden = !invalid; entry.error.textContent = invalid ? reason : '';
          }
        };
        edit.renderErrors(); group.append(card);
      }
      items.append(group);
    }
    selectSection(selectedSection); syncReviewControls(); syncApply();
  }
  async function hydrateReview(value) {
    if (value.review_available && !value.review) { review = await api('/review?draft_id=' + encodeURIComponent(value.draft_id)); draft.review = review; renderReview(); }
  }
  async function updateReview(item, edit, action, holdBusy = false) {
    if ((busy && !holdBusy) || !review || !draft || !managed?.can_write) return;
    const body = {draft_id: draft.draft_id, revision: review.revision, item_id: item.id, action, remember: action === 'confirm' && edit.remember};
    if (action === 'confirm' && item.can_bind) body.series_id = edit.selected?.series_id || null;
    if (edit.sourceQuote !== null) body.source_quote = edit.sourceQuote;
    setBusy(true); inform('正在同步修改…');
    try {
      const result = await api('/review/update', body);
      if (body.remember) rememberedChoices.add(item.id); else rememberedChoices.delete(item.id);
      reviewEdits.delete(item.id); presentDraft(result, true); inform('');
    } catch (error) {
      if (error.status === 409) { invalidate(); inform(error.message, true); }
      else if (error.name !== 'AbortError') { edit.dirty = true; edit.errorMessage = error.message; edit.errorFields = Array.isArray(error.fields) ? error.fields : item.attention_fields?.length ? item.attention_fields : [item.can_bind ? 'binding' : 'content']; edit.renderErrors?.(); inform(''); }
    } finally { if (!holdBusy) setBusy(false); }
  }
  async function open() {
    if (busy) return;
    controller = new AbortController();
    if (!dialog.open) dialog.showModal();
    if (parent !== window) parent.postMessage({type: 'research-drawer-state', open: true}, location.origin);
    inform('正在读取当前逻辑与版本…');
    try {
      managed = await api(managePath()); showVersion(managed.current);
      byId('variety').value = managed.current || managed.latest ? managed.variety || '' : '';
      text('version-label', `${managed.current ? display.reference(managed.current) : '暂无当前逻辑'}。后续保存仍为草稿，原记录保留。`);
      const versions = byId('version'); versions.replaceChildren();
      for (const row of managed.versions) {
        const option = document.createElement('option'); option.value = row.version; option.textContent = `${display.reference(row)} · ${row.status}`; versions.append(option);
      }
      text('version-range', managed.versions_partial ? `最近${managed.versions.length}/${managed.version_total}版；更早版本可输入版号` : `${managed.version_total}个版本`);
      dialog.querySelector('[data-logic-tab="analyze"]').disabled = !managed.can_write;
      setBusy(false);
      if (managed.can_write) {
        const models = await api('/models'); const selector = byId('model'); selector.replaceChildren();
        for (const model of models.models) {
          const option = document.createElement('option'); option.value = model.id; option.textContent = model.name; selector.append(option);
        }
        if (!models.models.length) { selector.append(new Option('暂无可用对话模型', '')); }
      }
      await loadPanels();
      if (review && draft) renderReview();
      inform('');
      if (dialog.dataset.prefill === 'true') { tab('analyze'); dialog.dataset.prefill = 'false'; }
    } catch (error) { inform(error.message, true); }
  }
  function close() {
    controller?.abort(); stopSearches(); setBusy(false); dialog.close();
    if (parent !== window) parent.postMessage({type: 'research-drawer-state', open: false}, location.origin);
  }
  async function recover(error) {
    if (error.name === 'AbortError') return;
    if (error.status === 409) {
      invalidate();
      try { const refreshed = await api(managePath()); if (managed) managed.latest = refreshed.latest; } catch {}
      inform(error.message + ' 原文已保留，可重新分析。', true);
    } else inform(error.message, true);
  }
  byId('generate').addEventListener('click', async () => {
    if (busy || !managed?.can_write) return;
    if (!input.value.trim() || !byId('model').value) return inform('请添加完整原文并选择模型。', true);
    const variety = byId('variety').value.trim();
    if (!variety) return inform('请按研究原文填写品种代码。', true);
    if (new TextEncoder().encode(input.value).length > 48 * 1024) return inform('原文超过48 KiB，请缩小范围。', true);
    invalidate(); controller = new AbortController(); setBusy(true); inform('正在分析完整原文并匹配真实指标，尚未保存版本…');
    try {
      const baseline = variety && variety !== managed.variety ? await api('/manage?variety=' + encodeURIComponent(variety)) : managed;
      const value = await api('/generate', {model_id: byId('model').value, text: input.value,
        source_name: byId('file-name').textContent.slice(0, 128), base_version: baseline.latest?.version || 0,
        ...(variety ? {variety} : {}), ...panelTitle()});
      presentDraft(value); await hydrateReview(value); inform(value.review_available ? '' : value.notice);
    } catch (error) { await recover(error); }
    finally { setBusy(false); }
  });
  byId('apply').addEventListener('click', async () => {
    if (busy || !draft || !managed?.can_write) return;
    if (hasReviewEdits()) return inform('请先确认、保留文字或排除已修改的条目，再保存草稿。', true);
    const selected = draft; controller = new AbortController(); setBusy(true); inform('正在确认来源与版本并保存草稿…');
    try {
      const result = await api(selected.kind === 'structured' ? '/structured/apply' : '/apply', {draft_id: selected.draft_id, base_version: selected.base_version,
        ...(selected.kind !== 'structured' && review ? {review_revision: review.revision} : {})});
      invalidate(); inform(result.notice); if (result.id) navigateToJudgment(result.id); else location.reload();
    } catch (error) { await recover(error); }
    finally { setBusy(false); }
  });
  input.addEventListener('input', invalidate); byId('model').addEventListener('change', invalidate);
  byId('variety').addEventListener('input', invalidate);
  byId('new-panel-title')?.addEventListener('input', invalidate);
  byId('panel-switch')?.addEventListener('change', syncPanelName);
  byId('panel-open')?.addEventListener('click', () => {
    if (busy || !byId('panel-switch').value) return;
    if (input.value.trim() || draft) return inform('输入原文或草稿尚未处理，请先保存、保留或清空，再切换研判。', true);
    navigateToJudgment(byId('panel-switch').value);
  });
  byId('panel-more')?.addEventListener('click', async () => {
    if (busy) return;
    setBusy(true); try { await loadPanels(true); } catch (error) { inform(error.message, true); } finally { setBusy(false); }
  });
  byId('panel-save')?.addEventListener('click', async () => {
    if (busy || !managed?.can_write) return;
    const row = panels.find(item => String(item.judgment_id) === byId('panel-switch').value);
    const title = byId('panel-name').value.trim();
    if (!row || !title) return inform('请先选择已保存研判并填写名称。', true);
    setBusy(true);
    try {
      await api(row.named ? '/panels/' + encodeURIComponent(row.id) : '/panels', row.named ?
        {title, revision: row.revision} : {title, judgment_id: row.judgment_id, version: row.version}, row.named ? 'PATCH' : 'POST');
      await loadPanels(); inform('面板名称已保存；研判原文和生效状态保持不变。');
    } catch (error) { inform(error.message, true); } finally { setBusy(false); }
  });
  byId('file').addEventListener('change', async event => {
    const file = event.target.files?.[0]; if (!file) return;
    if (file.size > 48 * 1024) return inform('请选择48 KiB以内的完整 .md / .txt / .json 文件。', true);
    try { input.value = new TextDecoder('utf-8', {fatal: true}).decode(await file.arrayBuffer()); text('file-name', file.name); invalidate(); }
    catch { inform('文件不是有效UTF-8文本，请转换编码后再导入。', true); }
  });
  byId('skeleton').addEventListener('click', () => {
    if (input.value.trim()) return inform('请先保留现有输入，再清空以插入骨架。');
    input.value = ['# 研究逻辑', '## 主矛盾', '支持依据：\n反对依据：\n数据锚与验证窗口：',
      '## 短线边际', '## 成本线与口径', '## 边际定价权', '## 时间维度', '## 调研信息差',
      '立场、样本、时点、价格是否已反映：', '## 指标白名单', '## 编排范式与基调',
      '## 阈值、证伪条件与数据缺口'].join('\n\n'); invalidate(); input.focus();
  });
  byId('structured').addEventListener('click', async () => {
    if (busy || !managed?.can_write) return;
    let payload;
    try { payload = JSON.parse(input.value); }
    catch { return inform('请选择或粘贴完整的结构化 JSON 研究稿；普通文本请使用“分析原文”。', true); }
    invalidate(); controller = new AbortController(); setBusy(true); inform('正在核对结构和已授权指标，尚未保存…');
    try { const result = await api('/structured/preview', {payload, ...panelTitle()}); presentDraft(result); await hydrateReview(result); inform(result.notice); }
    catch (error) { await recover(error); }
    finally { setBusy(false); }
  });
  byId('reuse').addEventListener('click', () => {
    if (!displayed) return inform('尚无可复用版本。');
    if (input.value.trim() && input.value !== displayed.markdown) return inform('已有输入，请先保留或清空，再复用当前MD。');
    input.value = displayed.markdown; text('file-name', '复用已保存研判 MD'); invalidate(); tab('analyze'); input.focus();
  });
  byId('reuse-original').addEventListener('click', () => {
    if (!displayed?.source_text) return inform('尚无可复用原文。');
    if (input.value.trim() && input.value !== displayed.source_text) return inform('已有输入，请先保留或清空，再复用原文。');
    input.value = displayed.source_text; text('file-name', '复用已保存研判原文'); invalidate(); tab('analyze'); input.focus();
  });
  byId('download').addEventListener('click', () => {
    if (!displayed) return;
    const url = URL.createObjectURL(new Blob([displayed.markdown], {type: 'text/markdown;charset=utf-8'}));
    const link = document.createElement('a'); link.href = url; link.download = `research-logic-v${displayed.version}.md`; link.click(); URL.revokeObjectURL(url);
  });
  async function loadVersion(value) {
    if (!value) return;
    const selection = selectedId() ? '?judgment_id=' + encodeURIComponent(selectedId()) : '';
    try { const result = await api('/versions/' + encodeURIComponent(value) + selection); text('history-md', result.markdown); text('history-diff', result.diff.markdown); inform(''); }
    catch (error) { inform(error.message, true); }
  }
  byId('version').addEventListener('change', event => loadVersion(event.target.value));
  for (const [name, mode] of [['review-attention', 'attention'], ['review-all', 'all']]) byId(name)?.addEventListener('click', () => { reviewView = mode; syncReviewVisibility(); });
  byId('review-edit')?.addEventListener('click', () => {
    if (busy) return;
    byId('import').hidden = false; input.focus();
    inform('修改原文后需重新分析；当前原文已完整保留。');
  });
  byId('review-reuse')?.addEventListener('change', async () => {
    if (busy || !review || !draft || !managed?.can_write || hasReviewEdits()) return;
    const enabled = byId('review-reuse').checked;
    setBusy(true); inform('正在更新当前工作台的复用偏好…');
    try {
      const result = await api('/review/reuse', {draft_id: draft.draft_id, revision: review.revision, enabled});
      presentDraft(result, true);
      if (enabled) {
        const suggestions = review.sections.flatMap(section => section.items || []).filter(item => item.remembered && item.status === 'pending' && item.can_bind && item.binding?.series_id);
        for (const item of suggestions) { const edit = reviewEdits.get(item.id); edit.selected = item.binding; changed(edit); }
        for (const suggestion of suggestions) {
          if (!review || !draft) break;
          const item = review.sections.flatMap(section => section.items || []).find(row => row.id === suggestion.id);
          await updateReview(item, reviewEdits.get(item.id), 'confirm', true);
        }
      }
      inform('');
    } catch (error) { byId('review-reuse').checked = !enabled; if (error.name !== 'AbortError') inform(error.message, true); }
    finally { setBusy(false); }
  });
  byId('load-version').addEventListener('click', () => loadVersion(byId('version-number').value || byId('version').value));
  dialog.querySelectorAll('[data-logic-tab]').forEach(button => button.addEventListener('click', () => {
    tab(button.dataset.logicTab); if (button.dataset.logicTab === 'versions') loadVersion(byId('version').value);
  }));
  dialog.querySelector('[data-logic-close]').addEventListener('click', close);
  dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
  document.querySelectorAll('[data-open-logic]').forEach(button => button.addEventListener('click', open));
  window.JudgmentManager = {open, close};
  if (dialog.dataset.prefill === 'true') open();
})();
