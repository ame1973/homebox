import { create } from 'zustand'

export type Theme = 'system' | 'light' | 'dark'
export const themeKey = 'homebox.theme'

export function isTheme(value: unknown): value is Theme {
  return value === 'system' || value === 'light' || value === 'dark'
}

function storedTheme(): Theme {
  try {
    const value = localStorage.getItem(themeKey)
    return isTheme(value) ? value : 'system'
  } catch {
    return 'system'
  }
}

export const useTheme = create<{ theme: Theme; setTheme: (theme: Theme) => void }>((set) => ({
  theme: storedTheme(),
  setTheme: (theme) => {
    try { localStorage.setItem(themeKey, theme) } catch { /* Storage is optional for UI preferences. */ }
    set({ theme })
  },
}))
