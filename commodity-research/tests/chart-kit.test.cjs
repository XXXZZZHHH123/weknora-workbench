const assert = require('node:assert/strict');
const {test} = require('node:test');
const K = require('../src/tin/web/static/chart-kit.js');
const point = (x, value = x) => ({x, value, date: `2030-01-${String(x + 1).padStart(2, '0')}`});
const day = date => Date.parse(date + 'T00:00:00Z') / 86400000;

test('规则叠加只识别真实绑定，固定水平线去重且变换使用每段原始起点', () => {
  const series = [
    {key: 0, series_id: 'kb-bound', axis: 'left', points: [{date: '2030-01-01', x: 0, value: 0, raw: 10}]},
    {key: 1, series_id: 'kb-bound', axis: 'left', points: [{date: '2029-01-01', x: 0, value: 0, raw: 20}]},
    {key: 2, derived: {a: 'kb-bound', b: 'kb-other', op: 'sub'}, axis: 'left', points: []},
    {key: 3, series_id: 'kb-other', label: 'kb-bound', axis: 'right', points: []}
  ], rules = [{kind: 'fixed', rule_id: 'r', series_id: 'kb-bound', value: 30}];
  const original = structuredClone({series, rules});
  const raw = K.ruleOverlays(series, rules);
  assert.equal(raw.length, 1); assert.deepEqual(raw[0].keys, [0, 1]);
  assert.deepEqual(K.ruleOverlays(series, rules, 'change').map(item => [item.value, item.keys]), [[20, [0]], [10, [1]]]);
  assert.deepEqual(K.ruleOverlays(series, rules, 'rebase').map(item => item.value), [300, 150]);
  series[0].points[0].raw = 0;
  assert.equal(K.ruleOverlays(series, rules, 'rebase').length, 1);
  series[0].points[0].raw = 10;
  assert.deepEqual({series, rules}, original);
  assert.deepEqual(K.ruleOverlays(series, [{kind: 'fixed', series_id: 'kb-bound', value: '30'}]), []);
  assert.deepEqual(K.ruleOverlays(series, null), []);
});

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
  assert.equal(K.dateTicks(first, first + 3)[0].label, '01-01');
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
test('工作日序列的周末与节假日不断线，偶发的长缺口仍断线', () => {
  const days = [];
  for (let x = 0; x < 120; x++) if (x % 7 < 5 && ![30, 31, 75].includes(x)) days.push(point(x, x));
  assert.equal(K.medianInterval(days, true), 1);
  assert.deepEqual(K.splitObservations(days, true).map(run => run.length), [days.length]);
  const gapped = days.filter(p => p.x < 50 || p.x > 70);
  assert.deepEqual(K.splitObservations(gapped, true).map(run => run.length), [days.filter(p => p.x < 50).length, days.filter(p => p.x > 70).length]);
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

test('日期轴按跨度选年、季度、月份、周和日，刻度落在真实日历边界', () => {
  const years = K.dateTicks(day('2015-03-17'), day('2023-10-20'));
  assert.ok(years.length >= 3 && years.length <= 7);
  assert.ok(years.every(t => /^\d{4}$/.test(t.label) && new Date(t.x * 86400000).toISOString().slice(5, 10) === '01-01'));
  const quarters = K.dateTicks(day('2023-02-14'), day('2024-01-20'));
  assert.deepEqual(quarters.map(t => t.label), ['2023-04', '2023-07', '2023-10', '2024-01']);
  assert.ok(quarters.every(t => new Date(t.x * 86400000).getUTCDate() === 1));
  const months = K.dateTicks(day('2024-01-12'), day('2024-04-20'));
  assert.deepEqual(months.map(t => t.label), ['2024-02', '2024-03', '2024-04']);
  const weeks = K.dateTicks(day('2024-02-13'), day('2024-03-06'));
  assert.deepEqual(weeks.map(t => t.label), ['02-19', '02-26', '03-04']);
  assert.ok(weeks.every(t => new Date(t.x * 86400000).getUTCDay() === 1));
  assert.deepEqual(K.dateTicks(day('2024-02-28'), day('2024-03-01')).map(t => t.label), ['02-28', '02-29', '03-01']);
  assert.deepEqual(K.dateTicks(NaN, 10), []);
});

test('数值按量级加千分位或万亿，步长决定小数精度且保留零和负值', () => {
  assert.equal(K.formatNumber(200000), '20万');
  assert.equal(K.formatNumber(123456789), '1.235亿');
  assert.equal(K.formatNumber(-200000, 5000), '-20.0万');
  assert.equal(K.formatNumber(1234.5, .5), '1,234.5');
  assert.equal(K.formatNumber(.5, .5), '0.5');
  assert.equal(K.formatNumber(1, .5), '1.0');
  assert.equal(K.formatNumber(200500, 500), '20.05万');
  assert.equal(K.formatNumber(200000000, 10000000), '2.0亿');
  assert.equal(K.formatNumber(-0, 1), '0');
  assert.equal(K.formatNumber(null), '—');
  assert.equal(K.formatNumber(.000001, .000001), '0.000001');
});

test('年度长度的手工分段以第一段起点显示月份，其余手工分段显示第N天', () => {
  const segments = [{start: '2020-08-01', end: '2021-07-31'}, {start: '2021-09-01', end: '2022-08-31'}];
  assert.equal(K.annualSegments(segments), true);
  const ticks = K.cycleTicks({kind: 'segments', segments}, 0, 365, '2021-09-01');
  assert.deepEqual(ticks.map(t => t.label), ['8月', '9月', '10月', '11月', '12月', '1月', '2月', '3月', '4月', '5月', '6月', '7月']);
  assert.equal(ticks[1].x, 31);
  const short = [{start: '2020-08-01', end: '2021-01-31'}, segments[1]];
  assert.equal(K.annualSegments(short), false);
  assert.ok(K.cycleTicks({kind: 'segments', segments: short}, 0, 365).every(t => /^第 \d+ 天$/.test(t.label)));
  for (const length of [300, 400]) assert.equal(K.annualSegments([{start: '2020-01-01', end: new Date((day('2020-01-01') + length - 1) * 86400000).toISOString().slice(0, 10)}]), true);
});

test('周期默认当前与最近四个历史期，可配置1至40期且不解析图例名称', () => {
  const parts = Array.from({length: 10}, (_, index) => ({key: index, label: '<任意图例>' + index, start: `${2010 + index}-08-01`, current: index === 8})).reverse();
  assert.deepEqual([...K.visibleCycleKeys(parts)], [8, 7, 6, 5, 4]);
  assert.deepEqual([...K.visibleCycleKeys(parts, 1)], [8]);
  assert.deepEqual([...K.visibleCycleKeys(parts, 3)], [8, 7, 6]);
  assert.equal(K.visibleCycleKeys(parts, 40).size, 9, '当前周期之后的段不占默认期数');
  assert.deepEqual([...K.visibleCycleKeys(parts.map(p => ({...p, current: false})), 2)], [9, 8]);
  assert.deepEqual([...K.visibleCycleKeys(parts, 0)], [8, 7, 6, 5, 4]);
});

test('周期悬停降序，零值在缺失项之前且当前标识保持', () => {
  const rows = K.tooltipValues([{label: '缺失', points: [point(10, 9)]}, {label: '零值', points: [point(0, 0)]},
    {label: '当前', current: true, points: [point(0, 12)]}, {label: '最高', points: [point(0, 99)]}], 0, 1);
  assert.deepEqual(rows.map(row => row.label), ['最高', '当前', '零值', '缺失']);
  assert.equal(rows[1].current, true);
});
