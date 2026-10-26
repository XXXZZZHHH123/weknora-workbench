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
  let sourceViewerCleanup = () => {};
  let renderedEpoch = -1;
  let requestedSourceKnowledge = '';
  let requestedSourceEpoch = -1;
  let sourceBusy = false;
  let activeFragmentIndex = 0;
  let automationRun = 0;
  let processingDraftId = '';
  const fileProgress = new Map();
  let listScroll = 0;
  let returnFocus = null;
  const issueExplanations = new Map();

  function closeDetail() {
    const detail=document.getElementById('pdfDraftDetail');
    if(detail?.open)detail.close();
    document.documentElement.classList.remove('pdf-detail-open');
    root.classList.remove('reviewing-file');
    if(returnFocus?.isConnected)returnFocus.focus({preventScroll:true});
    window.scrollTo({top:listScroll,behavior:'instant'});
  }

  function openDetail() {
    const detail=document.getElementById('pdfDraftDetail');
    if(!detail || detail.open)return;
    listScroll=window.scrollY;returnFocus=document.activeElement;
    document.documentElement.classList.add('pdf-detail-open');
    root.classList.add('reviewing-file');
    detail.showModal();
    detail.scrollTop=0;
  }

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
    const detailStatus=document.getElementById('pdfDetailStatus');
    if(detailStatus){detailStatus.textContent=message||'';detailStatus.classList.toggle('error',Boolean(error));}
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
    sourceViewerCleanup();
    if (sourceUrl) URL.revokeObjectURL(sourceUrl);
    sourceUrl = '';
  }

  function clearScope(message = '账号或知识库已切换，请重新加载 PDF 草稿。') {
    closeDetail();automationRun++;processingDraftId='';fileProgress.clear();
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
        const fragment=fragments().find(f=>f.id===issue.fragment_id);
        item.append(make('p', `${fragment?`第 ${fragment.page} 页 · `:''}${issue.row!==undefined?`第 ${issue.row+1} 行 · 第 ${issue.column+1} 列 · `:''}${qualityHint(issue)}`),originalIssue(issue,fragment,issue.issue_index));
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
          const detail = make('small', ref.origin==='manual_transcription'?`第 ${ref.page} 页 · 人工补行 · ${ref.reason}`:`第 ${ref.page} 页 · 第 ${ref.row+1} 行 · 第 ${ref.column+1} 列 · 原文「${ref.text ?? ''}」`);
          td.append(document.createElement('br'), detail);
          if (ref.correction) td.append(make('small', `人工修正为「${ref.correction.text}」；依据：${ref.correction.reason}`));
        }
        tr.append(td);
      }
      const provenance = make('td');
      const refs = Object.values(row.source_cells || {}).flat();
      provenance.textContent = refs.map(ref => ref.origin==='manual_transcription'?`第 ${ref.page} 页人工补行`:`第 ${ref.page} 页第 ${ref.row+1} 行第 ${ref.column+1} 列`).join('；') || '来源缺失';
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
    closeDetail();
    operation += 1;
    draft = null;
    preview = null;
    previewSignature = '';
    selectedFragments = new Set();
    revokeSource();
    document.getElementById('pdfDraftDetail')?.replaceChildren();
    setStatus('');
    // The underlying list was never removed; preserve its controls and position.
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
      if(job.kind==='automatic' && job.draft_id && ['failed','cancelled'].includes(job.status)) {
        row.append(action('继续整理表格',()=>autoProcess(job.draft_id).catch(error=>setStatus(error.message,true)),true));
      } else if (job.retry_available && ['failed', 'cancelled'].includes(job.status)) {
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

  function displayedAutomation(record) {
    const state=record?.automation;
    if(!state?.groups?.length)return state;
    const reviewed=new Set(tables.filter(table=>table.draft_id===record.draft_id&&table.status==='reviewed').flatMap(table=>(table.fragments||[]).map(f=>f.id)));
    const groups=state.groups.map(group=>group.status!=='accepted'&&group.fragment_ids?.length&&group.fragment_ids.every(id=>reviewed.has(id))?{...group,status:'accepted',manually_reviewed:true}:group);
    const resolved=groups.filter(group=>group.manually_reviewed).length;
    if(!resolved)return state;
    const attention=groups.filter(group=>group.status!=='accepted').length;
    return {...state,groups,accepted_count:(state.accepted_count||0)+resolved,attention_count:attention,status:attention?'needs_attention':'completed'};
  }

  function renderDrafts() {
    const list = document.getElementById('pdfDraftList');
    if (!list) return;
    const expanded=new Set([...list.querySelectorAll('.pdf-file-history[open]')].map(item=>item.closest('[data-file-identity]')?.dataset.fileIdentity));
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
    for (const title of ['文件', '处理状态', '可查询表格', '']) headRow.append(make('th', title));
    head.append(headRow);
    const body = make('tbody');
    for (const group of groups) {
      const latest = group.versions[0];
      const row = make('tr');
      row.dataset.fileIdentity = fileIdentity(latest || group.published[0]);
      if(latest)row.dataset.draftId=latest.draft_id;
      const name = make('td'), nameInfo = make('div', undefined, 'pdf-file-name');
      nameInfo.append(make('strong', group.source.file_name || 'PDF 文件'));
      if (latest?.created_at) nameInfo.append(make('small', `最近识别 ${dateLabel(latest.created_at)}`));
      name.append(nameInfo);
      const state=displayedAutomation(latest);
      const result = make('td',undefined,'pdf-file-result');
      result.append(make('span',state?.problem?.title|| (state?.error?'自动整理未完成':state?.status==='processing'?'正在整理':state?.status==='completed'?'处理完成':state?.attention_count?`${state.attention_count} 张需要处理`:latest ? `${latest.fragment_count || 0} 处表格` : '已保存入库结果'),'pdf-file-stage'));
      if(state?.problem || state?.error){
        const problem=problemFor(state);
        const explanation=make('div',undefined,'pdf-file-problem');explanation.append(make('small',problem.message));
        result.append(explanation);
      }
      const published = make('td');
      published.append(make('span', group.published.length ? `${group.published.length} 张表格` : '尚未入库', group.published.length ? 'pdf-published-state' : 'muted'));
      const controls = make('td', undefined, 'pdf-file-actions');
      if (latest) {
        if((!state||state.status!=='completed') && !(state?.problem?.retryable===false && state.problem.action!=='model_settings')){
          const needsModel=state?.problem?.action==='model_settings';
          const run=action(needsModel?'选择处理模型':state?'重新整理':'一键整理并入库',needsModel?openProcessingModel:()=>autoProcess(latest.draft_id).catch(()=>{}));
          run.dataset.automaticDraft=latest.draft_id;
          controls.append(run);
        }
        controls.append(action(state?.attention_count?'处理疑点':'查看表格', () => selectDraft(latest.draft_id), true));
      }
      const history = make('details', undefined, 'pdf-file-history');
      history.open=expanded.has(row.dataset.fileIdentity);
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
    updateFileProgress();
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

  // These are parser protocol codes, not matches against document text or business intent.
  const qualityHints = {
    MODEL_UNCERTAINTY: '识别模型无法确定部分文字、表头或单位。请对照左侧原页检查内容，并在下方设置列名和单位。',
    MISSING_HEADER: '有列名缺失或看不清。请从原页确认各列含义；原页没有列名时，请填写明确的列名并说明依据。',
    UNREADABLE_CELL: '有单元格未能读清。标出的单元格必须对照原件填写；原件确实为空时可以保留空白。',
    TRANSCRIPTION_CELL_CONFLICT: '多次识别得到的单元格内容不同。请逐个核对标出的单元格，填写原件中的文字或数字。',
    TRANSCRIPTION_DISAGREEMENT: '多次识别的内容或行列数量不一致。请检查整张表，尤其是漏行、错列和数字。',
    NOT_DOUBLE_TRANSCRIBED: '此表没有完成多次识别比对。请对照原件检查全部内容。',
    VERIFICATION_INVALID: '识别结果的复查没有完成。请对照原件检查全部内容，或请管理员检查识别服务。',
    TABLE_COUNT_DISAGREEMENT: '多次识别得到的表格数量不同。请检查原页是否有漏掉或重复的表格。',
    uncertain_cell: '这个单元格未能可靠识别。请填写原件中的内容和核对依据；确认原件为空时可以留空。',
    row_alignment_unverified: '跨页表格缺少共同的行标识。请逐行核对对应关系并说明依据，不能只按行号拼接。',
    extraction_needs_review: '此表的识别结果需要核对。请检查文字、数字、表头和单位。',
    transcription_conflict: '多次识别得到的内容不同。请对照原件检查，并修正不一致的单元格。',
    missing_header: '有列名缺失或看不清。请对照原件填写列名和依据。'
  };
  const qualityHint = issue => qualityHints[issue.code] || '此处存在识别疑点。请对照原件检查文字、数字和表格结构；无法确认时先不要入库。';
  function originalIssue(issue, fragment, index) {
    const details=make('details',undefined,'pdf-technical');
    const description=make('p',issue.message && /[\u3400-\u9fff]/.test(issue.message) && !/[A-Za-z]{4}/.test(issue.message) ? issue.message : '正在读取这条记录的中文说明…');
    const advice=make('p','');
    details.append(make('summary','识别记录的中文说明'),description,advice);
    if(!fragment || !Number.isInteger(index) || index<0){description.textContent=qualityHint(issue);return details;}
    const savedEpoch=epoch,id=draft.draft_id,key=`${epoch}:${id}:${fragment.id}`;
    if(!issueExplanations.has(key)){
      const pending=api(`/api/pdf/drafts/${encodeURIComponent(id)}/issue-explanations?fragment_id=${encodeURIComponent(fragment.id)}`);
      issueExplanations.set(key,pending);pending.catch(()=>issueExplanations.delete(key));
    }
    issueExplanations.get(key).then(result=>{
      if(savedEpoch!==host.getAuthEpoch()||draft?.draft_id!==id||!details.isConnected)return;
      const item=result.issues?.find(item=>item.issue_index===index);
      description.textContent=item?.explanation||qualityHint(issue);advice.textContent=item?.action||'';
    }).catch(()=>{
      if(!details.isConnected)return;
      description.textContent='暂时无法取得详细中文说明。'+qualityHint(issue);
      details.append(action('重试中文说明',()=>{const next=originalIssue(issue,fragment,index);details.replaceWith(next);next.open=true;},true));
    });
    return details;
  }
  function focusReview() {
    const pane=document.querySelector('.pdf-fragment-pane');
    const detail=pane?.querySelector('.pdf-quality-detail');if(detail)detail.open=true;
    document.querySelector('.pdf-review-comparison')?.classList.remove('source-collapsed');
    document.querySelector('.pdf-review-comparison')?.scrollIntoView({block:'start'});
    const focus=detail?.querySelector('summary') || pane?.querySelector('button');focus?.focus({preventScroll:true});
  }
  function beginCorrection(fragment) {
    const editor=document.getElementById('pdfPlanEditor');
    const existing=jsonValue(editor);
    if(existing.fragment_ids?.length && (existing.fragment_ids.length!==1 || existing.fragment_ids[0]!==fragment.id) &&
      !window.confirm('开始核对这张表会替换尚未入库的编辑内容。请先保存当前表格，或确认放弃后继续。'))return;
    selectedFragments=new Set([fragment.id]);
    if(existing.fragment_ids?.length!==1 || existing.fragment_ids[0]!==fragment.id || !existing.columns?.length){
      const plan=createManualPlan();
      plan.columns.forEach((column,index)=>{column.name=`列 ${index+1}`;column.source_columns[fragment.id]=index;});
      editor.value=JSON.stringify(plan,null,2);
    }
    invalidatePreview();
    const form=document.getElementById('pdfManualForm');renderManualPlanForm(editor,form);
    document.querySelector('.pdf-manual-details').open=true;
    const pane=document.querySelector('.pdf-fragment-pane');
    renderFragmentInspector(pane,document.querySelector('.pdf-source-viewer'));
    updateSelectionCount();focusReview();
    setStatus('正在核对当前这一张表。点击单元格可修改；填写下方列名、表头和依据，再检查入库预览。');
  }
  function editCell(fragment,row,column,parent) {
    parent.querySelector('.pdf-cell-editor')?.remove();
    const editor=document.getElementById('pdfPlanEditor'),plan=jsonValue(editor);
    const cell=fragment.rows[row][column];
    const saved=(plan.cell_corrections||[]).find(c=>c.fragment_id===fragment.id&&c.row===row&&c.column===column);
    const box=make('fieldset',undefined,'pdf-cell-editor');box.append(make('legend',`第 ${fragment.page} 页 · 第 ${row+1} 行 · 第 ${column+1} 列`),make('p',`原始识别：${cell.text || '（空白）'}`));
    const value=make('textarea');value.rows=2;value.value=saved?.text??cell.text??'';value.setAttribute('aria-label','核对后的单元格内容');
    const reason=make('input');reason.value=saved?.reason||'';reason.placeholder='例如：对照原页第几行，确认数字或空白';reason.setAttribute('aria-label','单元格核对依据');
    const feedback=make('p','','pdf-inline-feedback');feedback.setAttribute('role','status');
    box.append(make('label','原件中的内容（确实为空时可留空）'),value,make('label','核对依据'),reason,action('保存此格修改',()=>{
      if(!reason.value.trim()){feedback.textContent='请填写你在原件中看到的内容或位置，作为核对依据。';reason.focus();return;}
      const updated=jsonValue(editor);updated.cell_corrections=(updated.cell_corrections||[]).filter(c=>!(c.fragment_id===fragment.id&&c.row===row&&c.column===column));
      updated.cell_corrections.push({fragment_id:fragment.id,row,column,text:value.value,reason:reason.value.trim()});
      editor.value=JSON.stringify(updated,null,2);invalidatePreview();
      renderFragmentInspector(parent,document.querySelector('.pdf-source-viewer'));
      setStatus('此格修改已暂存，原始识别内容保留。请继续核对，再检查入库预览。');
    }),action('取消',()=>box.remove(),true),feedback);
    parent.querySelector('.pdf-quality-detail').open=false;
    parent.insertBefore(box,parent.querySelector('.pdf-raw-table'));value.focus({preventScroll:true});box.scrollIntoView({block:'nearest'});
  }

  function saveInlinePlan(plan,parent) {
    const editor=document.getElementById('pdfPlanEditor');editor.value=JSON.stringify(plan,null,2);invalidatePreview();
    renderManualPlanForm(editor,document.getElementById('pdfManualForm'));
    renderFragmentInspector(parent,document.querySelector('.pdf-source-viewer'));
    setStatus('修改已暂存。核对完成后设置列名和表头，检查预览并入库。');
  }
  function rowEditor(parent,title) {
    parent.querySelector('.pdf-cell-editor')?.remove();
    const box=make('fieldset',undefined,'pdf-cell-editor');box.append(make('legend',title));
    const reason=make('input');reason.setAttribute('aria-label','行修改的原件依据');reason.placeholder='原件第几页、哪一行，为什么需要这样修改';
    const feedback=make('p','','pdf-inline-feedback');feedback.setAttribute('role','status');
    box.append(make('label','原件依据'),reason,feedback);parent.insertBefore(box,parent.querySelector('.pdf-raw-table'));
    return {box,reason,feedback};
  }
  function editRowReason(fragment,row,parent,plan) {
    const {box,reason,feedback}=rowEditor(parent,`删除第 ${row+1} 行`);
    box.append(make('p','此行会从入库结果中排除，原始识别记录保留，可以恢复。'),action('确认删除此行',()=>{
      if(!reason.value.trim()){feedback.textContent='请说明这行为什么不应入库。';reason.focus();return;}
      plan.excluded_rows=(plan.excluded_rows||[]).filter(item=>!(item.fragment_id===fragment.id&&item.row===row));
      plan.excluded_rows.push({fragment_id:fragment.id,row,reason:reason.value.trim()});
      plan.header_rows[fragment.id]=(plan.header_rows[fragment.id]||[]).filter(item=>item.row!==row);
      plan.cell_corrections=(plan.cell_corrections||[]).filter(item=>!(item.fragment_id===fragment.id&&item.row===row));
      saveInlinePlan(plan,parent);
    }),action('取消',()=>box.remove(),true));reason.focus({preventScroll:true});
  }
  function insertRow(fragment,parent,existing=null) {
    const plan=jsonValue(document.getElementById('pdfPlanEditor'));
    const {box,reason,feedback}=rowEditor(parent,existing?'修改补充行':'补充原件中漏掉的一行');
    reason.value=existing?.reason||'';
    const position=make('select');position.setAttribute('aria-label','补行位置');
    for(let i=-1;i<fragment.rows.length;i++){const option=make('option',i===-1?'放在第一行之前':`放在原第 ${i+1} 行之后`);option.value=String(i);position.append(option);}
    position.value=String(existing?.after_row??fragment.rows.length-1);box.prepend(make('label','补在哪一行'),position);
    const fields=make('div',undefined,'pdf-insert-fields'),values=[];
    for(let i=0;i<fragment.rows[0].length;i++){const label=make('label',plan.columns.find(c=>c.source_columns[fragment.id]===i)?.name||`列 ${i+1}`),input=make('input');input.value=existing?.texts[i]||'';input.setAttribute('aria-label',`补行第 ${i+1} 列`);label.append(input);fields.append(label);values.push(input);}
    box.insertBefore(fields,reason.previousSibling);
    box.append(action('保存补充行',()=>{
      if(!reason.value.trim()){feedback.textContent='请写明原件中的位置和核对依据。';reason.focus();return;}
      plan.inserted_rows=(plan.inserted_rows||[]).filter(item=>item.id!==existing?.id);
      plan.inserted_rows.push({id:existing?.id||crypto.randomUUID(),fragment_id:fragment.id,after_row:Number(position.value),texts:values.map(input=>input.value),reason:reason.value.trim()});
      saveInlinePlan(plan,parent);
    }),action('取消',()=>box.remove(),true));values[0]?.focus({preventScroll:true});
  }
  function renderInsertedRow(fragment,row,parent) {
    const tr=make('tr',undefined,'pdf-inserted-row');tr.append(make('th','补行'));
    for(const text of row.texts)tr.append(make('td',text));
    const controls=make('td');controls.append(action('修改补行',()=>insertRow(fragment,parent,row),true),action('移除此补行',()=>{
      const plan=jsonValue(document.getElementById('pdfPlanEditor'));plan.inserted_rows=plan.inserted_rows.filter(item=>item.id!==row.id);saveInlinePlan(plan,parent);
    },true));tr.append(controls);return tr;
  }
  function renderFragment(fragment, parent) {
    const rows = fragment.rows || [];
    let plan={};try{plan=jsonValue(document.getElementById('pdfPlanEditor') || {value:'{}'});}catch{/* Navigation remains available while advanced JSON is incomplete. */}
    const editing=plan.fragment_ids?.includes(fragment.id) && plan.columns?.length>0 && plan.fragment_ids.length===selectedFragments.size && plan.fragment_ids.every(id=>selectedFragments.has(id));
    const issues=[...(fragment.quality?.issues||[])];
    if(fragment.quality?.status==='needs_review'&&!issues.length)issues.push({code:'extraction_needs_review'});
    const quality=make('details',undefined,'pdf-quality-detail');
    quality.append(make('summary',issues.length?`识别疑点 · ${issues.length} 项`:'核对与修改'));
    quality.open=Boolean(editing);
    for(const [index,issue] of issues.entries()){
      const item=make('div',undefined,'pdf-quality-item');item.append(make('p',qualityHint(issue)));
      if(editing){
        const issueIndex=issue.code==='extraction_needs_review'&& !fragment.quality?.issues?.length?-1:index;
        const saved=(plan.quality_reviews||[]).find(r=>r.fragment_id===fragment.id&&r.issue_index===issueIndex);
        const reason=make('input');reason.value=saved?.reason||'';reason.placeholder='写下核对的位置、看到的内容及处理结果';reason.setAttribute('aria-label',`疑点 ${index+1} 核对依据`);
        const feedback=make('span',saved?'已记录核对依据':'','pdf-inline-feedback');feedback.setAttribute('role','status');
        item.append(reason,action('已核对，记录依据',()=>{
          if(!reason.value.trim()){feedback.textContent='请先填写具体核对依据。';reason.focus();return;}
          const updated=jsonValue(document.getElementById('pdfPlanEditor'));
          updated.quality_reviews=(updated.quality_reviews||[]).filter(r=>!(r.fragment_id===fragment.id&&r.issue_index===issueIndex));
          updated.quality_reviews.push({fragment_id:fragment.id,issue_index:issueIndex,reason:reason.value.trim()});
          document.getElementById('pdfPlanEditor').value=JSON.stringify(updated,null,2);invalidatePreview();feedback.textContent='已记录；仍需处理标出的单元格并检查预览。';
        },true),feedback);
      }
      item.append(originalIssue(issue,fragment,index));quality.append(item);
    }
    quality.append(make('p','对照左侧原件，点击下方单元格修改。漏掉的行可以补上，多出的行可以删除；处理后填写列名、表头并检查入库结果。','muted'));
    parent.append(quality);
    const step=editing?action('下一步：设置列名和表头',()=>{const section=document.querySelector('.pdf-manual-details');section.open=true;section.scrollIntoView({block:'start'});},true):action('核对并修改这张表',()=>beginCorrection(fragment));
    step.classList.add('pdf-review-step');parent.append(step);
    if(fragment.header_rows?.includes(0) && rows[0]?.every(cell=>!cell.text?.trim())){
      parent.append(make('p','首行是空白的表头候选。若原页没有列名，请根据原页含义设置列名，并将这行设为表头或删除；下面的实际数据继续保留。','pdf-table-meta'));
    }
    const rowActions=make('div',undefined,'pdf-inline-row-actions');
    rowActions.append(action('补充漏掉的行',()=>{if(!editing)beginCorrection(fragment);insertRow(fragment,parent);},true));
    parent.append(rowActions);
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
      for (const [columnIndex,cell] of row.entries()) {
        const td = make('td');
        const corrected=(plan.cell_corrections||[]).find(c=>c.fragment_id===fragment.id&&c.row===rowIndex&&c.column===columnIndex);
        const edit=action(corrected?.text??cell.text??'',()=>{if(!editing)beginCorrection(fragment);editCell(fragment,rowIndex,columnIndex,parent);},true);edit.classList.add('pdf-cell-value');edit.setAttribute('aria-label',`修改第 ${rowIndex+1} 行第 ${columnIndex+1} 列`);td.append(edit);
        if(cell.uncertain||corrected){td.classList.add('pdf-cell-flag');td.append(make('small',corrected?'已核对修改':'需核对'));}
        if (cell.rowspan > 1 || cell.colspan > 1) td.title = `合并 ${cell.rowspan}×${cell.colspan}`;
        tr.append(td);
      }
      const excluded=(plan.excluded_rows||[]).some(item=>item.fragment_id===fragment.id&&item.row===rowIndex);
      const controls=make('td');controls.append(action(excluded?'恢复此行':'删除此行',()=>{
        if(!editing)beginCorrection(fragment);
        const editor=document.getElementById('pdfPlanEditor'),updated=jsonValue(editor);
        if(excluded){updated.excluded_rows=updated.excluded_rows.filter(item=>!(item.fragment_id===fragment.id&&item.row===rowIndex));saveInlinePlan(updated,parent);}
        else editRowReason(fragment,rowIndex,parent,updated);
      },true));tr.append(controls);tr.classList.toggle('pdf-excluded-row',excluded);body.append(tr);
      for(const inserted of (plan.inserted_rows||[]).filter(item=>item.fragment_id===fragment.id&&item.after_row===rowIndex))body.append(renderInsertedRow(fragment,inserted,parent));
    }
    for(const inserted of (plan.inserted_rows||[]).filter(item=>item.fragment_id===fragment.id&&item.after_row===-1))body.prepend(renderInsertedRow(fragment,inserted,parent));
    header.append(make('th','行操作'));
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
    selection.append(checkbox,make('span','手动调整时选择此表'));parent.append(selection);
    parent.append(make('p',`第 ${fragment.page} 页 · ${fragment.rows?.length || 0} 行 · ${Math.max(0,...(fragment.rows || []).map(row=>row.length))} 列`,'pdf-table-meta'));
    renderFragment(fragment,parent);
    const raw=make('details',undefined,'pdf-technical');raw.append(make('summary','来源定位信息'),make('code',fragment.id));
    if(fragment.bbox)raw.append(make('p',`位置：${JSON.stringify(fragment.bbox)}`));parent.append(raw);
    viewer.dataset.page=String(fragment.page);
    viewer.contentWindow?.postMessage({type:'pdf-source-page',page:fragment.page},location.origin);
  }

  function createSourceViewer() {
    sourceViewerCleanup();
    const viewer=make('iframe');viewer.title='PDF 原件预览';viewer.className='pdf-source-viewer';
    viewer.src='/pdf-table-preview.html';
    const savedEpoch=epoch,savedOperation=operation,savedUrl=sourceUrl;
    const receive=async event=>{
      if(event.source!==viewer.contentWindow || event.origin!==location.origin) return;
      if(!viewer.isConnected || !current(savedEpoch,savedOperation)) {window.removeEventListener('message',receive);return;}
      if(event.data?.type==='pdf-source-ready') {
        try {
          const data=await (await fetch(savedUrl,{signal:scopeController.signal})).arrayBuffer();
          if(!viewer.isConnected || !current(savedEpoch,savedOperation)) return;
          const style=getComputedStyle(root);
          viewer.contentWindow.postMessage({type:'pdf-source-open',data,page:Number(viewer.dataset.page)||1,theme:{
            '--font':style.fontFamily,'--text':style.color,'--background':style.getPropertyValue('--paper'),
            '--border':style.getPropertyValue('--line')
          }},location.origin);
        }catch(error){if(current(savedEpoch,savedOperation))setStatus('原件预览加载失败，请使用“打开原件”。',true);}
      }
      if(event.data?.type==='pdf-source-rendered')viewer.dataset.renderedPage=String(event.data.page);
      if(event.data?.type==='pdf-source-error')setStatus('原件预览失败，可使用“打开原件”查看。',true);
    };
    window.addEventListener('message',receive);
    sourceViewerCleanup=()=>window.removeEventListener('message',receive);
    // Remove handlers even when a scope switch removes the iframe before it replies.
    scopeController.signal.addEventListener('abort',()=>window.removeEventListener('message',receive),{once:true});
    return viewer;
  }

  function openProcessingModel() {
    if(host.openModelSettings)host.openModelSettings();
    else setStatus('请通过页面右上角的“字段模型”选择可用模型。',true);
  }

  function problemFor(state) {
    return state?.problem || {code:'LEGACY_FAILURE',title:'此前整理未完成',
      message:'此前记录没有保存具体失败原因。可重新整理以获取检查结果，已有表格会保留。',action:'retry',retryable:true};
  }

  function updateFileProgress() {
    for(const button of root.querySelectorAll('[data-automatic-draft]')) {
      const busy=processingDraftId===button.dataset.automaticDraft;
      button.disabled=Boolean(processingDraftId);button.setAttribute('aria-busy',String(busy));
      if(!button.dataset.idleLabel)button.dataset.idleLabel=button.textContent;
      button.textContent=busy?'正在整理…':button.dataset.idleLabel;
    }
    for(const row of root.querySelectorAll('[data-draft-id]')){
      const stage=fileProgress.get(row.dataset.draftId);
      row.classList.toggle('pdf-processing',Boolean(stage));
      if(stage)row.querySelector('.pdf-file-stage').textContent=stage;
    }
    const button=document.getElementById('pdfAutoProcess');
    if(button){button.disabled=Boolean(processingDraftId);button.setAttribute('aria-busy',String(processingDraftId===draft?.draft_id));}
    const progress=document.getElementById('pdfAutomaticProgress');
    if(progress)progress.textContent=fileProgress.get(draft?.draft_id)||'';
    const title=document.querySelector('#pdfAutomationSlot .pdf-automation strong');
    if(title){if(!title.dataset.idleLabel)title.dataset.idleLabel=title.textContent;title.textContent=processingDraftId===draft?.draft_id?'正在整理表格':title.dataset.idleLabel;}
  }

  async function autoProcess(id,existingJobId='') {
    if(processingDraftId)return;
    const savedEpoch=epoch,run=++automationRun;
    const active=()=>savedEpoch===epoch && savedEpoch===host.getAuthEpoch() && run===automationRun && !scopeController.signal.aborted;
    processingDraftId=id;fileProgress.set(id,'正在准备整理…');updateFileProgress();
    try{
      const started=existingJobId?{id:existingJobId}:await api(`/api/pdf/drafts/${encodeURIComponent(id)}/auto-process`,{method:'POST'});
      for(;;){
        const job=await api(`/api/pdf/jobs/${encodeURIComponent(started.id)}`);
        if(!active())return;
        fileProgress.set(id,job.stage||'正在整理表格…');updateFileProgress();
        if(['failed','cancelled'].includes(job.status))throw new Error(job.error||'整理未完成，请重试。');
        if(job.status==='completed'){
          const [catalog,published]=await Promise.all([api('/api/pdf/drafts'),api('/api/pdf/tables')]);
          if(!active())return;
          drafts=catalog.drafts||[];tables=published.tables||[];
          fileProgress.delete(id);processingDraftId='';
          const scroll=window.scrollY;renderDrafts();
          // A shorter completion message must not clamp a list scrolled to its
          // bottom. Reserve just the lost footer space before restoring scroll.
          const missing=scroll+window.innerHeight-document.documentElement.scrollHeight;
          if(missing>0){const list=document.getElementById('pdfDraftList');list.style.paddingBottom=`${(parseFloat(list.style.paddingBottom)||0)+missing}px`;}
          window.scrollTo({top:scroll,behavior:'instant'});
          if(draft?.draft_id===id){
            draft.automation=job.automatic_result||drafts.find(item=>item.draft_id===id)?.automation;
            renderAutomation(document.getElementById('pdfAutomationSlot'));
          }
          updateFileProgress();return job;
        }
        if(!['processing','queued','pending','running'].includes(job.status))throw new Error('无法读取整理任务状态，请刷新记录。');
        await new Promise(resolve=>setTimeout(resolve,1200));
      }
    }catch(error){if(active())setStatus(error.message,true);throw error;}
    finally{if(active()){processingDraftId='';fileProgress.delete(id);updateFileProgress();}}
  }

  function renderAutomation(parent) {
    if(!parent)return;
    let slot=parent.id==='pdfAutomationSlot'?parent:document.getElementById('pdfAutomationSlot');
    if(!slot){slot=make('div');slot.id='pdfAutomationSlot';parent.append(slot);}
    slot.replaceChildren();
    const state=displayedAutomation(draft),problem=(state?.problem||state?.error)?problemFor(state):null;
    const panel=make('div',undefined,'pdf-automation'),summary=make('div');
    const accepted=state?.accepted_count||0,pending=state?.attention_count||0;
    summary.append(make('strong',problem?problem.title:state?`已入库 ${accepted} 张${pending?` · ${pending} 张需要处理`:''}`:'自动整理表格'));
    summary.append(make('p',problem?problem.message:'可用表格自动入库，只有识别疑点需要处理。','muted'));
    const existing=tables.filter(item=>fileIdentity(item)===fileIdentity({source:source(draft.bundle)})).length;
    if(existing)summary.append(make('small',`已保存 ${existing} 张可查询表格，当前整理不会影响已有结果。`,'muted'));
    const progress=make('p','','pdf-automatic-progress');progress.id='pdfAutomaticProgress';progress.setAttribute('role','status');summary.append(progress);
    const controls=make('div',undefined,'pdf-automation-actions');
    if(problem?.action==='model_settings')controls.append(action('选择处理模型',openProcessingModel));
    const reviewOnly=pending>0 && !problem && state?.groups?.every(group=>group.status==='accepted'||group.problem?.action==='review');
    if(reviewOnly)controls.append(action('查看需处理表格',()=>{const drawer=slot.querySelector('.pdf-exception-drawer');if(drawer)drawer.open=true;}));
    else if(!problem || problem.retryable!==false){
      const run=action(state?.status==='completed'?'重新检查表格':state?'重新整理':'一键整理并入库',()=>autoProcess(draft.draft_id).catch(()=>{}));
      run.id='pdfAutoProcess';run.dataset.automaticDraft=draft.draft_id;controls.append(run);
    }
    if(problem?.action==='review')controls.append(make('span','请在下方选择表格，处理对应片段','muted'));
    panel.append(summary,controls);slot.append(panel);
    const inspection=make('div',undefined,'pdf-automation-inspection');slot.append(inspection);
    if(problem){const detail=make('details',undefined,'pdf-problem-detail');detail.append(make('summary','错误详情'),make('p',`错误代码：${problem.code}`));
      if(problem.input_chars)detail.append(make('p',`本次内容 ${problem.input_chars.toLocaleString()} 字符，单次上限 ${problem.limit_chars.toLocaleString()} 字符。`));inspection.append(detail);}
    const exceptions=(state?.groups||[]).filter(group=>group.status!=='accepted');
    if(exceptions.length){
      const list=make('div',undefined,'pdf-exceptions');
      for(const group of exceptions){
        const pages=[...new Set(fragments().filter(f=>group.fragment_ids.includes(f.id)).map(f=>f.page))];
        const item=make('div',undefined,'pdf-exception'),text=make('div');
        text.append(make('strong',`第 ${pages.join('、')} 页 · ${group.problem?.title||group.message||'需要处理'}`));
        if(group.problem?.message)text.append(make('p',group.problem.message,'muted'));
        const recover=group.problem?.action;
        if(recover==='model_settings')item.append(text,action('选择处理模型',openProcessingModel,true));
        else if(recover==='retry')item.append(text,action('重试未完成表格',()=>autoProcess(draft.draft_id).catch(()=>{}),true));
        else item.append(text,action('查看疑点',()=>{
          const candidate=fragments().find(f=>group.fragment_ids.includes(f.id)&&(f.quality?.status==='needs_review'||f.quality?.issues?.length||f.rows?.some(row=>row.some(cell=>cell.uncertain)))) || fragments().find(f=>group.fragment_ids.includes(f.id));
          activeFragmentIndex=Math.max(0,fragments().indexOf(candidate));
          selectedFragments=new Set(group.fragment_ids);
          invalidatePreview('已定位疑点；点击“核对并修改这张表”开始处理。');
          const parent=document.querySelector('.pdf-fragment-pane'),viewer=document.querySelector('.pdf-source-viewer');
          renderFragmentInspector(parent,viewer);updateSelectionCount();focusReview();
        },true));list.append(item);
      }
      const drawer=make('details',undefined,'pdf-exception-drawer');drawer.append(make('summary',`查看需要处理的表格（${exceptions.length}）`),list);inspection.append(drawer);
    }
    updateFileProgress();
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
    const currentPlan=jsonValue(editor);
    const widths = selected.map(fragment => Math.max(0, ...(fragment.rows || []).map(row => row.length)));
    if (widths.some(width => width !== widths[0]) || widths[0] < 1) {
      parent.append(make('p', '所选片段列数不同，表单不会尝试推断列对齐。请分别选择兼容片段或使用高级 JSON 明确填写。', 'warning'));
      return;
    }
    parent.append(make('p', '所有选择均由审核人填写。页面上下文只是未绑定的来源文本；表头、单位和跨页关系不会自动推断。', 'muted'));
    for (const fragment of selected) {
      const section = make('fieldset');
      section.append(make('legend', `第 ${fragment.page} 页的表格`));
      const headers = make('details');
      headers.append(make('summary', '手动标记表头行（未勾选行保留为数据）'));
      for (const [rowIndex, row] of (fragment.rows || []).entries()) {
        const label = make('label', undefined, 'row');
        const checkbox = make('input');
        checkbox.type = 'checkbox';
        checkbox.dataset.headerFragment = fragment.id;
        checkbox.dataset.headerRow = String(rowIndex);
        checkbox.checked=Boolean(currentPlan.header_rows?.[fragment.id]?.some(h=>h.row===rowIndex));
        label.append(checkbox, make('span', `第 ${rowIndex+1} 行 · ${row.map(cell => cell.text ?? '').join(' | ')}`));
        const reason = make('input');
        reason.type = 'text'; reason.placeholder = '标记为表头的原因';
        reason.dataset.headerReasonFragment = fragment.id;
        reason.dataset.headerReasonRow = String(rowIndex);
        reason.value=currentPlan.header_rows?.[fragment.id]?.find(h=>h.row===rowIndex)?.reason||'';
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
        checkbox.checked=Boolean(currentPlan.excluded_rows?.some(e=>e.fragment_id===fragment.id&&e.row===rowIndex));
        label.append(checkbox, make('span', `第 ${rowIndex+1} 行 · ${row.map(cell => cell.text ?? '').join(' | ')}`));
        const reason = make('input');
        reason.type = 'text'; reason.placeholder = '排除此行的原因';
        reason.dataset.excludeReasonFragment = fragment.id;
        reason.dataset.excludeReasonRow = String(rowIndex);
        reason.value=currentPlan.excluded_rows?.find(e=>e.fragment_id===fragment.id&&e.row===rowIndex)?.reason||'';
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
      name.type = 'text'; name.value = currentPlan.columns?.[columnIndex]?.name || `列 ${columnIndex + 1}`;
      name.setAttribute('aria-label', `第 ${columnIndex+1} 列名称`);
      name.dataset.columnName = String(columnIndex);
      const type = make('select');
      type.dataset.columnType = String(columnIndex);
      type.setAttribute('aria-label', `第 ${columnIndex+1} 列类型`);
      for (const [value, label] of [['', '请选择类型'], ['text', '文本'], ['decimal', '小数']]) {
        const option = make('option', label); option.value = value; type.append(option);
      }
      type.value=currentPlan.columns?.[columnIndex]?.type||'';
      const unit = make('input');
      unit.type = 'text'; unit.placeholder = '单位（可留空）'; unit.dataset.columnUnit = String(columnIndex);unit.value=currentPlan.columns?.[columnIndex]?.unit||'';
      unit.setAttribute('aria-label', `第 ${columnIndex+1} 列单位`);
      row.append(make('span', `第 ${columnIndex+1} 列`), name, type, unit);
      for (const fragment of selected) {
        const mapping = make('select');
        mapping.dataset.sourceColumn = String(columnIndex);
        mapping.dataset.sourceFragment = fragment.id;
        mapping.setAttribute('aria-label', `${fragment.id} 的逻辑列 ${columnIndex} 来源`);
        const prompt = make('option', `选择第 ${fragment.page} 页对应的列`); prompt.value = ''; mapping.append(prompt);
        for (let sourceIndex = 0; sourceIndex < widths[0]; sourceIndex += 1) {
          const option = make('option', `第 ${sourceIndex+1} 列`); option.value = String(sourceIndex); mapping.append(option);
        }
        mapping.value=String(currentPlan.columns?.[columnIndex]?.source_columns?.[fragment.id]??'');
        row.append(mapping);
      }
      columnSet.append(row);
    }
    parent.append(columnSet, action('保存列名和表头', () => {
      try {
        const plan = createManualPlan();
        const previous=jsonValue(editor);
        for(const key of ['cell_corrections','quality_reviews','inserted_rows'])if(previous[key])plan[key]=previous[key].filter(item=>selectedFragments.has(item.fragment_id));
        for (const fragment of selected) {
          const headers = [];
          for (const checkbox of parent.querySelectorAll(`[data-header-fragment="${CSS.escape(fragment.id)}"]`)) {
            if (!checkbox.checked) continue;
            const row = Number(checkbox.dataset.headerRow);
            const reason = parent.querySelector(`[data-header-reason-fragment="${CSS.escape(fragment.id)}"][data-header-reason-row="${row}"]`)?.value.trim();
            if (!reason) throw new Error(`第 ${fragment.page} 页的表头行 ${row+1} 需要原因。`);
            headers.push({row, reason});
          }
          plan.header_rows[fragment.id] = headers;
          for (const checkbox of parent.querySelectorAll(`[data-exclude-fragment="${CSS.escape(fragment.id)}"]`)) {
            if (!checkbox.checked) continue;
            const row = Number(checkbox.dataset.excludeRow);
            const reason = parent.querySelector(`[data-exclude-reason-fragment="${CSS.escape(fragment.id)}"][data-exclude-reason-row="${row}"]`)?.value.trim();
            if (!reason) throw new Error(`第 ${fragment.page} 页排除第 ${row+1} 行需要原因。`);
            plan.excluded_rows.push({fragment_id: fragment.id, row, reason});
          }
        }
        plan.columns = plan.columns.map((column, index) => {
          const name = parent.querySelector(`[data-column-name="${index}"]`).value.trim();
          if (!name) throw new Error(`第 ${index+1} 列需要列名。`);
          const type = parent.querySelector(`[data-column-type="${index}"]`).value;
          if (!type) throw new Error(`第 ${index+1} 列请选择文本或小数类型。`);
          const unit = parent.querySelector(`[data-column-unit="${index}"]`).value.trim();
          const source_columns = Object.fromEntries(selected.map(fragment => {
            const value = parent.querySelector(`[data-source-column="${index}"][data-source-fragment="${CSS.escape(fragment.id)}"]`)?.value;
            if (value === undefined || value === '') throw new Error(`第 ${index+1} 列请选择第 ${fragment.page} 页对应的原始列。`);
            return [fragment.id, Number(value)];
          }));
          return {...column, name, type, source_columns,
            ...(unit ? {unit} : {})};
        });
        editor.value = JSON.stringify(plan, null, 2);
        editor.dispatchEvent(new Event('input', {bubbles: true}));
        setStatus('列名和表头已保存。下一步点击“验证并预览”，检查数据后确认入库。');
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
    const detail=document.getElementById('pdfDraftDetail');
    detail.replaceChildren();
    const heading=make('div',undefined,'pdf-review-heading');
    const back=action('返回文件列表',showFileList,true);back.id='pdfBackToFiles';
    heading.append(back,make('h2',drafts.find(item=>item.draft_id===id)?.source?.file_name||'表格处理结果'));
    const loading=make('div','正在读取表格与原件…','pdf-detail-loading');loading.setAttribute('role','status');
    detail.append(heading,loading);openDetail();
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
      if (current(savedEpoch, savedOperation)) {
        loading.textContent=error.message;loading.classList.add('error');
        loading.append(action('重新打开',()=>selectDraft(id),true));
        setStatus(error.message,true);
      }
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
    const title=make('div');title.append(make('h2',src.file_name || 'PDF 文件'),make('small','表格处理结果'));
    const sourceLink=make('a','打开原件');sourceLink.href=sourceUrl;sourceLink.target='_blank';sourceLink.rel='noopener noreferrer';
    heading.append(back,title,sourceLink);target.append(heading);
    renderAutomation(target);
    const provenance=make('details',undefined,'pdf-technical');
    provenance.append(make('summary','文件与解析信息'),make('p',`SHA256：${src.sha256 || '未知'} · 解析器：${parser.engine || '未知'} ${parser.version || ''}${parserProfile} · 提取状态：未独立验证。`));
    target.append(provenance);
    const cleaning=bundle.preprocessing;if(cleaning?.status==='cleaned')target.append(make('p',`已在识别副本中去除文字水印，上传原件保留。覆盖 ${cleaning.pages?.length||0} 页。`,'muted'));
    const viewer=createSourceViewer();
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
    const original=make('div',undefined,'pdf-original-pane');
    const originalHeading=make('div',undefined,'pdf-original-heading');originalHeading.append(make('h3','原始文件'),action('收起原件',event=>{
      sourceAndTables.classList.add('source-collapsed');
    },true));original.append(originalHeading,viewer);
    const showOriginal=action('显示原件',()=>sourceAndTables.classList.remove('source-collapsed'),true);showOriginal.classList.add('pdf-show-source');
    const fragmentList=make('div',undefined,'pdf-fragment-pane');
    sourceAndTables.append(showOriginal,original,fragmentList);target.append(sourceAndTables);
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
    mappingSection.append(make('h3','检查入库内容'),make('p','核对流程：修改单元格并记录依据 → 保存列名、表头和单位 → 验证并预览 → 确认入库。原件无法辨认时，请先更换文件。','muted'));
    const selectionCount=make('span','','pdf-selection-count');selectionCount.id='pdfSelectionCount';mappingSection.append(selectionCount);
    const manualTitle = make('h3', '设置列名与表头');
    const manualForm = make('div');manualForm.id='pdfManualForm';
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
    const manualDetails=make('details',undefined,'pdf-manual-details');manualDetails.append(make('summary','设置列名与表头'),make('p','先设置列名和单位；原表中用作表头的行请单独勾选。保存字段设置后，再检查预览。','muted'),manualTitle,manualForm,planDetails);
    const controls = make('div', undefined, 'row');
    controls.style.marginTop = '12px';
    const reparseControls=make('section',undefined,'pdf-reparse-controls');
    reparseControls.append(make('h3','重新读取文件'),make('p','适合文件没有读全或识别服务已经调整后再试。已有入库表格继续保留；原件本身看不清时，请更换清晰文件。','muted'));
    if(config.engine==='remote_vlm'){
      reparseControls.append(action('重新读取原文件',()=>reparseDraft('standard','auto').catch(error=>setStatus(error.message,true)),true),make('p','当前服务统一使用已配置的识别模型，不提供模型档位或读取方式选择。同一文件和相同配置可能复用已有结果；已标出的疑点请在下方核对修改。','muted'));
    }else{
      const modeLabel=make('label','读取方式');const reparseMode=make('select');reparseMode.id='pdfReparseOcrMode';reparseMode.setAttribute('aria-label','文件读取方式');
      for(const [value,label] of [['auto','自动判断（建议）'],['ocr','按图片读取'],['txt','读取文件内的文字']]){const option=make('option',label);option.value=value;reparseMode.append(option);}
      modeLabel.append(reparseMode);reparseControls.append(modeLabel,make('p','自动判断：由服务选择方式。按图片读取：适合扫描件。读取文件内的文字：适合文字可复制的 PDF，扫描件可能读不到。','muted'));
      const row=make('div',undefined,'row');
      for(const [tier,label,description] of [['basic','按基础档重新识别','使用服务的基础档位重新读取。'],['standard','按标准档重新识别','改用服务的标准档位重新读取；效果和耗时取决于服务配置。']]){const option=make('div');option.append(action(label,()=>reparseDraft(tier,reparseMode.value).catch(error=>setStatus(error.message,true)),true),make('p',description,'muted'));row.append(option);}
      reparseControls.append(row);
    }
    // Visible beside the file status, never buried in the manual-mapping disclosure.
    target.insertBefore(reparseControls,provenance);
    const manualTemplate = action('重新设置所选表格的字段', () => {
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
    mappingSection.append(controls,options,manualDetails);
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
    const status=make('p','','pdf-detail-status');status.id='pdfDetailStatus';status.setAttribute('role','status');target.append(status);
    updateFileProgress();
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
    const previous=jsonValue(editor);
    const hasReview=['cell_corrections','quality_reviews'].some(key=>previous[key]?.length);
    for(const key of ['cell_corrections','quality_reviews'])if(previous[key])result.plan[key]=previous[key].filter(item=>ids.includes(item.fragment_id));
    editor.value = JSON.stringify(result.plan, null, 2);
    renderManualPlanForm(editor,document.getElementById('pdfManualForm'));
    renderFragmentInspector(document.querySelector('.pdf-fragment-pane'),document.querySelector('.pdf-source-viewer'));
    if(hasReview){await previewPlan();return;}
    preview = result.preview || null;
    previewSignature = JSON.stringify(result.plan);
    if (preview) renderPlanPreview(preview);
    updatePublishButton();
    setStatus('字段已识别。请对照原件核对数据后再发布。');
  }

  async function previewPlan() {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    const plan = jsonValue(document.getElementById('pdfPlanEditor'));
    if(plan.fragment_ids?.length!==selectedFragments.size || plan.fragment_ids.some(id=>!selectedFragments.has(id)))throw new Error('当前选择的表格已经改变，请先为所选表格设置字段，再预览。');
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
    document.getElementById('pdfPublicationSection')?.scrollIntoView({block:'start'});
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
    if(current(savedEpoch,savedOperation))renderAutomation(document.getElementById('pdfAutomationSlot'));
  }

  async function reparseDraft(tier, ocrMode) {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    if (!['basic', 'standard'].includes(tier) || !['auto', 'ocr', 'txt'].includes(ocrMode)) {
      throw new Error('复核模型或识别模式无效。');
    }
    const savedEpoch = epoch;
    const savedOperation = operation;
    if (host.unifiedUpload && draft.source?.knowledge_id) {
      const sourceId=draft.source.knowledge_id;
      setStatus('正在重新处理知识库中的原文件，离开页面后仍会继续。');
      await api('/api/v1/knowledge/'+encodeURIComponent(sourceId)+'/reparse',{method:'POST'});
      const result=await host.loadKnowledgeResult(sourceId);
      if(current(savedEpoch,savedOperation)&&result.draft_id)await selectDraft(result.draft_id);
      return;
    }
    setStatus('正在读取已保存的原文件并检查识别配置；相同配置可能复用已有结果。');
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
        await loadTables(savedEpoch, savedOperation);
        if (!current(savedEpoch, savedOperation)) return;
        if(job.automatic_result?.status==='completed'){
          showFileList();setStatus(job.stage||'表格已自动入库，可用于查询与问答。');return job;
        }
        if (await selectDraft(draftId) === false) throw new Error('PDF 已识别，但无法打开核对结果，请检查原件读取权限后重试。');
        if(job.automatic_result)setStatus(job.stage||'可用表格已入库，请处理剩余识别疑点。');
        return job;
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
      const response = await host.loadKnowledgeResult(id, {signal: scopeController.signal});
      if (!isCurrentSource()) return;
      if (!response.draft_id) {
        if(response.processing?.result?.message){setStatus(response.processing.result.message,Boolean(response.processing.attention_count));return;}
        throw new Error('这份文件还没有表格处理结果。请从目录的文件操作中重新处理，后台会自动完成。');
      }
      await loadDrafts(savedEpoch, savedOperation);
      await loadTables(savedEpoch, savedOperation);
      if (isCurrentSource()) await selectDraft(response.draft_id);
      if (isCurrentSourceScope()) setStatus('已打开这份知识库文件的处理结果。');
    } catch (error) {
      if (isCurrentSourceScope()) setStatus(error.message || '读取 PDF 原件失败。', true);
    } finally {
      if (isCurrentSourceScope()) sourceBusy = false;
    }
  }

  function closeKnowledgeSource() {
    closeDetail();
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
    if(job.native_processing&&job.source_knowledge_id){
      const result=await host.loadKnowledgeResult(job.source_knowledge_id);
      if(current(savedEpoch,savedOperation)&&result.draft_id)return selectDraft(result.draft_id);
      return;
    }
    return await pollJob(job.id, savedEpoch, savedOperation);
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
      const activeJob=jobs.find(job=>['processing','pending','queued','running'].includes(job.status));
      if(activeJob?.kind==='automatic' && activeJob.draft_id)void autoProcess(activeJob.draft_id,activeJob.id).catch(()=>{});
      else if(activeJob)void pollJob(activeJob.id,savedEpoch,savedOperation).catch(error=>{
        if(current(savedEpoch,savedOperation))setStatus(error.message,true);
      });
    } catch (error) {
      if (current(savedEpoch, savedOperation)) {
        root.replaceChildren(make('p', error.message, 'error'));
        renderedEpoch = -1;
      }
      if (throwOnError) throw error;
    }
  }

  function renderShell() {
    closeDetail();
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
    const detail=make('dialog');detail.id='pdfDraftDetail';detail.setAttribute('aria-label','PDF 表格处理详情');
    detail.addEventListener('cancel',event=>{event.preventDefault();showFileList();});root.append(detail);

  }

  window.addEventListener('structured-auth-scope-change', () => {
    epoch = host.getAuthEpoch();
    clearScope();
  });
  window.addEventListener('message', event => {
    if(event.source===window.parent && event.origin===location.origin && event.data?.type==='structured-view' && event.data.mode==='query')closeDetail();
  });
  window.addEventListener('structured-workspace-ready', () => loadWorkspace());
  window.addEventListener('pagehide', () => {
    closeDetail();
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
    try { return await uploadFile(file); }
    finally { if (savedEpoch === host.getAuthEpoch()) root.inert = false; }
  };
  host.openKnowledgeSource = openKnowledgeSource;
  host.closeKnowledgeSource = closeKnowledgeSource;
})();
