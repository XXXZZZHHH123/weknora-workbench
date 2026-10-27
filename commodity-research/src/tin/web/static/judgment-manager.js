(function () {
  const dialog = document.getElementById('judgment-manager');
  if (!dialog) return;
  const byId = name => document.getElementById('logic-' + name);
  const input = byId('input'), status = byId('status');
  const endpoint = (window.RESEARCH_PREFIX || '') + '/api/sn/judgment';
  let managed, draft, displayed, busy = false, controller, panels = [];
  const selectedId = () => dialog.dataset.judgmentId || '';
  const managePath = () => '/manage' + (selectedId() ? '?judgment_id=' + encodeURIComponent(selectedId()) : '');
  function inform(message, error = false) {
    status.textContent = message || '';
    status.dataset.error = String(error);
    status.dataset.busy = String(busy);
  }
  function invalidate() { draft = null; byId('preview').hidden = true; }
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
  }
  async function api(path, body, method = 'POST') {
    const response = await researchFetch(endpoint + path, body === undefined ? {signal: controller?.signal} : {
      method, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body), signal: controller?.signal
    });
    let value;
    try { value = await response.json(); } catch { throw new Error('研究逻辑服务暂时不可用，输入已保留。'); }
    if (!response.ok) { const detail = value.detail; const message = typeof detail === 'string' ? detail :
      detail?.message ? detail.message + (detail.issues?.length ? '：' + detail.issues.slice(0, 4).map(item => `${item.path} ${item.message}`).join('；') : '') : '请求未完成，输入已保留。';
      const error = new Error(message); error.status = response.status; throw error; }
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
      option.textContent = `${row.title} · v${row.version} · ${row.status || '草稿'}`; selector.append(option);
    }
    selector.value = previous || selectedId() || String(managed.current?.id || panels[0]?.judgment_id || '');
    byId('panel-range').textContent = `${panels.length}/${result.total} 个已保存研判`;
    byId('panel-more').hidden = !result.has_more;
    syncPanelName();
  }
  function text(id, value) { byId(id).textContent = value || ''; }
  function showVersion(row) {
    displayed = row;
    text('current-md', row?.markdown || '尚无研究逻辑。可在“导入与分析”添加原文。');
    text('original-source', row?.source_text || '尚无原始输入');
    text('current-state', row ? `v${row.version} · ${row.status} · ${row.author}` : '尚无研究逻辑');
  }
  function presentDraft(value) {
    draft = value; byId('preview').hidden = false;
    text('preview-md', value.markdown); text('generated-diff', value.diff.markdown);
    text('coverage', `${value.bindings.length} 个真实指标绑定 · ${value.candidate_count}/${value.catalog_total} 个${value.candidate_subset ? '候选' : '目录'}指标`);
    const bindings = byId('bindings'); bindings.replaceChildren();
    for (const binding of value.bindings) {
      const chip = document.createElement('span'); chip.textContent = `${binding.name} · ${binding.unit || '单位待确认，仅描述'}`; bindings.append(chip);
    }
    const gaps = byId('unresolved'); gaps.replaceChildren();
    for (const item of value.unresolved) {
      const line = document.createElement('li'); line.textContent = `${item.section}：${item.text} — ${item.reason}`; gaps.append(line);
    }
    const fields = byId('field-diff'); fields.replaceChildren();
    for (const change of value.diff.fields) {
      const section = document.createElement('section'), title = document.createElement('strong'); title.textContent = change.label;
      const before = document.createElement('pre'), after = document.createElement('pre');
      before.textContent = '原：' + JSON.stringify(change.before, null, 2);
      after.textContent = '新：' + JSON.stringify(change.after, null, 2);
      section.append(title, before, after); fields.append(section);
    }
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
      text('version-label', `${managed.current ? `当前展示 v${managed.current.version}` : '暂无当前逻辑'} · 最新基准 v${managed.latest?.version || 0}。新版本保存后仍为草稿。`);
      const versions = byId('version'); versions.replaceChildren();
      for (const row of managed.versions) {
        const option = document.createElement('option'); option.value = row.version; option.textContent = `v${row.version} · ${row.status}`; versions.append(option);
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
      inform('');
      if (dialog.dataset.prefill === 'true') { tab('analyze'); dialog.dataset.prefill = 'false'; }
    } catch (error) { inform(error.message, true); }
  }
  function close() {
    controller?.abort(); setBusy(false); dialog.close();
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
      presentDraft(value); inform(value.notice);
    } catch (error) { await recover(error); }
    finally { setBusy(false); }
  });
  byId('apply').addEventListener('click', async () => {
    if (busy || !draft || !managed?.can_write) return;
    const selected = draft; controller = new AbortController(); setBusy(true); inform('正在确认来源与版本并保存草稿…');
    try {
      const result = await api(selected.kind === 'structured' ? '/structured/apply' : '/apply', {draft_id: selected.draft_id, base_version: selected.base_version});
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
    try { const result = await api('/structured/preview', {payload, ...panelTitle()}); presentDraft(result); inform(result.notice); }
    catch (error) { await recover(error); }
    finally { setBusy(false); }
  });
  byId('reuse').addEventListener('click', () => {
    if (!displayed) return inform('尚无可复用版本。');
    if (input.value.trim() && input.value !== displayed.markdown) return inform('已有输入，请先保留或清空，再复用当前MD。');
    input.value = displayed.markdown; text('file-name', `复用 v${displayed.version} MD`); invalidate(); tab('analyze'); input.focus();
  });
  byId('reuse-original').addEventListener('click', () => {
    if (!displayed?.source_text) return inform('尚无可复用原文。');
    if (input.value.trim() && input.value !== displayed.source_text) return inform('已有输入，请先保留或清空，再复用原文。');
    input.value = displayed.source_text; text('file-name', `复用 v${displayed.version} 原文`); invalidate(); tab('analyze'); input.focus();
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
