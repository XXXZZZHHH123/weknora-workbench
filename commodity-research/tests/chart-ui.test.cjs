const assert = require('node:assert/strict'), {test} = require('node:test'), fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');
const {cycle, definition} = require('./chart-ui-fixture.cjs');
const window = {};
vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../src/tin/web/static/chart-ui.js'), 'utf8'), {window, URLSearchParams, Headers, setTimeout});
const U = window.ChartUI;
test('统计面板展示排名、近期均值、后续逐期明细与样本，动态文本转义', () => {
  const element = {}; U.stats(element, cycle());
  assert.equal(element.hidden, false);
  assert.match(element.innerHTML, /1\/4/); assert.match(element.innerHTML, /较近 3 期均值/);
  assert.match(element.innerHTML, /上涨 1 段\/共 2 段/); assert.match(element.innerHTML, /历史参照，不是预测；样本 3 期/);
  assert.match(element.innerHTML, /&lt;历史段&gt;/); assert.doesNotMatch(element.innerHTML, /<历史段>/);
});
test('变换后的指数不标原始单位，后续变化明确为原始观测', () => {
  const payload = cycle(); payload.template = {...payload.template, transform: 'rebase'};
  const element = {}; U.stats(element, payload);
  assert.match(element.innerHTML, /当前值 27（起点=100）/); assert.doesNotMatch(element.innerHTML, /当前值 27 吨/);
  assert.match(element.innerHTML, /数值口径：起点=100（无量纲）/);
  assert.match(element.innerHTML, /后续变化按原始观测计算；变化值单位：吨/);
});
test('统计不可用时明确原因且不画有效统计', () => {
  const payload = cycle(); payload.position = {...payload.position, n: 2, status: 'insufficient_cycles'};
  const element = {}; U.stats(element, payload);
  assert.match(element.innerHTML, /有效往期不足 3 期/); assert.doesNotMatch(element.innerHTML, /同期排名/);
});
test('多指标统计逐行展示同期排名及各自近期均值，计算序列标签安全转义', () => {
  const payload = {mode: 'cycle', curves: [{label: '指标甲', unit: '吨', cycle: cycle()},
    {label: '计算 <A/B>', unit: '倍', cycle: {...cycle(), position: {...cycle().position, recent_mean: 2.5}}}]};
  const element = {}; U.stats(element, payload);
  assert.equal(element.hidden, false); assert.match(element.innerHTML, /较近 5 期均值/);
  assert.match(element.innerHTML, /指标甲[\s\S]*1\/4[\s\S]*计算 &lt;A\/B&gt;[\s\S]*2.5/);
  assert.doesNotMatch(element.innerHTML, /<A\/B>|往期后续/);
});
test('输入与提示安全转义，自定义起点按真实月日验证', () => {
  assert.equal(U.esc('<img a="\'&">'), '&lt;img a=&quot;&#39;&amp;&quot;&gt;');
  assert.equal(U.validMonthDay('02-29'), true); assert.equal(U.validMonthDay('02-30'), false);
  assert.equal(U.validMonthDay('9-1'), false); assert.equal(U.validMonthDay('13-01'), false);
});
test('曲线提示使用指标名，不把来源ID作为可见提示', () => {
  assert.equal(U.message({code: 'unit_missing', series_id: 'kb-private-identity', label: '目录指标'}), '目录指标：曲线缺少单位，请核对口径');
  assert.equal(U.message({code: 'unit_missing', series_id: 'kb-private-identity'}), '曲线缺少单位，请核对口径');
  assert.equal(U.message('empty_segments_omitted'), '已省略没有观测的周期');
});
test('卡片提示为带title的小标记，详情提示为小字，并在无提示时清理', () => {
  const element = {attrs: {}, setAttribute(key, value) { this.attrs[key] = value; }};
  const warning = {code: 'unit_missing', label: '指标 <来源>', series_id: 'kb-private-identity'};
  U.noticeMarker(element, [warning, warning]);
  assert.equal(element.textContent, '!'); assert.equal(element.hidden, false);
  assert.equal(element.className, 'charts-warning-marker');
  assert.equal(element.title, '指标 <来源>：曲线缺少单位，请核对口径');
  assert.equal(element.attrs['aria-label'], element.title);
  assert.equal(element.attrs.role, 'img');
  U.noticeMarker(element, []); assert.equal(element.hidden, true); assert.equal(element.title, '');
  U.notices(element, [warning, warning, 'empty_segments_omitted'], 'subtle');
  assert.equal(element.className, 'charts-footnote');
  assert.equal(element.textContent, '指标 <来源>：曲线缺少单位，请核对口径；已省略没有观测的周期');
  U.notices(element, [], 'subtle'); assert.equal(element.hidden, true);
});
test('详情已在图下展示提示时，统计区域可省略重复提示且无黄色大块', () => {
  const payload = cycle(); payload.warnings = ['empty_segments_omitted'];
  const element = {}; U.stats(element, payload, {showWarnings: false});
  assert.doesNotMatch(element.innerHTML, /已省略没有观测的周期|charts-notice/);
  U.stats(element, payload); assert.match(element.innerHTML, /class="charts-footnote">已省略没有观测的周期/);
});
test('导入汇总按起点计张数，合并不同期数且保留原表分段计数', () => {
  const charts = [];
  for (const [start, count] of [['01-01', 30], ['09-01', 12], [null, 4]]) {
    for (let i = 0; i < count; i++) charts.push({convertible: true, mode: 'cycle', definition: {...definition,
      time: {...definition.time, cycles: i + 1, template: start ? {kind: 'repeat', start} : {kind: 'segments'}}}});
  }
  charts.push({convertible: true, mode: 'trend'});
  charts.push(...Array.from({length: 86}, () => ({convertible: false})));
  assert.equal(U.importSummary(charts), '本表 133 张：46 张周期图（起点 01-01 30 张、起点 09-01 12 张、原表分段 4 张），1 张走势图，86 张无法转换');
});
test('导入页先列可转换图，无法转换图放在默认折叠的details中且原因安全转义', () => {
  const unavailable = {chart_key: 'bad', title: '<img src=x>', convertible: false,
    reason: '原因 <script>', missing_curves: [{label: '<未知曲线>', reason: 'unconfirmed_sheet'}]};
  const available = {chart_key: 'good', title: '可转换图', convertible: true, mode: 'cycle', definition,
    warnings: [{code: 'unit_missing', label: '目录指标', series_id: 'kb-private-identity'}]};
  const original = structuredClone([unavailable, available]);
  const html = U.importMarkup([unavailable, available]);
  assert.deepEqual([unavailable, available], original);
  assert.ok(html.indexOf('data-import-chart="good"') < html.indexOf('<details'));
  assert.ok(html.indexOf('<details') < html.indexOf('data-import-chart="bad"'));
  assert.match(html, /<summary>1 张无法转换，展开查看原因<\/summary>/);
  assert.doesNotMatch(html, /<details[^>]*\bopen|<img|<script|kb-private-identity/);
  assert.match(html, /原因 &lt;script&gt;/); assert.match(html, /工作表尚未确认/);
  assert.doesNotMatch(U.importMarkup([available]), /<details/);
  assert.match(U.importSummary([]), /本表 0 张：0 张周期图，0 张走势图，0 张无法转换/);
});
