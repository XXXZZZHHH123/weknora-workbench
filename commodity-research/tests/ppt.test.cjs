const assert = require('node:assert/strict'), {test} = require('node:test');
const P = require('../src/tin/web/static/ppt.js'), X = require('../src/tin/web/static/ppt-export.js');
const position = {x: 9525 * 10, y: 9525 * 20, cx: 9525 * 400, cy: 9525 * 180};
function slot(id = 'slot-1', extra = {}) { return {slot_id: id, slide: 1, kind: 'chart', replaceable: true, position: {...position}, ...extra}; }

test('EMU 精确换算；组合形状以 page_position 排布，导出仍使用 position 尺寸', () => {
  assert.equal(P.emuToPx(914400), 96); assert.equal(P.emuToPx(4762.5), .5); assert.equal(P.emuToPx(-9525), -1);
  assert.throws(() => P.emuToPx('9525'), /坐标无效/); assert.throws(() => P.emuToPx(Infinity), /坐标无效/);
  const presentation = {width_emu: 9525 * 1000, height_emu: 9525 * 500};
  const grouped = slot('grouped', {page_position: {x: 9525 * 100, y: 9525 * 50, cx: 9525 * 200, cy: 9525 * 90}});
  assert.deepEqual(P.slotRect(grouped, presentation), {left: 10, top: 10, width: 20, height: 18});
  assert.deepEqual(P.slotSize(grouped), {width: 400, height: 180});
  assert.deepEqual(P.slotRect(slot(), presentation), {left: 1, top: 4, width: 40, height: 36});
  assert.throws(() => P.slotSize(slot('zero', {position: {...position, cx: 0}})), /大于零/);
});
test('版面缩放保持幻灯片比例，宽高约束同时生效且不放大', () => {
  const presentation = {width_emu: 9525 * 1000, height_emu: 9525 * 500};
  assert.deepEqual(P.slideLayout(presentation, 600), {width: 600, height: 300, scale: .6});
  assert.deepEqual(P.slideLayout(presentation, 600, 200), {width: 400, height: 200, scale: .4});
  assert.deepEqual(P.slideLayout(presentation, 2000), {width: 1000, height: 500, scale: 1});
  assert.throws(() => P.slideLayout({...presentation, width_emu: 0}, 200), /尺寸无效/);
});
test('状态统计与明确批量确认：只选择尚未绑定且可替换图位的第一候选', () => {
  const slide = {slots: [slot('unbound'), slot('bound'), slot('keep'), slot('native', {replaceable: false}), slot('table', {kind: 'table'}), slot('none')]};
  const bindings = {bound: {kind: 'chart', chart_id: 'saved'}, keep: {kind: 'keep'}};
  const candidates = {unbound: [{kind: 'chart', chart_id: 'first', score: 0.6}, {kind: 'chart', chart_id: 'second', score: 0.55}],
    bound: [{kind: 'chart', chart_id: 'other', score: 0.9}], keep: [{kind: 'chart', chart_id: 'other', score: 0.9}], native: [{kind: 'chart', chart_id: 'other', score: 0.9}],
    table: [{kind: 'series', series_id: 'series-a', rows: 3}]};
  const original = structuredClone(bindings);
  assert.deepEqual(P.counts({presentation: {slides: [slide]}, bindings}), {bound: 1, kept: 2, pending: 3});
  assert.deepEqual(P.firstCandidates(slide, bindings, candidates), [{slot_id: 'unbound', binding: {kind: 'chart', chart_id: 'first'}},
    {slot_id: 'table', binding: {kind: 'series', series_id: 'series-a', rows: 3}}]);
  assert.deepEqual(P.unclearCandidates(slide, bindings, candidates), []);
  assert.deepEqual(bindings, original, '生成候选选择不修改现有绑定');
});
test('批量确认不选择分数过低或与第二名接近的图表候选', () => {
  const slide = {slots: [slot('tied'), slot('weak'), slot('clear')]};
  const candidates = {tied: [{kind: 'chart', chart_id: 'a', score: 0.242}, {kind: 'chart', chart_id: 'b', score: 0.241}],
    weak: [{kind: 'chart', chart_id: 'c', score: 0.35}], clear: [{kind: 'chart', chart_id: 'd', score: 0.6}, {kind: 'chart', chart_id: 'e', score: 0.5}]};
  assert.deepEqual(P.firstCandidates(slide, {}, candidates), [{slot_id: 'clear', binding: {kind: 'chart', chart_id: 'd'}}]);
  assert.deepEqual(P.unclearCandidates(slide, {}, candidates), ['tied', 'weak']);
  assert.equal(P.confidentCandidate([{kind: 'chart', chart_id: 'x', score: 0.45}, {kind: 'chart', chart_id: 'y', score: 0.44}]), null);
});
test('并发池上限为4；单项失败不阻断其他任务且按输入顺序返回', async () => {
  let active = 0, maximum = 0;
  const results = await P.mapLimit(Array.from({length: 13}, (_, i) => i), 99, async i => {
    active++; maximum = Math.max(maximum, active);
    await new Promise(resolve => setTimeout(resolve, (13 - i) % 3 + 2)); active--;
    if (i === 4) throw new Error('合成失败'); return i * 2;
  });
  assert.equal(maximum, 4); assert.equal(active, 0); assert.equal(results[4].status, 'rejected');
  results.forEach((result, i) => { if (i !== 4) assert.equal(result.value, i * 2); });
  assert.deepEqual(await P.mapLimit([], 4, () => assert.fail()), []);
  await assert.rejects(P.mapLimit([1], 0, () => {}), /并发数无效/);
});
test('数据日期来自实际有效观测；小表取最新 rows 条，不用请求截止日或补零', () => {
  const payload = {through: '2030-01-10', points: [{date: '2030-01-03', value: 3}, {date: '2030-01-01', value: 0},
    {date: '2030-01-04', value: null}, {date: 'bad', value: 9}, {date: '2030-01-02', value: 2}]};
  assert.deepEqual(X.latestRows(payload, 2, '2030-01-10').map(p => p.value), [2, 3]);
  assert.equal(X.lastObservation(payload.points, '2030-01-10'), '2030-01-03');
  assert.throws(() => X.latestRows(payload, 0, '2030-01-10'), /行数/);
  assert.throws(() => X.lastObservation([{date: '2030-01-11', value: 1}], '2030-01-10'), /之后/);
  assert.throws(() => X.lastObservation([{date: '2030-01-01', value: null}], '2030-01-10'), /暂无/);
  assert.equal(X.validDate('2030-02-29'), false); assert.equal(X.validDate('2032-02-29'), true);
});
test('渲染按真实接口取数、精确CSS尺寸、单行小表limit=2；始终销毁渲染器', async () => {
  const calls = [], draws = [], targets = []; let destroyed = 0;
  const deps = {api: async path => {
    calls.push(path);
    if (path.startsWith('/series/')) return {name: '指标', points: [{date: '2030-01-01', value: 1}, {date: '2030-01-02', value: 0}]};
    if (path.includes('/data?')) return {revision: 7, as_of: '2030-01-10', curves: [{points: [{date: '2030-01-02', value: 2}]}]};
    return {revision: 7, definition: {title: '图表'}};
  }, kit: {render(...args) { draws.push(['chart', ...args]); return {async toPNGBlob() { return new Blob(['png'], {type: 'image/png'}); }, destroy() { destroyed++; }}; },
    renderTable(...args) { draws.push(['table', ...args]); return {async toPNGBlob() { return new Blob(['png'], {type: 'image/png'}); }, destroy() { destroyed++; }}; }},
  createTarget() { const target = {style: {}, remove() { this.removed = true; }}; targets.push(target); return target; }};
  const chart = await X.renderSlot(slot(), {kind: 'chart', chart_id: 'chart-1'}, '2030-01-10', deps);
  const table = await X.renderSlot(slot(), {kind: 'series', series_id: 'series-1', rows: 1}, '2030-01-10', deps);
  assert.deepEqual(calls, ['/charts/chart-1', '/charts/chart-1/data?date=2030-01-10', '/series/series-1/history?date=2030-01-10&limit=2']);
  assert.equal(chart.dataAsOf, '2030-01-02'); assert.equal(table.dataAsOf, '2030-01-02');
  assert.deepEqual(draws[0][3], {width: 400, height: 180, exactSize: true, definition: {title: '图表'}});
  assert.deepEqual(draws[1][2], [{date: '2030-01-02', value: 0}]);
  assert.equal(targets[0].style.width, '400px'); assert.equal(targets[0].style.height, '180px');
  assert.equal(destroyed, 2); assert.ok(targets.every(target => target.removed));
  await assert.rejects(X.renderSlot(slot('unsupported', {replaceable: false}), {kind: 'chart'}, '2030-01-10', deps), /保留原图/);
  const huge = {...deps, kit: {...deps.kit, renderTable() { return {async toPNGBlob() { return new Blob([new Uint8Array(4 * 1024 * 1024 + 1)], {type: 'image/png'}); }, destroy() { destroyed++; }}; }}};
  await assert.rejects(X.renderSlot(slot(), {kind: 'series', series_id: 's', rows: 1}, '2030-01-10', huge), error => error.dataAsOf === '2030-01-02' && /4 MB/.test(error.message));
  assert.equal(destroyed, 3);
});
test('失效来源、图表并发修改及空观测拒绝生成图片', async () => {
  for (const data of [{revision: 2}, {revision: 1, notices: [{code: 'missing'}]},
    {revision: 1, curves: [{status: 'missing'}]}, {revision: 1, curves: [{points: []}]}]) {
    await assert.rejects(X.renderSlot(slot(), {kind: 'chart', chart_id: 'id'}, '2030-01-01', {
      api: async path => path.includes('/data?') ? data : {revision: 1, definition: {title: '图'}},
      createTarget() { assert.fail('无效图表不应进入渲染'); }
    }), /更新|失效|暂无/);
  }
});
test('multipart字段与后端一致；meta记录失败；下载地址保持研究台前缀并拒绝外部地址', () => {
  const png = new Blob(['png'], {type: 'image/png'}), meta = {ok: {data_as_of: '2030-01-01'}, bad: {error: '读取失败'}};
  const form = X.buildFormData('2030-01-10', true, new Map([['ok', png]]), meta);
  assert.deepEqual([...form.keys()], ['date', 'mark_headlines', 'meta', 'ok']);
  assert.equal(form.get('mark_headlines'), 'true'); assert.deepEqual(JSON.parse(form.get('meta')), meta);
  assert.equal(form.get('ok').type, 'image/png'); assert.equal(form.has('bad'), false);
  globalThis.RESEARCH_PREFIX = '/research/workbenches/fixture';
  assert.equal(X.downloadURL({export_id: 'export-1', download_url: '/api/sn/ppt-templates/t/exports/export-1'}, 't'), '/research/workbenches/fixture/api/sn/ppt-templates/t/exports/export-1');
  assert.equal(X.downloadURL({export_id: 'export-1', download_url: 'javascript:alert(1)'}, 't'), null);
  delete globalThis.RESEARCH_PREFIX;
});
test('API请求使用研究台前缀和researchFetch；JSON与multipart的Content-Type分别处理', async t => {
  const previous = globalThis.researchFetch, oldPrefix = globalThis.RESEARCH_PREFIX, calls = [];
  t.after(() => { globalThis.researchFetch = previous; globalThis.RESEARCH_PREFIX = oldPrefix; });
  globalThis.RESEARCH_PREFIX = '/research/workbenches/fixture';
  globalThis.researchFetch = async (url, init) => { calls.push({url, init}); return {ok: true, json: async () => ({revision: 2})}; };
  await P.api('/ppt-templates/t/bindings', {method: 'PATCH', body: {revision: 1, slots: []}});
  const form = X.buildFormData('2030-01-01', false, new Map(), {});
  await P.api('/ppt-templates/t/export', {method: 'POST', body: form});
  assert.equal(calls[0].url, '/research/workbenches/fixture/api/sn/ppt-templates/t/bindings');
  assert.equal(calls[0].init.headers.get('Content-Type'), 'application/json');
  assert.equal(JSON.parse(calls[0].init.body).revision, 1);
  assert.equal(calls[1].init.body, form); assert.equal(calls[1].init.headers.has('Content-Type'), false);
  globalThis.researchFetch = async () => ({ok: false, status: 409, json: async () => ({detail: '修订冲突'})});
  await assert.rejects(P.api('/ppt-templates/t'), error => error.status === 409 && error.message === '修订冲突');
});
test('上传实际字节进度与解析阶段分开；XHR携带与researchFetch相同的账号核验头', async t => {
  const previous = globalThis.XMLHttpRequest, context = globalThis.RESEARCH_CONTEXT, prefix = globalThis.RESEARCH_PREFIX;
  t.after(() => { globalThis.XMLHttpRequest = previous; globalThis.RESEARCH_CONTEXT = context; globalThis.RESEARCH_PREFIX = prefix; });
  class Events {
    constructor() { this.events = new Map(); }
    addEventListener(name, callback) { this.events.set(name, callback); }
    emit(name, event) { this.events.get(name)?.(event); }
  }
  let xhr;
  globalThis.XMLHttpRequest = class extends Events {
    constructor() { super(); xhr = this; this.headers = {}; this.upload = new Events(); }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(key, value) { this.headers[key] = value; }
    send(form) {
      assert.equal(form.get('file').name, 'synthetic.pptx');
      queueMicrotask(() => {
        this.upload.emit('progress', {lengthComputable: true, loaded: 40, total: 80}); this.upload.emit('load');
        this.status = 201; this.responseText = '{"id":"template-1"}'; this.emit('load'); this.emit('loadend');
      });
    }
  };
  globalThis.RESEARCH_CONTEXT = {tenant: 7, user: 'fixture-user'}; globalThis.RESEARCH_PREFIX = '/research/workbenches/fixture';
  const progress = [], file = new File(['synthetic'], 'synthetic.pptx');
  assert.deepEqual(await P.uploadFile(file, (...args) => progress.push(args)), {id: 'template-1'});
  assert.deepEqual(progress, [['upload', 0], ['upload', 50], ['parse', null]]);
  assert.deepEqual(xhr.headers, {'X-Tenant-ID': '7', 'X-Research-User': 'fixture-user'});
  assert.equal(xhr.url, '/research/workbenches/fixture/api/sn/ppt-templates');
});
