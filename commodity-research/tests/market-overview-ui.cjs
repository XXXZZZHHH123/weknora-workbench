// Isolated browser fixture: actual market/indicator templates and JS, synthetic in-memory facts only.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
if (!process.argv.includes('--run') && !process.argv.includes('--render-only')) { console.log('Prepared only. --run executes a synthetic in-memory browser fixture; --render-only checks its templates.'); process.exit(0); }
const repo = path.resolve(__dirname, '..'), web = path.join(repo, 'src/tin/web');
const rendered = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', `
import json
from datetime import datetime
from dataclasses import asdict
from fastapi import Request
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool
import tin.models
from tin.db import Base,make_engine
from tin.models import Indicator,Observation
from tin.config import SHANGHAI
from tin.integration.identity import Identity,current_identity
from tin.integration.workbench_scope import WorkbenchScope,current_workbench
from tin.web import app as web
from tin.export.board import _obs_card
from tin.export.indicators import previews
identity=Identity('fixture-user','fixture@example.invalid','隔离测试',17,'owner')
scope=WorkbenchScope(identity,'a0000000-0000-4000-8000-000000000001','b0000000-0000-4000-8000-000000000001',17,{'name':'隔离市场工作台','workspace_mode':'isolated','can_edit':True},scope_version=2,sources_revision=1)
current_identity.set(identity);current_workbench.set(scope)
engine=make_engine('sqlite://',poolclass=StaticPool,connect_args={'check_same_thread':False});Base.metadata.create_all(engine)
factory=sessionmaker(engine,expire_on_commit=False)
stamp=datetime(2026,3,3,15,tzinfo=SHANGHAI)
with factory() as session:
 for n in range(1,19):
  sid='kb-'+format(n,'048x')
  session.add(Indicator(series_id=sid,name='隔离指标%02d'%n,variety='',category='知识库字段',caliber={'time_type':'原表日期'},unit='件',source='知识库结构化',frequency='日',fetch_mode='readonly',status='可用'))
  session.flush();session.add(Observation(series_id=sid,value=n,as_of=stamp,fetched_at=stamp,caliber_snapshot={'time_type':'原表日期'},source='知识库结构化',status='正常'))
 session.commit()
def session():
 value=factory();value.info.update(workbench_id=scope.workbench_id,knowledge_base_id=scope.knowledge_base_id,knowledge_base_tenant_id=17,knowledge_facts=True,knowledge_facts_catalog={});return value
web.SessionLocal=session
from tin.integration import source_status
source_status.source_status_hint=lambda *args,**kwargs:{'pending':2,'pending_fields':7,'message':'知识库还有 2 张工作表待确认（含 7 个字段），确认后才能在这里使用','links':[{'url':'/platform/knowledge-bases/fixture-kb/structured-data?mode=import&source_knowledge_id=fixture-original','source_file':'隔离来源'}]}
from tin.web import catalog as catalog_api
with session() as database:
 directory_rows=web.indicator_data.catalog(database)
def directory(scope,offset=0,**kwargs):
 return {'indicators':directory_rows,'offset':offset,'total':len(directory_rows),'next_offset':None}
catalog_api.indicator_page=directory
request=Request({'type':'http','method':'GET','path':'/sn','query_string':b'','headers':[],'scheme':'http','server':('market.fixture',80)})
market=web.variety_page(request).body.decode();indicators=web.indicators_page(request).body.decode()
cards={};favorites={}
with session() as database:
 for n in range(1,19):
  sid='kb-'+format(n,'048x');card=_obs_card(database,sid,'隔离指标%02d'%n,sid,stamp.date())
  cards[sid]=web.templates.get_template('_market_cards.html').render(cards=[card])
  preview=previews(database,[sid],stamp.date())
  favorites[sid]={'card':preview[0],'html':web.templates.get_template('_indicator_cards.html').render(cards=preview,d='2026-03-03')}
charts={f'chart-{n}':web.templates.get_template('_market_cards.html').render(cards=[{'kind':'chart','id':f'chart-{n}','title':f'隔离图表{n}','status':'pending'}]) for n in range(1,7)}
print(json.dumps({'market':market,'indicators':indicators,'cards':cards,'chart_cards':charts,'favorites':favorites,'catalog':directory_rows},ensure_ascii=False))
`], { cwd: repo, encoding: 'utf8', env: process.env });
assert.equal(rendered.status, 0, rendered.stderr);
const fixture = JSON.parse(rendered.stdout), ids = Object.keys(fixture.cards), prefix = '/workbenches/a0000000-0000-4000-8000-000000000001';
if (process.argv.includes('--render-only')) { console.log('market-overview templates: passed'); process.exit(0); }
const { chromium } = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const favoritesKey = 'tin.indicators.favorites.v1:17:fixture-user:a0000000-0000-4000-8000-000000000001';
const report = {mode: 'isolated_market_focus_ui_fixture', real_model_calls: 0, real_business_mutations: 0, assertions: [], requests: 0};
(async () => {
  const browser = await chromium.launch({headless: true,
    ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {}),
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking', '--renderer-process-limit=1', '--js-flags=--max-old-space-size=128']});
  try {
    const page = await browser.newPage({viewport: {width: 1400, height: 950}, reducedMotion: 'reduce'}), errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let fail = false, core = [], favorites = {format: 'favorites.v1', revision: 1,
      items: [{kind: 'indicator', id: ids[17], added_at: '2026-03-03T00:00:00Z', status: 'active', title: '隔离指标18'}]};
    const chartItems = Object.keys(fixture.chart_cards).map((id, index) => ({id, revision: 1, definition: {
      title: '隔离图表' + (index + 1), group: '测试分组', note: '', curves: [{series_id: ids[index], label: '测试曲线', kind: 'line', axis: 'left'}],
      time: {mode: 'cycle', template: {kind: 'repeat', name: '自然年', start: '01-01', period: 'year'}, transform: 'raw', cycles: 8, band_cycles: 5}, axes: {left: {}, right: {}}}}));
    const cardHTML = item => item.kind === 'chart' ? fixture.chart_cards[item.id] : fixture.cards[item.id];
    const title = item => item.kind === 'chart' ? chartItems.find(chart => chart.id === item.id).definition.title : '隔离指标' + String(ids.indexOf(item.id) + 1).padStart(2, '0');
    function marketHTML() {
      return fixture.market.replace(/(<script id="market-core-catalog" type="application\/json">)([\s\S]*?)(<\/script>)/,
        (_, before, value, after) => before + JSON.stringify({...JSON.parse(value), core_items: core, charts: chartItems.map(chart => ({id: chart.id, title: chart.definition.title}))}) + after)
        .replace(/(<div id="market-core-grid"[^>]*>)[\s\S]*?(<\/div>)/, (_, before, after) => before + core.map(cardHTML).join('') + after);
    }
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()); report.requests++;
      assert.equal(url.origin, 'http://market.fixture', 'No external requests');
      const pathName = url.pathname;
      const writable = [prefix + '/api/sn/favorites', prefix + '/api/sn/market/core/config', prefix + '/api/sn/market/core/suggestions'];
      assert.ok(request.method() === 'GET' || writable.includes(pathName), 'Only synthetic fixture writes');
      if (pathName === prefix + '/sn') return route.fulfill({contentType: 'text/html; charset=utf-8', body: marketHTML()});
      if (pathName === prefix + '/sn/indicators') return route.fulfill({contentType: 'text/html; charset=utf-8', body: fixture.indicators});
      if (pathName === prefix + '/api/sn/favorites') {
        if (request.method() === 'PUT') {
          const body = request.postDataJSON(); assert.equal(body.revision, favorites.revision);
          favorites = {...favorites, revision: favorites.revision + 1, items: body.items.map(item => ({...item, status: 'active', title: title(item)}))};
        }
        return route.fulfill({json: favorites});
      }
      if (pathName === prefix + '/api/sn/market/core/config') {
        core = request.postDataJSON().items; assert.ok(core.length <= 16); return route.fulfill({json: {items: core}});
      }
      if (pathName === prefix + '/api/sn/strategies/models') return route.fulfill({json: {models: [{id: 'fixture-model', name: '隔离模型'}]}});
      if (pathName === prefix + '/api/sn/market/core/suggestions') {
        assert.equal(request.postDataJSON().model_id, 'fixture-model');
        return route.fulfill({json: {items: [0, 2].map((index, i) => ({series_id: ids[index], name: '隔离指标' + String(index + 1).padStart(2, '0'), group: i ? '供给' : '核心标的', reason: '隔离模型给出的研究理由'})), notice: '共读取 18 个字段。', truncated: false}});
      }
      if (pathName === prefix + '/api/sn/indicators/catalog') {
        const requested = url.searchParams.getAll('series_id'), search = url.searchParams.get('search') || '';
        const indicators = fixture.catalog.filter(row => (!requested.length || requested.includes(row.series_id)) && (!search || row.name.includes(search)));
        return route.fulfill({json: {indicators, total: indicators.length, offset: 0, next_offset: null}});
      }
      // The core list asks whether its indicators have confirmed default-library replacements.
      if (pathName === prefix + '/api/sn/indicator-dedup/references') return route.fulfill({json: {items: [], related_charts: [], can_rewrite: true, revision: 1, sources_revision: 7, reason: ''}});
      if (pathName === prefix + '/api/sn/charts') return route.fulfill({json: {items: chartItems, total: chartItems.length, next_offset: null}});
      if (pathName.startsWith(prefix + '/api/sn/charts/')) {
        const id = pathName.split('/')[6], item = chartItems.find(item => item.id === id); assert.ok(item, pathName);
        if (pathName.endsWith('/data')) return route.fulfill({json: {id, revision: 1, unit: '件', template: item.definition.time.template,
          segments: [{current: true, label: '2026', start: '2026-01-01', points: [{x: 60, date: '2026-03-02', raw: 12, value: 12}, {x: 61, date: '2026-03-03', raw: 14, value: 14}]}],
          band: [], position: {rank: 2, n: 4}, warnings: []}});
        return route.fulfill({json: item});
      }
      if (pathName.endsWith('/cycle')) {
        const id = pathName.split('/').at(-2), value = ids.indexOf(id) + 1;
        return route.fulfill({json: {unit: '件', segments: [{points: [{date: '2026-03-03', raw: value, value}]}], position: {n: 0}, warnings: []}});
      }
      if (pathName === prefix + '/api/sn/market/core') {
        if (fail) return route.fulfill({status: 503, json: {detail: '隔离读取失败'}});
        const items = url.searchParams.getAll('item').map(value => { const [kind, id] = value.split(':'); return {kind, id}; });
        assert.deepEqual(items, core); return route.fulfill({json: {items, ready_count: items.filter(item => item.kind === 'indicator').length, html: items.map(cardHTML).join('')}});
      }
      if (pathName.startsWith('/static/')) {
        const file = path.join(web, 'static', path.basename(pathName)); assert.ok(fs.existsSync(file));
        return route.fulfill({contentType: pathName.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(file)});
      }
      if (pathName.startsWith('/fonts/')) {
        const file = path.join(repo, '..', 'WeKnora/frontend/public', pathName); assert.ok(fs.existsSync(file), file);
        return route.fulfill({contentType: pathName.endsWith('.woff2') ? 'font/woff2' : 'text/css', body: fs.readFileSync(file)});
      }
      if (pathName === '/favicon.ico') return route.fulfill({status: 204});
      throw Error('Unexpected fixture route ' + pathName);
    });
    await page.goto('http://market.fixture' + prefix + '/sn');
    await page.waitForSelector('#market-core-open');
    assert.match(await page.locator('#market-core-title').innerText(), /核心关注/);
    assert.match(await page.locator('#market-core-empty [data-source-status-hint]').innerText(), /2 张工作表待确认（含 7 个字段）/);
    await page.locator('#market-core-suggestions-open').click();
    await page.waitForFunction(() => !document.getElementById('market-core-suggestions-run').disabled);
    await page.locator('#market-core-suggestions-run').click();
    await page.waitForSelector('#market-core-suggestions-list input');
    assert.equal(await page.locator('#market-core-suggestions-list label').count(), 2);
    assert.match(await page.locator('#market-core-suggestions-list').innerText(), /核心标的.*隔离指标01/s);
    await page.locator('#market-core-suggestions-list input').nth(1).uncheck();
    await page.locator('#market-core-suggestions-adopt').click();
    await page.waitForFunction(() => document.querySelectorAll('[data-market-card-series]').length === 1);
    assert.deepEqual(core, [{kind: 'indicator', id: ids[0]}]);
    report.assertions.push('empty_focus_review_hint_model_picker_and_adopt_checked_suggestions');
    await page.evaluate(([key, value]) => localStorage.setItem(key, JSON.stringify(value)), [favoritesKey, [ids[17]]]);
    const box = index => page.locator(`#market-core-choices input[value="${ids[index]}"]`);
    await page.locator('#market-core-open').click();
    assert.equal(await page.locator('#market-core-source').inputValue(), 'favorites');
    await page.locator('#market-core-source').selectOption('catalog'); await page.waitForSelector(`#market-core-choices input[value="${ids[16]}"]`);
    for (let n = 1; n < 16; n++) await box(n).check();
    await box(16).click(); assert.equal(await box(16).isChecked(), false); assert.match(await page.locator('#market-core-dialog-error').innerText(), /16/);
    await page.locator('#market-core-cancel').click(); assert.equal(core.length, 1);
    report.assertions.push('sixteen_limit_and_cancel_keep_shared_configuration');
    await page.locator('#market-core-open').click(); await page.locator('#market-core-source').selectOption('catalog');
    await box(0).uncheck(); await box(3).check(); await box(1).check();
    await page.getByRole('button', {name: '上移隔离指标02', exact: true}).click();
    await page.locator('#market-core-source').selectOption('charts');
    await page.locator('#market-core-choices input[value="chart-1"]').check();
    assert.deepEqual(await page.locator('#market-core-selected > li > span').allTextContents(), ['1. 隔离指标02', '2. 隔离指标04', '3. 隔离图表1（图表）']);
    await page.locator('#market-core-save').click();
    await page.waitForSelector('[data-market-card-chart="chart-1"] svg');
    assert.deepEqual(core, [{kind: 'indicator', id: ids[1]}, {kind: 'indicator', id: ids[3]}, {kind: 'chart', id: 'chart-1'}]);
    assert.match(await page.locator('[data-focus-chart-summary]').innerText(), /最新值 14 件/);
    assert.match(await page.locator('[data-focus-chart-summary]').innerText(), /同期第 2\/5/);
    assert.ok(favorites.items.some(item => item.kind === 'chart' && item.id === 'chart-1'));
    assert.deepEqual(await page.evaluate(key => JSON.parse(localStorage.getItem(key)), favoritesKey), [ids[17]]);
    report.assertions.push('mixed_order_shared_save_auto_account_favorites_cycle_thumbnail_and_rank');
    await page.reload(); await page.waitForSelector('[data-market-card-chart="chart-1"] svg');
    assert.equal(await page.locator('[data-market-card-series]').count(), 2);
    report.assertions.push('reload_restores_authoritative_shared_items');
    fail = true; await page.locator('#market-core-open').click(); await page.locator('#market-core-source').selectOption('catalog'); await box(4).check(); await page.locator('#market-core-save').click();
    await page.waitForSelector('#market-core-error:not([hidden])'); assert.equal(core.length, 4); assert.equal(await page.locator('[data-market-card-series]').count(), 0);
    fail = false; await page.locator('#market-core-retry').click(); await page.waitForFunction(() => document.querySelectorAll('[data-market-card-series]').length === 3);
    report.assertions.push('card_read_failure_preserves_shared_selection_and_retry');
    const screenshotDir = process.env.MARKET_SCREENSHOTS;
    if (screenshotDir) { fs.mkdirSync(screenshotDir, {recursive: true}); await page.locator('#market-core').scrollIntoViewIfNeeded(); await page.screenshot({path: path.join(screenshotDir, 'market-focus-fixture.png')}); }
    assert.deepEqual(errors, []); report.assertions.push('zero_browser_errors_and_only_synthetic_writes');
    console.log(JSON.stringify(report));
  } finally { await browser.close(); }
})().catch(error => {console.error(error.stack); process.exitCode = 1;});
