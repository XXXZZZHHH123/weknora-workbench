document.addEventListener('DOMContentLoaded', () => {
  'use strict';
  const root = document.getElementById('market-core'), node = document.getElementById('market-core-catalog');
  if (!root || !node) return;
  const P = window.IndicatorPreferences, metadata = JSON.parse(node.textContent), key = P.identity;
  const get = id => document.getElementById('market-core-' + id), dialog = get('dialog'), grid = get('grid');
  const catalog = new Map((metadata.indicators || []).map(row => [row.series_id, row]));
  const charts = new Map((metadata.charts || []).map(row => [row.id, row]));
  let selected = (metadata.core_items || (metadata.defaults || []).map(id => ({kind: 'indicator', id}))).map(({kind, id}) => ({kind, id}));
  let draft = [], source = 'favorites', directoryController, directoryTimer, directoryGeneration = 0, controller, generation = 0, closed = false;
  const renderers = new Set(), chartControllers = new Set(), schedule = P.pool(4);
  const announce = message => { get('notice').textContent = message; get('notice').hidden = !message; };
  const normalize = value => String(value || '').normalize('NFKC').toLocaleLowerCase();
  const title = item => item.kind === 'chart' ? (charts.get(item.id)?.title || charts.get(item.id)?.definition?.title || item.title || item.id) : (catalog.get(item.id)?.name || item.title || item.id);
  function button(label, action, disabled = false) {
    const control = document.createElement('button'); control.type = 'button'; control.className = 'market-core-button';
    control.textContent = label; control.disabled = disabled; control.addEventListener('click', action); return control;
  }
  async function request(path, options = {}) {
    const response = await window.researchFetch((window.RESEARCH_PREFIX || '') + '/api/sn' + path, options), data = await response.json();
    if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '核心关注暂时不可用，请重试。');
    return data;
  }
  function syncCount() {
    get('count').textContent = String(selected.length); get('empty').hidden = !!selected.length;
    const total = get('total'); if (total) total.textContent = String(selected.length);
  }
  const observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    for (const entry of entries) if (entry.isIntersecting) { observer.unobserve(entry.target); enqueueChart(entry.target); }
  }, {rootMargin: '180px'}) : null;
  function clearCharts() {
    observer?.disconnect(); chartControllers.forEach(value => value.abort());
    renderers.forEach(value => value.destroy()); renderers.clear();
  }
  function enqueueChart(card) {
    if (card.dataset.queued === 'true') return;
    card.dataset.queued = 'true'; const own = generation;
    schedule(async () => {
      if (closed || own !== generation || !card.isConnected) return;
      const current = new AbortController(); chartControllers.add(current);
      const plot = card.querySelector('[data-focus-chart-plot]'), summary = card.querySelector('[data-focus-chart-summary]');
      try {
        const path = '/charts/' + encodeURIComponent(card.dataset.marketCardChart);
        const record = await request(path, {signal: current.signal});
        const payload = await request(path + '/data' + (metadata.date ? '?date=' + encodeURIComponent(metadata.date) : ''), {signal: current.signal});
        if (closed || own !== generation || !card.isConnected || current.signal.aborted) return;
        if (payload.revision !== undefined && payload.revision !== record.revision) throw new Error('图表版本已变化，请重新读取核心关注。');
        plot.replaceChildren(); renderers.add(window.ChartKit.render(plot, payload, {definition: record.definition, thumbnail: true, width: 680, height: 250}));
        summary.replaceChildren();
        for (const row of P.chartSummary(payload, record.definition)) {
          const p = document.createElement('p'), value = Number.isFinite(row.value) ? new Intl.NumberFormat('zh-CN', {maximumFractionDigits: 4}).format(row.value) + ' ' + row.unit : '暂无有效值';
          p.textContent = (row.label ? row.label + ' · ' : '') + '最新值 ' + value + (row.date ? '（' + row.date + '）' : ''); summary.append(p);
          if (record.definition.time.mode === 'cycle') {
            const rank = document.createElement('p'); rank.className = 'market-core-muted';
            rank.textContent = row.rank !== null && row.n !== null ? `同期第 ${row.rank}/${row.n + 1}` : '同期位置暂不可用'; summary.append(rank);
          }
        }
        if ((payload.warnings || []).includes('observation_window_limited')) {
          const warning = document.createElement('p'); warning.className = 'market-core-muted'; warning.textContent = '已达到观测读取上限，周期位置仅覆盖已读取窗口。'; summary.append(warning);
        }
      } catch (error) {
        if (error.name !== 'AbortError' && own === generation && card.isConnected) {
          plot.replaceChildren(); const text = document.createElement('p'); text.textContent = error.message; plot.append(text);
          plot.append(button('重试', () => { card.dataset.queued = 'false'; enqueueChart(card); }));
        }
      } finally { chartControllers.delete(current); }
    }).catch(error => announce(error.message));
  }
  function observeCharts() {
    grid.querySelectorAll('[data-market-card-chart]').forEach(card => { if (observer) observer.observe(card); else enqueueChart(card); });
  }
  async function loadCards() {
    controller?.abort(); controller = new AbortController(); const own = ++generation;
    clearCharts(); get('error').hidden = true; grid.replaceChildren(); syncCount();
    const ready = get('ready'); if (ready) ready.textContent = selected.length ? '…' : '0';
    if (!selected.length) { grid.removeAttribute('aria-busy'); return; }
    grid.setAttribute('aria-busy', 'true');
    try {
      const params = new URLSearchParams(); selected.forEach(item => params.append('item', key(item)));
      if (metadata.date) params.set('date', metadata.date);
      const data = await request('/market/core?' + params, {signal: controller.signal});
      if (own !== generation) return;
      if (JSON.stringify(data.items) !== JSON.stringify(selected) || typeof data.html !== 'string' || !Number.isInteger(data.ready_count) || data.ready_count < 0 || data.ready_count > selected.length) throw new Error('返回的核心关注范围不匹配，请刷新后重试。');
      grid.innerHTML = data.html; if (ready) ready.textContent = String(data.ready_count); observeCharts(); window.AccountFavorites?.syncButtons(grid);
    } catch (error) {
      if (own !== generation || error.name === 'AbortError') return;
      if (ready) ready.textContent = '—'; get('error').querySelector('span').textContent = error.message; get('error').hidden = false;
    } finally { if (own === generation) grid.removeAttribute('aria-busy'); }
  }
  function renderSelected(focusIndex, direction) {
    const list = get('selected'); list.replaceChildren(); get('draft-count').textContent = `${draft.length}/16`; get('selected-empty').hidden = !!draft.length;
    draft.forEach((entry, index) => {
      const item = document.createElement('li'), label = document.createElement('span'), actions = document.createElement('div'), name = title(entry);
      label.textContent = `${index + 1}. ${name}${entry.kind === 'chart' ? '（图表）' : ''}`; item.append(label);
      for (const [delta, text] of [[-1, '上移'], [1, '下移']]) {
        const move = button(text, () => { const target = index + delta; [draft[index], draft[target]] = [draft[target], draft[index]]; renderSelected(target, delta); }, index + delta < 0 || index + delta >= draft.length);
        move.setAttribute('aria-label', text + name); move.dataset.direction = String(delta); actions.append(move);
      }
      const remove = button('移出', () => { draft = draft.filter(value => key(value) !== key(entry)); renderSelected(); void renderCatalog(); });
      remove.setAttribute('aria-label', '移出核心关注' + name); actions.append(remove); item.append(actions); list.append(item);
    });
    if (focusIndex !== undefined) {
      const item = list.children[focusIndex]; ([...item.querySelectorAll('button')].find(value => value.dataset.direction === String(direction) && !value.disabled) || item.querySelector('button:not(:disabled)'))?.focus();
    }
  }
  async function renderCatalog(offset = 0) {
    directoryController?.abort(); directoryController = new AbortController(); const own = ++directoryGeneration;
    const query = get('search').value.trim(), list = get('choices'); list.replaceChildren(); get('no-results').hidden = true; list.setAttribute('aria-busy', 'true');
    try {
      let rows = [], next = null;
      if (source === 'favorites') {
        const account = window.AccountFavorites; if (!account) throw new Error('账号自选需要当前来源授权，请重新打开研究台。'); await account.ready();
        rows = account.items.filter(item => item.status !== 'invalid').map(item => ({...item, name: title(item), detail: item.kind === 'chart' ? '账号自选 · 图表' : '账号自选 · 指标'}));
      } else if (source === 'charts') {
        const data = await request('/charts?' + new URLSearchParams({q: query, offset, limit: 100}), {signal: directoryController.signal});
        rows = data.items.map(row => { charts.set(row.id, row); return {kind: 'chart', id: row.id, name: row.definition.title, detail: (row.definition.group || '未分组') + ' · 图表'}; }); next = data.next_offset;
      } else if (metadata.catalog_paged) {
        const data = await window.ResearchCatalog.read({search: query, offset, signal: directoryController.signal}); next = data.next_offset;
        rows = data.indicators.map(row => { catalog.set(row.series_id, row); return {kind: 'indicator', id: row.series_id, name: row.name, detail: [row.category, row.unit, row.source].filter(Boolean).join(' · ')}; });
      } else rows = [...catalog.values()].map(row => ({kind: 'indicator', id: row.series_id, name: row.name, detail: [row.category, row.unit, row.source].filter(Boolean).join(' · ')}));
      if (own !== directoryGeneration) return;
      for (const row of rows) {
        if ((source === 'favorites' || source === 'catalog' && !metadata.catalog_paged) && query && !normalize(`${row.name} ${row.id} ${row.detail}`).includes(normalize(query))) continue;
        const label = document.createElement('label'), checkbox = document.createElement('input'), content = document.createElement('span');
        checkbox.type = 'checkbox'; checkbox.value = row.id; checkbox.dataset.kind = row.kind; checkbox.checked = draft.some(item => key(item) === key(row));
        checkbox.addEventListener('change', () => {
          get('dialog-error').hidden = true;
          if (checkbox.checked && draft.length >= 16) { checkbox.checked = false; get('dialog-error').textContent = '核心关注最多选择 16 项。'; get('dialog-error').hidden = false; return; }
          draft = checkbox.checked ? [...draft, {kind: row.kind, id: row.id, title: row.name}] : draft.filter(item => key(item) !== key(row)); renderSelected();
        });
        const name = document.createElement('b'), detail = document.createElement('small'); name.textContent = row.name; detail.textContent = row.detail;
        content.append(name, detail); label.append(checkbox, content); list.append(label);
      }
      get('no-results').hidden = list.children.length > 0;
      if (offset || next !== null) {
        const pagination = document.createElement('div'); if (offset) pagination.append(button('上一页', () => renderCatalog(Math.max(0, offset - (source === 'charts' ? 100 : 200)))));
        if (next !== null) pagination.append(button('下一页', () => renderCatalog(next))); list.append(pagination);
      }
    } catch (error) { if (own === directoryGeneration && error.name !== 'AbortError') { get('dialog-error').textContent = error.message; get('dialog-error').hidden = false; } }
    finally { if (own === directoryGeneration) list.removeAttribute('aria-busy'); }
  }
  async function saveItems(items) {
    if (!metadata.can_edit_shared) throw new Error('只有研究台编辑成员可以保存共享核心关注。');
    const account = window.AccountFavorites; if (!account) throw new Error('账号自选需要当前来源授权，请重新打开研究台。');
    await account.refresh();
    // The server verifies source scope. This set only validates choices exposed by this dialog.
    const allowed = new Set([...catalog.keys()].map(id => 'indicator:' + id)); charts.forEach((_, id) => allowed.add('chart:' + id));
    account.items.filter(item => item.status !== 'invalid').forEach(item => allowed.add(key(item)));
    const plan = P.focusPlan(items, account.items, allowed); await account.replace(plan.favorites);
    let saved;
    try { saved = await request('/market/core/config', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({items: plan.core})}); }
    catch (error) { throw new Error('账号自选已保存，共享核心关注未保存：' + error.message); }
    if (JSON.stringify(saved.items) !== JSON.stringify(plan.core)) throw new Error('保存的核心关注范围不一致，请刷新后重试。');
    selected = plan.core; announce('工作台核心关注与排列已保存；选中的条目已加入账号自选。移出核心关注不会移出自选。'); await loadCards();
  }
  get('open').addEventListener('click', () => { draft = selected.map(item => ({...item})); get('search').value = ''; get('dialog-error').hidden = true; renderSelected(); void renderCatalog(); dialog.showModal(); get('search').focus(); });
  get('source').addEventListener('change', () => { source = get('source').value; get('search').value = ''; void renderCatalog(); });
  get('search').addEventListener('input', () => { clearTimeout(directoryTimer); directoryGeneration++; directoryController?.abort(); directoryTimer = setTimeout(() => renderCatalog(), 240); });
  get('cancel').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { directoryGeneration++; directoryController?.abort(); clearTimeout(directoryTimer); get('open').focus(); });
  get('save').addEventListener('click', async () => {
    if (get('save').disabled) return; get('save').disabled = true;
    try { await saveItems(draft); dialog.close(); }
    catch (error) { get('dialog-error').textContent = error.message; get('dialog-error').hidden = false; }
    finally { get('save').disabled = !metadata.can_edit_shared; }
  });
  const suggestions = get('suggestions-dialog'), model = get('suggestions-model'), run = get('suggestions-run'), adopt = get('suggestions-adopt');
  let models, running = false, suggested = [];
  const suggestionStatus = message => { get('suggestions-status').textContent = message; };
  function syncSuggestions() { run.disabled = running || !model.value; adopt.disabled = running || !get('suggestions-list').querySelector('input:checked') || !metadata.can_edit_shared; }
  get('suggestions-open')?.addEventListener('click', async () => {
    suggestions.showModal();
    if (!models) {
      model.disabled = true;
      try {
        const data = await request('/strategies/models'); models = data.models || []; model.replaceChildren();
        models.forEach(item => model.append(new Option(item.name, item.id)));
        if (!models.length) { model.append(new Option('暂无可用通用模型', '')); suggestionStatus('当前空间没有可用的通用模型，请先在模型设置中配置。'); }
        else { model.disabled = false; try { const remembered = localStorage.getItem('research-strategy-text-model'); if (models.some(item => item.id === remembered)) model.value = remembered; } catch (_) {} }
      } catch (error) { suggestionStatus('模型列表未读取：' + error.message); }
    }
    syncSuggestions();
  });
  model.addEventListener('change', () => { try { localStorage.setItem('research-strategy-text-model', model.value); } catch (_) {} syncSuggestions(); });
  get('suggestions-close').addEventListener('click', () => suggestions.close());
  run.addEventListener('click', async () => {
    if (running || !model.value) return; running = true; suggested = []; get('suggestions-list').replaceChildren(); syncSuggestions(); suggestionStatus('正在根据研究框架核对工作表，再从真实字段中生成建议…');
    try {
      const data = await request('/market/core/suggestions', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({model_id: model.value})});
      suggested = data.items || [];
      for (const row of suggested) {
        catalog.set(row.series_id, {series_id: row.series_id, name: row.name || row.series_id, unit: row.unit, source: row.sheet});
        const label = document.createElement('label'), checkbox = document.createElement('input'), content = document.createElement('span'), name = document.createElement('b'), reason = document.createElement('small');
        checkbox.type = 'checkbox'; checkbox.value = row.series_id; checkbox.checked = true; checkbox.addEventListener('change', syncSuggestions);
        name.textContent = `${row.group} · ${row.name || row.series_id}`; reason.textContent = row.reason; content.append(name, reason); label.append(checkbox, content); get('suggestions-list').append(label);
      }
      suggestionStatus(data.notice || (suggested.length ? '请核对分组和理由，勾选后采用。' : '模型未给出可采用建议。'));
    } catch (error) { suggestionStatus(error.message); }
    finally { running = false; syncSuggestions(); }
  });
  adopt.addEventListener('click', async () => {
    const ids = new Set([...get('suggestions-list').querySelectorAll('input:checked')].map(input => input.value)); if (!ids.size || running) return;
    running = true; syncSuggestions();
    try { await saveItems(suggested.filter(row => ids.has(row.series_id)).map(row => ({kind: 'indicator', id: row.series_id}))); suggestions.close(); }
    catch (error) { suggestionStatus(error.message); }
    finally { running = false; syncSuggestions(); }
  });
  get('retry').addEventListener('click', loadCards); syncCount(); observeCharts();
  window.addEventListener('pagehide', () => { closed = true; generation++; controller?.abort(); directoryGeneration++; directoryController?.abort(); clearTimeout(directoryTimer); clearCharts(); });
  window.addEventListener('pageshow', event => { if (event.persisted) { closed = false; void loadCards(); } });
});
