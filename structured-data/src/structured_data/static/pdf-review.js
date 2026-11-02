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
  let submitting = false;
  let publishedSignature = '';
  const fileProgress = new Map();
  let listScroll = 0;
  let returnFocus = null;


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
    button.addEventListener('click', event => {
      try {const result=handler(event);if(result?.catch)result.catch(error=>setStatus(error.message,true));}
      catch(error){setStatus(error.message,true);}
    });
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

  function invalidatePreview(message = '修改已暂存') {
    preview = null;
    previewSignature = '';
    const pane = document.getElementById('pdfPreview');
    if (pane) pane.replaceChildren(make('p', message, 'muted'));
    updatePublishButton();
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

  function readEditingPlan() { return jsonValue(document.getElementById('pdfPlanEditor')); }
  function writePlan(plan) {
    document.getElementById('pdfPlanEditor').value=JSON.stringify(plan,null,2);
    invalidatePreview();setStatus('修改已暂存');
  }

  function renderPlanPreview(result) {
    const pane = document.getElementById('pdfPreview');
    if (!pane) return;
    pane.replaceChildren();
    const publication = document.getElementById('pdfPublicationSection');
    if (publication) publication.hidden = false;
    const coverage = result.coverage || {};
    const note = make('p', `${coverage.logical_row_count ?? result.rows?.length ?? 0} 行 · ${result.columns?.length || 0} 列`);
    pane.append(note);
    const unresolved=coverage.unresolved_quality_issues||[];
    if(unresolved.length){
      const box=make('div',undefined,'pdf-preview-issues');
      const groups=new Map();
      for(const issue of unresolved){
        if(issue.code==='uncertain_cell'){
          const fragment=fragments().find(f=>f.id===issue.fragment_id);
          if(!fragment)continue;
          const item=make('div',undefined,'pdf-quality-item');
          item.append(make('span',`第 ${fragment.page} 页 · 第 ${issue.row+1} 行，第 ${issue.column+1} 列`));
          item.append(action('查看并修改',()=>locateCell(fragment,issue.row,issue.column),true));box.append(item);
        }else if(issue.code==='row_alignment_unverified'){
          const item=make('div',undefined,'pdf-quality-item');
          const pages=[...new Set(fragments().filter(f=>readEditingPlan().fragment_ids.includes(f.id)).map(f=>f.page))];
          item.append(make('span',`第 ${pages.join('、')} 页 · 跨页行对应关系`),action('行对应正确',()=>{
            const plan=readEditingPlan();plan.row_alignment_review={reason:'用户操作：确认跨页行对应关系'};writePlan(plan);item.replaceChildren(make('span','已确认'));
          },true));box.append(item);
        }else{
          const fragment=fragments().find(f=>f.id===issue.fragment_id);
          if(fragment)groups.set(fragment.id,fragment);
        }
      }
      for(const fragment of groups.values()){
        const item=make('div',undefined,'pdf-quality-item');
        item.append(make('span',`第 ${fragment.page} 页 · 整表核对（未提供具体位置）`),action('查看这张表',()=>{
          activeFragmentIndex=fragments().indexOf(fragment);renderFragmentInspector(document.querySelector('.pdf-fragment-pane'),document.querySelector('.pdf-source-viewer'));focusReview();
        },true));box.append(item);
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
        tr.append(td);
      }
      const provenance = make('td');
      const refs = Object.values(row.source_cells || {}).flat();
      const locations=make('details');locations.append(make('summary','查看来源'));
      for(const ref of refs)locations.append(make('p',ref.origin==='manual_transcription'?`第 ${ref.page} 页 · 补充行`:`第 ${ref.page} 页 · 第 ${ref.row+1} 行 · 第 ${ref.column+1} 列 · 原文：${ref.text??''}`));
      provenance.append(locations);
      tr.append(provenance);
      tbody.append(tr);
    }
    table.append(thead, tbody);
    wrap.append(table);
    pane.append(wrap);
    if (rows.length > 100) pane.append(make('p', `仅显示前 100 行；共 ${rows.length} 行。`, 'muted'));

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

  const actionReceipt = (fragment, action, row, column) =>
    `用户操作：${action}；第 ${fragment.page} 页${row===undefined?'':`，第 ${row+1} 行`}${column===undefined?'':`，第 ${column+1} 列`}`;
  function tableIssues(fragment) {
    const issues=fragment.quality?.issues||[];
    return issues.length?issues.map((issue,index)=>({...issue,issue_index:index})):
      fragment.quality?.status==='needs_review'?[{issue_index:-1}]:[];
  }
  function confirmTableFlags(fragment,parent) {
    const plan=readEditingPlan();
    plan.quality_reviews=(plan.quality_reviews||[]).filter(r=>r.fragment_id!==fragment.id);
    for(const issue of tableIssues(fragment))plan.quality_reviews.push({fragment_id:fragment.id,issue_index:issue.issue_index,reason:actionReceipt(fragment,'对照原件确认整张表')});
    saveInlinePlan(plan,parent);
  }
  function locateCell(fragment,row,column) {
    activeFragmentIndex=fragments().indexOf(fragment);
    if(!readEditingPlan().fragment_ids?.includes(fragment.id)&&!beginCorrection(fragment))return;
    const parent=document.querySelector('.pdf-fragment-pane');
    renderFragmentInspector(parent,document.querySelector('.pdf-source-viewer'));
    editCell(fragment,row,column,parent);
    const cell=parent.querySelector(`[data-source-row="${row}"][data-source-column="${column}"]`);
    cell?.classList.add('pdf-cell-focused');cell?.scrollIntoView({block:'nearest',inline:'nearest'});
  }
  function markHeaderRow(fragment,row,parent) {
    const plan=readEditingPlan(),headers=plan.header_rows[fragment.id]||[];
    if(headers.some(h=>h.row===row))plan.header_rows[fragment.id]=headers.filter(h=>h.row!==row);
    else{
      const cells=fragment.rows[row];
      if(cells.every(cell=>!String(cell.text||'').trim()))throw new Error(`第 ${row+1} 行为空，不能用作列名。`);
      for(const column of plan.columns){
        const sourceColumn=column.source_columns?.[fragment.id];
        if(sourceColumn===undefined)continue;
        const text=plan.cell_corrections?.find(c=>c.fragment_id===fragment.id&&c.row===row&&c.column===sourceColumn)?.text??cells[sourceColumn]?.text;
        if(text?.trim()){
          const old=column.name;column.name=text.trim();
          if(plan.key_columns)plan.key_columns=plan.key_columns.map(key=>key===old?column.name:key);
        }
      }
      plan.header_rows[fragment.id]=[...headers,{row,reason:actionReceipt(fragment,'用作列名行',row)}];
      plan.excluded_rows=(plan.excluded_rows||[]).filter(r=>!(r.fragment_id===fragment.id&&r.row===row));
      plan.cell_corrections=(plan.cell_corrections||[]).filter(c=>!(c.fragment_id===fragment.id&&c.row===row));
    }
    plan.quality_reviews=(plan.quality_reviews||[]).filter(r=>r.fragment_id!==fragment.id);
    saveInlinePlan(plan,parent);
  }
  function columnHeading(fragment,sourceColumn,plan) {
    const th=make('th');
    const index=plan.columns?.findIndex(c=>c.source_columns?.[fragment.id]===sourceColumn)??-1;
    if(index<0){th.textContent=`第 ${sourceColumn+1} 列`;return th;}
    const column=plan.columns[index],name=make('input');
    name.value=column.name;name.setAttribute('aria-label',`第 ${sourceColumn+1} 列名称`);name.dataset.columnName=String(index);
    name.addEventListener('input',()=>{
      const updated=readEditingPlan(),old=updated.columns[index].name;
      updated.columns[index].name=name.value;
      if(updated.key_columns)updated.key_columns=updated.key_columns.map(key=>key===old?name.value:key);
      writePlan(updated);
    });
    const options=make('details',undefined,'pdf-column-options');options.append(make('summary','格式与单位'));
    const type=make('select');type.setAttribute('aria-label',`第 ${sourceColumn+1} 列类型`);type.dataset.columnType=String(index);
    for(const [value,label] of [['text','文本'],['decimal','数字']]){const option=make('option',label);option.value=value;type.append(option);}type.value=column.type;
    type.addEventListener('change',()=>{const updated=readEditingPlan();updated.columns[index].type=type.value;writePlan(updated);});
    const unit=make('input');unit.value=column.unit||'';unit.placeholder='单位';unit.dataset.columnUnit=String(index);unit.setAttribute('aria-label',`第 ${sourceColumn+1} 列单位`);
    unit.addEventListener('input',()=>{const updated=readEditingPlan();if(unit.value.trim())updated.columns[index].unit=unit.value.trim();else delete updated.columns[index].unit;writePlan(updated);});
    options.append(type,unit);th.append(make('small',`第 ${sourceColumn+1} 列`),name,options);return th;
  }
  function focusReview() {
    const pane=document.querySelector('.pdf-fragment-pane');
    const detail=pane?.querySelector('.pdf-review-findings');
    document.querySelector('.pdf-review-comparison')?.classList.remove('source-collapsed');
    document.querySelector('.pdf-review-comparison')?.scrollIntoView({block:'start'});
    const focus=detail?.querySelector('button') || pane?.querySelector('button');focus?.focus({preventScroll:true});
  }
  function beginCorrection(fragment) {
    const editor=document.getElementById('pdfPlanEditor');
    const existing=jsonValue(editor);
    const group=displayedAutomation(draft)?.groups?.find(g=>g.status!=='accepted'&&g.fragment_ids?.includes(fragment.id));
    const ids=group?.plan?.fragment_ids || [fragment.id];
    if(existing.fragment_ids?.length && (existing.fragment_ids.length!==ids.length || !ids.every(id=>existing.fragment_ids.includes(id))) &&
      !window.confirm('开始核对这张表会替换尚未入库的编辑内容。请先保存当前表格，或确认放弃后继续。'))return false;
    selectedFragments=new Set(ids);
    if(existing.fragment_ids?.length!==ids.length || !ids.every(id=>existing.fragment_ids.includes(id)) || !existing.columns?.length){
      const plan=group?.plan ? structuredClone(group.plan) : createManualPlan();
      if(!group?.plan)plan.columns.forEach((column,index)=>{column.name=`列 ${index+1}`;column.source_columns[fragment.id]=index;});
      editor.value=JSON.stringify(plan,null,2);
    }
    invalidatePreview();
    const pane=document.querySelector('.pdf-fragment-pane');
    renderFragmentInspector(pane,document.querySelector('.pdf-source-viewer'));
    focusReview();
    setStatus('');
    return true;
  }
  function editCell(fragment,row,column,parent) {
    parent.querySelector('.pdf-cell-editor')?.remove();
    const editor=document.getElementById('pdfPlanEditor'),plan=readEditingPlan();
    const cell=fragment.rows[row][column];
    const saved=(plan.cell_corrections||[]).find(c=>c.fragment_id===fragment.id&&c.row===row&&c.column===column);
    const box=make('fieldset',undefined,'pdf-cell-editor');box.append(make('legend',`第 ${fragment.page} 页 · 第 ${row+1} 行 · 第 ${column+1} 列`),make('p',`原始识别：${cell.text || '（空白）'}`));
    const value=make('textarea');value.rows=2;value.value=saved?.text??cell.text??'';value.setAttribute('aria-label','核对后的单元格内容');
    box.append(make('label','原件中的内容（确实为空时可留空）'),value,action('保存修改',()=>{
      const updated=readEditingPlan();updated.quality_reviews=(updated.quality_reviews||[]).filter(r=>r.fragment_id!==fragment.id);updated.cell_corrections=(updated.cell_corrections||[]).filter(c=>!(c.fragment_id===fragment.id&&c.row===row&&c.column===column));
      updated.cell_corrections.push({fragment_id:fragment.id,row,column,text:value.value,reason:actionReceipt(fragment,'保存单元格',row,column)});
      editor.value=JSON.stringify(updated,null,2);invalidatePreview();
      renderFragmentInspector(parent,document.querySelector('.pdf-source-viewer'));
      setStatus('修改已暂存');
    }),action('取消',()=>{box.remove();updatePublishButton();},true));
    parent.insertBefore(box,parent.querySelector('.pdf-raw-table'));updatePublishButton();value.focus({preventScroll:true});box.scrollIntoView({block:'nearest'});
  }

  function saveInlinePlan(plan,parent) {
    const editor=document.getElementById('pdfPlanEditor');
    editor.value=JSON.stringify(plan,null,2);invalidatePreview();
    renderFragmentInspector(parent,document.querySelector('.pdf-source-viewer'));
    setStatus('修改已暂存');
  }
  function rowEditor(parent,title) {
    parent.querySelector('.pdf-cell-editor')?.remove();
    const box=make('fieldset',undefined,'pdf-cell-editor');box.append(make('legend',title));
    parent.insertBefore(box,parent.querySelector('.pdf-raw-table'));updatePublishButton();
    return {box};
  }
  function editRowReason(fragment,row,parent,plan) {
    const {box}=rowEditor(parent,`删除第 ${row+1} 行？`);
    box.append(action('确认删除此行',()=>{
      plan.excluded_rows=(plan.excluded_rows||[]).filter(item=>!(item.fragment_id===fragment.id&&item.row===row));
      plan.excluded_rows.push({fragment_id:fragment.id,row,reason:actionReceipt(fragment,'删除行',row)});
      plan.header_rows[fragment.id]=(plan.header_rows[fragment.id]||[]).filter(item=>item.row!==row);
      plan.cell_corrections=(plan.cell_corrections||[]).filter(item=>!(item.fragment_id===fragment.id&&item.row===row));
      saveInlinePlan(plan,parent);
    }),action('取消',()=>{box.remove();updatePublishButton();},true));box.querySelector('button').focus({preventScroll:true});
  }
  function insertRow(fragment,parent,existing=null) {
    const plan=readEditingPlan();
    const {box}=rowEditor(parent,existing?'修改补充行':'补充一行');
    const position=make('select');position.setAttribute('aria-label','补行位置');
    for(let i=-1;i<fragment.rows.length;i++){const option=make('option',i===-1?'放在第一行之前':`放在原第 ${i+1} 行之后`);option.value=String(i);position.append(option);}
    position.value=String(existing?.after_row??fragment.rows.length-1);box.append(make('label','插入位置'),position);
    const fields=make('div',undefined,'pdf-insert-fields'),values=[];
    for(let i=0;i<fragment.rows[0].length;i++){const label=make('label',plan.columns.find(c=>c.source_columns[fragment.id]===i)?.name||`列 ${i+1}`),input=make('input');input.value=existing?.texts[i]||'';input.setAttribute('aria-label',`补行第 ${i+1} 列`);label.append(input);fields.append(label);values.push(input);}
    box.append(fields,action('保存补充行',()=>{
      plan.inserted_rows=(plan.inserted_rows||[]).filter(item=>item.id!==existing?.id);
      plan.inserted_rows.push({id:existing?.id||crypto.randomUUID(),fragment_id:fragment.id,after_row:Number(position.value),texts:values.map(input=>input.value),reason:actionReceipt(fragment,'补充行')});
      saveInlinePlan(plan,parent);
    }),action('取消',()=>{box.remove();updatePublishButton();},true));values[0]?.focus({preventScroll:true});
  }
  function renderInsertedRow(fragment,row,parent) {
    const tr=make('tr',undefined,'pdf-inserted-row');tr.append(make('th','补行'));
    for(const text of row.texts)tr.append(make('td',text));
    const controls=make('td');controls.append(action('修改补行',()=>insertRow(fragment,parent,row),true),action('移除此补行',()=>{
      const plan=readEditingPlan();plan.inserted_rows=plan.inserted_rows.filter(item=>item.id!==row.id);saveInlinePlan(plan,parent);
    },true));tr.append(controls);return tr;
  }
  function renderFragment(fragment, parent) {
    const rows = fragment.rows || [];
    let plan={};try{plan=jsonValue(document.getElementById('pdfPlanEditor') || {value:'{}'});}catch{/* Missing plan does not block source navigation. */}
    const editing=plan.fragment_ids?.includes(fragment.id) && plan.columns?.length>0 && plan.fragment_ids.length===selectedFragments.size && plan.fragment_ids.every(id=>selectedFragments.has(id));
    const issues=tableIssues(fragment);
    const remaining=issues.filter(issue=>!(plan.quality_reviews||[]).some(r=>r.fragment_id===fragment.id&&r.issue_index===issue.issue_index));
    const uncertain=[];
    for(const [row,cells] of rows.entries()){
      if(plan.header_rows?.[fragment.id]?.some(h=>h.row===row)||plan.excluded_rows?.some(r=>r.fragment_id===fragment.id&&r.row===row))continue;
      for(const [column,cell] of cells.entries())if(cell.uncertain&&!plan.cell_corrections?.some(c=>c.fragment_id===fragment.id&&c.row===row&&c.column===column))uncertain.push({row,column});
    }
    for(const issue of remaining)for(const cell of issue.cells||[]){
      const row=cell.row,column=cell.column;
      if(Number.isInteger(row)&&Number.isInteger(column)&&fragment.rows[row]?.[column]&&!uncertain.some(c=>c.row===row&&c.column===column)&&!plan.cell_corrections?.some(c=>c.fragment_id===fragment.id&&c.row===row&&c.column===column)&&!plan.header_rows?.[fragment.id]?.some(h=>h.row===row)&&!plan.excluded_rows?.some(r=>r.fragment_id===fragment.id&&r.row===row))uncertain.push({row,column});
    }
    if(remaining.length||uncertain.length){
      const quality=make('section',undefined,'pdf-review-findings');quality.append(make('strong',uncertain.length?`${uncertain.length} 个单元格待确认`:'整表核对'));
      for(const {row,column} of uncertain){const item=make('div',undefined,'pdf-quality-item');
        item.append(make('span',`第 ${row+1} 行，第 ${column+1} 列`),action('查看并修改',()=>locateCell(fragment,row,column),true));quality.append(item);
      }
      if(remaining.length){
        for(const row of fragment.header_rows||[]){
          const cells=fragment.rows[row];
          if(!Number.isInteger(row)||!Array.isArray(cells)||!cells.every(cell=>!String(cell.text||'').trim())||plan.excluded_rows?.some(r=>r.fragment_id===fragment.id&&r.row===row))continue;
          const blank=make('div',undefined,'pdf-quality-item');blank.append(make('span',`识别结果第 ${row+1} 行为空`),action('查看这一行',()=>{const target=parent.querySelector(`tr[data-source-row="${row}"]`);target?.classList.add('pdf-row-focused');target?.scrollIntoView({block:'nearest'});},true));quality.append(blank);
        }
        const item=make('div',undefined,'pdf-quality-item');
        item.append(make('span',`第 ${fragment.page} 页 · 整表（未提供具体错误位置）`));
        if(editing)item.append(action('已对照原件核对',()=>confirmTableFlags(fragment,parent),true));
        quality.append(item);
      }
      parent.append(quality);
    }
    if(!editing){const start=action('修改这张表',()=>beginCorrection(fragment),true);start.classList.add('pdf-review-step');parent.append(start);}
    const rowActions=make('div',undefined,'pdf-inline-row-actions');
    rowActions.append(action('补充漏掉的行',()=>{if(!editing&&!beginCorrection(fragment))return;insertRow(fragment,parent);},true));
    parent.append(rowActions);
    const wrap = make('div', undefined, 'pdf-raw-table scroll');
    const table = make('table'), head = make('thead'), header = make('tr');
    header.append(make('th', '行'));
    const width = Math.max(0, ...rows.map(row=>row.length));
    for (let i=0;i<width;i++) header.append(columnHeading(fragment,i,editing?plan:{}));
    head.append(header);
    const body = make('tbody');
    for (const [rowIndex,row] of rows.entries()) {
      const tr = make('tr');tr.dataset.sourceRow=String(rowIndex);
      const isHeader=plan.header_rows?.[fragment.id]?.some(h=>h.row===rowIndex);
      tr.classList.toggle('pdf-header-row',Boolean(isHeader));
      tr.append(make('th',isHeader?`${rowIndex+1} · 列名`:rowIndex+1));
      for (const [columnIndex,cell] of row.entries()) {
        const td = make('td');td.dataset.sourceRow=String(rowIndex);td.dataset.sourceColumn=String(columnIndex);
        const corrected=(plan.cell_corrections||[]).find(c=>c.fragment_id===fragment.id&&c.row===rowIndex&&c.column===columnIndex);
        const edit=action(corrected?.text??cell.text??'',()=>{if(!editing&&!beginCorrection(fragment))return;editCell(fragment,rowIndex,columnIndex,parent);},true);edit.classList.add('pdf-cell-value');edit.setAttribute('aria-label',`修改第 ${rowIndex+1} 行第 ${columnIndex+1} 列`);td.append(edit);
        if(cell.uncertain||corrected){td.classList.add('pdf-cell-flag');td.append(make('small',corrected?'已核对修改':'需核对'));}
        if (cell.rowspan > 1 || cell.colspan > 1) td.title = `合并 ${cell.rowspan}×${cell.colspan}`;
        tr.append(td);
      }
      const excluded=(plan.excluded_rows||[]).some(item=>item.fragment_id===fragment.id&&item.row===rowIndex);
      const controls=make('td');
      const menu=make('details',undefined,'pdf-row-menu');menu.append(make('summary','操作'));
      menu.append(action(isHeader?'改为数据行':'用这行作列名',()=>{if(!editing&&!beginCorrection(fragment))return;markHeaderRow(fragment,rowIndex,parent);},true));
      menu.append(action(excluded?'恢复此行':'删除此行',()=>{
        if(!editing&&!beginCorrection(fragment))return;
        const editor=document.getElementById('pdfPlanEditor'),updated=readEditingPlan();
        if(excluded){updated.excluded_rows=updated.excluded_rows.filter(item=>!(item.fragment_id===fragment.id&&item.row===rowIndex));saveInlinePlan(updated,parent);}
        else editRowReason(fragment,rowIndex,parent,updated);
      },true));controls.append(menu);tr.append(controls);tr.classList.toggle('pdf-excluded-row',excluded);body.append(tr);
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
    parent.append(make('p',`第 ${fragment.page} 页 · ${fragment.rows?.length || 0} 行 · ${Math.max(0,...(fragment.rows || []).map(row=>row.length))} 列`,'pdf-table-meta'));
    renderFragment(fragment,parent);
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
    summary.append(make('strong',problem?problem.title:state?.status==='processing'?'正在解析整理…':state?`解析完成 · 已入库 ${accepted} 张${pending?` · 待核对 ${pending} 张`:''}`:'待整理'));
    if(problem)summary.append(make('p',problem.message,'muted'));

    const progress=make('p','','pdf-automatic-progress');progress.id='pdfAutomaticProgress';progress.setAttribute('role','status');summary.append(progress);
    const controls=make('div',undefined,'pdf-automation-actions');
    if(problem?.action==='model_settings')controls.append(action('选择处理模型',openProcessingModel));
    const reviewOnly=pending>0 && !problem && state?.groups?.every(group=>group.status==='accepted'||group.problem?.action==='review');
    if(reviewOnly)controls.append(action('查看需处理表格',()=>{const drawer=slot.querySelector('.pdf-exception-drawer');if(drawer)drawer.open=true;}));
    else if(state?.status!=='completed' && (!problem || problem.retryable!==false)){
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
        text.append(make('strong',`第 ${pages.join('、')} 页 · ${group.problem?.title||'待核对'}`));
        if(group.problem?.message)text.append(make('p',group.problem.message,'muted'));
        const recover=group.problem?.action;
        if(recover==='model_settings')item.append(text,action('选择处理模型',openProcessingModel,true));
        else if(recover==='retry')item.append(text,action('重试未完成表格',()=>autoProcess(draft.draft_id).catch(()=>{}),true));
        else item.append(text,action('查看疑点',()=>{
          const candidate=fragments().find(f=>group.fragment_ids.includes(f.id)&&(f.quality?.status==='needs_review'||f.quality?.issues?.length||f.rows?.some(row=>row.some(cell=>cell.uncertain)))) || fragments().find(f=>group.fragment_ids.includes(f.id));
          activeFragmentIndex=Math.max(0,fragments().indexOf(candidate));
          if(candidate&&beginCorrection(candidate))slot.querySelector('.pdf-exception-drawer').open=false;
        },true));list.append(item);
      }
      const drawer=make('details',undefined,'pdf-exception-drawer');drawer.append(make('summary',`查看需要处理的表格（${exceptions.length}）`),list);inspection.append(drawer);
    }
    updateFileProgress();
  }

  async function selectDraft(id) {
    const savedEpoch = epoch;
    const savedOperation = ++operation;
    draft = null;
    activeFragmentIndex = 0;
    selectedFragments = new Set();
    preview = null;
    publishedSignature='';
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
      const pending=displayedAutomation(draft)?.groups?.find(g=>g.status!=='accepted');
      activeFragmentIndex=Math.max(0,fragments().findIndex(f=>pending?.fragment_ids?.includes(f.id)));
      renderSelectedDraft();
      if(pending||!tables.some(t=>t.draft_id===id)){const first=fragments()[activeFragmentIndex];if(first)beginCorrection(first);}
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
    const heading=make('div',undefined,'pdf-review-heading');
    const back=action('返回文件列表',showFileList,true);back.id='pdfBackToFiles';
    const title=make('div');title.append(make('h2',src.file_name || 'PDF 文件'),make('small','表格处理结果'));
    const sourceLink=make('a','打开原件');sourceLink.href=sourceUrl;sourceLink.target='_blank';sourceLink.rel='noopener noreferrer';
    heading.append(back,title,sourceLink);target.append(heading);
    renderAutomation(target);
    const editor=make('textarea');editor.id='pdfPlanEditor';editor.hidden=true;
    editor.value=JSON.stringify({bundle_sha256:draft.bundle_sha256,fragment_ids:[],header_rows:{},columns:[],excluded_rows:[],boundary_merges:[]});
    target.append(editor);
    const viewer=createSourceViewer();
    heading.append(action('重新解析',()=>reparseDraft('standard','auto'),true));
    const sourceAndTables=make('div',undefined,'pdf-review-comparison');
    const original=make('div',undefined,'pdf-original-pane');
    const originalHeading=make('div',undefined,'pdf-original-heading');originalHeading.append(make('h3','原始文件'),action('收起原件',event=>{
      sourceAndTables.classList.add('source-collapsed');
    },true));original.append(originalHeading,viewer);
    const showOriginal=action('显示原件',()=>sourceAndTables.classList.remove('source-collapsed'),true);showOriginal.classList.add('pdf-show-source');
    const fragmentList=make('div',undefined,'pdf-fragment-pane');
    sourceAndTables.append(showOriginal,original,fragmentList);target.append(sourceAndTables);
    renderFragmentInspector(fragmentList,viewer);
    const controls=make('div',undefined,'pdf-table-actions');
    controls.append(action('查看入库预览',()=>previewPlan(),true));target.append(controls);
    const confirmation = make('div', undefined, 'mapping-block');

    const publish = action('确认并入库', () => confirmPlan().catch(error => setStatus(error.message, true)));
    publish.id = 'pdfPublish';
    publish.disabled = true;
    confirmation.append(publish);
    const publishSection=make('div',undefined,'pdf-publication-section');publishSection.id='pdfPublicationSection';publishSection.hidden=true;
    const previewPane = make('div');
    previewPane.id = 'pdfPreview';
    previewPane.append(make('p', '选择片段并预览映射后，结果会显示在这里。', 'muted'));
    publishSection.append(make('h3','入库预览'),previewPane);target.append(publishSection);
    confirmation.classList.add('pdf-confirm-bar');target.append(confirmation);
    const status=make('p','','pdf-detail-status');status.id='pdfDetailStatus';status.setAttribute('role','status');target.append(status);
    updateFileProgress();
    updatePublishButton();
  }

  function updatePublishButton() {
    const publish = document.getElementById('pdfPublish');
    const editor = document.getElementById('pdfPlanEditor');
    let plan={};try{plan=jsonValue(editor||{value:'{}'});}catch{/* Invalid JSON cannot be submitted. */}
    if (publish) {
      const pendingEdit=Boolean(document.querySelector('.pdf-cell-editor'));
      const saved=publishedSignature===JSON.stringify(plan)&&!pendingEdit;
      publish.textContent=saved?'已入库':submitting?'正在保存…':'确认并入库';
      publish.disabled=saved||submitting||!plan.columns?.length||!plan.fragment_ids?.length;
      publish.closest('.pdf-confirm-bar').hidden=!plan.columns?.length;
    }
  }

  async function confirmPlan() {
    if(submitting)return;
    submitting=true;updatePublishButton();
    try {
      const savedEpoch=epoch,savedOperation=operation,id=draft?.draft_id;
      const pendingEdit=document.querySelector('.pdf-cell-editor');
      if(pendingEdit){pendingEdit.querySelector('button')?.click();if(pendingEdit.isConnected)throw new Error('请先完成当前修改。');}
      const plan=readEditingPlan();
      if(!preview || JSON.stringify(plan)!==previewSignature)await previewPlan();
      if(!current(savedEpoch,savedOperation)||draft?.draft_id!==id)return;
      if(preview?.coverage?.unresolved_quality_issues?.length){setStatus('还有待核对的内容，请处理下方标记。',true);return;}
      await publishPlan();
    } finally {submitting=false;updatePublishButton();}
  }

  async function previewPlan() {
    if (!draft) throw new Error('请先打开一个 PDF 草稿。');
    const plan = jsonValue(document.getElementById('pdfPlanEditor'));
    const names=new Set();
    for(const [index,column] of (plan.columns||[]).entries()){
      const name=column.name.trim();if(!name)throw new Error(`第 ${index+1} 列还没有名称。`);
      if(names.has(name.toLowerCase()))throw new Error(`列名“${name}”重复，请为每列填写不同名称。`);names.add(name.toLowerCase());
    }
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
    setStatus(result.coverage?.unresolved_quality_issues?.length?'请处理待核对的内容。':'检查通过，可以确认入库。');
  }

  async function publishPlan() {
    if (!preview) throw new Error('请先验证并预览当前计划。');
    if (preview.coverage?.unresolved_quality_issues?.length) throw new Error('请先逐项处理质量问题并重新预览。');
    const editor = document.getElementById('pdfPlanEditor');
    const plan = jsonValue(editor);
    if (JSON.stringify(plan) !== previewSignature) throw new Error('计划内容在预览后已改变，请重新预览。');
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
    publishedSignature=JSON.stringify(plan);
    updatePublishButton();
    setStatus('已入库，可用于查询与问答。');
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
