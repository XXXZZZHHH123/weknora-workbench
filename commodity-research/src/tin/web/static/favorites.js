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
  // Indicator cards keep the earlier design: one batched preview request returns
  // each card's latest value, change and trend sparkline (click opens the history).
  const PREVIEW_BATCH = 40, previews = new Map();
  async function loadPreviews(cards, current) {
    const wanted = cards.filter(card => !previews.has(card._previewId));
    for (let offset = 0; offset < wanted.length; offset += PREVIEW_BATCH) {
      const chunk = wanted.slice(offset, offset + PREVIEW_BATCH), query = new URLSearchParams();
      chunk.forEach(card => query.append('series_id', card._previewId));
      if (root.dataset.date) query.set('date', root.dataset.date);
      try {
        const data = await read('/indicators/previews?' + query);
        const template = document.createElement('template'); template.innerHTML = data.html || '';
        template.content.querySelectorAll('[data-favorite-card]').forEach(node => previews.set(node.dataset.favoriteCard, node));
        chunk.forEach(card => { if (!previews.has(card._previewId)) previews.set(card._previewId, null); });
      } catch (error) {
        if (current !== generation) return;
        chunk.forEach(card => {
          const target = card.querySelector('.favorite-details'); target.replaceChildren();
          paragraph(target, error.message, 'indicator-warning');
          target.append(button('重试', () => loadPreviews([card], generation), '重试读取自选走势'));
        });
      }
    }
    if (current === generation) cards.forEach(card => card.isConnected && previews.has(card._previewId) && fillPreview(card));
  }
  function fillPreview(card) {
    const preview = previews.get(card._previewId), target = card.querySelector('.favorite-details');
    if (!preview) { target.replaceChildren(); paragraph(target, '暂无走势数据', 'indicator-muted'); return; }
    const trend = preview.querySelector('.macro-trend-card')?.cloneNode(true);
    // The whole card opens the indicator page; hovering shows the larger chart instead.
    trend?.querySelectorAll('.macro-chart-trigger, .macro-expand-icon').forEach(node => node.remove());
    trend?.classList.remove('is-interactive');
    const frequency = preview.querySelector('.indicator-card-actions .indicator-muted');
    if (frequency) card.querySelector('.favorite-frequency').textContent = frequency.textContent.trim();
    card.querySelector('h3')?.remove();
    target.replaceChildren(...[trend].filter(Boolean));
  }
  function draw(task, value) {
    task.target.replaceChildren();
    renderers.add(window.ChartKit.render(task.target, value.data, {definition: value.definition, thumbnail: true, width: 680, height: 250}));
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
      const record = await read('/charts/' + encodeURIComponent(item.id), controller.signal);
      const data = await read('/charts/' + encodeURIComponent(item.id) + '/data' + date, controller.signal);
      if (data.revision !== undefined && data.revision !== record.revision) throw new Error('图表已有新版本，请重新加载自选。');
      value = {data, definition: record.definition};
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
      (card?.querySelector(`[data-move="${delta}"]:not(:disabled)`) || card?.querySelector('.favorite-tools button:not(:disabled)'))?.focus();
    } catch (error) { message(error.message); }
  }
  const BOOKMARK = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4.5h10a1 1 0 0 1 1 1V20l-6-3.2L6 20V5.5a1 1 0 0 1 1-1z"/></svg>';
  function tool(text, label, action, disabled) {
    const value = document.createElement('button'); value.type = 'button'; value.className = 'favorite-tool';
    value.textContent = text; value.title = label; value.setAttribute('aria-label', label); value.disabled = disabled;
    value.addEventListener('click', action); return value;
  }
  const pageURL = item => (window.RESEARCH_PREFIX || '') + '/sn/' + (item.kind === 'indicator' ? 'indicators/' : 'charts/') + encodeURIComponent(item.id) + date;
  function render() {
    const signature = JSON.stringify(client.items);
    if (signature === rendered) return;
    rendered = signature; generation++; observer?.disconnect(); controllers.forEach(controller => controller.abort()); queue = [];
    renderers.forEach(renderer => renderer.destroy()); renderers.clear(); hideHover();
    get('favorites').replaceChildren();
    const indicatorCards = [];
    client.items.forEach((item, index) => {
      const card = document.createElement('article'), title = document.createElement('h3'), details = document.createElement('div'), tools = document.createElement('div');
      card.className = 'favorite-entry'; card.dataset.favoriteCard = `${item.kind}:${item.id}`; card.draggable = true;
      const invalid = item.status === 'invalid', label = item.title || item.id;
      details.className = 'favorite-details'; tools.className = 'favorite-tools';
      let body = card;
      if (invalid) { title.textContent = `${item.kind === 'chart' ? '图表' : '指标'} ${label}`; paragraph(details, '已失效', 'indicator-warning'); }
      else {
        body = document.createElement('a'); body.className = 'favorite-link'; body.href = pageURL(item); body.draggable = false;
        body.setAttribute('aria-label', `${item.kind === 'chart' ? '打开图表' : '打开指标'} ${label}`);
        title.textContent = label; details.textContent = item.kind === 'indicator' ? '正在读取走势…' : '等待进入视野…';
        body.addEventListener('mouseenter', () => showHover(card, item, label));
        body.addEventListener('focus', () => showHover(card, item, label));
        body.addEventListener('mouseleave', hideHover); body.addEventListener('blur', hideHover);
      }
      if (body !== card) { body.append(title, details); card.append(body); } else card.append(title, details);
      if (!invalid && item.kind === 'indicator') {
        const pick = document.createElement('label'), box = document.createElement('input');
        pick.className = 'favorite-tool favorite-pick'; pick.title = '选择作图';
        box.type = 'checkbox'; box.dataset.builderFavorite = item.id; box.setAttribute('aria-label', '选择作图 ' + label);
        pick.append(box); tools.append(pick);
      }
      for (const [delta, text, name] of [[-1, '‹', '左移'], [1, '›', '右移']]) {
        const control = tool(text, `${name} ${label}`, () => move(index, delta), index + delta < 0 || index + delta >= client.items.length);
        control.dataset.move = String(delta); tools.append(control);
      }
      const frequency = document.createElement('span'); frequency.className = 'favorite-frequency'; tools.append(frequency);
      const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'favorites-bookmark';
      Object.assign(remove.dataset, {favorite: item.id, favoriteKind: item.kind, favoriteLabel: label});
      remove.setAttribute('aria-pressed', 'true'); remove.innerHTML = BOOKMARK; remove.title = invalid ? '移除已失效自选' : '移出自选';
      if (invalid) { remove.dataset.favoriteInvalid = 'true'; remove.setAttribute('aria-label', '移除已失效自选 ' + label); }
      tools.append(remove); card.append(tools); get('favorites').append(card);
      dragging(card, index);
      if (!invalid && item.kind === 'indicator') { card._previewId = item.id; indicatorCards.push(card); }
      else if (!invalid) {
        const key = `${item.kind}:${item.id}:${item.chart_revision || ''}`;
        card._favoriteTask = {card, item, target: details, generation, key};
        if (observer) observer.observe(card); else if (tab === 'favorites') enqueue(card);
      }
    });
    if (indicatorCards.length) loadPreviews(indicatorCards, generation);
    client.syncButtons();
  }
  // Drag a card onto another to reorder; the ‹ › buttons do the same from the keyboard.
  let dragFrom = null;
  function dragging(card, index) {
    card.addEventListener('dragstart', event => {
      if (client.saving) { event.preventDefault(); return; }
      dragFrom = index; card.classList.add('is-dragging'); hideHover();
      event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', card.dataset.favoriteCard);
    });
    card.addEventListener('dragend', () => {
      dragFrom = null; get('favorites').querySelectorAll('.is-dragging, .drop-before, .drop-after').forEach(node => node.classList.remove('is-dragging', 'drop-before', 'drop-after'));
    });
    card.addEventListener('dragover', event => {
      if (dragFrom === null || dragFrom === index) return;
      event.preventDefault(); event.dataTransfer.dropEffect = 'move';
      const box = card.getBoundingClientRect(), after = event.clientX > box.left + box.width / 2;
      card.classList.toggle('drop-after', after); card.classList.toggle('drop-before', !after);
    });
    card.addEventListener('dragleave', () => card.classList.remove('drop-before', 'drop-after'));
    card.addEventListener('drop', async event => {
      if (dragFrom === null || dragFrom === index) return;
      event.preventDefault();
      const after = card.classList.contains('drop-after'), from = dragFrom, next = [...client.items], [moved] = next.splice(from, 1);
      let target = index + (after ? 1 : 0); if (from < target) target--;
      next.splice(target, 0, moved); card.classList.remove('drop-before', 'drop-after');
      try { await client.replace(next); } catch (error) { message(error.message); }
    });
  }
  // Hovering (or focusing) a card shows a larger chart; it only reads data, never navigates.
  const hover = document.createElement('div'), hoverCache = new Map();
  hover.className = 'favorite-hover'; hover.hidden = true; hover.setAttribute('role', 'tooltip'); document.body.append(hover);
  let hoverTimer = 0, hoverToken = 0, hoverRenderer = null;
  function hideHover() {
    clearTimeout(hoverTimer); hoverToken++; hover.hidden = true;
    hoverRenderer?.destroy(); hoverRenderer = null; hover.replaceChildren();
  }
  function placeHover(card) {
    const box = card.getBoundingClientRect(), width = Math.min(640, window.innerWidth - 32), height = 340;
    let left = box.right + 12; if (left + width > window.innerWidth - 16) left = box.left - width - 12;
    if (left < 16) left = Math.max(16, Math.min(box.left, window.innerWidth - width - 16));
    let top = left === box.right + 12 || left === box.left - width - 12 ? box.top : box.bottom + 8;
    top = Math.max(16, Math.min(top, window.innerHeight - height - 16));
    Object.assign(hover.style, {left: left + 'px', top: top + 'px', width: width + 'px'});
  }
  async function hoverData(item) {
    const key = `${item.kind}:${item.id}:${item.chart_revision || ''}`;
    if (hoverCache.has(key)) return hoverCache.get(key);
    let value;
    if (item.kind === 'indicator') {
      const history = await read('/series/' + encodeURIComponent(item.id) + '/history?' + new URLSearchParams({limit: '520', ...(root.dataset.date ? {date: root.dataset.date} : {})}));
      value = {data: {curves: [{label: history.name, axis: 'left', unit: history.unit, points: history.points || []}]}, title: history.name};
    } else {
      value = cache.get(key) || await (async () => {
        const record = await read('/charts/' + encodeURIComponent(item.id));
        return {data: await read('/charts/' + encodeURIComponent(item.id) + '/data' + date), definition: record.definition};
      })();
      value = {...value, title: value.definition?.title};
    }
    hoverCache.set(key, value); return value;
  }
  function showHover(card, item, label) {
    if (dragFrom !== null || !window.ChartKit) return;
    clearTimeout(hoverTimer);
    const token = ++hoverToken;
    hoverTimer = setTimeout(async () => {
      placeHover(card); hover.hidden = false; hover.replaceChildren(); paragraph(hover, '正在读取 ' + label + '…', 'indicator-muted');
      try {
        const value = await hoverData(item);
        if (token !== hoverToken) return;
        hover.replaceChildren(); hoverRenderer?.destroy();
        const plot = document.createElement('div'); hover.append(plot);
        hoverRenderer = window.ChartKit.render(plot, value.data, {definition: value.definition, title: value.title || label, width: 640, height: 300});
      } catch (error) {
        if (token === hoverToken) { hover.replaceChildren(); paragraph(hover, error.message, 'indicator-warning'); }
      }
    }, 280);
  }
  document.addEventListener('keydown', event => { if (event.key === 'Escape') hideHover(); });
  window.addEventListener('scroll', hideHover, {passive: true});
  let legacy = [], legacyScoped = false, legacyKey = '', migrating = false, lastAccountError = '';
  try {
    const entry = window.FavoritesClient?.legacyEntry(localStorage, window.RESEARCH_CONTEXT);
    if (entry) ({ids: legacy, scoped: legacyScoped, key: legacyKey} = entry);
  } catch (error) { message(error.message); }
  const syncedKey = () => legacyKey + ':synced';
  function synced() { try { return localStorage.getItem(syncedKey()) === '1'; } catch (_) { return true; } }
  async function availableLegacy() {
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
    return available;
  }
  // This account's own earlier browser favorites join the account once, without a click,
  // as they appeared before. The browser record itself is kept unchanged.
  async function carryOver() {
    if (migrating || !legacyScoped || !legacy.length || synced()) return;
    migrating = true;
    try {
      const available = await availableLegacy();
      const present = new Set(client.items.filter(item => item.kind === 'indicator').map(item => item.id));
      const missing = available.filter(id => !present.has(id));
      if (missing.length) {
        await client.replace(window.FavoritesClient.mergeIndicators(client.items, missing));
        message(`已把本浏览器原有的 ${missing.length} 个自选同步到账号。`);
        if (!userSelected && !validTab(requested)) choose('favorites');
      }
      try { localStorage.setItem(syncedKey(), '1'); } catch (_) { /* Retried on the next visit. */ }
    } catch (error) { message(error.message); }
    finally { migrating = false; }
  }
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
    get('legacy-sync').hidden = !client.loaded || !!client.items.length || !legacy.length || legacyScoped;
    if (client.loaded && !client.error) carryOver();
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
      const available = await availableLegacy();
      if (!available.length) throw new Error('本浏览器的旧自选均已不在当前研究台目录，未写入账号。');
      const unavailable = legacy.length - available.length;
      await client.replace(window.FavoritesClient.mergeIndicators([], available));
      message(`已同步 ${available.length} 个自选到账号。${unavailable ? `另有 ${unavailable} 个旧条目已失效，浏览器原记录仍保留。` : ''}`);
      userSelected = true; choose('favorites');
    } catch (error) { get('legacy-sync-note').textContent = error.message; message(error.message); }
  });
  get('favorites-retry').addEventListener('click', async () => {
    rendered = ''; cache.clear(); previews.clear();
    try { await client.refresh(); } catch (_) { /* The subscription exposes the read error. */ }
  });
  if (client) {
    client.subscribe(update); update();
    client.ready().catch(() => {});
  } else message('账号自选需要当前来源授权，请重新打开研究台。');
  choose(validTab(requested) ? requested : 'catalog', validTab(requested));
  window.addEventListener('pagehide', () => { closed = true; generation++; hideHover(); observer?.disconnect(); controllers.forEach(controller => controller.abort()); renderers.forEach(renderer => renderer.destroy()); });
  window.addEventListener('pageshow', event => {
    if (event.persisted) { closed = false; rendered = ''; cache.clear(); previews.clear(); if (client?.loaded) render(); }
  });
});
