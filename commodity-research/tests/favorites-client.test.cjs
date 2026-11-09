const test = require('node:test'), assert = require('node:assert/strict');
const {create, mergeIndicators, legacy} = require('../src/tin/web/static/favorites-client.js');
const item = (id, kind = 'indicator', status = 'active') => ({kind, id, status, added_at: '2026-01-01T00:00:00Z'});
function fixture(initial = [], options = {}) {
  let state = {format: 'favorites.v1', revision: initial.length ? 1 : 0, items: initial}, conflict = options.conflict;
  const calls = [];
  const client = create(async (_, request = {}) => {
    calls.push(request);
    if (options.failure) return {ok: false, status: options.failure, json: async () => ({detail: '合成权限错误'})};
    if (request.method === 'PUT') {
      const body = JSON.parse(request.body);
      if (conflict) {
        conflict = false; state = {...state, revision: state.revision + 1, items: [item('remote', 'chart')]};
        return {ok: false, status: 409, json: async () => ({detail: '账号自选已更新，请重新加载后再修改'})};
      }
      assert.equal(body.revision, state.revision);
      assert.equal(body.format, 'favorites.v1');
      state = {...state, revision: state.revision + 1, items: body.items.map(value => ({...value, status: 'active', added_at: value.added_at || '2026-11-06T00:00:00Z'}))};
    }
    return {ok: true, json: async () => structuredClone(state)};
  });
  return {client, calls};
}

test('opening and rereading a watchlist only GETs and never migrates browser storage', async () => {
  const {client, calls} = fixture();
  await Promise.all([client.ready(), client.ready()]);
  assert.equal(calls.length, 1); assert.equal(calls[0].method, undefined);
  assert.equal(client.revision, 0); assert.deepEqual(client.items, []);
});

test('indicator and chart stars persist the full list with revision and distinct kinds', async () => {
  const {client, calls} = fixture([item('same')]);
  await client.toggle('chart', 'same');
  assert.equal(client.revision, 2); assert.equal(client.items.length, 2);
  await client.toggle('indicator', 'same');
  assert.deepEqual(client.items.map(value => value.kind), ['chart']);
  assert.equal(JSON.parse(calls.at(-1).body).revision, 2);
});

test('a revision conflict reloads the account and never automatically overwrites it', async () => {
  const {client, calls} = fixture([item('first')], {conflict: true});
  await assert.rejects(client.toggle('indicator', 'local'), error => error.status === 409);
  assert.deepEqual(client.items.map(value => value.id), ['remote']);
  assert.equal(calls.filter(call => call.method === 'PUT').length, 1);
  assert.equal(client.error.status, 409); assert.equal(client.saving, false);
});

test('automatic core additions preserve order, chart entries, missing entries and timestamps', async () => {
  const {client, calls} = fixture([item('chart', 'chart'), item('gone', 'indicator', 'invalid'), item('already')]);
  await client.addIndicators(['new', 'already', 'new']);
  const sent = JSON.parse(calls.at(-1).body).items;
  assert.deepEqual(sent.map(value => value.id), ['chart', 'gone', 'already', 'new']);
  assert.equal(sent[1].added_at, '2026-01-01T00:00:00Z');
  assert.equal(Object.hasOwn(sent[1], 'status'), false);
});

test('reordering and removing invalid entries keeps untouched entries', async () => {
  const {client, calls} = fixture([item('gone', 'chart', 'invalid'), item('live')]);
  await client.ready(); await client.replace([...client.items].reverse());
  assert.deepEqual(JSON.parse(calls.at(-1).body).items.map(value => value.id), ['live', 'gone']);
  await client.toggle('chart', 'gone');
  assert.deepEqual(client.items.map(value => value.id), ['live']);
});

test('the 100 item limit rejects core additions without silently evicting anything', async () => {
  const original = Array.from({length: 100}, (_, index) => item(String(index)));
  assert.throws(() => mergeIndicators(original, ['extra']), /100/);
  const {client, calls} = fixture(original);
  await assert.rejects(client.addIndicators(['extra']), /100/);
  assert.equal(calls.some(call => call.method === 'PUT'), false);
  assert.deepEqual(client.items, original);
});

test('403 and 409 errors retain their status and never mark the account loaded', async () => {
  for (const status of [403, 409]) {
    const {client} = fixture([], {failure: status});
    await assert.rejects(client.ready(), error => error.status === status);
    assert.equal(client.loaded, false); assert.equal(client.error.status, status);
  }
});

test('legacy scoped and old unscoped records are offered read-only, without filtering or migration', () => {
  const context = {tenant: 17, user: 'fixture', workbench: 'workbench'};
  const values = new Map([['tin.indicators.favorites.v1', '["old","gone","old"]']]);
  const storage = {getItem: key => values.get(key) ?? null};
  assert.deepEqual(legacy(storage, context), ['old', 'gone']);
  values.set('tin.indicators.favorites.v1:17:fixture:workbench', '["scoped"]');
  assert.deepEqual(legacy(storage, context), ['scoped']);
  values.set('tin.indicators.favorites.v1:17:fixture:workbench', '[]');
  assert.deepEqual(legacy(storage, context), []);
  assert.equal(values.get('tin.indicators.favorites.v1'), '["old","gone","old"]');
});
