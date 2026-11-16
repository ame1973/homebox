import { act, renderHook } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { advanceBarcodeHandoff, clearBarcodeHandoff, readBarcodeHandoff, retainBarcodeHandoff, startBarcodeHandoff, useBarcodeHandoff } from './scan-handoff'

afterEach(() => { clearBarcodeHandoff() })

it('keeps the exact selected barcode only in memory under an opaque route marker', () => {
  const writes = [vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(history, 'pushState'), vi.spyOn(history, 'replaceState')]
  const draft = { rawValue: '\0é\r\n001', symbology: 'CODE_128' as const }
  const token = startBarcodeHandoff(draft)
  expect(token).not.toContain(draft.rawValue)
  expect(readBarcodeHandoff(token, '/items/new')).toEqual(draft)
  expect(readBarcodeHandoff(token, '/items/new?unknown=1')).toBeUndefined()
  expect(readBarcodeHandoff('other', '/items/new')).toBeUndefined()
  expect(readBarcodeHandoff(token, '/items/new')).not.toBe(draft)
  expect(Object.isFrozen(readBarcodeHandoff(token, '/items/new'))).toBe(true)
  for (const write of writes) expect(write).not.toHaveBeenCalled()
})

it('advances only the original marker to the created Item barcode editor', () => {
  const token = startBarcodeHandoff({ rawValue: '036000291452' })
  expect(advanceBarcodeHandoff('wrong', '/items/a/barcodes/new')).toBe(false)
  expect(advanceBarcodeHandoff(token, '/items/a/barcodes/new')).toBe(true)
  expect(advanceBarcodeHandoff(token, '/items/b/barcodes/new')).toBe(false)
  expect(readBarcodeHandoff(token, '/items/new')).toBeUndefined()
  expect(readBarcodeHandoff(token, '/items/a/barcodes/new')).toEqual({ rawValue: '036000291452' })
  expect(readBarcodeHandoff(token, '/items/b/barcodes/new')).toBeUndefined()
})

it('updates mounted consumers when a handoff advances or is cleared', () => {
  const token = startBarcodeHandoff({ rawValue: '012345678905', symbology: 'UPC_A' })
  const hook = renderHook(({ path }) => useBarcodeHandoff(token, path), { initialProps: { path: '/items/new' } })
  expect(hook.result.current?.rawValue).toBe('012345678905')
  act(() => { expect(advanceBarcodeHandoff(token, '/items/a/barcodes/new')).toBe(true) })
  expect(hook.result.current).toBeUndefined()
  hook.rerender({ path: '/items/a/barcodes/new' })
  expect(hook.result.current?.rawValue).toBe('012345678905')
  act(() => { clearBarcodeHandoff() })
  expect(hook.result.current).toBeUndefined()
})

it('drops the handoff on a different route, session clear, or newer selection', () => {
  const first = startBarcodeHandoff({ rawValue: 'first' })
  retainBarcodeHandoff(first, '/items/new')
  expect(readBarcodeHandoff(first, '/items/new')).toEqual({ rawValue: 'first' })
  const second = startBarcodeHandoff({ rawValue: 'second', symbology: 'UPC_A' })
  clearBarcodeHandoff(first)
  expect(readBarcodeHandoff(first, '/items/new')).toBeUndefined()
  expect(readBarcodeHandoff(second, '/items/new')?.rawValue).toBe('second')
  retainBarcodeHandoff(second, '/items')
  expect(readBarcodeHandoff(second, '/items/new')).toBeUndefined()
  const third = startBarcodeHandoff({ rawValue: 'third' })
  clearBarcodeHandoff()
  expect(readBarcodeHandoff(third, '/items/new')).toBeUndefined()
})
