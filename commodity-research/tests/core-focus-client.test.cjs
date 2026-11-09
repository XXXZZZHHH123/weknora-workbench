const assert = require('node:assert/strict');
const test = require('node:test');
const P = require('../src/tin/web/static/indicator-preferences.js');
const indicator = id => ({kind: 'indicator', id}), chart = id => ({kind: 'chart', id});

test('focus plans preserve mixed order, deduplicate by kind and merge account favorites without removing entries', () => {
  const selected = [chart('same'), indicator('same'), chart('same'), indicator('other')];
  const existing = [{...indicator('withdrawn'), status: 'invalid', added_at: '2026-01-01'}, {...chart('same'), added_at: '2026-01-02'}];
  const original = JSON.parse(JSON.stringify(existing));
  const result = P.focusPlan(selected, existing, new Set(selected.map(P.identity)));
  assert.deepEqual(result.core, [chart('same'), indicator('same'), indicator('other')]);
  assert.deepEqual(result.favorites, [...existing, indicator('same'), indicator('other')]);
  assert.deepEqual(existing, original);
  assert.deepEqual(P.focusPlan([], result.favorites, new Set()).favorites, result.favorites);
});

test('focus accepts sixteen mixed items and rejects unknown ids and account overflow without truncation', () => {
  const items = Array.from({length: 17}, (_, i) => i % 2 ? chart('c-' + i) : indicator('i-' + i));
  const allowed = new Set(items.map(P.identity));
  assert.equal(P.focusPlan(items.slice(0, 16), [], allowed).core.length, 16);
  assert.throws(() => P.focusPlan(items, [], allowed), /16/);
  assert.throws(() => P.focusPlan([chart('unknown')], [], allowed), /当前目录/);
  const favorites = Array.from({length: 100}, (_, i) => indicator('old-' + i));
  assert.throws(() => P.focusPlan([items[0]], favorites, allowed), /100/);
  assert.equal(favorites.length, 100);
});

test('cycle card latest value uses current segment raw observations and same-period rank n plus one', () => {
  const definition = {time: {mode: 'cycle'}, curves: [{label: '测试曲线'}]};
  const data = {unit: '件', segments: [
    {current: false, points: [{date: '2025-11-10', raw: 999, value: 888}]},
    {current: true, points: [{date: '2026-11-04', raw: 12, value: 100}, {date: '2026-11-06', raw: null, value: null}, {date: '2026-11-05', raw: 14, value: 117}]},
  ], position: {rank: 2, n: 5}};
  assert.deepEqual(P.chartSummary(data, definition), [{label: '测试曲线', value: 14, date: '2026-11-05', unit: '件', rank: 2, n: 5}]);
  assert.equal(P.chartSummary({segments: [], position: null}, definition)[0].value, null);
});

test('trend card summary keeps each curve and ignores missing values', () => {
  const summary = P.chartSummary({curves: [{label: '甲', unit: '%', points: [{date: '2026-11-01', value: 7}, {date: '2026-11-02', value: null}]}, {label: '乙', points: []}]}, {time: {mode: 'recent'}});
  assert.equal(summary[0].value, 7); assert.equal(summary[0].date, '2026-11-01'); assert.equal(summary[1].value, null);
});

test('multi-cycle cards summarize every real or computed curve and keep missing current periods empty', () => {
  const definition = {time: {mode: 'cycle'}, curves: [{label: '量'}, {derived: {op: 'div', a: 'a', b: 'b'}, label: '研究员比值'}, {label: '缺当前'}]};
  const result = (raw, rank, n) => ({segments: [{current: false, points: [{date: '2025-11-05', raw: 999}]},
    {current: true, points: [{date: '2026-11-05', raw, value: 110}, {date: '2026-11-06', raw: null, value: null}]}], position: {rank, n}});
  const data = {mode: 'cycle', curves: [{label: '量', unit: '吨', cycle: result(14, 2, 5)},
    {label: '研究员比值', unit: '倍', cycle: result(2, 3, 7)},
    {label: '缺当前', unit: '吨', cycle: {segments: [{current: false, points: [{date: '2025-11-05', raw: 999}]}], position: null}}]};
  const original = structuredClone(data);
  assert.deepEqual(P.chartSummary(data, definition), [
    {label: '量', value: 14, date: '2026-11-05', unit: '吨', rank: 2, n: 5},
    {label: '研究员比值', value: 2, date: '2026-11-05', unit: '倍', rank: 3, n: 7},
    {label: '缺当前', value: null, date: null, unit: '吨', rank: null, n: null}]);
  assert.deepEqual(data, original);
});

test('transformed trend cards retain original values with their actual source units', () => {
  const summary = P.chartSummary({transform: 'rebase', curves: [{label: '量', unit: '吨', points: [
    {date: '2026-11-01', raw: 20, value: 100}, {date: '2026-11-02', raw: 30, value: 150}]}]}, {time: {mode: 'recent', transform: 'rebase'}});
  assert.deepEqual(summary, [{label: '量', value: 30, date: '2026-11-02', unit: '吨', rank: null, n: null}]);
});

test('chart work is lazy until queued, shares at most four running tasks and survives failed tasks', async () => {
  const enqueue = P.pool(4); let active = 0, peak = 0, started = 0;
  const releases = [];
  const tasks = Array.from({length: 11}, (_, i) => async () => {
    started++; active++; peak = Math.max(peak, active);
    await new Promise(resolve => releases.push(resolve)); active--;
    if (i === 2) throw Error('synthetic failure'); return i;
  });
  assert.equal(started, 0);
  const results = tasks.map(task => enqueue(task).catch(error => error.message));
  for (let round = 0; round < 5; round++) {
    await new Promise(resolve => setImmediate(resolve));
    assert.ok(active <= 4); releases.splice(0).forEach(resolve => resolve());
  }
  const values = await Promise.all(results);
  assert.equal(peak, 4); assert.equal(started, 11); assert.equal(values[2], 'synthetic failure'); assert.equal(values[10], 10);
});
