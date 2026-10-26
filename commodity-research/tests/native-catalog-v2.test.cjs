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
  f.load('indicator-preferences.js'); f.load('indicators.js'); f.boot(); await settle();
  assert.equal(favorite.getAttribute('aria-label'), '移出 Fixture b');
  assert.equal(favorite.getAttribute('aria-pressed'), 'true');
  assert.ok(requests.every((url) => !new URL(url, 'http://fixture.invalid').pathname.endsWith('/catalog')));
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

test('v2 personal core survives reload when its ID is outside the initial bounded projection', async () => {
  const context = { tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench', scope_version: 2 };
  const storage = new Map([['tin.market.core.v1:17:fixture-user:fixture-workbench', JSON.stringify([sid('b')])]]);
  const f = fixture({ context, storage, metadata: { indicators: [row('a')], defaults: [sid('a')], catalog_paged: true, can_edit_shared: false } });
  const requests = [];
  f.window.researchFetch = async (url) => {
    const parsed = new URL(url, 'http://fixture.invalid'); requests.push(parsed);
    if (parsed.pathname.endsWith('/catalog')) return { ok: true, json: async () => ({ indicators: [row('b')], total: 1, offset: 0, next_offset: null }) };
    return { ok: true, json: async () => ({ series_ids: [sid('b')], ready_count: 1, html: '<div>Fixture B card</div>' }) };
  };
  f.load('indicator-preferences.js'); f.load('catalog-client.js'); f.load('market-overview.js'); f.boot(); await settle();
  assert.equal(f.element('market-core-count').textContent, '1', 'An off-page valid saved core must not disappear');
  assert.ok(requests.some((url) => url.pathname.endsWith('/catalog') && url.searchParams.getAll('series_id').includes(sid('b'))), 'Validate saved IDs against the actual authorized global metadata catalog');
  assert.ok(requests.some((url) => url.pathname.endsWith('/core') && url.searchParams.getAll('series_id').includes(sid('b'))));
});

test('adding v2 core does not silently evict an existing full watchlist', async () => {
  const context = { tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench', scope_version: 2 };
  const favoriteKey = 'tin.indicators.favorites.v1:17:fixture-user:fixture-workbench';
  const original = Array.from({ length: 40 }, (_, index) => 'kb-' + index.toString(16).padStart(48, '0'));
  const f = fixture({ context, storage: new Map([[favoriteKey, JSON.stringify(original)]]), metadata: { indicators: [row('a')], defaults: [sid('a')], catalog_paged: true, can_edit_shared: false } });
  f.window.researchFetch = async (url) => new URL(url, 'http://fixture.invalid').pathname.endsWith('/catalog')
    ? { ok: true, json: async () => ({ indicators: [row('a')], total: 1, offset: 0, next_offset: null }) }
    : { ok: true, json: async () => ({ series_ids: [sid('a')], ready_count: 1, html: '<div>Fixture A card</div>' }) };
  f.load('indicator-preferences.js'); f.load('catalog-client.js'); f.load('market-overview.js'); f.boot();
  await f.element('market-core-open').emit('click'); await settle();
  await f.element('market-core-save').emit('click'); await settle();
  assert.deepEqual(JSON.parse(f.storage.get(favoriteKey)), original, 'A 40-item watchlist must be preserved instead of truncating its last item');
  assert.equal(f.element('market-core-dialog-error').hidden, false);
  assert.match(f.element('market-core-dialog-error').textContent, /40/);
});
