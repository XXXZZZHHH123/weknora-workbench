import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

// The v0.9.0 login page collapses to one column at 720px and hides the showcase there.
const source = readFileSync(new URL('./Login.vue', import.meta.url), 'utf8')
const narrowStyles = source.slice(source.indexOf('@media (max-width: 720px)'))

test('phone login shows the form first and reserves room for the header', () => {
  assert.ok(narrowStyles.startsWith('@media (max-width: 720px)'))
  assert.match(narrowStyles, /\.login-layout\s*\{[^}]*flex-direction:\s*column;[^}]*padding:\s*112px\b/)
  assert.match(narrowStyles, /\.showcase-section\s*\{\s*display:\s*none;/)
})

test('narrow login headers keep the logo and links pinned to opposite edges', () => {
  assert.match(narrowStyles, /\.header-logo\s*\{[^}]*left:[^}]*top:\s*var\(--app-space-8\)/)
  assert.match(narrowStyles, /\.header-links\s*\{[^}]*right:[^}]*top:\s*var\(--app-space-8\)/)
  // Only the short language label is shown, never the full language name.
  assert.match(source, /<span class="link-text">\{\{ currentLangOption\?\.shortLabel \}\}<\/span>/)
})
