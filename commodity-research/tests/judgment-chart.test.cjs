const test = require('node:test');
const assert = require('node:assert/strict');
const {movingAverage, shape, streakText} = require('../src/tin/web/static/judgment-chart.js');
const points = values => values.map((value, index) => ({date: `2030-01-${String(index + 1).padStart(2, '0')}`, value}));

test('MA uses a full window and retains missing observations as breaks', () => {
  assert.deepEqual(movingAverage(points([2, 4, 6, null, 10, 12, 14]), 3), [null, null, 4, null, null, null, 12]);
  assert.deepEqual(movingAverage(points([0, 0, 0]), 2), [null, 0, 0]);
  assert.deepEqual(movingAverage(points([2, 4, 6]), 4), [null, null, null]);
  for (const invalid of [1, 121, NaN, 2.5]) assert.deepEqual(movingAverage(points([2, 4]), invalid), [null, null]);
});

test('chart coordinates include verified fixed boundaries and preserve null gaps', () => {
  const data = shape(points([2, null, 6]), [{kind: 'fixed', value: 19, name: 'Risk boundary'}], 2);
  assert.equal(data.points[1].y, null);
  assert.equal(data.fixed[0].value, 19);
  assert.ok(data.fixed[0].y < data.points[2].y);
  assert.ok(data.points[0].y > data.points[2].y);
  assert.deepEqual(data.averages[0].values, [null, null, null]);
});

test('configured MA boundaries display without selecting an extra overlay', () => {
  const data = shape(points([3, 6, 9]), [{kind: 'sma', window: 3}], 0);
  assert.equal(data.averages.length, 1);
  assert.deepEqual(data.averages[0].values, [null, null, 6]);
  assert.equal(shape(points([3, 6, 9]), [{kind: 'sma', window: 3}], 3).averages.length, 1);
});

test('finite opposite extremes and a single observation keep finite chart geometry', () => {
  for (const values of [[-1.7e308, 1.7e308], [0], [12, 12]]) {
    const data = shape(points(values), [], 0);
    assert.ok(data.points.every(point => Number.isFinite(point.x) && Number.isFinite(point.y)));
  }
});

test('inventory labels require explicit semantics and never infer business meaning', () => {
  const streak = {direction: 'up', count: 3, period: '个数据日', at_least: false};
  assert.equal(streakText(streak), '↑ 连升3个数据日');
  assert.equal(streakText(streak, 'inventory'), '↑ 累库3个数据日');
  assert.equal(streakText({...streak, direction: 'down', period: '期'}, 'inventory'), '↓ 去库3期');
  assert.equal(streakText(null, 'inventory'), '');
});

test('boundary tooltip uses natural language and does not round tiny nonzero conditions to zero', () => {
  const {boundaryText} = require('../src/tin/web/static/judgment-chart.js');
  assert.equal(boundaryText({name: 'Risk', operator: '>', value: 0.000000000001}, 'kg'), 'Risk 高于 0.000000000001 kg');
  assert.match(boundaryText({name: 'Range', operator: 'between', value: 12}), /区间边界 12/);
});
