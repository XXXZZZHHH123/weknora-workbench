/* DOM and raster-contract checks; no browser process or network required. */
const assert = require('node:assert/strict'), {test} = require('node:test');
const K = require('../src/tin/web/static/chart-kit.js');
class Element {
  constructor(name, doc) { this.name = name; this.ownerDocument = doc; this.attrs = {}; this.children = []; this.style = {setProperty() {}}; this.events = {}; this._text = ''; }
  set textContent(value) { this._text = String(value); this.children = []; }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  removeAttribute(key) { delete this.attrs[key]; }
  addEventListener(key, callback) { this.events[key] = callback; }
  append(...children) { children.forEach(child => { child.parent = this; this.children.push(child); }); }
  prepend(child) { child.parent = this; this.children.unshift(child); }
  replaceChildren(...children) { this.children = []; this._text = ''; this.append(...children); }
  replaceWith(child) { this.parent.children[this.parent.children.indexOf(this)] = child; child.parent = this.parent; }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(child => child !== this); }
  querySelectorAll() { return this.children.flatMap(child => [child, ...child.querySelectorAll()]); }
  cloneNode(deep) { const clone = new Element(this.name, this.ownerDocument); clone.attrs = {...this.attrs}; clone._text = this._text; if (deep) clone.append(...this.children.map(child => child.cloneNode(true))); return clone; }
  get viewBox() { const [x, y, width, height] = this.attrs.viewBox.split(' ').map(Number); return {baseVal: {x, y, width, height}}; }
}
function fixture(t) {
  const keys = ['document', 'Image', 'getComputedStyle', 'XMLSerializer'], previous = Object.fromEntries(keys.map(key => [key, globalThis[key]]));
  t.after(() => keys.forEach(key => { if (previous[key] === undefined) delete globalThis[key]; else globalThis[key] = previous[key]; }));
  const canvases = [], doc = {createElement(name) {
    const element = new Element(name, doc);
    if (name === 'canvas') {
      canvases.push(element); element.getContext = () => ({drawImage() {}});
      element.toBlob = callback => callback(new Blob(['png'], {type: 'image/png'}));
    }
    return element;
  }, createElementNS(ns, name) { return new Element(name, doc); }};
  doc.documentElement = doc.createElement('html'); globalThis.document = doc;
  globalThis.Image = class { set src(value) { queueMicrotask(() => this.onload()); } };
  globalThis.getComputedStyle = () => ({getPropertyValue(name) { return name === 'fill' ? '#ffffff' : '#111111'; }});
  globalThis.XMLSerializer = class { serializeToString(node) { return JSON.stringify({name: node.name, attrs: node.attrs, text: node.textContent}); } };
  const container = doc.createElement('div'); doc.documentElement.append(container);
  return {container, canvases};
}
function nodes(element) { return [element, ...element.children.flatMap(nodes)]; }

test('表格紧凑绘制日期/数值两列，保留0和小数；动态文本通过textContent插入', async t => {
  const {container, canvases} = fixture(t);
  const points = [{date: '2030-01-01', value: 0}, {date: '<img onerror=bad()>', value: .00001}, {date: '2030-01-03', value: null}];
  const original = structuredClone(points), handle = K.renderTable(container, points, {width: 240.5, height: 72, title: '<表格>'});
  const all = nodes(container), svg = all.find(node => node.name === 'svg'), texts = all.filter(node => node.name === 'text').map(node => node.textContent);
  assert.deepEqual(texts, ['日期', '数值', '2030-01-01', '0', '<img onerror=bad()>', '0.00001']);
  assert.equal(all.filter(node => node.name === 'img').length, 0); assert.equal(svg.attrs['aria-label'], '<表格>');
  assert.equal(svg.attrs.viewBox, '0 0 240.5 72'); assert.equal(svg.attrs.width, '240.5');
  assert.ok(all.some(node => node.attrs.fill === 'var(--surface)')); assert.ok(all.some(node => node.attrs.stroke === 'var(--line)'));
  assert.equal(typeof handle.toSVG(), 'string'); assert.equal((await handle.toPNGBlob()).type, 'image/png');
  assert.equal(canvases[0].width, 481); assert.equal(canvases[0].height, 144);
  assert.deepEqual(points, original); handle.destroy(); assert.equal(container.children.length, 0);
});
test('60行按给定图位高度收紧；空数据不补零，无效尺寸明确失败', t => {
  const {container} = fixture(t), points = Array.from({length: 60}, (_, i) => ({date: '2030-01-01', value: i}));
  const handle = K.renderTable(container, points, {width: 180, height: 400});
  const texts = nodes(container).filter(node => node.name === 'text');
  assert.equal(texts.length, 122); assert.ok(Number(texts[0].attrs['font-size']) < 4);
  assert.ok(Number(texts.at(-1).attrs.y) < 400); handle.destroy();
  assert.throws(() => K.renderTable(container, []), /暂无有效观测/);
  assert.throws(() => K.renderTable(container, [{date: '2030-01-01', value: 1}], {width: -1}), /尺寸无效/);
});
test('导出小表使用固定深色与清晰字号，按最长单元格分配列宽并保持2倍PNG尺寸', async t => {
  const {container, canvases} = fixture(t);
  const points = [{date: '2030-01-01', value: -123456789.12345678}, {date: '2030-01-02', value: 0}];
  const handle = K.renderTable(container, points, {width: 300, height: 96, export: true});
  const all = nodes(container), texts = all.filter(node => node.name === 'text');
  assert.ok(texts.every(node => Number(node.attrs['font-size']) >= 11 && node.attrs.fill === '#30343b'));
  assert.ok(all.some(node => node.name === 'rect' && node.attrs.fill === '#ffffff' && node.attrs.height === '96'));
  const split = Number(all.find(node => node.name === 'line' && node.attrs.x1 === node.attrs.x2).attrs.x1);
  assert.ok(split < 150, '长数值自动获得比日期更多的宽度');
  assert.equal(texts.at(-1).textContent, '0');
  await handle.toPNGBlob(); const canvas = canvases.at(-1);
  assert.equal(canvas.width, 600); assert.equal(canvas.height, 192); handle.destroy();
  const shorter = K.renderTable(container, [{date: '2030-01-01', value: 1}], {width: 300, height: 96, export: true});
  const shorterSplit = Number(nodes(container).find(node => node.name === 'line' && node.attrs.x1 === node.attrs.x2).attrs.x1);
  assert.ok(shorterSplit > split); shorter.destroy();
  assert.throws(() => K.renderTable(container, Array.from({length: 60}, () => points[0]), {width: 300, height: 400, export: true}), /至少 11px/);
});
test('导出图表PNG精确保留图位2倍像素，字号没有被exactSize缩小', async t => {
  const {container, canvases} = fixture(t);
  const handle = K.render(container, {curves: [{label: '曲线', points: [{date: '2030-01-01', value: 1}]}]},
    {width: 400.5, height: 180, exactSize: true, export: true, definition: {title: '导出标题'}});
  assert.ok(nodes(container).filter(node => node.name === 'text').every(node => Number(node.attrs['font-size']) >= 11));
  await handle.toPNGBlob(); const canvas = canvases.at(-1);
  assert.equal(canvas.width, 801); assert.equal(canvas.height, 360); handle.destroy();
});
test('PPT短图位精确保留宽高比，PNG为CSS尺寸2倍；常规图表仍遵守原最小高度', async t => {
  const {container, canvases} = fixture(t), payload = {curves: [{label: '曲线', points: [{date: '2030-01-01', value: 1}]}]};
  const handle = K.render(container, payload, {width: 320, height: 90, exactSize: true});
  const svg = nodes(container).find(node => node.name === 'svg');
  assert.equal(svg.attrs.viewBox, '0 0 320 90'); assert.equal(svg.attrs.height, '90');
  assert.ok(svg.children.some(node => node.name === 'g' && node.attrs.transform.startsWith('scale(')));
  await handle.toPNGBlob(); assert.equal(canvases[0].width, 640); assert.equal(canvases[0].height, 180); handle.destroy();
  const normal = K.render(container, payload, {width: 320, height: 90});
  assert.ok(Number(nodes(container).find(node => node.name === 'svg').attrs.height) > 90); normal.destroy();
});
