/* DOM construction smoke checks, usable even when Chromium sockets are denied.
 * The browser suites remain responsible for actual layout, export and events. */
const assert = require('node:assert/strict'), {test} = require('node:test');
const K = require('../src/tin/web/static/chart-kit.js');
const palette = require('../src/tin/web/static/chart-palette.js');
const {cycle, definition} = require('./chart-ui-fixture.cjs');
class Element {
  constructor(name, doc) { this.name = name; this.ownerDocument = doc; this.attrs = {}; this.children = []; this.style = {}; this.events = {}; this._text = ''; }
  set textContent(value) { this._text = String(value); this.children = []; }
  get textContent() { return this._text + this.children.map(c => c.textContent).join(''); }
  setAttribute(key, value) { this.attrs[key] = value; }
  addEventListener(key, callback) { this.events[key] = callback; }
  append(...nodes) { nodes.forEach(node => { node.parent = this; this.children.push(node); }); }
  prepend(node) { node.parent = this; this.children.unshift(node); }
  replaceChildren(...nodes) { this.children = []; this._text = ''; this.append(...nodes); }
  replaceWith(node) { const index = this.parent.children.indexOf(this); this.parent.children[index] = node; node.parent = this.parent; }
  remove() { this.parent.children = this.parent.children.filter(node => node !== this); }
  getBoundingClientRect() { return {left: 0, top: 0, width: Number(this.attrs.width), height: Number(this.attrs.height)}; }
}
function fixture() {
  const doc = {createElement(name) { return new Element(name, doc); }, createElementNS(ns, name) { return new Element(name, doc); }};
  doc.documentElement = doc.createElement('html');
  globalThis.document = doc;
  return doc.createElement('div');
}
function nodes(element) { return [element, ...element.children.flatMap(nodes)]; }
function attr(element, key, value) { return nodes(element).filter(node => Object.hasOwn(node.attrs, key) && (value === undefined || node.attrs[key] === value)); }
function pathXs(node) { return [...node.attrs.d.matchAll(/[ML](-?[\d.]+),/g)].map(match => Number(match[1])); }
function assertClose(actual, expected) { assert.ok(Math.abs(actual - expected) < 1e-9, `${actual} ≈ ${expected}`); }

test('导出按实际图位布局：深色文字、完整标题、白底、浅网格和区间带，细线提高下限', () => {
  for (const [width, height] of [[320, 180], [400, 180], [960, 400], [1600, 600]]) {
    const container = fixture(), payload = cycle(), def = structuredClone(definition), style = K.exportStyle(width);
    def.title = '导出标题 <img onerror=bad()>';
    def.time.segment_styles = payload.segments.map(part => ({label: part.label, width: .5}));
    const original = structuredClone(payload), handle = K.render(container, payload, {width, height, exactSize: true, export: true, definition: def});
    const all = nodes(container), svg = all.find(node => node.name === 'svg'), texts = all.filter(node => node.name === 'text');
    assert.equal(svg.attrs.viewBox, `0 0 ${width} ${height}`);
    assert.equal(svg.attrs['aria-label'], def.title);
    assert.equal(all.filter(node => node.attrs.transform).length, 0, '字号和线宽不再被场景缩小');
    assert.equal(attr(container, 'data-chart-title').map(node => node.textContent).join(''), def.title);
    assert.ok(attr(container, 'data-chart-title').every(node => Number(node.attrs['font-size']) >= 13));
    assert.ok(texts.every(node => Number(node.attrs['font-size']) >= 11 && node.attrs.fill === style.text));
    assert.ok(texts.every(node => Number(node.attrs.y) > 0 && Number(node.attrs.y) < height));
    assert.ok(all.some(node => node.name === 'rect' && node.attrs.fill === '#ffffff' && Number(node.attrs.width) === width && Number(node.attrs.height) === height));
    assert.ok(all.some(node => node.name === 'line' && node.attrs.stroke === style.grid));
    assert.ok(attr(container, 'data-band', 'range').every(node => node.attrs.fill === style.band && Number(node.attrs['fill-opacity']) <= .13));
    for (const [index, part] of payload.segments.entries()) {
      const group = attr(container, 'data-series', String(index))[0], curve = attr(group, 'data-curve-run')[0];
      const line = attr(container, 'data-legend', String(index))[0].children.find(node => node.name === 'line');
      assert.equal(Number(curve.attrs['stroke-width']), part.current ? style.currentLineWidth : style.lineWidth);
      assert.equal(line.attrs['stroke-width'], curve.attrs['stroke-width']); assert.equal(group.attrs.opacity, '1');
    }
    assert.equal(all.filter(node => node.name === 'img').length, 0);
    assert.deepEqual(payload, original); handle.destroy();
  }
});
test('导出图例根据文字宽度换行，横轴日期避开重叠，过小图位明确失败而不缩小文字', () => {
  const container = fixture(), payload = {curves: Array.from({length: 4}, (_, i) => ({label: '合成指标图例 ' + i,
    points: [{date: '2030-01-01', value: i}, {date: '2030-02-01', value: i + 2}]}))};
  const handle = K.render(container, payload, {width: 320, height: 220, exactSize: true, export: true, definition: {title: '合成图表'}});
  const legends = attr(container, 'data-legend').map(node => node.children.find(child => child.name === 'text'));
  assert.ok(new Set(legends.map(node => node.attrs.y)).size > 1);
  assert.ok(legends.every(node => Number(node.attrs.x) >= 24 && Number(node.attrs.x) < 320));
  const dates = attr(container, 'data-date-tick');
  assert.ok(dates.length >= 2 && dates.length <= K.dateTicks(Date.parse('2030-01-01') / 86400000, Date.parse('2030-02-01') / 86400000).length);
  for (let i = 1; i < dates.length; i++) assert.ok(Number(dates[i].attrs.x) - Number(dates[i - 1].attrs.x) >= 5 * .62 * 11 + 6);
  handle.destroy();
  assert.throws(() => K.render(container, payload, {width: 320, height: 60, export: true}), /图位过小/);
});

test('月度周期曲线和band居中、标签在月中而刻度在月界，tooltip保留真实月末日期', () => {
  const payload = {template: {kind: 'repeat', start: '09-01', period: 'quarter'}, freq_days: 28, tolerance_days: 1,
    segments: [{label: '2023', start: '2023-09-01', end: '2023-11-30', current: true,
      points: [{x: 29, date: '2023-09-30', value: 10, raw: 100}, {x: 60, date: '2023-10-31', value: 20, raw: 200},
        {x: 90, date: '2023-11-30', value: 30, raw: 300}]}],
    band: [{x: 0, min: 1, max: 3, mean: 2, n: 3}, {x: 28, min: 5, max: 9, mean: 7, n: 6},
      {x: 56, min: 2, max: 4, mean: 3, n: 4}, {x: 84, min: 3, max: 5, mean: 4, n: 3}]};
  const original = structuredClone(payload);
  for (const thumbnail of [false, true]) {
    const container = fixture(), handle = K.render(container, payload, {thumbnail, definition: {axes: {left: {min: 0, max: 40}}}});
    const x = day => 72 + 816 * day / 90;
    const expectedXs = [15, 45.5, 76].map(day => Number(x(day).toFixed(2)));
    assert.deepEqual(pathXs(attr(container, 'data-curve-run')[0]), expectedXs);
    assert.deepEqual(pathXs(attr(container, 'data-band', 'mean')[0]), expectedXs, 'band同月去重且与观测居中对齐');
    const meanY = Number(attr(container, 'data-band', 'mean')[0].attrs.d.match(/^M[\d.]+,([\d.]+)/)[1]);
    const bottom = thumbnail ? 156 : 362;
    assert.equal(meanY, Number((bottom - 7 / 40 * (bottom - 28)).toFixed(2)), 'band选n最大点的均值');
    const labels = nodes(container).filter(node => node.name === 'text' && /^\d+月$/.test(node.textContent));
    assert.deepEqual(labels.map(node => node.textContent), ['9月', '10月', '11月']);
    labels.forEach((label, index) => assertClose(Number(label.attrs.x), x([15, 45.5, 76][index])));
    const tickLines = nodes(container).filter(node => node.name === 'line' && Number(node.attrs.y1) === bottom && Number(node.attrs.y2) === bottom + 4);
    assert.equal(tickLines.length, 3);
    tickLines.forEach((line, index) => assertClose(Number(line.attrs.x1), x([0, 30, 61][index])));
    if (!thumbnail) {
      const overlay = attr(container, 'data-chart-hover')[0];
      const tip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
      for (const [index, center] of [15, 45.5, 76].entries()) {
        overlay.events.pointermove({clientX: x(center), clientY: 100});
        assert.equal(tip.hidden, false);
        assert.ok(tip.textContent.includes(payload.segments[0].points[index].date));
        assert.doesNotMatch(tip.textContent, /此位置缺失/);
      }
    }
    assert.deepEqual(payload, original, '渲染不改变计算结果、原坐标或真实日期');
    handle.destroy();
  }
});
test('周度及低于28天的周期保持原坐标、band和月界标签，事件模板也不居中', () => {
  for (const freq of [7, 27, 27.9]) {
    const payload = cycle(); payload.freq_days = freq;
    const container = fixture(), handle = K.render(container, payload);
    const x = day => Number((72 + 816 * day / 365).toFixed(2));
    assert.deepEqual(pathXs(attr(attr(container, 'data-series', '3')[0], 'data-curve-run')[0]), [0, 7, 14].map(x));
    assert.deepEqual(attr(container, 'data-band', 'mean').flatMap(pathXs), [0, 14, 21, 350].map(x));
    const labels = nodes(container).filter(node => node.name === 'text' && /^\d+月$/.test(node.textContent));
    assert.deepEqual(labels.slice(0, 2).map(node => Number(node.attrs.x)), [72, 72 + 816 * 30 / 365]);
    handle.destroy();
  }
  const payload = {template: {kind: 'event', before_days: 28, after_days: 28}, freq_days: 30,
    segments: [{label: '事件', start: '2023-09-01', end: '2023-09-29', current: true,
      points: [{x: 0, date: '2023-09-01', value: 1}, {x: 28, date: '2023-09-29', value: 2}]}],
    band: [{x: 0, min: 0, max: 2, mean: 1, n: 3}, {x: 28, min: 1, max: 3, mean: 2, n: 4}]};
  const container = fixture(), handle = K.render(container, payload);
  assert.deepEqual(pathXs(attr(container, 'data-curve-run')[0]), [480, 888]);
  assert.deepEqual(pathXs(attr(container, 'data-band', 'mean')[0]), [480, 888]);
  assert.ok(nodes(container).some(node => node.name === 'text' && node.textContent === '事件日' && node.attrs.x === '480'));
  handle.destroy();
});

test('真实renderer构建完整周期band、断线和可切换图例，tooltip不跳至远点', () => {
  const container = fixture(), handle = K.render(container, cycle(), {definition});
  const band = attr(container, 'data-band', 'range');
  const current = attr(container, 'data-series', '3')[0];
  assert.ok(Math.max(...band.flatMap(pathXs)) > Math.max(...attr(current, 'data-curve-run').flatMap(pathXs)), '带覆盖到当前段之后');
  assert.equal(attr(attr(container, 'data-series', '0')[0], 'data-curve-run').length, 2, '更早曲线不跨缺口');
  assert.equal(attr(current, 'data-curve-run')[0].attrs['stroke-width'], '3');
  assert.equal(attr(attr(container, 'data-series', '2')[0], 'data-curve-run')[0].attrs['stroke-dasharray'], '7 4');
  const overlay = attr(container, 'data-chart-hover')[0];
  overlay.events.pointermove({clientX: 72 + 816 * 200 / 365, clientY: 100});
  const tooltip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
  assert.match(tooltip.textContent, /第 200 天/); assert.equal(tooltip.children.filter(node => node.textContent.includes('此位置缺失')).length, 4);
  attr(container, 'data-legend', '3')[0].events.click(); assert.equal(attr(container, 'data-series', '3').length, 0);
  attr(container, 'data-legend', '3')[0].events.keydown({key: 'Enter', preventDefault() {}}); assert.equal(attr(container, 'data-series', '3').length, 1);
  assert.equal(typeof handle.toSVG, 'function'); assert.equal(typeof handle.toPNGBlob, 'function'); handle.destroy(); assert.equal(container.children.length, 0);
});
test('真实renderer支持16条线柱面积、左右轴和样式，动态标签保持文本', () => {
  const curves = Array.from({length: 16}, (_, index) => ({series_id: 'fixture-' + index, label: '<svg onload=bad()>曲线' + index,
    kind: ['line', 'bar', 'area'][index % 3], axis: index % 2 ? 'right' : 'left', unit: index % 2 ? '元' : '吨', style: {color: '#123456'},
    points: [0, 1, 2, 9, 10].map(day => ({date: `2030-01-${String(day + 1).padStart(2, '0')}`, value: day + index}))}));
  const container = fixture(), handle = K.render(container, {curves});
  assert.equal(attr(container, 'data-series').length, 16);
  assert.ok(nodes(container).some(node => node.name === 'text' && node.textContent === '元'));
  assert.ok(nodes(container).some(node => node.name === 'text' && node.textContent === '吨'));
  assert.ok(attr(container, 'data-series', '1')[0].children.every(node => node.name === 'rect'));
  assert.ok(attr(container, 'data-series', '2')[0].children.some(node => node.attrs['fill-opacity'] === '0.16'));
  assert.equal(nodes(container).filter(node => node.name === 'svg').length, 1, '标签不会作为SVG元素插入');
  handle.destroy();
});
test('空数据给出明确状态且不画零值；真实的0观测仍绘制', () => {
  const container = fixture(), handle = K.render(container, {curves: [{label: '缺失', points: [{date: '2030-01-01', value: null}]}]});
  assert.match(container.textContent, /暂无有效观测/); assert.equal(attr(container, 'data-curve-run').length, 0); handle.destroy();
  const zero = K.render(container, {curves: [{label: '零观测', points: [{date: '2030-01-01', value: 0}]}]});
  assert.equal(attr(container, 'data-curve-run').length, 1); assert.doesNotMatch(container.textContent, /暂无有效观测/); zero.destroy();
});
test('周期默认配色区分各年，仅当前段沿用指标颜色；图例与线条样式一致', () => {
  const container = fixture(), handle = K.render(container, cycle(), {definition});
  const colors = [];
  for (let index = 0; index < 4; index++) {
    const key = String(index), group = attr(container, 'data-series', key)[0];
    const curve = attr(group, 'data-curve-run')[0];
    const legend = attr(container, 'data-legend', key)[0].children.find(c => c.name === 'line');
    colors.push(curve.attrs.stroke);
    assert.equal(curve.attrs.stroke, index === 3 ? '#123456' : palette.color(index));
    for (const name of ['stroke', 'stroke-width', 'stroke-dasharray']) assert.equal(legend.attrs[name], curve.attrs[name]);
    assert.equal(legend.attrs.opacity, group.attrs.opacity);
  }
  assert.equal(new Set(colors).size, 4);
  assert.equal(attr(container, 'data-series', '0')[0].attrs.opacity, '0.5');
  assert.equal(attr(container, 'data-series', '2')[0].attrs.opacity, '0.85');
  handle.destroy();
  const def = structuredClone(definition); def.curves[0].style.color = null;
  const accent = K.render(container, cycle(), {definition: def});
  assert.equal(attr(attr(container, 'data-series', '3')[0], 'data-curve-run')[0].attrs.stroke, 'var(--primary)'); accent.destroy();
});
test('原表段样式严格按图例标签匹配，保留颜色、线宽和虚线，包括上一段的实线', () => {
  const payload = cycle(), def = structuredClone(definition);
  const styles = payload.segments.map((s, i) => ({label: s.label, color: ['#AABBCC', '#112233', '#445566', '#778899'][i],
    width: [1.25, 2.5, 4, 2.25][i], dash: ['dot', 'dash', 'solid', 'dot'][i]}));
  def.time.segment_styles = styles.slice().reverse();
  const container = fixture(), handle = K.render(container, payload, {definition: def});
  styles.forEach((style, i) => {
    const group = attr(container, 'data-series', String(i))[0], curve = attr(group, 'data-curve-run')[0];
    const legend = attr(container, 'data-legend', String(i))[0].children.find(c => c.name === 'line');
    assert.equal(curve.attrs.stroke, style.color); assert.equal(curve.attrs['stroke-width'], String(style.width));
    assert.equal(curve.attrs['stroke-dasharray'], style.dash === 'solid' ? undefined : style.dash === 'dot' ? '2 4' : '7 4');
    assert.equal(group.attrs.opacity, '1');
    for (const name of ['stroke', 'stroke-width', 'stroke-dasharray']) assert.equal(legend.attrs[name], curve.attrs[name]);
  });
  handle.destroy();
});
test('不同格式的段标签不猜测映射，也不按样式顺序匹配；空颜色使用默认配色', () => {
  const payload = cycle(), def = structuredClone(definition);
  def.time.segment_styles = [{label: '2029 ', color: '#FF0000', width: 9, dash: 'dot'},
    {label: '28/29', color: '#00FF00', width: 9, dash: 'solid'},
    {label: payload.segments[1].label, color: null, width: null, dash: null}];
  const container = fixture(), handle = K.render(container, payload, {definition: def});
  assert.equal(attr(attr(container, 'data-series', '3')[0], 'data-curve-run')[0].attrs.stroke, '#123456');
  const previous = attr(attr(container, 'data-series', '2')[0], 'data-curve-run')[0];
  assert.equal(previous.attrs.stroke, palette.color(2)); assert.equal(previous.attrs['stroke-dasharray'], '7 4');
  assert.equal(attr(attr(container, 'data-series', '1')[0], 'data-curve-run')[0].attrs.stroke, palette.color(1));
  handle.destroy();
});
test('空段不画线、不占图例或横轴，也不改变上一条有效段的虚线', () => {
  const payload = cycle();
  payload.segments.push({label: '空未来', start: '2030-09-01', end: '2039-08-31', points: []},
    {label: '无效段', start: '2031-09-01', end: '2039-08-31', points: [{x: 0, value: null}]});
  payload.segments.splice(3, 0, {label: '空往期', start: '2028-10-01', end: '2029-09-30', points: []});
  const original = structuredClone(payload), container = fixture(), handle = K.render(container, payload, {definition});
  assert.deepEqual(payload, original);
  assert.equal(attr(container, 'data-series').length, 4); assert.equal(attr(container, 'data-legend').length, 4);
  assert.doesNotMatch(container.textContent, /空未来|无效段|空往期/);
  assert.equal(attr(attr(container, 'data-series', '2')[0], 'data-curve-run')[0].attrs['stroke-dasharray'], '7 4');
  attr(container, 'data-chart-hover')[0].events.pointermove({clientX: 72 + 816 * 7 / 365, clientY: 100});
  const tip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
  assert.match(tip.textContent, /2029-09-08/); handle.destroy();
});
test('renderer实际纵轴遵循刻度数量和精度，双轴网格数一致', () => {
  for (const thumbnail of [false, true]) {
    const container = fixture(), handle = K.render(container, {curves: [
      {label: '整数', axis: 'left', points: [{date: '2030-01-01', value: 20}, {date: '2030-01-02', value: 80}]},
      {label: '小数', axis: 'right', points: [{date: '2030-01-01', value: .5}, {date: '2030-01-02', value: 2}]}]}, {thumbnail});
    for (const axis of ['left', 'right']) {
      const ticks = attr(container, 'data-axis-tick', axis).map(n => n.textContent);
      assert.ok(thumbnail ? ticks.length <= 4 : ticks.length >= 5 && ticks.length <= 7);
      assert.deepEqual(ticks, axis === 'left'
        ? thumbnail ? ['0', '50', '100', '150'] : ['0', '20', '40', '60', '80', '100']
        : thumbnail ? ['0', '1', '2', '3'] : ['0.0', '0.5', '1.0', '1.5', '2.0', '2.5']);
    }
    handle.destroy();
  }
});

test('默认只绘制五期，全部图例可点开，全部年份开关保留区间带和悬停所有期', () => {
  const payload = cycle(), seed = payload.segments[0];
  payload.segments = Array.from({length: 10}, (_, index) => ({...structuredClone(seed), label: '期' + index,
    start: `${2010 + index}-09-01`, end: `${2011 + index}-08-31`, current: index === 9,
    points: seed.points.map(p => ({...p, value: index * 10 + p.x, date: new Date(Date.UTC(2010 + index, 8, 1 + p.x)).toISOString().slice(0, 10)}))}));
  const original = structuredClone(payload), container = fixture(), handle = K.render(container, payload, {definition});
  assert.deepEqual(attr(container, 'data-series').map(node => node.attrs['data-series']), ['5', '6', '7', '8', '9']);
  assert.equal(attr(container, 'data-legend').length, 10);
  const band = attr(container, 'data-band', 'range').map(node => node.attrs.d);
  attr(container, 'data-chart-hover')[0].events.pointermove({clientX: 72, clientY: 100});
  const tip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
  assert.equal(tip.children.length, 11);
  assert.match(tip.children[1].textContent, /^期9（当前）/);
  assert.equal(tip.children[1].style.fontWeight, '700');
  assert.match(tip.children[10].textContent, /^期0/);
  attr(container, 'data-show-all-cycles')[0].events.click();
  assert.equal(attr(container, 'data-series').length, 10);
  assert.equal(attr(container, 'data-show-all-cycles')[0].attrs['aria-pressed'], 'true');
  attr(container, 'data-show-all-cycles')[0].events.click();
  assert.equal(attr(container, 'data-series').length, 5);
  attr(container, 'data-legend', '0')[0].events.click();
  assert.equal(attr(container, 'data-series').length, 6);
  attr(container, 'data-show-all-cycles')[0].events.click();
  for (let i = 0; i < 10; i++) attr(container, 'data-legend', String(i))[0].events.click();
  assert.equal(attr(container, 'data-series').length, 0);
  assert.deepEqual(attr(container, 'data-band', 'range').map(node => node.attrs.d), band);
  assert.deepEqual(payload, original);
  handle.destroy();
  const configured = K.render(container, payload, {definition: {...definition, time: {...definition.time, visible_cycles: 2}}});
  assert.deepEqual(attr(container, 'data-series').map(node => node.attrs['data-series']), ['8', '9']);
  configured.destroy();
  const exported = K.render(container, payload, {width: 640, height: 300, export: true, definition});
  assert.equal(attr(container, 'data-series').length, 5);
  assert.equal(attr(container, 'data-legend').length, 5);
  assert.equal(attr(container, 'data-show-all-cycles').length, 0);
  exported.destroy();
});

test('年度手工分段渲染月份轴使用第一段，短分段仍显示第N天', () => {
  const payload = {template: {kind: 'segments'}, freq_days: 7,
    segments: [{label: '首段', start: '2020-08-01', end: '2021-07-31', points: [{x: 0, date: '2020-08-01', value: 1}]},
      {label: '当前', start: '2022-09-01', end: '2023-08-31', current: true, points: [{x: 0, date: '2022-09-01', value: 2}]}], band: []};
  const container = fixture(), handle = K.render(container, payload);
  assert.equal(attr(container, 'data-date-tick')[0].textContent, '8月');
  assert.equal(attr(container, 'data-date-tick').at(-1).textContent, '7月');
  handle.destroy(); payload.segments[0].end = '2020-12-31';
  const short = K.render(container, payload);
  assert.ok(attr(container, 'data-date-tick').every(node => /^第 \d+ 天$/.test(node.textContent)));
  short.destroy();
});

test('周期标注当前最新有效值，走势标注各曲线最新值，拥挤时保留左轴主曲线', () => {
  const container = fixture(), payload = cycle(), handle = K.render(container, payload, {definition});
  assert.equal(attr(container, 'data-latest-value').length, 1);
  assert.equal(attr(container, 'data-latest-value')[0].attrs['data-latest-value'], '3');
  assert.equal(attr(container, 'data-latest-value')[0].textContent, '27');
  handle.destroy();
  const curves = [
    {label: '左', axis: 'left', points: [{date: '2030-01-03', value: 20}, {date: '2030-01-01', value: 10}, {date: '2030-01-04', value: null}]},
    {label: '右', axis: 'right', points: [{date: '2030-01-01', value: 100}, {date: '2030-01-03', value: 300}]}];
  const trend = K.render(container, {curves}, {definition: {axes: {left: {min: 0, max: 100}, right: {min: 0, max: 1000}}}});
  assert.deepEqual(attr(container, 'data-latest-value').map(node => node.textContent), ['300', '20']);
  assert.ok(attr(container, 'data-latest-value').every(node => Number(node.attrs.x) >= 72 && Number(node.attrs.x) <= 888));
  trend.destroy();
  const crowded = K.render(container, {curves: Array.from({length: 30}, (_, index) => ({label: '曲线' + index, axis: index % 2 ? 'right' : 'left',
    points: [{date: '2030-01-01', value: 1}, {date: '2030-01-02', value: 2}]}))});
  assert.equal(attr(container, 'data-latest-value').length, 1);
  assert.equal(attr(container, 'data-latest-value')[0].attrs['data-latest-value'], '0');
  crowded.destroy();
});

test('左右轴共享网格位置，右轴刻度和单位沿用右曲线颜色，固定范围保持', () => {
  for (const fixed of [false, true]) {
    const container = fixture(), curves = [
      {label: '量', axis: 'left', unit: '手', style: {color: '#112233'}, points: [{date: '2030-01-01', value: 200000}, {date: '2030-01-02', value: 500000}]},
      {label: '比率', axis: 'right', unit: '%', style: {color: '#CC5500'}, points: [{date: '2030-01-01', value: .3}, {date: '2030-01-02', value: 2.1}]}];
    const original = structuredClone(curves), definition = fixed ? {axes: {left: {min: 123456, max: 567890}, right: {min: .1, max: 2.7}}} : {};
    const handle = K.render(container, {curves}, {definition});
    const left = attr(container, 'data-axis-tick', 'left'), right = attr(container, 'data-axis-tick', 'right');
    assert.equal(left.length, right.length);
    left.forEach((node, index) => assertClose(Number(node.attrs.y), Number(right[index].attrs.y)));
    assert.ok(right.every(node => node.attrs.fill === '#CC5500'));
    assert.equal(attr(container, 'data-axis-unit', 'right')[0].attrs.fill, '#CC5500');
    assert.equal(attr(container, 'data-axis-unit', 'left')[0].textContent, '手');
    assert.ok(left.some(node => /万$/.test(node.textContent)));
    if (fixed) assert.ok(left.every(node => Number(node.attrs.y) >= 28 && Number(node.attrs.y) <= 366));
    assert.deepEqual(curves, original);
    handle.destroy();
  }
});

test('四十个历史期在320×180的PPT图位只占默认五期图例', () => {
  const seed = cycle(), payload = {...seed, segments: Array.from({length: 40}, (_, index) => ({
    label: String(1980 + index), start: `${1980 + index}-09-01`, end: `${1981 + index}-08-31`, current: index === 39,
    points: [0, 7, 14].map(x => ({x, date: new Date(Date.UTC(1980 + index, 8, 1 + x)).toISOString().slice(0, 10), value: index + x}))}))};
  const container = fixture(), handle = K.render(container, payload, {width: 320, height: 180, exactSize: true, export: true,
    definition: {...definition, title: '合成图'}});
  assert.equal(attr(container, 'data-series').length, 5);
  assert.equal(attr(container, 'data-legend').length, 5);
  assert.equal(nodes(container).find(node => node.name === 'svg').attrs.viewBox, '0 0 320 180');
  assert.equal(attr(container, 'data-latest-value').length, 1);
  handle.destroy();
});

test('同月多条观测居中到相同横坐标时，末端标注仍取真实日期最新值', () => {
  const payload = {template: {kind: 'repeat', start: '01-01', period: 'year'}, freq_days: 28,
    segments: [{label: '当前', start: '2030-01-01', end: '2030-12-31', current: true,
      points: [{x: 0, date: '2030-01-01', value: 10}, {x: 28, date: '2030-01-29', value: 20}]}], band: []};
  for (const reverse of [false, true]) {
    const container = fixture(), copy = structuredClone(payload);
    if (reverse) copy.segments[0].points.reverse();
    const original = structuredClone(copy), handle = K.render(container, copy);
    const xs = pathXs(attr(container, 'data-curve-run')[0]);
    assert.equal(xs[0], xs[1], '月内两条真实观测使用同一月份中心');
    assert.equal(attr(container, 'data-latest-value')[0].textContent, '20');
    assert.deepEqual(copy, original);
    handle.destroy();
  }
});

function multiCycleFixture() {
  const first = cycle(), second = cycle();
  second.segments.forEach(part => part.points.forEach(point => { point.value /= 10; }));
  return {mode: 'cycle', template: first.template, band: first.band, curves: [
    {series_id: 'fixture-a', label: '研究员选择的量', axis: 'left', unit: '吨', curve_cycles: 'current_and_previous',
      style: {color: '#123456', dash: 'solid'}, cycle: first},
    {series_id: null, derived: {op: 'div', a: 'fixture-a', b: 'fixture-b'}, label: '研究员比值 <安全>',
      axis: 'right', unit: '倍', style: {color: '#CC5500'}, cycle: second}]};
}

test('多指标周期只显示各自当前期与所选上一期，同色虚线、双轴且始终无区间带', () => {
  const payload = multiCycleFixture(), original = structuredClone(payload), container = fixture();
  const handle = K.render(container, payload, {definition: {time: {mode: 'cycle', curve_cycles: 'current'}}});
  assert.equal(attr(container, 'data-series').length, 3);
  assert.equal(attr(container, 'data-band').length, 0, '即使响应包含区间带也不画多指标区间带');
  assert.equal(attr(container, 'data-show-all-cycles').length, 0);
  const current = attr(attr(container, 'data-series', '0')[0], 'data-curve-run')[0];
  const previous = attr(attr(container, 'data-series', '1')[0], 'data-curve-run')[0];
  assert.equal(current.attrs.stroke, '#123456'); assert.equal(previous.attrs.stroke, current.attrs.stroke);
  assert.equal(current.attrs['stroke-dasharray'], undefined);
  assert.equal(previous.attrs['stroke-dasharray'], '7 4', '上一期覆盖曲线的实线配置');
  const previousLegend = attr(container, 'data-legend', '1')[0].children.find(node => node.name === 'line');
  assert.equal(previousLegend.attrs['stroke-dasharray'], previous.attrs['stroke-dasharray']);
  assert.equal(attr(container, 'data-axis-unit', 'left')[0].textContent, '吨');
  assert.equal(attr(container, 'data-axis-unit', 'right')[0].textContent, '倍');
  const left = attr(container, 'data-axis-tick', 'left'), right = attr(container, 'data-axis-tick', 'right');
  assert.equal(left.length, right.length);
  left.forEach((node, i) => assertClose(Number(node.attrs.y), Number(right[i].attrs.y)));
  assert.ok(right.every(node => node.attrs.fill === '#CC5500'));
  assert.match(attr(container, 'data-legend', '2')[0].textContent, /研究员比值 <安全>/);
  assert.equal(nodes(container).filter(node => node.name === 'svg').length, 1);
  assert.deepEqual(payload, original);
  handle.destroy();
});

test('多指标周期沿用每条曲线的频率与匹配容差，tooltip保留日期及各自单位', () => {
  const payload = multiCycleFixture(), container = fixture();
  payload.curves[0].curve_cycles = 'current';
  payload.curves[1].cycle.freq_days = 1; payload.curves[1].cycle.tolerance_days = 1;
  payload.curves[1].cycle.segments.at(-1).points = [{x: 10, date: '2029-09-11', value: 2}];
  const handle = K.render(container, payload);
  const overlay = attr(container, 'data-chart-hover')[0], tip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
  overlay.events.pointermove({clientX: 72 + 816 * 9 / 364, clientY: 100});
  assert.match(tip.textContent, /20 吨 · 2029-09-08/);
  assert.match(tip.textContent, /2\.0 倍 · 2029-09-11/);
  overlay.events.pointermove({clientX: 72 + 816 * 7 / 364, clientY: 100});
  assert.match(tip.textContent, /研究员比值 <安全>.*此位置缺失/);
  assert.deepEqual(pathXs(attr(attr(container, 'data-series', '0')[0], 'data-curve-run')[0]),
    [0, 7, 14].map(x => Number((72 + 816 * x / 364).toFixed(2))));
  handle.destroy();
});

test('八指标周期遵循全图上一期选项，指标无有效当前期不冒充当前或按标签猜年份', () => {
  const payload = multiCycleFixture(), prototype = payload.curves[0];
  payload.curves = Array.from({length: 8}, (_, index) => ({...structuredClone(prototype), curve_cycles: undefined,
    series_id: 'fixture-' + index, label: '指标' + index, style: {}, axis: 'left', cycle: cycle()}));
  payload.curves.forEach(curve => curve.cycle.segments.forEach(part => { part.label = '<任意期标签>'; }));
  payload.curves[7].cycle.segments.at(-1).points = [];
  const container = fixture(), handle = K.render(container, payload,
    {definition: {time: {mode: 'cycle', curve_cycles: 'current_and_previous'}}});
  assert.equal(attr(container, 'data-series').length, 15);
  assert.equal(attr(container, 'data-legend').length, 15);
  assert.equal(attr(container, 'data-series').filter(group => attr(group, 'data-curve-run').some(node => node.attrs['stroke-dasharray'] === '7 4')).length, 8);
  assert.equal(attr(container, 'data-latest-value').some(node => node.attrs['data-latest-value'] === '14'), false);
  const first = attr(attr(container, 'data-series', '0')[0], 'data-curve-run')[0];
  const prior = attr(attr(container, 'data-series', '1')[0], 'data-curve-run')[0];
  assert.equal(first.attrs.stroke, prior.attrs.stroke);
  assert.equal(attr(attr(container, 'data-series', '2')[0], 'data-curve-run')[0].attrs.stroke, palette.color(1));
  handle.destroy();
});

test('走势计算序列使用研究员图例与比值单位，不显示公式ID或自动推断名称', () => {
  const container = fixture(), label = '<研究员定义的公式图例>', handle = K.render(container,
    {mode: 'recent', curves: [{series_id: null, derived: {op: 'div', a: 'fixture-a', b: 'fixture-b'}, label, unit: '倍',
      points: [{date: '2030-01-01', value: 2}, {date: '2030-01-02', value: 3}]}]});
  assert.match(attr(container, 'data-legend', '0')[0].textContent, /研究员定义的公式图例/);
  assert.equal(attr(container, 'data-axis-unit', 'left')[0].textContent, '倍');
  assert.doesNotMatch(container.textContent, /fixture-a|fixture-b/);
  handle.destroy();
});

test('共同周期起点保护陈旧指标，缺上一期时不以更早有数据的周期替代', () => {
  const payload = multiCycleFixture(), container = fixture();
  payload.common_current_start = '2029-09-01'; payload.common_previous_start = '2028-09-01';
  payload.curves[1].curve_cycles = 'current_and_previous';
  payload.curves[1].cycle.segments.pop();
  payload.curves[1].cycle.segments.at(-1).current = true;
  const original = structuredClone(payload), handle = K.render(container, payload);
  const derivedLegend = attr(container, 'data-legend', '2')[0];
  assert.match(derivedLegend.textContent, /2028/);
  assert.doesNotMatch(derivedLegend.textContent, /当前/);
  assert.equal(attr(attr(container, 'data-series', '2')[0], 'data-curve-run')[0].attrs['stroke-dasharray'], '7 4');
  assert.deepEqual(payload, original); handle.destroy();
  payload.curves[1].cycle.segments.pop();
  const missing = K.render(container, payload);
  assert.equal(attr(container, 'data-series').length, 2, '2027有数据也不会冒充2028');
  assert.ok(attr(container, 'data-legend').every(node => !node.textContent.includes('研究员比值')));
  missing.destroy();
});

test('走势变换明确标明指数或变化值，不把rebase数值误标为原始单位', () => {
  for (const transform of ['change', 'rebase']) {
    const container = fixture(), handle = K.render(container, {transform, curves: [{label: '指标', unit: '吨',
      points: [{date: '2030-01-01', value: 100}, {date: '2030-01-02', value: 110}]}]});
    assert.equal(attr(container, 'data-axis-unit', 'left')[0].textContent, transform === 'rebase' ? '起点=100' : '变化值（吨）');
    const overlay = attr(container, 'data-chart-hover')[0], tip = nodes(container).find(node => node.className === 'chart-kit-tooltip');
    overlay.events.pointermove({clientX: 72, clientY: 100});
    if (transform === 'rebase') { assert.match(tip.textContent, /起点=100/); assert.doesNotMatch(tip.textContent, /吨/); }
    else assert.match(tip.textContent, /吨/);
    handle.destroy();
  }
});

test('规则固定值和区间按真实ID叠加，曲线同名与派生操作数不会建立关联', () => {
  const container = fixture(), payload = {curves: [
    {series_id: 'kb-right', label: '相同名称', axis: 'right', points: [{date: '2030-01-01', value: 10}, {date: '2030-01-02', value: 20}]},
    {series_id: 'kb-other', label: '相同名称', points: [{date: '2030-01-01', value: 1}]},
    {derived: {op: 'sub', a: 'kb-only-operand', b: 'kb-other'}, label: '派生', points: [{date: '2030-01-01', value: 1}]}
  ]}, overlays = [
    {series_id: 'kb-right', rule_id: 'range', kind: 'fixed', value: 8, name: '<阈值下界>'},
    {series_id: 'kb-right', rule_id: 'range', kind: 'fixed', value: 24, name: '<阈值上界>'},
    {series_id: 'kb-only-operand', rule_id: 'operand', kind: 'fixed', value: 10000},
    {series_id: '相同名称', rule_id: 'name', kind: 'fixed', value: 9999}
  ];
  const original = structuredClone({payload, overlays}), handle = K.render(container, payload, {overlays});
  assert.equal(attr(container, 'data-rule-overlay').length, 2);
  assert.ok(attr(container, 'data-rule-series').every(node => node.attrs['data-rule-series'] === 'kb-right'));
  assert.equal(attr(container, 'data-rule-boundary').length, 2);
  assert.match(attr(container, 'data-rule-label')[0].textContent, /<阈值下界>/);
  const rightTicks = attr(container, 'data-axis-tick', 'right').map(node => Number(node.textContent));
  assert.ok(Math.max(...rightTicks) >= 24 && Math.min(...rightTicks) <= 8);
  assert.deepEqual({payload, overlays}, original); handle.destroy();
});

test('SMA动态阈值使用服务器真实日期映射周期位置，缺失值断开且周期显隐一同生效', () => {
  const container = fixture(), payload = {template: {kind: 'repeat', start: '01-01', period: 'year'}, freq_days: 7,
    segments: [{label: '任意标签', start: '2030-01-01', end: '2030-12-31', current: true,
      points: [0, 7, 14, 21].map((x, i) => ({x, date: `2030-01-${String(x + 1).padStart(2, '0')}`, value: 10 + i, raw: 10 + i}))}],
    overlays: [{series_id: 'kb-exact', rule_id: 'sma', kind: 'sma', window: 3, name: '动态阈值', points: [
      {date: '2029-12-25', value: 8}, {date: '2030-01-01', value: 9}, {date: '2030-01-08', value: 10},
      {date: '2030-01-15', value: null}, {date: '2030-01-22', value: 12}
    ]}]};
  const handle = K.render(container, payload, {definition: {curves: [{series_id: 'kb-exact'}]}});
  const runs = attr(container, 'data-rule-boundary');
  assert.equal(runs.length, 2);
  assert.deepEqual(pathXs(runs[0]), [72, Number((72 + 816 * 7 / 364).toFixed(2))]);
  assert.deepEqual(pathXs(runs[1]), [Number((72 + 816 * 21 / 364).toFixed(2))]);
  attr(container, 'data-chart-hover')[0].events.pointermove({clientX: 72, clientY: 100});
  assert.match(attr(container, 'data-rule-tooltip')[0].textContent, /动态阈值 MA3：9/);
  attr(container, 'data-legend', '0')[0].events.click();
  assert.equal(attr(container, 'data-rule-overlay').length, 0);
  handle.destroy();
});

test('规则阈值按各周期原值起点变换，月度动态阈值与真实观测同位置', () => {
  const container = fixture(), payload = {template: {kind: 'repeat', start: '09-01', period: 'quarter', transform: 'rebase'}, freq_days: 30,
    segments: [{label: '研究期', start: '2030-09-01', end: '2030-11-30', current: true,
      points: [{x: 29, date: '2030-09-30', value: 100, raw: 20}, {x: 60, date: '2030-10-31', value: 150, raw: 30}]}]},
    overlays = [{series_id: 'kb-raw', rule_id: 'fixed', kind: 'fixed', value: 10},
      {series_id: 'kb-raw', rule_id: 'sma', kind: 'sma', window: 2, points: [{date: '2030-09-30', value: 18}, {date: '2030-10-31', value: 24}]}];
  const original = structuredClone(payload), handle = K.render(container, payload, {definition: {curves: [{series_id: 'kb-raw'}]}, overlays});
  assert.equal(attr(container, 'data-rule-overlay').length, 2);
  assert.match(attr(container, 'data-rule-label')[0].textContent, /50/);
  assert.deepEqual(pathXs(attr(container, 'data-rule-boundary').find(node => node.name === 'path')), pathXs(attr(container, 'data-curve-run')[0]));
  assert.deepEqual(payload, original); handle.destroy();
});

test('变化量证伪边界按真实日期显示为动态线，不标成均线', () => {
  const container = fixture(), payload = {curves: [{series_id: 'kb-change', label: '变化规则指标',
    points: [{date: '2030-01-01', value: 10}, {date: '2030-01-02', value: 12}, {date: '2030-01-03', value: 15}]}]},
    overlays = [{series_id: 'kb-change', rule_id: 'change', kind: 'dynamic', name: '前次观测加变化阈值',
      points: [{date: '2030-01-01', value: null}, {date: '2030-01-02', value: 13}, {date: '2030-01-03', value: 15}]}];
  const handle = K.render(container, payload, {overlays});
  assert.equal(attr(container, 'data-rule-overlay', 'dynamic').length, 1);
  assert.equal(attr(container, 'data-rule-boundary').length, 1);
  assert.doesNotMatch(attr(container, 'data-rule-overlay')[0].textContent, /MA/);
  attr(container, 'data-chart-hover')[0].events.pointermove({clientX: 480, clientY: 100});
  assert.match(attr(container, 'data-rule-tooltip')[0].textContent, /前次观测加变化阈值：13/);
  handle.destroy();
});

test('全为正数时纵轴留白不越过 0，不出现负刻度', () => {
  for (const [low, high] of [[600, 9000], [645, 9100], [0, 50]]) {
    const container = fixture();
    K.render(container, {curves: [{label: '库存', axis: 'left', points: [{date: '2030-01-01', value: low}, {date: '2030-01-02', value: high}]}]});
    const ticks = attr(container, 'data-axis-tick', 'left').map(node => node.textContent);
    assert.ok(ticks.length >= 5 && ticks.every(label => !label.trim().startsWith('-')), ticks.join(','));
  }
  const container = fixture();
  K.render(container, {curves: [{label: '利润', axis: 'left', points: [{date: '2030-01-01', value: -300}, {date: '2030-01-02', value: 900}]}]});
  assert.ok(attr(container, 'data-axis-tick', 'left').some(node => node.textContent.trim().startsWith('-')), '跨零数据仍保留负半轴');
});

test('周期图图例说明灰色底和灰色虚线：往期区间与均值', () => {
  const container = fixture();
  K.render(container, cycle(), {definition: structuredClone(definition)});
  const items = attr(container, 'data-band-legend');
  assert.deepEqual(items.map(node => node.attrs['data-band-legend']), ['range', 'mean']);
  const labels = items.map(node => node.children.find(child => child.name === 'text').textContent);
  assert.match(labels[0], /^近 3 .区间往.同期最低至最高（近 3 .）$/);
  assert.match(labels[1], /^近 3 .均值往.同期均值（近 3 .）$/);
  const flat = fixture();
  K.render(flat, {curves: [{label: '甲', axis: 'left', points: [{date: '2030-01-01', value: 1}, {date: '2030-01-02', value: 2}]}]});
  assert.equal(attr(flat, 'data-band-legend').length, 0, '非周期图不出现区间图例');
});
