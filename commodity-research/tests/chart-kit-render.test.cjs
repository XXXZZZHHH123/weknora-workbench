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
test('renderer实际纵轴遵循刻度数量和精度，双轴分别计算', () => {
  for (const thumbnail of [false, true]) {
    const container = fixture(), handle = K.render(container, {curves: [
      {label: '整数', axis: 'left', points: [{date: '2030-01-01', value: 20}, {date: '2030-01-02', value: 80}]},
      {label: '小数', axis: 'right', points: [{date: '2030-01-01', value: .5}, {date: '2030-01-02', value: 2}]}]}, {thumbnail});
    for (const axis of ['left', 'right']) {
      const ticks = attr(container, 'data-axis-tick', axis).map(n => n.textContent);
      assert.ok(thumbnail ? ticks.length <= 4 : ticks.length >= 5 && ticks.length <= 7);
      assert.deepEqual(ticks, axis === 'left'
        ? thumbnail ? ['0', '50', '100'] : ['0', '20', '40', '60', '80', '100']
        : thumbnail ? ['0', '1', '2', '3'] : ['0.0', '0.5', '1.0', '1.5', '2.0', '2.5']);
    }
    handle.destroy();
  }
});
