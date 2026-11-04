// 策略页的两个工具：文字整理（模型把一段话整理成方案草稿）和 md管理（全部方案导出为 Markdown，检查后导入）。
(() => {
  'use strict';
  const byId = id => document.getElementById(id);
  const api = (window.RESEARCH_PREFIX || '') + '/api/sn';
  const editor = () => window.ResearchStrategyEditor;

  async function request(path, options) {
    const response = await researchFetch(api + path, options);
    let body; try { body = await response.json(); } catch (_) { throw new Error('服务返回异常，请重试；输入内容仍保留。'); }
    if (!response.ok) {
      const detail = body?.detail;
      const error = new Error(typeof detail === 'string' ? detail : detail?.message || '操作未完成，请稍后重试。');
      error.status = response.status; error.preview = detail?.preview; throw error;
    }
    return body;
  }
  const post = (path, value) => request(path, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(value)});
  function say(node, message, error = false) {
    if (!node) return;
    node.textContent = message; node.dataset.error = String(error); node.setAttribute('role', error ? 'alert' : 'status');
  }
  function element(tag, text, className) {
    const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (className) node.className = className; return node;
  }
  function closeOn(dialog) {
    dialog.querySelectorAll('[data-tool-close]').forEach(node => node.addEventListener('click', () => dialog.close()));
  }

  // ---------- md管理 ----------
  const markdown = byId('strategy-markdown-dialog');
  const markdownTool = (() => {
    if (!markdown) return null;
    const text = byId('strategy-markdown-text'), preview = byId('strategy-markdown-preview'), note = byId('strategy-markdown-status');
    const check = byId('strategy-markdown-check'), apply = byId('strategy-markdown-apply');
    let checked = null, plan = null, busy = false, exported = false;
    closeOn(markdown);
    function lock(value) {
      busy = value;
      for (const node of markdown.querySelectorAll('footer button')) node.disabled = value || (node === apply && (!plan?.can_apply || checked !== text.value));
      text.readOnly = value;
    }
    function invalidate() { checked = null; plan = null; if (apply) apply.disabled = true; }
    text.addEventListener('input', () => { invalidate(); preview.hidden = true; });
    async function exportAll() {
      lock(true); say(note, '正在导出…');
      try {
        const data = await request('/research-strategies/markdown');
        text.value = data.markdown; exported = true; invalidate(); preview.hidden = true;
        const extra = [data.truncated ? '只导出了最近 100 份' : '', data.skipped ? `${data.skipped} 份历史记录尚未整理，未导出` : ''].filter(Boolean);
        say(note, `已导出 ${data.count} 份方案` + (extra.length ? '（' + extra.join('；') + '）' : '') + '。');
      } catch (error) { say(note, error.message, true); } finally { lock(false); }
    }
    const labels = {create: '新建', update: '修改', unchanged: '无变化', error: '需修改'};
    function render(value) {
      preview.replaceChildren(); preview.hidden = false;
      const counts = value.counts;
      preview.append(element('p', `新建 ${counts.create} 份，修改 ${counts.update} 份，无变化 ${counts.unchanged} 份` +
        (counts.error ? `，${counts.error} 份需要修改后才能导入` : '') + (value.untouched ? `；文档里没有的 ${value.untouched} 份方案保持不变` : '') + '。', 'strategy-markdown-summary'));
      const list = element('ol', undefined, 'strategy-markdown-rows');
      for (const row of value.cards) {
        const item = element('li'); item.dataset.action = row.action;
        const head = element('div', undefined, 'strategy-markdown-row-head');
        head.append(element('span', labels[row.action] || row.action, 'strategy-markdown-badge'), element('strong', row.title), element('small', `第 ${row.line} 行`));
        item.append(head);
        if (row.changes?.length) item.append(element('p', '改动：' + row.changes.join('、')));
        for (const message of row.errors || []) item.append(element('p', message, 'strategy-markdown-error'));
        for (const message of row.warnings || []) item.append(element('p', message, 'strategy-markdown-warning'));
        list.append(item);
      }
      preview.append(list);
    }
    async function runCheck() {
      if (!text.value.trim()) { say(note, '请先导出或粘贴策略 Markdown。', true); return null; }
      const sent = text.value; lock(true); say(note, '正在检查…');
      try {
        plan = await post('/research-strategies/markdown/preview', {markdown: sent});
        if (text.value !== sent) { invalidate(); say(note, '内容在检查期间有改动，请重新检查。', true); return null; }
        checked = sent; render(plan);
        say(note, plan.can_apply ? '检查完成，确认无误后点“导入”。' : plan.counts.error ? '有方案需要修改，修改后重新检查。' : '文档与当前方案一致，没有需要导入的修改。', !plan.can_apply && plan.counts.error > 0);
        return plan;
      } catch (error) { invalidate(); say(note, error.message, true); return null; } finally { lock(false); }
    }
    async function runApply() {
      if (!plan || checked !== text.value) { if (!(await runCheck())?.can_apply) return; }
      const counts = plan.counts;
      if (!window.confirm(`将新建 ${counts.create} 份、修改 ${counts.update} 份方案` + (plan.untouched ? `；文档里没有的 ${plan.untouched} 份保持不变` : '') + '。确认导入？')) return;
      lock(true); say(note, '正在导入…');
      try {
        const result = await post('/research-strategies/markdown/apply', {markdown: checked});
        editor()?.imported(result.saved || []);
        const created = result.saved.filter(row => row.action === 'create').length, updated = result.saved.length - created;
        lock(false); await exportAll();
        say(note, `已导入：新建 ${created} 份，修改 ${updated} 份。下方已是导入后的最新内容，可继续修改。`);
      } catch (error) {
        if (error.preview) { plan = error.preview; render(plan); }
        say(note, error.message, true);
      } finally { lock(false); }
    }
    check?.addEventListener('click', runCheck);
    apply?.addEventListener('click', runApply);
    byId('strategy-markdown-export').addEventListener('click', () => {
      if (text.value.trim() && checked !== text.value && exported && !window.confirm('重新导出会替换当前文本中的修改，继续？')) return;
      exportAll();
    });
    byId('strategy-markdown-copy').addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(text.value); say(note, '已复制。'); }
      catch (_) { text.select(); say(note, '浏览器未允许直接复制，已选中全文，请按 Ctrl+C 复制。', true); }
    });
    byId('strategy-markdown-download').addEventListener('click', () => {
      const link = document.createElement('a'), day = new Date().toISOString().slice(0, 10);
      link.href = URL.createObjectURL(new Blob([text.value], {type: 'text/markdown;charset=utf-8'}));
      link.download = `策略方案-${day}.md`; document.body.append(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    });
    byId('strategy-markdown-open')?.addEventListener('click', () => {
      if (!markdown.open) markdown.showModal();
      if (!exported && !busy) exportAll();
    });
    return {
      // Organized drafts arrive as new cards without identifiers; they are checked, never imported unseen.
      load(value, message) {
        if (!markdown.open) markdown.showModal();
        text.value = value; exported = true; invalidate(); preview.hidden = true;
        return runCheck().then(() => { if (message) say(note, message); });
      },
    };
  })();

  // ---------- 文字整理 ----------
  const dialog = byId('strategy-text-dialog');
  if (dialog) {
    const input = byId('strategy-text-input'), model = byId('strategy-text-model'), run = byId('strategy-text-run');
    const note = byId('strategy-text-status'), count = byId('strategy-text-count');
    let models = null, running = false;
    closeOn(dialog);
    const remembered = () => { try { return localStorage.getItem('research-strategy-text-model') || ''; } catch (_) { return ''; } };
    function sync() {
      count.textContent = `${input.value.length} / 6000`;
      run.disabled = running || !input.value.trim() || !model.value;
    }
    input.addEventListener('input', sync); model.addEventListener('change', () => {
      try { localStorage.setItem('research-strategy-text-model', model.value); } catch (_) { /* Remembering the model is optional. */ }
      sync();
    });
    async function loadModels() {
      if (models) return;
      try {
        const value = await request('/strategies/models');
        models = value.models || []; model.replaceChildren();
        for (const item of models) model.append(new Option(item.name, item.id));
        if (!models.length) { model.append(new Option('暂无可用通用模型', '')); say(note, '当前空间没有可用的通用模型，请先在模型设置中配置。', true); }
        else { model.disabled = false; if (models.some(item => item.id === remembered())) model.value = remembered(); }
      } catch (error) { models = null; say(note, '模型列表未读取：' + error.message, true); }
      sync();
    }
    byId('strategy-text-open')?.addEventListener('click', () => {
      if (!dialog.open) dialog.showModal();
      loadModels(); sync(); input.focus();
    });
    run.addEventListener('click', async () => {
      if (running || !input.value.trim() || !model.value) return;
      running = true; sync(); input.readOnly = true; say(note, '正在整理，通常需要十几秒到一分钟…');
      try {
        const result = await post('/research-strategies/draft-from-text', {text: input.value, model_id: model.value});
        const cards = result.cards || [];
        if (cards.length === 1) {
          if (!editor()?.openDraft(cards[0].body, '由文字整理 · 待核对', cards[0].notes || [])) throw new Error('正在保存其他方案，请稍后再试。');
          dialog.close(); input.value = ''; say(note, '');
        } else if (cards.length > 1 && markdownTool) {
          dialog.close();
          await markdownTool.load(result.markdown, `模型整理出 ${cards.length} 份方案，作为新方案列在下方；检查无误后再导入。`);
          input.value = ''; say(note, '');
        } else throw new Error('模型没有整理出方案，请补充描述后重试。');
      } catch (error) { say(note, error.message + ' 原文仍保留。', true); }
      finally { running = false; input.readOnly = false; sync(); }
    });
  }
})();
