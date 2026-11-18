const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.RESEARCH_PLAYWRIGHT);

const markup = JSON.parse(fs.readFileSync(0, 'utf8'));
const prefix = '/research/workbenches/fixture';
const id = 'setup-fixture-00001';
const source = {kind: 'knowledge_file', knowledge_id: 'weekly', source_binding_id: 'binding', file_version: 'a'.repeat(64)};
const job = {id, purpose: 'workbench_setup', workbench_id: 'fixture', state: 'running', phase: 'dimensions'};
const origin = 'http://localhost';
const jobs = '/api/sn/workbench-setup/jobs';

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.SHEET_REVIEW_BROWSER,
    args: ['--no-sandbox', '--disable-gpu', '--renderer-process-limit=1']});
  try {
    const page = await browser.newPage();
    let actor = 'first', finished = false, submitted = false;
    const searched = [];
    function shell(content) {
      const bootstrap = `<script>window.RESEARCH_PREFIX=${JSON.stringify(prefix)};
        window.RESEARCH_CONTEXT={tenant:1,user:${JSON.stringify(actor)},workbench:'fixture'};
        window.researchFetch = window.fetch.bind(window);</script>`;
      return {contentType: 'text/html; charset=utf-8', body: '<!doctype html>' + bootstrap + content};
    }
    await page.route('**/*', async route => {
      const request = route.request();
      const url = new URL(request.url());
      const endpoint = url.pathname.slice(prefix.length);
      const reply = (json, status = 200) => route.fulfill({status, json});
      if (url.pathname === prefix + '/') return route.fulfill(shell(markup.home));
      if (url.pathname.startsWith('/static/')) {
        const file = path.join(__dirname, '../src/tin/web/static', path.basename(url.pathname));
        return route.fulfill({path: file});
      }
      if (endpoint === jobs + '/latest') return reply({job: null});
      if (endpoint === '/api/sn/strategies/models') return reply({models: [{id: 'fixture-model', name: '模型'}]});
      if (endpoint === '/api/sn/charts/import/report/files') {
        searched.push(url.searchParams.get('search'));
        return reply({items: [{title: '<往期周报>', citation: source}], next_offset: null});
      }
      if (endpoint === jobs && request.method() === 'POST') {
        assert.match(request.headers()['idempotency-key'], /^[a-f0-9]{32}$/);
        assert.deepEqual(request.postDataJSON(), {source, model_id: 'fixture-model', parts: ['dimensions', 'charts', 'layout', 'judgment']});
        submitted = true;
        return reply(job, 202);
      }
      if (endpoint === `${jobs}/${id}`) return reply(finished ? {...job, state: 'succeeded', result_available: true} : job);
      if (endpoint === `${jobs}/${id}/result`) return reply({report: {name: '<往期周报>'}, items: [
        {part: 'dimensions', state: 'succeeded', counts: {dimensions: 8, sections: 24}, data: {dimensions: []}},
        {part: 'charts', state: 'succeeded', counts: {ready: 8, needs_mapping: 12, failed: 3}, data: {job_id: 'charts'}},
        {part: 'layout', state: 'failed', message: '证据读取失败'},
        {part: 'judgment', state: 'succeeded', counts: {drafts: 1}, review_available: true},
      ]});
      if (endpoint === '/sn/dimensions') return route.fulfill(shell(markup.dimensions));
      if (endpoint === '/api/sn/dimensions') return reply({can_write: true, revision: 0, dimensions: []});
      if (endpoint === '/api/sn/dimensions/data') return reply({revision: 0, dimensions: []});
      throw new Error('Unexpected request ' + request.url());
    });
    await page.goto(origin + prefix + '/');
    const guide = page.locator('#workbench-setup-guide');
    await guide.waitFor();
    await guide.getByRole('button', {name: '选择周报'}).click();
    const dialog = page.getByRole('dialog');
    await dialog.waitFor();
    assert.equal(await dialog.locator('input[name="part"]:checked').count(), 9);
    assert.equal(await dialog.getByText('来自所选周报的当期观点，只生成草稿，需改成当前判断后再保存').count(), 3);
    await dialog.getByLabel('查找周报').fill('周报');
    await page.waitForRequest(request => new URL(request.url()).searchParams.get('search') === '周报');
    await dialog.getByLabel('已授权周报').selectOption({label: '<往期周报>'});
    await dialog.getByLabel('平台模型').selectOption('fixture-model');
    const selectedParts = new Set(['dimensions', 'charts', 'layout', 'judgment']);
    for (const part of await dialog.locator('input[name="part"]').all()) {
      if (selectedParts.has(await part.getAttribute('value'))) continue;
      await part.uncheck();
    }
    await dialog.getByRole('button', {name: '开始建立'}).click();
    await dialog.getByText('正在归纳维度，可关闭窗口。').waitFor();
    assert.equal(await dialog.locator('#workbench-setup-status').getAttribute('data-busy'), 'true');
    await dialog.getByRole('button', {name: '关闭', exact: true}).click();
    await page.getByRole('button', {name: '查看任务'}).waitFor();
    assert.ok(submitted);
    assert.ok(searched.includes('周报'));
    finished = true;
    await page.goto(origin + prefix + '/?analysis_job=' + id);
    await dialog.getByRole('heading', {name: '核对清单'}).waitFor();
    assert.match(await dialog.locator('#workbench-setup-review-items').innerText(), /维度 8 个，环节 24 个/);
    assert.match(await dialog.locator('#workbench-setup-review-items').innerText(), /可导入 8 张，需补映射 12 张，失败 3 项/);
    assert.match(await dialog.locator('#workbench-setup-review-items').innerText(), /证据读取失败/);
    assert.equal(await dialog.getByRole('link', {name: /^去核对/}).count(), 3);
    assert.match(await dialog.getByRole('link', {name: '去核对研判草稿'}).getAttribute('href'), /\/sn\/judgment\?.*setup_part=judgment/);
    await dialog.getByRole('link', {name: '去核对研究维度'}).click();
    const review = new URL(page.url());
    assert.equal(review.pathname, prefix + '/sn/dimensions');
    assert.equal(review.searchParams.get('setup_job'), id);
    assert.equal(review.searchParams.get('setup_part'), 'dimensions');
    await page.goto(origin + prefix + '/sn/dimensions');
    const dimensionsEntry = page.getByRole('button', {name: /^从周报建立整个工作台/});
    await dimensionsEntry.click();
    await dialog.getByRole('heading', {name: '从周报建立工作台'}).waitFor();

    await page.goto(origin + prefix + '/');
    await guide.getByRole('button', {name: '关闭周报建立引导'}).click();
    await page.reload();
    assert.equal(await guide.isVisible(), false);
    actor = 'second';
    await page.reload();
    await guide.waitFor();
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
