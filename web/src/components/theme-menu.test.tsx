import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { ThemeMenu } from './theme-menu'
import { useTheme } from '@/lib/theme'
import { messages } from '@/lib/messages'

beforeEach(() => { useTheme.setState({ theme: 'system' }); localStorage.clear() })

it('applies and persists a selection using the accessible radio menu', async () => {
  render(<ThemeMenu />)
  await userEvent.click(screen.getByRole('button', { name: messages.theme.label }))
  await userEvent.click(screen.getByRole('menuitemradio', { name: messages.theme.dark }))
  expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  expect(localStorage.getItem('homebox.theme')).toBe('dark')
  await userEvent.click(screen.getByRole('button', { name: messages.theme.label }))
  expect(screen.getByRole('menuitemradio', { name: messages.theme.dark })).toHaveAttribute('aria-checked', 'true')
  await userEvent.click(screen.getByRole('menuitemradio', { name: messages.theme.light }))
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')
})

it('follows OS changes in system mode and removes the media listener on unmount', () => {
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  vi.spyOn(window, 'matchMedia').mockReturnValue(media)
  const view = render(<ThemeMenu />)
  const callback = vi.mocked(media.addEventListener).mock.calls[0]?.[1]
  expect(callback).toBeTypeOf('function')
  act(() => {
    Object.assign(media, { matches: true })
    if (typeof callback === 'function') callback.call(media, new Event('change') as MediaQueryListEvent)
  })
  expect(document.documentElement).toHaveClass('dark')
  act(() => useTheme.getState().setTheme('light'))
  expect(document.documentElement).not.toHaveClass('dark')
  view.unmount()
  expect(media.removeEventListener).toHaveBeenCalledWith('change', callback)
})
