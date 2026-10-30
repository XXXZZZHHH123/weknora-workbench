const test = require('node:test');
const assert = require('node:assert/strict');
const {movingAverage, shape, streakText, boundaryText} = require(process.env.JUDGMENT_CHART_TEST_SOURCE || '../src/tin/web/static/judgment-chart.js');
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
  assert.equal(boundaryText({name: 'Risk', operator: '>', value: 0.000000000001}, 'kg'), 'Risk 高于 0.000000000001 kg');
  assert.match(boundaryText({name: 'Range', operator: 'between', value: 12}), /区间边界 12/);
});

test('axes use real observation dates and values on the same scale as lines and fixed boundaries', () => {
  const data = shape(points([12, 18, 24]), [{kind: 'fixed', value: 30, name: 'Synthetic boundary'}], 2);
  assert.equal(data.axes.x[0].date, '2030-01-01');
  assert.equal(data.axes.x.at(-1).date, '2030-01-03');
  assert.equal(data.axes.x[0].x, data.points[0].x);
  assert.equal(data.axes.x.at(-1).x, data.points.at(-1).x);
  const top = data.axes.y.find(tick => tick.value === 30);
  assert.ok(top, 'Verified thresholds must remain represented by the real numeric scale');
  assert.equal(top.y, data.fixed[0].y);
  assert.equal(data.axes.y.find(tick => tick.value === 12).y, data.points[0].y);
  assert.ok(data.axes.y.every(tick => tick.label && Number.isFinite(tick.value)));
});

test('responsive plotting reserves readable axis gutters and retains boundaries and MA within the plot', () => {
  const data = shape(points([19000, 21000, 20000, 22000, 23000]), [{kind: 'fixed', value: 24000}], 3, 300);
  assert.equal(data.plot.width, 300);
  assert.ok(data.plot.left >= 40 && data.plot.right <= 292);
  assert.equal(data.axes.x.length, 2, 'Narrow charts only need the first and last date labels');
  for (const point of [...data.points, ...data.averages[0].points].filter(point => point.y !== null)) {
    assert.ok(point.x >= data.plot.left && point.x <= data.plot.right);
    assert.ok(point.y >= data.plot.top && point.y <= data.plot.bottom);
  }
  assert.ok(data.fixed[0].y >= data.plot.top && data.fixed[0].y <= data.plot.bottom);
});

test('axes remain finite and readable for flat, tiny and extreme data without inventing dates', () => {
  for (const values of [[0], [12, 12], [0.000000000001, 0.000000000002], [-1.7e308, 1.7e308]]) {
    const data = shape(points(values), [], 0, 280);
    assert.ok(data.axes.y.length >= 1);
    assert.ok(data.axes.y.every(tick => Number.isFinite(tick.value) && Number.isFinite(tick.y) && !/NaN|Infinity/.test(tick.label)));
    assert.ok(data.axes.x.every(tick => points(values).some(point => point.date === tick.date)));
    assert.equal(new Set(data.axes.x.map(tick => tick.date)).size, data.axes.x.length);
  }
  const absent = shape([], [], 0, 280);
  assert.deepEqual(absent.axes, {x: [], y: []});
});

test('MA crossing boundaries remain dynamic and use natural language without a numeric zero substitute', () => {
  assert.equal(boundaryText({kind: 'sma', name: 'Risk', operator: 'crosses_below', window: 20}), 'Risk 下穿 MA20');
  assert.equal(boundaryText({kind: 'sma', name: 'Recovery', operator: 'crosses_above', window: 5}), 'Recovery 上穿 MA5');
  const data = shape(points([3, 6, 9]), [{kind: 'sma', window: 3, operator: 'crosses_below'}], 0, 300);
  assert.equal(data.fixed.length, 0);
  assert.deepEqual(data.averages[0].values, [null, null, 6]);
});
