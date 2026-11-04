(function () {
  const dialog = document.getElementById('judgment-manager');
  if (!dialog) return;
  const byId = name => document.getElementById('logic-' + name);
  const display = window.ResearchDisplay || {text: value => String(value ?? ''), prose: value => String(value ?? ''), reference: row => row?.title || (row?.created_at ? '研究判断 · ' + row.created_at.slice(0, 16).replace('T', ' ') : '已保存研究判断')};
  const input = byId('input'), status = byId('status');
  const endpoint = (window.RESEARCH_PREFIX || '') + '/api/sn/judgment';
  let managed, draft, displayed, busy = false, controller, panels = [], review = null, reviewEpoch = 0;
  let activeJob = null, jobTimer, jobEpoch = 0, submissionSerial = 0, submittingJob = false;
  let latestResultJob = null;
  let itemConfirmation = null;
  let uncertainSubmission = null;
  let historyEntries = [], historical = null, historyEpoch = 0, historyPending = false;
  const reviewEdits = new Map();
  const rememberedChoices = new Set();
  let reviewControls = [], searchSessions = [], selectedSection = '', reviewView = 'all';
  let bindingControlSerial = 0;
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
    closeItemConfirmation();
    stopJobMonitor(true); submissionSerial++;
    stopSearches(); draft = null; review = null; reviewEdits.clear(); rememberedChoices.clear();
    byId('preview').hidden = true;
    if (byId('import')) byId('import').hidden = false;
    syncDiscard();
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
    if (byId('restore-result')) byId('restore-result').disabled = value || managed?.can_write === false;
    syncDiscard();
    syncManagement();
    syncReviewControls();
    syncApply();
  }
  async function api(path, body, method = 'POST', signal = controller?.signal, headers = {}, keepalive = false) {
    const response = await researchFetch(endpoint + path, body === undefined ? {signal} : {
      method, headers: {'Content-Type': 'application/json', ...headers}, body: JSON.stringify(body), signal, keepalive
    });
    let value;
    try { value = await response.json(); } catch { throw new Error('研究逻辑服务暂时不可用，输入已保留。'); }
    if (!response.ok) { const detail = value.detail; const message = typeof detail === 'string' ? detail :
      detail?.message ? detail.message + (detail.issues?.length ? '：' + detail.issues.slice(0, 4).map(item => item.message).join('；') : '') : '请求未完成，输入已保留。';
      const error = new Error(message); error.status = response.status; error.fields = detail?.attention_fields;
      if (typeof detail?.frequency_required === 'boolean') error.frequencyRequired = detail.frequency_required;
      if (detail?.code === 'binding_warning' && typeof detail.confirmation_token === 'string') error.bindingWarningToken = detail.confirmation_token;
      if (detail?.code === 'number_review' && typeof detail.confirmation_token === 'string') error.numberReviewToken = detail.confirmation_token;
      throw error; }
    return value;
  }
  function panelTitle() { const title = byId('new-panel-title')?.value.trim(); return title ? {panel_title: title} : {}; }
  function navigateToJudgment(identifier) {
    const url = new URL(location.href || endpoint.replace('/api/sn/judgment', '/sn/judgment'), location.origin);
    if (identifier) url.searchParams.set('judgment_id', String(identifier));
    else url.searchParams.delete('judgment_id');
    url.searchParams.delete('analysis_job');
    if (typeof location.assign === 'function') location.assign(url.href); else location.href = url.href;
  }
  const jobId = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value) ? value : '';
  function checkJob(job, strictScope = false) {
    const workbench = window.RESEARCH_CONTEXT?.workbench;
    if (!jobId(job?.id) || (strictScope ? job.purpose !== 'judgments' : job.purpose && job.purpose !== 'judgments')
        || (strictScope ? !workbench || job.workbench_id !== workbench : job.workbench_id && workbench && job.workbench_id !== workbench))
      throw new Error('后台任务范围已改变，请重新打开工作台。');
  }
  // An unsaved background result can be discarded: the one offered for viewing, or the one under review now.
  function discardableJob() {
    if (managed?.can_write === false) return null;
    if (draft && activeJob?.state === 'succeeded' && jobId(activeJob.id)) return activeJob;
    return latestResultJob;
  }
  function syncDiscard() {
    const button = byId('discard-result');
    if (!button) return;
    button.hidden = !discardableJob(); button.disabled = busy;
  }
  async function discoverLatestResult() {
    const button = byId('restore-result');
    latestResultJob = null;
    if (button) button.hidden = true;
    if (!button || !managed?.can_write) return;
    const currentController = controller;
    try {
      const result = await api('/jobs/latest');
      if (!dialog.open || controller !== currentController || currentController.signal.aborted || !result?.job) return;
      checkJob(result.job, true);
      if (result.job.state !== 'succeeded' || result.job.result_available !== true) return;
      latestResultJob = result.job; button.hidden = false;
    } catch { /* A missing temporary result never blocks the editor. */ }
    finally { syncDiscard(); }
  }
  function announceJob(job, appliedId) {
    const workbenchId = window.RESEARCH_CONTEXT?.workbench;
    if (parent === window || !workbenchId || !jobId(job?.id)) return;
    parent.postMessage({type: appliedId ? 'research-analysis-job-applied' : 'research-analysis-job-started',
      workbenchId, jobId: job.id, ...(appliedId ? {judgmentId: appliedId} : {})}, location.origin);
  }
  function stopJobMonitor(forget = false) {
    clearTimeout(jobTimer); jobEpoch++;
    if (forget) activeJob = null;
  }
  function requestKey() {
    const bytes = new Uint8Array(16);
    (window.crypto || globalThis.crypto).getRandomValues(bytes);
    return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('');
  }
  async function followJob(identifier) {
    stopJobMonitor(); const epoch = jobEpoch;
    activeJob = {...activeJob, id: identifier};
    const current = () => epoch === jobEpoch && activeJob?.id === identifier && dialog.open;
    async function poll() {
      if (!current()) return;
      setBusy(true);
      try {
        const job = await api('/jobs/' + encodeURIComponent(identifier));
        if (!current()) return;
        checkJob(job);
        if (job.id !== identifier) throw new Error('当前请求与返回的任务不一致，请重新打开任务。');
        activeJob = job;
        if (job.state === 'queued' || job.state === 'running') {
          const labels = {queued: '等待分析', selecting: '正在匹配指标', analyzing: '正在分析原文', validating: '正在校验结果'};
          inform((labels[job.phase] || '正在后台分析') + '，可关闭窗口。');
          jobTimer = setTimeout(poll, 2000); return;
        }
        if (job.state === 'succeeded') {
          const value = await api('/jobs/' + encodeURIComponent(identifier) + '/result');
          if (!current()) return;
          if (value.job) { checkJob(value.job); if (value.job.id !== identifier) throw new Error('分析结果与当前任务不一致。'); }
          input.value = value.source_text || input.value;
          if (value.variety) byId('variety').value = value.variety;
          if (value.model_id) byId('model').value = value.model_id;
          if (value.source_name) text('file-name', value.source_name);
          if (byId('new-panel-title') && value.panel_title !== undefined) byId('new-panel-title').value = value.panel_title;
          presentDraft(value); await hydrateReview(value); tab('analyze'); inform(previewNotice(value));
        } else inform(job.message || ({failed: '分析失败，请重新发起。', interrupted: '分析已中断，请重新发起。', expired: '分析结果已过期，请重新分析。', applied: '此分析已保存。', superseded: '已有更新的研究分析结果，可在导入研判中查看最新暂存结果。'}[job.state] || '分析暂不可用。'), job.state !== 'superseded');
        announceJob(job); setBusy(false);
      } catch (error) {
        if (current() && error.name !== 'AbortError') { setBusy(false); inform(error.message, true); }
      }
    }
    return poll();
  }
  function syncPanelName() {
    const row = panels.find(item => String(item.judgment_id) === byId('panel-switch')?.value);
    if (byId('panel-name')) byId('panel-name').value = row?.title || '';
    if (byId('panel-save')) byId('panel-save').textContent = row?.named ? '保存名称' : '引用为面板';
  }
  async function loadPanels(append = false) {
    if (!byId('panel-switch')) { renderHistory(); return; }
    const result = await api('/panels' + (append ? '?offset=' + panels.length : ''));
    const previous = byId('panel-switch').value;
    panels = [...new Map((append ? [...panels, ...result.items] : result.items).map(row => [row.judgment_id, row])).values()];
    const selector = byId('panel-switch'); selector.replaceChildren();
    for (const row of panels) {
      const option = document.createElement('option'); option.value = String(row.judgment_id);
      option.textContent = `${display.reference(row)} · ${row.status || '草稿'}`; selector.append(option);
    }
    selector.value = previous || selectedId() || String(managed.current?.id || panels[0]?.judgment_id || '');
    byId('panel-range').textContent = `${panels.length}/${result.total} 个已保存研判`;
    byId('panel-more').hidden = !result.has_more;
    syncPanelName();
    renderHistory(result.has_more);
  }
  function text(id, value) { byId(id).textContent = display.text(value || ''); }
  function showVersion(row) {
    displayed = row;
    text('current-md', row?.markdown || '尚无研究逻辑。可在“导入与分析”添加原文。');
    text('original-source', row?.source_text || '尚无原始输入');
    text('current-state', row ? `${display.reference(row)} · ${row.status}` : '尚无研究逻辑');
    syncManagement();
  }
  function syncManagement() {
    const writing = managed?.can_write === true;
    if (byId('current-delete')) byId('current-delete').disabled = busy || !writing || !displayed || !Number.isSafeInteger(displayed.delete_revision);
    for (const name of ['history-download', 'history-reuse', 'history-delete']) if (byId(name))
      byId(name).disabled = busy || historyPending || !historical || (name !== 'history-download' && !writing)
        || (name === 'history-delete' && !Number.isSafeInteger(historical?.delete_revision));
    if (byId('history-more')) byId('history-more').disabled = busy;
  }
  function renderHistory(hasMore = false) {
    const selector = byId('version'), previous = selector.value;
    const rows = panels.length ? panels.map(row => ({...row, record_id: row.judgment_id}))
      : (managed?.versions || []).map(row => ({...row, record_id: row.id, variety: managed.variety}));
    historyEntries = rows.map(row => ({...row, key: row.record_id ? 'record:' + row.record_id : 'version:' + row.version}));
    selector.replaceChildren();
    for (const row of historyEntries) {
      const option = document.createElement('option'); option.value = row.key;
      option.textContent = `${display.reference(row)} · ${row.status || '草稿'}`; selector.append(option);
    }
    selector.value = historyEntries.some(row => row.key === previous) ? previous : historyEntries[0]?.key || '';
    if (!historyEntries.some(row => String(row.record_id) === String(historical?.id))) {
      historical = null; text('history-md', '选择一份历史研判查看。'); text('history-diff', '');
    }
    text('version-range', `${historyEntries.length} 份已保存研判`);
    if (byId('history-more')) byId('history-more').hidden = !hasMore;
    syncManagement();
  }
  function exportMarkdown(row) {
    if (!row || typeof row.markdown !== 'string') return;
    const url = URL.createObjectURL(new Blob([row.markdown], {type: 'text/markdown;charset=utf-8'}));
    const link = document.createElement('a'); link.href = url; link.download = `research-logic-v${row.version}.md`;
    link.click(); URL.revokeObjectURL(url);
  }
  function reuseMarkdown(row, original = false) {
    const value = original ? row?.source_text : row?.markdown;
    if (!value) return inform('尚无可复用内容。');
    if (busy || draft || input.value.trim() && input.value !== value) return inform('已有输入或待保存草稿，请先保留或清空，再复用研判。');
    input.value = value;
    if (row.variety) byId('variety').value = row.variety;
    text('file-name', original ? '复用已保存研判原文' : '复用已保存研判 MD');
    invalidate(); tab('analyze'); input.focus();
  }
  async function removeVersion(row) {
    if (busy || !managed?.can_write || !row || !Number.isSafeInteger(row.delete_revision)) return;
    if (draft || input.value.trim()) return inform('请先保留或清空尚未处理的输入和草稿，再删除研判。', true);
    const label = panels.find(item => item.judgment_id === row.id)?.title || display.reference(row);
    if (!window.confirm(`确认删除“${label}”？删除后将从研判列表移除。`)) return;
    setBusy(true); inform('正在删除研判…');
    try {
      await api('/records/' + encodeURIComponent(row.id), {version: row.version, revision: row.delete_revision, confirmed: true}, 'DELETE');
      if (String(displayed?.id) === String(row.id)) {
        const wb = window.RESEARCH_CONTEXT?.workbench;
        if (parent !== window && wb) {
          try {
            const outer = new URL(parent.location.href);
            if (outer.origin === location.origin && outer.pathname.split('/').includes(wb)
                && outer.searchParams.get('judgment_id') === String(row.id)) {
              outer.searchParams.delete('judgment_id'); outer.searchParams.delete('analysis_job');
              parent.location.assign(outer.href); return;
            }
          } catch { /* A standalone or cross-origin host retains native navigation. */ }
        }
        navigateToJudgment(''); return;
      }
      historical = null; historyEpoch++;
      await loadPanels(); text('history-md', '该研判已删除，请选择其他记录。'); text('history-state', '已删除'); inform('研判已删除。');
    } catch (error) { if (error.name !== 'AbortError') inform(error.message, true); }
    finally { setBusy(false); }
  }
  function summarize(value) {
    if (value === null || value === undefined || value === '') return '未设置';
    if (Array.isArray(value)) return value.length + ' 项研究内容';
    if (typeof value === 'object') return Object.keys(value).length + ' 项研究信息';
    return display.text(String(value));
  }
  function previewNotice(value) {
    if (value.analysis_complete === false) return value.notice || '部分原文尚未完成核对，已保留可用结果。';
    if (value.analysis_reused) return '已复用相同原文的解析结果。';
    return value.notice || '';
  }
  function presentDraft(value, preserveEdits = false) {
    draft = value; byId('preview').hidden = false;
    text('preview-md', value.markdown); text('generated-diff', value.diff?.markdown);
    text('coverage', `${(value.bindings || []).length} 个已绑定指标`);
    const bindings = byId('bindings'); bindings.replaceChildren();
    for (const binding of value.bindings || []) {
      const chip = document.createElement('span'); chip.textContent = `${display.text(binding.name)} · ${binding.unit || '来源未注明单位，仅描述'}`; bindings.append(chip);
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
    else for (const [id, edit] of reviewEdits) if (!edit.dirty && !edit.extraRowsAdded) reviewEdits.delete(id);
    review = value.review || null;
    if (!preserveEdits) { reviewView = 'all'; selectedSection = ''; }
    renderReview(); syncDiscard(); inform(previewNotice(value));
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
      pendingCount() ? `有 ${pendingCount()} 项需处理，也可先保存草稿；未通过校验的规则不会参与执行。` :
      draft?.analysis_complete === false ? '部分原文尚未完成核对，可先保存现有草稿。' : '内容已解析，可以保存草稿；保存后不会自动生效。';
    if (byId('review-reuse')) byId('review-reuse').disabled = busy || !review || unsynced || !managed?.can_write;
    if (byId('review-edit')) byId('review-edit').disabled = busy;
    if (byId('review-discard')) byId('review-discard').disabled = busy || managed?.can_write === false;
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
    return [row?.unit || '单位未注明', shortFile(row?.source_file), row?.sheet].filter(Boolean).join(' · ');
  }
  // Match the server's finite missing-metadata markers; never infer a caliber
  // from an indicator title or from research prose.
  const unitPresent = value => typeof value === 'string' && !['', '—', '未注明'].includes(value.trim());
  const frequencyPresent = value => typeof value === 'string' && !['', '—', '未注明', '未确认'].includes(value.trim());
  function sourceCaliber(row) {
    if (!row) return '暂无';
    const unit = unitPresent(row.unit) ? row.unit.trim() : '';
    return '采用指标口径 · ' + (unit ? display.text(unit) : '来源未注明单位');
  }
  function changed(edit) { edit.dirty = true; edit.renderCondition?.(); edit.renderErrors?.(); edit.renderConfirmation?.(); edit.renderOutcome?.(); syncReviewVisibility(); syncReviewControls(); syncApply(); }
  const ruleNumber = '[+-]?(?:[0-9]+(?:\\.[0-9]+)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?';
  const singleRuleNumber = new RegExp('^' + ruleNumber + '$');
  const rangeRuleNumbers = new RegExp('^\\[\\s*(' + ruleNumber + ')\\s*,\\s*(' + ruleNumber + ')\\s*\\]$');
  const comparisonExpression = new RegExp('^(value|change)\\s*(>=|<=|>|<)\\s*(' + ruleNumber + ')$');
  const comparisonWords = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于'};
  const averageCrossingWords = {crosses_below: '下穿', crosses_above: '上穿'};
  function ruleCondition(item, edit) {
    const rule = item.rule || {}, sourceUnit = unitPresent(edit.selected?.unit) ? edit.selected.unit.trim() : '';
    const unit = sourceUnit ? ' ' + display.text(sourceUnit) : edit.selected ? '（来源未注明单位）' : '（尚未关联指标）';
    const details = [rule.type, rule.horizon].filter(value => typeof value === 'string' && value).map(value => display.text(value));
    if (item.kind === 'thresholds') {
      const literal = typeof rule.value_literal === 'string' ? rule.value_literal.trim() : '';
      let target;
      if (literal.startsWith('{')) try { target = JSON.parse(literal); } catch { /* Invalid operands remain marked for review. */ }
      if (target?.kind === 'sma' && Object.keys(target).length === 2
          && Number.isInteger(target.window) && target.window >= 2 && target.window <= 120) {
        if (Object.hasOwn(averageCrossingWords, rule.operator)) {
          return [`指标值${averageCrossingWords[rule.operator]} ${target.window} 期均线`, ...details].join('；');
        }
        if (Object.hasOwn(comparisonWords, rule.operator)) {
          return [`指标值${comparisonWords[rule.operator]} MA${target.window}`, ...details].join('；');
        }
      }
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
  function needsAttention(item) {
    if (reviewEdits.get(item.id)?.dirty === true) return true;
    if (['ready', 'confirmed', 'kept_text', 'excluded'].includes(item.status)) return false;
    return item.status === 'pending' || item.requires_attention === true;
  }
  function syncReviewVisibility() {
    if (!review?.sections || !byId('review-items')) return;
    const allItems = review.sections.flatMap(section => section.items || []), count = allItems.filter(needsAttention).length;
    const bound = new Set((draft?.bindings || []).map(binding => binding.series_id).filter(identifier => typeof identifier === 'string' && identifier)).size;
    byId('review-count').textContent = `已解析 ${review.total ?? allItems.length} 项 · 已关联 ${bound} 项指标 · 需处理 ${count} 项`;
    byId('review-attention')?.setAttribute('aria-pressed', String(reviewView === 'attention'));
    byId('review-all')?.setAttribute('aria-pressed', String(reviewView === 'all'));
    const visibleSections = review.sections.filter(section => reviewView === 'all' || section.items?.some(needsAttention));
    if (!visibleSections.some(section => section.id === selectedSection)) selectedSection = visibleSections[0]?.id || '';
    for (const node of byId('review-nav').children) {
      const section = review.sections.find(section => section.id === node.dataset.reviewSection);
      node.hidden = !visibleSections.includes(section); node.setAttribute('aria-current', String(section?.id === selectedSection));
      const remaining = (section?.items || []).filter(needsAttention).length;
      node.children[1].textContent = remaining ? `${remaining} 需处理` : `${section?.items?.length || 0} 项`;
    }
    for (const group of byId('review-items').children) {
      group.hidden = group.dataset.reviewSection !== selectedSection;
      for (const card of group.children) {
        if (!card.dataset.reviewItem) continue;
        const item = allItems.find(row => row.id === card.dataset.reviewItem), dirty = reviewEdits.get(item.id)?.dirty;
        card.hidden = reviewView === 'attention' && !needsAttention(item); card.dataset.reviewDirty = String(Boolean(dirty));
      }
    }
    if (byId('review-empty')) {
      byId('review-empty').hidden = visibleSections.length > 0;
      byId('review-empty').textContent = reviewView === 'attention' ? '暂无需要处理的问题，可切换“全部内容”查看研究逻辑。' : '原文没有提供可展示的研究条目，可返回补充原文。';
    }
  }
  function bindingSlots(edit) { return [edit, ...(edit.additional || [])]; }
  function hasBinding(edit) { return bindingSlots(edit).some(slot => slot.selected?.series_id); }
  function pendingBindingQuery(edit) { return bindingSlots(edit).some(slot => !slot.selected && slot.query?.trim()); }
  function closeItemConfirmation() {
    const current = itemConfirmation; itemConfirmation = null;
    if (!current) return;
    current.capture();
    reviewControls = reviewControls.filter(control => !current.node.contains(control.node));
    if (current.node.open) current.node.close();
    current.node.remove();
  }
  async function confirmWholeItem(item, edit) {
    if (busy || !review || !draft || !managed?.can_write) return;
    const quantitative = item.kind === 'thresholds' || item.kind === 'falsifiers';
    if (!quantitative || !edit.selected || pendingBindingQuery(edit)) return updateReview(item, edit, 'confirm');
    const caliber = item.caliber || {}, same = item.binding?.series_id === edit.selected.series_id;
    const sourceUnit = same && Object.hasOwn(caliber, 'catalog_unit') ? caliber.catalog_unit : edit.selected.unit;
    const sourceFrequency = same && Object.hasOwn(caliber, 'catalog_frequency') ? caliber.catalog_frequency : edit.selected.frequency;
    const knownUnit = unitPresent(sourceUnit);
    const knownFrequency = frequencyPresent(sourceFrequency);
    const frequencyRequired = edit.frequencyRequired ?? caliber.frequency_required;
    if (knownUnit && (frequencyRequired !== true || knownFrequency)) {
      const updated = await updateReview(item, edit, 'confirm');
      // The current catalog may reveal missing cadence after the preview was
      // rendered. Continue into the form on the same click, without reposting.
      if (updated === false && edit.frequencyRequired === true && !knownFrequency)
        return confirmWholeItem(item, edit);
      return updated;
    }
    closeItemConfirmation();
    const modal = element('dialog', 'logic-confirm-dialog'); modal.id = 'logic-confirm-item-dialog';
    const title = element('h3', '', '核对并确认整条'); title.id = 'logic-confirm-item-title'; modal.setAttribute('aria-labelledby', title.id);
    const content = element('div', 'logic-confirm-content');
    content.append(element('strong', '', display.text(item.title)), element('p', '', (display.prose || display.text)(item.description || '')),
      element('p', 'logic-confirm-condition', ruleCondition(item, edit)),
      element('p', 'logic-confirm-source', '计算指标：' + display.text(edit.selected.name || '已选指标')));
    if (edit.additional?.some(slot => slot.selected)) content.append(element('p', 'logic-confirm-source',
      '关联指标：' + edit.additional.filter(slot => slot.selected).map(slot => display.text(slot.selected.name || '已选指标')).join('、')));
    const prior = edit.confirmationDraft || {};
    let unit = null, frequency = null;
    if (knownUnit) content.append(element('p', 'logic-confirm-source', '采用指标单位：' + display.text(sourceUnit)));
    else {
      const suggestion = [same ? caliber.unit : null, caliber.model_unit, item.rule?.unit_confirmed]
        .find(value => unitPresent(value) && value.trim() !== '未确认') || '';
      const label = element('label', 'logic-confirm-label', suggestion && !Object.hasOwn(prior, 'unit') && !caliber.confirmed ? '数据单位（建议值，请核对）' : '数据单位（请核对）');
      unit = registerControl(element('input'), item, 'confirm-unit'); unit.type = 'text'; unit.maxLength = 80;
      unit.value = prior.unit ?? suggestion; unit.placeholder = '填写本条数据的单位';
      label.append(unit); content.append(label);
    }
    if (knownFrequency) content.append(element('p', 'logic-confirm-source', '采用指标频率：' + display.text(sourceFrequency)));
    else {
      const label = element('label', 'logic-confirm-label', frequencyRequired === true ? '数据频率（请核对）' : '数据频率（可选）');
      frequency = registerControl(element('select'), item, 'confirm-frequency');
      for (const [value, text] of [['', '未选择'], ['日', '日度'], ['周', '周度'], ['月', '月度'], ['季', '季度'], ['年', '年度']]) {
        const option = element('option', '', text); option.value = value; frequency.append(option);
      }
      frequency.value = prior.frequency ?? (same && ['日', '周', '月', '季', '年'].includes(caliber.frequency) ? caliber.frequency : '');
      label.append(frequency); content.append(label);
    }
    const basisLabel = element('label', 'logic-confirm-label', '核对说明');
    const basis = registerControl(element('textarea'), item, 'confirm-basis'); basis.rows = 2; basis.maxLength = 1000;
    basis.value = prior.basis ?? caliber.basis ?? '用户在本条确认窗口核对口径';
    if (!basis.value.trim()) basis.value = '用户在本条确认窗口核对口径';
    basisLabel.append(basis); content.append(basisLabel);
    const error = element('p', 'logic-field-error'); error.setAttribute('role', 'status'); error.hidden = true;
    const actions = element('div', 'logic-confirm-actions');
    const cancel = registerControl(element('button', '', '返回修改'), item, 'confirm-cancel'); cancel.type = 'button';
    const submit = registerControl(element('button', 'logic-primary', '确认整条'), item, 'confirm-submit'); submit.type = 'button';
    const capture = () => { edit.confirmationDraft = {unit: unit?.value ?? '', frequency: frequency?.value ?? '', basis: basis.value}; };
    cancel.addEventListener('click', closeItemConfirmation);
    submit.addEventListener('click', async () => {
      if (busy || itemConfirmation?.node !== modal) return;
      capture();
      if (!knownUnit && !unit.value.trim()) { error.textContent = '请填写并核对本条数据单位。'; error.hidden = false; unit.focus(); return; }
      if (frequencyRequired === true && !knownFrequency && !frequency.value) { error.textContent = '请选择并核对本条数据频率。'; error.hidden = false; frequency.focus(); return; }
      const value = {mode: knownUnit ? 'catalog' : 'declared', basis: basis.value.trim() || '用户在本条确认窗口核对口径', scope_note: caliber.scope_note || ''};
      if (!knownUnit) value.unit = unit.value.trim();
      if (frequency?.value) value.frequency = frequency.value;
      const updated = await updateReview(item, edit, 'confirm', false, value);
      if (updated) closeItemConfirmation();
      else { error.textContent = edit.errorMessage || '本条仍有需要核对的内容，请修改后再确认。'; error.hidden = false; }
    });
    actions.append(cancel, submit); modal.append(title, content, error, actions); dialog.append(modal);
    itemConfirmation = {node: modal, capture};
    modal.addEventListener('close', () => { if (itemConfirmation?.node === modal) closeItemConfirmation(); }); modal.showModal();
    (unit || frequency || submit).focus();
  }
  function cancelBindingField(node) {
    node.cancelBindingSearch?.();
    reviewControls = reviewControls.filter(control => !node.contains(control.node));
  }
  function renderBindings(item, edit, toolbar) {
    const group = element('div', 'logic-binding-group'), rows = element('div', 'logic-binding-rows');
    const limit = Number.isSafeInteger(item.max_bindings) ? Math.min(8, Math.max(1, item.max_bindings)) : 8;
    const add = item.can_add_binding ? registerControl(element('button', 'logic-binding-add', '新增绑定指标'), item, 'add-binding', () => bindingSlots(edit).length >= limit) : null;
    function drawRows() {
      for (const row of rows.children) cancelBindingField(row);
      rows.replaceChildren();
      bindingSlots(edit).forEach((slot, index) => {
        const row = renderBinding(item, edit, slot, index);
        row.dataset.bindingIndex = String(index);
        if (index > 0) {
          const remove = registerControl(element('button', 'logic-binding-remove', '移除'), item, 'remove-binding');
          remove.type = 'button'; remove.setAttribute('aria-label', '移除第 ' + (index + 1) + ' 个关联指标');
          remove.addEventListener('click', () => {
            if (busy || !managed?.can_write) return;
            const bound = Boolean(slot.selected?.series_id), queried = Boolean(slot.query?.trim());
            edit.additional = edit.additional.filter(value => value !== slot);
            edit.extraRowsAdded = edit.additional.some(value => !value.selected);
            drawRows();
            if (bound) { edit.bindingChanged = true; changed(edit); return updateReview(item, edit, hasBinding(edit) ? 'confirm' : 'keep_text'); }
            if (queried && !pendingBindingQuery(edit)) {
              edit.errorFields = undefined; edit.errorMessage = '';
              edit.dirty = edit.bindingChanged || edit.sourceQuote !== null;
              edit.renderErrors?.(); syncReviewVisibility(); syncApply();
            }
          });
          row.append(remove);
        }
        rows.append(row);
      });
      syncReviewControls();
    }
    group.append(rows);
    if (add) {
      add.type = 'button'; add.title = item.rule ? '新增关联指标不改变此规则的计算指标。' : '';
      add.addEventListener('click', () => {
        if (busy || !managed?.can_write || bindingSlots(edit).length >= limit) return;
        edit.additional.push({selected: null, query: ''}); edit.extraRowsAdded = true; drawRows();
      });
      toolbar.append(add);
    }
    drawRows(); return group;
  }
  function renderBinding(item, edit, selection = edit, index = 0) {
    const field = element('div', 'logic-binding-field logic-binding-row');
    const label = element('label', 'logic-item-label', index === 0 && item.rule ? '计算指标' : index ? '关联指标 ' + (index + 1) : '关联指标');
    const search = registerControl(element('input', 'logic-binding-search'), item, 'search');
    search.type = 'text'; search.id = 'logic-binding-' + (++bindingControlSerial);
    label.setAttribute('for', search.id); search.value = selection.selected ? display.text(selection.selected.name) : selection.query || '';
    search.placeholder = item.status === 'kept_text' ? '暂无，暂不关联' : '搜索全部授权指标'; search.autocomplete = 'off';
    search.setAttribute('role', 'combobox'); search.setAttribute('aria-autocomplete', 'list');
    search.setAttribute('aria-expanded', 'false'); search.setAttribute('aria-label', item.title + '：' + (index === 0 && item.rule ? '计算指标' : '关联指标 ' + (index + 1)) + '，搜索全部授权指标');
    const menu = element('div', 'logic-binding-menu'); menu.hidden = true;
    const list = element('div', 'logic-binding-options'); list.id = search.id + '-options'; list.setAttribute('role', 'listbox');
    search.setAttribute('aria-controls', list.id);
    const feedback = element('div', 'logic-binding-feedback'); feedback.setAttribute('aria-live', 'polite');
    const more = registerControl(element('button', 'logic-binding-more', '更多指标'), item, 'more'); more.type = 'button'; more.hidden = true;
    menu.append(list, feedback, more);
    const meta = element('p', 'logic-binding-meta', selection.selected ? bindingMeta(selection.selected) : '请先选择指标');
    meta.title = meta.textContent; meta.textContent = sourceCaliber(selection.selected);
    search.title = meta.title;
    const session = {timer: null, controller: null, token: 0, rows: [], active: -1, next: null, epoch: reviewEpoch, list, loaded: null, quiet: false}; searchSessions.push(session);
    field.cancelBindingSearch = () => { clearTimeout(session.timer); session.controller?.abort(); session.token++; searchSessions = searchSessions.filter(value => value !== session); };
    const options = () => [null, ...session.rows];
    function closeMenu() { menu.hidden = true; search.setAttribute('aria-expanded', 'false'); search.removeAttribute?.('aria-activedescendant'); }
    function showMenu() { menu.hidden = false; search.setAttribute('aria-expanded', 'true'); }
    function activate(index) {
      session.active = index;
      [...list.children].forEach((option, position) => option.setAttribute('aria-selected', String(position === index)));
      if (index >= 0) { search.setAttribute('aria-activedescendant', list.children[index].id); list.children[index].scrollIntoView?.({block: 'nearest'}); }
    }
    function choose(row) {
      if (busy || !managed?.can_write || session.epoch !== reviewEpoch || !bindingSlots(edit).includes(selection)) return;
      if (row && bindingSlots(edit).some(slot => slot !== selection && slot.selected?.series_id === row.series_id)) { feedback.textContent = '该指标已在本条绑定，请选择其他指标。'; return; }
      if (selection === edit && selection.selected?.series_id !== row?.series_id) { delete edit.confirmationDraft; delete edit.frequencyRequired; }
      selection.selected = row; selection.query = ''; edit.bindingChanged = true; search.value = row ? display.text(row.name) : ''; meta.title = row ? bindingMeta(row) : '暂无，暂不关联';
      meta.textContent = row ? sourceCaliber(row) : '暂无'; search.title = meta.title;
      if (!hasBinding(edit)) { edit.remember = false; rememberedChoices.delete(item.id); }
      changed(edit); closeMenu(); return updateReview(item, edit, hasBinding(edit) ? 'confirm' : 'keep_text');
    }
    function optionNode(row, index) {
      const option = element('button', 'logic-binding-option'); option.type = 'button'; option.id = list.id + '-' + index;
      option.setAttribute('role', 'option'); option.setAttribute('aria-selected', 'false'); option.tabIndex = -1;
      const name = element('span', '', row ? display.text(row.name) : '暂无，暂不关联');
      option.title = row ? bindingMeta(row) : '保留文字，暂不关联指标'; option.append(name);
      if (row) option.append(element('small', '', bindingMeta(row)));
      option.addEventListener('click', () => choose(row)); return option;
    }
    function drawOptions() {
      list.replaceChildren(...options().map(optionNode));
      session.active = -1; more.hidden = session.next === null || session.next === undefined;
    }
    // Later pages are appended under the rows already shown, so an option being clicked is never replaced.
    function appendOptions(rows) {
      const start = options().length;
      session.rows = [...session.rows, ...rows]; list.append(...rows.map((row, offset) => optionNode(row, start + offset)));
      more.hidden = session.next === null || session.next === undefined;
    }
    async function load(append = false) {
      clearTimeout(session.timer); session.controller?.abort(); session.controller = new AbortController();
      const token = ++session.token, draftId = draft?.draft_id, wanted = selection.query || '';
      if (!draftId) return;
      showMenu(); if (!append) drawOptions(); feedback.textContent = '正在搜索授权目录…'; more.disabled = true;
      try {
        const query = new URLSearchParams({draft_id: draftId, search: wanted, offset: String(append ? session.next : 0), limit: '20'});
        const result = await api('/review/indicators?' + query, undefined, 'GET', session.controller.signal);
        if (token !== session.token || session.epoch !== reviewEpoch || draft?.draft_id !== draftId) return;
        const rows = result.items || [];
        session.next = result.next_offset; session.loaded = wanted;
        if (append) appendOptions(rows.filter(row => !session.rows.some(existing => existing.series_id === row.series_id)));
        else { session.rows = rows; drawOptions(); }
        feedback.textContent = session.rows.length ? `已显示 ${session.rows.length} / ${result.total} 个匹配指标` : '没有匹配指标，可调整搜索或保留文字。';
      } catch (error) {
        if (error.name !== 'AbortError' && token === session.token && session.epoch === reviewEpoch) feedback.textContent = '指标目录暂时不可用，请稍后重试。';
      } finally { if (token === session.token) more.disabled = false; }
    }
    function openList() {
      // Reopen what was already loaded for this query (including later pages) instead of starting over.
      if (session.loaded === (selection.query || '') && session.rows.length) { showMenu(); return; }
      return load();
    }
    search.addEventListener('focus', () => { if (!session.quiet) return openList(); });
    // Focus may already be here (kept after saving this item); a click still opens the list.
    search.addEventListener('click', () => { if (menu.hidden) return openList(); });
    search.addEventListener('input', () => {
      selection.query = search.value; selection.selected = null; edit.errorFields = [...new Set([...(edit.errorFields || item.attention_fields || []), 'binding'])]; edit.errorMessage = '请选择搜索结果中的指标。'; changed(edit);
      session.token++; session.controller?.abort(); clearTimeout(session.timer);
      session.rows = []; session.loaded = null; drawOptions(); more.hidden = true;
      meta.textContent = '请选择搜索结果中的指标'; showMenu(); feedback.textContent = '等待搜索…';
      session.timer = setTimeout(() => load(), 180);
    });
    search.addEventListener('keydown', event => {
      if (event.key === 'Escape') { closeMenu(); event.preventDefault(); }
      else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        if (menu.hidden && session.loaded !== (selection.query || '')) return openList();
        showMenu();
        const length = options().length;
        activate(session.active < 0 ? (event.key === 'ArrowDown' ? 0 : length - 1) :
          (session.active + (event.key === 'ArrowDown' ? 1 : -1) + length) % length);
      } else if (event.key === 'Enter' && !menu.hidden) {
        event.preventDefault(); if (session.active >= 0) return choose(options()[session.active]);
      } else if (event.key === 'Tab') closeMenu();
    });
    search.addEventListener('blur', event => { if (!menu.contains(event.relatedTarget)) session.timer = setTimeout(closeMenu, 120); });
    // Any press inside the open list (options, 更多指标, its scrollbar or gaps) keeps focus in the search box.
    menu.addEventListener('mousedown', event => event.preventDefault());
    more.addEventListener('click', () => { if (session.next !== null && session.next !== undefined) return load(true); });
    field.focusSearch = () => { session.quiet = true; try { search.focus({preventScroll: true}); } finally { session.quiet = false; } };
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
      if (section.id === 'unresolved') navButton.title = '未归入结构化条目的原文';
      navButton.addEventListener('click', () => selectSection(section.id)); nav.append(navButton);
      const group = element('section', 'logic-review-group'); group.dataset.reviewSection = section.id;
      const heading = element('h3', '', section.title);
      if (section.id === 'unresolved') heading.title = '未归入结构化条目的原文';
      group.append(heading);
      for (const item of sectionItems) {
        let edit = reviewEdits.get(item.id);
        if (!edit) { edit = {selected: Object.hasOwn(item, 'binding') ? item.binding : item.ai_binding || null, additional: (item.related_bindings || []).map(selected => ({selected, query: ''})), remember: rememberedChoices.has(item.id), dirty: false, query: '', sourceQuote: null, bindingChanged: false}; reviewEdits.set(item.id, edit); }
        const card = element('article', 'logic-review-card'); card.dataset.reviewItem = item.id; card.dataset.reviewStatus = item.status;
        card.append(element('strong', 'logic-item-title', display.text(item.title)));
        const fields = new Map();
        function field(node, name) {
          node.dataset.reviewField = name; node.className += ' logic-review-field';
          const error = element('p', 'logic-field-error'); error.setAttribute('role', 'status'); error.hidden = true;
          node.append(error); fields.set(name, {node, error}); return node;
        }
        const note = element('div', 'logic-item-description');
        note.append(element('p', '', (display.prose || display.text)(item.description || ''))); field(note, 'content'); card.append(note);
        if (item.rule) {
          const condition = element('div', 'logic-item-condition'); condition.dataset.reviewItem = item.id; condition.dataset.reviewControl = 'condition';
          const value = element('span'); condition.append(value); field(condition, 'condition');
          edit.renderCondition = () => { value.textContent = ruleCondition(item, edit); condition.dataset.conditionText = value.textContent; };
          edit.renderCondition(); card.append(condition);
        }
        const columns = element('div', 'logic-item-columns'), original = element('div', 'logic-item-original');
        const toolbar = element('div', 'logic-item-toolbar');
        let rememberLabel = null;
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
          const binding = renderBindings(item, edit, toolbar); field(binding, 'binding');
          rememberLabel = element('label', 'logic-remember-label');
          const remember = registerControl(element('input'), item, 'remember', () => !hasBinding(edit)); remember.type = 'checkbox'; remember.checked = edit.remember;
          remember.addEventListener('change', () => { edit.remember = remember.checked; changed(edit); return updateReview(item, edit, 'confirm'); });
          rememberLabel.append(remember, element('span', '', '记住选择')); columns.append(binding); card.append(columns);
        }
        const confirmItem = registerControl(element('button', 'logic-confirm-whole', '核对并确认整条'), item, 'confirm-item'); confirmItem.type = 'button';
        confirmItem.addEventListener('click', () => confirmWholeItem(item, edit));
        toolbar.append(confirmItem);
        if (rememberLabel) toolbar.append(rememberLabel);
        const outcome = element('span', 'logic-item-outcome'); outcome.setAttribute('role', 'status'); outcome.setAttribute('aria-live', 'polite');
        edit.renderOutcome = () => {
          outcome.textContent = edit.saving ? '正在核对…' : edit.errorMessage ? '未确认，请处理红框' : item.status === 'confirmed' && !edit.dirty ? (item.manually_confirmed ? '已人工确认（未能从原文自动核对）' : '已确认') : '';
          outcome.dataset.error = String(Boolean(edit.errorMessage) && !edit.saving); outcome.hidden = !outcome.textContent;
        };
        edit.renderOutcome(); toolbar.append(outcome);
        edit.renderConfirmation = () => { confirmItem.hidden = !needsAttention(item); toolbar.hidden = !item.can_bind && confirmItem.hidden && outcome.hidden; };
        edit.renderConfirmation(); card.append(toolbar);
        const actions = element('div', 'logic-item-actions');
        for (const [action, label] of [['keep_text', '仅保留文字'], ['exclude', '排除']]) {
          const button = registerControl(element('button', '', label), item, action); button.type = 'button'; button.addEventListener('click', () => updateReview(item, edit, action)); actions.append(button);
        }
        const adjustments = element('details', 'logic-item-adjustments'); adjustments.dataset.reviewItem = item.id; adjustments.dataset.reviewControl = 'adjust'; adjustments.open = Boolean(edit.adjusting);
        adjustments.append(element('summary', '', '调整'));
        if (!item.can_bind) adjustments.append(original);
        adjustments.append(actions); adjustments.addEventListener('toggle', () => { edit.adjusting = adjustments.open; }); card.append(adjustments);
        edit.renderErrors = () => {
          const marked = needsAttention(item) ? edit.errorFields || item.attention_fields || (item.status === 'pending' ? [item.can_bind ? 'binding' : 'content'] : []) : [];
          const reasons = item.kind === 'unresolved_binding' ? item.attention_reasons || [] : item.warnings?.length ? item.warnings : item.attention_reasons || [];
          const reason = display.text(edit.errorMessage || (typeof reasons[0] === 'string' ? reasons[0] : reasons[0]?.message) || '请修改红框位置。');
          for (const [name, entry] of fields) {
            const invalid = marked.some(value => (value === 'caliber' ? 'binding' : value) === name); entry.node.dataset.invalid = String(invalid); entry.node.setAttribute('aria-invalid', String(invalid)); entry.error.hidden = !invalid; entry.error.textContent = invalid ? reason : '';
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
  // Saving one item re-renders the review and disables its controls meanwhile. Keep the reviewer on the
  // same card and control instead of letting focus fall to the page and later cards jump into place.
  function findNode(node, test) {
    for (const child of node?.children || []) { if (test(child)) return child; const found = findNode(child, test); if (found) return found; }
    return null;
  }
  function reviewCards() {
    const cards = [];
    for (const group of byId('review-items')?.children || []) for (const node of group.children || []) if (node.dataset?.reviewItem && String(node.className).includes('logic-review-card')) cards.push(node);
    return cards;
  }
  function reviewPlace(itemId) {
    const cards = reviewCards(), start = cards.findIndex(node => node.dataset.reviewItem === itemId);
    if (start < 0) return null;
    const active = document.activeElement, own = active && cards[start].contains(active) ? active : null;
    let binding = null;
    for (let node = own; node && node !== cards[start]; node = node.parentElement) if (node.dataset?.bindingIndex !== undefined) { binding = node.dataset.bindingIndex; break; }
    return {item: itemId, order: cards.slice(start).map(node => node.dataset.reviewItem), top: cards[start].getBoundingClientRect?.().top,
      control: own?.dataset?.reviewControl || null, binding};
  }
  function keepPlace(place) {
    if (!place || typeof place.top !== 'number') return;
    const cards = new Map(reviewCards().map(node => [node.dataset.reviewItem, node]));
    const anchor = place.order.map(id => cards.get(id)).find(node => node && !node.hidden);
    const top = anchor?.getBoundingClientRect?.().top;
    if (typeof top !== 'number' || Math.abs(top - place.top) < 1) return;
    for (let box = anchor.parentElement; box; box = box.parentElement) {
      if (box.scrollHeight > box.clientHeight && /(auto|scroll)/.test(getComputedStyle(box).overflowY)) { box.scrollTop += top - place.top; return; }
    }
    document.scrollingElement?.scrollBy?.(0, top - place.top);
  }
  function restoreFocus(place) {
    if (!place?.control) return;
    const card = reviewCards().find(node => node.dataset.reviewItem === place.item);
    if (!card || card.hidden) return;
    const row = place.binding === null ? null : findNode(card, node => node.dataset?.bindingIndex === place.binding);
    if (place.control === 'search' && row?.focusSearch) return row.focusSearch();
    const control = findNode(row || card, node => node.dataset?.reviewControl === place.control);
    if (control && !control.disabled && !control.hidden && control.getClientRects?.().length) control.focus?.({preventScroll: true});
  }
  async function updateReview(item, edit, action, holdBusy = false, caliber = null) {
    if ((busy && !holdBusy) || !review || !draft || !managed?.can_write) return;
    if (action === 'confirm' && item.can_bind && pendingBindingQuery(edit)) { edit.errorFields = ['binding']; edit.errorMessage = '请选择搜索结果中的指标，或选择“暂无”。'; changed(edit); return; }
    const body = {draft_id: draft.draft_id, revision: review.revision, item_id: item.id, action, remember: action === 'confirm' && edit.remember};
    if (item.can_bind && (action === 'confirm' || action === 'keep_text')) body.series_id = action === 'keep_text' ? null : edit.selected?.series_id || null;
    if (item.can_add_binding && (action === 'confirm' || action === 'keep_text')) body.related_series_ids = action === 'keep_text' ? [] : (edit.additional || []).map(slot => slot.selected?.series_id).filter(Boolean);
    if (caliber !== null && action === 'confirm') body.caliber = caliber;
    if (edit.sourceQuote !== null) body.source_quote = edit.sourceQuote;
    const place = reviewPlace(item.id);
    edit.saving = true; edit.renderOutcome?.(); setBusy(true); inform('正在同步修改…');
    try {
      let result;
      for (let attempt = 0; attempt < 3; attempt++) {
        try { result = await api('/review/update', body); break; }
        catch (error) {
          if (action !== 'confirm' || attempt === 2) throw error;
          if (error.bindingWarningToken && body.series_id && !Object.hasOwn(body, 'binding_warning_token')) {
            if (!window.confirm(error.message)) {
              reviewEdits.delete(item.id); renderReview(); inform('已取消绑定。'); return;
            }
            body.binding_warning_token = error.bindingWarningToken;
          } else if (error.numberReviewToken && !Object.hasOwn(body, 'number_review_token')) {
            if (!window.confirm(error.message)) {
              edit.errorMessage = '未确认数值，本条保持待核对。'; edit.errorFields = ['condition'];
              edit.renderErrors?.(); inform('未确认数值，本条保持待核对。', true); return false;
            }
            body.number_review_token = error.numberReviewToken;
          } else throw error;
        }
      }
      if (body.remember) rememberedChoices.add(item.id); else rememberedChoices.delete(item.id);
      reviewEdits.delete(item.id); presentDraft(result, true); keepPlace(place);
      inform(action === 'confirm' ? '本条已确认。' : action === 'keep_text' ? '本条已保留为文字。' : '本条已排除。');
      return true;
    } catch (error) {
      if (error.status === 409) { invalidate(); inform(error.message, true); }
      else if (error.name !== 'AbortError') { edit.dirty = true; edit.errorMessage = error.message; if (typeof error.frequencyRequired === 'boolean') edit.frequencyRequired = error.frequencyRequired; edit.errorFields = Array.isArray(error.fields) ? error.fields : item.attention_fields?.length ? item.attention_fields : [item.can_bind ? 'binding' : 'content']; edit.renderErrors?.(); edit.renderConfirmation?.(); inform('本条未确认：' + error.message, true); }
      return false;
    } finally { edit.saving = false; edit.renderOutcome?.(); if (!holdBusy) { setBusy(false); restoreFocus(place); } }
  }
  async function open(requestedJob) {
    if (busy) return;
    controller = new AbortController();
    if (!dialog.open) dialog.showModal();
    if (parent !== window) parent.postMessage({type: 'research-drawer-state', open: true}, location.origin);
    inform('正在读取当前逻辑与版本…');
    try {
      managed = await api(managePath()); showVersion(managed.current);
      byId('variety').value = managed.current || managed.latest ? managed.variety || '' : '';
      text('version-label', `${managed.current ? display.reference(managed.current) : '暂无当前逻辑'}。后续保存仍为草稿，原记录保留。`);
      historical = null; historyEpoch++;
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
      await discoverLatestResult();
      if (dialog.dataset.prefill === 'true') { tab('analyze'); dialog.dataset.prefill = 'false'; }
      const selectedJob = jobId(requestedJob) || (['queued', 'running'].includes(activeJob?.state) ? activeJob.id : null);
      if (selectedJob) { tab('analyze'); await followJob(selectedJob); }
      else if (submittingJob) { setBusy(true); inform('正在提交后台分析，可关闭窗口。'); }
    } catch (error) { inform(error.message, true); }
  }
  function close() {
    closeItemConfirmation();
    historyEpoch++; historyPending = false;
    stopJobMonitor();
    controller?.abort(); stopSearches(); setBusy(false); dialog.close();
    if (byId('restore-result')) byId('restore-result').hidden = true;
    if (byId('discard-result')) byId('discard-result').hidden = true;
    if (parent !== window) parent.postMessage({type: 'research-drawer-state', open: false}, location.origin);
  }
  // Discard the result under review, whether it came from a background analysis or an import.
  byId('review-discard')?.addEventListener('click', async () => {
    if (busy || !draft?.draft_id || managed?.can_write === false) return;
    if (!window.confirm('舍弃后，这份尚未保存的研判结果及其中已做的指标选择会被删除，不能恢复；已保存的研判不受影响。确定舍弃吗？')) return;
    const token = draft.draft_id, job = jobId(activeJob?.id) ? activeJob.id : null;
    setBusy(true); inform('正在舍弃…');
    try {
      await api('/preview/discard', {draft_id: token});
      if (latestResultJob && (latestResultJob.id === job || token.startsWith('job.' + latestResultJob.id + '.'))) {
        latestResultJob = null; if (byId('restore-result')) byId('restore-result').hidden = true;
      }
      invalidate(); renderReview(); setBusy(false); inform('已舍弃这份尚未保存的研判结果。');
    } catch (error) { setBusy(false); inform(error.message, true); }
    finally { syncDiscard(); }
  });
  byId('discard-result')?.addEventListener('click', async () => {
    const job = discardableJob();
    if (busy || !job) return;
    if (!window.confirm('舍弃后，这份暂存结果及其中已做的指标选择会被删除，不能恢复；已保存的研判不受影响。确定舍弃吗？')) return;
    const viewing = Boolean(draft) && activeJob?.id === job.id;
    setBusy(true); inform('正在舍弃暂存结果…');
    try {
      await api('/jobs/' + encodeURIComponent(job.id) + '/discard', {});
      if (latestResultJob?.id === job.id) { latestResultJob = null; if (byId('restore-result')) byId('restore-result').hidden = true; }
      if (viewing) { invalidate(); renderReview(); }
      setBusy(false); inform('已舍弃暂存结果。');
    } catch (error) { setBusy(false); inform(error.message, true); }
    finally { syncDiscard(); }
  });
  byId('restore-result')?.addEventListener('click', async () => {
    if (busy || !managed?.can_write || !latestResultJob) return;
    try { checkJob(latestResultJob, true); } catch (error) { return inform(error.message, true); }
    if ((input.value.trim() || draft || hasReviewEdits()) && !window.confirm('查看最新暂存结果将替换当前原文和未保存草稿（包括指标选择）。是否继续？')) return;
    tab('analyze'); await followJob(latestResultJob.id);
  });
  async function recover(error) {
    if (error.name === 'AbortError') return;
    if (error.status === 409) {
      invalidate();
      try { const refreshed = await api(managePath()); if (managed) managed.latest = refreshed.latest; } catch {}
      inform(error.message + ' 原文已保留，可重新分析。', true);
    } else inform(error.message, true);
  }
  byId('generate').addEventListener('click', async () => {
    if (busy || submittingJob || !managed?.can_write) return;
    if (!input.value.trim() || !byId('model').value) return inform('请添加完整原文并选择模型。', true);
    const variety = byId('variety').value.trim();
    if (!variety) return inform('请按研究原文填写品种代码。', true);
    if (new TextEncoder().encode(input.value).length > 48 * 1024) return inform('原文超过48 KiB，请缩小范围。', true);
    const submittedInput = {model_id: byId('model').value, text: input.value,
      source_name: byId('file-name').textContent.slice(0, 128), variety, ...panelTitle()};
    invalidate(); const submitted = submissionSerial; submittingJob = true;
    controller = new AbortController(); setBusy(true); inform('正在提交后台分析…');
    try {
      const baseline = await api(variety !== managed.variety ? '/manage?variety=' + encodeURIComponent(variety) : managePath(), undefined, 'GET', null);
      const body = {...submittedInput, base_version: baseline.latest?.version || 0}, signature = JSON.stringify(body);
      const key = uncertainSubmission?.signature === signature ? uncertainSubmission.key : requestKey();
      uncertainSubmission = {signature, key};
      const value = await api('/jobs', body, 'POST', null, {'Idempotency-Key': key}, true);
      uncertainSubmission = null;
      const identifier = jobId(value.id || value.job_id);
      if (!identifier) throw new Error('任务返回无效，请从右下角查看分析记录。');
      announceJob({...value, id: identifier});
      if (submitted !== submissionSerial) return;
      activeJob = {...value, id: identifier};
      if (dialog.open) await followJob(identifier);
    } catch (error) { if (error.status) uncertainSubmission = null; await recover(error); }
    finally {
      submittingJob = false;
      if (!activeJob || !['queued', 'running'].includes(activeJob.state) || !dialog.open) setBusy(false);
    }
  });
  byId('apply').addEventListener('click', async () => {
    if (busy || !draft || !managed?.can_write) return;
    if (hasReviewEdits()) return inform('请先确认、保留文字或排除已修改的条目，再保存草稿。', true);
    const selected = draft; controller = new AbortController(); setBusy(true); inform('正在确认来源与版本并保存草稿…');
    try {
      const result = await api(selected.kind === 'structured' ? '/structured/apply' : '/apply', {draft_id: selected.draft_id, base_version: selected.base_version,
        ...(selected.kind !== 'structured' && review ? {review_revision: review.revision} : {})});
      if (activeJob && result.id) announceJob(activeJob, result.id);
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
    reuseMarkdown(displayed);
  });
  byId('reuse-original').addEventListener('click', () => {
    reuseMarkdown(displayed, true);
  });
  byId('download').addEventListener('click', () => {
    exportMarkdown(displayed);
  });
  async function loadVersion(value) {
    const entry = historyEntries.find(row => row.key === value);
    if (!entry || busy) return;
    const epoch = ++historyEpoch; historyPending = true; historical = null; syncManagement();
    text('history-state', '正在读取历史研判…');
    const sameVariety = entry.variety === managed.variety;
    const identifier = sameVariety && selectedId() ? selectedId() : entry.record_id;
    const query = new URLSearchParams(identifier ? {judgment_id: String(identifier)} : {variety: entry.variety || managed.variety});
    try {
      const result = await api('/versions/' + encodeURIComponent(entry.version) + '?' + query);
      if (epoch !== historyEpoch || !dialog.open) return;
      if (entry.record_id && String(result.id) !== String(entry.record_id) || result.version !== entry.version)
        throw new Error('历史研判已变化，请重新选择');
      historical = result; text('history-md', result.markdown);
      text('history-diff', sameVariety ? result.diff?.markdown || '与当前研判内容一致。' : '不同品种的研判不作版本差异比较。');
      text('history-state', `${entry.title || display.reference(result)} · ${result.status || '草稿'}`); inform('');
    } catch (error) { if (epoch === historyEpoch && error.name !== 'AbortError') { text('history-state', '读取未完成'); inform(error.message, true); } }
    finally { if (epoch === historyEpoch) { historyPending = false; syncManagement(); } }
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
        const suggestions = review.sections.flatMap(section => section.items || []).filter(item => item.remembered && item.status === 'pending' && item.can_bind && (item.binding?.series_id || item.related_bindings?.length));
        for (const item of suggestions) { const edit = reviewEdits.get(item.id); edit.selected = item.binding; edit.additional = (item.related_bindings || []).map(selected => ({selected, query: ''})); changed(edit); }
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
  byId('current-delete')?.addEventListener('click', () => removeVersion(displayed));
  byId('history-delete')?.addEventListener('click', () => removeVersion(historical));
  byId('history-download')?.addEventListener('click', () => { if (!busy && !historyPending) exportMarkdown(historical); });
  byId('history-reuse')?.addEventListener('click', () => { if (!historyPending) reuseMarkdown(historical); });
  byId('history-more')?.addEventListener('click', async () => {
    if (busy) return; setBusy(true);
    try { await loadPanels(true); } catch (error) { inform(error.message, true); } finally { setBusy(false); }
  });
  dialog.querySelectorAll('[data-logic-tab]').forEach(button => button.addEventListener('click', () => {
    tab(button.dataset.logicTab); if (button.dataset.logicTab === 'versions') loadVersion(byId('version').value);
  }));
  dialog.querySelector('[data-logic-close]').addEventListener('click', close);
  dialog.addEventListener('cancel', event => { event.preventDefault(); close(); });
  document.querySelectorAll('[data-open-logic]').forEach(button => button.addEventListener('click', open));
  window.JudgmentManager = {open, close, openJob: identifier => open(jobId(identifier))};
  const requestedJob = jobId(new URL(location.href).searchParams.get('analysis_job'));
  if (requestedJob) open(requestedJob);
  else if (dialog.dataset.prefill === 'true') open();
})();
