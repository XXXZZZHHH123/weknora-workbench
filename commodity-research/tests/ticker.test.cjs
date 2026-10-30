const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[2] || path.join(__dirname, '../src/tin/web/static/ticker.js'), 'utf8');

class Events {
  constructor() { this.listeners = new Map(); }
  addEventListener(name, callback, options = {}) {
    const handlers = this.listeners.get(name) || [];
    handlers.push({callback, once: options.once}); this.listeners.set(name, handlers);
  }
  emit(name, event = {}) {
    for (const handler of [...(this.listeners.get(name) || [])]) {
      handler.callback({target: this, currentTarget: this, ...event});
      if (handler.once) this.listeners.set(name, this.listeners.get(name).filter(item => item !== handler));
    }
  }
}

function fixture({width = 400, content = 900, reduced = false, count = 1, ready = 'loading'} = {}) {
  let clock = 0, nextFrame = 0;
  const frames = new Map(), observers = [], media = Object.assign(new Events(), {matches: reduced});
  class Element extends Events {
    constructor(kind, measuredWidth = 0) {
      super(); this.kind = kind; this.width = measuredWidth; this.dataset = {};
      this.attributes = new Map(); this.children = []; this.parentElement = null; this.position = 0;
      this.clientWidth = kind === 'viewport' ? width : measuredWidth;
    }
    get scrollWidth() {
      if (this.kind === 'viewport') return Math.max(this.clientWidth, this.track.scrollWidth);
      return this.children.length ? this.children.reduce((sum, child) => sum + child.width, 0) + 24 * (this.children.length - 1) : this.width;
    }
    get scrollLeft() { return Math.min(this.position, Math.max(0, this.scrollWidth - this.clientWidth)); }
    set scrollLeft(value) { this.position = Math.max(0, Math.min(value, Math.max(0, this.scrollWidth - this.clientWidth))); }
    getBoundingClientRect() { return {width: this.width, height: 20}; }
    querySelector(selector) {
      if (selector === '[data-ticker-track]') return this.track || null;
      if (selector === '[data-ticker-items]') return this.items || null;
      return this.children.find(child => child.hasAttribute(selector.slice(1, -1))) || null;
    }
    contains(target) { return target === this || this.children.some(child => child.contains(target)); }
    append(child) { child.parentElement = this; this.children.push(child); }
    remove() { if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(child => child !== this); }
    setAttribute(name, value) { this.attributes.set(name, String(value)); }
    removeAttribute(name) { this.attributes.delete(name); }
    hasAttribute(name) { return this.attributes.has(name); }
    getAttribute(name) { return this.attributes.get(name) ?? null; }
    cloneNode() {
      const cloned = new Element(this.kind, this.width);
      cloned.attributes = new Map(this.attributes); return cloned;
    }
  }
  const tickers = Array.from({length: count}, () => {
    const viewport = new Element('viewport'), track = new Element('track'), items = new Element('items', content);
    items.setAttribute('data-ticker-items', ''); viewport.track = track; viewport.items = items;
    viewport.append(track); track.append(items); return viewport;
  });
  const document = Object.assign(new Events(), {hidden: false, readyState: ready,
    querySelectorAll: selector => selector === '[data-ticker]' ? tickers : []});
  const window = Object.assign(new Events(), {
    matchMedia: () => media, getComputedStyle: () => ({columnGap: '24px'}),
    requestAnimationFrame(callback) { frames.set(++nextFrame, callback); return nextFrame; },
    cancelAnimationFrame(id) { frames.delete(id); },
    ResizeObserver: class {
      constructor(callback) { this.callback = callback; observers.push(this); }
      observe() {}
    },
  });
  const context = vm.createContext({window, document, console});
  const execute = () => vm.runInContext(source, context);
  execute(); if (ready === 'loading') document.emit('DOMContentLoaded');
  function advance(duration) {
    for (let elapsed = 0; elapsed < duration; elapsed += 16) {
      clock += 16;
      const pending = [...frames.values()]; frames.clear(); pending.forEach(callback => callback(clock));
    }
  }
  function refresh() { observers.forEach(observer => observer.callback()); }
  function copies(viewport = tickers[0]) { return viewport.track.children.filter(child => child.hasAttribute('data-ticker-copy')); }
  return {tickers, viewport: tickers[0], window, document, media, frames, advance, refresh, copies, execute};
}

let checks = 0;
function check(name, callback) { callback(); checks++; console.log(`PASS ${name}`); }

check('overflow moves at a steady readable speed and loops without a blank section', () => {
  const f = fixture();
  assert.equal(f.copies().length, 1, 'Overflow requires one seamless continuation');
  assert.equal(f.copies()[0].getAttribute('aria-hidden'), 'true', 'The visual copy must not be read twice');
  f.advance(1024); assert.ok(f.viewport.scrollLeft > 26 && f.viewport.scrollLeft < 30);
  f.viewport.emit('mouseenter'); f.viewport.scrollLeft = 923; f.viewport.emit('mouseleave'); f.advance(160);
  assert.ok(f.viewport.scrollLeft > 0 && f.viewport.scrollLeft < 8, 'The next group must continue at the start');
});

check('hover and focus pause; focus moving within the ticker stays paused', () => {
  const f = fixture(); f.advance(200);
  for (const [pause, resume] of [['mouseenter', 'mouseleave'], ['focusin', 'focusout']]) {
    f.viewport.emit(pause); const held = f.viewport.scrollLeft;
    f.advance(300); assert.equal(f.viewport.scrollLeft, held);
    if (pause === 'focusin') {
      f.viewport.emit('focusout', {relatedTarget: f.viewport.items}); f.advance(100);
      assert.equal(f.viewport.scrollLeft, held);
    }
    f.viewport.emit(resume, {relatedTarget: null}); f.advance(100);
    assert.ok(f.viewport.scrollLeft > held);
  }
});

check('pointer release and cancellation resume from the manually selected position', () => {
  for (const event of ['pointerup', 'pointercancel']) {
    const f = fixture(); f.advance(100); f.viewport.emit('pointerdown');
    f.viewport.scrollLeft = 150; f.advance(100); assert.equal(f.viewport.scrollLeft, 150);
    f.window.emit(event); f.advance(100);
    assert.ok(f.viewport.scrollLeft > 150 && f.viewport.scrollLeft < 155);
  }
});

check('hidden pages stop scheduling and resume without a background time jump', () => {
  const f = fixture(); f.advance(100); const held = f.viewport.scrollLeft;
  f.document.hidden = true; f.document.emit('visibilitychange');
  assert.equal(f.frames.size, 0); f.advance(10000); assert.equal(f.viewport.scrollLeft, held);
  f.document.hidden = false; f.document.emit('visibilitychange'); f.advance(100);
  assert.ok(f.viewport.scrollLeft > held && f.viewport.scrollLeft < held + 4);
});

check('reduced motion leaves native manual scrolling available and reacts to preference changes', () => {
  const f = fixture({reduced: true});
  assert.equal(f.frames.size, 0); assert.equal(f.copies().length, 0);
  f.viewport.scrollLeft = 180; f.advance(200); assert.equal(f.viewport.scrollLeft, 180);
  f.media.matches = false; f.media.emit('change'); f.advance(100);
  assert.equal(f.copies().length, 1); assert.ok(f.viewport.scrollLeft > 180);
  f.media.matches = true; f.media.emit('change');
  assert.equal(f.frames.size, 0); assert.equal(f.copies().length, 0);
});

check('short, exactly fitting and zero-width tickers stay static', () => {
  for (const options of [{content: 200}, {content: 400}, {width: 0}]) {
    const f = fixture(options); f.advance(200);
    assert.equal(f.copies().length, 0); assert.equal(f.frames.size, 0); assert.equal(f.viewport.scrollLeft, 0);
  }
});

check('resize removes unnecessary copies and later restarts a single animation', () => {
  const f = fixture(); f.advance(100); f.viewport.clientWidth = 1200; f.refresh();
  assert.equal(f.copies().length, 0); assert.equal(f.frames.size, 0); assert.equal(f.viewport.scrollLeft, 0);
  f.viewport.clientWidth = 320; f.window.emit('resize'); f.advance(100);
  assert.equal(f.copies().length, 1); assert.equal(f.frames.size, 1); assert.ok(f.viewport.scrollLeft > 0);
});

check('page restoration and repeated initialization never duplicate animation or copied content', () => {
  const f = fixture({ready: 'complete'}); f.advance(100);
  f.execute(); f.document.emit('DOMContentLoaded'); assert.equal(f.frames.size, 1); assert.equal(f.copies().length, 1);
  f.window.emit('pagehide'); assert.equal(f.frames.size, 0);
  f.window.emit('pageshow', {persisted: true}); f.window.emit('pageshow', {persisted: true}); f.advance(100);
  assert.equal(f.frames.size, 1); assert.equal(f.copies().length, 1); assert.ok(f.viewport.scrollLeft > 0);
});

check('multiple tickers retain independent pause and movement state', () => {
  const f = fixture({count: 2}); f.advance(100);
  const [first, second] = f.tickers; first.emit('mouseenter');
  const held = first.scrollLeft, moving = second.scrollLeft; f.advance(200);
  assert.equal(first.scrollLeft, held); assert.ok(second.scrollLeft > moving);
  assert.equal(f.copies(first).length, 1); assert.equal(f.copies(second).length, 1);
});

check('pages without a ticker safely perform no animation', () => {
  const f = fixture({count: 0}); f.window.emit('resize'); f.window.emit('pageshow'); f.advance(100);
  assert.equal(f.frames.size, 0);
});

console.log(`${checks} ticker interaction checks passed`);
