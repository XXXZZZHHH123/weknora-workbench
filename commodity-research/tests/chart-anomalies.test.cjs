/* Execute the shipped library controller without Chromium or local sockets. */
const assert = require('node:assert/strict'), {test} = require('node:test');
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');
const assets = path.join(__dirname, '../src/tin/web/static');
class Element {
  constructor() { this.children = []; this.events = {}; this.attrs = {}; this.hidden = false; this.dataset = {}; this.value = ''; }
  addEventListener(name, callback) { this.events[name] = callback; }
  setAttribute(name, value) { this.attrs[name] = value; }
  replaceChildren(...children) { this.children = children; }
  append(child) { this.children.push(child); }
}
async function library(data, {error, enabled = true} = {}) {
  const elements = new Map(), requests = [];
  const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  get('chart-page-config').textContent = JSON.stringify({enabled, date: '2029-09-15'});
  const page = new Element(); page.dataset.chartsPage = 'library';
  const document = {getElementById: get, querySelector: () => page, createElement: () => new Element()};
  const window = {addEventListener() {}, RESEARCH_PREFIX: '/research/workbenches/synthetic'};
  const context = vm.createContext({document, window, URLSearchParams, Headers, AbortController, setTimeout, clearTimeout});
  vm.runInContext(fs.readFileSync(path.join(assets, 'chart-ui.js'), 'utf8'), context);
  window.ChartUI = {...window.ChartUI, async api(url) {
    requests.push(url);
    if (url.startsWith('/charts/anomalies')) { if (error) throw error; return data; }
    if (url.startsWith('/charts?')) return {items: [], total: 0, next_offset: null};
    throw new Error('Unexpected API: ' + url);
  }};
  vm.runInContext(fs.readFileSync(path.join(assets, 'charts.js'), 'utf8'), context);
  await new Promise(setImmediate);
  return {get, requests};
}
function payload(count = 12) {
  return {as_of: '2029-09-15', total: count, failed: [], items: Array.from({length: count}, (_, i) => ({
    id: 'chart-' + (i + 1), title: '图表' + (i + 1), metric: '合成指标', date: '2029-09-14', value: 35,
    rank: 1, n: 6, unit: '吨', transform: 'raw', previous_rank: 3, previous_n: 5, previous_date: '2029-09-07',
    reasons: ['extreme_high', 'rank_change'], warnings: []
  }))};
}
test('默认前10条，展开全部和收起复用已授权结果，详情链接保留研究台与截止日', async () => {
  const {get, requests} = await library(payload());
  const rows = get('chart-anomalies-rows'), more = get('chart-anomalies-more');
  assert.equal((rows.innerHTML.match(/<tr>/g) || []).length, 10);
  assert.match(rows.innerHTML, /href="\/research\/workbenches\/synthetic\/sn\/charts\/chart-1\?date=2029-09-15"/);
  assert.match(rows.innerHTML, /合成指标/); assert.match(rows.innerHTML, /2029-09-14/);
  assert.match(rows.innerHTML, /35 吨/); assert.match(rows.innerHTML, /1\/7/); assert.match(rows.innerHTML, /3\/6/);
  assert.match(rows.innerHTML, /同期前两高/); assert.match(rows.innerHTML, /排名变化/);
  assert.equal(more.hidden, false); assert.equal(more.attrs['aria-expanded'], 'false');
  assert.equal(more.textContent, '展开全部 12 条');
  more.events.click();
  assert.equal((rows.innerHTML.match(/<tr>/g) || []).length, 12);
  assert.equal(more.attrs['aria-expanded'], 'true'); assert.equal(more.textContent, '收起为前 10 条');
  more.events.click();
  assert.equal((rows.innerHTML.match(/<tr>/g) || []).length, 10);
  assert.equal(requests.filter(url => url.startsWith('/charts/anomalies')).length, 1);
  assert.equal(requests.find(url => url.startsWith('/charts/anomalies')), '/charts/anomalies?date=2029-09-15');
});
test('空清单文案和无需展开的短清单', async () => {
  const empty = await library(payload(0));
  assert.equal(empty.get('chart-anomalies-status').textContent, '本期没有同期前两高、前两低或排名明显变化的图表');
  assert.equal(empty.get('chart-anomalies-table').hidden, true);
  assert.equal(empty.get('chart-anomalies-more').hidden, true);
  assert.equal(empty.get('chart-anomalies-failed').hidden, true);
  const short = await library(payload(10));
  assert.equal(short.get('chart-anomalies-more').hidden, true);
});
test('标题、指标、日期、单位、原因、失败原因和URL属性全部转义', async () => {
  const data = payload(1), unsafe = '<img src=x onerror="window.__xss=1"> & \' <script>';
  Object.assign(data.items[0], {id: 'chart/"<>', title: unsafe, metric: unsafe, date: unsafe, unit: unsafe,
    previous_date: unsafe, reasons: ['extreme_low', unsafe]});
  data.failed = [{title: unsafe, reason: unsafe}];
  const {get} = await library(data);
  const html = get('chart-anomalies-rows').innerHTML, failed = get('chart-anomalies-failed-list').innerHTML;
  assert.match(html, /&lt;img src=x onerror=&quot;window.__xss=1&quot;&gt; &amp; &#39; &lt;script&gt;/);
  assert.match(html, /chart%2F%22%3C%3E\?date=2029-09-15/);
  assert.match(html, /同期前两低/); assert.match(failed, /&lt;img/);
  assert.doesNotMatch(html + failed, /<img|<script>/);
  assert.equal(get('chart-anomalies-failed').hidden, false);
  assert.match(get('chart-anomalies-failed-summary').textContent, /1 张图表读取或计算失败/);
});
test('上一观测没有排名时明确显示，变换值沿用正确口径与窗口提示', async () => {
  const data = payload(1);
  Object.assign(data.items[0], {previous_rank: null, previous_n: 2, transform: 'rebase', warnings: ['observation_window_limited']});
  const {get} = await library(data);
  assert.match(get('chart-anomalies-rows').innerHTML, /暂无可用排名/);
  assert.match(get('chart-anomalies-rows').innerHTML, /35（起点=100）/);
  assert.doesNotMatch(get('chart-anomalies-rows').innerHTML, /35 吨/);
  assert.match(get('chart-anomalies-status').textContent, /1 张图表已达到观测读取上限/);
});
test('接口权限错误显示原因，禁用页面不发请求', async () => {
  for (const status of [403, 409]) {
    const error = Object.assign(new Error(status === 403 ? '受限视角暂不开放图表库' : '请重新打开研究台'), {status});
    const {get} = await library(null, {error});
    assert.match(get('chart-anomalies-status').textContent, /同期异动读取失败：受限视角|同期异动读取失败：请重新打开研究台/);
  }
  const disabled = await library(null, {enabled: false});
  assert.deepEqual(disabled.requests, []);
});
