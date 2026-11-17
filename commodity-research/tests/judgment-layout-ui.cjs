/* Historical/current real templates, scripts and CSS; isolated synthetic data only. */
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT || 'playwright');
const root = path.resolve(__dirname, '../..'), web = path.join(root, 'commodity-research/src/tin/web');
const fonts = path.join(root, 'WeKnora/frontend/public/fonts');
const out = process.env.JUDGMENT_LAYOUT_EVIDENCE_DIR || '/tmp/judgment-layout-20261117';
const baseline = process.env.JUDGMENT_LAYOUT_BASELINE || '45e53c16';
const origin = 'http://judgment-layout.fixture', prefix = '/research/workbenches/layout-fixture';
fs.mkdirSync(out, {recursive: true});
const timeline = Array.from({length: 26}, (_, index) => ({date: new Date(Date.UTC(2032, 0, index + 1)).toISOString().slice(0, 10), value: 100 + index * 2}));
const metric = {series_id: 'synthetic-stock', name: '合成库存观测', unit: '吨', latest: 150, previous: 148,
  date: '2032-01-26', previous_date: '2032-01-25', change: 2, change_pct: 1.35,
  timeline, segments: ['12,40 220,8'], history_complete: true, boundaries: [], point_count: 26};
const dimensions = [{id: 'supply', label: '供应变化'}, {id: 'demand', label: '需求观察'}];
const dimensionItems = [
  {section: 'supporting', index: 0, title: '支持依据：合成库存数据与完整研究说明，需要保持文字自然换行。'.repeat(4), dimension: 'supply'},
  {section: 'opposing', index: 0, title: '反对依据：合成定性观察'},
  {section: 'thresholds', index: 0, title: '阈值：合成观察区间', dimension: 'demand'},
  {section: 'falsifiers', index: 0, title: '证伪：合成多方条件'}];
function fixture(restricted, tagged) {
  const data = {
    j: {id: 42, author: '合成研究者', written_at: '2032-01-26', review_due: '2032-02-02', status: '草稿', tone: '保持合成观察',
      contradiction: {text: '合成研究背景', bull: {claim: '', evidence: []}, bear: {claim: '', evidence: []}},
      thresholds: [{id: 'synthetic-range'}], falsifiers: [{id: 'synthetic-bull-falsifier'}, {id: 'synthetic-bear-falsifier'}], whitelist: ['synthetic-stock']},
    names: {'synthetic-stock': metric.name}, block: {expired_days: 0, blockers: []}, versions: [], d: '2032-01-26',
    restricted_view: restricted, restricted_data: {}, nav: 'judgment', selected_judgment_id: 42,
    radar: [{id: 'synthetic-range', name: '合成观察区间', series_id: 'synthetic-stock', type: '观察位', horizon: '短期',
      condition: '处于 120 至 160 吨之间', value: 150, unit: '吨', as_of: '2032-01-26', distance: '在区间内', progress: 75, state: '在区间内', action_hint: '继续观察'}],
    research: {counts: {available: 1, bound: 1}, threshold_related: {}, threshold_dimensions: {},
      sides: {
        bull: {claim: '合成多方观点较长：供给变化需要结合连续观测与库存变化核对，完整论点应留在同一栏并且在支持依据标题之前。'.repeat(3),
          evidence: [{desc: '合成多方依据一：库存数据保持完整图表与观测。', series_id: 'synthetic-stock', metric},
            {desc: '合成多方依据二：文字观察不应被空白图表替代。', metric: null}],
          falsifiers: [{rule: {id: 'synthetic-bull-falsifier', desc: '合成多方证伪条件', side: 'bull'}, condition: '库存高于 180 吨', state: '未触发', metric}]},
        bear: {claim: '合成空方观点：需求仍需验证。',
          evidence: [{desc: '合成空方依据一：与多方对照的库存解释。', series_id: 'synthetic-stock', metric}],
          falsifiers: [{rule: {id: 'synthetic-bear-falsifier', desc: '合成空方证伪条件', side: 'bear'}, condition: '库存低于 100 吨', state: '未触发', metric: null}]}},
      shared: [{metric, bull_texts: ['合成多方的库存解释'], bear_texts: ['合成空方的库存解释'], bull_falsifiers: [], bear_falsifiers: []}],
      pending_conditions: [{text: '合成待验证条件：后续供给变化需要人工核对。', requires_manual_observation: true}]}}
  if (tagged) {
    Object.assign(data.research.sides.bull.evidence[0], {dimension: 'supply', dimension_label: '供应变化'});
    Object.assign(data.research.sides.bear.evidence[0], {dimension: 'demand', dimension_label: '需求观察'});
    Object.assign(data.research.sides.bull.falsifiers[0], {dimension_label: '供应变化'});
    data.research.sides.bull.falsifiers[0].rule.dimension = 'supply';
    data.research.threshold_dimensions['synthetic-range'] = '需求观察';
    data.research.shared[0].bull_evidence = [{desc: '合成多方的库存解释', dimension: 'supply', dimension_label: '供应变化'}];
    data.research.shared[0].bear_evidence = [{desc: '合成空方的库存解释', dimension: 'demand', dimension_label: '需求观察'}];
  }
  return data;
}
function render(revision, data) {
  const result = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', String.raw`
import json, subprocess, sys
from types import SimpleNamespace
from jinja2 import Environment, FileSystemLoader, BaseLoader, TemplateNotFound, select_autoescape
revision, root, data = sys.argv[1], sys.argv[2], json.load(sys.stdin)
class HistoricalLoader(BaseLoader):
    def get_source(self, env, template):
        result = subprocess.run(['git', 'show', revision+':commodity-research/src/tin/web/templates/'+template], cwd=root, capture_output=True, text=True)
        if result.returncode: raise TemplateNotFound(template)
        return result.stdout, template, lambda: True
env=Environment(loader=HistoricalLoader() if revision else FileSystemLoader(root+'/commodity-research/src/tin/web/templates'), autoescape=select_autoescape(['html']))
scope=SimpleNamespace(metadata=SimpleNamespace(name='合成研究台'), scope_version=1, restricted=data['restricted_view'], view_modules=['judgments'], knowledge_base_id='fixture')
env.globals.update(research_workbench=lambda:scope, research_identity=lambda:None, research_prefix=lambda:'/research/workbenches/layout-fixture', research_url=lambda value:'/research/workbenches/layout-fixture'+value,
 research_form_url=lambda value:value, research_static_prefix=lambda:'', static_version=lambda:'layout-fixture', human_text=lambda value,*args:str(value or ''),
 indicator_name=lambda value,*args:data['names'].get(value,'合成指标'), author_label=lambda value,*args:str(value or ''), plain_item=lambda value,*args:str(value or ''),
 chart_palette=lambda:json.load(open(root+'/commodity-research/src/tin/web/static/chart-palette.json')))
env.filters['num']=lambda value,*args:str(value) if value is not None else '—'
env.filters['local']=lambda value,*args:str(value) if value is not None else '—'
data['request']=SimpleNamespace(url=SimpleNamespace(path='/sn/judgment'), query_params={})
print(env.get_template('judgment.html').render(**data))
`, revision, root], {encoding: 'utf8', input: JSON.stringify(data), maxBuffer: 8 * 1024 * 1024});
  assert.equal(result.status, 0, result.stderr); return result.stdout;
}
const historyAssets = new Map();
function asset(revision, filename) {
  if (!revision) return fs.readFileSync(path.join(web, filename));
  const key = revision + ':' + filename;
  if (!historyAssets.has(key)) {
    const result = spawnSync('git', ['show', revision + ':commodity-research/src/tin/web/' + filename], {cwd: root, maxBuffer: 16 * 1024 * 1024});
    assert.equal(result.status, 0, String(result.stderr)); historyAssets.set(key, result.stdout);
  }
  return historyAssets.get(key);
}
async function open(browser, revision, restricted, tagged, width) {
  const page = await browser.newPage({viewport: {width, height: 960}, reducedMotion: 'reduce', colorScheme: 'light'});
  const errors = [], calls = [], html = render(revision, fixture(restricted, tagged));
  page.setDefaultTimeout(10000); page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', async route => {
    const url = new URL(route.request().url()), p = url.pathname;
    if (url.origin !== origin) { errors.push('Unexpected origin ' + url.origin); return route.abort(); }
    if (p === '/sn/judgment') return route.fulfill({contentType: 'text/html; charset=utf-8', body: html});
    if (p.startsWith('/fonts/')) {
      const file = path.resolve(fonts, p.slice(7));
      assert.ok(file.startsWith(fonts + path.sep));
      return route.fulfill({contentType: file.endsWith('.css') ? 'text/css' : 'font/woff2', body: fs.readFileSync(file)});
    }
    if (p.startsWith('/static/')) return route.fulfill({contentType: p.endsWith('.css') ? 'text/css' : 'application/javascript', body: asset(revision, p.slice(1))});
    calls.push(p);
    if (p === prefix + '/api/sn/judgment/library-references') return route.fulfill({json: {references: [], related: {}, rules: [], as_of: '2032-01-26'}});
    if (p === prefix + '/api/sn/judgment/manage') return route.fulfill({json: {variety: 'ZZ', can_write: true,
      current: {id: 42, version: 2, status: '草稿', markdown: '# 合成研究逻辑', source_text: '合成研究原文', dimension_items: dimensionItems,
        chart_targets: [{target: {kind: 'judgment'}, label: '整条研判'}]}, latest: {version: 2}, versions: [], research_dimensions: dimensions}});
    if (p === prefix + '/api/sn/judgment/models') return route.fulfill({json: {models: [{id: 'fixture-model', name: '合成补标模型'}]}});
    if (p === prefix + '/api/sn/judgment/panels') return route.fulfill({json: {items: [], total: 0, has_more: false}});
    if (p === prefix + '/api/sn/judgment/jobs/latest') return route.fulfill({json: {job: null}});
    errors.push('Unhandled fixture request ' + p); return route.abort();
  });
  await page.goto(origin + '/sn/judgment', {waitUntil: 'networkidle'});
  await page.evaluate(() => document.fonts.ready);
  await page.waitForFunction(() => [...document.querySelectorAll('[data-metric-trend]')].every(svg => Math.abs(svg.viewBox.baseVal.width - svg.getBoundingClientRect().width) < 1));
  return {page, errors, calls};
}
async function inspect(page) {
  return page.evaluate(() => {
    const box = node => {const r = node.getBoundingClientRect(); return {x: r.x, y: r.y, width: r.width, height: r.height, bottom: r.bottom, right: r.right};};
    const research = document.querySelector('.judgment-research').cloneNode(true);
    research.querySelectorAll('.judgment-dimension-tag').forEach(node => node.remove());
    research.querySelectorAll('script, svg, [data-chart-tooltip]').forEach(node => node.remove());
    const walk = node => node.nodeType === Node.TEXT_NODE ? node.textContent.replace(/\s+/g, ' ').trim() : node.nodeType === Node.ELEMENT_NODE ? {
      tag: node.tagName, attributes: [...node.attributes].filter(attr => attr.name !== 'style').map(attr => [attr.name, attr.value]),
      children: [...node.childNodes].map(walk).filter(Boolean)} : null;
    return {viewport: innerWidth, scroll: document.documentElement.scrollWidth, structure: walk(research), researchBounds: box(document.querySelector('.judgment-research')),
      headings: [...document.querySelectorAll('.judgment-research h1,.judgment-research h2,.judgment-hypothesis>h3,.judgment-hypothesis>h4')].map(node => node.textContent),
      geometry: [...document.querySelectorAll('.judgment-research-header,.judgment-research-summary,.judgment-research-section,.judgment-hypotheses,.judgment-hypothesis,.judgment-hypothesis>h3,.judgment-hypothesis-claim,.judgment-hypothesis>h4,[data-research-item],#judgment-thresholds tr')].map(node => ({selector: node.className, ...box(node)})),
      columns: [...document.querySelectorAll('.judgment-hypotheses>.judgment-hypothesis')].map(node => ({...box(node), title: box(node.querySelector('h3')), claim: box(node.querySelector('.judgment-hypothesis-claim')), support: box(node.querySelector('.judgment-support-heading')), evidence: [...node.querySelectorAll('[data-research-item="evidence"]')].map(box), falsifier: box(node.querySelector('.judgment-falsifier-section'))})),
      badgeCount: document.querySelectorAll('.judgment-dimension-tag').length,
      grouped: document.querySelectorAll('[data-dimension-group],.judgment-dimension-group').length,
      thresholdHeaders: [...document.querySelectorAll('#judgment-thresholds thead th')].map(node => node.textContent.trim()),
      bodyText: document.querySelector('.judgment-research').textContent};
  });
}
function validate(layout, width) {
  assert.ok(layout.scroll <= width, `${width}px: no page overflow`);
  assert.equal(layout.columns.length, 2); assert.equal(layout.grouped, 0); assert.equal(layout.thresholdHeaders.length, 7);
  assert.ok(!layout.bodyText.includes('未标注')); assert.ok(!layout.thresholdHeaders.includes('维度'));
  for (const column of layout.columns) {
    assert.ok(column.claim.y >= column.title.bottom - 1, `${width}px: claim follows title`);
    assert.ok(column.support.y >= column.claim.bottom - 1, `${width}px: support heading follows complete claim`);
    assert.ok(column.evidence[0].y >= column.support.bottom - 1, `${width}px: evidence follows support heading`);
    assert.ok(column.falsifier.y >= column.evidence.at(-1).bottom - 1, `${width}px: falsifier heading follows evidence`);
  }
  const [bull, bear] = layout.columns;
  if (width > 760) {
    assert.ok(bear.x >= bull.right - 1, 'Desktop has separate opposing columns');
    for (const key of ['title', 'claim', 'support', 'falsifier']) assert.ok(Math.abs(bull[key].y - bear[key].y) < 1, `${key}: aligned desktop rows`);
  } else assert.ok(bear.y >= bull.bottom - 1, 'Mobile stacks opposing columns');
}
function sameGeometry(before, after, message) {
  assert.equal(after.geometry.length, before.geometry.length, message + ': node count');
  before.geometry.forEach((item, index) => {
    for (const key of ['x', 'y', 'width', 'height']) assert.ok(Math.abs(after.geometry[index][key] - item[key]) < 1,
      `${message}: node ${index} ${item.selector} ${key}, baseline ${item[key]}, current ${after.geometry[index][key]}`);
  });
}
function identicalPixels(before, after) {
  const result = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', String.raw`
from PIL import Image, ImageChops
import sys
before, after = (Image.open(name).convert('RGB') for name in sys.argv[1:])
assert before.size == after.size, (before.size, after.size)
assert ImageChops.difference(before, after).getbbox() is None, 'Research region pixels differ'
`, before, after], {encoding: 'utf8'});
  assert.equal(result.status, 0, result.stderr);
}
function cropResearch(input, output, bounds) {
  const result = spawnSync(process.env.RESEARCH_PYTHON || 'python', ['-c', String.raw`
from PIL import Image
import json, sys
bounds=json.loads(sys.argv[3])
Image.open(sys.argv[1]).crop(tuple(round(bounds[key]) for key in ('x','y','right','bottom'))).save(sys.argv[2])
`, input, output, JSON.stringify(bounds)], {encoding: 'utf8'});
  assert.equal(result.status, 0, result.stderr);
}
(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
    ...(process.env.SHEET_REVIEW_BROWSER ? {executablePath: process.env.SHEET_REVIEW_BROWSER} : {})});
  const report = {baseline, passed: false, checks: [], screenshots: [], layouts: []};
  try {
    for (const restricted of [false, true]) for (const width of [1280, 390]) {
      const name = `${restricted ? 'restricted' : 'regular'}-${width}`;
      const old = await open(browser, baseline, restricted, false, width), oldLayout = await inspect(old.page);
      validate(oldLayout, width); assert.deepEqual(old.errors, []);
      const oldPath = path.join(out, `baseline-${name}.png`); await old.page.screenshot({path: oldPath, fullPage: true}); report.screenshots.push(oldPath);
      const oldResearchPath = path.join(out, `baseline-${name}-research.png`); cropResearch(oldPath, oldResearchPath, oldLayout.researchBounds); report.screenshots.push(oldResearchPath);
      if (process.argv.includes('--baseline-only')) {await old.page.close(); continue;}
      const current = await open(browser, '', restricted, false, width), currentLayout = await inspect(current.page);
      validate(currentLayout, width); assert.equal(currentLayout.badgeCount, 0); assert.deepEqual(current.errors, []);
      assert.deepEqual(currentLayout.structure, oldLayout.structure, `${name}: original structure and content restored`);
      sameGeometry(oldLayout, currentLayout, name + ': untagged layout');
      const currentPath = path.join(out, `restored-${name}.png`); await current.page.screenshot({path: currentPath, fullPage: true}); report.screenshots.push(currentPath);
      const currentResearchPath = path.join(out, `restored-${name}-research.png`); cropResearch(currentPath, currentResearchPath, currentLayout.researchBounds); report.screenshots.push(currentResearchPath);
      identicalPixels(oldResearchPath, currentResearchPath);
      const tagged = await open(browser, '', restricted, true, width), taggedLayout = await inspect(tagged.page);
      validate(taggedLayout, width); assert.deepEqual(tagged.errors, []);
      assert.ok(taggedLayout.badgeCount >= 4, 'Explicit evidence and rule tags are visible');
      assert.deepEqual(taggedLayout.structure, currentLayout.structure, `${name}: labels add only inline badges`);
      assert.deepEqual(taggedLayout.headings, currentLayout.headings, `${name}: section order and titles unchanged`);
      const badgeStyles = await tagged.page.locator('.judgment-dimension-tag').evaluateAll(nodes => nodes.map(node => {
        const style = getComputedStyle(node); return {fontSize: parseFloat(style.fontSize), text: node.textContent.trim(), color: style.color};}));
      assert.ok(badgeStyles.every(item => item.fontSize <= 11 && ['供应变化', '需求观察'].includes(item.text)), 'Badges use small dimension names');
      const taggedPath = path.join(out, `tagged-${name}.png`); await tagged.page.screenshot({path: taggedPath, fullPage: true}); report.screenshots.push(taggedPath);
      // Hiding the only added visuals restores every geometric measurement.
      await tagged.page.addStyleTag({content: '.judgment-dimension-tag{display:none!important}'});
      sameGeometry(currentLayout, await inspect(tagged.page), name + ': labels hidden');
      if (restricted) assert.equal(await current.page.locator('#judgment-manager,[data-judgment-library-target]').count(), 0);
      else {
        await current.page.locator('[data-open-logic]').click();
        await current.page.locator('#logic-dimension-editor').waitFor({state: 'visible'});
        await current.page.locator('#logic-dimension-model option').filter({hasText: '合成补标模型'}).waitFor({state: 'attached'});
        const manager = await current.page.evaluate(() => {
          const dialog = document.querySelector('#judgment-manager'), dimension = dialog.querySelector('#logic-dimension-editor'), chart = dialog.querySelector('#logic-chart-editor');
          const b = node => {const r = node.getBoundingClientRect(); return {left: r.left, right: r.right, top: r.top, bottom: r.bottom};};
          return {viewport: innerWidth, ...b(dialog), clientWidth: dialog.clientWidth, scrollWidth: dialog.scrollWidth,
            dimension: b(dimension), chart: b(chart), controls: [...dimension.querySelectorAll('select,button')].map(b),
            rows: [...dimension.querySelectorAll('.logic-dimension-row')].map(node => ({title: b(node.querySelector('.logic-dimension-title')), select: b(node.querySelector('select'))}))};
        });
        assert.ok(manager.left >= 0 && manager.right <= width, `${name}: manager remains in viewport`);
        assert.ok(manager.scrollWidth <= manager.clientWidth + 1, `${name}: manager has no horizontal overflow`);
        assert.ok(manager.chart.top >= manager.dimension.bottom - 1, `${name}: dimension editor precedes chart editor without overlap`);
        assert.ok(manager.controls.every(item => item.left >= manager.left && item.right <= manager.right), `${name}: dimension controls fit dialog`);
        for (const row of manager.rows) assert.ok(row.select.left >= row.title.right - 1 || row.select.top >= row.title.bottom - 1, `${name}: dimension title and select do not overlap`);
        await current.page.locator('#logic-dimension-editor').scrollIntoViewIfNeeded();
        const managerPath = path.join(out, `manager-${width}.png`); await current.page.screenshot({path: managerPath, fullPage: true}); report.screenshots.push(managerPath);
        report.layouts.push({name, manager});
      }
      report.layouts.push({name, baseline: oldLayout.geometry, restored: currentLayout.geometry, taggedBadges: taggedLayout.badgeCount});
      report.checks.push(`${name}: original DOM/geometry and identical research-region pixels, no heading overlap, seven threshold columns, inline tags only, no untagged placeholders`);
      await Promise.all([old.page.close(), current.page.close(), tagged.page.close()]);
    }
    report.passed = true;
    console.log(`PASS judgment-layout-ui: ${report.checks.length} full-page comparisons; screenshots and report: ${out}`);
  } catch (error) {report.error = String(error.stack || error); throw error;}
  finally {fs.writeFileSync(path.join(out, 'layout-report.json'), JSON.stringify(report, null, 2)); await browser.close();}
})().catch(error => {console.error(error); process.exitCode = 1;});
