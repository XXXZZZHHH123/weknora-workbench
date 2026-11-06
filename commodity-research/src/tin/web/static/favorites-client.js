(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else {
    root.FavoritesClient = api;
    const client = api.create((path, options) => root.researchFetch((root.RESEARCH_PREFIX || '') + '/api/sn/favorites' + path, options));
    root.AccountFavorites = client;
    const sync = (container = document) => {
      container.querySelectorAll('[data-favorite]').forEach(button => {
        const active = client.items.some(item => item.kind === (button.dataset.favoriteKind || 'indicator') && item.id === button.dataset.favorite);
        const label = button.dataset.favoriteLabel;
        button.setAttribute('aria-pressed', String(active));
        button.disabled = !client.loaded || client.saving;
        if (label) button.setAttribute('aria-label', `${active ? button.dataset.favoriteInvalid ? '移除已失效自选' : '移出自选' : '加入自选'} ${label}`);
        if (button.classList.contains('favorites-star')) {
          const text = active ? '★ 自选' : '☆ 自选';
          if (button.textContent !== text) button.textContent = text;
        }
      });
    };
    client.syncButtons = sync;
    client.subscribe(() => sync());
    document.addEventListener('DOMContentLoaded', () => {
      if (!document.querySelector('[data-favorite], #indicator-workspace, #market-core')) return;
      client.ready().catch(error => root.showToast?.(error.message));
      document.addEventListener('click', async event => {
        const button = event.target.closest('[data-favorite]');
        if (!button || button.disabled) return;
        event.preventDefault();
        try { await client.toggle(button.dataset.favoriteKind || 'indicator', button.dataset.favorite); }
        catch (error) { root.showToast?.(error.message); }
      });
      window.addEventListener('pageshow', event => {
        if (event.persisted) {
          observer.observe(document.body, {childList: true, subtree: true});
          client.refresh().catch(error => root.showToast?.(error.message));
        }
      });
      // Dynamic catalog pages and chart cards use the same account state.
      const observer = new MutationObserver(changes => {
        for (const change of changes) for (const node of change.addedNodes) if (node.nodeType === 1) {
          if (node.matches('[data-favorite]')) sync(node.parentElement);
          else sync(node);
        }
      });
      observer.observe(document.body, {childList: true, subtree: true});
      window.addEventListener('pagehide', () => observer.disconnect());
    });
  }
})(typeof window === 'undefined' ? globalThis : window, function () {
  'use strict';
  const identity = item => `${item.kind}:${item.id}`;
  const payload = items => items.map(({kind, id, added_at}) => ({kind, id, ...(added_at ? {added_at} : {})}));
  function mergeIndicators(items, identifiers) {
    const result = payload(items), seen = new Set(items.map(identity));
    for (const id of new Set(identifiers)) if (!seen.has(`indicator:${id}`)) result.push({kind: 'indicator', id});
    if (result.length > 100) throw new Error('自选最多 100 项，请先移出部分自选。');
    return result;
  }
  function legacy(storage, context) {
    const base = 'tin.indicators.favorites.v1';
    const key = context?.workbench ? `${base}:${context.tenant}:${context.user}:${context.workbench}` : base;
    // Unscoped legacy choices are only offered explicitly, never written on read.
    const raw = storage.getItem(key) ?? (key === base ? null : storage.getItem(base));
    if (raw === null) return [];
    const ids = JSON.parse(raw);
    if (!Array.isArray(ids) || ids.some(id => typeof id !== 'string' || !id || id.length > 128)) throw new Error('本浏览器的旧自选格式无效。');
    return [...new Set(ids)];
  }
  function create(request) {
    let items = [], revision = 0, loaded = false, saving = false, error = null, pending;
    const listeners = new Set();
    const publish = () => listeners.forEach(listener => listener(client));
    async function api(options) {
      const response = await request('', options);
      const data = await response.json();
      if (!response.ok) {
        const failure = new Error(typeof data.detail === 'string' ? data.detail : '账号自选暂时无法保存，请重试。');
        failure.status = response.status; throw failure;
      }
      if (data.format !== 'favorites.v1' || !Number.isSafeInteger(data.revision) || data.revision < 0 ||
          !Array.isArray(data.items) || data.items.length > 100 ||
          data.items.some(item => !['indicator', 'chart'].includes(item.kind) || typeof item.id !== 'string' || !['active', 'invalid'].includes(item.status)) ||
          new Set(data.items.map(identity)).size !== data.items.length) throw new Error('账号自选返回格式无效，请刷新后重试。');
      return data;
    }
    function accept(data) { items = data.items; revision = data.revision; loaded = true; error = null; }
    async function refresh() {
      if (pending) return pending;
      pending = (async () => {
        try { accept(await api()); }
        catch (failure) { error = failure; throw failure; }
        finally { pending = null; publish(); }
      })();
      return pending;
    }
    async function replace(next) {
      if (!loaded) await refresh();
      if (saving) throw new Error('账号自选正在保存，请稍后再试。');
      if (next.length > 100) throw new Error('自选最多 100 项，请先移出部分自选。');
      saving = true; error = null; publish();
      try { accept(await api({method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({format: 'favorites.v1', revision, items: payload(next)})})); }
      catch (failure) {
        if (failure.status === 409) {
          try { await refresh(); } catch (_) { /* Keep the last visible selection and expose the conflict. */ }
        }
        error = failure; throw failure;
      } finally { saving = false; publish(); }
      return items;
    }
    const client = {
      get items() { return items; }, get revision() { return revision; }, get loaded() { return loaded; },
      get saving() { return saving; }, get error() { return error; },
      subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
      ready() { return loaded ? Promise.resolve() : refresh(); }, refresh, replace,
      async toggle(kind, id) {
        await client.ready();
        const exists = items.some(item => item.kind === kind && item.id === id);
        return replace(exists ? items.filter(item => item.kind !== kind || item.id !== id) : [...items, {kind, id}]);
      },
      async addIndicators(identifiers) { await refresh(); return replace(mergeIndicators(items, identifiers)); },
    };
    return client;
  }
  return Object.freeze({create, mergeIndicators, legacy, payload});
});
