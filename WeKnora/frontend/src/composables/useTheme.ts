import { ref } from 'vue'
import {
  loadPreference,
  savePreference,
  migratePreferencesIntoUser,
} from './preferenceStorage'

export type ThemeMode = 'light' | 'dark' | 'system'

const THEME_KEY = 'theme'

function loadTheme(): ThemeMode {
  const v = loadPreference(THEME_KEY)
  if (v === 'light' || v === 'dark' || v === 'system') return v
  return 'light'
}

// Shared reactive state across all consumers
const currentTheme = ref<ThemeMode>(loadTheme())
const effectiveTheme = ref<'light' | 'dark'>(currentTheme.value === 'dark' ? 'dark' : 'light')

let lastEffective: 'light' | 'dark' | null = null

function getSystemTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** 原生窗口与页面使用同一个画布令牌，避免主题切换后出现第二套底色。 */
function syncWailsNativeChrome(effective: 'light' | 'dark') {
  const bg = 'var(--td-bg-color-page)'
  document.documentElement.style.background = bg
  document.documentElement.style.minHeight = '100%'
  document.documentElement.style.colorScheme = effective === 'dark' ? 'dark' : 'light'
  if (document.body) {
    document.body.style.background = bg
    document.body.style.minHeight = '100%'
  }
  const appEl = document.getElementById('app')
  if (appEl) {
    appEl.style.background = bg
    appEl.style.minHeight = '100%'
  }
  const w = (window as unknown as {
    runtime?: {
      WindowSetDarkTheme?: () => void
      WindowSetLightTheme?: () => void
      WindowSetBackgroundColour?: (r: number, g: number, b: number, a: number) => void
    }
  }).runtime
  if (!w?.WindowSetBackgroundColour) return
  try {
    if (effective === 'dark') {
      w.WindowSetDarkTheme?.()
    } else {
      w.WindowSetLightTheme?.()
    }
    const color = getComputedStyle(document.documentElement).backgroundColor
    const channels = /^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/.exec(color)
    if (channels) w.WindowSetBackgroundColour(Number(channels[1]), Number(channels[2]), Number(channels[3]), 255)
  } catch {
    /* 非桌面壳或未注入 runtime */
  }
}

function applyTheme(mode: ThemeMode) {
  const effective = mode === 'system' ? getSystemTheme() : mode
  effectiveTheme.value = effective
  if (lastEffective === effective) return
  lastEffective = effective
  document.documentElement.setAttribute('theme-mode', effective)
  syncWailsNativeChrome(effective)
}

export function useTheme() {
  function setTheme(mode: ThemeMode): boolean {
    if (mode !== 'light' && mode !== 'dark' && mode !== 'system') return false
    currentTheme.value = mode
    savePreference(THEME_KEY, mode)
    applyTheme(mode)
    return true
  }

  return { currentTheme, effectiveTheme, setTheme }
}

/** Call once in main.ts to initialise theme and listen for OS changes. */
export function initTheme() {
  currentTheme.value = loadTheme()
  applyTheme(currentTheme.value)

  // React to OS theme changes when user chose "system"
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (currentTheme.value === 'system') {
      applyTheme('system')
    }
  })
}

/** Re-read preferences from storage (call after login / logout). */
export function reloadThemeFromStorage() {
  migratePreferencesIntoUser()
  currentTheme.value = loadTheme()
  applyTheme(currentTheme.value)
}
