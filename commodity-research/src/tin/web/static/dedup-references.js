/* Existing bindings change only after the researcher accepts a saved revision. */
(() => {
  const prefix = window.RESEARCH_PREFIX || '';
  const endpoint = `${prefix}/api/sn/indicator-dedup/references`;
  async function read(node) {
    const kind = node.dataset.dedupReferences;
    const id = node.dataset.dedupReferenceId || '';
    const requestKey = `${kind}:${id}`;
    node.dataset.dedupRequest = requestKey;
    node.replaceChildren(); node.hidden = true;
    if ((kind === 'judgment' || kind === 'strategy') && !id) return;
    try {
      const response = await window.researchFetch(`${endpoint}?${new URLSearchParams({kind, id})}`);
      if (!response.ok) throw new Error('暂时无法核对已保存引用的指标对应关系。');
      const result = await response.json();
      if (node.dataset.dedupRequest !== requestKey) return;
      if (!result.items.length && !result.related_charts.length) return;
      node.hidden = false;
      const title = document.createElement('p');
      title.textContent = '可改为引用 default 库指标';
      title.style.fontWeight = '600'; node.append(title);
      if (result.items.length) {
        const list = document.createElement('ul');
        for (const item of result.items) {
          const row = document.createElement('li');
          row.append(document.createTextNode(`${item.personal_name} → `));
          const link = document.createElement('a');
          link.href = item.url; link.textContent = `${item.default_name}（查看对应指标）`;
          row.append(link); list.append(row);
        }
        node.append(list);
      }
      if (result.reason) {
        const reason = document.createElement('p'); reason.textContent = result.reason; node.append(reason);
      }
      for (const item of result.related_charts) {
        const row = document.createElement('p');
        row.append(document.createTextNode('引用的图表也有可对应指标：'));
        const link = document.createElement('a'); link.href = item.url; link.textContent = `${item.title} · 前往核对`;
        row.append(link); node.append(row);
      }
      if (!result.can_rewrite || !result.items.length) return;
      const hint = document.createElement('p');
      hint.textContent = '点击确认后保存新修订；原修订保留。'; node.append(hint);
      const button = document.createElement('button');
      button.type = 'button'; button.className = 'rw-btn';
      button.textContent = '确认改为 default 库指标并保存新修订'; node.append(button);
      const status = document.createElement('p'); status.setAttribute('role', 'status'); node.append(status);
      button.addEventListener('click', async () => {
        button.disabled = true; status.textContent = '正在保存新修订…';
        try {
          const saved = await window.researchFetch(endpoint + '/rewrite', {method: 'POST',
            headers: {'Content-Type': 'application/json'}, body: JSON.stringify({kind, id,
              revision: result.revision, sources_revision: result.sources_revision, confirmed: true,
              replacements: Object.fromEntries(result.items.map(item => [item.personal_id, item.default_id]))})});
          if (!saved.ok) {
            const error = await saved.json();
            throw new Error(typeof error.detail === 'string' ? error.detail : '引用未保存，请刷新后重新确认。');
          }
          status.textContent = '已保存新修订，正在刷新。';
          window.location.reload();
        } catch (error) { status.textContent = error.message; button.disabled = false; }
      });
    } catch (error) {
      if (node.dataset.dedupRequest === requestKey) {
        node.hidden = false; node.textContent = error.message;
      }
    }
  }
  document.querySelectorAll('[data-dedup-references]').forEach(read);
  document.addEventListener('click', event => {
    const selection = event.target.closest('[data-strategy-select]');
    if (!selection) return;
    document.querySelectorAll('[data-dedup-references="strategy"][data-dedup-workspace]').forEach(node => {
      node.dataset.dedupReferenceId = selection.dataset.strategySelect; read(node);
    });
  });
})();
