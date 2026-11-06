/* Real strategy Jinja/JS/CSS for the text and Markdown tools; every API response is synthetic. */
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const {spawnSync} = require('node:child_process');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '../..'), web = path.join(root, 'commodity-research/src/tin/web');
const origin = 'http://strategy-tools.fixture', api = '/research/workbenches/fixture/api/sn';
const card = title => ({format: 'research-strategy-card.v1', title, direction: 'observe', thesis: '', watch_for: '',
  trigger: '原有触发条件', response_action: '等待确认后应对', invalidation: '条件变化后撤回', review_date: '2030-01-02', original_text: ''});
const record = (id, title) => ({id, title, status: 'draft', revision: 1, source_ids: [], source_snapshots: [],
  card: card(title), direction_label: '等待验证', can_edit: true, can_apply: true, scope_status: 'current'});
const a = record('00000000-0000-4000-8000-000000000201', '合成方案甲');
const records = new Map([[a.id, a]]);
const planRecord = {id: 41, title: '合成交易计划', status: 'draft', version: 1, scope_status: 'current',
  plan: {title: '合成交易计划', thesis: '原有研究逻辑', invalidation: '条件变化后撤回', entry_condition: '人工核对后入场',
    entry: {low: 100, high: 110, reason: '合成入场依据'}, target: {low: 120, high: 130, reason: '合成目标依据'},
    stop: {low: 80, high: 90, reason: '合成止损依据'}, review_at: '2030-01-02T08:00:00Z', risk_budget: 100,
    legs: [{contract: 'FIXTURE2601', side: 'buy', ratio: 1}], evidence: [], alternative_explanation: '合成替代解释',
    market_expectation: '合成市场预期', catalyst: '下一次观察', horizon_days: 2}};
const exported = '# 策略方案\n\n## 合成方案甲\n<!-- card: ' + a.id + ' revision: 1 -->\n- 方向：等待验证\n- 复核日期：2030-01-02\n- 依据研判：不引用\n\n### 适用 / 触发\n原有触发条件\n\n### 应对措施\n等待确认后应对\n\n### 暂停 / 撤回\n条件变化后撤回\n';
const condition = {kind: 'judgment_condition', field: 'invalidation', id: '17', revision: 2, rule_kind: 'falsifier', rule_id: 'synthetic-rule',
  snapshot: {title: '库存连续两期增加', subtitle: '库存增减 · 连续 2 期'}};
const drafted = {card: {format: 'research-strategy-card.v1', title: '累库撤回方案', direction: 'bear', trigger: '指标高于一万二',
  response_action: '分批减仓', invalidation: '库存连续两周累库', thesis: '', watch_for: '', original_text: '库存连续两周累库就撤回；高于一万二分批减仓。',
  review_date: '2030-01-31', judgment_reference: {kind: 'judgment', id: '17', revision: 2}, citations: [condition]}, source_ids: [], acknowledge_observation: false};
const rendered = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', String.raw`
import json,sys
from types import SimpleNamespace
from jinja2 import Environment,FileSystemLoader,DictLoader,ChoiceLoader,select_autoescape
data=json.load(sys.stdin)
host='''<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="/static/app.css"><style>[hidden]{display:none!important}body{margin:0}main{max-width:1440px;margin:16px auto;padding:0 16px}</style><script>window.RESEARCH_PREFIX='/research/workbenches/fixture';window.researchFetch=(url,options)=>fetch(url,options)</script></head><body><main>{% block main %}{% endblock %}</main></body></html>'''
env=Environment(loader=ChoiceLoader([DictLoader({'base.html':host}),FileSystemLoader(sys.argv[1])]),autoescape=select_autoescape(['html']))
env.globals.update(research_workbench=lambda:SimpleNamespace(metadata=SimpleNamespace(name='隔离研究台')),research_prefix=lambda:'/research/workbenches/fixture',research_static_prefix=lambda:'',static_version=lambda:'tools-fixture',human_text=lambda x,names=None:str(x or ''))
[a,plan]=data
cards={'enabled':True,'can_write':True,'items':[dict(a,card=None)],'selected':a,'metadata_only':True}
entries=[dict(plan,key='plan:'+str(plan['id']),status_label='草稿',kind_label='方向策略',review_date='2030-01-02')]
print(env.get_template('strategies.html').render(names={},restricted_view=False,workspace_can_write=True,platform_models=True,research_cards=cards,strategies=[],strategy_entries=entries,workspace_selected='research:'+a['id'],workspace_initial=None))
`, path.join(web, 'templates')], {input: JSON.stringify([a, planRecord]), encoding: 'utf8', maxBuffer: 4 * 1024 * 1024});
assert.equal(rendered.status, 0, rendered.stderr);
assert.match(rendered.stdout, /id="strategy-workspace-data"|strategy-workspace/);

(async () => {
  const browser = await chromium.launch({headless: true, ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {}),
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu', '--renderer-process-limit=1']});
  const failures = [], calls = []; let page;
  try {
    page = await browser.newPage({viewport: {width: 1280, height: 900}});
    page.setDefaultTimeout(10000);
    page.on('pageerror', error => failures.push(error.message));
    page.on('console', message => { if (message.type() === 'error') failures.push('console ' + message.text()); });
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url()), method = request.method();
      if (url.origin !== origin) { failures.push('Unexpected origin ' + url.origin); return route.abort(); }
      if (url.pathname === '/sn/strategies') return route.fulfill({contentType: 'text/html', body: rendered.stdout});
      if (url.pathname.startsWith('/static/')) {
        const file = path.join(web, 'static', path.basename(url.pathname));
        if (fs.existsSync(file)) return route.fulfill({body: fs.readFileSync(file), contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript'});
        return route.fulfill({status: 404, body: ''});
      }
      const endpoint = url.pathname.startsWith(api) ? url.pathname.slice(api.length) : '';
      calls.push(method + ' ' + endpoint);
      if (endpoint === '/research-strategies/sources') return route.fulfill({json: {items: [], has_more: false}});
      if (/^\/research-strategies\/[^/]+\/revisions$/.test(endpoint)) return route.fulfill({json: {items: [], current_revision: 1, truncated: false}});
      if (endpoint === '/research-references') return route.fulfill({json: {items: [{id: '17', revision: 2, title: '合成研判'}], total: 1}});
      if (/\/monitor$/.test(endpoint)) return route.fulfill({json: {conditions: [], indicators: [], counts: {conditions: 0, triggered: 0, missing: 0, indicators: 0}, notice: ''}});
      if (endpoint === '/research-strategies/markdown') return route.fulfill({json: {markdown: exported, count: 1, total: 1, skipped: 0, truncated: false, can_import: true}});
      if (endpoint === '/research-strategies/markdown/preview' && method === 'POST') {
        const text = request.postDataJSON().markdown;
        if (text.includes('坏方向')) return route.fulfill({json: {cards: [{line: 3, title: '合成方案甲', card_id: a.id, action: 'error', changes: [],
          warnings: [], errors: ['第 5 行的方向无法识别：坏方向（可写偏多观察、偏空观察、区间观察或等待验证）。']}], counts: {create: 0, update: 0, unchanged: 0, error: 1}, untouched: 0, can_apply: false}});
        const changed = text.includes('新的触发条件'), created = text.includes('## 新方案');
        return route.fulfill({json: {cards: [
          {line: 3, title: '合成方案甲', card_id: a.id, action: changed ? 'update' : 'unchanged', changes: changed ? ['适用 / 触发'] : [], warnings: [], errors: []},
          ...(created ? [{line: 20, title: '新方案', card_id: null, action: 'create', changes: [], warnings: ['未填写应对措施，将保存为观察方案。'], errors: []}] : [])],
          counts: {create: created ? 1 : 0, update: changed ? 1 : 0, unchanged: changed ? 0 : 1, error: 0}, untouched: 2, can_apply: changed || created}});
      }
      if (endpoint === '/research-strategies/markdown/apply' && method === 'POST') {
        const saved = {...a, title: '合成方案甲', revision: 2, card: {...a.card, trigger: '新的触发条件'}, action: 'update'};
        records.set(a.id, saved);
        return route.fulfill({json: {saved: [saved], counts: {create: 0, update: 1, unchanged: 0, error: 0}, untouched: 2}});
      }
      if (endpoint === '/strategies/models') return route.fulfill({json: {models: [{id: 'synthetic-model', name: '合成通用模型'}]}});
      if (endpoint === '/strategies/' + planRecord.id && method === 'GET') return route.fulfill({json: planRecord});
      if (endpoint === '/research-strategies/draft-from-text' && method === 'POST') {
        const body = request.postDataJSON();
        assert.equal(body.model_id, 'synthetic-model');
        if (body.text.includes('两份')) return route.fulfill({json: {cards: [{body: drafted, notes: []}, {body: drafted, notes: []}],
          markdown: '# 策略方案\n\n## 累库撤回方案\n- 方向：偏空观察\n- 复核日期：2030-01-31\n\n### 适用 / 触发\nA\n\n### 暂停 / 撤回\nB\n\n## 新方案\n- 复核日期：2030-01-31\n\n### 适用 / 触发\nC\n\n### 暂停 / 撤回\nD\n'}});
        return route.fulfill({json: {cards: [{body: drafted, notes: ['依据研判按所引研判条件设置。']}]}});
      }
      const selected = endpoint.match(/^\/research-strategies\/([a-f0-9-]+)$/);
      if (selected && records.has(selected[1])) return route.fulfill({json: records.get(selected[1])});
      failures.push('Unhandled ' + method + ' ' + url.pathname); return route.abort();
    });
    await page.goto(origin + '/sn/strategies', {waitUntil: 'networkidle'});

    // md管理: export on open, check, invalidation on edit, confirmed apply, re-export.
    await page.locator('#strategy-markdown-open').click();
    const md = page.locator('#strategy-markdown-dialog'), text = md.locator('#strategy-markdown-text'), apply = md.locator('#strategy-markdown-apply');
    await md.locator('#strategy-markdown-status').filter({hasText: '已导出 1 份方案'}).waitFor();
    assert.equal(await text.inputValue(), exported);
    assert.equal(await apply.isDisabled(), true, 'Nothing can be imported before a check');
    await text.fill(exported.replace('原有触发条件', '新的触发条件'));
    await md.locator('#strategy-markdown-check').click();
    await md.locator('.strategy-markdown-rows li[data-action=update]').waitFor();
    assert.match(await md.locator('.strategy-markdown-rows').innerText(), /修改[\s\S]*合成方案甲[\s\S]*改动：适用 \/ 触发/);
    assert.match(await md.locator('.strategy-markdown-summary').innerText(), /保持不变/);
    assert.equal(await apply.isEnabled(), true);
    await text.press('End'); await text.type(' ');
    assert.equal(await apply.isDisabled(), true, 'Editing after a check requires checking again');
    await md.locator('#strategy-markdown-check').click(); await apply.waitFor({state: 'attached'});
    await page.waitForFunction(() => !document.querySelector('#strategy-markdown-apply').disabled);
    let confirmed = '';
    page.once('dialog', dialog => { confirmed = dialog.message(); dialog.accept(); });
    await apply.click();
    await md.locator('#strategy-markdown-status').filter({hasText: '已导入：新建 0 份，修改 1 份'}).waitFor();
    assert.match(confirmed, /修改 1 份方案；文档里没有的 2 份保持不变/);
    assert.equal(calls.filter(row => row === 'POST /research-strategies/markdown/apply').length, 1);
    assert.equal(calls.filter(row => row === 'GET /research-strategies/markdown').length, 2, 'The text is refreshed after importing');
    // A document with problems lists them and cannot be imported.
    await text.fill(exported.replace('等待验证', '坏方向'));
    await md.locator('#strategy-markdown-check').click();
    await md.locator('.strategy-markdown-error').filter({hasText: '方向无法识别'}).waitFor();
    assert.equal(await apply.isDisabled(), true);
    await md.locator('[data-tool-close]').first().click();

    // 文字整理: one card opens as an unsaved draft in the form with its citation and notes.
    await page.locator('#strategy-text-open').click();
    const tool = page.locator('#strategy-text-dialog'), run = tool.locator('#strategy-text-run');
    await page.waitForFunction(() => !document.querySelector('#strategy-text-model').disabled);
    assert.equal(await run.isDisabled(), true, 'An empty text cannot be organized');
    await tool.locator('#strategy-text-input').fill('库存连续两周累库就撤回；高于一万二分批减仓。');
    await run.click();
    const form = page.locator('#research-card-dialog');
    await form.waitFor({state: 'visible'});
    assert.equal(await tool.evaluate(node => node.open), false);
    assert.equal(await page.locator('#research-card-title').inputValue(), '累库撤回方案');
    assert.equal(await page.locator('#research-card-direction').inputValue(), 'bear');
    assert.equal(await page.locator('#research-card-invalidation').inputValue(), '库存连续两周累库');
    assert.equal(await page.locator('#research-card-review').inputValue(), '2030-01-31');
    assert.match(await page.locator('[data-citation-list="invalidation"]').innerText(), /库存连续两期增加/);
    assert.match(await page.locator('#research-card-form-status').innerText(), /请核对：依据研判按所引研判条件设置/);
    assert.match(await page.locator('#research-card-filename').innerText(), /由文字整理/);
    assert.equal(await page.locator('#research-sidebar-list [data-draft-mark]:not([hidden])').count(), 1, 'The draft is marked unsaved');
    assert.equal(calls.filter(row => /^(POST|PATCH) \/research-strategies$/.test(row)).length, 0, 'Organizing never saves');
    // Several cards go to the Markdown preview instead.
    await page.locator('#strategy-text-open').click();
    await tool.locator('#strategy-text-input').fill('两份方案：一份累库撤回，一份另起。');
    await run.click();
    await md.locator('#strategy-markdown-status').filter({hasText: '模型整理出 2 份方案'}).waitFor();
    assert.equal(await md.evaluate(node => node.open), true);
    assert.match(await text.inputValue(), /## 累库撤回方案[\s\S]*## 新方案/);
    await md.locator('.strategy-markdown-rows li[data-action=create]').waitFor();
    await md.locator('[data-tool-close]').first().click();
    // This fixture is a plain-HTTP origin (no crypto.randomUUID); a new card still opens.
    assert.equal(await page.evaluate(() => typeof crypto.randomUUID), 'undefined');
    await page.locator('#research-card-add').click();
    await page.waitForFunction(() => document.querySelector('#research-card-dialog-title')?.textContent === '新建策略方案');
    assert.equal(await page.locator('#research-sidebar-list [data-draft-mark]:not([hidden])').count(), 2, 'Both unsaved drafts are listed');
    // Unsaved cards can be dropped: the blank new one, then the organized one.
    const discard = page.locator('#research-card-discard'), marks = page.locator('#research-sidebar-list [data-draft-mark]:not([hidden])');
    assert.equal(await discard.innerText(), '放弃草稿');
    page.once('dialog', dialog => dialog.accept()); await discard.click();
    await page.locator('#strategy-workspace-status').filter({hasText: '已放弃未保存的方案'}).waitFor();
    assert.equal(await marks.count(), 1);
    await page.locator('#research-sidebar-list [data-strategy-select^="new:"]').first().click();
    await page.locator('#research-card-dialog').waitFor({state: 'visible'});
    assert.equal(await page.locator('#research-card-title').inputValue(), '累库撤回方案');
    page.once('dialog', dialog => dialog.dismiss()); await discard.click();
    assert.equal(await marks.count(), 1, 'Cancelling keeps the draft');
    page.once('dialog', dialog => dialog.accept()); await discard.click();
    await page.waitForFunction(() => !document.querySelector('#research-sidebar-list [data-strategy-select^="new:"]'));
    assert.equal(await marks.count(), 0);
    // Unsaved edits to a saved card are dropped and the saved version is shown again.
    await page.locator('#research-sidebar-list [data-strategy-select^="research:"]').first().click();
    await page.locator('#strategy-dynamic-preview button', {hasText: '编辑'}).first().click();
    await page.locator('#research-card-dialog').waitFor({state: 'visible'});
    assert.equal(await discard.isHidden(), true, 'Nothing to drop before an edit');
    await page.locator('#research-card-title').fill('改过但不想要的名称');
    assert.equal(await discard.innerText(), '放弃修改');
    page.once('dialog', dialog => dialog.accept()); await discard.click();
    await page.locator('#strategy-workspace-status').filter({hasText: '已放弃未保存的修改'}).waitFor();
    assert.equal(await page.locator('#strategy-dynamic-preview h2').innerText(), '合成方案甲');
    assert.equal(await marks.count(), 0);
    assert.equal(calls.filter(row => /^POST \/research-strategies$|^PATCH \/research-strategies\/[0-9a-f-]+$/.test(row)).length, 0, 'Discarding never saves');
    // Transaction-plan revisions have the same discard behavior as research cards.
    const planEntry = page.locator('[data-strategy-select="plan:' + planRecord.id + '"]');
    await planEntry.click();
    await page.locator('#strategy-dynamic-preview button', {hasText: '编辑为独立草稿'}).click();
    const planEditor = page.locator('#strategy-plan-editor'), planDiscard = page.locator('#strategy-plan-discard');
    await planEditor.waitFor({state: 'visible'});
    assert.equal(await planDiscard.isHidden(), true, 'An unchanged transaction plan has nothing to discard');
    await page.locator('#plan-edit-title').fill('尚未保存的交易计划修订');
    await page.locator('#plan-edit-thesis').fill('尚未保存的研究逻辑');
    assert.equal(await planDiscard.isVisible(), true);
    assert.equal(await planEntry.locator('[data-draft-mark]').isVisible(), true);
    page.once('dialog', dialog => dialog.dismiss()); await planDiscard.click();
    assert.equal(await page.locator('#plan-edit-title').inputValue(), '尚未保存的交易计划修订', 'Cancelling a discard keeps the revision');
    assert.equal(await planEditor.isVisible(), true);
    await planEditor.locator('[data-plan-edit-close]').first().click();
    await planEntry.click();
    await planEditor.waitFor({state: 'visible'});
    assert.equal(await page.locator('#plan-edit-thesis').inputValue(), '尚未保存的研究逻辑', 'Returning to preview keeps the revision available');
    page.once('dialog', dialog => dialog.accept()); await planDiscard.click();
    await page.locator('#strategy-workspace-status').filter({hasText: '已放弃未保存的修改'}).waitFor();
    assert.equal(await planEditor.isHidden(), true);
    assert.equal(await page.locator('#strategy-dynamic-preview h2').innerText(), planRecord.title);
    assert.match(await page.locator('#strategy-dynamic-preview').innerText(), /原有研究逻辑/);
    assert.equal(await planEntry.locator('[data-draft-mark]').isHidden(), true);
    await page.locator('#strategy-dynamic-preview button', {hasText: '编辑为独立草稿'}).click();
    assert.equal(await page.locator('#plan-edit-title').inputValue(), planRecord.title, 'Reopening starts from the saved transaction plan');
    assert.equal(await page.locator('#plan-edit-thesis').inputValue(), planRecord.plan.thesis);
    assert.equal(await planDiscard.isHidden(), true);
    assert.equal(calls.filter(row => /^(POST|PATCH|DELETE) \/strategies(?:\/|$)/.test(row)).length, 0, 'Discarding never saves or deletes a transaction plan');
    assert.deepEqual(failures, []);
    console.log('PASS real Chromium: Markdown/text tools and discarding unsaved cards or transaction-plan revisions; synthetic data only.');
  } catch (error) {
    console.error('calls:', calls.join(' | '), '\nfailures:', failures.join(' | '), '\nstatus:', page && await page.locator('#strategy-markdown-status').textContent().catch(() => ''), '\ntext status:', page && await page.locator('#strategy-text-status').textContent().catch(() => ''), '\nform:', page && await page.evaluate(() => [document.querySelector('#research-card-dialog')?.hidden, !!window.ResearchStrategyEditor, document.querySelector('#strategy-text-dialog')?.open]).catch(e => e.message));
    throw error;
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
