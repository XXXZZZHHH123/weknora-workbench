import shutil
import subprocess
from pathlib import Path


def test_quick_entry_handles_empty_sources_and_preserves_user_time():
    script = Path(__file__).resolve().parents[1] / 'src/tin/web/static/app.js'
    program = r'''
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[1], 'utf8');
const fn = source.slice(source.indexOf('function syncDrawerUnit()'), source.indexOf('function showToast('));
function fixture(options, touched) {
  const elements = {
    'drawer-series': { selectedOptions: options },
    'drawer-unit': { innerText: 'previous unit' },
    'drawer-as-of': { value: 'previous time', dataset: { touched: touched ? 'true' : '' } },
  };
  const context = vm.createContext({ document: { getElementById: id => elements[id] } });
  vm.runInContext(fn, context);
  context.syncDrawerUnit();
  return elements;
}
let state = fixture([], false);
assert.equal(state['drawer-unit'].innerText, '');
assert.equal(state['drawer-as-of'].value, '');
state = fixture([], true);
assert.equal(state['drawer-as-of'].value, 'previous time');
state = fixture([{ dataset: { unit: 'kg', asOf: '2031-02-03T10:00' } }], false);
assert.equal(state['drawer-unit'].innerText, 'kg');
assert.equal(state['drawer-as-of'].value, '2031-02-03T10:00');
state = fixture([{ dataset: { unit: 'kg', asOf: '2031-02-03T10:00' } }], true);
assert.equal(state['drawer-as-of'].value, 'previous time');
'''
    node = shutil.which('node')
    assert node, 'Node is required by the research web interaction tests'
    result = subprocess.run([node, '-e', program, str(script)], capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stdout + result.stderr
