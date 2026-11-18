const assert = require('node:assert/strict');
const read = require('node:fs').readFileSync;
const {join} = require('node:path');
const test = require('node:test');
const {runInNewContext} = require('node:vm');

const script = read(join(__dirname, '../src/tin/web/static/analysis-jobs.js'), 'utf8');
const purposes = ['monitor_draft', 'report_dimension_draft', 'report_chart_import', 'dimension_inference',
  'dimension_fields', 'focus_suggestions', 'judgment_dimensions', 'strategy_text', 'strategy_generation'];

function fixture(purpose) {
  const id = 'draft-job-abcdefghijklmn', origin = 'https://synthetic.invalid';
  let content = 'researcher input', open = true, state = 'running', timer;
  const saved = [], requests = [], announced = [];
  const job = () => ({id, purpose, workbench_id: 'test-workbench', state, phase_label: '正在核对候选'});
  const button = {}, status = {textContent: '', dataset: {}};
  const window = {RESEARCH_CONTEXT: {workbench: 'test-workbench'}, crypto: require('node:crypto').webcrypto};
  runInNewContext(script, {URL, AbortController, Uint8Array, window,
    location: {href: origin + '/?analysis_job=' + id, origin},
    parent: {postMessage: (...args) => announced.push(args)},
    clearTimeout: () => { timer = undefined; },
    setTimeout: callback => { timer = callback; },
    researchFetch: async (path, options) => {
      requests.push([path, options?.method || 'GET']);
      return {ok: true, json: async () => path.endsWith('/result') ? {draft: 'generated', job: job()} : job()};
    }
  });
  const jobs = window.ResearchAnalysisJobs;
  const controller = jobs.create({purpose, button, status, snapshot: () => content, isOpen: () => open,
    present: (result, {current}) => { if (current()) { saved.push(result.draft); content = result.draft; } }});
  return {id, button, status, jobs, controller, saved, requests, announced,
    setContent: value => { content = value; },
    getContent: () => content,
    close: () => { open = false; controller.pause(); },
    reopen: () => { open = true; },
    succeed: () => { state = 'succeeded'; },
    tick: async () => { const next = timer; timer = undefined; await next?.(); }};
}

for (const purpose of purposes) {
  test(purpose + ' preserves an edited form and only retrieves private results', async () => {
    const f = fixture(purpose);
    await f.controller.submit('/api/sn/synthetic', {});
    assert.equal(f.button.disabled, true);
    assert.equal(f.status.dataset.analysisBusy, 'true');
    assert.match(f.status.textContent, /正在核对候选，可关闭窗口/);
    assert.equal(f.announced[0][0].type, 'research-analysis-job-started');
    assert.equal(f.announced[0][0].purpose, purpose);
    await f.controller.submit('/api/sn/synthetic', {});
    f.setContent('edited while model runs');
    f.succeed();
    await f.tick();
    assert.equal(f.getContent(), 'edited while model runs');
    assert.match(f.status.textContent, /编辑内容已改动，未覆盖/);
    assert.equal(f.status.dataset.analysisBusy, 'false');
    assert.equal(f.button.disabled, false);
    assert.deepEqual(f.saved, []);
    assert.equal(f.requests.filter(([, method]) => method === 'POST').length, 1);
  });

  test(purpose + ' continues after the editor closes and restores on demand', async () => {
    const f = fixture(purpose);
    await f.controller.submit('/api/sn/synthetic', {});
    f.close();
    f.succeed();
    await f.tick();
    assert.deepEqual(f.saved, []);
    f.reopen();
    await f.controller.discover();
    assert.equal(f.getContent(), 'generated');
    assert.deepEqual(f.saved, ['generated']);
    const requested = await f.jobs.requested(purpose);
    assert.equal(requested.id, f.id);
    assert.equal(await f.jobs.requested('different-purpose'), null);
    assert.equal(f.requests.filter(([, method]) => method === 'POST').length, 1);
  });
}

test('a closed editor cannot change another controller’s shared button and status', async () => {
  const button = {}, status = {textContent: '', dataset: {}};
  const window = {RESEARCH_CONTEXT: {workbench: 'test-workbench'}, crypto: require('node:crypto').webcrypto};
  let currentPurpose = 'dimension_inference', finish;
  runInNewContext(script, {window, parent: window, URL, AbortController, Uint8Array, setTimeout, clearTimeout,
    location: {origin: 'https://synthetic.invalid'}, researchFetch: () => new Promise(resolve => { finish = resolve; })});
  const first = window.ResearchAnalysisJobs.create({purpose: currentPurpose, button, status, snapshot: () => [],
    isOpen: () => currentPurpose === 'dimension_inference', disabled: () => currentPurpose !== 'dimension_inference'});
  const pending = first.submit('/api/sn/synthetic', {});
  assert.equal(button.disabled, true);
  currentPurpose = 'dimension_fields';
  button.disabled = false;
  status.textContent = '读取指标模型';
  finish({ok: false, status: 503, json: async () => ({detail: '归纳任务提交失败'})});
  await pending;
  assert.equal(button.disabled, false);
  assert.equal(status.textContent, '读取指标模型');
});

test('presenting a result can close the source dialog without leaving the controller busy', async () => {
  const window = {RESEARCH_CONTEXT: {workbench: 'test-workbench'}};
  const job = {id: 'draft-job-abcdefghijklmn', purpose: 'strategy_text', state: 'succeeded', workbench_id: 'test-workbench'};
  let open = true;
  runInNewContext(script, {window, parent: window, URL, AbortController, setTimeout, clearTimeout,
    researchFetch: async path => ({ok: true, json: async () => path.endsWith('/result') ? {job} : job})});
  const controller = window.ResearchAnalysisJobs.create({purpose: job.purpose, snapshot: () => '',
    status: {textContent: '', dataset: {}}, isOpen: () => open, present: () => { open = false; }});
  await controller.restore(job.id);
  assert.equal(open, false);
  assert.equal(controller.busy, false);
});
