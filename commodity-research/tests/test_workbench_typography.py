"""Embedded research retains the host's font roles without scaling twice."""

import shutil
import subprocess
from pathlib import Path


def test_research_font_roles_sync_and_restore_without_copying_other_preferences():
    node = shutil.which("node")
    assert node, "Node is required by the research web interaction tests"
    script = Path(__file__).resolve().parents[1] / "src/tin/web/static/app.js"
    checks = r"""
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {runInNewContext} = require('node:vm');
const source = readFileSync(process.argv[1], 'utf8');
const start = source.indexOf('function followWorkspaceTypography()');
const end = source.indexOf('followWorkspaceTypography();', start);
assert.ok(start >= 0 && end > start);
const roles = ['--app-font-family', '--app-font-family-mono', '--cds-font-display',
  '--cds-font-reading', '--cds-font-code'];
function fixture({standalone = false, crossOrigin = false} = {}) {
  const values = new Map(roles.map((role, index) => [role, `font-${index}`]));
  values.set('zoom', '1.125');
  values.set('--cds-font-interface', 'must-retain-local-alias');
  values.set('--app-theme', 'must-not-copy');
  const applied = new Map(), classes = new Set(), events = {};
  const root = {};
  let changed = () => {}, observing = false;
  const parent = {get document() {
    if (crossOrigin) throw Error('SecurityError');
    return {documentElement: root};
  }};
  const window = {parent,
    getComputedStyle(element) {
      assert.equal(element, root);
      return {getPropertyValue: name => values.get(name) || ''};
    },
    addEventListener(name, callback) {events[name] = callback;},
  };
  if (standalone) window.parent = window;
  runInNewContext(source.slice(start, end) + 'followWorkspaceTypography();', {
    window,
    document: {documentElement: {
      classList: {add: value => classes.add(value)},
      style: {setProperty: (key, value) => applied.set(key, value)},
    }},
    MutationObserver: class {
      constructor(callback) {changed = () => {if (observing) callback();};}
      observe(target, options) {
        assert.equal(target, root);
        assert.equal(options.attributeFilter.join(','), 'style');
        observing = true;
      }
      disconnect() {observing = false;}
    },
  });
  return {values, applied, classes, events, changed: () => changed(),
    observing: () => observing};
}
const embedded = fixture();
assert.deepEqual([...embedded.applied.keys()], roles);
assert.ok(embedded.classes.has('workspace-embedded'));
for (const role of roles) {
  embedded.values.set(role, `changed-${role}`);
}
embedded.changed();
for (const role of roles) assert.equal(embedded.applied.get(role), `changed-${role}`);
assert.equal(embedded.applied.has('zoom'), false);
assert.equal(embedded.applied.has('--cds-font-interface'), false);
assert.equal(embedded.applied.has('--app-theme'), false);
embedded.events.pagehide();
assert.equal(embedded.observing(), false);
embedded.values.set('--cds-font-reading', 'restored-reading');
embedded.changed();
assert.notEqual(embedded.applied.get('--cds-font-reading'), 'restored-reading');
embedded.events.pageshow();
assert.equal(embedded.observing(), true);
assert.equal(embedded.applied.get('--cds-font-reading'), 'restored-reading');
for (const options of [{standalone: true}, {crossOrigin: true}]) {
  const fallback = fixture(options);
  assert.equal(fallback.applied.size, 0);
  assert.equal(fallback.classes.size, 0);
  assert.equal(fallback.observing(), false);
}
"""
    result = subprocess.run(
        [node, "-e", checks, str(script)], capture_output=True, text=True, timeout=10
    )
    assert result.returncode == 0, result.stdout + result.stderr
