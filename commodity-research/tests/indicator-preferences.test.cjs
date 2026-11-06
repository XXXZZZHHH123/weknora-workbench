const assert = require('node:assert/strict');
const test = require('node:test');
const path = require('node:path');
const preferences = require(process.env.MARKET_PREFERENCES || path.join(__dirname, '../src/tin/web/static/indicator-preferences.js'));

const item = id => ({kind: 'indicator', id});
const context = { tenant: 17, user: 'fixture-user', workbench: 'fixture-workbench' };
const allowed = new Set(Array.from({ length: 105 }, (_, index) => `field-${index}`));
function memory() {
  const values = new Map();
  return { values, getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
}

test('core selection preserves order, auto-adds favorites and keeps favorites when core is removed', () => {
  const first = preferences.plan(['field-3', 'field-1', 'field-3'], ['field-2', 'field-3'].map(item), allowed);
  assert.deepEqual(first, { core: ['field-3', 'field-1'], favorites: ['field-2', 'field-3', 'field-1'].map(item) });
  const second = preferences.plan(['field-1'], first.favorites, allowed);
  assert.deepEqual(second, { core: ['field-1'], favorites: ['field-2', 'field-3', 'field-1'].map(item) });
  assert.deepEqual(preferences.plan([], second.favorites, allowed).favorites, second.favorites);
});

test('the existing favorites key is preserved and selections isolate real actor/tenant/workbench identity', () => {
  assert.equal(preferences.key(context), 'tin.indicators.favorites.v1:17:fixture-user:fixture-workbench');
  const keys = [context, { ...context, tenant: 18 }, { ...context, user: 'other-user' }, { ...context, workbench: 'other-workbench' }].map((value) => preferences.key(value, 'core'));
  assert.equal(new Set(keys).size, 4);
  assert.equal(preferences.key(null), 'tin.indicators.favorites.v1');
  const storage = memory();
  assert.equal(preferences.saveCore(storage, context, preferences.plan(['field-1'], [item('field-2')], allowed)), true);
  assert.deepEqual(preferences.read(storage, keys[0], allowed, 12), ['field-1']);
  assert.equal(preferences.read(storage, keys[1], allowed, 12), null);
});

test('only current real catalog IDs survive persisted choices, including an explicitly empty selection', () => {
  const storage = memory(), key = preferences.key(context, 'core');
  storage.setItem(key, JSON.stringify(['field-4', 'withdrawn', 'field-4', 'field-0']));
  assert.deepEqual(preferences.read(storage, key, allowed, 12), ['field-4', 'field-0']);
  storage.setItem(key, '[]'); assert.deepEqual(preferences.read(storage, key, allowed, 12), []);
  for (const raw of ['{}', '[3]', '{broken']) {
    storage.setItem(key, raw); assert.throws(() => preferences.read(storage, key, allowed, 12));
  }
  assert.throws(() => preferences.plan(['not-in-this-kb'], [], allowed), /当前知识库/);
});

test('limits never silently evict favorites or truncate an explicit selection', () => {
  assert.equal(preferences.plan([...allowed].slice(0, 16), [], allowed).core.length, 16);
  assert.throws(() => preferences.plan([...allowed].slice(0, 17), [], allowed), /16/);
  const favorites = [...allowed].slice(0, 100).map(item), copy = [...favorites];
  assert.throws(() => preferences.plan(['field-104'], favorites, allowed), /100/);
  assert.deepEqual(favorites, copy);
  assert.equal(preferences.plan(['field-2'], favorites, allowed).favorites.length, 100);
});

test('saving browser core never writes or migrates legacy browser favorites', () => {
  const storage = memory(), coreKey = preferences.key(context, 'core'), favoritesKey = preferences.key(context);
  storage.setItem(favoritesKey, '["field-2"]');
  assert.equal(preferences.saveCore(storage, context, preferences.plan(['field-1'], [], allowed)), true);
  assert.equal(storage.getItem(coreKey), '["field-1"]');
  assert.equal(storage.getItem(favoritesKey), '["field-2"]');
});

test('a browser denying storage is reported without claiming persistence', () => {
  const denied = { setItem() { throw Error('denied'); } };
  assert.equal(preferences.saveCore(denied, context, preferences.plan(['field-1'], [], allowed)), false);
});

test('core auto-add preserves existing charts, invalid items, timestamps and order', () => {
  const original = [{kind: 'chart', id: 'chart-1', added_at: '2026-01-01'},
    {kind: 'indicator', id: 'withdrawn', status: 'invalid'}, item('field-2')];
  const result = preferences.plan(['field-2', 'field-4'], original, allowed);
  assert.deepEqual(result.favorites, [...original, item('field-4')]);
  assert.deepEqual(original, result.favorites.slice(0, 3));
});
