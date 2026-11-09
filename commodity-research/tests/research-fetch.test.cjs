const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Evaluate only the shared wrapper from the page script; the rest of app.js needs a DOM.
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/app.js'), 'utf8');
const wrapper = source.slice(source.indexOf('async function researchFetch'), source.indexOf('\n}\n', source.indexOf('async function researchFetch')) + 2);
const load = fetch => vm.runInNewContext(`${wrapper}\nresearchFetch`, {window: {fetch}, Headers, parent: {postMessage() {}}, location: {origin: 'x'}});

test('a dropped connection becomes a readable Chinese error instead of "Failed to fetch"', async () => {
  const researchFetch = load(async () => { throw new TypeError('Failed to fetch'); });
  await assert.rejects(researchFetch('/api/x'), error => {
    assert.match(error.message, /网络连接中断/);
    assert.equal(error.network, true);
    assert.equal(error.cause.message, 'Failed to fetch');
    return true;
  });
});

test('cancellation keeps its AbortError so callers can still ignore it', async () => {
  const abort = Object.assign(new Error('aborted'), {name: 'AbortError'});
  const researchFetch = load(async () => { throw abort; });
  await assert.rejects(researchFetch('/api/x'), error => error === abort);
});

test('ordinary responses pass through unchanged', async () => {
  const response = {status: 200};
  assert.equal(await load(async () => response)('/api/x'), response);
});
