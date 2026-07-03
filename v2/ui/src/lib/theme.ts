export type Theme = 'dark' | 'light'

const STORAGE_KEY = 'norn-theme'

function systemTheme(): Theme {
  if (typeof window === 'undefined') return 'dark'
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

export function getInitialTheme(): Theme {
  if (typeof window === 'undefined') return 'dark'
  const stored = window.localStorage.getItem(STORAGE_KEY)
  return stored === 'light' || stored === 'dark' ? stored : systemTheme()
}

export function applyTheme(theme: Theme): void {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.theme = theme
}

export function initializeTheme(): Theme {
  const theme = getInitialTheme()
  applyTheme(theme)
  return theme
}

export function setStoredTheme(theme: Theme): void {
  applyTheme(theme)
  if (typeof window !== 'undefined') {
    window.localStorage.setItem(STORAGE_KEY, theme)
  }
}
