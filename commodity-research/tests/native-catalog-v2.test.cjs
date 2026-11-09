// Lightweight behavioral fixtures: execute the actual browser scripts in Node VM.
// No browser, Python process, real account, source file, model, or persistent database.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '../src/tin/web/static');
const sid = (letter) => 'kb-' + letter.repeat(48);
const row = (letter) => ({ series_id: sid(letter), name: 'Fixture ' + letter, category: 'Fixture', unit: '件', source: 'Fixture source' });
const deferred = () => { let resolve; const promise = new Promise((done) => { resolve = done; }); return { promise, resolve }; };
const settle = async () => { for (let index = 0; index < 10; index++) await Promise.resolve(); };


function accountFixture(items = []) {
  return {items, loaded: true, saving: false, ready: async () => {}, refresh: async () => {},
    subscribe() {}, syncButtons() {}, replace: async () => { throw Error('Unexpected write'); }};
}

class Element {
  constructor(id) {
    this.id = id; this.children = []; this.listeners = {}; this.dataset = {}; this.attributes = {};
    this.value = ''; this.hidden = false; this.disabled = false; this._html = ''; this.textContent = '';
    this.classList = { toggle() {}, add() {}, remove() {} };
  }
  addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
  async emit(name) { return Promise.all((this.listeners[name] || []).map((fn) => fn({ target: this }))); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; this._html = ''; }
  set innerHTML(value) { this._html = value; this.children = []; }
  get innerHTML() { return this._html; }
  setAttribute(name, value) { this.attributes[name] = value; }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; }
  hasAttribute(name) { return Object.hasOwn(this.attributes, name); }
  querySelector() { return new Element(); }
  querySelectorAll() { return []; }
  focus() {}
  showModal() { this.open = true; }
  close() { this.open = false; void this.emit('close'); }
}

function fixture({ context = {}, storage = new Map(), metadata = {} } = {}) {
  const nodes = new Map();
  const element = (id) => { if (!nodes.has(id)) nodes.set(id, new Element(id)); return nodes.get(id); };
  const ready = [];
  const document = { getElementById: element, createElement: () => new Element(), querySelector: () => null, querySelectorAll: () => [], documentElement: { scrollHeight: 2000 }, addEventListener(name, fn) { if (name === 'DOMContentLoaded') ready.push(fn); } };
  const window = { RESEARCH_CONTEXT: context, RESEARCH_PREFIX: '/workbenches/fixture',
    localStorage: { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: (key) => storage.delete(key) },
    addEventListener() {}, location: { reload() {} } };
  const sandbox = vm.createContext({ window, document, URLSearchParams, URL, AbortController, console, setTimeout, clearTimeout });
  sandbox.researchFetch = (...args) => window.researchFetch(...args);
  element('market-core-catalog').textContent = JSON.stringify(metadata);
  return { window, nodes, element, sandbox, storage, ready,
    load(file) { vm.runInContext(fs.readFileSync(path.join(web, file), 'utf8'), sandbox, { filename: file }); },
    boot() { ready.forEach((fn) => fn()); } };
}

test('shared catalog client sends literal global search and at most 200 metadata rows', async () => {
  const f = fixture(), requests = [];
  f.window.researchFetch = async (url, options) => {
    requests.push([new URL(url, 'http://fixture.invalid'), options]);
    return { ok: true, json: async () => ({ indicators: [row('a')], total: 202, offset: 200, next_offset: 201 }) };
  };
  f.load('catalog-client.js');
  await f.window.ResearchCatalog.read({ search: '[literal] A%', offset: 200, identifiers: [sid('a')] });
  assert.equal(requests[0][0].searchParams.get('search'), '[literal] A%');
  assert.equal(requests[0][0].searchParams.get('limit'), '200');
  assert.equal(requests[0][0].searchParams.get('offset'), '200');
  assert.equal(requests[0][0].searchParams.get('format'), 'metadata');
  assert.deepEqual(requests[0][0].searchParams.getAll('series_id'), [sid('a')]);
});

test('shared catalog rejects a pagination cursor that skips actual rows', async () => {
  const f = fixture();
  f.window.researchFetch = async () => ({ ok: true, json: async () => ({ indicators: [row('a')], total: 500, offset: 0, next_offset: 200 }) });
  f.load('catalog-client.js');
  await assert.rejects(f.window.ResearchCatalog.read(), /分页信息/);
});

test('indicator directory search keeps the selected historical date', async () => {
  const f = fixture(), requests = [];
  Object.assign(f.element('indicator-workspace').dataset, { catalogRemote: 'true', catalogTotal: '0', catalogOffset: '0', date: '2026-01-02' });
  f.element('indicator-workspace').querySelector = () => null;
  f.sandbox.location = { href: 'http://fixture.invalid/sn/indicators?date=2026-01-02', hash: '' };
  f.sandbox.history = { replaceState() {} }; f.sandbox.localStorage = f.window.localStorage;
  Object.assign(f.window, { scrollY: 0, innerHeight: 900 });
  f.window.researchFetch = async (url) => {
    requests.push(new URL(url, 'http://fixture.invalid'));
    return { ok: true, json: async () => ({ indicators: [], total: 0, offset: 0, next_offset: null, html: '', nav_html: '' }) };
  };
  f.load('indicator-preferences.js'); f.load('indicators.js'); f.boot();
  await f.element('indicator-search-clear').emit('click'); await settle();
  assert.equal(requests[0].searchParams.get('date'), '2026-01-02');
});

test('off-page favorite retains its server-rendered accessible name without another metadata request', async () => {
  const f = fixture({ storage: new Map([['tin.indicators.favorites.v1', JSON.stringify([sid('b')])]]) }), requests = [];
  const root = f.element('indicator-workspace'), favorite = new Element();
  Object.assign(root.dataset, { catalogRemote: 'true', catalogTotal: '0', catalogOffset: '0' });
  favorite.dataset.favorite = sid('b'); favorite.setAttribute('aria-label', '移出 Fixture b');
  root.querySelectorAll = (selector) => selector === '[data-favorite]' ? [favorite] : [];
  root.querySelector = () => null;
  f.sandbox.location = { href: 'http://fixture.invalid/sn/indicators', search: '', hash: '' };
  f.sandbox.history = { replaceState() {} }; f.sandbox.localStorage = f.window.localStorage; f.sandbox.performance = { now: () => 5000 };
  Object.assign(f.window, { scrollY: 0, innerHeight: 900 });
  f.window.researchFetch = async (url) => { requests.push(url); return { ok: true, json: async () => ({ cards: [], html: '' }) }; };
  f.window.AccountFavorites = accountFixture([{kind: 'indicator', id: sid('b'), status: 'active'}]);
  f.window.AccountFavorites.syncButtons = () => favorite.setAttribute('aria-pressed', 'true');
  f.load('indicator-preferences.js'); f.load('indicators.js'); f.boot(); await settle();
  assert.equal(favorite.getAttribute('aria-label'), '移出 Fixture b');
  assert.equal(favorite.getAttribute('aria-pressed'), 'true');
  assert.ok(requests.every((url) => !new URL(url, 'http://fixture.invalid').pathname.endsWith('/catalog')));
});

function pagedIndicators({ total = 405, offset = 0, count = 200, favorites = [] } = {}) {
  const storage = new Map([['tin.indicators.favorites.v1', JSON.stringify(favorites)]]);
  const f = fixture({ storage }), requests = [], previewRequests = [], compareRequests = [], maps = [], root = f.element('indicator-workspace');
  f.sandbox.Map = class ObservedMap extends Map { constructor(entries) { super(entries); maps.push(this); } };
  const header = new Element('indicator-header-total');
  const makeRows = (start, length) => Array.from({ length }, (_, index) => {
    const element = new Element();
    Object.assign(element.dataset, { seriesId: 'fixture.' + (start + index), name: 'Fixture field ' + (start + index), search: 'Fixture field ' + (start + index) });
    return element;
  });
  let visibleRows = makeRows(offset, count);
  Object.assign(root.dataset, { catalogRemote: 'true', catalogTotal: String(total), catalogOffset: String(offset) });
  root.querySelector = () => null;
  root.querySelectorAll = (selector) => selector === '[data-indicator-row]' ? visibleRows : selector === '[data-catalog-total-count]' ? [header] : [];
  Object.defineProperty(f.element('indicator-catalog-groups'), 'innerHTML', {
    get() { return ''; }, set(value) { const data = JSON.parse(value); visibleRows = makeRows(data.offset, data.count); },
  });
  f.sandbox.location = { href: 'http://fixture.invalid/sn/indicators', search: '', hash: '' };
  f.sandbox.history = { replaceState() {} }; f.sandbox.localStorage = f.window.localStorage;
  Object.assign(f.window, { scrollY: 0, innerHeight: 900 });
  f.window.researchFetch = async (url) => {
    const parsed = new URL(url, 'http://fixture.invalid'), query = parsed.searchParams;
    if (parsed.pathname.endsWith('/previews')) {
      previewRequests.push(query); return { ok: true, json: async () => ({ cards: [], html: '' }) };
    }
    if (parsed.pathname.endsWith('/compare')) {
      compareRequests.push(query); return { ok: true, json: async () => ({ status: 'unavailable', reason: 'Fixture has no shared observations' }) };
    }
    requests.push(query);
    const at = Number(query.get('offset')), matched = query.get('search') === 'no-match' ? 0 : query.get('search') ? 1 : total;
    const length = Math.min(200, Math.max(0, matched - at));
    return { ok: true, json: async () => ({ indicators: Array.from({ length }, (_, i) => ({ series_id: 'fixture.' + (at + i) })),
      total: matched, offset: at, next_offset: at + length < matched ? at + length : null,
      html: JSON.stringify({ offset: at, count: length }), nav_html: '' }) };
  };
  f.window.AccountFavorites = accountFixture(favorites.map(id => ({kind: 'indicator', id, status: 'active'})));
  f.load('indicator-preferences.js'); f.load('indicators.js'); f.boot();
  return { ...f, requests, previewRequests, compareRequests, header, namesCache: maps.find(map => map.has('fixture.' + offset)) };
}

test('top and bottom indicator pagers show actual totals and only request explicit pages', async () => {
  const f = pagedIndicators();
  assert.equal(f.header.textContent, '405');
  assert.equal(f.element('indicator-result-count').textContent, '本页 200 项 · 范围共 405 项');
  assert.deepEqual(f.requests, [], 'Opening the page must not preload other metadata pages');
  for (const suffix of ['-top', '']) {
    assert.equal(f.element('indicator-page-status' + suffix).textContent, '1–200 / 405 项');
    assert.equal(f.element('indicator-prev-page' + suffix).disabled, true);
    assert.equal(f.element('indicator-next-page' + suffix).disabled, false);
  }
  await f.element('indicator-next-page-top').emit('click'); await settle();
  for (const suffix of ['-top', '']) assert.equal(f.element('indicator-page-status' + suffix).textContent, '201–400 / 405 项');
  await f.element('indicator-next-page').emit('click'); await settle();
  for (const suffix of ['-top', '']) {
    assert.equal(f.element('indicator-page-status' + suffix).textContent, '401–405 / 405 项');
    assert.equal(f.element('indicator-next-page' + suffix).disabled, true);
  }
  await f.element('indicator-prev-page-top').emit('click'); await settle();
  assert.deepEqual(f.requests.map(query => query.get('offset')), ['200', '400', '200']);
  assert.ok(f.requests.every(query => query.get('limit') === '200'));
});

test('search and clear reset both indicator pagers, including no-result state', async () => {
  const f = pagedIndicators({ offset: 400, count: 5 });
  f.element('indicator-search').value = 'off-page literal';
  await f.element('indicator-search').emit('input');
  await new Promise(resolve => setTimeout(resolve, 280)); await settle();
  assert.equal(f.requests.at(-1).get('search'), 'off-page literal');
  assert.equal(f.requests.at(-1).get('offset'), '0');
  assert.equal(f.header.textContent, '1');
  for (const suffix of ['-top', '']) assert.equal(f.element('indicator-page-status' + suffix).textContent, '1–1 / 1 项');
  f.element('indicator-search').value = 'no-match';
  await f.element('indicator-search').emit('input');
  await new Promise(resolve => setTimeout(resolve, 280)); await settle();
  for (const suffix of ['-top', '']) {
    assert.equal(f.element('indicator-page-status' + suffix).textContent, '0–0 / 0 项');
    assert.equal(f.element('indicator-prev-page' + suffix).disabled, true);
    assert.equal(f.element('indicator-next-page' + suffix).disabled, true);
  }
  assert.equal(f.element('indicator-no-results').hidden, false);
  await f.element('indicator-search-clear-input').emit('click'); await settle();
  assert.equal(f.requests.at(-1).get('offset'), '0');
  assert.equal(f.requests.at(-1).get('search'), '');
  assert.equal(f.header.textContent, '405');
  assert.equal(f.element('indicator-result-count').textContent, '本页 200 项 · 范围共 405 项');
  assert.equal(f.requests.length, 3);
});

test('long indicator paging retains only current, favorite and compared names without extra reads', async () => {
  const favoriteIds = Array.from({ length: 40 }, (_, index) => 'fixture.' + index);
  const f = pagedIndicators({ total: 2400, favorites: favoriteIds }); await settle();
  assert.equal(f.namesCache.size, 200);
  const chosen = [40, 41, 42, 43].map(index => 'fixture.' + index);
  for (const identifier of chosen) {
    const box = new Element(); box.dataset.compare = identifier; box.checked = true;
    box.closest = selector => selector === '[data-compare]' ? box : null;
    f.element('indicator-workspace').listeners.change[0]({ target: box });
  }
  for (let page = 1; page < 12; page++) {
    await f.element('indicator-next-page-top').emit('click'); await settle();
    assert.ok(f.namesCache.size <= 244, 'Visited metadata names must not accumulate beyond current 200 + favorite 40 + comparison 4');
    for (const identifier of [...favoriteIds, ...chosen]) assert.ok(f.namesCache.has(identifier));
    for (const index of [40, 41, 42, 43]) assert.match(f.element('indicator-selection').textContent, new RegExp('Fixture field ' + index));
  }
  assert.equal(f.namesCache.size, 244);
  await f.element('indicator-compare-open').emit('click'); await settle();
  assert.deepEqual(f.compareRequests[0].getAll('series_id'), chosen);
  assert.deepEqual(f.requests.map(query => Number(query.get('offset'))), Array.from({ length: 11 }, (_, index) => (index + 1) * 200));
  assert.equal(f.previewRequests.length, 0, 'Catalog paging must not read favorites previews');
  assert.equal(f.compareRequests.length, 1);
});

test('diagram metadata pagination survives category expansion and renders next page', async () => {
  const f = fixture({ context: { scope_version: 2 } }), requests = [];
  f.window.researchFetch = async (url) => {
    const offset = Number(new URL(url, 'http://fixture.invalid').searchParams.get('offset')); requests.push(offset);
    return { ok: true, json: async () => ({ indicators: [row(offset ? 'b' : 'a')], total: 2, offset, next_offset: offset ? null : 1 }) };
  };
  f.load('catalog-client.js'); f.load('diagram.js');
  vm.runInContext("dg.layout={nodes:[{id:'n',label:'Fixture node',binding:{}}]};dg.active='n'", f.sandbox);
  await vm.runInContext('dgFetchPool()', f.sandbox);
  assert.equal(f.element('dg-pool').children.find((child) => child.textContent === '下一页').textContent, '下一页');
  vm.runInContext("dgToggleCat('Fixture')", f.sandbox);
  const next = f.element('dg-pool').children.find((child) => child.textContent === '下一页');
  assert.ok(next, 'Re-rendering a category must keep the metadata pager');
  await next.emit('click');
  assert.deepEqual(requests, [0, 1]);
  assert.match(f.element('dg-pool-count').textContent, /2–2 \/ 2/);
  assert.ok(f.element('dg-pool').children.find((child) => child.textContent === '上一页'));
});

test('diagram stale metadata response cannot replace the newer search page', async () => {
  const f = fixture({ context: { scope_version: 2 } }), first = deferred(), second = deferred(); let requests = 0;
  f.window.researchFetch = () => (++requests === 1 ? first.promise : second.promise);
  f.load('catalog-client.js'); f.load('diagram.js');
  vm.runInContext("dg.layout={nodes:[{id:'n',label:'Fixture node',binding:{}}]};dg.active='n'", f.sandbox);
  const old = vm.runInContext('dgFetchPool()', f.sandbox);
  f.element('dg-search').value = 'B';
  const fresh = vm.runInContext('dgFetchPool()', f.sandbox);
  second.resolve({ ok: true, json: async () => ({ indicators: [row('b')], total: 1, offset: 0, next_offset: null }) }); await fresh;
  first.resolve({ ok: true, json: async () => ({ indicators: [row('a')], total: 1, offset: 0, next_offset: null }) }); await old;
  assert.equal(vm.runInContext('dg.pool[0].field', f.sandbox), sid('b'));
});

test('diagram stale suggestions must never be displayed for another active node', async () => {
  const f = fixture({ context: { scope_version: 2 } }), first = deferred(), second = deferred(); let requests = 0;
  f.sandbox.researchFetch = () => (++requests === 1 ? first.promise : second.promise);
  f.load('diagram.js');
  vm.runInContext("dg.layout={nodes:[{id:'a',label:'A',binding:{}},{id:'b',label:'B',binding:{}}]};dg.active='a'", f.sandbox);
  const old = vm.runInContext('dgSuggest(dg.layout.nodes[0])', f.sandbox);
  vm.runInContext("dg.active='b'", f.sandbox);
  const fresh = vm.runInContext('dgSuggest(dg.layout.nodes[1])', f.sandbox);
  const response = (letter) => ({ ok: true, json: async () => ({ suggestions: [{ ...row(letter), points: null, why: 'Fixture literal match', frequency: '日' }] }) });
  second.resolve(response('b')); await fresh;
  first.resolve(response('a')); await old;
  assert.match(f.element('dg-suggest').innerHTML, /Fixture b/);
  assert.doesNotMatch(f.element('dg-suggest').innerHTML, /Fixture a/);
});

test('v2 shared core_items survive a bounded catalog and ignore old personal browser selections', async () => {
  const context = {tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench', scope_version: 2};
  const browserKey = 'tin.market.core.v1:17:fixture-user:fixture-workbench';
  const items = [{kind: 'indicator', id: sid('b')}, {kind: 'chart', id: 'selected-chart'}];
  const f = fixture({context, storage: new Map([[browserKey, JSON.stringify([sid('a')])]]),
    metadata: {indicators: [row('a')], defaults: [sid('a')], core_items: items,
      charts: [{id: 'selected-chart', title: 'Fixture chart'}], catalog_paged: true, can_edit_shared: false}});
  const requests = [];
  f.window.researchFetch = async url => {
    const parsed = new URL(url, 'http://fixture.invalid'); requests.push(parsed);
    assert.ok(parsed.pathname.endsWith('/core'), 'Saved shared items are already authorized server metadata; no browser restoration catalog reads');
    return {ok: true, json: async () => ({items, ready_count: 1, html: '<div>Fixture B card and chart</div>'})};
  };
  f.load('indicator-preferences.js'); f.load('catalog-client.js'); f.load('market-overview.js'); f.boot(); await settle();
  assert.equal(f.element('market-core-count').textContent, '2');
  assert.equal(requests.length, 0, 'Initial server-rendered core does not issue a restoration request');
  await f.element('market-core-retry').emit('click'); await settle();
  assert.deepEqual(requests[0].searchParams.getAll('item'), ['indicator:' + sid('b'), 'chart:selected-chart']);
  assert.deepEqual(requests[0].searchParams.getAll('series_id'), []);
  assert.equal(f.element('market-core-grid').innerHTML, '<div>Fixture B card and chart</div>');
  assert.equal(f.storage.get(browserKey), JSON.stringify([sid('a')]), 'Old browser selections are neither migrated nor allowed to replace shared core');
});

test('adding shared v2 core does not silently evict an existing full account watchlist', async () => {
  const context = {tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench', scope_version: 2};
  const favoriteKey = 'tin.indicators.favorites.v1:17:fixture-user:fixture-workbench';
  const original = Array.from({length: 100}, (_, index) => 'kb-' + index.toString(16).padStart(48, '0'));
  const f = fixture({context, storage: new Map([[favoriteKey, JSON.stringify(original)]]), metadata: {
    indicators: [row('a')], core_items: [{kind: 'indicator', id: sid('a')}], catalog_paged: true, can_edit_shared: true}});
  f.window.researchFetch = async () => {throw Error('No write or card read is allowed after account overflow');};
  f.window.AccountFavorites = accountFixture(original.map(id => ({kind: 'indicator', id, status: 'active'})));
  f.load('indicator-preferences.js'); f.load('catalog-client.js'); f.load('market-overview.js'); f.boot();
  await f.element('market-core-open').emit('click'); await settle();
  await f.element('market-core-save').emit('click'); await settle();
  assert.deepEqual(f.window.AccountFavorites.items.map(item => item.id), original);
  assert.deepEqual(JSON.parse(f.storage.get(favoriteKey)), original, 'Legacy browser watchlist must remain untouched');
  assert.equal(f.element('market-core-dialog-error').hidden, false);
  assert.match(f.element('market-core-dialog-error').textContent, /100/);
});

test('saving the actual mixed picker writes shared items and account favorites and leaves legacy browser data untouched', async () => {
  const {create} = require('../src/tin/web/static/favorites-client.js');
  const context = {tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench', scope_version: 2};
  const legacyKey = 'tin.indicators.favorites.v1:17:fixture-user:fixture-workbench';
  const items = [{kind: 'chart', id: 'selected-chart'}, {kind: 'indicator', id: sid('a')}];
  const f = fixture({context, storage: new Map([[legacyKey, '["browser-only"]']]), metadata: {
    indicators: [row('a')], core_items: items, charts: [{id: 'selected-chart', title: 'Selected chart'}], can_edit_shared: true}});
  let account = {format: 'favorites.v1', revision: 3, items: [
    {kind: 'chart', id: 'existing-chart', status: 'active'},
    {kind: 'indicator', id: sid('b'), status: 'invalid', added_at: '2026-01-01T00:00:00Z'}]};
  const requests = [];
  f.window.researchFetch = async (url, options = {}) => {
    const parsed = new URL(url, 'http://fixture.invalid'); requests.push([parsed, options]);
    if (parsed.pathname.endsWith('/favorites')) {
      if (options.method === 'PUT') {
        const body = JSON.parse(options.body); assert.equal(body.revision, 3);
        account = {...account, revision: 4, items: body.items.map(item => ({...item, status: 'active'}))};
      }
      return {ok: true, json: async () => structuredClone(account)};
    }
    if (parsed.pathname.endsWith('/config')) return {ok: true, json: async () => ({items: JSON.parse(options.body).items})};
    assert.ok(parsed.pathname.endsWith('/core'));
    return {ok: true, json: async () => ({items, ready_count: 1, html: '<div>Fixture mixed core</div>'})};
  };
  f.window.AccountFavorites = create((_, options) => f.window.researchFetch('/api/sn/favorites', options));
  f.load('indicator-preferences.js'); f.load('market-overview.js'); f.boot();
  await f.element('market-core-open').emit('click'); await settle();
  await f.element('market-core-save').emit('click'); await settle();
  assert.deepEqual(account.items.map(item => item.id), ['existing-chart', sid('b'), 'selected-chart', sid('a')]);
  assert.equal(account.items[1].added_at, '2026-01-01T00:00:00Z');
  assert.equal(f.storage.get(legacyKey), '["browser-only"]');
  assert.match(f.element('market-core-notice').textContent, /已加入账号自选/);
  assert.equal(requests.filter(([, options]) => options.method === 'PUT').length, 1);
  const save = requests.find(([url]) => url.pathname.endsWith('/config'));
  assert.deepEqual(JSON.parse(save[1].body), {items});
  const read = requests.find(([url]) => url.pathname.endsWith('/core'));
  assert.deepEqual(read[0].searchParams.getAll('item'), ['chart:selected-chart', 'indicator:' + sid('a')]);
});

test('actual picker accepts sixteen entries and rejects a seventeenth chart without altering the draft', async () => {
  const indicators = Array.from({length: 16}, (_, i) => ({...row('a'), series_id: 'kb-' + i.toString(16).padStart(48, '0'), name: 'Fixture ' + i}));
  const items = indicators.map(row => ({kind: 'indicator', id: row.series_id}));
  const f = fixture({metadata: {indicators, core_items: items, can_edit_shared: true}});
  f.window.AccountFavorites = accountFixture();
  f.window.researchFetch = async url => {
    assert.ok(new URL(url, 'http://fixture.invalid').pathname.endsWith('/charts'));
    return {ok: true, json: async () => ({items: [{id: 'extra-chart', definition: {title: 'Extra chart'}}], total: 1, next_offset: null})};
  };
  f.load('indicator-preferences.js'); f.load('market-overview.js'); f.boot();
  await f.element('market-core-open').emit('click'); await settle();
  assert.equal(f.element('market-core-selected').children.length, 16);
  assert.equal(f.element('market-core-draft-count').textContent, '16/16');
  f.element('market-core-source').value = 'charts'; await f.element('market-core-source').emit('change'); await settle();
  const checkbox = f.element('market-core-choices').children[0].children[0]; checkbox.checked = true; await checkbox.emit('change');
  assert.equal(checkbox.checked, false);
  assert.match(f.element('market-core-dialog-error').textContent, /16/);
  assert.equal(f.element('market-core-selected').children.length, 16);
});
