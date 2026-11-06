document.addEventListener('DOMContentLoaded', () => {
  'use strict';
  const root = document.getElementById('indicator-workspace'), client = window.AccountFavorites;
  if (!root || root.dataset.readonly === 'true') return;
  const get = id => document.getElementById('indicator-' + id);
  const tabs = [...root.querySelectorAll('[data-indicator-tab]')], panels = [...root.querySelectorAll('[data-indicator-panel]')];
  let tab, userSelected = false, resolvedDefault = false, rendered = '', generation = 0, active = 0, queue = [], closed = false;
  const renderers = new Set(), controllers = new Set(), cache = new Map();
  const requested = new URLSearchParams(location.search).get('tab');
  const validTab = value => ['favorites', 'catalog', 'charts'].includes(value);
  function choose(value, updateURL = true) {
    tab = value;
    tabs.forEach(button => {
      const current = button.dataset.indicatorTab === tab;
      button.setAttribute('aria-selected', String(current)); button.tabIndex = current ? 0 : -1;
    });
    panels.forEach(panel => { panel.hidden = panel.dataset.indicatorPanel !== tab; });
    root.querySelectorAll('[data-catalog-explanations]').forEach(section => { section.hidden = tab !== 'catalog'; });
    const url = new URL(location.href); url.searchParams.set('tab', tab);
    if (updateURL) history.replaceState(null, '', url);
    document.querySelectorAll('.date-switcher').forEach(form => {
      let input = form.querySelector('input[name="tab"]');
      if (!input) { input = document.createElement('input'); input.type = 'hidden'; input.name = 'tab'; form.append(input); }
      input.value = tab;
      form.querySelectorAll('a').forEach(link => { const target = new URL(link.href); target.searchParams.set('tab', tab); link.href = target; });
    });
    document.dispatchEvent(new CustomEvent('indicator:tab', {detail: {tab}}));
    if (tab === 'favorites' && !observer) root.querySelectorAll('[data-favorite-card]').forEach(card => enqueue(card));
  }
  tabs.forEach((button, index) => {
    button.addEventListener('click', () => { userSelected = true; choose(button.dataset.indicatorTab); });
    button.addEventListener('keydown', event => {
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
      if (event.key === 'Home') next = 0;
      if (event.key === 'End') next = tabs.length - 1;
      if (next !== undefined) { event.preventDefault(); tabs[next].focus(); tabs[next].click(); }
    });
  });
  root.querySelector('[data-open-catalog]')?.addEventListener('click', () => { userSelected = true; choose('catalog'); });
  window.addEventListener('popstate', () => { const value = new URLSearchParams(location.search).get('tab'); if (validTab(value)) { userSelected = true; choose(value, false); } });
  function message(text) { get('storage-note').textContent = text; get('storage-note').hidden = !text; }
  function button(text, action, label, disabled = false) {
    const value = document.createElement('button'); value.type = 'button'; value.className = 'indicator-button';
    value.textContent = text; value.setAttribute('aria-label', label); value.disabled = disabled;
    value.addEventListener('click', action); return value;
  }
  const date = root.dataset.date ? '?date=' + encodeURIComponent(root.dataset.date) : '';
  async function read(path, signal) {
    const response = await researchFetch((window.RESEARCH_PREFIX || '') + '/api/sn' + path, {signal});
    const data = await response.json();
    if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '自选数据读取失败，请重试。');
    return data;
  }
  function paragraph(parent, text, className = '') {
    const p = document.createElement('p'); p.textContent = text; p.className = className; parent.append(p); return p;
  }
  const fmt = value => Number.isFinite(value) ? new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 4}).format(value) : '—';
  function indicatorSummary(target, data) {
    const points = new Map();
    for (const segment of data.segments || []) for (const point of segment.points || []) if (Number.isFinite(point.raw)) points.set(point.date, point.raw);
    const ordered = [...points].sort(([a], [b]) => a.localeCompare(b)), latest = ordered.at(-1), previous = ordered.at(-2);
    target.replaceChildren();
    paragraph(target, latest ? `${fmt(latest[1])} ${data.unit || ''}` : '暂无有效值', 'favorite-value');
    paragraph(target, latest ? '观测日期 ' + latest[0] : '截至所选日期没有有效观测', 'indicator-muted');
    const change = latest && previous ? latest[1] - previous[1] : null;
    paragraph(target, change === null ? '较上期：暂无可比观测' : `较上期：${change > 0 ? '+' : ''}${fmt(change)} ${data.unit === '%' ? '个百分点' : data.unit || ''}`);
    const position = data.position;
    paragraph(target, position?.rank != null ? `自然年同期第 ${position.rank}/${position.n + 1} · 往期 ${position.n} 期` : '自然年周期位置：有效往期不足 3 期', 'indicator-muted');
    if (data.warnings?.includes('observation_window_limited')) paragraph(target, '已达到观测读取上限，周期位置仅覆盖已读取窗口。', 'indicator-muted');
  }
  function draw(task, value) {
    if (task.item.kind === 'indicator') indicatorSummary(task.target, value.data);
    else {
      task.target.replaceChildren();
      renderers.add(window.ChartKit.render(task.target, value.data, {definition: value.definition, thumbnail: true, width: 680, height: 250}));
    }
  }
  function enqueue(card) {
    const task = card._favoriteTask;
    if (!task || task.queued) return;
    task.queued = true; queue.push(task); pump();
  }
  async function run(task, controller) {
    const {item, target, key} = task;
    let value = cache.get(key);
    if (!value) {
      if (item.kind === 'indicator') value = {data: await read('/indicators/' + encodeURIComponent(item.id) + '/cycle' + date, controller.signal)};
      else {
        const record = await read('/charts/' + encodeURIComponent(item.id), controller.signal);
        const data = await read('/charts/' + encodeURIComponent(item.id) + '/data' + date, controller.signal);
        if (data.revision !== undefined && data.revision !== record.revision) throw new Error('图表已有新版本，请重新加载自选。');
        value = {data, definition: record.definition};
      }
      if (task.generation === generation && !controller.signal.aborted) cache.set(key, value);
    }
    if (task.generation === generation && target.isConnected && !controller.signal.aborted) draw(task, value);
  }
  function pump() {
    while (!closed && active < 4 && queue.length) {
      const task = queue.shift();
      if (task.generation !== generation || !task.target.isConnected) continue;
      active++;
      const controller = new AbortController(); controllers.add(controller);
      run(task, controller).catch(error => {
        if (error.name !== 'AbortError' && task.generation === generation && task.target.isConnected) {
          task.target.replaceChildren(); paragraph(task.target, error.message, 'indicator-warning');
          task.target.append(button('重试', () => { task.queued = false; enqueue(task.card); }, '重试读取自选数据'));
        }
      }).finally(() => { active--; controllers.delete(controller); pump(); });
    }
  }
  const observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    for (const entry of entries) if (entry.isIntersecting) { observer.unobserve(entry.target); enqueue(entry.target); }
  }, {rootMargin: '180px'}) : null;
  async function move(index, delta) {
    const next = [...client.items], item = next[index], target = index + delta;
    [next[index], next[target]] = [next[target], next[index]];
    try {
      await client.replace(next);
      const card = [...get('favorites').children].find(card => card.dataset.favoriteCard === `${item.kind}:${item.id}`);
      (card?.querySelector(`[data-move="${delta}"]:not(:disabled)`) || card?.querySelector('button:not(:disabled)'))?.focus();
    } catch (error) { message(error.message); }
  }
  function render() {
    const signature = JSON.stringify(client.items);
    if (signature === rendered) return;
    rendered = signature; generation++; observer?.disconnect(); controllers.forEach(controller => controller.abort()); queue = [];
    renderers.forEach(renderer => renderer.destroy()); renderers.clear();
    get('favorites').replaceChildren();
    client.items.forEach((item, index) => {
      const card = document.createElement('article'), title = document.createElement('h3'), details = document.createElement('div'), actions = document.createElement('div');
      card.className = 'favorite-entry'; card.dataset.favoriteCard = `${item.kind}:${item.id}`;
      const invalid = item.status === 'invalid', label = item.title || item.id;
      if (invalid) { title.textContent = `${item.kind === 'chart' ? '图表' : '指标'} ${label}`; paragraph(details, '已失效', 'indicator-warning'); }
      else {
        const link = document.createElement('a'); link.textContent = label;
        link.href = (window.RESEARCH_PREFIX || '') + '/sn/' + (item.kind === 'indicator' ? 'indicators/' : 'charts/') + encodeURIComponent(item.id) + date;
        title.append(link); details.textContent = '等待进入视野…';
      }
      details.className = 'favorite-details'; actions.className = 'favorite-actions';
      if (!invalid && item.kind === 'indicator') {
        const selection = document.createElement('label'), box = document.createElement('input');
        box.type = 'checkbox'; box.dataset.builderFavorite = item.id; box.setAttribute('aria-label', '选择作图 ' + label);
        selection.append(box, document.createTextNode(' 选择作图')); actions.append(selection);
      }
      for (const [delta, text] of [[-1, '上移'], [1, '下移']]) {
        const control = button(text, () => move(index, delta), `${text} ${label}`, index + delta < 0 || index + delta >= client.items.length);
        control.dataset.move = String(delta); actions.append(control);
      }
      const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'favorites-star';
      Object.assign(remove.dataset, {favorite: item.id, favoriteKind: item.kind, favoriteLabel: label});
      remove.setAttribute('aria-pressed', 'true'); remove.textContent = invalid ? '移除' : '★ 自选';
      if (invalid) { remove.className = 'indicator-button'; remove.dataset.favoriteInvalid = 'true'; remove.setAttribute('aria-label', '移除已失效自选 ' + label); }
      actions.append(remove); card.append(title, details, actions); get('favorites').append(card);
      if (!invalid) {
        const key = `${item.kind}:${item.id}:${item.chart_revision || ''}`;
        card._favoriteTask = {card, item, target: details, generation, key};
        if (observer) observer.observe(card); else if (tab === 'favorites') enqueue(card);
      }
    });
    client.syncButtons();
  }
  let legacy = [], lastAccountError = '';
  try { legacy = window.FavoritesClient?.legacy(localStorage, window.RESEARCH_CONTEXT) || []; }
  catch (error) { message(error.message); }
  function update() {
    if (!client) return;
    get('favorite-count').textContent = String(client.items.length);
    get('favorites-empty').hidden = !client.loaded || !!client.items.length;
    get('favorites-error').hidden = !client.error;
    if (client.error) {
      get('favorites-error').querySelector('span').textContent = client.error.message;
      message(client.error.message); lastAccountError = client.error.message;
    } else if (lastAccountError && get('storage-note').textContent === lastAccountError) {
      message(''); lastAccountError = '';
    }
    get('legacy-sync').hidden = !client.loaded || !!client.items.length || !legacy.length;
    get('legacy-sync-button').textContent = `把本浏览器的 ${legacy.length} 个自选同步到账号`;
    get('legacy-sync-button').disabled = client.saving;
    if (client.loaded) {
      render();
      get('favorites').querySelectorAll('[data-move]').forEach((button, index) => {
        const position = Math.floor(index / 2), target = position + Number(button.dataset.move);
        button.disabled = client.saving || target < 0 || target >= client.items.length;
      });
      if (!resolvedDefault) {
        resolvedDefault = true;
        if (!userSelected && !validTab(requested)) choose(client.items.length ? 'favorites' : 'catalog');
      }
    }
  }
  get('legacy-sync-button').addEventListener('click', async () => {
    try {
      // Recheck the server before an explicit migration; another browser may have saved meanwhile.
      await client.refresh();
      if (client.items.length) throw new Error('账号自选已有内容，请重新加载后核对。');
      const available = [];
      for (let offset = 0; offset < legacy.length; offset += 100) {
        const query = new URLSearchParams({format: 'metadata', limit: '200'});
        legacy.slice(offset, offset + 100).forEach(id => query.append('series_id', id));
        const data = await read('/indicators/catalog?' + query);
        if (!Array.isArray(data.indicators) || data.next_offset !== null || data.total !== data.indicators.length) throw new Error('旧自选目录未完整返回，请重试。');
        const requested = new Set(legacy.slice(offset, offset + 100));
        if (data.indicators.some(row => !requested.has(row.series_id))) throw new Error('旧自选目录范围不匹配。');
        const ids = new Set(data.indicators.map(row => row.series_id));
        available.push(...legacy.slice(offset, offset + 100).filter(id => ids.has(id)));
      }
      if (!available.length) throw new Error('本浏览器的旧自选均已不在当前研究台目录，未写入账号。');
      const unavailable = legacy.length - available.length;
      await client.replace(window.FavoritesClient.mergeIndicators([], available));
      message(`已同步 ${available.length} 个自选到账号。${unavailable ? `另有 ${unavailable} 个旧条目已失效，浏览器原记录仍保留。` : ''}`);
      userSelected = true; choose('favorites');
    } catch (error) { get('legacy-sync-note').textContent = error.message; message(error.message); }
  });
  get('favorites-retry').addEventListener('click', async () => {
    rendered = ''; cache.clear();
    try { await client.refresh(); } catch (_) { /* The subscription exposes the read error. */ }
  });
  if (client) {
    client.subscribe(update); update();
    client.ready().catch(() => {});
  } else message('账号自选需要当前来源授权，请重新打开研究台。');
  choose(validTab(requested) ? requested : 'catalog', validTab(requested));
  window.addEventListener('pagehide', () => { closed = true; generation++; observer?.disconnect(); controllers.forEach(controller => controller.abort()); renderers.forEach(renderer => renderer.destroy()); });
  window.addEventListener('pageshow', event => {
    if (event.persisted) { closed = false; rendered = ''; cache.clear(); if (client?.loaded) render(); }
  });
});
