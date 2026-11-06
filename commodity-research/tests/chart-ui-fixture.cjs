/* Shared isolated Jinja/Playwright harness. All requests are fulfilled in-process. */
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const {spawnSync} = require('node:child_process');
const root = path.resolve(__dirname, '../..'), web = path.join(root, 'commodity-research/src/tin/web');
const prefix = '/research/workbenches/fixture', origin = 'http://charts.fixture', api = prefix + '/api/sn';
const template = {kind: 'repeat', name: '年度起点', start: '09-01', period: 'year', align: 'days', transform: 'raw'};
const templates = {items: [{id: '自然年', builtin: true, template: {...template, name: '自然年', start: '01-01'}},
  {id: 'saved-template', builtin: false, revision: 1, template}]};
const definition = {format: 'chart.v1', title: '合成图 <img src=x onerror=window.__xss=1>', group: '合成分组', note: '<b>合成备注</b>',
  curves: [{series_id: 'kb-fixture-series', label: '合成指标', kind: 'line', axis: 'left', style: {color: '#123456', width: 2, dash: 'solid'}, descriptor: {}}],
  time: {mode: 'cycle', template, cycles: 4, band_cycles: 5, transform: 'raw'}, axes: {left: {min: null, max: null}, right: {min: null, max: null}}, origin: {kind: 'manual', ref: ''}};
function cycle() {
  const segments = [2026, 2027, 2028, 2029].map((year, index) => ({label: index === 0 ? '<历史段>' : String(year), start: `${year}-09-01`, end: `${year + 1}-08-31`, current: index === 3,
    flags: index === 3 ? ['incomplete'] : [], points: (index === 3 ? [0, 7, 14] : [0, 7, 14, 35, 42]).map(x => ({x,
      date: new Date(Date.UTC(year, 8, 1 + x)).toISOString().slice(0, 10), value: 10 + index + x, raw: 10 + index + x}))}));
  return {template, freq_days: 7, tolerance_days: 3, as_of: '2029-09-15', segments, unit: '吨', warnings: [],
    position: {status: 'ok', x: 14, date: '2029-09-15', value: 27, n: 3, rank: 1, min: 24, max: 26, median: 25, mean: 25,
      vs_recent_mean: {kind: 'pct', value: .08, cycles: 3}, peers: segments.slice(0, 3).reverse().map(s => ({label: s.label, date: s.points[2].date, value: s.points[2].value}))},
    band: [{x: 0, min: 10, max: 12, mean: 11, n: 3}, {x: 7, min: 17, max: 19, mean: 18, n: 2},
      {x: 14, min: 24, max: 26, mean: 25, n: 3}, {x: 21, min: 31, max: 33, mean: 32, n: 3}, {x: 350, min: 360, max: 362, mean: 361, n: 3}],
    forward: [{h: 4, days: 28, items: [{label: '<历史段>', date: '2026-09-15', end_date: '2026-10-13', change: 3, pct: .125},
      {label: '2027', date: '2027-09-15', end_date: '2027-10-13', change: -1, pct: -.04}], up: 1, n: 2, median: 1, min: -1, max: 3}]};
}
function record(id = 'chart-1', changes = {}) { return {id, revision: 1, status: 'draft', definition: structuredClone(definition), warnings: [], ...changes}; }
function render(templateName, config = {}, extra = {}) {
  const input = {template: templateName, config: {enabled: true, can_write: true, id: 'chart-1', date: '2029-09-15', write_reason: '', ...config}, extra};
  const rendered = spawnSync(process.env.RESEARCH_PYTHON || path.join(root, '.venv/bin/python'), ['-c', String.raw`
import json,sys
from types import SimpleNamespace
from jinja2 import Environment,FileSystemLoader,DictLoader,ChoiceLoader,select_autoescape
data=json.loads(sys.argv[3])
host='''<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="/static/app.css"><style>[hidden]{display:none!important}body{margin:0}main{max-width:1280px;margin:16px auto;padding:0 16px}</style><script>window.RESEARCH_PREFIX='/research/workbenches/fixture';window.researchFetch=(url,options)=>fetch(url,options);window.showToast=()=>{};window.copyText=()=>{};window.ChartPaletteColors={{ palette|tojson }};</script><script src="/static/chart-palette.js"></script></head><body><main>{% block main %}{% endblock %}</main></body></html>'''
env=Environment(loader=ChoiceLoader([DictLoader({'base.html':host}),FileSystemLoader(sys.argv[1])]),autoescape=select_autoescape(['html']))
scope=SimpleNamespace(metadata=SimpleNamespace(name='合成研究台'),scope_version=2,restricted=False,can_edit=data['config']['can_write'])
env.globals.update(research_workbench=lambda:scope,research_prefix=lambda:'/research/workbenches/fixture',research_url=lambda s:'/research/workbenches/fixture'+s,research_static_prefix=lambda:'',static_version=lambda:'chart-ui-fixture')
ctx=dict(chart_page=data['config'],request=SimpleNamespace(query_params={'date':data['config'].get('date')}),palette=json.load(open(sys.argv[2])),restricted_view=False,**data['extra'])
print(env.get_template(data['template']).render(**ctx))
`, path.join(web, 'templates'), path.join(web, 'static/chart-palette.json'), JSON.stringify(input)], {stdio: ['ignore', 'pipe', 'pipe'], timeout: 20000, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024});
  assert.equal(rendered.status, 0, rendered.stderr); return rendered.stdout;
}
async function launch() {
  const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
  return chromium.launch({headless: true, timeout: 20000, ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {}),
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu', '--renderer-process-limit=1']});
}
async function open(browser, pathname, html, handler, options = {}) {
  const context = await browser.newContext({viewport: {width: 1280, height: 900}, acceptDownloads: true});
  const page = await context.newPage(), errors = [], calls = [];
  page.setDefaultTimeout(10000);
  page.on('pageerror', error => errors.push(error.message));
  // Expected 403/409 API responses are asserted by the tests, not treated as JS errors.
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== origin) { errors.push('Unexpected origin ' + url.origin); return route.abort(); }
    if (url.pathname === pathname) return route.fulfill({contentType: 'text/html', body: html});
    if (options.documents?.[url.pathname]) return route.fulfill({contentType: 'text/html', body: options.documents[url.pathname]});
    if (url.pathname.startsWith('/static/')) {
      const file = path.join(web, 'static', path.basename(url.pathname));
      if (fs.existsSync(file)) return route.fulfill({body: fs.readFileSync(file), contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript'});
    }
    if (url.pathname.startsWith(api)) {
      const contentType = request.headers()['content-type'] || '';
      const entry = {method: request.method(), path: url.pathname.slice(api.length), url,
        body: request.postData() && contentType.includes('application/json') ? request.postDataJSON() : null,
        rawBody: request.postDataBuffer(), headers: request.headers()}; calls.push(entry);
      const reply = (json, status = 200) => route.fulfill({status, json});
      if (await handler({...entry, route, reply}) !== false) return;
    }
    errors.push('Unhandled ' + request.method() + ' ' + url.pathname); return route.abort();
  });
  await page.goto(origin + pathname, {waitUntil: 'networkidle'});
  return {page, context, errors, calls, async close() { assert.deepEqual(errors, []); await context.close(); }};
}
module.exports = {assert, render, launch, open, prefix, origin, template, templates, definition, record, cycle};
