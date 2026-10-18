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
  const values = new Map([['--app-font-family', '"Chosen Interface", sans-serif'], ['--app-font-family-mono', '"Chosen Code", monospace'], ['--app-font-family-reading', 'must-not-copy'], ['zoom', '1.1']])
  const applied = new Map<string, string>(), events: Record<string, () => void> = {}
  let changed = () => {}, observing = false
  const parentRoot = {}
  const parent = { get document() { if (options.crossOrigin) throw Error('SecurityError'); return { documentElement: parentRoot } } }
  const window: any = { parent, getComputedStyle: (element: unknown) => { assert.equal(element, parentRoot); return { getPropertyValue: (key: string) => values.get(key) || '' } },
    addEventListener: (name: string, callback: () => void) => { events[name] = callback } }
  if (options.standalone) window.parent = window
  runInNewContext(source.slice(start, end) + 'followWorkspaceTypography();', {
    window, document: { documentElement: { style: { setProperty: (key: string, value: string) => applied.set(key, value) } } },
    MutationObserver: class {
      constructor(callback: () => void) { changed = () => { if (observing) callback() } }
      observe(target: unknown, config: { attributeFilter: string[] }) { assert.equal(target, parentRoot); assert.equal(config.attributeFilter.join(','), 'style'); observing = true }
      disconnect() { observing = false }
    },
  })
  return { values, applied, events, changed: () => changed(), observing: () => observing }
}

for (const surface of Object.keys(surfaces) as Array<keyof typeof surfaces>) {
  test(`${surface}: inherits only selected interface/code fonts and follows later changes`, () => {
    const f = fixture(surface)
    assert.deepEqual([...f.applied.keys()], ['--app-font-family', '--app-font-family-mono'])
    assert.equal(f.applied.get('--app-font-family'), '"Chosen Interface", sans-serif')
    f.values.set('--app-font-family', '"Next Interface", sans-serif')
    f.values.set('--app-font-family-mono', '"Next Code", monospace')
    f.changed()
    assert.equal(f.applied.get('--app-font-family'), '"Next Interface", sans-serif')
    assert.equal(f.applied.get('--app-font-family-mono'), '"Next Code", monospace')
    assert.equal(f.applied.has('zoom'), false, 'The parent already scales the iframe; never apply zoom twice')
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
  })
}
