import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'

const preferences = readFileSync(new URL('./preferenceStorage.ts', import.meta.url), 'utf8')
const source = readFileSync(new URL('./useTheme.ts', import.meta.url), 'utf8')
const actualCode = (value: string) => ts.transpileModule(value.replace(/^import[\s\S]*?from ['"][^'"]+['"]\s*;?\s*/gm, '').replace(/^export /gm, ''), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

function theme(initial = 'light', systemDark = false, native = false) {
  const values = new Map([['weknora_user', JSON.stringify({ id: 'actor-a' })], ['WeKnora_actor-a_theme', initial]])
  const listeners: Array<() => void> = [], calls: unknown[][] = []
  const html = { style: {} as any, attrs: {} as Record<string, string>, setAttribute: (key: string, value: string) => { html.attrs[key] = value } }
  const body = { style: {} as any }, app = { style: {} as any }
  const media = { matches: systemDark, addEventListener: (_name: string, listener: () => void) => listeners.push(listener) }
  const window = { matchMedia: () => media, runtime: native ? {
    WindowSetDarkTheme: () => calls.push(['dark']), WindowSetLightTheme: () => calls.push(['light']),
    WindowSetBackgroundColour: (...args: number[]) => calls.push(['background', ...args]),
  } : undefined }
  const context = vm.createContext({
    ref, window, document: { documentElement: html, body, getElementById: () => app },
    localStorage: { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key) },
    getComputedStyle: () => ({ backgroundColor: html.attrs['theme-mode'] === 'dark' ? 'rgb(17, 28, 40)' : 'rgb(244, 247, 250)' }),
    console: { warn: () => {} },
  })
  vm.runInContext(actualCode(preferences) + actualCode(source) + '\ninitTheme()', context)
  const api = vm.runInContext('useTheme()', context)
  return { api, values, listeners, calls, html, body, app, media, context }
}

test('the shared effective theme exposes the mode actually applied to native and embedded pages', () => {
  const current = theme('dark')
  assert.equal(current.api.currentTheme.value, 'dark')
  assert.equal(current.api.effectiveTheme.value, 'dark')
  assert.equal(current.html.attrs['theme-mode'], 'dark')
  assert.equal(current.api.setTheme('light'), true)
  assert.equal(current.api.effectiveTheme.value, 'light')
  assert.equal(current.values.get('WeKnora_actor-a_theme'), 'light')
})

test('system preference changes update only the actual theme while retaining the user choice', () => {
  const current = theme('system', false)
  assert.equal(current.listeners.length, 1)
  assert.equal(current.api.effectiveTheme.value, 'light')
  current.media.matches = true
  current.listeners[0]()
  assert.equal(current.api.effectiveTheme.value, 'dark')
  assert.equal(current.api.currentTheme.value, 'system')
  assert.equal(current.values.get('WeKnora_actor-a_theme'), 'system')
  current.api.setTheme('light')
  current.media.matches = false
  current.listeners[0]()
  assert.equal(current.api.effectiveTheme.value, 'light')
})

test('all native canvas surfaces resolve the same CSS page token rather than a second fixed palette', () => {
  const current = theme()
  for (const element of [current.html, current.body, current.app]) {
    assert.equal(element.style.background, 'var(--td-bg-color-page)')
  }
  current.api.setTheme('dark')
  assert.equal(current.html.style.colorScheme, 'dark')
  for (const element of [current.html, current.body, current.app]) assert.equal(element.style.background, 'var(--td-bg-color-page)')
})

test('the optional Wails shell receives the actual computed CSS canvas colors once per change', () => {
  const current = theme('light', false, true)
  assert.deepEqual(current.calls[1], ['background', 244, 247, 250, 255])
  current.api.setTheme('dark')
  assert.deepEqual(current.calls[3], ['background', 17, 28, 40, 255])
  const count = current.calls.length
  current.api.setTheme('dark')
  assert.equal(current.calls.length, count)
})

test('invalid theme modes are rejected and different users retain their own preference', () => {
  const current = theme('dark')
  assert.equal(current.api.setTheme('unexpected'), false)
  assert.equal(current.api.effectiveTheme.value, 'dark')
  current.values.set('weknora_user', JSON.stringify({ id: 'actor-b' }))
  current.values.set('WeKnora_actor-b_theme', 'light')
  vm.runInContext('reloadThemeFromStorage()', current.context)
  assert.equal(current.api.effectiveTheme.value, 'light')
  assert.equal(current.values.get('WeKnora_actor-a_theme'), 'dark')
})
