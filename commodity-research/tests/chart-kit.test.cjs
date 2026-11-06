const assert = require('node:assert/strict');
const {test} = require('node:test');
const K = require('../src/tin/web/static/chart-kit.js');
const point = (x, value = x) => ({x, value, date: `2030-01-${String(x + 1).padStart(2, '0')}`});
const day = date => Date.parse(date + 'T00:00:00Z') / 86400000;

test('导出字号以1倍图位像素计，小图不低于11/13px，大图按宽度增加且有上限', () => {
  const small = K.exportStyle(160), base = K.exportStyle(640), wide = K.exportStyle(960), maximum = K.exportStyle(1280);
  assert.deepEqual(small, base);
  assert.equal(base.axisFontSize, 11); assert.equal(base.legendFontSize, 11); assert.equal(base.titleFontSize, 13);
  assert.equal(base.currentLineWidth, 2.5); assert.equal(base.lineWidth, 1.5);
  for (const key of ['axisFontSize', 'legendFontSize', 'titleFontSize', 'tableFontSize', 'currentLineWidth', 'lineWidth']) {
    assert.equal(wide[key], base[key] * 1.5); assert.equal(maximum[key], base[key] * 2);
  }
  assert.deepEqual(K.exportStyle(10000), maximum);
  assert.equal(base.text, '#30343b'); assert.equal(base.background, '#ffffff');
  for (const value of [0, -1, NaN, Infinity, '640']) assert.throws(() => K.exportStyle(value), /尺寸无效/);
});

test('年度刻度从模板起点依次跨年，不把9月起点画成自然年', () => {
  const ticks = K.cycleTicks({kind: 'repeat', start: '09-01', period: 'year'}, 0, 364, '2029-09-01');
  assert.deepEqual(ticks.map(t => t.label), ['9月', '10月', '11月', '12月', '1月', '2月', '3月', '4月', '5月', '6月', '7月', '8月']);
  assert.equal(ticks[1].x, 30); assert.equal(ticks[4].x, 122);
  assert.equal(K.cycleTicks({kind: 'repeat', start: '09-01', period: 'year'}, 0, 365, '2029-09-01').length, 12, '往期闰年的额外一天不添加下一周期的9月刻度');
});
test('月份刻度依真实日历处理闰年和非月初起点', () => {
  const ticks = K.cycleTicks({kind: 'repeat', start: '02-15', period: 'year'}, 0, 100, '2028-02-15');
  assert.equal(ticks[1].label, '3月'); assert.equal(ticks[1].x, 15);
  assert.equal(K.cycleTicks({kind: 'repeat', start: '01-01', period: 'year'}, 0, 365, '2028-01-01')[2].x, 60);
});
test('月末观测显示在所在月份中点，按真实月长处理闰年和段起点', () => {
  assert.equal(K.monthMidpoint(day('2023-09-30'), day('2023-09-01')), 15);
  assert.equal(K.monthMidpoint(day('2023-10-31'), day('2023-09-01')), 45.5);
  assert.equal(K.monthMidpoint(day('2023-02-28'), day('2023-02-01')), 14);
  assert.equal(K.monthMidpoint(day('2024-02-29'), day('2024-02-01')), 14.5);
  assert.equal(K.monthMidpoint(day('2024-02-29'), day('2024-02-15')), .5);
});
test('月度月份标签居中，刻度线保留月界；周度和事件刻度不变', () => {
  const template = {kind: 'repeat', start: '09-01', period: 'year'};
  const ticks = K.cycleTicks(template, 0, 365, '2023-09-01', true);
  assert.equal(ticks.length, 12);
  assert.deepEqual(ticks.slice(0, 3), [
    {x: 0, label: '9月', labelX: 15}, {x: 30, label: '10月', labelX: 45.5}, {x: 61, label: '11月', labelX: 76}
  ]);
  const partial = K.cycleTicks({kind: 'repeat', start: '02-15', period: 'year'}, 0, 100, '2024-02-15', true);
  assert.deepEqual(partial[0], {x: -14, label: '2月', labelX: .5});
  const weekly = K.cycleTicks(template, 0, 365, '2023-09-01', false);
  assert.deepEqual(weekly, K.cycleTicks(template, 0, 365, '2023-09-01'));
  assert.deepEqual(weekly[1], {x: 30, label: '10月'});
  assert.deepEqual(K.cycleTicks({kind: 'event'}, -56, 56, '2023-09-01', true), K.cycleTicks({kind: 'event'}, -56, 56));
});
test('月度band按模板起点映射，同月保留n最大点且不修改原数据或填补低样本月', () => {
  const band = [
    {x: 0, min: 1, max: 3, mean: 2, n: 3}, {x: 28, min: 5, max: 9, mean: 7, n: 6},
    {x: 29, min: 0, max: 2, mean: 1, n: 4}, {x: 56, min: 1, max: 2, mean: 1.5, n: 2},
    {x: 84, min: 2, max: 4, mean: 3, n: 4}, {x: 140, min: 3, max: 5, mean: 4, n: 3}
  ];
  const original = structuredClone(band), centered = K.monthlyBand(band, '2023-09-01');
  assert.deepEqual(centered.map(p => [p.x, p.n, p.mean]), [[15, 6, 7], [45.5, 2, 1.5], [76, 4, 3], [137.5, 3, 4]]);
  assert.deepEqual(K.bandRuns(centered, 28).map(run => run.map(p => p.x)), [[15], [76], [137.5]]);
  assert.deepEqual(band, original);
});
test('事件使用相对周，手工分段使用天，走势保留日期', () => {
  const ticks = K.cycleTicks({kind: 'event'}, -56, 56);
  assert.deepEqual(ticks[0], {x: -56, label: '−8周'});
  assert.deepEqual(ticks[ticks.length - 1], {x: 56, label: '+8周'});
  assert.ok(ticks.some(t => t.x === 0 && t.label === '事件日'));
  assert.ok(K.cycleTicks({kind: 'segments'}, 0, 120).every(t => t.label.endsWith('天')));
  const first = Date.parse('2030-01-01T00:00:00Z') / 86400000;
  assert.equal(K.dateTicks(first, first + 3)[0].label, '2030-01-01');
});
test('断线阈值严格大于中位间隔的2.5倍，不生成插值或零点', () => {
  assert.equal(K.shouldBreak(0, 17.5, 7), false); assert.equal(K.shouldBreak(0, 18, 7), true);
  const points = [0, 7, 14, 42, 49].map(x => point(x));
  assert.equal(K.medianInterval(points, true), 7);
  const runs = K.splitObservations(points, true);
  assert.deepEqual(runs.map(run => run.map(p => p.x)), [[0, 7, 14], [42, 49]]);
  assert.equal(runs.flat().length, points.length);
  const raw = [{date: '2030-01-01', value: 0}, {date: '2030-01-02', value: 1}, {date: '2030-01-03', value: 2}, {date: '2030-01-20', value: 3}];
  assert.equal(K.medianInterval(raw), 1); assert.deepEqual(K.splitObservations(raw).map(run => run.length), [3, 1]);
});
test('明确缺失的观测断线；负数和零保留', () => {
  const points = [point(0, -1), point(1, 0), point(2, null), point(3, 2), point(4, NaN)];
  assert.deepEqual(K.splitObservations(points, true).map(run => run.map(p => p.value)), [[-1, 0], [2]]);
});
test('band只画n≥3且数值有效的点，包含当前段之后的完整网格', () => {
  const points = [{x: 0, min: -1, max: 2, mean: .5, n: 2}, {x: 7, min: 0, max: 3, mean: 1, n: 3},
    {x: 350, min: 1, max: 4, mean: 2, n: 5}, {x: 357, min: 4, max: 1, mean: 2, n: 5}, {x: 364, min: null, max: 3, mean: 2, n: 8}];
  assert.deepEqual(K.filterBand(points).map(p => p.x), [7, 350]);
  assert.equal(points.length, 5, '过滤不修改接口结果');
});
test('band不跨过低样本点或接口省略的n=0网格', () => {
  const band = [0, 7, 14, 21, 210, 217].map(x => ({x, min: 0, max: 2, mean: 1, n: x === 7 ? 2 : 3}));
  assert.deepEqual(K.bandRuns(band, 7).map(run => run.map(p => p.x)), [[0], [14, 21], [210, 217]]);
});
test('tooltip取容差内最近点、同距取较早点，超范围或无效值不补造', () => {
  const points = [point(0, 10), point(7, 20), point(14, 30), point(15, null)];
  assert.equal(K.nearestPoint(points, 9, 3).value, 20);
  assert.equal(K.nearestPoint(points, 10, 2), null);
  assert.equal(K.nearestPoint(points, 3.5, 4).x, 0);
  assert.equal(K.nearestPoint([], 0, 3), null);
  const rows = K.tooltipValues([{label: '<历史段>', points}, {label: '当前', current: true, points: [point(8, 0)]}], 9, 2);
  assert.equal(rows[0].label, '<历史段>'); assert.equal(rows[0].point.date, points[1].date);
  assert.equal(rows[1].point.value, 0); assert.equal(rows[1].current, true);
});
test('纵轴以1/2/5步长生成整洁刻度，小数按步长保留一致精度', () => {
  const whole = K.niceAxis(13.93, 89.27);
  assert.equal(whole.step, 20);
  assert.deepEqual(whole.ticks.map(t => t.label), ['0', '20', '40', '60', '80', '100']);
  const decimal = K.niceAxis(.41, 2.29);
  assert.equal(decimal.step, .5);
  assert.deepEqual(decimal.ticks.map(t => t.label), ['0.0', '0.5', '1.0', '1.5', '2.0', '2.5']);
  assert.ok(K.niceAxis(-.00031, .00089).ticks.every(t => !t.label.startsWith('-0.0000')));
});
test('跨量级纵轴覆盖数据，大图5至7个刻度，缩略图最多4个', () => {
  for (let exponent = -6; exponent <= 8; exponent++) {
    for (const [lower, upper] of [[0, 1], [-.2, 1.1], [-20, -3], [9.87, 13.01], [0, 130], [1, 161]]) {
      const min = lower * 10 ** exponent, max = upper * 10 ** exponent;
      for (const thumbnail of [false, true]) {
        const axis = K.niceAxis(min, max, {thumbnail});
        assert.ok(axis.min <= min && axis.max >= max);
        assert.ok(thumbnail ? axis.ticks.length <= 4 : axis.ticks.length >= 5 && axis.ticks.length <= 7);
        assert.ok([1, 2, 5].some(n => Math.abs(axis.step / 10 ** Math.floor(Math.log10(axis.step) + 1e-10) - n) < 1e-8));
        assert.equal(new Set(axis.ticks.map(t => t.label)).size, axis.ticks.length);
        assert.ok(axis.ticks.every(t => Number(t.label) === t.value));
      }
    }
  }
});
test('手动指定纵轴边界保持原值，刻度只落在边界内', () => {
  for (const thumbnail of [false, true]) {
    const axis = K.niceAxis(1.3, 16.1, {fixedMin: true, fixedMax: true, thumbnail});
    assert.equal(axis.min, 1.3); assert.equal(axis.max, 16.1);
    assert.ok(axis.ticks.every(t => t.value >= 1.3 && t.value <= 16.1));
  }
  assert.equal(K.niceAxis(.3, 130, {fixedMin: true}).min, .3);
  assert.equal(K.niceAxis(-130, -.3, {fixedMax: true}).max, -.3);
});
test('tooltip不列出没有有效点的周期，零值段仍保留', () => {
  const rows = K.tooltipValues([{label: '空', points: []}, {label: '无效', points: [point(0, null)]},
    {label: '零值', points: [point(0, 0)]}], 0, 1);
  assert.deepEqual(rows.map(row => row.label), ['零值']);
  assert.equal(rows[0].point.value, 0);
});
