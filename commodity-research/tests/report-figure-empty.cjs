const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/report-figures.js'), 'utf8');
async function scenario(count) {
  let calls = 0, ready;
  const element = () => ({dataset: {}, textContent: '', append(...children) { this.children = children; }, addEventListener() {}});
  const host = count === null ? null : Object.assign(element(), {dataset: {seriesCount: String(count), url: '/api/sn/reports/figures', day: '2026-01-09', period: 'daily', revision: ''}, replaceChildren(...children) { this.children = children; }});
  vm.runInNewContext(source, {URL, Intl, console, window: {}, location: {origin: 'http://fixture.test'}, document: {
    addEventListener(_event, callback) { ready = callback; }, getElementById() { return host; }, createElement: element,
  }, fetch: async (url) => { calls++; assert.equal(url.searchParams.get('date'), '2026-01-09'); return {ok: false, status: 409}; }});
  ready(); await new Promise(setImmediate); return {calls, host};
}
(async () => {
  assert.equal((await scenario(null)).calls, 0, 'No selected figures => no host => no fetch');
  const empty = await scenario(0); assert.equal(empty.calls, 0); assert.match(empty.host.textContent, /未选择报告图表/);
  const stale = await scenario(1); assert.equal(stale.calls, 1); assert.match(stale.host.children[0].textContent, /已有变化/);
  console.log('PASS: no-host and explicit-empty perform zero fetch; selected-figure 409 remains visible');
})().catch((error) => { console.error(error); process.exitCode = 1; });
