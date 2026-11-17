const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/report-figures.js'), 'utf8');
async function scenario(count, live = true) {
  let calls = 0, ready;
  const element = () => ({dataset: {}, textContent: '', children: [], append(...children) { this.children.push(...children); }, replaceChildren(...children) { this.children = children; }, addEventListener() {}});
  const plot = element(), card = Object.assign(element(), {dataset: {reportFigure: 'chart:fixture'}, querySelector() { return plot; }});
  const host = count === null ? null : Object.assign(element(), {dataset: {live: String(live), url: '/api/sn/reports/figures', day: '2026-01-09', period: 'daily', revision: ''}, querySelectorAll() { return count ? [card] : []; }});
  const boot = {textContent: JSON.stringify(count ? [{key: 'chart:fixture',kind: 'chart'}] : [])};
  vm.runInNewContext(source, {URL, Intl, console, window: {}, location: {origin: 'http://fixture.test'}, document: {
    addEventListener(_event, callback) { ready = callback; }, getElementById(id) { return id === 'report-figure-data' ? boot : host; }, createElement: element,
  }, fetch: async (url) => { calls++; assert.equal(url.searchParams.get('date'), '2026-01-09'); return {ok: false, status: 409, json: async () => ({})}; }});
  ready(); await new Promise(setImmediate); return {calls, host, plot};
}
(async () => {
  assert.equal((await scenario(null)).calls, 0);
  assert.equal((await scenario(0)).calls, 0);
  assert.equal((await scenario(1, false)).calls, 0, 'Frozen archive performs no chart reads');
  const stale = await scenario(1); assert.equal(stale.calls, 1); assert.match(stale.plot.children[0].textContent, /已有变化/);
  console.log('PASS: no-host, explicit-empty, frozen archive zero fetch; selected-figure 409 remains visible');
})().catch((error) => { console.error(error); process.exitCode = 1; });
