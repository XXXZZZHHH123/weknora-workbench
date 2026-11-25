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

test('generated recommendations fill the canvas draft, show scope and clear on save or individual withdrawal', async () => {
  const node = (id, label) => ({id, label, x: 0, y: 0, w: 140, h: 66, binding: {kind: 'none'}});
  const draft = {name: 'New diagram', nodes: [node('keep', 'Recorded volume'), node('undo', 'Current stock'),
    node('ambiguous', 'Total supply')], edges: []};
  const choice = (sid, name, reason, dimensions) => ({series_id: sid, name, reason, dimensions});
  const ranked = (candidate, alternatives = []) => ({recommended: candidate,
    recommendations: candidate ? [candidate, ...alternatives] : alternatives, ambiguous: !candidate});
  const preview = {markdown: '# New diagram', version: 'source-version', recommendation_version: 'catalog-version',
    indicator_recommendations: {
      keep: ranked(choice('series-1', 'Recorded volume', '<verified measurement>')),
      undo: ranked(choice('series-2', 'Inventory', 'Same stock definition')),
      ambiguous: ranked(null, [choice('series-3', 'Supply definition', 'Geographic scope differs', {Region: 'North'}),
        choice('series-4', 'Supply definition', 'Alternate coverage', {Region: 'South'})]),
    }};
  const nodes = new Map(['dg-recommend', 'dg-recommend-status', 'dg-recommend-restore', 'dg-save', 'dg-md-msg',
    'dg-md-text', 'dg-md-name'].map(key => [key, {textContent: '', value: '', dataset: {},
      classList: {toggle() {}}, addEventListener() {}}]));
  nodes.get('dg-md-text').value = preview.markdown;
  const saves = [];
  const window = {
    dgGetGenerationPreview: () => preview, dgGeneratedJobId: () => id,
    dgCancelGeneration: () => 1, dgGenerationIsCurrent: value => value === 1,
    ResearchAnalysisJobs: {
      create: () => ({busy: false, pause() {}, detach() {}}),
      message: (element, text) => {element.textContent = text;},
    },
  };
  const context = vm.createContext({window,
    document: {getElementById: key => nodes.get(key), addEventListener() {}},
    dgGeneratorRequest: async (path, options) => {
      assert.equal(path, '/preview');
      assert.equal(JSON.parse(options.body).markdown, preview.markdown);
      return structuredClone(draft);
    },
    researchFetch: async (url, options) => {
      assert.match(url, /\/jobs\/synthetic-diagram-job-0001\/apply$/);
      saves.push(JSON.parse(options.body));
      return {ok: true, json: async () => ({saved: {id: 2, name: 'New diagram'}})};
    },
    location: {href: 'http://fixture.invalid/sn/diagram'}, history: {replaceState() {}},
    showToast() {}, toggleDrawer() {}, URL,
  });
  for (const filename of ['diagram.js', 'diagram-indicator-recommendations.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../src/tin/web/static', filename), 'utf8'), context);
  }
  vm.runInContext(`
    dg.layout = {__id: 1, __name: 'Saved diagram', nodes: [], edges: []};
    dgRender = dgFit = dgLoad = () => {};
    this.canvas = {open: () => dgImportMarkdown(false), save: dgSave,
      binding: id => dg.layout.nodes.find(n => n.id === id).binding,
      draw: id => dgNode(dg.layout.nodes.find(n => n.id === id)),
      withdraw: id => { dg.active = id; dgUnbind(); }};
  `, context);
  await context.canvas.open();
  assert.equal(nodes.get('dg-md-msg').textContent, '正在打开画布草稿…');
  assert.equal(saves.length, 0);
  assert.equal(context.canvas.binding('keep').series_id, 'series-1');
  assert.match(context.canvas.draw('keep'), /dg-recommendation-pending.*[\s\S]*待核对/);
  assert.match(context.canvas.draw('keep'), /&lt;verified measurement&gt;/);
  assert.equal(context.canvas.binding('ambiguous').kind, 'none');
  assert.match(context.canvas.draw('ambiguous'), /节点口径待明确[\s\S]*Region：North[\s\S]*Region：South/);
  context.canvas.withdraw('undo');
  assert.equal(context.canvas.binding('undo').kind, 'none');
  assert.doesNotMatch(context.canvas.draw('undo'), /dg-recommendation-pending/);
  await context.canvas.save();
  assert.equal(saves.length, 1);
  assert.equal(saves[0].id, null);
  assert.equal(saves[0].recommendation_version, 'catalog-version');
  assert.equal(saves[0].layout.nodes[0].binding.series_id, 'series-1');
  assert.equal(saves[0].layout.nodes[1].binding.kind, 'none');
  assert.equal(saves[0].layout.nodes[2].binding.kind, 'none');
  assert.doesNotMatch(context.canvas.draw('keep'), /dg-recommendation-pending|待核对/);
});
