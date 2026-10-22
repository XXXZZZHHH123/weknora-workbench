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
  let draft = null;
  let selectedFragments = new Set();
  let preview = null;
  let previewSignature = '';
  let sourceUrl = '';
  let renderedEpoch = -1;
  let requestedSourceKnowledge = '';
  let requestedSourceEpoch = -1;
  let sourceBusy = false;

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
    scopeController.abort();
    scopeController = new AbortController();
    requestedSourceKnowledge = '';
    requestedSourceEpoch = -1;
    sourceBusy = false;
    revokeSource();
    config = {configured: false, max_bytes: 50 * 1024 * 1024};
    drafts = [];
    tables = [];
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
    const coverage = result.coverage || {};
    const note = make('p', `预览行数：${coverage.logical_row_count ?? result.rows?.length ?? 0}；原始片段：${coverage.selected_fragment_count ?? '—'}；来源文档完整性：${coverage.source_document_completeness || 'unknown'}。OCR/解析结果尚未独立验证。`);
    pane.append(note);
    if (coverage.extraction_verified === false || result.extraction_verified === false) {
      pane.append(make('p', '此预览只验证映射和来源覆盖，不代表 OCR 或原件识别已核实；公式不会作为可执行公式。', 'warning'));
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

  function renderPublishedTables() {
    const target = document.getElementById('pdfTables');
    if (!target) return;
    target.replaceChildren();
    target.append(make('h3', `已发布 PDF 表格 · ${tables.length}`));
    if (!tables.length) {
      target.append(make('p', '当前空间还没有发布的 PDF 表格。', 'muted'));
      return;
    }
    const list = make('ul');
    for (const table of tables) {
      const item = make('li');
      const src = table.source || {};
      item.textContent = `${src.file_name || 'PDF'} · ${table.columns?.length || 0} 列 · ${table.coverage?.logical_row_count || 0} 行 · ${table.extraction_verified ? '已验证提取' : '提取未验证'} · ${table.table_id}`;
      list.append(item);
    }
    target.append(list);
  }

  function renderDrafts() {
    const list = document.getElementById('pdfDraftList');
    if (!list) return;
    list.replaceChildren();
    if (!drafts.length) {
      list.append(make('p', '暂无 PDF 草稿。', 'muted'));
      return;
    }
    for (const item of drafts) {
      const row = make('div', undefined, 'row');
      const detail = make('span', `${item.source?.file_name || 'PDF'} · ${item.fragment_count || 0} 个片段 · ${item.raw_row_count || 0} 行 · ${item.draft_id}`);
      row.append(detail, action('核对草稿', () => selectDraft(item.draft_id), true));
      list.append(row);
    }
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
    const details = make('details');
    const summary = make('summary', `第 ${fragment.page} 页 · ${fragment.rows?.length || 0} 行 · ${fragment.id}`);
    details.append(summary);
    if (fragment.bbox) details.append(make('p', `片段位置：${JSON.stringify(fragment.bbox)}`, 'muted'));
    const wrap = make('div', undefined, 'scroll');
    const table = make('table');
    const body = make('tbody');
    for (const [rowIndex, row] of (fragment.rows || []).entries()) {
      const tr = make('tr');
      tr.append(make('th', `行 ${rowIndex}`));
      for (const [columnIndex, cell] of row.entries()) {
        const td = make('td');
        td.append(make('span', cell.text, 'pdf-raw-cell'));
        td.append(make('small', `列 ${columnIndex}${cell.bbox ? ` · ${JSON.stringify(cell.bbox)}` : ''}${cell.rowspan > 1 || cell.colspan > 1 ? ` · 合并 ${cell.rowspan}×${cell.colspan}` : ''}`));
        tr.append(td);
      }
      body.append(tr);
    }
    table.append(body);
    wrap.append(table);
    details.append(wrap);
    parent.append(details);
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
      setStatus('草稿已载入。请先检查原件和每个表片段，再生成或编写映射。');
    } catch (error) {
      if (current(savedEpoch, savedOperation)) setStatus(error.message, true);
    }
  }

  function renderSelectedDraft() {
    const target = document.getElementById('pdfDraftDetail');
    if (!target || !draft) return;
    target.replaceChildren();
    const bundle = draft.bundle || {};
    const src = source(bundle);
    const parser = bundle.parser || {};
    const parserProfile = parser.requested_tier
      ? ` · 请求配置：${parser.requested_tier} / ${parser.requested_ocr_mode || 'auto'}` : '';
    const provenance = make('p', `文件：${src.file_name || '未知'} · SHA256：${src.sha256 || '未知'} · 解析器：${parser.engine || '未知'} ${parser.version || ''}${parserProfile} · 提取状态：未独立验证。`);
    target.append(provenance);
    const sourceLink = make('a', '在新标签页查看原始 PDF');
    sourceLink.href = sourceUrl;
    sourceLink.target = '_blank';
    sourceLink.rel = 'noopener noreferrer';
    target.append(sourceLink);
    const viewer = make('iframe');
    viewer.src = sourceUrl;
    viewer.title = `原始 PDF：${src.file_name || '文件'}`;
    viewer.loading = 'lazy';
    viewer.style.cssText = 'display:block;width:100%;height:420px;border:1px solid var(--line);border-radius:8px;margin-top:12px';
    target.append(make('p', '原始 OCR/版面识别结果需要人工核对；同页相邻片段或列数相同不足以证明它们属于同一张续表。', 'warning'));

    const fragmentList = make('div');
    fragmentList.append(make('h3', `原始表片段 · ${(bundle.fragments || []).length}`));
    for (const fragment of bundle.fragments || []) {
      const label = make('label', undefined, 'row');
      label.style.alignItems = 'center';
      const checkbox = make('input');
      checkbox.type = 'checkbox';
      checkbox.checked = selectedFragments.has(fragment.id);
      checkbox.addEventListener('change', () => {
        if (checkbox.checked) selectedFragments.add(fragment.id);
        else selectedFragments.delete(fragment.id);
        invalidatePreview();
      });
      label.append(checkbox, make('span', `纳入片段 ${fragment.id}`));
      fragmentList.append(label);
      renderFragment(fragment, fragmentList);
    }
    const sourceAndTables = make('div');
    sourceAndTables.style.cssText = 'display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,360px),1fr));gap:16px;align-items:start;margin-top:12px';
    sourceAndTables.append(viewer, fragmentList);
    target.append(sourceAndTables);
    const formulas = bundle.formulas || [];
    if (formulas.length) {
      const formulaList = make('details');
      formulaList.append(make('summary', `公式线索 · ${formulas.length} 项（仅为识别线索，不可执行）`));
      for (const formula of formulas) formulaList.append(make('p', `第 ${formula.page || '未知'} 页 · ${formula.latex || formula.text || JSON.stringify(formula)}`));
      target.append(formulaList);
    }
    const context = bundle.page_context || [];
    const contextDetails = make('details');
    contextDetails.append(make('summary', `页面上下文（未绑定到表格） · ${context.length} 项`));
    if (bundle.page_context_coverage === 'incomplete') contextDetails.append(make('p', `上下文提取不完整：${(bundle.page_context_warnings || []).join('；')}`, 'warning'));
    contextDetails.append(make('p', '这些解析器文本保留原始页码、位置和区块类型，只作为候选证据。它们不自动成为任何表格的单位、标题或脚注。', 'muted'));
    for (const item of context) contextDetails.append(make('p', `第 ${item.page ?? '未知'} 页 · ${item.block_type || '未知区块'} · ${item.text || item.text_parts?.join('') || ''}${item.bbox ? ` · 坐标 ${JSON.stringify(item.bbox)}` : ''}`));
    if (context.length || bundle.page_context_coverage === 'incomplete') target.append(contextDetails);
    if ((bundle.fragments || []).some(fragment => (fragment.rows || []).some(row => row.every(cell => !String(cell.text || '').trim())))) {
      target.append(make('p', '检测到全空原始行。请对照 PDF 核对；这些行不会在审核界面中自动删除。', 'warning'));
    }
    if (formulas.length) target.append(make('p', '公式仅保留识别到的 LaTeX/文本，没有解析器置信度证明，也不会自动计算。', 'warning'));
    const manualTitle = make('h3', '人工映射表单');
    const manualForm = make('div');
    const planDetails = make('details');
    planDetails.append(make('summary', '高级映射 JSON'));
    const planHelp = make('p', 'header_rows 的 row 为片段内零起算行号；每列 source_columns 必须为每个片段填写原始零起算列号。表头移除需填写 {row, reason}。边界续接需显式填写 boundary_merges。', 'muted');
    const editor = make('textarea');
    editor.id = 'pdfPlanEditor';
    editor.rows = 20;
    editor.spellcheck = false;
    editor.setAttribute('aria-label', 'PDF 表格映射计划 JSON');
    editor.value = JSON.stringify({bundle_sha256: draft.bundle_sha256, fragment_ids: [], header_rows: {}, columns: [], excluded_rows: [], boundary_merges: []}, null, 2);
    editor.addEventListener('input', () => invalidatePreview());
    planDetails.append(planHelp, editor);
    target.append(manualTitle, manualForm, planDetails);
    const controls = make('div', undefined, 'row');
    controls.style.marginTop = '12px';
    const reparseMode = make('select');
    reparseMode.id = 'pdfReparseOcrMode';
    reparseMode.setAttribute('aria-label', '复核 OCR 模式');
    for (const [value, label] of [['auto', '自动选择 OCR'], ['ocr', '强制 OCR'], ['txt', '尝试文本模式']]) {
      const option = make('option', label); option.value = value; reparseMode.append(option);
    }
    controls.append(
      reparseMode,
      action('使用基础模型复核', () => reparseDraft('basic', reparseMode.value).catch(error => setStatus(error.message, true)), true),
      action('使用标准模型复核', () => reparseDraft('standard', reparseMode.value).catch(error => setStatus(error.message, true)), true),
      action('生成手动计划模板', () => {
        try {
          editor.value = JSON.stringify(createManualPlan(), null, 2);
          renderManualPlanForm(editor, manualForm);
          invalidatePreview('手动计划已创建，请填写表头、列名、单位与跨页关系，再预览。');
        } catch (error) { setStatus(error.message, true); }
      }, true),
      action('请求 AI 映射建议', () => suggestPlan().catch(error => setStatus(error.message, true))),
      action('验证并预览', () => previewPlan().catch(error => setStatus(error.message, true)), true)
    );
    target.append(controls);
    target.append(make('p', '复核使用当前服务端配置的 MinerU，生成新的独立草稿。不同模型的识别结果仍需逐格对照原 PDF；不会覆盖当前草稿、已发布表或自动发布。', 'warning'));
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
    target.append(confirmation);
    const previewPane = make('div');
    previewPane.id = 'pdfPreview';
    previewPane.append(make('p', '选择片段并预览映射后，结果会显示在这里。', 'muted'));
    target.append(make('h3', '校验预览'), previewPane);
  }

  function updatePublishButton() {
    const publish = document.getElementById('pdfPublish');
    const checkbox = document.getElementById('pdfPublishReviewed');
    if (publish) publish.disabled = !preview || !checkbox?.checked;
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
    setStatus('已收到映射建议并通过结构校验；请逐格核对原件后再确认发布。');
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
    setStatus(`已发布表格 ${result.table_id || ''}。提取状态仍为未独立验证。`);
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
      if (job.status === 'failed') throw new Error(job.error || 'PDF 解析失败。');
      if (job.status === 'completed') {
        const draftId = job.draft_id || job.result?.draft_id;
        if (!draftId) throw new Error('解析完成但没有可读取的 PDF 草稿编号。');
        await loadDrafts(savedEpoch, savedOperation);
        if (!current(savedEpoch, savedOperation)) return;
        await selectDraft(draftId);
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

  async function loadWorkspace() {
    if (!host.isWorkspaceReady()) return;
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
      const [draftResult, tableResult] = await Promise.all([api('/api/pdf/drafts'), api('/api/pdf/tables')]);
      if (!current(savedEpoch, savedOperation)) return;
      drafts = draftResult.drafts || [];
      tables = tableResult.tables || [];
      renderDrafts();
      renderPublishedTables();
      setStatus(config.configured ? 'PDF 解析服务已配置。导入后先核对原件与片段，再预览并人工发布。' : 'PDF 解析服务尚未配置。已有草稿和已发布表仍可查看；导入入口已停用。');
    } catch (error) {
      if (current(savedEpoch, savedOperation)) {
        root.replaceChildren(make('p', error.message, 'error'));
      }
    }
  }

  function renderShell() {
    root.replaceChildren();
    const fileLabel = make('label', '选择 PDF 文件');
    const file = make('input');
    file.id = 'pdfUploadFile';
    file.type = 'file';
    file.accept = '.pdf,application/pdf';
    fileLabel.append(file);
    const upload = action('上传并解析', () => uploadFile(file.files?.[0]).catch(error => setStatus(error.message, true)));
    upload.id = 'pdfUpload';
    upload.disabled = !config.configured;
    const refresh = action('刷新记录', () => Promise.all([loadDrafts(), loadTables()]).catch(error => setStatus(error.message, true)), true);
    const toolbar = make('div', undefined, 'row');
    toolbar.append(fileLabel, upload, refresh);
    root.append(toolbar);
    root.append(make('p', `单个 PDF 最大 ${Math.round((config.max_bytes || 0) / 1024 / 1024)} MB。提交、轮询、草稿读取和原件查看均使用当前工作台登录身份。`, 'muted'));
    const instructionLabel = make('label', '映射补充说明（可选）');
    instructionLabel.style.maxWidth = '760px';
    const instructions = make('textarea');
    instructions.id = 'pdfInstructions';
    instructions.rows = 3;
    instructions.placeholder = '描述需要核对的标题、表头、单位或跨页边界；不要将推测当作原件事实。';
    instructionLabel.append(instructions);
    root.append(instructionLabel);
    const status = make('p', '', 'muted');
    status.id = 'pdfReviewStatus';
    status.setAttribute('role', 'status');
    status.setAttribute('aria-live', 'polite');
    root.append(status);
    root.append(make('h3', '待核对 PDF 草稿'));
    const draftList = make('div');
    draftList.id = 'pdfDraftList';
    root.append(draftList);
    const detail = make('div');
    detail.id = 'pdfDraftDetail';
    root.append(detail);
    const tableList = make('div');
    tableList.id = 'pdfTables';
    root.append(tableList);
    file.addEventListener('change', () => {
      const name = file.files?.[0]?.name || '未选择文件';
      setStatus(name);
    });
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
  host.openKnowledgeSource = openKnowledgeSource;
  host.closeKnowledgeSource = closeKnowledgeSource;
})();
