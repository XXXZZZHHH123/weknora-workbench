// Isolated browser fixture: actual market/indicator templates and JS, synthetic in-memory facts only.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
if (!process.argv.includes('--run')) { console.log('Prepared only. --run executes a synthetic in-memory browser fixture; no real account or business data.'); process.exit(0); }
const { chromium } = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
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
scope=WorkbenchScope(identity,'a0000000-0000-4000-8000-000000000001','b0000000-0000-4000-8000-000000000001',17,{'name':'隔离市场工作台','workspace_mode':'isolated'})
current_identity.set(identity);current_workbench.set(scope)
engine=make_engine('sqlite://',poolclass=StaticPool,connect_args={'check_same_thread':False});Base.metadata.create_all(engine)
factory=sessionmaker(engine,expire_on_commit=False)
stamp=datetime(2026,3,3,15,tzinfo=SHANGHAI)
with factory() as session:
 for n in range(1,15):
  sid='kb-'+format(n,'048x')
  session.add(Indicator(series_id=sid,name='隔离指标%02d'%n,variety='',category='知识库字段',caliber={'time_type':'原表日期'},unit='件',source='知识库结构化',frequency='日',fetch_mode='readonly',status='可用'))
  session.flush();session.add(Observation(series_id=sid,value=n,as_of=stamp,fetched_at=stamp,caliber_snapshot={'time_type':'原表日期'},source='知识库结构化',status='正常'))
 session.commit()
def session():
 value=factory();value.info.update(workbench_id=scope.workbench_id,knowledge_base_id=scope.knowledge_base_id,knowledge_base_tenant_id=17);return value
web.SessionLocal=session
request=Request({'type':'http','method':'GET','path':'/sn','query_string':b'','headers':[],'scheme':'http','server':('market.fixture',80)})
market=web.variety_page(request).body.decode();indicators=web.indicators_page(request).body.decode()
cards={};favorites={}
with session() as database:
 for n in range(1,15):
  sid='kb-'+format(n,'048x');card=_obs_card(database,sid,'隔离指标%02d'%n,sid,stamp.date())
  cards[sid]=web.templates.get_template('_market_cards.html').render(cards=[card])
  preview=previews(database,[sid],stamp.date())
  favorites[sid]={'card':preview[0],'html':web.templates.get_template('_indicator_cards.html').render(cards=preview,d='2026-03-03')}
print(json.dumps({'market':market,'indicators':indicators,'cards':cards,'favorites':favorites},ensure_ascii=False))
`], { cwd: repo, encoding: 'utf8', env: process.env });
assert.equal(rendered.status, 0, rendered.stderr);
const fixture = JSON.parse(rendered.stdout), ids = Object.keys(fixture.cards), prefix = '/workbenches/a0000000-0000-4000-8000-000000000001';
const coreKey = 'tin.market.core.v1:17:fixture-user:a0000000-0000-4000-8000-000000000001';
const favoritesKey = 'tin.indicators.favorites.v1:17:fixture-user:a0000000-0000-4000-8000-000000000001';
const report = { mode: 'isolated_market_ui_fixture', real_model_calls: 0, real_business_mutations: 0, assertions: [], requests: 0 };
(async () => {
  const browser = await chromium.launch({ headless: true,
    ...(process.env.SHEET_REVIEW_BROWSER ? { executablePath: process.env.SHEET_REVIEW_BROWSER } : {}),
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-background-networking', '--renderer-process-limit=1', '--js-flags=--max-old-space-size=128'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1400, height: 950 }, reducedMotion: 'reduce' }), errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    let fail = false;
    await page.route('**/*', async (route) => {
      const request = route.request(), url = new URL(request.url()); report.requests++;
      assert.equal(request.method(), 'GET', 'Fixture must never schedule model calls or business mutations');
      assert.equal(url.origin, 'http://market.fixture', 'No external requests');
      if (url.pathname === prefix + '/sn') return route.fulfill({ contentType: 'text/html; charset=utf-8', body: fixture.market });
      if (url.pathname === prefix + '/sn/indicators') return route.fulfill({ contentType: 'text/html; charset=utf-8', body: fixture.indicators });
      if (url.pathname === prefix + '/api/sn/market/core') {
        if (fail) return route.fulfill({ status: 503, json: { detail: 'Isolated outage' } });
        const selected = url.searchParams.getAll('series_id');
        assert.ok(selected.length <= 12 && selected.every((id) => ids.includes(id)));
        return route.fulfill({ json: { series_ids: selected, ready_count: selected.length, html: selected.map((id) => fixture.cards[id]).join('') } });
      }
      if (url.pathname === prefix + '/api/sn/indicators/previews') {
        const selected = url.searchParams.getAll('series_id');
        return route.fulfill({ json: { cards: selected.map((id) => fixture.favorites[id].card), html: selected.map((id) => fixture.favorites[id].html).join('') } });
      }
      if (url.pathname.startsWith('/static/')) {
        const file = path.join(web, 'static', path.basename(url.pathname));
        assert.ok(fs.existsSync(file));
        return route.fulfill({ contentType: url.pathname.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(file) });
      }
      if (url.pathname.startsWith('/fonts/')) {
        const file = path.join(repo, '..', 'WeKnora/frontend/public', url.pathname);
        assert.ok(fs.existsSync(file), file);
        return route.fulfill({ contentType: url.pathname.endsWith('.woff2') ? 'font/woff2' : 'text/css', body: fs.readFileSync(file) });
      }
      if (url.pathname === '/favicon.ico') return route.fulfill({ status: 204 });
      throw Error('Unexpected fixture route ' + url.pathname);
    });
    await page.goto('http://market.fixture' + prefix + '/sn');
    await page.waitForSelector('#market-core-open');
    await page.evaluate(() => { window.fixtureSourceMessages = []; window.addEventListener('message', (event) => { if (event.origin === location.origin && event.data?.type === 'research-open-sources') window.fixtureSourceMessages.push(event.data.type); }); });
    await page.locator('#market-contract-source > summary').click();
    await page.locator('#market-contract-source').getByRole('button', { name: '资料与来源', exact: true }).click();
    await page.waitForFunction(() => window.fixtureSourceMessages.length === 1);
    await page.locator('#market-contract-source > summary').click();
    report.assertions.push('source_button_uses_existing_same_origin_host_bridge');
    await page.evaluate(([key, value]) => localStorage.setItem(key, JSON.stringify(value)), [favoritesKey, [ids[13]]]);
    const box = (index) => page.locator(`#market-core-choices input[value="${ids[index]}"]`);
    await page.locator('#market-core-open').click();
    await page.waitForSelector('#market-core-dialog[open]');
    assert.equal(await page.locator('#market-core-selected > li').count(), 12);
    await box(12).click();
    assert.equal(await box(12).isChecked(), false); assert.match(await page.locator('#market-core-dialog-error').innerText(), /12/);
    await page.locator('#market-core-cancel').click();
    assert.equal(await page.evaluate((key) => localStorage.getItem(key), coreKey), null);
    report.assertions.push('limit_and_cancel_preserve_preferences');
    await page.locator('#market-core-open').click();
    for (let n = 0; n < 12; n++) await box(n).uncheck();
    await box(3).check(); await box(1).check();
    await page.getByRole('button', { name: '上移隔离指标02', exact: true }).click();
    assert.deepEqual(await page.locator('#market-core-selected > li > span').allTextContents(), ['1. 隔离指标02', '2. 隔离指标04']);
    await page.locator('#market-core-save').click();
    await page.waitForFunction(() => document.querySelectorAll('[data-market-card-series]').length === 2 && !document.getElementById('market-core-grid').hasAttribute('aria-busy'));
    assert.deepEqual(await page.locator('[data-market-card-series]').evaluateAll((elements) => elements.map((element) => element.dataset.marketCardSeries)), [ids[1], ids[3]]);
    assert.deepEqual(await page.evaluate((key) => JSON.parse(localStorage.getItem(key)), favoritesKey), [ids[13], ids[1], ids[3]]);
    report.assertions.push('ordered_selection_and_auto_favorites_use_actual_dom');
    await page.reload();
    await page.waitForFunction(() => document.querySelectorAll('[data-market-card-series]').length === 2);
    assert.deepEqual(await page.evaluate((key) => JSON.parse(localStorage.getItem(key)), coreKey), [ids[1], ids[3]]);
    await page.locator('#market-core-open').click(); await box(1).uncheck(); await page.keyboard.press('Escape');
    assert.deepEqual(await page.evaluate((key) => JSON.parse(localStorage.getItem(key)), coreKey), [ids[1], ids[3]]);
    report.assertions.push('reload_and_keyboard_cancel_restore_scoped_order');
    const screenshotDir = process.env.MARKET_SCREENSHOTS;
    if (screenshotDir) {
      fs.mkdirSync(screenshotDir, { recursive: true });
      await page.locator('#market-core-open').click();
      await page.screenshot({ path: path.join(screenshotDir, 'market-core-picker-fixture.png') });
      await page.locator('#market-core-cancel').click();
      await page.locator('#market-core').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(screenshotDir, 'market-core-selected-fixture.png') });
    }
    fail = true; await page.locator('#market-core-open').click(); await box(4).check(); await page.locator('#market-core-save').click();
    await page.waitForSelector('#market-core-error:not([hidden])');
    assert.deepEqual(await page.evaluate((key) => JSON.parse(localStorage.getItem(key)), coreKey), [ids[1], ids[3], ids[4]]);
    assert.equal(await page.locator('[data-market-card-series]').count(), 0);
    fail = false; await page.locator('#market-core-retry').click();
    await page.waitForFunction(() => document.querySelectorAll('[data-market-card-series]').length === 3);
    report.assertions.push('read_failure_keeps_selection_and_retry_works_without_old_cards');
    await page.goto('http://market.fixture' + prefix + '/sn/indicators');
    await page.waitForFunction(() => document.querySelectorAll('[data-favorite-card]').length === 4);
    for (const index of [13, 1, 3, 4]) assert.equal(await page.locator(`tr[data-series-id="${ids[index]}"] [data-favorite]`).getAttribute('aria-pressed'), 'true');
    report.assertions.push('existing_indicator_page_reads_same_favorites');
    assert.deepEqual(errors, []); report.assertions.push('zero_browser_errors_and_zero_mutation_requests');
    console.log(JSON.stringify(report));
  } finally { await browser.close(); }
})().catch((error) => { console.error(error.stack); process.exitCode = 1; });
