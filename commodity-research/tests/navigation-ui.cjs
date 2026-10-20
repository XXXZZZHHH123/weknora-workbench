const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(process.argv[2], 'utf8');

function fixture() {
  const listeners = {}, timers = new Map(); let timerId = 0;
  const element = () => ({ hidden: true, textContent: '', attributes: new Map(),
    setAttribute(k, v) { this.attributes.set(k, String(v)); }, removeAttribute(k) { this.attributes.delete(k); },
    hasAttribute(k) { return this.attributes.has(k); }, getAttribute(k) { return this.attributes.get(k) || null; },
    classList: { values: new Set(), add(k) { this.values.add(k); }, remove(k) { this.values.delete(k); } } });
  const status = element(), progress = element(), main = element();
  const links = [];
  const document = {
    getElementById: id => id === 'research-navigation-status' ? status : id === 'research-navigation-progress' ? progress : null,
    querySelector: () => main,
    querySelectorAll: () => links.filter(l => l.hasAttribute('data-research-loading')),
    addEventListener: (name, fn) => { listeners['document:' + name] = fn; },
  };
  const window = { RESEARCH_PREFIX: '/research/workbenches/fixture',
    addEventListener: (name, fn) => { listeners['window:' + name] = fn; } };
  const location = new URL('https://app.test/research/workbenches/fixture/sn');
  vm.runInNewContext(source, { document, window, location, URL,
    setTimeout: (fn, delay) => { timers.set(++timerId, { fn, delay }); return timerId; },
    clearTimeout: id => timers.delete(id) });
  const link = (href = '/research/workbenches/fixture/sn/indicators') => {
    const a = Object.assign(element(), { href: new URL(href, location).href, textContent: '指标', target: '',
      closest: () => a }); links.push(a); return a;
  };
  const click = (a, extra = {}) => listeners['document:click']({ target: a, button: 0, defaultPrevented: false,
    preventDefault() { throw Error('Native navigation must not be intercepted'); }, ...extra });
  const advance = threshold => {
    for (const [id, timer] of [...timers]) if (timer.delay <= threshold) { timers.delete(id); timer.fn(); }
  };
  return { listeners, status, progress, main, timers, link, click, advance };
}

let f = fixture(); const selected = f.link();
f.click(selected);
assert.equal(f.main.getAttribute('aria-busy'), 'true');
assert.equal(f.status.hidden, true, 'Quick pages must not flash a loading badge');
f.advance(120);
assert.equal(f.status.hidden, false);
assert.match(f.status.textContent, /指标/);
assert.equal(f.progress.hidden, false);
assert.equal(selected.hasAttribute('data-research-loading'), true);
f.listeners['window:pageshow']({ persisted: true });
assert.equal(f.status.hidden, true);
assert.equal(f.progress.hidden, true);
assert.equal(f.main.hasAttribute('aria-busy'), false);
assert.equal(selected.hasAttribute('data-research-loading'), false);
assert.equal(f.timers.size, 0);

for (const extra of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }, { defaultPrevented: true }]) {
  f = fixture(); f.click(f.link(), extra); assert.equal(f.timers.size, 0);
}
for (const href of ['https://external.test/elsewhere', '/platform/knowledge-bases/fixture', '#section']) {
  f = fixture(); f.click(f.link(href)); assert.equal(f.timers.size, 0);
}
f = fixture(); let link = f.link(); link.target = '_blank'; f.click(link); assert.equal(f.timers.size, 0);
f = fixture(); link = f.link(); link.setAttribute('download', ''); f.click(link); assert.equal(f.timers.size, 0);
f = fixture(); f.click(f.link()); f.advance(120); f.listeners['document:keydown']({ key: 'Escape' });
assert.equal(f.progress.hidden, true); assert.equal(f.timers.size, 0);
f = fixture(); f.click(f.link()); f.advance(20000);
assert.match(f.status.textContent, /较长/);
f.listeners['window:pagehide'](); assert.equal(f.timers.size, 0);
console.log('Navigation feedback: native links, scope, delays, slow state and history restore passed');
