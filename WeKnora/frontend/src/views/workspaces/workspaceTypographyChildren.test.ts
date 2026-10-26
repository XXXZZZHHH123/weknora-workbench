import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const surfaces = {
  research: new URL('../../../../../commodity-research/src/tin/web/static/app.js', import.meta.url),
  structured: new URL('../../../../../structured-data/src/structured_data/index.html', import.meta.url),
}

function fixture(surface: keyof typeof surfaces, options: { standalone?: boolean; crossOrigin?: boolean } = {}) {
  const source = readFileSync(surfaces[surface], 'utf8')
  const start = source.indexOf('function followWorkspaceTypography()')
  const end = source.indexOf('followWorkspaceTypography();', start)
  assert.ok(start >= 0 && end > start)
  const values = new Map([
    ['--app-font-family', '"Chosen Interface", sans-serif'],
    ['--app-font-family-mono', '"Chosen Code", monospace'],
    ['--cds-font-display', '"Chosen Display", serif'],
    ['--cds-font-reading', '"Chosen Reading", serif'],
    ['--cds-font-code', '"Chosen Code Role", monospace'],
    ['--cds-font-interface', 'must-retain-local-alias'],
    ['--app-font-family-reading', 'must-not-copy'],
    ['zoom', '1.1'],
  ])
  const classes = new Set<string>()
  const applied = new Map<string, string>(), events: Record<string, () => void> = {}
  let changed = () => {}, observing = false
  const parentRoot = {}
  const parent = { get document() { if (options.crossOrigin) throw Error('SecurityError'); return { documentElement: parentRoot } } }
  const window: any = { parent, getComputedStyle: (element: unknown) => { assert.equal(element, parentRoot); return { getPropertyValue: (key: string) => values.get(key) || '' } },
    addEventListener: (name: string, callback: () => void) => { events[name] = callback } }
  if (options.standalone) window.parent = window
  runInNewContext(source.slice(start, end) + 'followWorkspaceTypography();', {
    window, document: { documentElement: { classList: { add: (name: string) => classes.add(name) }, style: { setProperty: (key: string, value: string) => applied.set(key, value) } } },
    MutationObserver: class {
      constructor(callback: () => void) { changed = () => { if (observing) callback() } }
      observe(target: unknown, config: { attributeFilter: string[] }) { assert.equal(target, parentRoot); assert.equal(config.attributeFilter.join(','), surface === 'structured' ? 'style,theme-mode,class' : 'style'); observing = true }
      disconnect() { observing = false }
    },
  })
  return { values, applied, classes, events, changed: () => changed(), observing: () => observing }
}

for (const surface of Object.keys(surfaces) as Array<keyof typeof surfaces>) {
  test(`${surface}: inherits the selected font roles and follows later changes`, () => {
    const f = fixture(surface)
    assert.equal(f.classes.has('workspace-embedded'), surface === 'research', 'Only recognized embedded research pages use the compact shell')
    assert.deepEqual([...f.applied.keys()], ['--app-font-family', '--app-font-family-mono', '--cds-font-display', '--cds-font-reading', '--cds-font-code'])
    assert.equal(f.applied.get('--app-font-family'), '"Chosen Interface", sans-serif')
    f.values.set('--app-font-family', '"Next Interface", sans-serif')
    f.values.set('--app-font-family-mono', '"Next Code", monospace')
    f.values.set('--cds-font-display', '"Next Display", serif')
    f.values.set('--cds-font-reading', '"Next Reading", serif')
    f.values.set('--cds-font-code', '"Next Code Role", monospace')
    f.changed()
    assert.equal(f.applied.get('--app-font-family'), '"Next Interface", sans-serif')
    assert.equal(f.applied.get('--app-font-family-mono'), '"Next Code", monospace')
    assert.equal(f.applied.get('--cds-font-display'), '"Next Display", serif')
    assert.equal(f.applied.get('--cds-font-reading'), '"Next Reading", serif')
    assert.equal(f.applied.get('--cds-font-code'), '"Next Code Role", monospace')
    assert.equal(f.applied.has('--cds-font-interface'), false, 'Keep the local interface alias attached to the selected UI font')
    assert.equal(f.applied.has('--app-font-family-reading'), false)
    assert.equal(f.applied.has('zoom'), false, 'The parent already scales the iframe; never apply zoom twice')
    if (surface === 'structured') {
      f.values.set('--td-brand-color', '#9a4d30')
      f.values.set('--td-text-color-secondary', '#59534e')
      f.changed()
      assert.equal(f.applied.get('--td-brand-color'), '#9a4d30')
      assert.equal(f.applied.get('--td-text-color-secondary'), '#59534e')
    }
  })

  test(`${surface}: pagehide disconnects and a cached-page restore resumes typography updates`, () => {
    const f = fixture(surface)
    f.events.pagehide!()
    assert.equal(f.observing(), false)
    f.values.set('--app-font-family', 'restored-font')
    f.events.pageshow!()
    assert.equal(f.observing(), true)
    assert.equal(f.applied.get('--app-font-family'), 'restored-font')
  })

  test(`${surface}: standalone and inaccessible parents retain local defaults`, () => {
    assert.equal(fixture(surface, { standalone: true }).applied.size, 0)
    assert.equal(fixture(surface, { crossOrigin: true }).applied.size, 0)
    assert.equal(fixture(surface, { standalone: true }).classes.size, 0)
    assert.equal(fixture(surface, { crossOrigin: true }).classes.size, 0)
  })
}
