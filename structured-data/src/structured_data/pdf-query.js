/* Published PDF tables share the file picker with workbook datasets. */
const PDFQuery = (() => {
  let tables = [], request = 0, active = null, offset = 0;
  const prefix = 'pdf:';
  const scopeOf = t => prefix + t.source.sha256;
  const pages = t => [...new Set((t.fragments||[]).map(f => f.page))].join('、');
  const title = t => t.title || (t.fragments||[]).map(f => f.caption).filter(Boolean).join(' / ') || `第 ${pages(t)} 页表格`;
  const quote = name => '"' + name.replaceAll('"', '""') + '"';
  const literal = value => "'" + value.replaceAll("'", "''") + "'";
  // Display decimals without padding introduced by DECIMAL(38,18). Keep the
  // exact response and source text in provenance; never round through Number.
  const numericText = value => typeof value==='string' ? value.replace(/(\.\d*?[1-9])0+$/,'$1').replace(/\.0+$/,'') : value;
  function elements() {
    if ($('pdfQueryList')) return;
    $('seriesSearch').insertAdjacentHTML('beforeend', '<div id="pdfQueryList" hidden></div>');
    $('queryPreview').insertAdjacentHTML('beforeend', '<div id="pdfQueryDetail" hidden></div>');
  }
  function clear() {
    elements(); request++; active = null; offset = 0;
    $('pdfQueryList').hidden = true; $('pdfQueryList').replaceChildren();
    $('pdfQueryDetail').hidden = true; $('pdfQueryDetail').replaceChildren();
    $('queryPreview').classList.remove('pdf-query-active');
    $('seriesSearchForm').hidden = false;
  }
  function deactivate() { request++; active=null;$('pdfQueryDetail').hidden=true;$('queryPreview').classList.remove('pdf-query-active'); }
  function groups() { return [...new Map(tables.map(t => [scopeOf(t), t.source.file_name])).entries()]; }
  function showList(scope) {
    elements();
    const search=$('search').value.trim().toLocaleLowerCase();
    const selected = tables.filter(t => (scope === ALL_DATASETS || scopeOf(t) === scope) &&
      (!search || [title(t),t.source.file_name,...t.columns.map(c=>c.name)].join(' ').toLocaleLowerCase().includes(search)));
    $('pdfQueryList').hidden = !selected.length;
    $('seriesSearchForm').hidden = scope.startsWith(prefix);
    $('pdfQueryList').innerHTML = `<h3>PDF 表格 · ${selected.length}</h3>` + selected.map(t => `<button type="button" class="series-choice" data-pdf-table="${esc(t.table_id)}" aria-pressed="${t.table_id === active?.table_id}"><strong>${esc(title(t))}</strong><span class="series-line">${esc(t.source.file_name)}</span><span class="series-range">第 ${esc(pages(t))} 页 · ${esc(t.coverage?.logical_row_count??0)} 行 · ${esc(t.columns.length)} 列</span></button>`).join('');
    $('pdfQueryList').onclick = event => {
      const button = event.target.closest('[data-pdf-table]');
      if (button) void selectTable(button.dataset.pdfTable).catch(error => note(error.message));
    };
  }
  async function selectTable(id, requestApi = api) {
    const ticket = ++request, epoch = authEpoch, scope = $('dataset').value;
    clearQuerySelection(); $('queryPreview').classList.add('pdf-query-active');
    $('pdfQueryDetail').hidden = false; $('pdfQueryDetail').innerHTML = '<p role="status">正在读取表格与原页出处…</p>';
    const table = await requestApi(`/api/pdf/tables/${encodeURIComponent(id)}?limit=100`);
    if (ticket !== request || epoch !== authEpoch || scope !== $('dataset').value) return;
    active = table; offset = 0; showList(scope);
    const columns = table.columns.map(c => `<option value="${esc(c.name)}">${esc(c.name)}${c.unit ? '（' + esc(c.unit) + '）' : ''}</option>`).join('');
    const numeric = table.columns.filter(c => c.type === 'decimal');
    $('pdfQueryDetail').innerHTML = `<div class="query-heading"><span class="query-eyebrow">PDF 表格</span><h2>${esc(title(table))}</h2><p class="muted">${esc(table.source.file_name)} · 第 ${esc(pages(table))} 页 · 共 ${table.total_row_count} 行</p></div>
      <form id="pdfQueryForm" class="pdf-query-filters"><label>筛选列<select id="pdfFilterColumn"><option value="">全部数据</option>${columns}</select></label><label>条件<select id="pdfFilterOperator"><option value="contains">包含文字</option><option value="eq">等于</option><option value="gt">大于</option><option value="gte">大于等于</option><option value="lt">小于</option><option value="lte">小于等于</option><option value="empty">为空</option></select></label><label>筛选值<input id="pdfFilterValue" aria-label="PDF 筛选值"></label><label>显示方式<select id="pdfQueryOperation"><option value="rows">逐行查看</option><option value="count">符合条件的行数</option>${numeric.length ? '<option value="sum">数值合计</option><option value="avg">数值平均</option><option value="min">数值最小值</option><option value="max">数值最大值</option>' : ''}</select></label><label id="pdfValueLabel" hidden>计算列<select id="pdfValueColumn">${numeric.map(c => `<option value="${esc(c.name)}">${esc(c.name)}${c.unit ? '（' + esc(c.unit) + '）' : ''}</option>`).join('')}</select></label><button type="submit">查询</button></form>
      <p class="muted pdf-query-notice">${table.status === 'reviewed' ? '此表已人工核对。' : '此表已自动入库，可对照原件核查。'}${table.coverage.table_input_complete ? '' : ' 此表的数据范围尚不完整，统计仅覆盖已入库部分。'}每次查询一张表，避免把历史版本或重叠表格重复计算。</p>
      <div id="pdfQueryStatus" role="status" aria-live="polite"></div><div id="pdfQueryOutput"></div><div class="pdf-query-pagination"><button id="pdfQueryPrevious" type="button" class="secondary">上一页</button><span id="pdfQueryPage"></span><button id="pdfQueryNext" type="button" class="secondary">下一页</button></div>`;
    $('pdfQueryOperation').onchange = () => { $('pdfValueLabel').hidden = !['sum','avg','min','max'].includes($('pdfQueryOperation').value); };
    $('pdfQueryForm').onsubmit = event => { event.preventDefault(); offset = 0; void run().catch(fail); };
    $('pdfQueryPrevious').onclick = () => { offset = Math.max(0, offset - 100); void run().catch(fail); };
    $('pdfQueryNext').onclick = () => { offset += 100; void run().catch(fail); };
    await run(requestApi);
  }
  function fail(error) { if ($('pdfQueryStatus')) $('pdfQueryStatus').textContent = '查询未完成：' + error.message; }
  async function run(requestApi = api) {
    const table = active; if (!table) return;
    const ticket = ++request, epoch = authEpoch, operation = $('pdfQueryOperation').value;
    const column = table.columns.find(c => c.name === $('pdfFilterColumn').value), operator = $('pdfFilterOperator').value, value = $('pdfFilterValue').value;
    let where = '';
    if (column) {
      const name = quote(column.name), parameter = literal(value);
      if (operator === 'empty') where = `${name} IS NULL`;
      else if (operator === 'contains') where = `contains(CAST(${name} AS VARCHAR), ${parameter})`;
      else {
        if (['gt','gte','lt','lte'].includes(operator) && column.type !== 'decimal') throw Error('大于、小于适用于数值列，请选择数值列或使用文字条件。');
        const symbol = {eq:'=',gt:'>',gte:'>=',lt:'<',lte:'<='}[operator];
        if (!symbol) throw Error('请选择有效的筛选条件。');
        where = `${name} ${symbol} ${column.type === 'decimal' ? `CAST(${parameter} AS DECIMAL(38,18))` : parameter}`;
      }
    }
    const numeric = table.columns.find(c => c.name === $('pdfValueColumn').value && c.type === 'decimal');
    let select = '*';
    if (operation === 'count') select = 'count(*) AS "行数"';
    else if (['sum','avg','min','max'].includes(operation)) {
      if (!numeric) throw Error('请选择要计算的数值列。');
      select = `${operation}(${quote(numeric.name)}) AS ${quote(numeric.name + ({sum:'合计',avg:'平均',min:'最小值',max:'最大值'}[operation]) + (numeric.unit ? '（' + numeric.unit + '）' : ''))}, count(${quote(numeric.name)}) AS "参与计算的数值数"`;
    }
    const sql = `SELECT ${select} FROM data${where ? ' WHERE ' + where : ''}${operation === 'rows' ? ` LIMIT 101 OFFSET ${offset}` : ''}`;
    $('pdfQueryStatus').textContent = '正在查询…';
    const result = await requestApi(`/api/pdf/tables/${encodeURIComponent(table.table_id)}/query?` + new URLSearchParams({sql,limit:'100'}));
    if (ticket !== request || epoch !== authEpoch || active !== table) return;
    const indexes = result.columns.map((name,i) => ({name,i})).filter(c => c.name !== '_row_id');
    $('pdfQueryOutput').innerHTML = `<div class="scroll"><table><thead><tr>${indexes.map(c=>`<th>${esc(c.name)}</th>`).join('')}${operation === 'rows' ? '<th>原页出处</th>' : ''}</tr></thead><tbody>${result.rows.map((row,i) => {
      const sourceRows = result.row_provenance[i]?.source_rows || [], cells = sourceRows.flatMap(r => Object.values(r.source_cells).flat()), originPages = [...new Set(cells.map(c=>c.page))];
      return `<tr>${indexes.map(c=>{const numeric=operation!=='rows'||table.columns.some(column=>column.name===c.name&&column.type==='decimal');return `<td${numeric?' class="pdf-numeric"':''} title="${esc(row[c.i])}">${row[c.i] === null ? '<span class="muted">空值</span>' : esc(numeric?numericText(row[c.i]):row[c.i])}</td>`;}).join('')}${operation === 'rows' ? `<td class="pdf-query-source">${originPages.map(page=>`<button type="button" class="secondary subtle-button" data-source-page="${page}">第 ${page} 页</button>`).join(' ')}<details><summary>单元格出处</summary>${sourceRows.map(r=>Object.entries(r.source_cells).map(([name,refs])=>`<p>${esc(name)}：${refs.map(c=>c.origin==='manual_transcription'?`第 ${esc(c.page)} 页，人工补行：${esc(c.reason)}`:`第 ${esc(c.page)} 页，识别第 ${esc(c.row+1)} 行、第 ${esc(c.column+1)} 列；原文「${esc(c.text)}」`).join('；')}</p>`).join('')).join('')}</details></td>` : ''}</tr>`;
    }).join('') || `<tr><td colspan="${indexes.length+1}">没有符合条件的数据，请调整筛选条件。</td></tr>`}</tbody></table></div>`;
    $('pdfQueryOutput').onclick = async event => {
      const button = event.target.closest('[data-source-page]'); if (!button) return;
      // Protected originals are fetched with the current identity, never via a
      // bearer token embedded in a public URL.
      const popup = window.open('about:blank','_blank');if(popup)popup.opener=null;
      try { const response = await fetch(`${base}/api/pdf/drafts/${encodeURIComponent(table.draft_id)}/source`,{headers:authHeaders()}); if (!response.ok) throw Error('暂时无法读取原件'); const blob = URL.createObjectURL(await response.blob()); if (popup) popup.location.href = blob + '#page=' + button.dataset.sourcePage; setTimeout(()=>URL.revokeObjectURL(blob),300000); }
      catch(error) { popup?.close(); fail(error); }
    };
    $('pdfQueryStatus').textContent = operation === 'rows' ? `本页 ${result.rows.length} 行${result.output_truncated ? '，还有下一页' : ''}` : `计算覆盖筛选后的全部数据；此表共载入 ${result.input_count} 行。空值不参与数值计算。`;
    $('pdfQueryPrevious').disabled = offset === 0 || operation !== 'rows'; $('pdfQueryNext').disabled = !result.output_truncated || operation !== 'rows';
    $('pdfQueryPage').textContent = operation === 'rows' ? `第 ${Math.floor(offset/100)+1} 页` : '';
  }
  window.addEventListener('structured-auth-scope-change',()=>{ tables=[]; clear(); });
  return {
    async loadCatalog(requestApi,isCurrent) { const result=await requestApi('/api/pdf/tables');if(isCurrent()) tables=result.tables||[]; },
    ids:()=>tables.map(t=>t.table_id), count:()=>tables.length, fileCount:()=>groups().length, options:()=>groups().map(([id,name])=>`<option value="${esc(id)}">PDF · ${esc(name)} · ${tables.filter(t=>scopeOf(t)===id).length} 张表</option>`).join(''),
    deactivate,hasScope:id=>typeof id==='string'&&id.startsWith(prefix)&&tables.some(t=>scopeOf(t)===id), clear,showList,
    async selectScope(id,isCurrent,requestApi) { showList(id);$('seriesRows').innerHTML='';$('seriesCount').textContent='PDF 表格';$('versionNotice').textContent='选择已入库表格查看数据、筛选和计算。';$('stats').innerHTML='';if(isCurrent()){const first=tables.find(t=>scopeOf(t)===id);if(first)await selectTable(first.table_id,requestApi);} }
  };
})();
