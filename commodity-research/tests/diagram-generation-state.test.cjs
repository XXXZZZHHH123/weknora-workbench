const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const test = require('node:test'), vm = require('node:vm');
const id = 'synthetic-diagram-job-0001';
function fixture(job) {
  const nodes = new Map(), requests = [], timers = [];
  const get = name => {
    if (!nodes.has(name)) nodes.set(name, {textContent: '', value: '', disabled: false, hidden: true,
      options: [], selectedOptions: [], classList: {contains: () => false}, addEventListener() {}});
    return nodes.get(name);
  };
  get('dg-md-text').value = 'User-authored Markdown'; get('dg-ai-model').value = 'configured';
  const window = {RESEARCH_CONTEXT: {workbench: 'synthetic'}, addEventListener() {}};
  const context = vm.createContext({window, parent: window, location: {href: 'http://fixture.invalid/sn/diagram', origin: 'http://fixture.invalid'},
    document: {getElementById: get, readyState: 'loading', addEventListener() {}},
    MutationObserver: class {observe() {}}, AbortController, URL, clearTimeout() {}, setTimeout(fn) {timers.push(fn); return timers.length;},
    researchFetch: async (url, options) => {requests.push({url, options}); return {ok: true, json: async () => job};}});
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../src/tin/web/static/diagram-generation.js'), 'utf8') +
    `\nthis.view={check:dgCheckJob,follow:async identifier=>{dgGeneratorJob={id:identifier};return dgFollowJob(identifier)},active:dgJobActive};`, context);
  return {view: context.view, nodes, requests, timers};
}
test('superseded diagram tasks terminate naturally without loading old results or touching editor input', async () => {
  const env = fixture({id, purpose: 'diagram', workbench_id: 'synthetic', state: 'superseded', result_available: false});
  assert.doesNotThrow(() => env.view.check({id, purpose: 'diagram', workbench_id: 'synthetic', state: 'superseded'}));
  await env.view.follow(id);
  assert.match(env.nodes.get('dg-ai-status').textContent, /已有更新.*结果/);
  assert.doesNotMatch(env.nodes.get('dg-ai-status').textContent, /范围.*改变|权限/);
  assert.equal(env.nodes.get('dg-md-text').value, 'User-authored Markdown');
  assert.equal(env.requests.length, 1); assert.equal(env.timers.length, 0); assert.equal(env.view.active(), false);
});
test('same-workbench responses for a different task cannot replace the task being followed', async () => {
  const env = fixture({id: 'synthetic-diagram-job-0002', purpose: 'diagram', workbench_id: 'synthetic', state: 'superseded'});
  await env.view.follow(id);
  assert.match(env.nodes.get('dg-ai-status').textContent, /当前.*任务.*不一致/);
  assert.equal(env.nodes.get('dg-md-text').value, 'User-authored Markdown'); assert.equal(env.requests.length, 1);
});
test('a real purpose or workbench mismatch remains rejected even for a superseded result', () => {
  const env = fixture(null);
  for (const job of [{id, purpose: 'judgments', workbench_id: 'synthetic', state: 'superseded'},
    {id, purpose: 'diagram', workbench_id: 'another', state: 'superseded'}]) assert.throws(() => env.view.check(job), /范围/);
});
