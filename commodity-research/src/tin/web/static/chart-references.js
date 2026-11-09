/* Shared explicit picker and dated rendering for personal research chart pins. */
(function (root) {
  'use strict';
  const restrictedNotice = '图表引用暂不对受限视角开放';
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    const fetcher = root.researchFetch || root.fetch.bind(root);
    const response = await fetcher((root.RESEARCH_PREFIX || '') + '/api/sn/chart-references' + path, {...options, headers});
    const data = await response.json().catch(() => ({}));
    if (!response.ok) { const error = new Error(typeof data.detail === 'string' ? data.detail : '图表引用读取失败'); error.status = response.status; throw error; }
    return data;
  }
  function pick(options = {}) {
    if (options.restricted) return Promise.resolve(null);
    return new Promise(resolve => {
      const modal = document.createElement('dialog');
      modal.className = 'chart-reference-picker'; modal.setAttribute('aria-label', '插入图表');
      modal.style.cssText = 'width:min(720px,94vw);max-height:85vh;padding:24px;border:1px solid #d1d5db;border-radius:12px;background:var(--surface,#fff);color:var(--text,#111);z-index:100';
      modal.innerHTML = '<header style="display:flex;justify-content:space-between"><h2>插入图表</h2><button type="button" data-chart-picker-close>关闭</button></header>' +
        '<div style="display:flex;gap:12px;flex-wrap:wrap;margin:16px 0"><label>搜索图表 <input data-chart-picker-search maxlength="200" type="search"></label>' +
        '<label>分组 <select data-chart-picker-group><option value="">全部分组</option></select></label><label><input type="checkbox" data-chart-picker-favorites> 仅自选</label></div>' +
        '<p data-chart-picker-status role="status"></p><div data-chart-picker-items></div><button type="button" data-chart-picker-more hidden>加载更多</button>';
      document.body.append(modal);
      let generation = 0, timer, next = null, closed = false, controller;
      const status = modal.querySelector('[data-chart-picker-status]'), items = modal.querySelector('[data-chart-picker-items]'), more = modal.querySelector('[data-chart-picker-more]');
      const search = modal.querySelector('[data-chart-picker-search]'), group = modal.querySelector('[data-chart-picker-group]'), favorites = modal.querySelector('[data-chart-picker-favorites]');
      function done(value) { if (closed) return; closed = true; clearTimeout(timer); controller?.abort(); modal.close(); modal.remove(); resolve(value); }
      async function load(append = false) {
        const token = ++generation;
        controller?.abort(); controller = new AbortController();
        status.textContent = '正在读取图表库…'; more.disabled = true;
        if (!append) items.replaceChildren();
        const query = new URLSearchParams({q: search.value, favorites: String(favorites.checked), offset: String(append ? next || 0 : 0)});
        if (group.value) query.set('group', group.value);
        try {
          const data = await api('/options?' + query, {signal: controller.signal});
          if (closed || token !== generation) return;
          const selectedGroup = group.value;
          group.innerHTML = '<option value="">全部分组</option>' + (data.groups || []).map(g => `<option value="${esc(g)}">${esc(g)}</option>`).join('');
          group.value = selectedGroup;
          for (const chart of data.items || []) {
            const button = document.createElement('button'); button.type = 'button';
            button.dataset.chartPickerId = chart.id;
            button.style.cssText = 'display:block;width:100%;text-align:left;padding:12px;border-bottom:1px solid #ddd';
            button.innerHTML = `<strong>${esc(chart.title)}</strong><small style="display:block">${esc(chart.group)} · 修订 ${esc(chart.revision)} · ${esc(chart.definition_summary)}</small>`;
            button.addEventListener('click', () => done({kind: 'chart', id: chart.id, revision: chart.revision,
              title: chart.title, definition_summary: chart.definition_summary, ...(options.asOf ? {as_of: options.asOf} : {})}));
            items.append(button);
          }
          next = data.next_offset; more.hidden = next == null; more.disabled = false;
          status.textContent = data.total ? `共 ${data.total} 张图表` : '没有符合条件的图表';
        } catch (error) { if (!closed && token === generation && error.name !== 'AbortError') status.textContent = error.status === 403 ? restrictedNotice : error.message; }
      }
      search.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(), 180); });
      group.addEventListener('change', () => load()); favorites.addEventListener('change', () => load());
      more.addEventListener('click', () => load(true));
      modal.querySelector('[data-chart-picker-close]').addEventListener('click', () => done(null));
      modal.addEventListener('cancel', event => { event.preventDefault(); done(null); });
      modal.showModal(); search.focus(); load();
    });
  }
  function positionSummary(payload) {
    const curves = payload.mode === 'cycle' && Array.isArray(payload.curves) ? payload.curves : payload.segments ? [{label: '', cycle: payload}] : [];
    return curves.map(curve => {
      const position = curve.cycle?.position;
      if (!position) return [curve.label, '暂无周期位置'].filter(Boolean).join('：');
      const value = root.ChartKit?.formatNumber ? root.ChartKit.formatNumber(position.value) : String(position.value ?? '—');
      return [curve.label, `${position.date} · 周期第 ${position.x} 天 · ${value}`,
        position.rank != null ? `同期排名 ${position.rank}/${position.n + 1}` : '有效往期不足，暂无同期排名'].filter(Boolean).join(' · ');
    }).join('；');
  }
  function mount(container, options = {}) {
    if (!container) return {destroy() {}};
    container.__chartReferences?.destroy();
    const controllers = new Set(), renderers = new Set(); let destroyed = false;
    const handle = {destroy() { destroyed = true; for (const c of controllers) c.abort(); for (const r of renderers) r?.destroy?.(); }};
    container.__chartReferences = handle;
    container.replaceChildren();
    if (options.restricted) { container.textContent = restrictedNotice; return handle; }
    for (const raw of options.references || []) {
      const reference = {...raw, as_of: raw.as_of || options.asOf};
      const card = document.createElement('section'); card.className = 'chart-reference-card'; card.dataset.chartReference = reference.id;
      card.style.cssText = 'border:1px solid var(--line,#ddd);border-radius:8px;padding:12px;margin:12px 0;min-width:0;overflow-wrap:anywhere';
      card.innerHTML = `<header style="display:flex;gap:12px;justify-content:space-between;align-items:center;flex-wrap:wrap"><strong data-chart-reference-title>${esc(reference.title || '引用图表')}</strong>` +
        '<label>数据 <select data-chart-reference-mode><option value="historical">当时</option><option value="current">当前数据</option></select></label></header>' +
        `<p data-chart-reference-definition style="font-size:12px">${esc(reference.definition_summary)}</p><p data-chart-reference-status role="status"></p>` +
        '<div data-chart-reference-plot></div><p data-chart-reference-position style="font-size:12px"></p><p data-chart-reference-notice style="font-size:12px"></p>';
      container.append(card);
      const mode = card.querySelector('[data-chart-reference-mode]'), status = card.querySelector('[data-chart-reference-status]'), plot = card.querySelector('[data-chart-reference-plot]');
      plot.style.cssText = 'width:100%;min-width:0';
      let generation = 0, previousController, renderer;
      async function draw() {
        const token = ++generation;
        previousController?.abort(); const controller = new AbortController(); previousController = controller; controllers.add(controller);
        renderer?.destroy?.(); renderers.delete(renderer); plot.replaceChildren();
        status.textContent = '正在读取图表…';
        card.querySelector('[data-chart-reference-position]').textContent = '';
        card.querySelector('[data-chart-reference-notice]').textContent = '';
        try {
          const data = await api('/render', {method: 'POST', signal: controller.signal,
            body: JSON.stringify({reference, current: mode.value === 'current', rules: options.rules || []})});
          if (destroyed || token !== generation) return;
          card.querySelector('[data-chart-reference-title]').textContent = data.title || data.reference?.title || reference.title || '引用图表';
          card.querySelector('[data-chart-reference-definition]').textContent = data.reference?.definition_summary || reference.definition_summary || '';
          status.textContent = [data.notice, `${data.current ? '当前数据' : '当时'} · 截止 ${data.as_of} · 修订 ${reference.revision}`].filter(Boolean).join(' · ');
          if (data.payload && root.ChartKit) {
            const width = Math.max(240, Math.floor(plot.clientWidth || (options.thumbnail ? 360 : 960)));
            renderer = root.ChartKit.render(plot, data.payload, {definition: data.definition, overlays: data.overlays || [], thumbnail: !!options.thumbnail, width, height: options.thumbnail ? 190 : 300});
            const wrapper = plot.querySelector('.chart-kit'), svg = plot.querySelector('svg');
            if (wrapper) { wrapper.style.position = 'relative'; wrapper.style.minWidth = '0'; }
            if (svg) { svg.style.display = 'block'; svg.style.width = '100%'; svg.style.height = 'auto'; }
            renderers.add(renderer);
          }
          card.querySelector('[data-chart-reference-position]').textContent = positionSummary(data.payload || {});
          card.querySelector('[data-chart-reference-notice]').textContent = [data.data_notice, data.dedup_notice].filter(Boolean).join('；');
        } catch (error) {
          if (!destroyed && token === generation && error.name !== 'AbortError') {
            if (error.status === 403) { container.replaceChildren(); container.textContent = restrictedNotice; handle.destroy(); }
            else status.textContent = error.message;
          }
        } finally { controllers.delete(controller); }
      }
      mode.addEventListener('change', draw); draw();
    }
    return handle;
  }
  root.ChartReferences = {pick, mount, positionSummary, restrictedNotice};
})(typeof window === 'undefined' ? globalThis : window);
