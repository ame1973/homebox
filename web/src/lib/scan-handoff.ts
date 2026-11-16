import { useSyncExternalStore } from 'react'
import type { BarcodeSearchQuery } from '@/lib/api/barcode-search'

export type ScanBarcodeDraft = Readonly<{ rawValue: string; symbology?: BarcodeSearchQuery['symbology'] }>
// Each of 512 astral code points can use two six-character JSON Unicode escapes.
export const barcodeJSONInputMax = 512 * 12 + 2
type Handoff = Readonly<{ token: string; path: string; draft: ScanBarcodeDraft }>

// The barcode stays only in this page's memory, never in URL/history state or a
// persistent write queue. The opaque marker binds each explicit navigation.
let current: Handoff | undefined
const listeners = new Set<() => void>()
function update(value: Handoff | undefined) {
  if (current === value) return
  current = value
  for (const listener of listeners) listener()
}
function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}
function snapshot() { return current }

export function useBarcodeHandoff(token: unknown, path: string): ScanBarcodeDraft | undefined {
  const value = useSyncExternalStore(subscribe, snapshot, () => undefined)
  return typeof token === 'string' && value?.token === token && value.path === path ? value.draft : undefined
}

export function startBarcodeHandoff(draft: ScanBarcodeDraft): string {
  const token = crypto.randomUUID()
  update(Object.freeze({ token, path: '/items/new', draft: Object.freeze({ ...draft }) }))
  return token
}

export function readBarcodeHandoff(token: unknown, path: string): ScanBarcodeDraft | undefined {
  return typeof token === 'string' && current?.token === token && current.path === path ? current.draft : undefined
}

export function advanceBarcodeHandoff(token: unknown, path: string): boolean {
  if (typeof token !== 'string' || !current || current.token !== token || current.path !== '/items/new') return false
  update(Object.freeze({ ...current, path }))
  return true
}

export function clearBarcodeHandoff(token?: string) {
  if (token === undefined || token === current?.token) update(undefined)
}

export function retainBarcodeHandoff(token: unknown, path: string) {
  if (current && (token !== current.token || path !== current.path)) update(undefined)
}
