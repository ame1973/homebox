import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi, type Mock } from 'vitest'
import { monitorShell, type ShellObservation, type ShellPhase } from '@/lib/shell-monitor'
import { messages } from '@/lib/messages'
import { ShellIndicator } from './shell-indicator'

vi.mock('@/lib/shell-monitor', () => ({ monitorShell: vi.fn() }))
const createMonitor = vi.mocked(monitorShell)
const text = messages.shell
let publish!: (value: ShellObservation) => void
let check: Mock<() => boolean>, prepare: Mock<() => boolean>, stop: Mock<() => void>

beforeEach(() => {
  vi.clearAllMocks()
  check = vi.fn().mockReturnValue(true); prepare = vi.fn().mockReturnValue(true); stop = vi.fn()
  createMonitor.mockImplementation((report) => { publish = report; report({ phase: 'checking', busy: true }); return { check, prepare, stop } })
})

function show(phase: ShellPhase, busy = false) { act(() => publish({ phase, busy })) }

it('shows each bounded state and stops the observer on unmount', () => {
  const view = render(<ShellIndicator />)
  const region = screen.getByRole('complementary', { name: text.label })
  expect(region).toHaveTextContent(text.phases.checking)
  for (const phase of ['installing', 'ready', 'available', 'waiting', 'outdated', 'missing', 'error', 'foreign', 'unsupported', 'paused'] as const) {
    show(phase)
    expect(region).toHaveTextContent(text.phases[phase])
    expect(region).toHaveTextContent(text.help[phase])
  }
  view.unmount()
  expect(stop).toHaveBeenCalledTimes(1)
})

it('only prepares from an explicit available, missing or failed state', async () => {
  const user = userEvent.setup()
  render(<ShellIndicator />)
  for (const [phase, label] of [['available', text.prepareUpdate], ['missing', text.prepare], ['error', text.retry]] as const) {
    show(phase)
    await user.click(screen.getByRole('button', { name: label }))
  }
  expect(prepare).toHaveBeenCalledTimes(3)
  expect(check).not.toHaveBeenCalled()
  for (const phase of ['ready', 'waiting', 'outdated', 'foreign'] as const) {
    show(phase)
    await user.click(screen.getByRole('button', { name: text.check }))
  }
  expect(check).toHaveBeenCalledTimes(4)
  expect(prepare).toHaveBeenCalledTimes(3)
})

it('retains focus on the same control through preparation and waiting without duplicate actions', async () => {
  const user = userEvent.setup()
  render(<ShellIndicator />)
  show('available')
  const button = screen.getByRole('button', { name: text.prepareUpdate })
  await user.tab()
  expect(button).toHaveFocus()
  await user.keyboard('{Enter}')
  expect(prepare).toHaveBeenCalledTimes(1)
  show('installing', true)
  expect(button).toHaveFocus()
  expect(button).toHaveAttribute('aria-disabled', 'true')
  await user.keyboard('{Enter}')
  expect(prepare).toHaveBeenCalledTimes(1); expect(check).not.toHaveBeenCalled()
  show('waiting')
  expect(screen.getByRole('button', { name: text.check })).toBe(button)
  expect(button).toHaveFocus()
})

it('does not offer an action when unsupported or run one while paused', async () => {
  const user = userEvent.setup()
  render(<ShellIndicator />)
  show('unsupported')
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
  show('paused')
  const button = screen.getByRole('button', { name: text.check })
  expect(button).toHaveAttribute('aria-disabled', 'true')
  await user.click(button)
  expect(check).not.toHaveBeenCalled(); expect(prepare).not.toHaveBeenCalled()
})
