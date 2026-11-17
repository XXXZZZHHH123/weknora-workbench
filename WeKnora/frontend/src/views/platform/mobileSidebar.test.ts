import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const handlers = ts.transpile(source.slice(source.indexOf('const mobileSidebarMedia ='), source.indexOf('// 用于跟踪拖拽')))

function fixture(narrow = true) {
  const focused: string[] = []
  const watchers: Array<{ get: () => unknown; run: (...args: any[]) => unknown }> = []
  const uiStore = {
    viewportIsNarrow: narrow, sidebarCollapsed: false,
    collapseSidebar() { this.sidebarCollapsed = true },
    setNarrowViewport(value: boolean) { this.viewportIsNarrow = value },
  }
  const api = runInNewContext(`${handlers}; ({handleSidebarEscape, closeMobileSidebar, handleMobileNavigationClick})`, {
    uiStore, route: { fullPath: '/platform/knowledge-bases' },
    window: { matchMedia: () => ({ matches: narrow }) },
    document: { querySelector: (selector: string) => ({ focus: () => focused.push(selector) }) },
    nextTick: () => Promise.resolve(),
    watch: (get: () => unknown, run: (...args: any[]) => unknown) => watchers.push({ get, run }),
  })
  return { ...api, uiStore, focused, watchers }
}

test('mobile drawer closes on Escape or backdrop and restores the navigation trigger focus', async () => {
  for (const action of ['escape', 'backdrop']) {
    const f = fixture()
    let prevented = false
    if (action === 'escape') f.handleSidebarEscape({ key: 'Escape', preventDefault() { prevented = true } })
    else f.closeMobileSidebar(true)
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(f.uiStore.sidebarCollapsed, true)
    assert.equal(prevented, action === 'escape')
    assert.deepEqual(f.focused, ['.mobile-sidebar-toggle-trigger'])
  }
})

test('mobile target selection and route changes dismiss the drawer; desktop Escape preserves it', () => {
  const f = fixture()
  f.handleMobileNavigationClick({ target: { closest: (selector: string) => selector === '.workspace-nav-node__link' } })
  assert.equal(f.uiStore.sidebarCollapsed, true)
  f.uiStore.sidebarCollapsed = false
  f.watchers[0].run()
  assert.equal(f.uiStore.sidebarCollapsed, true)
  const desktop = fixture(false)
  desktop.handleSidebarEscape({ key: 'Escape', preventDefault() { assert.fail('desktop must not consume Escape') } })
  assert.equal(desktop.uiStore.sidebarCollapsed, false)
})

test('opening the mobile drawer focuses its close control', async () => {
  const f = fixture()
  await f.watchers[1].run([true, false], [true, true])
  assert.deepEqual(f.focused, ['.mobile-sidebar-close-trigger'])
})
