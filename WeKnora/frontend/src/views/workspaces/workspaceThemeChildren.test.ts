import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

function fixture(surface: 'structured' | 'research', options: { embedded?: boolean; parentTheme?: string; saved?: string; systemDark?: boolean; storageBlocked?: boolean } = {}) {
  const html = readFileSync(new URL('../../../../../structured-data/src/structured_data/index.html', import.meta.url), 'utf8')
  const research = readFileSync(new URL('../../../../../commodity-research/src/tin/web/static/app.js', import.meta.url), 'utf8')
  const source = surface === 'structured' ? html.match(/<script id="workspace-theme">([\s\S]*?)<\/script>/)?.[1]
    : research.slice(research.indexOf('// 页面交互'), research.indexOf('function togglePopover'))
  assert.ok(source, `${surface} theme initialization is absent`)
  const classes = new Set<string>(), attrs = new Map<string, string>(), listeners: Record<string, (event: any) => void> = {}, mediaListeners: Array<(event: any) => void> = []
  const stored = new Map<string, string>(options.saved ? [['tin-theme', options.saved]] : []), writes: string[] = [], messages: any[] = []
  const root = { style: {} as Record<string, string>, setAttribute: (key: string, value: string) => attrs.set(key, value), getAttribute: (key: string) => attrs.get(key) || null,
    classList: { contains: (key: string) => classes.has(key), toggle(key: string, force?: boolean) { const enabled = force ?? !classes.has(key); enabled ? classes.add(key) : classes.delete(key); return enabled }, add: (key: string) => classes.add(key) } }
  const parent = { document: { documentElement: { getAttribute: () => options.parentTheme || null } }, postMessage: (data: any, target: string) => messages.push({ data, target }) }
  const media = { matches: !!options.systemDark, addEventListener: (event: string, listener: (value: any) => void) => { assert.equal(event, 'change'); mediaListeners.push(listener) } }
  const window: any = { location: { origin: 'https://test.local' }, matchMedia: () => media, addEventListener: (name: string, handler: (event: any) => void) => { listeners[name] = handler } }
  window.parent = options.embedded ? parent : window
  const context: any = { window, parent: window.parent, location: window.location, document: { documentElement: root },
    localStorage: { getItem(key: string) { if (options.storageBlocked) throw new Error('Storage disabled'); return stored.get(key) || null },
      setItem(key: string, value: string) { if (options.storageBlocked) throw new Error('Storage disabled'); writes.push(key); stored.set(key, value) } } }
  runInNewContext(source, context)
  return { attrs, classes, root, stored, writes, messages,
    receive: (data: any, override: Record<string, unknown> = {}) => listeners.message?.({ origin: 'https://test.local', source: parent, data, ...override }),
    system: (dark: boolean) => { media.matches = dark; mediaListeners.forEach(listener => listener({ matches: dark })) },
    toggle: () => context.toggleTheme(),
  }
}

for (const surface of ['structured', 'research'] as const) {
  test(`${surface}: embedded theme starts from the parent and rejects foreign or malformed messages`, () => {
    const f = fixture(surface, { embedded: true, parentTheme: 'dark', saved: 'light' })
    assert.equal(f.attrs.get('theme-mode'), 'dark')
    assert.equal(f.root.style.colorScheme, 'dark')
    assert.equal(f.messages[0]?.data.type, 'workspace-theme-ready')
    f.receive({ type: 'workspace-theme', mode: 'light' }, { origin: 'https://untrusted.example' })
    f.receive({ type: 'workspace-theme', mode: 'light' }, { source: {} })
    f.receive({ type: 'workspace-theme', mode: 'invalid' })
    assert.equal(f.attrs.get('theme-mode'), 'dark')
    f.receive({ type: 'workspace-theme', mode: 'light' })
    assert.equal(f.attrs.get('theme-mode'), 'light')
    assert.equal(f.classes.has('dark'), false)
    f.system(true)
    assert.equal(f.attrs.get('theme-mode'), 'light', 'embedded theme remains controlled by the parent')
    assert.deepEqual(f.writes, [])
  })

  test(`${surface}: standalone theme follows the system without polling and honors a saved preference`, () => {
    const system = fixture(surface, { systemDark: true })
    assert.equal(system.attrs.get('theme-mode'), 'dark')
    system.system(false)
    assert.equal(system.attrs.get('theme-mode'), 'light')
    assert.deepEqual(system.messages, [])
    const saved = fixture(surface, { saved: 'dark' })
    saved.system(false)
    assert.equal(saved.attrs.get('theme-mode'), 'dark')
  })

  test(`${surface}: disabled storage still permits initialization and parent theme updates`, () => {
    const f = fixture(surface, { storageBlocked: true, embedded: true, parentTheme: 'light' })
    f.receive({ type: 'workspace-theme', mode: 'dark' })
    assert.equal(f.attrs.get('theme-mode'), 'dark')
  })
}

test('research toggle requests a parent preference change and waits for its acknowledgment', () => {
  const f = fixture('research', { embedded: true, parentTheme: 'light' })
  f.toggle()
  assert.equal(f.messages.at(-1)?.data.type, 'workspace-theme-request')
  assert.equal(f.messages.at(-1)?.data.mode, 'dark')
  assert.equal(f.attrs.get('theme-mode'), 'light')
  assert.deepEqual(f.writes, [])
  f.receive({ type: 'workspace-theme', mode: 'dark' })
  assert.equal(f.attrs.get('theme-mode'), 'dark')
})

test('standalone research toggle persists the original preference and works when storage is blocked', () => {
  const f = fixture('research'); f.toggle()
  assert.equal(f.stored.get('tin-theme'), 'dark')
  assert.equal(f.attrs.get('theme-mode'), 'dark')
  const blocked = fixture('research', { storageBlocked: true }); blocked.toggle()
  assert.equal(blocked.attrs.get('theme-mode'), 'dark')
})
