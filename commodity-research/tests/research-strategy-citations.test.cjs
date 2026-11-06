const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

class Node {
  constructor() { this.value = ''; this.children = []; this.dataset = {}; this.listeners = {}; this.hidden = false; this.classList = {toggle() {}}; }
  append(...nodes) { this.children.push(...nodes); }
  prepend(...nodes) { this.children.unshift(...nodes); }
  replaceChildren(...nodes) { this.children = [...nodes]; }
  get firstChild() { return this.children[0]; }
  addEventListener(name, callback) { this.listeners[name] = callback; }
  setAttribute() {}
  removeAttribute() {}
  querySelectorAll() { return []; }
  querySelector() { return null; }
  closest() { return {open: false}; }
  focus() {}
}
function fixture() {
  const nodes = new Map();
  const get = id => { if (!nodes.has(id)) nodes.set(id, new Node()); return nodes.get(id); };
  get('strategy-workspace-data').textContent = JSON.stringify({plans: [], selected: null, can_write: true});
  get('research-card-data').textContent = JSON.stringify({items: [], enabled: true});
  get('research-card-filename');
  const document = {getElementById: id => id === 'strategy-plan-form' ? null : get(id),
    createElement: () => new Node(), querySelectorAll: () => [], addEventListener() {}};
  const window = {addEventListener() {}, confirm: () => true, location: {origin: 'https://fixture.invalid'}, RESEARCH_PREFIX: '/research/workbenches/fixture'};
  const context = vm.createContext({window, document, URL, URLSearchParams, AbortController,
    location: {href: 'https://fixture.invalid/sn/strategies', origin: 'https://fixture.invalid'},
    setTimeout, clearTimeout, structuredClone, console});
  let code = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/research-strategy.js'), 'utf8');
  code = code.replace('  select(selected);\n})();', `  window.fixture = {fillCard, readCardForm,
    parseImport: typeof parseImport === 'function' ? parseImport : null,
    citationIdentity: typeof citationIdentity === 'function' ? citationIdentity : null,
    applyImport: typeof applyImport === 'function' ? applyImport : null,
    observationText: typeof observationText === 'function' ? observationText : null,
    citationLink: typeof citationLink === 'function' ? citationLink : null};\n})();`);
  vm.runInContext(code, context);
  return {view: window.fixture, nodes, window};
}
const citation = (field = 'trigger') => ({kind: 'indicator', field, series_id: 'synthetic-series', source_version: 'a'.repeat(64),
  snapshot: {title: '真实指标名称', subtitle: 'kg · Synthetic source'}});
const card = () => ({format: 'research-strategy-card.v1', title: 'Synthetic plan', direction: 'observe',
  thesis: '范围', watch_for: '核对', trigger: '触发文字', response_action: '应对文字', invalidation: '撤回文字',
  review_date: '2030-01-02', original_text: ''});
const plain = value => JSON.parse(JSON.stringify(value));

test('editing and draft roundtrip retain independent field citations without inserting IDs into prose', () => {
  const {view} = fixture(), original = {...card(), citations: [citation(), citation('invalidation')]};
  view.fillCard({card: original, source_ids: []});
  const restored = view.readCardForm();
  assert(Array.isArray(restored.card.citations), 'Form roundtrip keeps typed citations');
  assert.deepEqual(plain(restored.card.citations), original.citations);
  for (const name of ['thesis', 'watch_for', 'trigger', 'response_action', 'invalidation', 'original_text'])
    assert.equal(restored.card[name], original[name]);
});
test('JSON import accepts old cards and all typed citation identities but strips untrusted display snapshots', () => {
  const {view} = fixture(); assert.equal(typeof view.parseImport, 'function');
  const legacy = view.parseImport(JSON.stringify({card: card(), source_ids: ['original-source']}), 'plan.json');
  assert.deepEqual(plain(legacy.body.card), card());
  const entries = [citation(), {kind: 'judgment_condition', field: 'thesis', id: '17', revision: 2, rule_kind: 'threshold', rule_id: 'boundary'},
    {kind: 'knowledge_file', field: 'original_text', source_binding_id: 'binding', knowledge_id: 'file', file_version: 'b'.repeat(64)}];
  const loaded = view.parseImport(JSON.stringify({...card(), citations: entries}), 'plan.json');
  assert.equal(loaded.body.card.citations.length, 3); assert(!loaded.body.card.citations[0].snapshot);
  assert.equal(loaded.body.card.trigger, card().trigger);
});
test('invalid citation type, field, version, shape and over-capacity JSON fail before changing the live form', () => {
  const {view} = fixture(); assert.equal(typeof view.parseImport, 'function');
  view.fillCard({card: card(), source_ids: []}); const before = plain(view.readCardForm());
  const invalid = [{...citation(), field: 'unknown'}, {...citation(), source_version: ''},
    {...citation(), kind: 'unknown'}, {...citation(), url: 'https://outside.invalid'},
    ...[null, 'untrusted']];
  for (const item of invalid) assert.throws(() => view.parseImport(JSON.stringify({...card(), citations: [item]}), 'plan.json'));
  assert.throws(() => view.parseImport(JSON.stringify({...card(), citations: Array.from({length: 33}, () => citation())}), 'plan.json'));
  assert.deepEqual(plain(view.readCardForm()), before);
});
test('chart citations retain frozen metadata through editing and import only their explicit identity', () => {
  const {view} = fixture(), chart = {kind: 'chart', field: 'thesis', id: 'chart-fixture', revision: 3,
    source_version: 'c'.repeat(64), source_versions: {'fixture-series': 'c'.repeat(64)},
    as_of: '2026-01-02', title: '保存时图名', definition_summary: '周期图 · 1 条曲线',
    definition: {format: 'chart.v1', curves: [{series_id: 'fixture-series'}]}, snapshot: {title: '保存时图名'}};
  view.fillCard({card: {...card(), citations: [chart]}, source_ids: []});
  assert.deepEqual(plain(view.readCardForm().card.citations), [chart]);
  assert.deepEqual(plain(view.parseImport(JSON.stringify({...card(), citations: [chart]}), 'plan.json').body.card.citations),
    [{kind: 'chart', field: 'thesis', id: chart.id, revision: 3}]);
  assert.throws(() => view.citationIdentity({...chart, revision: true}));
  assert.throws(() => view.citationIdentity({...chart, guessed_title_id: 'forbidden'}));
});
test('plain original imports preserve text literally and reject oversized content', () => {
  const {view} = fixture(); assert.equal(typeof view.parseImport, 'function');
  const text = '<script>window.unsafe=true</script>\n原始条件 ≥ 12.500';
  assert.equal(view.parseImport(text, 'source.md').text, text);
  assert.throws(() => view.parseImport('x'.repeat(24001), 'source.txt'));
});
test('parsed fragments require explicit confirmation even in an empty form and never interpret JSON as a strategy', () => {
  const {view, nodes, window} = fixture(), notes = [];
  window.confirm = message => { notes.push(message); return true; };
  const empty = Object.fromEntries(Object.keys(card()).map(key => [key, ''])); empty.direction = 'observe';
  view.fillCard({card: empty, source_ids: []});
  const text = JSON.stringify(card()), notice = '知识库已有解析片段，可能有重叠、表格或人工修订，不等同原件全文。';
  assert.equal(view.applyImport(text, 'source.json', undefined, {content_kind: 'parsed_fragments', notice}), true);
  assert.equal(notes.length, 1); assert(notes[0].includes(notice));
  assert.equal(nodes.get('research-card-title').value, 'source.json');
  assert.equal(nodes.get('research-card-trigger').value, '');
  assert.equal(nodes.get('research-card-original').value, text);
  assert.match(nodes.get('research-card-filename').textContent, /已解析文本片段/);
});
test('cancelling fragment import preserves existing prose, citations and filename', () => {
  const {view, nodes, window} = fixture(); window.confirm = () => false;
  view.fillCard({card: {...card(), citations: [citation()]}, source_ids: []});
  nodes.get('research-card-filename').textContent = '先前已导入.md';
  const before = plain(view.readCardForm());
  assert.equal(view.applyImport('片段', 'source.pdf', undefined, {content_kind: 'parsed_fragments', notice: '不是原件全文'}), false);
  assert.deepEqual(plain(view.readCardForm()), before);
  assert.equal(nodes.get('research-card-filename').textContent, '先前已导入.md');
});
test('zero observations retain their exact value and only absent text is omitted', () => {
  const {view} = fixture();
  assert.equal(view.observationText({value: 0, unit: 'kg'}), '0 kg');
  assert.equal(view.observationText({value: '0.000', unit: ''}), '0.000');
  assert.equal(view.observationText({value: 0, unit: null}), '0');
});
test('native reference links retain current workbench scope while knowledge originals use the existing platform route', () => {
  const {view} = fixture();
  assert.equal(view.citationLink('/sn/judgment?judgment_id=17', '查看研判').href,
    'https://fixture.invalid/research/workbenches/fixture/sn/judgment?judgment_id=17');
  const file = view.citationLink('/platform/knowledge-bases/library?knowledge_id=original', '预览原件');
  assert.equal(file.href, 'https://fixture.invalid/platform/knowledge-bases/library?knowledge_id=original');
  assert.equal(file.target, '_blank'); assert.equal(file.rel, 'noopener');
  assert.equal(view.citationLink('https://outside.invalid/file', '外部地址'), null);
});
