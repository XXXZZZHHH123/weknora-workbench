import { test } from 'node:test';
import assert from 'node:assert/strict';
import { excelSourceChartOption } from './excelSourceChart';
import type { ExcelPreviewChart } from './excelPreviewCharts';

const style = { color: '#222222', muted: '#666666', border: '#dddddd', font: 'serif' };
function fixture(): ExcelPreviewChart { return { id: 'part', title: 'Saved chart', notices: [],
  axes: [{ id: 'x', type: 'category' }, { id: 'right', type: 'value', position: 'right' }, { id: 'left', type: 'value', position: 'left' }],
  series: [{ name: 'Line', type: 'line', axisId: 'left', categoryAxisId: 'x', values: [1, null, 0, 4], categories: ['A', 'B', 'C', 'D'] },
    { name: 'Bars', type: 'bar', axisId: 'right', categoryAxisId: 'x', values: [100, 200], categories: ['A', 'B'] }] }; }
test('combo preserves real axis IDs even when axis declaration order differs', () => {
  const { option } = excelSourceChartOption(fixture(), style);
  const value = option as any;
  assert.deepEqual(value.series.map((series: any) => [series.type, series.yAxisIndex]), [['line', 1], ['bar', 0]]);
  assert.deepEqual(value.yAxis.map((axis: any) => axis.position), ['right', 'left']);
  assert.deepEqual(value.series[0].data.map((point: any) => point.value), [1, null, 0, 4]);
  assert.equal(value.series[0].connectNulls, false);
});
test('source titles and legends remain plain text; tooltips never request HTML or URL content', () => {
  const chart = fixture(); chart.title = '<img src=x onerror=alert(1)>'; chart.series[0]!.name = '{evil|<script>}';
  const value = excelSourceChartOption(chart, style).option as any;
  assert.equal(value.title.text, chart.title); assert.equal(value.tooltip.renderMode, 'richText');
  assert.equal(value.tooltip.formatter([{seriesName: chart.series[0]!.name, data: {rawValue: 0, name: '<img>'}}]), '<img> evil|<script>：0');
  assert.equal('toolbox' in value, false); assert.equal('graphic' in value, false);
});
test('unknown axis relationships are explicit and never silently mapped to the first axis', () => {
  const chart = fixture(); chart.series.forEach(series => { series.axisId = 'not-real'; });
  const result = excelSourceChartOption(chart, style);
  assert.equal(result.option, null); assert(result.notices.some(notice => notice.includes('坐标轴关系缺失')));
});
test('horizontal bars swap coordinate dimensions and preserve values', () => {
  const chart = fixture(); chart.series = [{ ...chart.series[1]!, horizontal: true }];
  const value = excelSourceChartOption(chart, style).option as any;
  assert.equal(value.xAxis[0].type, 'value'); assert.equal(value.yAxis[0].type, 'category');
  assert.deepEqual(value.series[0].data.map((point: any) => point.value), [100, 200]);
});
test('independent stacked groups do not merge and area remains an area line', () => {
  const chart = fixture(); chart.series[0] = {...chart.series[0]!, type: 'area', grouping: 'stacked', groupId: 'g1'};
  chart.series[1] = {...chart.series[1]!, grouping: 'stacked', groupId: 'g2'};
  const value = excelSourceChartOption(chart, style).option as any;
  assert.notEqual(value.series[0].stack, value.series[1].stack); assert(value.series[0].areaStyle);
});
test('percentage stacking normalizes only drawing coordinates and keeps raw tooltip values', () => {
  const chart = fixture(); chart.series = [
    {...chart.series[0]!, grouping: 'percentStacked', groupId: 'p', values: [2, null]},
    {...chart.series[0]!, name: 'Other', grouping: 'percentStacked', groupId: 'p', values: [6, 8]},
  ];
  const value = excelSourceChartOption(chart, style).option as any;
  assert.equal(value.series[0].data[0].value, .25); assert.equal(value.series[0].data[0].rawValue, 2);
  assert.equal(value.series[0].data[1].value, null); assert.equal(value.series[1].data[1].value, 1);
});
test('mixed percentage and ordinary units on one value axis are never drawn together', () => {
  const chart = fixture(); chart.series[0]!.grouping = 'percentStacked'; chart.series[1]!.axisId = 'left';
  const result = excelSourceChartOption(chart, style); assert.equal(result.option, null);
  assert(result.notices.some(notice => notice.includes('百分比堆积和普通数值')));
});
test('date axes preserve time spacing and missing category labels stay empty', () => {
  const chart = fixture(); chart.axes[0]!.type = 'date'; chart.axes[0]!.inverse = true;
  chart.series = [{...chart.series[0]!, categoryTimes: [0, 86400000, 864000000, 950400000]}];
  const value = excelSourceChartOption(chart, style).option as any;
  assert.equal(value.xAxis[0].type, 'time'); assert.equal(value.xAxis[0].inverse, true);
  assert.equal(value.series[0].data[2].value[0], 864000000);
  chart.axes[0]!.type = 'category'; chart.series[0]!.categories[1] = null;
  const category = excelSourceChartOption(chart, style).option as any; assert.equal(category.xAxis[0].data[1], '');
});
test('date axes with entirely missing cached timestamps are explicit and not rendered', () => {
  const chart = fixture(); chart.axes[0]!.type = 'date'; chart.series = [{...chart.series[0]!, categoryTimes: [null, null]}];
  const result = excelSourceChartOption(chart, style); assert.equal(result.option, null);
  assert(result.notices.some(notice => notice.includes('缺少可靠时间缓存')));
});
test('bar and area axes include the zero baseline while preserving explicit original bounds', () => {
  const chart = fixture(); chart.series[0]!.type = 'area';
  chart.axes[1]!.min = 20; chart.axes[1]!.max = 240;
  const value = excelSourceChartOption(chart, style).option as any;
  assert.equal(value.yAxis[0].scale, false); assert.equal(value.yAxis[1].scale, false);
  assert.equal(value.yAxis[0].min, 20); assert.equal(value.yAxis[0].max, 240);
  const line = excelSourceChartOption(fixture(), style).option as any; assert.equal(line.yAxis[1].scale, true);
});
