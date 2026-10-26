/* PDF import and explicit review adapter for the authenticated structured workspace. */
(() => {
  'use strict';

  const host = window.pdfReviewHost;
  const root = document.getElementById('pdfReviewApp');
  if (!host || !root) return;

  let epoch = host.getAuthEpoch();
  let scopeController = new AbortController();
  let operation = 0;
  let config = {configured: false, max_bytes: 50 * 1024 * 1024};
  let drafts = [];
  let tables = [];
  let jobs = [];
  let draft = null;
  let selectedFragments = new Set();
  let preview = null;
  let previewSignature = '';
  let sourceUrl = '';
  let renderedEpoch = -1;
  let requestedSourceKnowledge = '';
  let requestedSourceEpoch = -1;
  let sourceBusy = false;
  let activeFragmentIndex = 0;

  const make = (tag, text, className) => {
    const node = document.createElement(tag);
    if (text !== undefined && text !== null) node.textContent = String(text);
    if (className) node.className = className;
    return node;
  };
  const action = (label, handler, secondary = false) => {
    const button = make('button', label, secondary ? 'secondary' : '');
    button.type = 'button';
    button.addEventListener('click', handler);
    return button;
  };
  const current = (savedEpoch, savedOperation = operation) =>
    savedEpoch === host.getAuthEpoch() && savedEpoch === epoch && savedOperation === operation && !scopeController.signal.aborted;
  const api = (path, options = {}, timeoutMs = 30000) =>
    host.api(path, options, {signal: scopeController.signal, timeoutMs});
  const source = bundle => bundle?.source || {};
  const fragments = () => draft?.bundle?.fragments || [];

  function setStatus(message, error = false) {
    const status = document.getElementById('pdfReviewStatus');
    host.onUploadStatus?.(message || '', error);
    if (!status) return;
    status.textContent = message || '';
    status.classList.toggle('error', Boolean(error));
    status.setAttribute('role', error ? 'alert' : 'status');
  }

  function invalidatePreview(message = '计划已修改，请重新预览后才能发布。') {
    preview = null;
    previewSignature = '';
    const pane = document.getElementById('pdfPreview');
    if (pane) pane.replaceChildren(make('p', message, 'muted'));
    const checkbox = document.getElementById('pdfPublishReviewed');
    if (checkbox) checkbox.checked = false;
    const publish = document.getElementById('pdfPublish');
    if (publish) publish.disabled = true;
  }

  function revokeSource() {
    if (sourceUrl) URL.revokeObjectURL(sourceUrl);
    sourceUrl = '';
  }

  function clearScope(message = '账号或知识库已切换，请重新加载 PDF 草稿。') {
    operation += 1;
    root.inert = false;
    scopeController.abort();
    scopeController = new AbortController();
    requestedSourceKnowledge = '';
    requestedSourceEpoch = -1;
    sourceBusy = false;
    revokeSource();
    config = {configured: false, max_bytes: 50 * 1024 * 1024};
    drafts = [];
    tables = [];
    jobs = [];
    draft = null;
    selectedFragments = new Set();
    preview = null;
    previewSignature = '';
    renderedEpoch = -1;
    root.replaceChildren(make('p', message, 'muted'));
  }

  function jsonValue(textarea) {
    try {
      const value = JSON.parse(textarea.value);
      if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error();
      return value;
    } catch {
      throw new Error('计划必须是有效的 JSON 对象。');
    }
  }

  function renderPlanPreview(result) {
    const pane = document.getElementById('pdfPreview');
    if (!pane) return;
    pane.replaceChildren();
    const publication = document.getElementById('pdfPublicationSection');
    if (publication) publication.hidden = false;
    const coverage = result.coverage || {};
    const note = make('p', `预览行数：${coverage.logical_row_count ?? result.rows?.length ?? 0}；原始片段：${coverage.selected_fragment_count ?? '—'}；原件覆盖：${coverage.source_document_complete === true ? '已确认' : '尚未确认'}。OCR/解析结果尚未独立验证。`);
    pane.append(note);
    if (coverage.extraction_verified === false || result.extraction_verified === false) {
      pane.append(make('p', '此预览只验证映射和来源覆盖，不代表 OCR 或原件识别已核实；公式不会作为可执行公式。', 'warning'));
    }
    const unresolved = coverage.unresolved_quality_issues || [];
    if (coverage.join_mode === 'horizontal') {
      pane.append(make('p', coverage.row_alignment_verified
        ? '横向续列表：按每页实际标识核对行对应；共有标识保留全部来源。'
        : '横向续列表缺少各页共有行标识。行号映射不能自动证明对应正确，发布前必须对照原件逐行确认。', 'warning'));
    }
    if (unresolved.length) {
      const box = make('fieldset');
      box.append(make('legend', `待处理质量问题 · ${unresolved.length}`));
      box.append(make('p', '已知问题不能通过最后的发布勾选绕过。请填写具体原页证据，修正不确定单元格，然后重新预览。', 'warning'));
      for (const [index, issue] of unresolved.entries()) {
        const item = make('div');
        item.append(make('p', `${issue.fragment_id || ''} ${issue.row !== undefined ? `行 ${issue.row} 列 ${issue.column}` : ''} · ${issue.code || 'quality'} · ${issue.message || ''}`));
        const reason = make('input');
        reason.type = 'text'; reason.placeholder = '核对的页码、原文及处理依据（必填）';
        reason.setAttribute('aria-label', `质量问题 ${index + 1} 核对依据`);
        item.append(reason);
        let corrected;
        if (issue.code === 'uncertain_cell') {
          corrected = make('input'); corrected.type = 'text'; corrected.placeholder = '原件真实单元格文本（原件空白可留空）';
          corrected.setAttribute('aria-label', `质量问题 ${index + 1} 原件文本`);
          item.append(corrected);
        }
        item.append(action('记录此项人工处理', () => {
          try {
            if (!reason.value.trim()) throw new Error('请填写此项的具体原件证据。');
            const editor = document.getElementById('pdfPlanEditor');
            const plan = jsonValue(editor);
            if (issue.code === 'row_alignment_unverified') {
              plan.row_alignment_review = {reason: reason.value.trim()};
            } else if (issue.code === 'uncertain_cell') {
              plan.cell_corrections = (plan.cell_corrections || []).filter(c => !(c.fragment_id === issue.fragment_id && c.row === issue.row && c.column === issue.column));
              plan.cell_corrections.push({fragment_id: issue.fragment_id, row: issue.row, column: issue.column, text: corrected.value, reason: reason.value.trim()});
            } else {
              plan.quality_reviews = (plan.quality_reviews || []).filter(r => !(r.fragment_id === issue.fragment_id && r.issue_index === issue.issue_index));
              plan.quality_reviews.push({fragment_id: issue.fragment_id, issue_index: issue.issue_index, reason: reason.value.trim()});
            }
            editor.value = JSON.stringify(plan, null, 2);
            editor.dispatchEvent(new Event('input', {bubbles: true}));
            setStatus('已记录此项核对依据，请重新预览查看剩余问题；尚未发布。');
          } catch (error) { setStatus(error.message, true); }
        }, true));
        box.append(item);
      }
      pane.append(box);
    }
    const columns = result.columns || [];
    const rows = result.rows || [];
    if (!columns.length || !rows.length) {
      pane.append(make('p', '预览没有可显示的数据行。', 'muted'));
      return;
    }
    const wrap = make('div', undefined, 'scroll');
    const table = make('table');
    const thead = make('thead');
    const headRow = make('tr');
    for (const column of columns) headRow.append(make('th', column.name));
    headRow.append(make('th', '原件单元格来源'));
    thead.append(headRow);
    const tbody = make('tbody');
    for (const row of rows.slice(0, 100)) {
      const tr = make('tr');
      for (const column of columns) {
        const td = make('td', row.values?.[column.name] ?? '');
        const refs = row.source_cells?.[column.name] || [];
        for (const ref of refs) {
          const detail = make('small', `第 ${ref.page} 页 · 行 ${ref.row} · 列 ${ref.column} · 原文「${ref.text ?? ''}」`);
          td.append(document.createElement('br'), detail);
          if (ref.correction) td.append(make('small', `人工修正为「${ref.correction.text}」；依据：${ref.correction.reason}`));
        }
        tr.append(td);
      }
      const provenance = make('td');
      const refs = Object.values(row.source_cells || {}).flat();
      provenance.textContent = refs.map(ref => `P${ref.page} R${ref.row} C${ref.column}`).join('；') || '来源缺失';
      tr.append(provenance);
      tbody.append(tr);
    }
    table.append(thead, tbody);
    wrap.append(table);
    pane.append(wrap);
    if (rows.length > 100) pane.append(make('p', `仅显示前 100 行；共 ${rows.length} 行。`, 'muted'));
    const raw = make('details');
    raw.append(make('summary', '查看完整预览 JSON'));
    const pre = make('pre');
    pre.textContent = JSON.stringify(result, null, 2);
    raw.append(pre);
    pane.append(raw);
  }

  const fileIdentity = item => /^[a-f0-9]{64}$/i.test(item.source?.sha256 || '')
    ? 'sha:' + item.source.sha256.toLowerCase() : 'record:' + (item.draft_id || item.table_id);
  const dateLabel = value => value && !Number.isNaN(Date.parse(value))
    ? new Date(value).toLocaleString('zh-CN', {hour12: false}) : '';

  function fileGroups() {
    const groups = new Map();
    for (const item of drafts) {
      const key = fileIdentity(item);
      if (!groups.has(key)) groups.set(key, {source: item.source || {}, versions: [], published: []});
      groups.get(key).versions.push(item);
    }
    for (const item of tables) {
      const key = fileIdentity(item);
      if (!groups.has(key)) groups.set(key, {source: item.source || {}, versions: [], published: []});
      groups.get(key).published.push(item);
    }
    for (const group of groups.values()) {
      group.versions.sort((a,b) => (Date.parse(b.created_at) || 0) - (Date.parse(a.created_at) || 0));
      if (group.versions.length) group.source = group.versions[0].source || group.source;
    }
    return [...groups.values()].sort((a,b) =>
      (Date.parse(b.versions[0]?.created_at) || 0) - (Date.parse(a.versions[0]?.created_at) || 0));
  }

  function renderPublishedTables() { renderDrafts(); }

  function showFileList() {
    operation += 1;
    draft = null;
    preview = null;
    previewSignature = '';
    selectedFragments = new Set();
    revokeSource();
    root.classList.remove('reviewing-file');
    document.getElementById('pdfDraftDetail')?.replaceChildren();
    setStatus('');
    renderDrafts();
  }

  function renderJobs() {
    const target = document.getElementById('pdfJobs');
    if (!target) return;
    target.replaceChildren();
    for (const job of jobs) {
      const row = make('div', undefined, 'row');
      const statuses = {queued:'排队中',pending:'等待处理',processing:'处理中',running:'处理中',completed:'识别完成',failed:'处理失败',cancelled:'已取消'};
      row.classList.add('processed-file');
      row.append(make('span', `${job.file_name || 'PDF'} · ${statuses[job.status] || job.status} · ${job.error || job.stage || ''}`));
      if (job.retry_available && ['failed', 'cancelled'].includes(job.status)) {
        const retry = action('手动重试此文件', async () => {
          retry.disabled = true;
          const savedEpoch = epoch, savedOperation = ++operation;
          try {
            setStatus('正在重新校验原件与权限；只重试这一份文件，已完成页面将按配置复用。');
            const result = await api(`/api/pdf/jobs/${encodeURIComponent(job.id)}/retry`, {method: 'POST'}, 120000);
            if (!current(savedEpoch, savedOperation)) return;
            await pollJob(result.id, savedEpoch, savedOperation);
            if (savedEpoch === epoch && savedEpoch === host.getAuthEpoch()) await loadJobs(savedEpoch, operation);
          } catch (error) {
            if (current(savedEpoch, savedOperation)) setStatus(error.message, true);
          } finally {
            if (current(savedEpoch, savedOperation)) retry.disabled = false;
          }
        }, true);
        row.append(retry);
      }
      target.append(row);
    }
  }

  async function loadJobs(savedEpoch = epoch, savedOperation = operation) {
    const result = await api('/api/pdf/jobs');
    if (!current(savedEpoch, savedOperation)) return;
    jobs = result.jobs || [];
    renderJobs();
  }

  function recordDetails(id) {
    const details = make('details', undefined, 'record-details');
    details.append(make('summary', '记录信息'), make('code', id || ''));
    return details;
  }

  function renderDrafts() {
    const list = document.getElementById('pdfDraftList');
    if (!list) return;
    list.replaceChildren();
    const groups = fileGroups();
    host.onFileCatalog?.(groups.length);
    const count = document.getElementById('pdfFileCount');
    if (count) count.textContent = `${groups.length} 个文件`;
    if (!groups.length) {
      list.append(make('div', '暂无 PDF 文件', 'workbook-empty'));
      return;
    }
    const table = make('table', undefined, 'pdf-file-table');
    const head = make('thead'), headRow = make('tr');
    for (const title of ['文件', '识别结果', '已发布', '']) headRow.append(make('th', title));
    head.append(headRow);
    const body = make('tbody');
    for (const group of groups) {
      const latest = group.versions[0];
      const row = make('tr');
      row.dataset.fileIdentity = fileIdentity(latest || group.published[0]);
      const name = make('td'), nameInfo = make('div', undefined, 'pdf-file-name');
      nameInfo.append(make('strong', group.source.file_name || 'PDF 文件'));
      if (latest?.created_at) nameInfo.append(make('small', `最近识别 ${dateLabel(latest.created_at)}`));
      name.append(nameInfo);
      const result = make('td', latest ? `${latest.fragment_count || 0} 处表格` : '已保存发布结果');
      const published = make('td');
      published.append(make('span', group.published.length ? `${group.published.length} 张表格` : '尚未发布', group.published.length ? 'pdf-published-state' : 'muted'));
      const controls = make('td', undefined, 'pdf-file-actions');
      if (latest) controls.append(action('核对表格', () => selectDraft(latest.draft_id), true));
      const history = make('details', undefined, 'pdf-file-history');
      history.append(make('summary', `历史与发布记录（${group.versions.length} 次识别）`));
      for (const item of group.versions) {
        const entry = make('div', undefined, 'pdf-history-entry');
        const publishedCount = group.published.filter(t => t.draft_id === item.draft_id).length;
        entry.append(make('span', `${dateLabel(item.created_at) || '识别记录'} · ${item.fragment_count || 0} 处表格${publishedCount ? ` · 已发布 ${publishedCount} 张` : ''}`), action('打开此版本', () => selectDraft(item.draft_id), true), recordDetails(item.draft_id));
        history.append(entry);
      }
      for (const item of group.published) {
        const entry = make('div', undefined, 'pdf-history-entry');
        entry.append(make('span', `${item.title || '已发布表格'} · ${item.columns?.length || 0} 列 · ${item.coverage?.logical_row_count || 0} 行`), make('small', item.extraction_verified ? '已独立验证提取' : '提取未独立验证'), recordDetails(item.table_id));
        history.append(entry);
      }
      name.append(history);
      row.append(name,result,published,controls);
      body.append(row);
    }
    table.append(head,body);
    const wrap = make('div', undefined, 'pdf-file-list-scroll');
    wrap.append(table);
    list.append(wrap);
  }

  function createManualPlan() {
    const selected = fragments().filter(fragment => selectedFragments.has(fragment.id));
    if (!selected.length) throw new Error('请先选择至少一个原始表片段。');
    const widths = selected.map(fragment => Math.max(0, ...(fragment.rows || []).map(row => row.length)));
    if (!widths.length || widths.some(width => width !== widths[0]) || widths[0] < 1) {
      throw new Error('所选片段列数不一致，不能生成无损手动模板；请调整片段后再编写计划。');
    }
    return {
      bundle_sha256: draft.bundle_sha256,
      fragment_ids: selected.map(fragment => fragment.id),
      header_rows: Object.fromEntries(selected.map(fragment => [fragment.id, []])),
      columns: Array.from({length: widths[0]}, (_, index) => ({
        name: `Column ${index + 1}`,
        type: 'text',
        source_columns: Object.fromEntries(selected.map(fragment => [fragment.id, null]))
      })),
      excluded_rows: [],
      boundary_merges: []
    };
  }

  function renderFragment(fragment, parent) {
    const rows = fragment.rows || [];
    const quality = fragment.quality;
    if (quality?.status === 'needs_review' || quality?.issues?.length) {
      const issue = make('details', undefined, 'pdf-quality-detail');
      issue.append(make('summary', '此表有识别疑点，展开核对'), make('p', (quality.issues || []).map(i=>i.message || i.code).join('；'), 'muted'));
      parent.append(issue);
    }
    const wrap = make('div', undefined, 'pdf-raw-table scroll');
    const table = make('table'), head = make('thead'), header = make('tr');
    header.append(make('th', '行'));
    const width = Math.max(0, ...rows.map(row=>row.length));
    for (let i=0;i<width;i++) header.append(make('th', `列 ${i+1}`));
    head.append(header);
    const body = make('tbody');
    for (const [rowIndex,row] of rows.entries()) {
      const tr = make('tr');
      tr.append(make('th', rowIndex+1));
      for (const cell of row) {
        const td = make('td');
        td.append(make('span', cell.text, 'pdf-raw-cell'));
        if (cell.rowspan > 1 || cell.colspan > 1) td.title = `合并 ${cell.rowspan}×${cell.colspan}`;
        tr.append(td);
      }
      body.append(tr);
    }
    table.append(head,body);wrap.append(table);parent.append(wrap);
  }

  function renderFragmentInspector(parent, viewer) {
    const items = fragments();
    parent.replaceChildren();
    if (!items.length) { parent.append(make('p','未识别到表格','muted')); return; }
    activeFragmentIndex = Math.min(activeFragmentIndex, items.length-1);
    const toolbar = make('div', undefined, 'pdf-fragment-toolbar');
    const picker = make('select');picker.id='pdfFragmentSelect';picker.setAttribute('aria-label','选择识别表格');
    for (const [i,item] of items.entries()) {
      const option = make('option', `表格 ${i+1} · 第 ${item.page} 页`); option.value=String(i);picker.append(option);
    }
    picker.value=String(activeFragmentIndex);
    picker.addEventListener('change',()=>{activeFragmentIndex=Number(picker.value);renderFragmentInspector(parent,viewer);});
    const previous=action('上一处',()=>{activeFragmentIndex--;renderFragmentInspector(parent,viewer);},true);
    const next=action('下一处',()=>{activeFragmentIndex++;renderFragmentInspector(parent,viewer);},true);
    previous.disabled=activeFragmentIndex===0;next.disabled=activeFragmentIndex===items.length-1;
    toolbar.append(picker,previous,next);parent.append(toolbar);
    const fragment=items[activeFragmentIndex];
    const selection=make('label',undefined,'pdf-fragment-choice');
    const checkbox=make('input');checkbox.type='checkbox';checkbox.checked=selectedFragments.has(fragment.id);
    checkbox.addEventListener('change',()=>{if(checkbox.checked)selectedFragments.add(fragment.id);else selectedFragments.delete(fragment.id);invalidatePreview();updateSelectionCount();});
    selection.append(checkbox,make('span','将此表加入本次核对'));parent.append(selection);
    parent.append(make('p',`第 ${fragment.page} 页 · ${fragment.rows?.length || 0} 行 · ${Math.max(0,...(fragment.rows || []).map(row=>row.length))} 列`,'pdf-table-meta'));
    renderFragment(fragment,parent);
    const raw=make('details',undefined,'pdf-technical');raw.append(make('summary','来源定位信息'),make('code',fragment.id));
    if(fragment.bbox)raw.append(make('p',`位置：${JSON.stringify(fragment.bbox)}`));parent.append(raw);
    const desired=sourceUrl+'#page='+fragment.page;
    if(viewer.getAttribute('src')!==desired)viewer.src=desired;
  }

  function updateSelectionCount() {
    const target=document.getElementById('pdfSelectionCount');
    if(target)target.textContent=`已选择 ${selectedFragments.size} 处表格`;
  }

  function renderManualPlanForm(editor, parent) {
    const selected = fragments().filter(fragment => selectedFragments.has(fragment.id));
    parent.replaceChildren();
    if (!selected.length) {
      parent.append(make('p', '先勾选原始片段，再生成手动核对表单。', 'muted'));
      return;
    }
    const widths = selected.map(fragment => Math.max(0, ...(fragment.rows || []).map(row => row.length)));
    if (widths.some(width => width !== widths[0]) || widths[0] < 1) {
      parent.append(make('p', '所选片段列数不同，表单不会尝试推断列对齐。请分别选择兼容片段或使用高级 JSON 明确填写。', 'warning'));
      return;
    }
    parent.append(make('p', '所有选择均由审核人填写。页面上下文只是未绑定的来源文本；表头、单位和跨页关系不会自动推断。', 'muted'));
    for (const fragment of selected) {
      const section = make('fieldset');
      section.append(make('legend', `片段 ${fragment.id} · 第 ${fragment.page} 页`));
      const headers = make('details');
      headers.append(make('summary', '手动标记表头行（未勾选行保留为数据）'));
      for (const [rowIndex, row] of (fragment.rows || []).entries()) {
        const label = make('label', undefined, 'row');
        const checkbox = make('input');
        checkbox.type = 'checkbox';
        checkbox.dataset.headerFragment = fragment.id;
        checkbox.dataset.headerRow = String(rowIndex);
        label.append(checkbox, make('span', `行 ${rowIndex} · ${row.map(cell => cell.text ?? '').join(' | ')}`));
        const reason = make('input');
        reason.type = 'text'; reason.placeholder = '标记为表头的原因';
        reason.dataset.headerReasonFragment = fragment.id;
        reason.dataset.headerReasonRow = String(rowIndex);
        headers.append(label, reason);
      }
      const excluded = make('details');
      excluded.append(make('summary', '手动排除原始行（需逐行填写原因；不排除空白行）'));
      for (const [rowIndex, row] of (fragment.rows || []).entries()) {
        const label = make('label', undefined, 'row');
        const checkbox = make('input');
        checkbox.type = 'checkbox';
        checkbox.dataset.excludeFragment = fragment.id;
        checkbox.dataset.excludeRow = String(rowIndex);
        label.append(checkbox, make('span', `行 ${rowIndex} · ${row.map(cell => cell.text ?? '').join(' | ')}`));
        const reason = make('input');
        reason.type = 'text'; reason.placeholder = '排除此行的原因';
        reason.dataset.excludeReasonFragment = fragment.id;
        reason.dataset.excludeReasonRow = String(rowIndex);
        excluded.append(label, reason);
      }
      section.append(headers, excluded);
      parent.append(section);
    }
    const columnSet = make('fieldset');
    columnSet.append(make('legend', '手动定义列名、类型与单位'));
    for (let columnIndex = 0; columnIndex < widths[0]; columnIndex += 1) {
      const row = make('div', undefined, 'row');
      const name = make('input');
      name.type = 'text'; name.value = `Column ${columnIndex + 1}`;
      name.setAttribute('aria-label', `第 ${columnIndex} 列名称`);
      name.dataset.columnName = String(columnIndex);
      const type = make('select');
      type.dataset.columnType = String(columnIndex);
      type.setAttribute('aria-label', `第 ${columnIndex} 列类型`);
      for (const [value, label] of [['', '请选择类型'], ['text', '文本'], ['decimal', '小数']]) {
        const option = make('option', label); option.value = value; type.append(option);
      }
      const unit = make('input');
      unit.type = 'text'; unit.placeholder = '单位（可留空）'; unit.dataset.columnUnit = String(columnIndex);
      unit.setAttribute('aria-label', `第 ${columnIndex} 列单位`);
      row.append(make('span', `逻辑列 ${columnIndex}`), name, type, unit);
      for (const fragment of selected) {
        const mapping = make('select');
        mapping.dataset.sourceColumn = String(columnIndex);
        mapping.dataset.sourceFragment = fragment.id;
        mapping.setAttribute('aria-label', `${fragment.id} 的逻辑列 ${columnIndex} 来源`);
        const prompt = make('option', `选择 ${fragment.id} 来源列`); prompt.value = ''; mapping.append(prompt);
        for (let sourceIndex = 0; sourceIndex < widths[0]; sourceIndex += 1) {
          const option = make('option', `第 ${sourceIndex} 列`); option.value = String(sourceIndex); mapping.append(option);
        }
        row.append(mapping);
      }
      columnSet.append(row);
    }
    parent.append(columnSet, action('应用人工填写到计划', () => {
      try {
        const plan = createManualPlan();
        for (const fragment of selected) {
          const headers = [];
          for (const checkbox of parent.querySelectorAll(`[data-header-fragment="${CSS.escape(fragment.id)}"]`)) {
            if (!checkbox.checked) continue;
            const row = Number(checkbox.dataset.headerRow);
            const reason = parent.querySelector(`[data-header-reason-fragment="${CSS.escape(fragment.id)}"][data-header-reason-row="${row}"]`)?.value.trim();
            if (!reason) throw new Error(`片段 ${fragment.id} 的表头行 ${row} 需要原因。`);
            headers.push({row, reason});
          }
          plan.header_rows[fragment.id] = headers;
          for (const checkbox of parent.querySelectorAll(`[data-exclude-fragment="${CSS.escape(fragment.id)}"]`)) {
            if (!checkbox.checked) continue;
            const row = Number(checkbox.dataset.excludeRow);
            const reason = parent.querySelector(`[data-exclude-reason-fragment="${CSS.escape(fragment.id)}"][data-exclude-reason-row="${row}"]`)?.value.trim();
            if (!reason) throw new Error(`片段 ${fragment.id} 的排除行 ${row} 需要原因。`);
            plan.excluded_rows.push({fragment_id: fragment.id, row, reason});
          }
        }
        plan.columns = plan.columns.map((column, index) => {
          const name = parent.querySelector(`[data-column-name="${index}"]`).value.trim();
          if (!name) throw new Error(`原始列 ${index} 需要列名。`);
          const type = parent.querySelector(`[data-column-type="${index}"]`).value;
          if (!type) throw new Error(`逻辑列 ${index} 需要明确选择文本或小数类型。`);
          const unit = parent.querySelector(`[data-column-unit="${index}"]`).value.trim();
          const source_columns = Object.fromEntries(selected.map(fragment => {
            const value = parent.querySelector(`[data-source-column="${index}"][data-source-fragment="${CSS.escape(fragment.id)}"]`)?.value;
            if (value === undefined || value === '') throw new Error(`逻辑列 ${index} 需要明确选择片段 ${fragment.id} 的来源列。`);
            return [fragment.id, Number(value)];
          }));
          return {...column, name, type, source_columns,
            ...(unit ? {unit} : {})};
        });
        editor.value = JSON.stringify(plan, null, 2);
        editor.dispatchEvent(new Event('input', {bubbles: true}));
        setStatus('人工填写已写入计划；请验证并预览，审核界面不会自动推断或排除来源行。');
      } catch (error) { setStatus(error.message, true); }
    }, true));
  }

  async function selectDraft(id) {
    const savedEpoch = epoch;
    const savedOperation = ++operation;
    draft = null;
    activeFragmentIndex = 0;
    selectedFragments = new Set();
    preview = null;
    revokeSource();
    setStatus('正在读取原始片段与受保护的 PDF 原件…');
    invalidatePreview('选择片段并预览映射后，结果会显示在这里。');
    try {
      const [record, response] = await Promise.all([
        api(`/api/pdf/drafts/${encodeURIComponent(id)}`),
        host.authenticatedFetch(`/api/pdf/drafts/${encodeURIComponent(id)}/source`, {signal: scopeController.signal})
      ]);
      if (!current(savedEpoch, savedOperation)) return;
      if (!response.ok) throw new Error('无法读取此草稿的原始 PDF。');
      const blob = await response.blob();
      if (!current(savedEpoch, savedOperation)) return;
      const url = URL.createObjectURL(blob);
      if (!current(savedEpoch, savedOperation)) { URL.revokeObjectURL(url); return; }
      draft = record;
      sourceUrl = url;
      renderSelectedDraft();
      setStatus('');
      return true;
    } catch (error) {
      if (current(savedEpoch, savedOperation)) setStatus(error.message, true);
      return false;
    }
  }

  function renderSelectedDraft() {
    const target = document.getElementById('pdfDraftDetail');
    if (!target || !draft) return;
    root.classList.add('reviewing-file');
    target.replaceChildren();
    const bundle = draft.bundle || {};
    const src = source(bundle);
    const parser = bundle.parser || {};
    const parserProfile = parser.requested_tier
      ? ` · 请求配置：${parser.requested_tier} / ${parser.requested_ocr_mode || 'auto'}` : '';
    const heading=make('div',undefined,'pdf-review-heading');
    const back=action('返回文件列表',showFileList,true);back.id='pdfBackToFiles';
    const title=make('div');title.append(make('h2',src.file_name || 'PDF 文件'),make('small','核对原件与识别表格，确认后发布'));
    const sourceLink=make('a','打开原件');sourceLink.href=sourceUrl;sourceLink.target='_blank';sourceLink.rel='noopener noreferrer';
    heading.append(back,title,sourceLink);target.append(heading);
    const provenance=make('details',undefined,'pdf-technical');
    provenance.append(make('summary','文件与解析信息'),make('p',`SHA256：${src.sha256 || '未知'} · 解析器：${parser.engine || '未知'} ${parser.version || ''}${parserProfile} · 提取状态：未独立验证。`));
    target.append(provenance);
    const viewer=make('iframe');viewer.title=`原始 PDF：${src.file_name || '文件'}`;viewer.loading='lazy';viewer.className='pdf-source-viewer';
    const proposals = bundle.continuation_proposals || [];
    if (proposals.length) {
      const proposed = make('details');
      proposed.append(make('summary', `跨页关系候选 · ${proposals.length} 项（尚未确认）`));
      proposed.append(make('p', '候选仅帮助定位，选择不会合并或发布；请检查原件后请求映射并逐项预览。', 'warning'));
      const actualIds = new Set((bundle.fragments || []).map(f => f.id));
      for (const proposal of proposals) {
        const item = make('div');
        const leftPage = fragments().find(f=>f.id===proposal.left_fragment_id)?.page ?? '未知';
        const rightPage = fragments().find(f=>f.id===proposal.right_fragment_id)?.page ?? '未知';
        item.append(make('p', `第 ${leftPage} 页与第 ${rightPage} 页 · ${proposal.kind === 'horizontal' ? '横向续列' : proposal.kind === 'vertical' ? '纵向续行' : '关系不确定'} · ${proposal.reason || ''}`));
        if (actualIds.has(proposal.left_fragment_id) && actualIds.has(proposal.right_fragment_id)) {
          item.append(action('选择这组片段核对', () => {
            selectedFragments = new Set([proposal.left_fragment_id, proposal.right_fragment_id]);
            invalidatePreview();
            renderSelectedDraft();
            setStatus('已选择候选片段；请核对原件后请求 AI 映射建议，不会自动合并或发布。');
          }, true));
        }
        proposed.append(item);
      }
      target.append(proposed);
    }
    const sourceAndTables=make('div',undefined,'pdf-review-comparison');
    const original=make('div',undefined,'pdf-original-pane');original.append(make('h3','原始文件'),viewer);
    const fragmentList=make('div',undefined,'pdf-fragment-pane');
    sourceAndTables.append(original,fragmentList);target.append(sourceAndTables);
    renderFragmentInspector(fragmentList,viewer);
    const supporting=make('details',undefined,'pdf-supporting');supporting.append(make('summary','更多来源线索'));
    const formulas = bundle.formulas || [];
    if (formulas.length) {
      const formulaList = make('details');
      formulaList.append(make('summary', `公式线索 · ${formulas.length} 项（仅为识别线索，不可执行）`));
      for (const formula of formulas) formulaList.append(make('p', `第 ${formula.page || '未知'} 页 · ${formula.latex || formula.text || JSON.stringify(formula)}`));
      supporting.append(formulaList);
    }
    const context = bundle.page_context || [];
    const contextDetails = make('details');
    contextDetails.append(make('summary', `页面上下文（未绑定到表格） · ${context.length} 项`));
    if (bundle.page_context_coverage === 'incomplete') contextDetails.append(make('p', `上下文提取不完整：${(bundle.page_context_warnings || []).join('；')}`, 'warning'));
    contextDetails.append(make('p', '这些解析器文本保留原始页码、位置和区块类型，只作为候选证据。它们不自动成为任何表格的单位、标题或脚注。', 'muted'));
    for (const item of context) contextDetails.append(make('p', `第 ${item.page ?? '未知'} 页 · ${item.block_type || '未知区块'} · ${item.text || item.text_parts?.join('') || ''}${item.bbox ? ` · 坐标 ${JSON.stringify(item.bbox)}` : ''}`));
    if (context.length || bundle.page_context_coverage === 'incomplete') supporting.append(contextDetails);
    if ((bundle.fragments || []).some(fragment => (fragment.rows || []).some(row => row.every(cell => !String(cell.text || '').trim())))) {
      supporting.append(make('p', '检测到全空原始行。请对照 PDF 核对；这些行不会在审核界面中自动删除。', 'warning'));
    }
    if (formulas.length) supporting.append(make('p', '公式仅保留识别到的 LaTeX/文本，没有解析器置信度证明，也不会自动计算。', 'warning'));
    target.append(supporting);
    const mappingSection=make('div',undefined,'pdf-mapping-section');
    mappingSection.append(make('h3','核对字段与数据范围'));
    const selectionCount=make('span','','pdf-selection-count');selectionCount.id='pdfSelectionCount';mappingSection.append(selectionCount);
    const manualTitle = make('h3', '人工映射表单');
    const manualForm = make('div');
    const planDetails = make('details');
    planDetails.append(make('summary', '高级映射 JSON'));
    const planHelp = make('p', 'header_rows 的 row 为片段内零起算行号；每列 source_columns 必须为每个片段填写原始零起算列号。表头移除需填写 {row, reason}。边界续接需显式填写 boundary_merges。横向续列使用 join_mode:horizontal、key_columns 和 row_groups；每列 source_columns 只映射实际贡献片段，每个数据行必须显式对应。缺少行标识时必须独立核对原件。修正使用 cell_corrections:[{fragment_id,row,column,text,reason}]，保留原文。', 'muted');
    const editor = make('textarea');
    editor.id = 'pdfPlanEditor';
    editor.rows = 20;
    editor.spellcheck = false;
    editor.setAttribute('aria-label', 'PDF 表格映射计划 JSON');
    editor.value = JSON.stringify({bundle_sha256: draft.bundle_sha256, fragment_ids: [], header_rows: {}, columns: [], excluded_rows: [], boundary_merges: []}, null, 2);
    editor.addEventListener('input', () => invalidatePreview());
    planDetails.append(planHelp, editor);
    const manualDetails=make('details',undefined,'pdf-manual-details');manualDetails.append(make('summary','手动调整字段'),manualTitle,manualForm,planDetails);
    const controls = make('div', undefined, 'row');
    controls.style.marginTop = '12px';
    const reparseMode = make('select');
    reparseMode.id = 'pdfReparseOcrMode';
    reparseMode.setAttribute('aria-label', '复核 OCR 模式');
    for (const [value, label] of [['auto', '自动选择 OCR'], ['ocr', '强制 OCR'], ['txt', '尝试文本模式']]) {
      const option = make('option', label); option.value = value; reparseMode.append(option);
    }
    const reparseControls=make('details',undefined,'pdf-reparse-controls');reparseControls.append(make('summary','重新识别文件'),make('p','生成独立版本，已有表格继续保留。','muted'));
    const reparseRow=make('div',undefined,'row');
    reparseRow.append(
      reparseMode,
      action('使用基础模型复核', () => reparseDraft('basic', reparseMode.value).catch(error => setStatus(error.message, true)), true),
      action('使用标准模型复核', () => reparseDraft('standard', reparseMode.value).catch(error => setStatus(error.message, true)), true),
    );
    reparseControls.append(reparseRow);
    const manualTemplate = action('生成手动计划模板', () => {
        try {
          editor.value = JSON.stringify(createManualPlan(), null, 2);
          renderManualPlanForm(editor, manualForm);
          manualDetails.open = true;
          invalidatePreview('手动计划已创建，请填写表头、列名、单位与跨页关系，再预览。');
        } catch (error) { setStatus(error.message, true); }
      }, true);
    manualDetails.insertBefore(manualTemplate, manualTitle);
    controls.append(
      Object.assign(action('识别表格字段', () => suggestPlan().catch(error => setStatus(error.message, true))), {id:'pdfSuggestPlan'}),
      action('验证并预览', () => previewPlan().catch(error => setStatus(error.message, true)), true)
    );
    const instructions=make('textarea');instructions.id='pdfInstructions';instructions.rows=2;instructions.placeholder='补充需要核对的表头、单位或跨页关系';instructions.setAttribute('aria-label','补充核对说明');
    const options=make('details',undefined,'pdf-instructions');options.append(make('summary','补充核对说明（可选）'),instructions);
    mappingSection.append(controls,options,manualDetails,reparseControls);
    target.append(mappingSection);updateSelectionCount();
    const confirmation = make('div', undefined, 'mapping-block');
    confirmation.append(make('h3', '人工确认发布'));
    confirmation.append(make('p', '发布会创建可查询的表格版本。确认表示你已检查原 PDF、片段关系、表头、列与单位，不表示 OCR 或提取准确率已经验证。', 'warning'));
    const reviewLabel = make('label');
    reviewLabel.style.flexDirection = 'row';
    reviewLabel.style.alignItems = 'center';
    const reviewed = make('input');
    reviewed.type = 'checkbox';
    reviewed.id = 'pdfPublishReviewed';
    reviewed.addEventListener('change', updatePublishButton);
    reviewLabel.append(reviewed, make('span', '我已逐项人工核对，并确认按此映射发布。'));
    confirmation.append(reviewLabel);
    const publish = action('确认并发布为可查询表', () => publishPlan().catch(error => setStatus(error.message, true)));
    publish.id = 'pdfPublish';
    publish.disabled = true;
    confirmation.append(publish);
    const publishSection=make('div',undefined,'pdf-publication-section');publishSection.id='pdfPublicationSection';publishSection.hidden=true;
    const previewPane = make('div');
    previewPane.id = 'pdfPreview';
    previewPane.append(make('p', '选择片段并预览映射后，结果会显示在这里。', 'muted'));
    publishSection.append(make('h3','预览与发布'),previewPane,confirmation);target.append(publishSection);
  }

  function updatePublishButton() {
    const publish = document.getElementById('pdfPublish');
    const checkbox = document.getElementById('pdfPublishReviewed');
    if (publish) publish.disabled = !preview || !checkbox?.checked || Boolean(preview?.coverage?.unresolved_quality_issues?.length);
  }

  async function suggestPlan() {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    const ids = fragments().filter(fragment => selectedFragments.has(fragment.id)).map(fragment => fragment.id);
    if (!ids.length) throw new Error('请先勾选希望核对的表片段。');
    const savedEpoch = epoch;
    const savedOperation = operation;
    setStatus('正在请求映射建议；建议仍需人工检查，且不自动发布。');
    const result = await api(`/api/pdf/drafts/${encodeURIComponent(draft.draft_id)}/suggest`, {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({fragment_ids: ids, instructions: document.getElementById('pdfInstructions')?.value || ''})
    }, 120000);
    if (!current(savedEpoch, savedOperation)) return;
    if (result.status !== 'ready' || !result.plan) {
      preview = null;
      previewSignature = '';
      invalidatePreview(result.message || '建议需要进一步核对；请调整片段选择或手动填写计划。');
      setStatus(result.message || '模型未给出可验证计划，请人工调整。', true);
      return;
    }
    const editor = document.getElementById('pdfPlanEditor');
    editor.value = JSON.stringify(result.plan, null, 2);
    preview = result.preview || null;
    previewSignature = JSON.stringify(result.plan);
    if (preview) renderPlanPreview(preview);
    updatePublishButton();
    setStatus('字段已识别。请对照原件核对数据后再发布。');
  }

  async function previewPlan() {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    const plan = jsonValue(document.getElementById('pdfPlanEditor'));
    const savedEpoch = epoch;
    const savedOperation = operation;
    setStatus('正在检查计划、来源覆盖和预览行…');
    const result = await api(`/api/pdf/drafts/${encodeURIComponent(draft.draft_id)}/preview`, {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({plan})
    });
    if (!current(savedEpoch, savedOperation)) return;
    preview = result;
    previewSignature = JSON.stringify(plan);
    renderPlanPreview(result);
    updatePublishButton();
    setStatus('计划校验通过。请检查每列原始单元格来源和跨页边界，再勾选人工确认。');
  }

  async function publishPlan() {
    if (!preview) throw new Error('请先验证并预览当前计划。');
    if (preview.coverage?.unresolved_quality_issues?.length) throw new Error('请先逐项处理质量问题并重新预览。');
    const editor = document.getElementById('pdfPlanEditor');
    const plan = jsonValue(editor);
    if (JSON.stringify(plan) !== previewSignature) throw new Error('计划内容在预览后已改变，请重新预览。');
    if (!document.getElementById('pdfPublishReviewed')?.checked) throw new Error('请先勾选人工核对确认。');
    const savedEpoch = epoch;
    const savedOperation = operation;
    const submit = {...plan, reviewed: true};
    setStatus('正在保存已人工确认的 PDF 表格版本…');
    const result = await api(`/api/pdf/drafts/${encodeURIComponent(draft.draft_id)}/publish`, {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({plan: submit, reviewed: true})
    });
    if (!current(savedEpoch, savedOperation)) return;
    preview = null;
    previewSignature = '';
    document.getElementById('pdfPublishReviewed').checked = false;
    updatePublishButton();
    setStatus('已发布表格，可用于查询与问答。提取结果仍未独立验证。');
    await loadTables(savedEpoch, savedOperation);
  }

  async function reparseDraft(tier, ocrMode) {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    if (!['basic', 'standard'].includes(tier) || !['auto', 'ocr', 'txt'].includes(ocrMode)) {
      throw new Error('复核模型或识别模式无效。');
    }
    const savedEpoch = epoch;
    const savedOperation = operation;
    setStatus('正在校验原件快照并启动独立复核；不会替换或发布当前草稿。');
    const result = await api(`/api/pdf/drafts/${encodeURIComponent(draft.draft_id)}/reparse`, {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({tier, ocr_mode: ocrMode})
    }, 120000);
    if (!current(savedEpoch, savedOperation)) return;
    if (result.status === 'completed' && result.draft_id) {
      await selectDraft(result.draft_id);
      return;
    }
    const jobId = result.id || result.job_id;
    if (!jobId) throw new Error('复核请求没有返回任务或草稿编号。');
    await pollJob(jobId, savedEpoch, savedOperation);
  }

  async function loadTables(savedEpoch = epoch, savedOperation = operation) {
    const result = await api('/api/pdf/tables');
    if (!current(savedEpoch, savedOperation)) return;
    tables = result.tables || [];
    renderPublishedTables();
  }

  async function loadDrafts(savedEpoch = epoch, savedOperation = operation) {
    const result = await api('/api/pdf/drafts');
    if (!current(savedEpoch, savedOperation)) return;
    drafts = result.drafts || [];
    renderDrafts();
  }

  async function pollJob(jobId, savedEpoch, savedOperation) {
    for (;;) {
      const job = await api(`/api/pdf/jobs/${encodeURIComponent(jobId)}`);
      if (!current(savedEpoch, savedOperation)) return;
      setStatus(job.stage || `PDF 任务：${job.status}`);
      if (['failed', 'cancelled'].includes(job.status)) {
        jobs = [job, ...jobs.filter(item => item.id !== job.id)];
        renderJobs();
        throw new Error(job.error || 'PDF 解析失败，可通过任务旁的按钮手动重试。');
      }
      if (job.status === 'completed') {
        const draftId = job.draft_id || job.result?.draft_id;
        if (!draftId) throw new Error('解析完成但没有可读取的 PDF 草稿编号。');
        await loadDrafts(savedEpoch, savedOperation);
        if (!current(savedEpoch, savedOperation)) return;
        if (await selectDraft(draftId) === false) throw new Error('PDF 已识别，但无法打开核对结果，请检查原件读取权限后重试。');
        return;
      }
      if (!['processing', 'queued', 'pending', 'running'].includes(job.status)) throw new Error(`未知的 PDF 任务状态：${job.status}`);
      await new Promise((resolve, reject) => {
        const signal = scopeController.signal;
        const cleanup = () => signal.removeEventListener('abort', aborted);
        const timer = setTimeout(() => { cleanup(); resolve(); }, 1200);
        const aborted = () => { clearTimeout(timer); cleanup(); reject(new Error('账号或知识库已切换，PDF 解析轮询已取消。')); };
        signal.addEventListener('abort', aborted, {once: true});
      });
    }
  }

  async function openKnowledgeSource(sourceKnowledgeId) {
    const id = String(sourceKnowledgeId || '');
    if (!id) throw new Error('PDF 原件编号为空。');
    const savedEpoch = host.getAuthEpoch();
    if (requestedSourceKnowledge === id && requestedSourceEpoch === savedEpoch && sourceBusy) return;
    host.showWorkspace?.();
    epoch = savedEpoch;
    requestedSourceKnowledge = id;
    requestedSourceEpoch = savedEpoch;
    sourceBusy = true;
    await loadWorkspace();
    if (savedEpoch !== host.getAuthEpoch() || savedEpoch !== epoch || requestedSourceKnowledge !== id || scopeController.signal.aborted) return;
    const savedOperation = ++operation;
    const isCurrentSource = () => current(savedEpoch, savedOperation) && requestedSourceKnowledge === id && requestedSourceEpoch === savedEpoch;
    const isCurrentSourceScope = () => savedEpoch === host.getAuthEpoch() && savedEpoch === epoch && requestedSourceKnowledge === id && requestedSourceEpoch === savedEpoch && !scopeController.signal.aborted;
    setStatus('正在读取知识库中的 PDF 原件并准备核对草稿…');
    try {
      const response = await api('/api/pdf/import-knowledge', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({source_knowledge_id: id})
      }, 120000);
      if (!isCurrentSource()) return;
      const jobId = response.id || response.job_id;
      const draftId = response.draft_id || response.result?.draft_id;
      if (response.status === 'completed' && draftId) {
        await loadDrafts(savedEpoch, savedOperation);
        if (isCurrentSource()) await selectDraft(draftId);
      } else if (jobId) {
        await pollJob(jobId, savedEpoch, savedOperation);
      } else {
        throw new Error('PDF 原件请求没有返回任务或草稿编号。');
      }
      if (isCurrentSourceScope()) setStatus(response.reused
        ? '已复用经当前原件内容校验的同配置 PDF 草稿。请继续核对原件、解析片段和映射。'
        : '已打开知识库中的 PDF 草稿。请核对原件、解析片段和映射后再预览。');
    } catch (error) {
      if (isCurrentSourceScope()) setStatus(error.message || '读取 PDF 原件失败。', true);
    } finally {
      if (isCurrentSourceScope()) sourceBusy = false;
    }
  }

  function closeKnowledgeSource() {
    requestedSourceKnowledge = '';
    requestedSourceEpoch = -1;
    sourceBusy = false;
    operation += 1;
    revokeSource();
    draft = null;
    selectedFragments = new Set();
    preview = null;
    previewSignature = '';
    root.replaceChildren(make('p', 'PDF 表格导入与核对', 'muted'));
  }

  async function uploadFile(file) {
    if (!config.configured) throw new Error('PDF 解析服务尚未配置，当前不能导入。');
    if (!file || !file.name.toLowerCase().endsWith('.pdf')) throw new Error('请选择 PDF 文件。');
    if (file.size > config.max_bytes) throw new Error(`PDF 超过 ${Math.round(config.max_bytes / 1024 / 1024)} MB。`);
    const savedEpoch = epoch;
    const savedOperation = ++operation;
    const form = new FormData();
    form.append('file', file);
    setStatus('正在安全上传 PDF 并启动解析…');
    const job = await api('/api/pdf/import', {method: 'POST', body: form}, 120000);
    if (!current(savedEpoch, savedOperation)) return;
    await pollJob(job.id, savedEpoch, savedOperation);
  }

  async function loadWorkspace({throwOnError = false} = {}) {
    if (!host.isWorkspaceReady()) throw new Error('工作区尚未就绪，请稍后重试。');
    const savedEpoch = host.getAuthEpoch();
    epoch = savedEpoch;
    const savedOperation = ++operation;
    try {
      const [settings] = await Promise.all([api('/api/pdf/config')]);
      if (!current(savedEpoch, savedOperation)) return;
      config = settings;
      if (renderedEpoch !== savedEpoch) {
        renderShell();
        renderedEpoch = savedEpoch;
      }
      const [draftResult, tableResult, jobResult] = await Promise.all([api('/api/pdf/drafts'), api('/api/pdf/tables'), api('/api/pdf/jobs')]);
      if (!current(savedEpoch, savedOperation)) return;
      drafts = draftResult.drafts || [];
      tables = tableResult.tables || [];
      jobs = jobResult.jobs || [];
      renderJobs();
      renderDrafts();
      renderPublishedTables();
      setStatus(config.configured ? '' : 'PDF 解析服务尚未配置，暂时无法处理新文件。');
    } catch (error) {
      if (current(savedEpoch, savedOperation)) {
        root.replaceChildren(make('p', error.message, 'error'));
        renderedEpoch = -1;
      }
      if (throwOnError) throw error;
    }
  }

  function renderShell() {
    root.replaceChildren();root.classList.remove('reviewing-file');
    const inventory=make('div',undefined,'pdf-inventory');inventory.id='pdfInventory';
    const toolbar=make('div',undefined,'pdf-inventory-toolbar');
    const heading=make('div');heading.append(make('h2','PDF 文件'),Object.assign(make('span','','muted'),{id:'pdfFileCount'}));
    const refresh=action('刷新记录',()=>Promise.all([loadDrafts(),loadTables(),loadJobs()]).catch(error=>setStatus(error.message,true)),true);
    toolbar.append(heading,refresh);inventory.append(toolbar);
    if(!host.unifiedUpload){
      const input=make('input');input.id='pdfUploadFile';input.type='file';input.accept='.pdf,application/pdf';
      const label=make('label','上传 PDF');label.append(input);
      const upload=action('上传并解析',()=>uploadFile(input.files?.[0]).catch(error=>setStatus(error.message,true)));upload.id='pdfUpload';upload.disabled=!config.configured;
      const row=make('div',undefined,'row');row.append(label,upload);inventory.append(row);
    }
    const jobs=make('div');jobs.id='pdfJobs';inventory.append(jobs);
    const list=make('div');list.id='pdfDraftList';inventory.append(list);
    root.append(inventory);
    const status=make('p','','pdf-status');status.id='pdfReviewStatus';status.setAttribute('role','status');status.setAttribute('aria-live','polite');root.append(status);
    const detail=make('div');detail.id='pdfDraftDetail';root.append(detail);

  }

  window.addEventListener('structured-auth-scope-change', () => {
    epoch = host.getAuthEpoch();
    clearScope();
  });
  window.addEventListener('structured-workspace-ready', () => loadWorkspace());
  window.addEventListener('pagehide', () => {
    operation += 1;
    scopeController.abort();
    revokeSource();
  });
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => { if (host.isWorkspaceReady()) loadWorkspace(); }, {once: true});
  } else if (host.isWorkspaceReady()) {
    loadWorkspace();
  }
  host.uploadFile = async file => {
    const savedEpoch = host.getAuthEpoch();
    await loadWorkspace({throwOnError: true});
    if (savedEpoch !== host.getAuthEpoch() || savedEpoch !== epoch || scopeController.signal.aborted) throw new Error('账号或知识库已切换，请重新上传。');
    root.inert = true;
    try { await uploadFile(file); }
    finally { if (savedEpoch === host.getAuthEpoch()) root.inert = false; }
  };
  host.openKnowledgeSource = openKnowledgeSource;
  host.closeKnowledgeSource = closeKnowledgeSource;
})();
