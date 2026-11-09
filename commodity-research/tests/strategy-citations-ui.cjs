/* Real strategy Jinja/JS/CSS with synthetic originals, references and saves only. */
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const {spawnSync} = require('node:child_process');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '../..'), web = path.join(root, 'commodity-research/src/tin/web');
const out = process.env.RESEARCH_CITATION_OUTPUT || path.join(root, '.build/strategy-citations-20261102');
const origin = 'http://strategy-citations.fixture', api = '/research/workbenches/fixture/api/sn';
fs.mkdirSync(out, {recursive: true, mode: 0o700});
const version = 'a'.repeat(64), fileVersion = 'b'.repeat(64);
const card = title => ({format: 'research-strategy-card.v1', title, direction: 'observe', thesis: '适用范围',
  watch_for: '核对公开数据', trigger: '原有触发条件', response_action: '等待确认后应对', invalidation: '条件变化后撤回',
  review_date: '2030-01-02', original_text: ''});
const record = (id, title) => ({id, title, status: 'draft', revision: 1, source_ids: [], source_snapshots: [],
  card: card(title), direction_label: '等待验证', can_edit: true, can_apply: true, scope_status: 'current'});
const a = record('00000000-0000-4000-8000-000000000101', '合成方案甲');
const b = record('00000000-0000-4000-8000-000000000102', '合成方案乙');
const records = new Map([a, b].map(item => [item.id, item]));
const chartReference = {kind: 'chart', id: 'fixture-chart', revision: 2, title: '合成周期图',
  source_version: version, source_versions: {'fixture-chart-series': version}, as_of: '2029-09-15', definition_summary: '周期对比 · 合成图表指标'};
const chartTemplate = {kind: 'repeat', name: '自然年', start: '01-01', period: 'year', align: 'days', transform: 'raw'};
const chartDefinition = {format: 'chart.v1', title: chartReference.title, group: '合成分组', note: '',
  curves: [{series_id: 'fixture-chart-series', label: '合成图表指标', axis: 'left', kind: 'line', style: {}}],
  time: {mode: 'cycle', template: chartTemplate, cycles: 4, transform: 'raw'}, axes: {left: {}, right: {}}, origin: {kind: 'manual', ref: ''}};
const chartPayload = {mode: 'cycle', unit: '吨', template: chartTemplate, as_of: chartReference.as_of,
  freq_days: 7, tolerance_days: 3, warnings: [], band: [], forward: [],
  segments: [2028, 2029].map(year => ({label: String(year), start: year + '-01-01', end: year + '-12-31', current: year === 2029, flags: [],
    points: [0, 7, 14].map(x => ({x, date: year + '-01-' + String(1 + x).padStart(2, '0'), value: 10 + x, raw: 10 + x}))})),
  position: {date: '2029-01-15', x: 14, value: 24, n: 3, rank: 1}};
function identity(kind, extra = {}) {
  return {kind, field: 'thesis', ...(kind === 'indicator' ? {series_id: 'synthetic-series', source_version: version}
    : kind === 'judgment_condition' ? {id: '17', revision: 2, rule_kind: 'threshold', rule_id: 'synthetic-boundary'}
      : {source_binding_id: 'binding', knowledge_id: 'original-text', file_version: fileVersion}), ...extra};
}
function preview(citation) {
  if (citation.kind === 'chart') return {citation: {...chartReference, definition: chartDefinition, field: citation.field,
    snapshot: {title: chartReference.title, subtitle: chartReference.definition_summary}}, title: chartReference.title,
    subtitle: chartReference.definition_summary, preview: {type: 'chart', reference: chartReference}};
  const type = citation.kind, name = citation.knowledge_id;
  const title = type === 'indicator' ? (citation.series_id === 'synthetic-price' ? '价格' : '库存量') : type === 'judgment_condition' ? '波动率高于19点' :
    ({'original-text': '研究原文.md', 'invalid-json': '不适用格式.json', pdf: '研究周报.pdf', 'parsed-pdf': '已有解析的周报.pdf', outdated: '已有更新的原文.md'}[name] || '知识库原件');
  const subtitle = type === 'indicator' ? 'kg · 已授权数据' : type === 'judgment_condition' ? '已保存研判 · 2030-01-01' : '已授权知识库 · 原件';
  const detail = type === 'indicator' ? {type: 'indicator', series_id: citation.series_id, name: title, unit: 'kg', detail_url: '/sn/indicators/synthetic-series'}
    : type === 'judgment_condition' ? {type: 'condition', text: '波动率高于19点时减少敞口，边界为严格大于。', rule_name: title, judgment_title: '已保存研判', origin_url: '/sn/judgment?judgment_id=17'}
      : {type: 'file', knowledge_id: name, file_name: title, file_type: name === 'pdf' ? 'pdf' : name === 'invalid-json' ? 'json' : 'md',
        url: '/platform/knowledge-bases/synthetic-kb?knowledge_id=' + name, content_supported: name !== 'pdf',
        ...(name === 'outdated' ? {version_status: 'updated', current_version: 'c'.repeat(64), notice: '原件已更新，当前引用仍指向原版本。'} : {version_status: 'current'})};
  return {citation: {...citation, snapshot: {title, subtitle, preview: detail}}, title, subtitle, preview: detail};
}
const rendered = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', String.raw`
import json,sys
from types import SimpleNamespace
from jinja2 import Environment,FileSystemLoader,DictLoader,ChoiceLoader,select_autoescape
data=json.load(sys.stdin)
host='''<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/fonts/workspace-typography.css"><link rel="stylesheet" href="/static/app.css"><style>[hidden]{display:none!important}body{margin:0}main{max-width:1440px;margin:16px auto;padding:0 16px}.hidden{display:none}</style><script>window.RESEARCH_PREFIX='/research/workbenches/fixture';window.researchFetch=window.fetch.bind(window);</script></head><body><main>{% block main %}{% endblock %}</main></body></html>'''
env=Environment(loader=ChoiceLoader([DictLoader({'base.html':host}),FileSystemLoader(sys.argv[1])]),autoescape=select_autoescape(['html']))
env.globals.update(research_workbench=lambda:SimpleNamespace(metadata=SimpleNamespace(name='隔离研究台')),research_prefix=lambda:'/research/workbenches/fixture',research_static_prefix=lambda:'',static_version=lambda:'citations-fixture',human_text=lambda x,names=None:str(x or ''))
a,b=data
cards={'enabled':True,'can_write':True,'items':[dict(a,card=None),dict(b,card=None)],'selected':a,'metadata_only':True}
context=dict(names={},workspace_can_write=True,platform_models=False,research_cards=cards,strategies=[],strategy_entries=[],workspace_selected='research:'+a['id'],workspace_initial=None,restricted_data={})
print(json.dumps({view:env.get_template('strategies.html').render(**context,restricted_view=restricted) for view,restricted in [('normal',False),('restricted',True)]}))
`, path.join(web, 'templates')], {input: JSON.stringify([a, b]), encoding: 'utf8', maxBuffer: 4 * 1024 * 1024});
assert.equal(rendered.status, 0, rendered.stderr);
const html = JSON.parse(rendered.stdout);
const report = {passed: false, requests: [], failures: [], cases: [], model_calls: 0, real_business_writes: 0};
let browser, failSave = false, failContent = false, failCycle = false;
(async () => {
  try {
    browser = await chromium.launch({headless: true, ...((process.env.SHEET_REVIEW_BROWSER || process.env.RESEARCH_CHROMIUM) ? {executablePath: process.env.SHEET_REVIEW_BROWSER || process.env.RESEARCH_CHROMIUM} : {}),
      args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu', '--renderer-process-limit=1']});
    const page = await browser.newPage({viewport: {width: 1440, height: 1000}}); page.setDefaultTimeout(15000);
    page.on('pageerror', error => report.failures.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()), method = request.method();
      report.requests.push({method, path: url.pathname, search: url.search});
      if (url.origin !== origin) { report.failures.push('Unexpected origin'); return route.abort(); }
      if (url.pathname === '/sn/strategies') return route.fulfill({contentType: 'text/html', body: url.searchParams.has('restricted') ? html.restricted : html.normal});
      if (url.pathname.startsWith('/static/')) {
        const filename = path.basename(url.pathname), file = path.join(web, 'static', filename);
        if (/^[\w.-]+$/.test(filename) && fs.existsSync(file)) return route.fulfill({body: fs.readFileSync(file), contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript'});
      }
      if (url.pathname.startsWith('/fonts/')) {
        const file = path.resolve(root, 'WeKnora/frontend/public' + url.pathname);
        if (file.startsWith(path.join(root, 'WeKnora/frontend/public/fonts/')) && fs.existsSync(file)) return route.fulfill({body: fs.readFileSync(file), contentType: file.endsWith('.css') ? 'text/css' : 'font/woff2'});
      }
      const endpoint = url.pathname.startsWith(api) ? url.pathname.slice(api.length) : '';
      if (endpoint === '/research-strategies/sources') return route.fulfill({json: {items: [], has_more: false}});
      if (/^\/research-strategies\/[^/]+\/revisions$/.test(endpoint)) return route.fulfill({json: {items: [], current_revision: 1, truncated: false}});
      if (endpoint === '/chart-references/options') return route.fulfill({json: {items: [chartReference], groups: ['合成分组'], total: 1, next_offset: null}});
      if (endpoint === '/chart-references/render') {
        const body = request.postDataJSON(); assert.equal(body.reference.id, chartReference.id); assert.equal(body.reference.revision, 2);
        return route.fulfill({json: {reference: chartReference, title: chartReference.title, status: 'updated', notice: '已有新修订',
          as_of: body.current ? '2030-01-02' : chartReference.as_of, current: body.current,
          definition: chartDefinition, payload: chartPayload, overlays: []}});
      }
      // Card monitoring (cited conditions' current state) is read-only; this fixture cites nothing it evaluates.
      if (/^\/research-strategies\/[^/]+\/monitor$/.test(endpoint) && method === 'GET') {
        const id = endpoint.split('/')[2], item = records.get(id);
        return route.fulfill({json: {conditions: [], indicators: [], charts: (item?.card?.citations || []).filter(row => row.kind === 'chart').map(row => ({reference: row})),
          counts: {conditions: 0, triggered: 0, missing: 0, indicators: 0}, notice: ''}});
      }
      if (endpoint === '/research-references') {
        const limit = Number(url.searchParams.get('limit') || 30), offset = Number(url.searchParams.get('offset') || 0);
        const all = Array.from({length: 137}, (_, index) => ({kind: 'judgment', id: String(index + 17), revision: 2,
          title: '合成研判 ' + (index + 1), written_at: '2030-01-01', status: '草稿'}));
        return route.fulfill({json: {items: all.slice(offset, offset + limit), total: all.length, truncated: offset + limit < all.length, offset}});
      }
      if (endpoint === '/research-strategies/citation-options') {
        const kind = url.searchParams.get('kind'), search = url.searchParams.get('search'), offset = Number(url.searchParams.get('offset'));
        if (kind === 'judgment_condition') { assert(url.searchParams.get('judgment_id')); assert.equal(url.searchParams.get('revision'), '2'); }
        if (search === 'slow') await new Promise(resolve => setTimeout(resolve, 900));
        let entries = kind === 'knowledge_file' ? ['original-text', 'invalid-json', 'pdf', 'outdated', 'parsed-pdf'].map(knowledge_id => identity(kind, {knowledge_id})) :
          [identity(kind, kind === 'judgment_condition' ? {id: url.searchParams.get('judgment_id')} : {})];
        let more = false, next = null;
        if (kind === 'indicator' && !search) {
          if (offset === 0) { more = true; next = 60; }
          else if (offset === 60) { entries = []; more = true; next = 90; }
          else entries = [identity(kind, {series_id: 'synthetic-price'})];
        }
        const items = entries.map(citation => { const data = preview(citation); return {citation, title: search === 'fast' ? '快速结果' : search === 'slow' ? '过时搜索结果' : data.title, subtitle: data.subtitle}; });
        return route.fulfill({json: {items, total: more ? 91 : items.length, has_more: more, next_offset: next, offset, limit: 30}});
      }
      if (endpoint === '/research-strategies/citation-preview' && method === 'POST') return route.fulfill({json: preview(request.postDataJSON().citation)});
      if (endpoint === '/research-strategies/citation-file-content') {
        if (failContent) return route.fulfill({status: 409, json: {detail: '原件版本已变化，请重新选择。'}});
        const id = url.searchParams.get('knowledge_id');
        return route.fulfill({json: {supported: true, knowledge_id: id, file_version: fileVersion,
          filename: id === 'invalid-json' ? '不适用格式.json' : id === 'parsed-pdf' ? '已有解析的周报.pdf' : '研究原文.md',
          content_kind: id === 'parsed-pdf' ? 'parsed_fragments' : 'original_text',
          ...(id === 'parsed-pdf' ? {notice: '知识库已有解析片段，可能有重叠、表格或人工修订，不等同原件全文。'} : {}),
          text: id === 'invalid-json' ? '{"unexpected":true}' : id === 'parsed-pdf' ? '已有的解析片段\n表格摘录仍须人工核对' : '来自知识库的研究原文\n<script>window.fixtureUnsafe=true</script>'}});
      }
      if (endpoint === '/indicators/previews') return route.fulfill({json: {cards: [{series_id: url.searchParams.get('series_id'), status: 'ok', value: url.searchParams.get('series_id') === 'synthetic-price' ? 0 : '12,500', unit: 'kg', as_of: '2030-01-01', change: '+15'}]}});
      if (/^\/indicators\/[^/]+\/cycle$/.test(endpoint)) {
        assert.equal(url.searchParams.get('date'), '2030-01-02');
        if (failCycle) return route.fulfill({status: 409, json: {detail: '该指标暂无足够的往年同期观测。'}});
        return route.fulfill({json: chartPayload});
      }
      const selected = endpoint.match(/^\/research-strategies\/([a-f0-9-]+)$/);
      if (selected && records.has(selected[1])) {
        const item = records.get(selected[1]);
        if (method === 'PATCH') {
          if (failSave) return route.fulfill({status: 409, json: {detail: '引用版本已改变，请重新核对。'}});
          const body = request.postDataJSON(); assert.equal(body.revision, item.revision);
          const saved = {...item, title: body.card.title, card: {...body.card, citations: (body.card.citations || []).map(citation => preview(citation).citation)}, revision: item.revision + 1};
          records.set(item.id, saved); return route.fulfill({json: saved});
        }
        return route.fulfill({json: item});
      }
      report.failures.push(method + ' ' + url.pathname); return route.abort();
    });
    const dialog = page.locator('#strategy-citation-dialog'), edit = page.locator('#research-card-dialog');
    const chooseKind = async kind => { await dialog.locator('[data-citation-kind="' + kind + '"]').click(); await dialog.locator('.strategy-citation-option').first().waitFor(); };
    const choose = async text => { await dialog.locator('.strategy-citation-option').filter({hasText: text}).first().click(); await dialog.locator('#strategy-citation-preview h3').waitFor(); };
    const done = () => dialog.getByRole('button', {name: '完成', exact: true}).click();
    await page.goto(origin + '/sn/strategies?date=2030-01-02', {waitUntil: 'networkidle'});
    assert(await page.evaluate(() => typeof window.ChartKit?.render === 'function'), 'Strategy workspace loads ChartKit');
    await page.evaluate(() => {
      const render = window.ChartKit.render; window.fixtureCycleRenders = [];
      window.ChartKit = {...window.ChartKit, render(container, payload, options) {
        if (container.hasAttribute('data-citation-indicator-cycle')) window.fixtureCycleRenders.push({width: options.width, height: options.height, containerWidth: container.clientWidth});
        return render(container, payload, options);
      }};
    });
    await page.getByRole('button', {name: '编辑', exact: true}).click();
    assert.equal(report.requests.filter(row => /citation-|indicators\/previews/.test(row.path)).length, 0, 'No citation directory or history is preloaded');
    await page.locator('#research-card-trigger').fill('未保存的触发文字');
    await page.locator('[data-citation-field="trigger"]').click(); await choose('库存量');
    await dialog.getByText('12,500 kg', {exact: true}).waitFor();
    const cycleRequests = () => report.requests.filter(row => /\/indicators\/[^/]+\/cycle$/.test(row.path));
    assert.equal(cycleRequests().length, 0, 'Opening the indicator preview only reads trend observations');
    assert.equal(await dialog.getByRole('tab', {name: '走势', exact: true}).getAttribute('aria-selected'), 'true');
    await dialog.getByRole('tab', {name: '同期', exact: true}).click();
    await dialog.locator('[data-citation-indicator-cycle] svg').waitFor();
    assert.equal(cycleRequests().length, 1);
    const cycleRender = await page.evaluate(() => window.fixtureCycleRenders[0]);
    assert.equal(cycleRender.height, 300); assert.equal(cycleRender.width, cycleRender.containerWidth);
    assert.equal(await dialog.locator('[data-citation-indicator-trend]').isVisible(), false);
    await dialog.getByRole('tab', {name: '走势', exact: true}).click(); await dialog.getByText('12,500 kg', {exact: true}).waitFor();
    await dialog.getByRole('tab', {name: '同期', exact: true}).click();
    assert.equal(cycleRequests().length, 1, 'Revisiting the same cycle view reuses the rendered chart');
    await dialog.getByRole('tab', {name: '走势', exact: true}).click();
    assert.match(await dialog.getByRole('link', {name: '查看指标详情 ↗'}).getAttribute('href'), /\/research\/workbenches\/fixture\/sn\/indicators\//);
    assert.equal(report.requests.filter(row => row.path.endsWith('/indicators/previews')).length, 1);
    await dialog.getByRole('button', {name: '引用此项', exact: true}).click();
    assert.equal(await page.locator('#research-card-trigger').inputValue(), '未保存的触发文字');
    await dialog.getByRole('button', {name: '加载更多', exact: true}).click();
    await page.waitForFunction(() => document.querySelector('#strategy-citation-options').innerText.includes('本批暂无') || !document.querySelector('#strategy-citation-more').disabled);
    await dialog.getByRole('button', {name: '加载更多', exact: true}).click();
    await page.waitForFunction(() => [...document.querySelectorAll('.strategy-citation-option')].length === 2);
    assert(report.requests.some(row => row.path.endsWith('/citation-options') && new URLSearchParams(row.search).get('offset') === '90'));
    await choose('价格'); await dialog.getByText('0 kg', {exact: true}).waitFor();
    assert.equal(cycleRequests().length, 1, 'Selecting another indicator does not preload its cycle data');
    failCycle = true; await dialog.getByRole('tab', {name: '同期', exact: true}).click();
    await dialog.locator('[data-citation-indicator-cycle][role="alert"]').getByText('该指标暂无足够的往年同期观测。', {exact: true}).waitFor();
    assert.equal(await dialog.locator('[data-citation-indicator-cycle] svg').count(), 0);
    failCycle = false; await dialog.getByRole('tab', {name: '走势', exact: true}).click(); await dialog.getByRole('tab', {name: '同期', exact: true}).click();
    await dialog.locator('[data-citation-indicator-cycle] svg').waitFor();
    report.cases.push('indicator cycle reads only on selection, uses page date and real ChartKit at container width/300px, reuses chart and displays API error before retry');
    await dialog.locator('#strategy-citation-search').fill('slow'); await page.waitForTimeout(300);
    await dialog.locator('#strategy-citation-search').fill('fast'); await dialog.getByText('快速结果', {exact: true}).waitFor(); await page.waitForTimeout(1000);
    assert(!await dialog.getByText('过时搜索结果', {exact: true}).count(), 'Late search cannot replace the current search');
    await chooseKind('judgment_condition'); await choose('波动率高于19点');
    for (let more = 0; more < 4; more++) {
      await dialog.getByRole('button', {name: '更多研判', exact: true}).click();
      await page.waitForFunction(() => !document.getElementById('strategy-citation-more-judgments').disabled);
    }
    await dialog.locator('#strategy-citation-judgment').selectOption('153:2');
    await choose('波动率高于19点');
    assert(report.requests.some(row => row.path.endsWith('/citation-options') && new URLSearchParams(row.search).get('judgment_id') === '153'), 'Versions beyond the first 100 are individually selected and read');
    assert.match(await dialog.locator('#strategy-citation-preview').innerText(), /严格大于/);
    assert(!/synthetic-boundary|synthetic-series/.test(await dialog.innerText()));
    await dialog.getByRole('button', {name: '引用此项', exact: true}).click(); await done();
    await page.locator('[data-strategy-select="research:' + b.id + '"]').click();
    await page.locator('#strategy-dynamic-preview h2').filter({hasText: b.title}).waitFor();
    await page.locator('[data-strategy-select="research:' + a.id + '"]').click();
    await edit.waitFor(); assert.equal(await page.locator('#research-card-trigger').inputValue(), '未保存的触发文字');
    assert.equal(await page.locator('[data-citation-list="trigger"] .strategy-citation-chip').count(), 2);
    report.cases.push('lazy indicator preview, exact condition, sparse paging, stale search and draft switching');
    await page.locator('[data-citation-field="invalidation"]').click(); await chooseKind('knowledge_file'); await choose('研究原文.md');
    const originalLink = dialog.getByRole('link', {name: '预览知识库原件 ↗'});
    assert.match(await originalLink.getAttribute('href'), /\/platform\/knowledge-bases\/synthetic-kb\?knowledge_id=original-text$/);
    assert.equal(await originalLink.getAttribute('target'), '_blank'); assert.equal(await originalLink.getAttribute('rel'), 'noopener');
    await dialog.getByRole('button', {name: '引用此项', exact: true}).click();
    assert.equal(await page.locator('#research-card-original').inputValue(), '', 'Linking a file never imports its body');
    await dialog.getByRole('button', {name: '导入正文', exact: true}).click(); await dialog.waitFor({state: 'hidden'});
    const imported = await page.locator('#research-card-original').inputValue(); assert.match(imported, /来自知识库/); assert.equal(await page.evaluate(() => window.fixtureUnsafe), undefined);
    assert.equal(await page.locator('[data-citation-list="original_text"] .strategy-citation-chip').count(), 1);
    page.once('dialog', prompt => prompt.dismiss());
    await page.locator('#research-card-file').setInputFiles({name: 'replacement.txt', mimeType: 'text/plain', buffer: Buffer.from('replacement')});
    await page.waitForTimeout(100); assert.equal(await page.locator('#research-card-original').inputValue(), imported);
    await page.locator('#research-card-library-import').click(); await choose('不适用格式.json');
    await dialog.getByRole('button', {name: '导入正文', exact: true}).click(); await dialog.locator('[role="alert"]').waitFor();
    assert.equal(await page.locator('#research-card-original').inputValue(), imported);
    await choose('研究周报.pdf'); assert.equal(await dialog.getByRole('button', {name: '导入正文', exact: true}).count(), 0);
    assert.match(await dialog.innerText(), /暂不支持导入正文/);
    await choose('已有更新的原文.md'); assert.equal(await dialog.getByRole('button', {name: '引用此项', exact: true}).isDisabled(), true);
    assert.equal(await dialog.getByRole('button', {name: '导入正文', exact: true}).count(), 0);
    await choose('研究原文.md'); failContent = true;
    await dialog.getByRole('button', {name: '导入正文', exact: true}).click(); await dialog.locator('[role="alert"]').waitFor();
    assert.equal(await page.locator('#research-card-original').inputValue(), imported); failContent = false;
    await done(); report.cases.push('file link versus explicit import, literal text safety, overwrite cancel and failed/unsupported/updated originals');
    const downloadPromise = page.waitForEvent('download'); await page.locator('#research-card-export').click();
    const download = await downloadPromise, exported = JSON.parse(fs.readFileSync(await download.path(), 'utf8'));
    assert.equal(exported.card.citations.length, 4);
    exported.card.title = '导入后可继续编辑'; exported.card.citations[0].snapshot = {title: '不可信客户端标题'};
    page.once('dialog', prompt => prompt.accept());
    await page.locator('#research-card-file').setInputFiles({name: 'roundtrip.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(exported))});
    await page.waitForFunction(() => document.getElementById('research-card-title').value === '导入后可继续编辑');
    assert(!await edit.getByText('不可信客户端标题', {exact: true}).count());
    failSave = true; await page.locator('#research-card-save').click(); await page.locator('#research-card-save-copy').waitFor();
    assert.equal(await page.locator('#research-card-trigger').inputValue(), '未保存的触发文字');
    assert.equal(await page.locator('[data-citation-list="trigger"] .strategy-citation-chip').count(), 2);
    failSave = false; await page.locator('#research-card-save').click(); await edit.waitFor({state: 'hidden'});
    await page.locator('#strategy-dynamic-preview h2').filter({hasText: '导入后可继续编辑'}).waitFor();
    assert(!/synthetic-series|synthetic-boundary|不可信客户端标题/.test(await page.locator('#strategy-dynamic-preview').innerText()));
    report.cases.push('JSON import/export, server rebuilt citation snapshots and failed save retains edits');
    await page.evaluate(() => document.fonts.ready); await page.screenshot({path: path.join(out, 'saved-citations-1440.png'), fullPage: true});
    await page.getByRole('button', {name: '编辑', exact: true}).click(); await page.locator('[data-citation-field="trigger"]').click();
    await dialog.getByRole('button', {name: '暂无，清除此项引用', exact: true}).click(); await dialog.waitFor({state: 'hidden'});
    assert.equal(await page.locator('[data-citation-list="trigger"] .strategy-citation-chip').count(), 0);
    assert.equal(await page.locator('[data-citation-list="invalidation"] .strategy-citation-chip').count(), 1);
    await page.setViewportSize({width: 390, height: 844}); await page.locator('[data-citation-field="response_action"]').click(); await chooseKind('knowledge_file');
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    assert(await dialog.evaluate(node => node.getBoundingClientRect().width <= innerWidth));
    await page.evaluate(() => document.fonts.ready); await page.screenshot({path: path.join(out, 'citation-picker-390.png'), fullPage: true}); await done();
    report.cases.push('clear is field-local, responsive narrow picker');
    await page.setViewportSize({width: 1440, height: 1000});
    await page.locator('#research-card-library-import').click(); await choose('已有解析的周报.pdf');
    const beforeFragments = await page.locator('#research-card-original').inputValue(); let confirmation = '';
    page.once('dialog', prompt => { confirmation = prompt.message(); return prompt.dismiss(); });
    await dialog.getByRole('button', {name: '导入正文', exact: true}).click();
    await dialog.getByText('已取消导入，原有输入保留。', {exact: true}).waitFor();
    assert.match(confirmation, /不等同原件全文/); assert.equal(await page.locator('#research-card-original').inputValue(), beforeFragments);
    page.once('dialog', prompt => { confirmation = prompt.message(); return prompt.accept(); });
    await dialog.getByRole('button', {name: '导入正文', exact: true}).click(); await dialog.waitFor({state: 'hidden'});
    assert.match(await page.locator('#research-card-original').inputValue(), /已有的解析片段/);
    assert.match(await page.locator('#research-card-filename').innerText(), /已解析文本片段/);
    await page.locator('[data-strategy-select="research:' + b.id + '"]').click(); await page.locator('#strategy-dynamic-preview h2').filter({hasText: b.title}).waitFor();
    await page.locator('[data-strategy-select="research:' + a.id + '"]').click(); await edit.waitFor();
    assert.match(await page.locator('#research-card-filename').innerText(), /已解析文本片段/);
    report.cases.push('existing parsed PDF fragments are labelled, explicitly confirmed and retained through draft switching');
    await page.locator('[data-strategy-chart-field="thesis"]').click();
    const picker = page.getByRole('dialog', {name: '插入图表', exact: true}); await picker.waitFor();
    await picker.locator('[data-chart-picker-group]').selectOption('合成分组');
    await picker.locator('[data-chart-picker-favorites]').check();
    await picker.locator('[data-chart-picker-search]').fill('合成');
    await page.waitForTimeout(250);
    await picker.getByRole('button', {name: /合成周期图/}).click(); await picker.waitFor({state: 'hidden'});
    assert.equal(await edit.locator('[data-citation-list="thesis"] [data-kind="chart"]').count(), 1);
    await page.locator('#research-card-save').click(); await edit.waitFor({state: 'hidden'});
    const chartCard = page.locator('.strategy-library-charts [data-chart-reference="fixture-chart"]');
    await chartCard.locator('svg').waitFor();
    await chartCard.locator('[data-chart-reference-position]').filter({hasText: '周期第 14 天'}).waitFor();
    assert.match(await chartCard.innerText(), /同期排名 1\/4/);
    await chartCard.locator('[data-chart-reference-mode]').selectOption('current');
    await chartCard.locator('[data-chart-reference-status]').filter({hasText: '当前数据 · 截止 2030-01-02'}).waitFor();
    await page.locator('.strategy-monitor-charts svg').waitFor();
    for (const selector of ['.strategy-library-charts .chart-reference-card', '.strategy-monitor-charts .chart-reference-card'])
      assert(await page.locator(selector).evaluate(node => node.querySelector('svg').getBoundingClientRect().right <= node.getBoundingClientRect().right + 1), 'Chart stays inside its card');
    await page.screenshot({path: path.join(out, 'chart-reference-thumbnails-1440.png'), fullPage: true});
    await page.setViewportSize({width: 390, height: 844});
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'Chart thumbnails do not overflow a narrow strategy page');
    await page.evaluate(() => {
      window.fixtureBeforeThemeSvg = document.querySelector('.strategy-library-charts svg');
      document.documentElement.classList.add('dark');
    });
    await page.waitForFunction(() => document.querySelector('.strategy-library-charts svg') !== window.fixtureBeforeThemeSvg);
    for (const selector of ['.strategy-library-charts .chart-reference-card', '.strategy-monitor-charts .chart-reference-card'])
      assert(await page.locator(selector).evaluate(node => node.querySelector('svg').getBoundingClientRect().right <= node.getBoundingClientRect().right + 1), 'Theme redraw keeps the chart inside its card');
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'Theme redraw stays responsive on a narrow strategy page');
    await page.evaluate(() => document.documentElement.classList.remove('dark'));
    await page.setViewportSize({width: 1440, height: 1000});
    assert(report.requests.some(row => row.path.endsWith('/chart-references/options') && new URLSearchParams(row.search).get('favorites') === 'true'));
    report.cases.push('shared chart picker with group/search/favorites, saved thumbnail, cycle position, current data switch, strategy monitor thumbnail and responsive theme redraw');
    const cyclesBeforeRestricted = cycleRequests().length;
    await page.goto(origin + '/sn/strategies?date=2030-01-02&restricted=1', {waitUntil: 'networkidle'});
    assert.equal(await page.locator('.indicator-view-switch').count(), 0, 'Restricted view has no trend/cycle switch');
    assert.equal(await page.locator('#strategy-citation-dialog').count(), 0);
    assert.equal(cycleRequests().length, cyclesBeforeRestricted, 'Restricted view never loads cycle data');
    report.cases.push('restricted strategy view omits indicator view switch and performs no cycle reads');
    assert.deepEqual(report.failures, []); report.passed = true;
  } catch (error) { report.error = error.stack; process.exitCode = 1; }
  finally {
    if (browser) await browser.close(); fs.writeFileSync(path.join(out, 'browser-result.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify({passed: report.passed, cases: report.cases, errors: report.failures, report: path.join(out, 'browser-result.json'), error: report.error}));
  }
})();
