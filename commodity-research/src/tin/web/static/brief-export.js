/* Export persisted reports only; no inference, hidden source copy, or browser token. */
(() => {
  'use strict';
  function bind(control) {
    if (control.dataset.exportBound) return;
    control.dataset.exportBound = '1';
    const details = control.querySelector('details'), form = control.querySelector('[data-export-form]');
    const folders = control.querySelector('[data-export-folder]'), submit = control.querySelector('[data-export-submit]');
    const status = control.querySelector('[data-export-status]');
    const kind = control.dataset.exportKind, identifier = control.dataset.exportId;
    let request = 0, controller = null, busy = false;
    const base = (window.RESEARCH_PREFIX || '') + '/api/sn/';
    function message(text, error = false) {
      status.replaceChildren(document.createTextNode(text));
      status.classList.toggle('text-[var(--alert)]', error);
    }
    async function json(response) {
      let payload;
      try { payload = await response.json(); } catch { throw Error('导出服务响应异常，请重试。'); }
      if (!response.ok) {
        const error = Error(typeof payload.detail === 'string' ? payload.detail : payload.detail?.message || '导出未完成，请重试。');
        if (response.status === 502 && payload.detail?.knowledge_base_id === control.dataset.exportKb
            && typeof payload.detail?.knowledge_id === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(payload.detail.knowledge_id)) error.document = payload.detail;
        throw error;
      }
      return payload;
    }
    details.addEventListener('toggle', async () => {
      if (!details.open || busy) return;
      const own = ++request, previous = folders.value;
      controller?.abort(); controller = new AbortController();
      folders.disabled = true; submit.disabled = true; message('读取文件夹…');
      try {
        const payload = await json(await researchFetch(base + 'brief/export/folders', { signal: controller.signal }));
        if (own !== request) return;
        if (payload.knowledge_base_id !== control.dataset.exportKb || !Array.isArray(payload.folders)) throw Error('绑定知识库已改变，请重新打开工作台。');
        const options = payload.folders.map(folder => {
          if (typeof folder.path !== 'string' || typeof folder.name !== 'string' || !Number.isInteger(folder.depth)) throw Error('文件夹列表格式无效。');
          const option = document.createElement('option'); option.value = folder.path;
          option.textContent = '　'.repeat(Math.min(folder.depth, 24)) + folder.name;
          return option;
        });
        folders.replaceChildren(...options); folders.value = options.some(option => option.value === previous) ? previous : '';
        folders.disabled = false; submit.disabled = false; message('');
      } catch (error) { if (own === request && error.name !== 'AbortError') message(error.message, true); }
    });
    form.addEventListener('submit', async event => {
      event.preventDefault(); if (busy || submit.disabled || folders.disabled) return;
      if (!['brief', 'snapshot', 'weekly'].includes(kind) || !identifier) { message('报告标识无效，请刷新后重试。', true); return; }
      busy = true; submit.disabled = true; folders.disabled = true; submit.textContent = '导出中…'; message('正在保存…');
      const selectedFolder = folders.value;
      try {
        const path = kind === 'brief' ? 'brief/' : 'reports/';
        const payload = await json(await researchFetch(base + path + encodeURIComponent(identifier) + (kind === 'weekly' ? '/weekly/export' : '/export'), {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ folder_path: selectedFolder }),
        }));
        if (payload.knowledge_base_id !== control.dataset.exportKb || payload.folder_path !== selectedFolder
            || typeof payload.knowledge_id !== 'string' || !payload.knowledge_id) throw Error('导出结果范围不匹配，请在知识库核对。');
        const link = document.createElement('a');
        link.href = '/platform/knowledge-bases/' + encodeURIComponent(payload.knowledge_base_id) + '?' + new URLSearchParams({ folder: selectedFolder, knowledge_id: payload.knowledge_id });
        link.target = '_top'; link.className = 'font-semibold text-[var(--primary)] hover:underline';
        link.textContent = payload.reused ? '已存在，查看文档 ↗' : '已保存，查看文档 ↗';
        status.replaceChildren(link); status.classList.remove('text-[var(--alert)]'); details.open = false;
      } catch (error) {
        message(error.message, true);
        if (error.document?.folder_path === selectedFolder) {
          const link = document.createElement('a');
          link.href = '/platform/knowledge-bases/' + encodeURIComponent(control.dataset.exportKb) + '?' + new URLSearchParams({folder: selectedFolder, knowledge_id: error.document.knowledge_id});
          link.target = '_top'; link.className = 'font-semibold text-[var(--primary)] hover:underline'; link.textContent = '查看处理详情 ↗';
          status.append(document.createTextNode(' '), link);
        }
      }
      finally { busy = false; folders.disabled = false; submit.disabled = false; submit.textContent = '确认导出'; }
    });
  }
  function start() { document.querySelectorAll('.brief-export').forEach(bind); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true }); else start();
})();
