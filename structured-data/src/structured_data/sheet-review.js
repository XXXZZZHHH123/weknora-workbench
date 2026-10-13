/* Bounded, single-sheet review. Raw worksheet cells are never stored in drafts. */
(() => {
  'use strict';
  const originalForm = renderMappingForm;
  const palette = [
    ['#cd747c','#fff0f0','#cd747c20','#cd747c35'],
    ['#d1af4e','#fff7dc','#d1af4e25','#d1af4e3a'],
    ['#83a7d4','#edf4fc','#83a7d425','#83a7d43a'],
    ['#967dc6','#f3eefe','#967dc622','#967dc63a'],
    ['#64a897','#ebf7f2','#64a89722','#64a8973a']
  ];
  const ui = { mapping:null, states:{}, selected:null, view:null, filter:'pending',
    request:0, controller:null, sheetController:null, jobController:null,
    checkSignature:null, mode:null, drag:null, selection:null, anchor:null, history:[],
    sample:null, sampleController:null, focusCell:null, restore:false, editable:false, previewMode:'table' };
  const copy = v => JSON.parse(JSON.stringify(v));
  const colName = n => { let s='';for(;n>0;n=Math.floor((n-1)/26))s=String.fromCharCode(65+(n-1)%26)+s;return s; };
  const colIndex = s => /^[A-Z]{1,3}$/.test(s||'') ? [...s].reduce((n,c)=>n*26+c.charCodeAt(0)-64,0) : 0;
  const cellPoint = s => {const m=/^([A-Z]{1,3})([1-9][0-9]*)$/.exec(s||'');return m?{row:+m[2],col:colIndex(m[1])}:null;};
  const colorVars = i => {const p=palette[i%palette.length];return `--block-color:${p[0]};--block-soft:${p[1]};--block-tint:${p[2]};--block-selected:${p[3]}`;};
  const clean = mapping => {const m=copy(mapping);m.blocks.forEach(b=>delete b._review_id);return m;};
  const publishedStatuses = new Set(['trusted_existing','auto_ready','template_ready','confirmed']);
  const isPublished = s => publishedStatuses.has(s?.status);
  function canonical(value) {
    if(Array.isArray(value))return value.map(canonical);
    if(!value||typeof value!=='object')return value;
    return Object.fromEntries(Object.keys(value).sort()
      .map(k=>[k,canonical(value[k])]));
  }
  function blockSignature(b) {
    const normalized={...b,columns:(b.columns||[]).map(c=>({...c,dimensions:c.dimensions||{}}))};
    delete normalized._review_id;delete normalized.header_expectations;
    return JSON.stringify(canonical(normalized));
  }
  const draftKey = () => currentImport&&currentSheet ? ['sheet-review:v1',activeTenant||'local',currentImport.import_id,currentSheet.name,currentSheet.fingerprint].map(encodeURIComponent).join(':') : null;
  const idFor = () => 'b-'+(globalThis.crypto?.randomUUID?.()||Date.now().toString(36)+'-'+Math.random().toString(36).slice(2));
  function isChartOnly(s) {
    const blocks = s.block_count ?? s.mapping?.blocks?.length ?? 0;
    return s.layout === 'chart_or_image' && !blocks && (s.rows || 0) <= 2;
  }
  function sheetVisible(s, needle) {
    if (needle && !s.name.toLowerCase().includes(needle)) return false;
    if (ui.filter === 'all') return true;
    if (ui.filter === 'charts') return isChartOnly(s);
    return s.status === 'needs_review' && !isChartOnly(s);
  }
  function sheetMeta(s) {
    const blocks = s.block_count ?? s.mapping?.blocks?.length ?? 0;
    const charts = s.chart_count || s.charts?.chart_count || 0;
    if (isChartOnly(s)) return `图表页 · ${charts} 个图 · ${s.rows || 0} 行 × ${s.columns || 0} 列`;
    return `${statusLabels[s.status] || s.status} · ${s.rows || 0} 行 × ${s.columns || 0} 列 · ${blocks} 个区块${charts ? ' · ' + charts + ' 个图' : ''}`;
  }
  function ensureBlocks(m) {
    const seen=new Set();
    m.blocks.forEach((b,i)=>{
      if(!b._review_id||seen.has(b._review_id))b._review_id=idFor();seen.add(b._review_id);
      const sig=blockSignature(b),prior=ui.states[b._review_id];
      if(!prior)ui.states[b._review_id]={enabled:true,checked:false,signature:sig};
      else if(prior.signature!==sig){prior.checked=false;prior.signature=sig;}
      const original=currentSheet?.mapping?.blocks?.[i],state=ui.states[b._review_id];
      state.published=isPublished(currentSheet)&&!!original&&sig===blockSignature(original)&&!state.rangePending;
      if(state.published&&currentSheet.status==='confirmed')state.checked=true;
    });
    Object.keys(ui.states).forEach(id=>{if(!seen.has(id))delete ui.states[id];});
    if(!seen.has(ui.selected))ui.selected=m.blocks[0]?._review_id||null;
    return m;
  }
  function currentMapping() {
    if($('mappingEditor'))ui.mapping=ensureBlocks(JSON.parse($('mappingEditor').value));
    return ui.mapping;
  }
  function saveDraft() {
    const key=draftKey();if(!key||!ui.mapping||!ui.editable)return;
    try {
      const text=JSON.stringify({version:1,at:Date.now(),mapping:ui.mapping,states:ui.states,selected:ui.selected});
      if(text.length>500000)throw Error('草稿过大');localStorage.setItem(key,text);
      const keys=Object.keys(localStorage).filter(k=>k.startsWith('sheet-review%3Av1:')||k.startsWith('sheet-review:v1:'));
      if(keys.length>30)keys.map(k=>{try{return [k,JSON.parse(localStorage.getItem(k)).at||0]}catch{return [k,0]}}).sort((a,b)=>a[1]-b[1]).slice(0,keys.length-30).forEach(([k])=>localStorage.removeItem(k));
    } catch {if($('draftNotice'))$('draftNotice').textContent='浏览器无法保存草稿，请在离开前完成确认。';}
  }
  function stopViewRequests() {ui.controller?.abort();ui.sheetController?.abort();ui.jobController?.abort();ui.sampleController?.abort();ui.request++;ui.view=null;ui.drag=null;ui.selection=null;ui.anchor=null;ui.sample=null;ui.focusCell=null;ui.history=[];ui.mode=null;}
  function rememberMapping() {
    if(!ui.mapping||mappingBusy)return;
    const item={mapping:copy(ui.mapping),states:copy(ui.states),selected:ui.selected};
    if(JSON.stringify(item)!==JSON.stringify(ui.history.at(-1)))ui.history.push(item);
    if(ui.history.length>20)ui.history.shift();
  }
  function undoMapping() {
    if(mappingBusy||!ui.history.length)return;
    const item=ui.history.pop();ui.states=item.states;ui.selected=item.selected;setMapping(item.mapping);
    $('mappingFeedback').textContent='已撤销上一次映射修改。';
  }
  async function timedApi(path,options={},controller=new AbortController(),ms=30000) {
    const timer=setTimeout(()=>controller.abort(),ms);
    try{return await api(path,{...options,signal:controller.signal});}
    catch(e){if(e.name==='AbortError')throw Error('请求已取消或超时，请重试。');throw e;}
    finally{clearTimeout(timer);}
  }
  const enabledBlocks = () => (ui.mapping?.blocks||[]).filter(b=>ui.states[b._review_id]?.enabled);
  const submittedMapping = () => clean({...ui.mapping,blocks:enabledBlocks()});
  function invalidateCheck(message='区块已修改，请重新检查全部选定数据。') {
    ui.checkSignature=null;mappingConfirmed=false;
    if($('mappingPreview'))$('mappingPreview').innerHTML='';
    if($('mappingFeedback')){$('mappingFeedback').className='review-feedback';$('mappingFeedback').textContent=message;}
  }
  invalidateMapping = function() {
    ui.sampleController?.abort();ui.sample=null;
    try {currentMapping();saveDraft();updateCardStates();renderLegend();renderGrid();}catch{}
    invalidateCheck();updateConfirmState();
  };
  setMapping = function(m) {
    ui.sampleController?.abort();ui.sample=null;ui.selection=null;ui.drag=null;
    Object.values(ui.states).forEach(state=>{state.rangePending=false;});
    ui.mapping=ensureBlocks(copy(m));$('mappingEditor').value=JSON.stringify(ui.mapping,null,2);
    renderMappingForm(ui.mapping);invalidateCheck();saveDraft();renderLegend();renderGrid();updateConfirmState();
  };
  selectImport = async function() {
    saveDraft();stopViewRequests();const id=$('importSelect').value;if(!id)return;
    const version=++reviewVersion,controller=new AbortController();ui.sheetController=controller;
    const r=await timedApi('/api/imports/'+encodeURIComponent(id)+'?summary_only=true',{},controller);
    if(version!==reviewVersion)return;currentImport=r;currentSheet=null;ui.mapping=null;ui.states={};
    const sheets=r.sheets||[];updateImportSummary();
    if(!sheets.some(s=>sheetVisible(s,'')))ui.filter='all';renderSheetList();
    $('sheetDetail').innerHTML='<p class="grid-loading">选择工作表，查看原表与对应区块。</p>';
    const first=sheets.findIndex(s=>sheetVisible(s,''));if(sheets.length)await selectSheet(first<0?0:first);
  };
  function updateImportSummary() {
    const sheets=currentImport?.sheets||[],ready=sheets.filter(isPublished).length;
    $('importSummary').textContent=`${currentImport?.source_file||''} · ${sheets.length} 张工作表 · ${ready} 张已可使用 · ${sheets.filter(s=>s.status==='needs_review').length} 张待确认`;
  }
  function tabState(s){return isPublished(s)?'ready':s.status==='needs_review'?'pending':'quiet';}
  function renderSheetList() {
    $('sheetList').innerHTML=(currentImport?.sheets||[]).map((s,i)=>({s,i})).filter(({s})=>sheetVisible(s,'')||(ui.filter==='pending'&&s.name===currentSheet?.name&&isPublished(s))).map(({s,i})=>`<button class="sheet-tab" data-sheet-index="${i}" aria-pressed="${s.name===currentSheet?.name}" title="${esc(s.name+' · '+sheetMeta(s))}"><i class="sheet-state ${tabState(s)}" aria-hidden="true"></i><strong>${esc(s.name)}</strong><small class="sr-only">${esc(sheetMeta(s))}</small></button>`).join('')||'<p class="muted" style="font-size:12px">没有匹配的工作表。</p>';
    document.querySelectorAll('[data-sheet-filter]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.sheetFilter===ui.filter)));
    renderSheetMenu();
  }
  function renderSheetMenu() {
    const host=$('sheetMenuList');if(!host)return;
    const needle=$('sheetSearch').value.trim().toLowerCase();
    host.innerHTML=(currentImport?.sheets||[]).map((s,i)=>({s,i})).filter(({s})=>!needle||s.name.toLowerCase().includes(needle)).map(({s,i})=>`<button data-sheet-index="${i}" title="${esc(sheetMeta(s))}">${esc(s.name)}<small>${esc(statusLabels[s.status]||s.status)}</small></button>`).join('')||'<p class="muted">没有匹配的工作表。</p>';
    if($('allSheets'))$('allSheets').textContent=`全部 ${currentImport?.sheets?.length||0} 张⌄`;
  }
  selectSheet = async function(index) {
    if(!currentImport)return;const s=currentImport.sheets[index];if(!s)return;
    saveDraft();stopViewRequests();const version=++reviewVersion,importId=currentImport.import_id,controller=new AbortController();ui.sheetController=controller;
    currentSheet=null;mappingConfirmed=false;mappingBusy=false;ui.checkSignature=null;
    $('sheetDetail').innerHTML='<p class="grid-loading">正在读取当前工作表…</p>';
    const sheet=await timedApi(`/api/imports/${encodeURIComponent(importId)}/sheet?name=${encodeURIComponent(s.name)}`,{},controller);
    if(version!==reviewVersion)return;Object.assign(s,{status:sheet.status,layout:sheet.layout,block_count:sheet.mapping?.blocks?.length||0,reasons:sheet.reasons});updateImportSummary();currentSheet=sheet;ui.mapping=null;ui.states={};ui.selected=null;renderSheetList();renderSheet(sheet);
    if(isChartOnly(sheet)){showPreview('charts');return;}
    showPreview('table');await loadWindow(1,1);
  };
  renderSheet = function(s) {
    const chartOnly = isChartOnly(s);
    ui.editable = !['empty','trusted_existing'].includes(s.status) && !chartOnly;
    let seed = copy(s.mapping || {sheet:s.name,fingerprint:s.fingerprint,blocks:[]});
    ui.restore = false;
    if (ui.editable) try {
      const d = JSON.parse(localStorage.getItem(draftKey()) || 'null');
      if (d?.version === 1 && d.mapping?.sheet === s.name && d.mapping?.fingerprint === s.fingerprint && Array.isArray(d.mapping.blocks) && d.mapping.blocks.length <= 100) {
        seed = d.mapping; ui.states = d.states || {}; ui.selected = d.selected; ui.restore = true;
      }
    } catch {}
    // Range input text is not part of a saved mapping until Apply is clicked.
    Object.values(ui.states).forEach(state=>{state.rangePending=false;});
    ui.mapping = ensureBlocks(seed);
    const chartCount = s.charts?.chart_count || 0;
    const switcher = chartCount && !chartOnly ? `<div class="view-switch"><button type="button" data-preview-mode="table" aria-pressed="true">数据表</button><button type="button" data-preview-mode="charts" aria-pressed="false">图表 ${chartCount}</button></div>` : '';
    const strip=ui.editable?`<div class="block-strip"><div id="blockLegend" class="block-chips"></div><button id="addMappingBlock" class="text-button" type="button">+ 添加区块</button><button id="undoMapping" class="text-button" type="button">↶ 撤销</button></div>`:'<div id="blockLegend" hidden></div>';
    const inspector=ui.editable?`<aside id="blockEditor" class="block-editor sheet-cards"><div class="editor-heading"><strong>字段与范围核对</strong><small>点击来源，对照左侧原表</small></div><div id="mappingForm"></div><div class="editor-footer"><label><input id="ackBlock" type="checkbox"><span id="ackBlockLabel">我已核对这个区块</span></label><button id="excludeCurrentBlock" class="exclude-block" type="button">这块不导入</button></div></aside>`:'<div id="mappingForm" hidden></div>';
    const source=`<div class="sheet-source"><div class="source-head"><h3>${esc(s.name)}</h3>${sheetStatusBadge(s)}${switcher}<button id="reviewFocus" class="text-button" type="button">展开核对</button></div><div id="tablePane" class="table-pane"><div class="view-tools"><button id="previousRows" class="secondary" type="button">↑ 上一段</button><button id="nextRows" class="secondary" type="button">↓ 下一段</button><button id="previousCols" class="secondary" type="button">← 左边</button><button id="nextCols" class="secondary" type="button">右边 →</button><input id="jumpCell" aria-label="定位原表单元格" placeholder="例如 AG90"><button id="jumpToCell" class="secondary" type="button">定位</button><span id="viewSummary" class="window-summary"></span></div><div id="sheetGrid" class="sheet-grid-wrap" tabindex="0" aria-label="当前工作表原表视图"><p class="grid-loading">正在加载原表…</p></div><div class="grid-caption"><strong id="selectedCaption"></strong><span>单击定位 · 拖动框选 · Shift 扩展 · Esc 取消</span></div><div id="rangeSelection" class="range-selection-actions" hidden><span>临时选区 <b id="selectionRange"></b></span><button id="applySelection" type="button">用于当前区块</button><button id="newBlockSelection" class="secondary" type="button">建立新区块</button><button id="cancelSelection" class="text-button" type="button">取消</button></div><div id="viewWarning" class="source-warning" role="status"></div><div id="selectionHint" class="source-note"></div></div><div id="chartPane" class="chart-pane" hidden></div></div>`;
    const footer=ui.editable?`<div class="review-footer"><div class="footer-progress" id="confirmHint" role="status"></div><div class="footer-actions"><button id="suggestMapping" class="secondary" type="button">AI 辅助生成建议</button><button id="confirmMapping" type="button">检查并导入这张表</button></div></div><p id="draftNotice" class="source-note">${ui.restore?'已恢复这张表的本地草稿。完整检查需要重新执行。':'调整会自动保存在当前浏览器；切换工作表后可继续核对。'}</p><div id="mappingFeedback" class="review-feedback" role="status"></div><details class="full-check-results"><summary>完整检查结果</summary><div id="mappingPreview" class="review-results"></div></details>`:`<p class="readonly-sheet muted">${chartOnly?'这张表只有图，不需要确认单元格导入。源表导入后，曲线才会关联到已导入的单元格。':s.status==='trusted_existing'?'本表沿用已核验的专用解析规则，通用区块编辑不覆盖该规则。':'此工作表没有需要确认的数据区块。'}</p>`;
    $('sheetDetail').innerHTML=`<div class="review-workspace">${strip}<div class="review-panes${ui.editable?'':' readonly'}">${source}${inspector}</div>${footer}${s.reasons?.length?`<details class="review-explanation"><summary>识别说明</summary><ul>${reasonsList(s.reasons)}</ul></details>`:''}<textarea id="mappingEditor" class="hidden">${esc(JSON.stringify(ui.mapping))}</textarea></div>`;
    $('mappingEditor').value = JSON.stringify(ui.mapping, null, 2);
    renderMappingForm(ui.mapping); renderLegend(); bindReviewEvents(); updateConfirmState();
    if (chartOnly) showPreview('charts');
  };
  renderMappingForm = function(m) {
    originalForm(m);
    $('mappingForm').querySelectorAll('.mapping-block').forEach((panel,i)=>{
      const b=m.blocks[i],state=ui.states[b._review_id];panel.dataset.blockId=b._review_id;panel.style.cssText=colorVars(i);
      const summary=panel.querySelector('summary');summary.innerHTML=`<span class="card-title"><i class="color-square"></i><span>${esc(b.name||'区块 '+(i+1))}</span></span><span class="card-state ${state?.enabled?(state.checked?'checked':''):'excluded'}">${state?.enabled?(state.checked?'✓ 已核对':'待确认'):'不导入'}</span>`;
      const body=document.createElement('div');body.className='block-body';
      const columns=panel.querySelector(':scope > .scroll');
      const settings=document.createElement('details');settings.className='block-settings';settings.innerHTML=`<summary>表头第 ${esc((b.header_rows||[]).join('、'))} 行 · 修改时间与类别来源</summary>`;
      [...panel.children].filter(el=>el!==summary&&el!==columns).forEach(el=>settings.append(el));
      body.append(settings);panel.append(body);
      const range=document.createElement('div');range.className='card-range';range.textContent=rangeLabel(b);summary.after(range);
      body.insertAdjacentHTML('afterbegin',`<div class="range-editor"><div class="row"><input data-range-input value="${esc(dataRangeLabel(b))}" aria-label="区块数据范围"><button class="secondary" data-map-action="apply-range" data-block="${i}">应用</button><button class="secondary" data-map-action="draw-range" data-block="${i}">框选调整</button></div><small>调整数据行和数值列；表头、日期或类别来源保持原设置。列可分别增删。</small></div>`);
      if(columns){
        columns.classList.add('field-table');columns.classList.toggle('matrix-fields',b.layout==='matrix');
        body.insertAdjacentHTML('beforeend','<h4 class="fields-heading">字段核对 <small>来源、名称和单位一起核对</small></h4>');body.append(columns);
        columns.querySelector('th').textContent='列 / 来源';
        columns.querySelectorAll('tr[data-column]').forEach((row,ci)=>{
          row.querySelector('td').insertAdjacentHTML('beforeend',`<span class="field-origin" data-field-origin="${ci}"></span>`);
          row.querySelector('.metric').insertAdjacentHTML('beforeend',`<small class="field-raw" data-field-raw="${ci}"></small>`);
          const remove=row.querySelector('[data-map-action="remove-column"]');if(remove){remove.textContent='×';remove.title='移除此数值列';remove.setAttribute('aria-label',`移除第 ${ci+1} 个数值列`);}
        });
      }
      body.insertAdjacentHTML('beforeend',`<div class="block-facts">${blockFacts(b)}</div><div class="block-sample"><div class="fields-heading">存储记录样本<button type="button" class="secondary" data-map-action="sample-block" data-block="${i}">预览几条记录</button></div><div class="block-sample-content" role="status">样本只用于核对；导入前仍检查全部选定数据。</div></div>`);
      body.querySelector('[data-map-action="remove-block"]')?.remove();
      panel.open=b._review_id===ui.selected;panel.classList.toggle('selected',panel.open);
      if(!ui.editable)body.querySelectorAll('input,select,button').forEach(el=>el.disabled=true);
    });
    updateDateSourceControls();updateFieldSources();updateCardStates();
  };
  function extent(b) {
    const cols=[...(b.columns||[]).map(c=>colIndex(c.column)),colIndex(b.date_column),colIndex(b.category_column),colIndex(b.row_label_column)].filter(Boolean);
    return {start:Math.min(b.data_start_row||1,...(b.header_rows||[b.data_start_row||1])),end:b.data_end_row||currentSheet?.rows||1,left:Math.min(...cols,16384),right:Math.max(...cols,1),cols};
  }
  function rangeLabel(b) {const e=extent(b);return `${colName(e.left)}${e.start}:${colName(e.right)}${e.end}${b.data_end_row==null?' · 数据至表末行':''}${b.date_cell?' · 共用日期 '+b.date_cell:''}`;}
  function blockFacts(b) {
    const time=b.layout==='matrix'?`横向期间 · 指标在 ${b.row_label_column||'未设置'} 列`:b.date_mode==='shared_cell'?`共用日期 ${b.date_cell||'未设置'}`:b.date_mode==='none'?'没有时间字段':`${b.date_column||'未设置'} 列 · ${b.date_mode==='period'?'原始期间标签':'每行日期'}`;
    return `<div><span>时间来源</span><strong>${esc(time)}</strong></div>${b.category_column?`<div><span>${esc(b.category_name||'类别')}来源</span><strong>${esc(b.category_column)} 列</strong></div>`:''}`;
  }
  function dataRangeLabel(b) {const cols=(b.columns||[]).map(c=>colIndex(c.column)).filter(Boolean);return `${colName(Math.min(...cols,1e5)===1e5?1:Math.min(...cols))}${b.data_start_row||1}:${colName(Math.max(...cols,1))}${b.data_end_row||currentSheet?.rows||1}`;}
  function hitBlock(b,row,col) {
    const data=row>=b.data_start_row&&row<=(b.data_end_row||currentSheet?.rows||1),header=(b.header_rows||[]).includes(row);
    const cols=[...(b.columns||[]).map(c=>colIndex(c.column)),colIndex(b.date_column),colIndex(b.category_column),colIndex(b.row_label_column)].filter(Boolean);
    if((data||header)&&cols.includes(col))return 'range';
    if((b.columns||[]).some(c=>(c.header_cells||[]).includes(colName(col)+row)))return 'header';
    const p=cellPoint(b.date_cell);if(p?.row===row&&p.col===col)return 'anchor';return null;
  }
  function renderLegend() {
    const host=$('blockLegend');if(!host)return;
    host.innerHTML=(ui.mapping?.blocks||[]).map((b,i)=>{const id=esc(b._review_id),state=ui.states[b._review_id],on=state?.enabled;return `<div class="block-chip${on?'':' disabled-block'}" style="${colorVars(i)}" title="${esc((b.name||'区块 '+(i+1))+' · '+rangeLabel(b))}"><button type="button" data-select-block="${id}" aria-pressed="${b._review_id===ui.selected}"><i class="color-square" aria-hidden="true"></i>区块 ${i+1}${(state?.published||state?.checked)&&on?'<span class="block-checked" aria-label="已导入或已核对">✓</span>':''}</button><button type="button" class="edit-block" data-edit-block="${id}">编辑</button></div>`;}).join('');
    if($('undoMapping'))$('undoMapping').disabled=mappingBusy||!ui.history.length;
  }
  function updateCardStates() {
    (ui.mapping?.blocks||[]).forEach((b,i)=>{
      const panel=[...document.querySelectorAll('[data-block-id]')].find(p=>p.dataset.blockId===b._review_id);if(!panel)return;
      const s=ui.states[b._review_id],badge=panel.querySelector('.card-state');
      if(badge){badge.className='card-state'+(s.enabled?(s.published||s.checked?' checked':''):' excluded');badge.textContent=s.enabled?(s.published?'✓ 已导入':s.checked?'✓ 已核对':'待核对'):'不导入';}
      const title=panel.querySelector('.card-title span');if(title)title.textContent=b.name||'区块 '+(i+1);
      const range=panel.querySelector('.card-range');if(range)range.textContent=rangeLabel(b);
      const facts=panel.querySelector('.block-facts');if(facts)facts.innerHTML=blockFacts(b);
      const exclude=panel.querySelector('.exclude-block');if(exclude)exclude.textContent=s.enabled?'这块不导入':'恢复导入';
      const input=panel.querySelector('[data-range-input]');if(input&&!input.dataset.pendingRange&&document.activeElement!==input)input.value=dataRangeLabel(b);
    });
    const b=selectedBlock(),state=b&&ui.states[b._review_id];
    if($('ackBlock')){$('ackBlock').checked=!!(state?.published||state?.checked);$('ackBlock').disabled=mappingBusy||!state?.enabled||!!state?.published;}
    if($('ackBlockLabel'))$('ackBlockLabel').textContent=state?.published?'已导入，修改后需重新核对':'我已核对这个区块';
    if($('excludeCurrentBlock')){$('excludeCurrentBlock').disabled=mappingBusy||!b;$('excludeCurrentBlock').textContent=state?.enabled?'这块不导入':'恢复导入';}
    updateFieldSources();
  }
  function selectedBlock(){return ui.mapping?.blocks.find(b=>b._review_id===ui.selected);}
  async function chooseBlock(id,locate=false) {
    ui.sampleController?.abort();ui.sample=null;ui.selected=id;ui.mode=null;ui.drag=null;ui.selection=null;if($('blockEditor'))$('blockEditor').hidden=false;renderLegend();
    document.querySelectorAll('[data-block-id]').forEach(panel=>{const on=panel.dataset.blockId===id;panel.open=on;panel.classList.toggle('selected',on);});
    const b=selectedBlock();if(b&&locate){const e=extent(b),r=ui.view?.range;if(!r||e.start<r.start_row||e.start>r.end_row||e.left<r.start_col||e.left>r.end_col)await loadWindow(e.start,e.left);}
    renderGrid();updateCardStates();updateConfirmState();saveDraft();
  }
  function updateFieldSources() {
    (ui.mapping?.blocks||[]).forEach(b=>{
      const panel=[...document.querySelectorAll('#mappingForm [data-block-id]')].find(p=>p.dataset.blockId===b._review_id);if(!panel)return;
      (b.columns||[]).forEach((c,i)=>{
        const origin=panel.querySelector(`[data-field-origin="${i}"]`),raw=panel.querySelector(`[data-field-raw="${i}"]`);
        if(origin)origin.innerHTML=(c.header_cells||[]).filter(cellPoint).map(cell=>`<button type="button" class="field-source" data-source-cell="${esc(cell)}" title="定位原表 ${esc(cell)}">${esc(cell)} ↗</button>`).join('');
        const sample=ui.view?.cells.find(cell=>cell.row===b.data_start_row&&cell.column===colIndex(c.column));
        if(raw)raw.textContent=sample?'原值：'+String(sample.display??sample.value??'空白'):'首行数据在当前窗口外';
      });
      const host=panel.querySelector('.block-sample-content');if(host&&!ui.sample)host.textContent='样本只用于核对；导入前仍检查全部选定数据。';
    });
  }
  async function locateSource(cell) {
    const p=cellPoint(cell);if(!p||p.row>currentSheet.rows||p.col>currentSheet.columns)return;
    ui.focusCell=p;const r=ui.view?.range;
    if(!r||p.row<r.start_row||p.row>r.end_row||p.col<r.start_col||p.col>r.end_col)await loadWindow(p.row,p.col);
    renderGrid();$('sheetGrid').querySelector(`[data-cell-row="${p.row}"][data-cell-col="${p.col}"]`)?.scrollIntoView({block:'nearest',inline:'nearest'});
  }
  async function previewBlock(index) {
    const b=currentMapping().blocks[index];if(!b)return;
    ui.sampleController?.abort();const controller=new AbortController();ui.sampleController=controller;
    const id=b._review_id,signature=blockSignature(b),version=reviewVersion;
    const panel=[...document.querySelectorAll('[data-block-id]')].find(p=>p.dataset.blockId===id),host=panel?.querySelector('.block-sample-content');if(!host)return;
    if(panel.querySelector('[data-range-input]').value.toUpperCase().trim()!==dataRangeLabel(b)){host.textContent='请先应用输入的范围，再预览这个区块的样本。';return;}
    host.textContent='正在读取这个区块的少量存储样本…';
    try {
      const mapping=clean({...ui.mapping,blocks:[b]});
      const result=await timedApi(`/api/imports/${encodeURIComponent(currentImport.import_id)}/sample-preview`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({mapping})},controller);
      if(controller.signal.aborted||version!==reviewVersion||id!==ui.selected||signature!==blockSignature(selectedBlock()))return;
      const block=result.blocks?.[0]||{},catalog=Object.fromEntries((block.series||[]).map(s=>[s.series_id,s]));
      ui.sample={id,signature};host.innerHTML=`<table class="sample-records"><thead><tr><th>时间 / 类别</th><th>字段</th><th>数值</th><th>来源</th></tr></thead><tbody>${(block.sample||[]).slice(0,12).map(row=>`<tr><td>${esc(row.date||row.period||'无时间')}${row.category?'<small>'+esc(row.category)+'</small>':''}</td><td>${esc(catalog[row.series_id]?.metric||'对应指标')}</td><td>${row.value==null?'缺失':esc(row.value)}</td><td><button type="button" class="field-source" data-source-cell="${esc(row.source_cell)}">${esc(row.source_cell)}</button></td></tr>`).join('')}</tbody></table><small>${esc(result.notice||'仅展示少量样本，完整检查通过后才能导入。')}</small>`;
    } catch(e){if(!controller.signal.aborted&&version===reviewVersion&&id===ui.selected)host.textContent=e.message;}
  }
  async function loadWindow(startRow,startCol) {
    if(!currentSheet||!currentImport)return;ui.controller?.abort();const controller=new AbortController();ui.controller=controller;
    const request=++ui.request,version=reviewVersion,sheet=currentSheet,importId=currentImport.import_id;
    const small=sheet.rows<=160&&sheet.columns<=64;
    const rowCount=small?Math.max(1,sheet.rows):60,colCount=small?Math.max(1,sheet.columns):24;
    const sr=small?1:Math.min(Math.max(1,startRow),Math.max(1,sheet.rows-rowCount+1)),sc=small?1:Math.min(Math.max(1,startCol),Math.max(1,sheet.columns-colCount+1));
    $('viewSummary').textContent='正在读取当前范围…';
    const params=new URLSearchParams({name:sheet.name,start_row:sr,start_col:sc,row_count:rowCount,col_count:colCount});
    try {
      const view=await timedApi(`/api/imports/${encodeURIComponent(importId)}/sheet-view?${params}`,{},controller,30000);
      if(request!==ui.request||version!==reviewVersion)return;
      if(!view.range||!Array.isArray(view.cells)||view.range.row_count>160||view.range.col_count>64||view.range.row_count*view.range.col_count>10240)throw Error('原表视图范围超出安全上限，请重新加载。');
      ui.view=view;renderGrid();$('viewSummary').textContent=`${small?'整张小表':'当前分段'} · ${colName(view.range.start_col)}${view.range.start_row}:${colName(view.range.end_col)}${view.range.end_row} / 共 ${view.total_rows} 行 × ${view.total_cols} 列`;
      $('previousRows').disabled=view.range.start_row<=1;$('nextRows').disabled=view.range.end_row>=view.total_rows;
      $('previousCols').disabled=view.range.start_col<=1;$('nextCols').disabled=view.range.end_col>=view.total_cols;
      $('viewWarning').textContent=(view.warnings||[]).map(describe).join('；');
      if(!small)$('viewWarning').textContent='大表分段显示，每次只保留当前范围。上下、左右翻段或输入单元格定位。'+($('viewWarning').textContent?' '+$('viewWarning').textContent:'');
    } catch(e){if(request!==ui.request||version!==reviewVersion)return;$('viewSummary').textContent='读取失败';$('viewWarning').textContent=e.message;if(!ui.view)$('sheetGrid').innerHTML='<p class="grid-loading">无法加载原表，请用上方定位按钮重试。</p>';}
  }
  function rgb(v) {const s=typeof v==='string'?v:v?.rgb;return /^[0-9a-f]{6,8}$/i.test(s||'')?'#'+s.slice(-6):'';}
  function sourceStyles(c) {
    const s=ui.view.styles?.[String(c?.style_id)]||{},f=s.font||{},a=s.alignment||{},rules=[];
    const bg=rgb(s.fill?.color),fg=rgb(f.color);if(bg)rules.push('--source-bg:'+bg);if(fg)rules.push('--source-fg:'+fg);
    if(f.bold)rules.push('font-weight:600');if(f.italic)rules.push('font-style:italic');if(f.underline)rules.push('text-decoration:underline');
    if(['left','center','right'].includes(a.horizontal))rules.push('text-align:'+a.horizontal);
    if(a.wrap_text)rules.push('white-space:pre-wrap');return rules.join(';');
  }
  function renderGrid() {
    if(!$('sheetGrid')||!ui.view)return;
    const v=ui.view,r=v.range,cells=new Map(v.cells.map(c=>[`${c.row},${c.column}`,c])),blocks=ui.mapping?.blocks||[],selected=selectedBlock(),selIndex=blocks.indexOf(selected);
    const selectedExtent=selected?extent(selected):null,merges=(v.merges||[]).filter(m=>m.end_row>=r.start_row&&m.start_row<=r.end_row&&m.end_col>=r.start_col&&m.start_col<=r.end_col);
    const mergeStarts=new Map(),covered=new Set();
    merges.forEach(m=>{const top=Math.max(m.start_row,r.start_row),left=Math.max(m.start_col,r.start_col),bottom=Math.min(m.end_row,r.end_row),right=Math.min(m.end_col,r.end_col);mergeStarts.set(`${top},${left}`,{...m,top,left,bottom,right});for(let rr=top;rr<=bottom;rr++)for(let cc=left;cc<=right;cc++)if(rr!==top||cc!==left)covered.add(`${rr},${cc}`);});
    let html='<table class="sheet-grid" aria-label="'+esc(currentSheet.name)+'"><colgroup><col style="width:38px">';
    for(let col=r.start_col;col<=r.end_col;col++)html+=`<col style="width:${Math.min(260,Math.max(85,(v.column_widths?.[col]||v.default_column_width||12)*7+10))}px">`;
    html+='</colgroup><thead><tr><th class="corner"></th>';
    for(let col=r.start_col;col<=r.end_col;col++)html+=`<th class="${selectedExtent?.cols.includes(col)?'active-axis':''}" style="${selIndex>=0?colorVars(selIndex):''}" scope="col">${colName(col)}</th>`;
    html+='</tr></thead><tbody>';
    for(let row=r.start_row;row<=r.end_row;row++){
      html+=`<tr style="height:${Math.min(90,Math.max(25,(v.row_heights?.[row]||v.default_row_height||20)*1.25))}px"><th class="row-number ${selected&&((row>=selectedExtent.start&&row<=selectedExtent.end)||cellPoint(selected.date_cell)?.row===row)?'active-axis':''}" style="${selIndex>=0?colorVars(selIndex):''}" scope="row">${row}</th>`;
      for(let col=r.start_col;col<=r.end_col;col++){
        const key=`${row},${col}`;if(covered.has(key))continue;const merge=mergeStarts.get(key),cell=merge?cells.get(`${merge.start_row},${merge.start_col}`)||cells.get(key):cells.get(key);
        const hits=blocks.map((b,i)=>({b,i,kind:ui.states[b._review_id]?.enabled&&hitBlock(b,row,col)})).filter(x=>x.kind),active=hits.find(x=>x.b._review_id===ui.selected)||hits[0];
        const classes=[active?'block-cell':'',active?.b._review_id===ui.selected?'active-block':'',active?.kind==='anchor'?'external-source':''];let styles=sourceStyles(cell);
        if(active){styles+=';'+colorVars(active.i);const b=active.b,color=palette[active.i%palette.length][0];if(!hitBlock(b,row-1,col))styles+=';border-top:2px solid '+color;if(!hitBlock(b,row+1,col))styles+=';border-bottom:2px solid '+color;if(!hitBlock(b,row,col-1))styles+=';border-left:2px solid '+color;if(!hitBlock(b,row,col+1))styles+=';border-right:2px solid '+color;}
        if(ui.focusCell?.row===row&&ui.focusCell?.col===col)classes.push('located-cell');
        const span=merge?` rowspan="${merge.bottom-merge.top+1}" colspan="${merge.right-merge.left+1}"`:'';
        const title=[`${colName(col)}${row}`,cell?.display??cell?.value??'',cell?.formula?'公式 '+cell.formula:'',active?.kind==='anchor'?'区块共用日期来源':'',hits.length>1?'多个区块共用此来源':''].filter(Boolean).join(' · ');
        html+=`<td class="${classes.filter(Boolean).join(' ')}" style="${esc(styles)}" data-cell-row="${row}" data-cell-col="${col}" ${active?`data-grid-block="${esc(active.b._review_id)}"`:''}${span} title="${esc(title)}">${esc(cell?.display??cell?.value??'')}`;
        html+='</td>';
      }html+='</tr>';
    }
    html+='</tbody></table>';const grid=$('sheetGrid'),scroll=[grid.scrollLeft,grid.scrollTop];grid.innerHTML=html;grid.scrollLeft=scroll[0];grid.scrollTop=scroll[1];grid.classList.toggle('selecting',Boolean(ui.mode||ui.drag));
    $('selectedCaption').textContent=selected?'当前选中：'+(selected.name||'区块 '+(selIndex+1))+' · '+rangeLabel(selected):'当前没有选定区块';
    paintSelection();updateFieldSources();
  }
  function showPreview(mode) {
    ui.previewMode=mode==='charts'?'charts':'table';
    const charts=ui.previewMode==='charts';
    if($('tablePane'))$('tablePane').hidden=charts;
    if($('chartPane'))$('chartPane').hidden=!charts;
    document.querySelectorAll('[data-preview-mode]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.previewMode===ui.previewMode)));
    if(charts)renderChartPane(currentSheet);
  }
  async function renderChartPane(s) {
    const pane=$('chartPane');if(!pane||!s)return;
    const charts=s.charts||{},count=charts.chart_count||0,images=charts.image_count||0;
    const note='图片不会被解析。图表本身不导入；源表导入后，曲线才会关联到已导入的单元格。';
    const fallback=`<div class="chart-grid"><article class="chart-card"><b>${esc(s.name)}</b><span>${count} 个图表 · ${images} 张图片</span><span>${note}</span></article></div>`;
    const dataset=$('dataset')?.value;
    if(!dataset||!count){pane.innerHTML=fallback;return;}
    pane.innerHTML='<p class="grid-loading">正在读取图表…</p>';
    try {
      const r=await api(`/api/datasets/${encodeURIComponent(dataset)}/charts?sheet=${encodeURIComponent(s.name)}&limit=12`);
      const items=r.charts||[];
      if(currentSheet?.name!==s.name||ui.previewMode!=='charts')return;
      if(!items.length){pane.innerHTML=fallback;return;}
      const label=status=>typeof chartStatus!=='undefined'&&chartStatus[status]||status||'';
      pane.innerHTML=`<div class="chart-grid">${items.map(c=>`<article class="chart-card"><b>${esc(c.title||'未命名图表')}</b><span>${(c.curves||[]).length} 条曲线 · ${esc(label(c.status))}</span></article>`).join('')}</div><p class="source-note">${note}</p>`;
    } catch { if(currentSheet?.name===s.name)pane.innerHTML=fallback; }
  }
  function parseRange(text) {
    const parts=String(text||'').toUpperCase().trim().split(':'),a=cellPoint(parts[0]),b=cellPoint(parts[1]);if(parts.length!==2||!a||!b||a.row>b.row||a.col>b.col||b.row>currentSheet.rows||b.col>currentSheet.columns)throw Error('请填写原表内的范围，例如 A4:D20。');
    return {top:a.row,bottom:b.row,left:a.col,right:b.col};
  }
  function cellDisplay(row,col){const c=ui.view?.cells.find(x=>x.row===row&&x.column===col);return String(c?.display??c?.value??'').trim();}
  function applyRange(index,range) {
    const m=copy(currentMapping()),b=m.blocks[index];if(!b)return;
    if(range.top<=Math.max(...(b.header_rows||[0])))throw Error('数据起始行必须在表头之后。表头可在右侧单独修改。');
    const excluded=[b.date_column,b.category_column,b.row_label_column].filter(Boolean),old=new Map(b.columns.map(c=>[c.column,c])),columns=[];
    for(let col=range.left;col<=range.right;col++){const name=colName(col);if(excluded.includes(name))continue;const prior=old.get(name);columns.push(prior|| (b.layout==='matrix'?{column:name,period:cellDisplay(Math.max(...b.header_rows),col),header_cells:b.header_rows.map(r=>name+r)}:{column:name,metric:cellDisplay(Math.max(...b.header_rows),col),unit:null,header_cells:b.header_rows.map(r=>name+r)}));}
    if(!columns.length)throw Error('范围里至少要保留一列数值。');rememberMapping();b.data_start_row=range.top;b.data_end_row=range.bottom;b.columns=columns;setMapping(m);
    $('mappingFeedback').textContent='已调整数据范围。表头和时间／类别来源保持原设置，请核对新增列的含义。';
  }
  function addRange(range) {
    if(range.bottom<=range.top)throw Error('请选择至少两行：第一行作表头，后面作为数据。');
    rememberMapping();const m=copy(currentMapping()),columns=[];for(let c=range.left;c<=range.right;c++)columns.push({column:colName(c),metric:cellDisplay(range.top,c),unit:null,header_cells:[colName(c)+range.top]});
    const b={_review_id:idFor(),name:'新数据区块 '+(m.blocks.length+1),date_mode:'none',header_rows:[range.top],data_start_row:range.top+1,data_end_row:range.bottom,columns};m.blocks.push(b);ui.selected=b._review_id;setMapping(m);
    $('mappingFeedback').textContent='已框选新区块：第一行暂作表头，没有推定日期。请核对表头、时间来源和指标名称。';
  }
  function startDraw(mode,id=ui.selected) {ui.mode=mode;ui.selected=id;ui.drag=null;$('selectionHint').textContent=mode==='add'?'在原表上按住并拖动，框选新区块（第一行暂作表头）。按 Esc 取消。':'按住并拖动选择新的数据范围，或使用卡片内的范围输入。按 Esc 取消。';$('sheetGrid').classList.add('selecting');}
  function selectedRange(a,b) {
    let range={top:Math.min(a.row,b.row),bottom:Math.max(a.row,b.row),left:Math.min(a.col,b.col),right:Math.max(a.col,b.col)};
    // A merged cell belongs to one selection even when its anchor is outside this window.
    for(let changed=true;changed;){changed=false;for(const m of ui.view?.merges||[]){
      if(m.end_row<range.top||m.start_row>range.bottom||m.end_col<range.left||m.start_col>range.right)continue;
      const next={top:Math.min(range.top,m.start_row),bottom:Math.max(range.bottom,m.end_row),left:Math.min(range.left,m.start_col),right:Math.max(range.right,m.end_col)};
      if(JSON.stringify(next)!==JSON.stringify(range)){range=next;changed=true;}
    }}return range;
  }
  function paintSelection() {
    const range=ui.drag?.moved?ui.drag.range:ui.selection?.range;
    $('sheetGrid')?.querySelectorAll('[data-cell-row]').forEach(cell=>{
      const row=+cell.dataset.cellRow,col=+cell.dataset.cellCol,on=!!range&&row<=range.bottom&&row+cell.rowSpan-1>=range.top&&col<=range.right&&col+cell.colSpan-1>=range.left;
      cell.classList.toggle('range-selection',on);
      for(const [cls,match] of [['selection-top',row<=range?.top],['selection-bottom',row+cell.rowSpan-1>=range?.bottom],['selection-left',col<=range?.left],['selection-right',col+cell.colSpan-1>=range?.right]])cell.classList.toggle(cls,on&&match);
    });
    if($('rangeSelection'))$('rangeSelection').hidden=!ui.selection;
    if(ui.selection){$('selectionRange').textContent=`${colName(range.left)}${range.top}:${colName(range.right)}${range.bottom}`;$('applySelection').textContent=ui.selection.mode==='add'?'建立新区块':`用于区块 ${ui.mapping.blocks.findIndex(b=>b._review_id===ui.selected)+1}`;}
  }
  function cancelSelection() {ui.drag=null;ui.selection=null;ui.mode=null;paintSelection();if($('selectionHint'))$('selectionHint').textContent='';}
  function applySelection(asNew=false) {
    if(mappingBusy||!ui.selection)return;
    const {range,mode}=ui.selection;
    try{if(asNew||mode==='add')addRange(range);else applyRange(ui.mapping.blocks.findIndex(b=>b._review_id===ui.selected),range);}catch(e){mappingError(e);}
  }
  function toggleIncluded() {
    const b=selectedBlock();if(!b||mappingBusy)return;rememberMapping();const state=ui.states[b._review_id];state.enabled=!state.enabled;state.checked=false;
    invalidateCheck('导入范围已变化，请重新检查全部选定数据。');saveDraft();updateCardStates();renderLegend();renderGrid();updateConfirmState();
  }
  function bindReviewEvents() {
    const grid=$('sheetGrid');
    if($('blockLegend'))$('blockLegend').onclick=e=>{const edit=e.target.closest('[data-edit-block]'),pick=edit||e.target.closest('[data-select-block]');if(pick)chooseBlock(edit?edit.dataset.editBlock:pick.dataset.selectBlock,true).then(()=>{if(edit)document.querySelector('.mapping-block.selected [data-column-field="metric"],.mapping-block.selected [data-column-field="period"]')?.focus();}).catch(mappingError);};
    $('mappingForm').onclick=e=>{
      const source=e.target.closest('[data-source-cell]');if(source){locateSource(source.dataset.sourceCell).catch(mappingError);return;}
      const summary=e.target.closest('.mapping-block>summary');if(summary){e.preventDefault();chooseBlock(summary.parentElement.dataset.blockId,true).catch(mappingError);return;}
      const button=e.target.closest('[data-map-action]');if(!button||!ui.editable)return;
      try {
        const m=currentMapping(),index=+button.dataset.block,b=m.blocks[index],action=button.dataset.mapAction;
        if(action==='apply-range'){applyRange(index,parseRange(button.closest('.mapping-block').querySelector('[data-range-input]').value));return;}
        if(action==='draw-range'){startDraw('resize',b._review_id);return;}
        if(action==='sample-block'){previewBlock(index).catch(mappingError);return;}
        rememberMapping();
        if(action==='remove-column')b.columns.splice(+button.dataset.column,1);
        if(action==='add-column')b.columns.push({column:'',metric:'',unit:null,header_cells:[]});
        if(action==='add-rule'){b.row_rules??=[];b.row_rules.push({row:b.data_start_row,metric:'',unit:null});}
        setMapping(m);
      }catch(err){mappingError(err);}
    };
    $('mappingForm').onfocusin=e=>{if(e.target.matches('input,select'))rememberMapping();};
    $('mappingForm').oninput=e=>{
      if(e.target.hasAttribute('data-range-input')){
        ui.sampleController?.abort();ui.sample=null;
        const panel=e.target.closest('[data-block-id]'),state=ui.states[panel.dataset.blockId],b=ui.mapping.blocks[+panel.dataset.block];state.checked=false;state.rangePending=e.target.value.toUpperCase().trim()!==dataRangeLabel(b);state.published=false;
        if(e.target.value.toUpperCase().trim()!==dataRangeLabel(b))e.target.dataset.pendingRange='true';else delete e.target.dataset.pendingRange;
        invalidateCheck('范围输入尚未应用，请先应用范围，再核对这个区块。');saveDraft();updateCardStates();renderLegend();updateConfirmState();return;
      }
      if(e.target.tagName==='INPUT')try{syncFormMapping();}catch(err){mappingError(err);}
    };
    $('mappingForm').onchange=e=>{if(e.target.hasAttribute('data-range-input'))return;try{syncFormMapping();if(e.target.dataset.field==='layout')renderMappingForm(currentMapping());else updateDateSourceControls();}catch(err){mappingError(err);}};
    if(ui.editable){$('mappingEditor').oninput=()=>{invalidateCheck();try{ui.mapping=ensureBlocks(JSON.parse($('mappingEditor').value));renderMappingForm(ui.mapping);saveDraft();renderLegend();renderGrid();}catch{$('mappingFeedback').textContent='高级设置尚不是完整有效的 JSON。';}updateConfirmState();};if($('addMappingBlock'))$('addMappingBlock').onclick=()=>startDraw('add');if($('previewMapping'))$('previewMapping').onclick=()=>previewMapping().catch(mappingError);if($('confirmMapping'))$('confirmMapping').onclick=()=>confirmMapping().catch(mappingError);if($('suggestMapping'))$('suggestMapping').onclick=()=>suggestMapping(false).catch(mappingError);}
    if($('ackBlock'))$('ackBlock').onchange=()=>{
      const b=currentMapping().blocks.find(b=>b._review_id===ui.selected);if(!b)return;
      const rangeInput=document.querySelector('.mapping-block.selected [data-range-input]');
      if($('ackBlock').checked&&rangeInput.value.toUpperCase().trim()!==dataRangeLabel(b)){$('ackBlock').checked=false;mappingError(Error('请先应用输入的范围，再核对这个区块。'));return;}
      if($('ackBlock').checked&&b.layout!=='matrix'&&b.columns.some(c=>!String(c.metric||'').trim())){$('ackBlock').checked=false;mappingError(Error('请先填写每个数值字段的指标名称。'));return;}
      rememberMapping();ui.states[b._review_id].checked=$('ackBlock').checked;saveDraft();updateCardStates();renderLegend();updateConfirmState();
    };
    if($('excludeCurrentBlock'))$('excludeCurrentBlock').onclick=toggleIncluded;
    if($('undoMapping'))$('undoMapping').onclick=undoMapping;
    if($('applySelection'))$('applySelection').onclick=()=>applySelection();
    if($('newBlockSelection'))$('newBlockSelection').onclick=()=>applySelection(true);
    if($('cancelSelection'))$('cancelSelection').onclick=cancelSelection;
    $('reviewFocus').onclick=()=>{const on=$('importReview').classList.toggle('review-focused');$('reviewFocus').textContent=on?'退出展开':'展开核对';};
    document.querySelectorAll('[data-preview-mode]').forEach(b=>b.onclick=()=>showPreview(b.dataset.previewMode));
    const page=(dr,dc)=>{const r=ui.view?.range;if(r)loadWindow(r.start_row+dr*r.row_count,r.start_col+dc*r.col_count);};
    $('previousRows').onclick=()=>page(-1,0);$('nextRows').onclick=()=>page(1,0);$('previousCols').onclick=()=>page(0,-1);$('nextCols').onclick=()=>page(0,1);
    $('jumpToCell').onclick=()=>{const p=cellPoint($('jumpCell').value.trim().toUpperCase());if(!p||p.row>currentSheet.rows||p.col>currentSheet.columns){$('viewWarning').textContent='请输入原表内的单元格，例如 AG90。';return;}loadWindow(p.row,p.col);};
    $('jumpCell').onkeydown=e=>{if(e.key==='Enter')$('jumpToCell').click();};
    document.querySelectorAll('[data-locate-region]').forEach(b=>b.onclick=()=>{const p=cellPoint(b.dataset.locateRegion.split(':')[0]);if(p)loadWindow(p.row,p.col);});
    if($('openSheetCharts'))$('openSheetCharts').onclick=async()=>{$('chartSheet').innerHTML=`<option value="${esc(currentSheet.name)}">${esc(currentSheet.name)}</option>`;await loadCharts();$('nativeCharts').scrollIntoView({behavior:'smooth'});};
    grid.onpointerdown=e=>{
      const cell=e.target.closest('[data-cell-row]');if(!cell||!ui.editable||mappingBusy||e.button!==0)return;
      e.preventDefault();ui.selection=null;const point={row:+cell.dataset.cellRow,col:+cell.dataset.cellCol};
      const anchor=e.shiftKey&&ui.anchor?ui.anchor:point;
      ui.drag={mode:ui.mode||'resize',start:anchor,end:point,range:selectedRange(anchor,point),x:e.clientX,y:e.clientY,moved:!!e.shiftKey,block:cell.dataset.gridBlock};
      if(!e.shiftKey)ui.anchor=point;grid.setPointerCapture(e.pointerId);paintSelection();
    };
    grid.onpointermove=e=>{
      if(!ui.drag)return;
      const cell=document.elementFromPoint(e.clientX,e.clientY)?.closest('[data-cell-row]');if(!cell||!grid.contains(cell))return;
      ui.drag.moved ||= Math.hypot(e.clientX-ui.drag.x,e.clientY-ui.drag.y)>4;
      ui.drag.end={row:+cell.dataset.cellRow,col:+cell.dataset.cellCol};ui.drag.range=selectedRange(ui.drag.start,ui.drag.end);
      if(!ui.paintFrame)ui.paintFrame=requestAnimationFrame(()=>{ui.paintFrame=null;paintSelection();});
      const rect=grid.getBoundingClientRect();
      if(e.clientY>rect.bottom-24)grid.scrollTop+=20;else if(e.clientY<rect.top+24)grid.scrollTop-=20;
      if(e.clientX>rect.right-24)grid.scrollLeft+=20;else if(e.clientX<rect.left+24)grid.scrollLeft-=20;
    };
    grid.onpointerup=e=>{
      const drag=ui.drag;if(!drag)return;ui.drag=null;ui.mode=null;
      if(grid.hasPointerCapture(e.pointerId))grid.releasePointerCapture(e.pointerId);
      if(drag.moved){ui.selection={range:drag.range,mode:drag.mode};paintSelection();}
      else{ui.selection=null;ui.focusCell=drag.end;if(drag.block)chooseBlock(drag.block).catch(mappingError);else renderGrid();}
    };
    grid.onpointercancel=cancelSelection;
    grid.onkeydown=e=>{if(e.key==='Escape'&&(ui.drag||ui.selection||ui.mode)){e.preventDefault();e.stopPropagation();cancelSelection();}};
  }
  readMapping = function() {
    currentMapping();const m=submittedMapping();if(!currentSheet||m.sheet!==currentSheet.name)throw Error('当前工作表已变化，请重新打开。');if(!m.blocks.length)throw Error('请保留至少一个要导入的区块。');
    for(const [i,b] of m.blocks.entries()){if(!b.columns?.length)throw Error(`区块 ${i+1} 至少需要一列数值。`);if(!Number.isInteger(b.data_start_row)||b.data_start_row<1)throw Error(`区块 ${i+1} 的数据起始行不正确。`);if(['shared_cell','none','period'].includes(b.date_mode)&&(!Number.isInteger(b.data_end_row)||b.data_end_row<b.data_start_row))throw Error(`区块 ${i+1} 需要明确的数据结束行。`);}
    return m;
  };
  updateConfirmState = function() {
    const included=enabledBlocks(),checkValid=ui.checkSignature===JSON.stringify(submittedMapping());mappingConfirmed=checkValid;
    const pending=included.filter(b=>!ui.states[b._review_id]?.checked&&!ui.states[b._review_id]?.published).length;
    const alreadyPublished=isPublished(currentSheet)&&included.length===(currentSheet.mapping?.blocks?.length||0)&&included.length===(ui.mapping?.blocks?.length||0)&&included.every(b=>ui.states[b._review_id]?.published);
    if($('confirmMapping')){$('confirmMapping').disabled=mappingBusy||!included.length||!!pending||alreadyPublished;$('confirmMapping').title=alreadyPublished?'本表已导入；修改映射后才需要重新核对和导入。':!included.length?'请至少保留一个要导入的区块。':pending?`还有 ${pending} 个区块需要核对字段。`:'';}
    if($('confirmHint'))$('confirmHint').innerHTML=alreadyPublished?'本表已导入，可直接查询<small>修改映射后，请重新核对并导入。</small>':included.length?`${pending?`还有 ${pending} 个区块待核对`:`${included.length} 个区块已核对`}<small>${mappingBusy?'正在检查和处理，请等待…':'导入前检查全部选定数据，通过后写入新版本。'}</small>`:`没有要导入的区块<small>恢复一个区块后才能确认这张表。</small>`;
    if($('undoMapping'))$('undoMapping').disabled=mappingBusy||!ui.history.length;
  };
  setMappingBusy = function(busy) {
    mappingBusy=busy;document.querySelectorAll('#mappingForm input,#mappingForm select,#mappingForm button,#addMappingBlock,#mappingEditor,#saveTemplate,#suggestMapping,#confirmMapping,#confirmGo,#aiContinue,#previewMapping,#ackBlock,#excludeCurrentBlock,#undoMapping,#applySelection,#newBlockSelection,#cancelSelection').forEach(el=>{el.disabled=busy;});
    for(const id of ['suggestMapping','confirmMapping','confirmGo','aiContinue','previewMapping'])if($(id))$(id).setAttribute('aria-busy',String(busy));updateCardStates();updateConfirmState();
  };
  async function scopedJob(id,version,onProgress) {
    const controller=new AbortController();ui.jobController=controller;
    for(;;){if(version!==reviewVersion||controller.signal.aborted)throw Error('工作表已切换，旧任务结果不会应用。');const r=await timedApi('/api/jobs/'+encodeURIComponent(id),{},controller);if(['processing','queued','pending','running'].includes(r.status)){onProgress?.(r.stage||'正在检查全部选定数据…');await new Promise(resolve=>setTimeout(resolve,1000));continue;}if(r.status==='failed')throw Error(r.error||'处理失败');return r.result||r;}
  }
  previewMapping = async function() {
    const mapping=readMapping(),signature=JSON.stringify(mapping),version=reviewVersion,importId=currentImport.import_id;setMappingBusy(true);invalidateCheck('正在检查全部选定数据；原表视图仅保留当前范围…');
    try {
      const job=await timedApi(`/api/imports/${encodeURIComponent(importId)}/preview?background=true`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({mapping})},new AbortController(),30000);
      if(version!==reviewVersion)return;
      const result=job.id?await scopedJob(job.id,version,s=>{if(version===reviewVersion)$('mappingFeedback').textContent=s;}):job;
      if(version!==reviewVersion||signature!==JSON.stringify(submittedMapping()))return;
      ui.checkSignature=signature;mappingConfirmed=true;$('mappingFeedback').textContent=`全部选定数据检查通过：${result.series_count} 个指标、${result.observation_count} 条记录。${result.notice||''}完整检查结果可展开查看。`;$('mappingPreview').innerHTML=blockPreviewHTML(result);saveDraft();
    }finally{if(version===reviewVersion)setMappingBusy(false);}
  };
  suggestMapping = async function(confirmed) {
    if(!confirmed){
      let cfg=null;try{cfg=await api('/api/model-settings');}catch{cfg=null;}
      const ready=!!(cfg&&cfg.has_api_key&&cfg.model);
      $('aiDialogText').textContent=ready?`会把当前表的少量表头样本发给「${cfg.model}」。这会消耗 token。结果只改区块范围和指标名，不导入。`:'还没有可用的模型。请先到模型设置填写模型名称和密钥。这次不会发送请求。';
      $('aiContinue').hidden=!ready;const dialog=$('aiDialog');if(!dialog.open)dialog.showModal();return;
    }
    if($('aiDialog')?.open)$('aiDialog').close();
    const version=reviewVersion;setMappingBusy(true);$('mappingFeedback').textContent='正在请求表头识别建议，结果需要人工确认…';
    try{const r=await timedApi(`/api/imports/${encodeURIComponent(currentImport.import_id)}/suggest`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sheet:currentSheet.name})});if(version!==reviewVersion)return;if(r.mapping){mappingBusy=false;rememberMapping();ui.states={};setMapping(r.mapping);}$('mappingFeedback').textContent=(r.notice||'已生成建议。')+' 请核对区块范围和字段，再确认这张表。';}finally{if(version===reviewVersion)setMappingBusy(false);}
  };
  confirmMapping = async function() {
    readMapping();
    const included=enabledBlocks(),excluded=(ui.mapping?.blocks||[]).filter(b=>!ui.states[b._review_id]?.enabled);
    if(included.some(b=>!ui.states[b._review_id]?.checked&&!ui.states[b._review_id]?.published))throw Error('请先逐块核对字段，再检查并导入这张表。');
    $('confirmList').innerHTML=included.map(b=>`<p>导入 ${esc(b.name||'区块')} · ${esc(rangeLabel(b))}</p>`).join('')+(excluded.length?`<p>不导入：${esc(excluded.map(b=>b.name||'区块').join('、'))}</p>`:'');
    const dialog=$('confirmDialog');if(!dialog.open)dialog.showModal();
  };
  async function publishMapping() {
    const mapping=readMapping(),version=reviewVersion,scopeEpoch=authEpoch,importId=currentImport.import_id,sheetName=currentSheet.name,key=draftKey();
    setMappingBusy(true);$('mappingFeedback').textContent='正在生成数据集新版本…';
    try {
      const job=await timedApi(`/api/imports/${encodeURIComponent(importId)}/confirm`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({mapping,save_template:!!$('saveTemplate')?.checked})});if(version!==reviewVersion)return;
      const result=await scopedJob(job.id,version,s=>{if(version===reviewVersion&&$('mappingFeedback'))$('mappingFeedback').textContent=s;});if(version!==reviewVersion)return;
      try{localStorage.removeItem(key);}catch{}ui.editable=false;note(`「${sheetName}」的选定区块已导入新版本。`);
      await refresh();if($('importSelect').value!==importId)await refreshImports(importId);
      if(result?.dataset_id&&!reviewOnly){$('dataset').value=result.dataset_id;await selectDataset();}
      notifyImportUpdated(importId,result?.dataset_id,'confirmed',scopeEpoch);
    }finally{if(version===reviewVersion)setMappingBusy(false);}
  }
  let confirmRunning=false;
  async function runConfirm() {
    if(confirmRunning||mappingBusy)return;confirmRunning=true;
    try{if($('confirmDialog')?.open)$('confirmDialog').close();await previewMapping();if(ui.checkSignature!==JSON.stringify(submittedMapping()))return;await publishMapping();}
    catch(e){mappingError(e);}finally{confirmRunning=false;}
  }
  $('aiCancel').onclick=()=>$('aiDialog').close();
  $('aiContinue').onclick=()=>suggestMapping(true).catch(mappingError);
  $('confirmBack').onclick=()=>$('confirmDialog').close();
  $('confirmGo').onclick=()=>runConfirm();
  $('sheetSearch').oninput=renderSheetMenu;
  if($('allSheets'))$('allSheets').onclick=()=>{const menu=$('sheetMenu');menu.hidden=!menu.hidden;$('allSheets').setAttribute('aria-expanded',String(!menu.hidden));if(!menu.hidden){renderSheetMenu();$('sheetSearch').focus();}};
  if($('sheetMenuList'))$('sheetMenuList').onclick=e=>{const b=e.target.closest('[data-sheet-index]');if(!b)return;$('sheetMenu').hidden=true;$('allSheets').setAttribute('aria-expanded','false');selectSheet(+b.dataset.sheetIndex).catch(noteError);};
  document.querySelectorAll('[data-sheet-filter]').forEach(b=>b.onclick=()=>{ui.filter=b.dataset.sheetFilter;renderSheetList();});
  $('sheetList').onclick=e=>{const b=e.target.closest('[data-sheet-index]');if(b)selectSheet(+b.dataset.sheetIndex).catch(noteError);};
  function closeSheetMenu() {
    if($('sheetMenu'))$('sheetMenu').hidden=true;
    if($('allSheets'))$('allSheets').setAttribute('aria-expanded','false');
  }
  document.addEventListener('pointerdown',e=>{if(!e.target.closest('#sheetMenu,#allSheets'))closeSheetMenu();});
  document.addEventListener('keydown',e=>{
    if(e.key==='Escape'){
      if(document.querySelector('dialog[open]'))return;
      if(ui.drag||ui.selection||ui.mode){cancelSelection();return;}
      if($('sheetMenu')&&!$('sheetMenu').hidden){closeSheetMenu();$('allSheets').focus();return;}
      $('importReview').classList.remove('review-focused');if($('reviewFocus'))$('reviewFocus').textContent='展开核对';
    }
    if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='z'&&!e.target.matches('input,textarea,select')){e.preventDefault();undoMapping();}
  });
  function noteError(e){if($('viewWarning'))$('viewWarning').textContent=e.message;else note(e.message);}
  // Expose pure coordinate helpers for fixture-based UI validation.
  window.sheetReviewHelpers={colName,colIndex,cellPoint,hitBlock,parseRange,clean};
})();
