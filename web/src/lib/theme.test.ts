import { beforeEach, expect, it, vi } from 'vitest'

beforeEach(() => { vi.resetModules(); localStorage.clear() })

it.each(['system', 'light', 'dark'])('loads and persists the %s preference', async (theme) => {
  localStorage.setItem('homebox.theme', theme)
  const { useTheme } = await import('./theme')
  expect(useTheme.getState().theme).toBe(theme)
  useTheme.getState().setTheme('dark')
  expect(localStorage.getItem('homebox.theme')).toBe('dark')
})

it('ignores an invalid stored value and rejects non-theme values', async () => {
  localStorage.setItem('homebox.theme', 'injected')
  const { useTheme, isTheme } = await import('./theme')
  expect(useTheme.getState().theme).toBe('system')
  expect(isTheme(null)).toBe(false)
  expect(isTheme('injected')).toBe(false)
})

it('works in memory when preference storage is unavailable', async () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Quota') })
  const { useTheme } = await import('./theme')
  expect(useTheme.getState().theme).toBe('system')
  useTheme.getState().setTheme('light')
  expect(useTheme.getState().theme).toBe('light')
})
