import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'
import type { Asset } from './assets'
import type { Item } from './catalog'
import type { Purchase, PurchaseLine } from './purchases'
import type { DisposalCorrection, DisposalRecord, PurchaseLineCorrection } from './financial-corrections'

type FinancialAPI = typeof import('./financial-corrections')
const fetchMock = vi.fn<typeof fetch>(), signal = () => new AbortController().signal, csrf = 'A'.repeat(43)
const item: Item = { id: 'old-item', revision: 7, name: 'Reviewed quantity', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' }
const target: Item = { ...item, id: 'new-item', revision: 8, tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
const purchase: Purchase = { id: 'purchase', revision: 12, merchant_name: 'Exact merchant', currency: 'JPY', purchased_at: '2026-11-06T12:00:00.123456789+08:00', receipt_note: '' }
const line: PurchaseLine = { id: 'old-line', revision: 3, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: 'Original' }
const lineVoid = { line_id: line.id, line_revision: line.revision, item_revision: item.revision, action: 'void', replacement: null, reason: '\0Void allocation é' } as const
const lineReplace = { ...lineVoid, action: 'replace', replacement: { item_id: target.id, item_revision: target.revision, item_unit_id: 'unit-2', quantity_base: 1, paid_minor: 99 } } as const
const replacementLine: PurchaseLine = { id: 'new-line', revision: 1, purchase_id: purchase.id, item_id: target.id, item_unit_id: 'unit-2', quantity_base: 1, paid_minor: 99, note: '' }
const lineEntry: PurchaseLineCorrection = { id: 'line-correction', revision: 1, purchase_id: purchase.id, action: 'void', reason: lineVoid.reason, created_at: '2026-11-07T02:00:00.000000001Z', before: line, after: null }
const lineReplacement: PurchaseLineCorrection = { ...lineEntry, action: 'replace', after: { ...replacementLine, revision: 1 } }
const asset: Asset = { item_unit_id: 'unit', revision: 9, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' }
const original = { item_unit_id: asset.item_unit_id, kind: 'sold', disposed_at: '2026-11-07T12:00:00.123456789+08:00', realized_proceeds_minor: 200, currency: 'JPY', note: 'Original' } as const
const record: DisposalRecord = { item_unit_id: asset.item_unit_id, revision: 6, original, current: original }
const voided = { ...record, revision: 7, current: null } as const satisfies DisposalRecord
const disposalVoid = { asset_revision: asset.revision, action: 'void', replacement: null, reason: '\0Void finance é' } as const
const disposalReplace = { ...disposalVoid, action: 'replace', replacement: { kind: 'sold', disposed_at: '2026-11-07T04:00:00.123456789Z', realized_proceeds_minor: 0, currency: 'JPY' } } as const
const disposalEntry: DisposalCorrection = { id: 'disposal-correction', revision: 1, item_unit_id: asset.item_unit_id, action: 'void', reason: disposalVoid.reason, created_at: lineEntry.created_at, before: record, after: voided }
const disposalReplacement: DisposalCorrection = { ...disposalEntry, action: 'replace', after: { ...record, revision: 7, current: { ...original, realized_proceeds_minor: 0, note: '' } } }
const context = () => ({ csrfToken: csrf, idempotencyKey: 'original-financial-key', signal: signal() })
const purchaseContext = () => ({ ...context(), purchase: structuredClone(purchase), line: structuredClone(line), item: structuredClone(item), replacementItem: null })
const replacementContext = () => ({ ...purchaseContext(), replacementItem: structuredClone(target) })
const disposalContext = () => ({ ...context(), asset: structuredClone(asset), disposal: structuredClone(record) })
const reply = (value: unknown, etag?: string, status = 200) => Response.json(value, { status, headers: etag === undefined ? {} : { ETag: etag } })
function captured(): Request {
  const request = fetchMock.mock.calls.at(-1)?.[0]
  if (!(request instanceof Request)) throw new Error('Expected a captured correction request')
  return request
}

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })
const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; etag?: string; proof?: number; invoke: (api: FinancialAPI) => Promise<unknown> }[] = [
  { name: 'Purchase correction page', method: 'GET', path: '/purchases/purchase/line-corrections', result: { data: [lineEntry], next_cursor: null }, invoke: (api) => api.fetchPurchaseCorrectionsPage(purchase.id, {}, signal()) },
  { name: 'Purchase void', method: 'POST', path: '/purchases/purchase/line-corrections', body: lineVoid, result: lineEntry, etag: '"1"', proof: 12, invoke: (api) => api.correctPurchaseLine(purchase.id, lineVoid, purchaseContext()) },
  { name: 'Purchase replacement', method: 'POST', path: '/purchases/purchase/line-corrections', body: lineReplace, result: lineReplacement, etag: '"1"', proof: 12, invoke: (api) => api.correctPurchaseLine(purchase.id, lineReplace, replacementContext()) },
  { name: 'retained Disposal record', method: 'GET', path: '/units/unit/disposal-record', result: record, etag: '"6"', invoke: (api) => api.fetchDisposalRecord(asset.item_unit_id, signal()) },
  { name: 'Disposal correction page', method: 'GET', path: '/units/unit/disposal-corrections', result: { data: [disposalEntry], next_cursor: null }, invoke: (api) => api.fetchDisposalCorrectionsPage(asset.item_unit_id, {}, signal()) },
  { name: 'Disposal void', method: 'POST', path: '/units/unit/disposal-corrections', body: disposalVoid, result: disposalEntry, etag: '"1"', proof: 6, invoke: (api) => api.correctDisposal(asset.item_unit_id, disposalVoid, disposalContext()) },
  { name: 'Disposal replacement', method: 'POST', path: '/units/unit/disposal-corrections', body: disposalReplace, result: disposalReplacement, etag: '"1"', proof: 6, invoke: (api) => api.correctDisposal(asset.item_unit_id, disposalReplace, disposalContext()) },
]

describe('stateless audited financial correction transports', () => {
  it.each(cases)('sends exactly one $name with independent original proofs', async (test) => {
    fetchMock.mockResolvedValueOnce(reply(test.result, test.etag))
    expect(await test.invoke(await import('./financial-corrections'))).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = captured()
    expect(request.url).toBe(`http://localhost/api/v1${test.path}`); expect(request.method).toBe(test.method)
    expect(request.credentials).toBe('same-origin'); expect(request.cache).toBe('no-store'); expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false); expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'original-financial-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.proof === undefined ? null : `"${test.proof}"`)
    expect(request.headers.has('X-Item-Revision')).toBe(false); expect(request.headers.has('X-Unit-Revision')).toBe(false)
    if (test.body === undefined) { expect(await request.text()).toBe(''); expect(request.headers.has('Content-Type')).toBe(false) }
    else expect(await request.json()).toEqual(test.body)
  })
  it.each(cases)('never retries a failed $name on reconnect', async (test) => {
    fetchMock.mockRejectedValueOnce(new TypeError('private transport detail'))
    await expect(test.invoke(await import('./financial-corrections'))).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online')); expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each(cases)('bounds the $name deadline without replay', async (test) => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const pending = expect(test.invoke(await import('./financial-corrections'))).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      const timeout = test.method === 'GET' ? 8_000 : 30_000
      await vi.advanceTimersByTimeAsync(timeout - 1); expect(captured().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await pending; expect(fetchMock).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
  it.each(cases)('rejects missing, weak or unexpected $name ETags', async (test) => {
    fetchMock.mockResolvedValueOnce(reply(test.result, test.etag === undefined ? '"1"' : `W/${test.etag}`))
    await expect(test.invoke(await import('./financial-corrections'))).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each(cases)('rejects extended $name replies rather than repairing them', async (test) => {
    fetchMock.mockResolvedValueOnce(reply({ ...test.result as object, private_extra: true }, test.etag))
    await expect(test.invoke(await import('./financial-corrections'))).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('preserves exact paths, encoded cursors, independent scopes and decoded UTF-8 bounds', async () => {
    const api = await import('./financial-corrections')
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    const cursor = 'opaque?x=1&limit=99+#'
    for (const id of ['new', 'tree', '採購 +?# é']) {
      await api.fetchPurchaseCorrectionsPage(id, { limit: 1, cursor }, signal())
      let url = new URL(captured().url); expect(decodeURIComponent(url.pathname)).toBe(`/api/v1/purchases/${id}/line-corrections`); expect(url.searchParams.get('cursor')).toBe(cursor)
      await api.fetchDisposalCorrectionsPage(id, { limit: 1, cursor }, signal())
      url = new URL(captured().url); expect(decodeURIComponent(url.pathname)).toBe(`/api/v1/units/${id}/disposal-corrections`); expect(url.searchParams.get('cursor')).toBe(cursor)
    }
    const boundary = '😀'.repeat(512)
    await api.fetchPurchaseCorrectionsPage(purchase.id, { cursor: boundary }, signal()); expect(new URL(captured().url).search.slice(1)).toHaveLength(6151)
    await api.fetchDisposalCorrectionsPage(asset.item_unit_id, { cursor: boundary }, signal())
    for (const invalid of [boundary + 'x', '\ud800', 'x'.repeat(2049)]) {
      await expect(api.fetchPurchaseCorrectionsPage(purchase.id, { cursor: invalid }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchDisposalCorrectionsPage(asset.item_unit_id, { cursor: invalid }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
  })
  it('rejects malformed identities, contexts and complete proof mismatches before sending', async () => {
    const api = await import('./financial-corrections')
    const attempts: (() => Promise<unknown>)[] = [
      () => api.fetchPurchaseCorrectionsPage('../bad', {}, signal()),
      () => api.fetchDisposalRecord('bad/owner', signal()),
      () => api.fetchDisposalCorrectionsPage(asset.item_unit_id, { limit: 101 }, signal()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, replacement: undefined } as never, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, action: 'replace' } as never, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, reason: '' }, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, reason: '\ud800' }, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, line_revision: 3.5 }, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, line_id: 'another' }, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, { ...lineVoid, item_revision: 8 }, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), purchase: { ...purchase, id: 'other' } }),
      () => api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), line: { ...line, purchase_id: 'other' } }),
      () => api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), item: { ...item, id: 'other' } }),
      () => api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), replacementItem: target }),
      () => api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), expectedRevision: 12 } as never),
      () => api.correctPurchaseLine(purchase.id, lineReplace, purchaseContext()),
      () => api.correctPurchaseLine(purchase.id, lineReplace, { ...replacementContext(), replacementItem: { ...target, revision: 9 } }),
      () => api.correctPurchaseLine(purchase.id, { ...lineReplace, replacement: { ...lineReplace.replacement, item_unit_id: null, item_id: item.id, item_revision: item.revision } }, { ...purchaseContext(), replacementItem: { ...item, name: 'Conflicting snapshot' } }),
      () => api.correctPurchaseLine(purchase.id, { ...lineReplace, replacement: { ...lineReplace.replacement, item_id: item.id, item_revision: item.revision } }, { ...purchaseContext(), replacementItem: item }),
      () => api.correctPurchaseLine(purchase.id, { ...lineReplace, replacement: { ...lineReplace.replacement, quantity_base: 2 } } as never, replacementContext()),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalVoid, replacement: undefined } as never, disposalContext()),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalVoid, action: 'replace' } as never, disposalContext()),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalVoid, asset_revision: 10 }, disposalContext()),
      () => api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), expectedRevision: 6 } as never),
      () => api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), asset: { ...asset, item_unit_id: 'other' } }),
      () => api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), disposal: { ...record, item_unit_id: 'other' } }),
      () => api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), disposal: voided }),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalReplace, replacement: { ...disposalReplace.replacement, currency: 'USD' } }, disposalContext()),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalReplace, replacement: { ...disposalReplace.replacement, kind: 'donated', realized_proceeds_minor: 1 } } as never, disposalContext()),
      () => api.correctDisposal(asset.item_unit_id, { ...disposalReplace, replacement: { ...disposalReplace.replacement, realized_proceeds_minor: Number.MAX_SAFE_INTEGER + 1 } }, disposalContext()),
    ]
    for (const attempt of attempts) await expect(attempt()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('accepts one identical Item snapshot for both roles without introducing another proof', async () => {
    const api = await import('./financial-corrections')
    const body = { ...lineReplace, replacement: { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 5, paid_minor: 0 } } as const
    const after = { ...replacementLine, item_id: item.id, item_unit_id: null, quantity_base: 5, paid_minor: 0 }
    fetchMock.mockResolvedValueOnce(reply({ ...lineReplacement, after }, '"1"'))
    await expect(api.correctPurchaseLine(purchase.id, body, { ...purchaseContext(), replacementItem: { ...item } })).resolves.toMatchObject({ after })
    expect(await captured().json()).toEqual(body); expect(captured().headers.get('If-Match')).toBe('"12"')
  })

  it('reads retained historical representation without applying new-write policies', async () => {
    const api = await import('./financial-corrections')
    const historicalLine = { ...line, item_unit_id: 'legacy-unit', quantity_base: 3 }
    const history = { ...lineEntry, before: historicalLine }
    const historicalDisposal = { ...original, kind: 'donated', realized_proceeds_minor: 17, currency: 'ZZZ' } as const
    const historicalRecord = { ...record, original: historicalDisposal, current: historicalDisposal }
    fetchMock.mockResolvedValueOnce(reply({ data: [history], next_cursor: null }))
    fetchMock.mockResolvedValueOnce(reply(historicalRecord, '"6"'))
    expect(await api.fetchPurchaseCorrectionsPage(purchase.id, {}, signal())).toEqual({ data: [history], next_cursor: null })
    expect(await api.fetchDisposalRecord(asset.item_unit_id, signal())).toEqual(historicalRecord)
    fetchMock.mockResolvedValueOnce(reply({ ...disposalEntry, before: historicalRecord, after: { ...historicalRecord, revision: 7, current: null } }, '"1"'))
    await expect(api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), disposal: historicalRecord })).resolves.toMatchObject({ action: 'void' })
  })

  it('distinguishes zero, void and explicit reinstatement while preserving all retained nanoseconds', async () => {
    const api = await import('./financial-corrections')
    fetchMock.mockResolvedValueOnce(reply(voided, '"7"')); expect(await api.fetchDisposalRecord(asset.item_unit_id, signal())).toEqual(voided)
    const restored = { ...disposalReplacement, before: voided, after: { ...disposalReplacement.after, revision: 8 } }
    fetchMock.mockResolvedValueOnce(reply(restored, '"1"'))
    expect(await api.correctDisposal(asset.item_unit_id, disposalReplace, { ...disposalContext(), disposal: voided })).toEqual(restored)
    expect(restored.after.current).toMatchObject({ realized_proceeds_minor: 0, disposed_at: original.disposed_at })
    const changed = { ...disposalReplace, replacement: { ...disposalReplace.replacement, disposed_at: '2026-11-07T12:00:00.123456788+08:00' } }
    const changedReply = { ...disposalReplacement, after: { ...disposalReplacement.after, current: { ...original, realized_proceeds_minor: 0, note: '', disposed_at: '2026-11-07T04:00:00.123456788Z' } } }
    fetchMock.mockResolvedValueOnce(reply(changedReply, '"1"')); expect(await api.correctDisposal(asset.item_unit_id, changed, disposalContext())).toEqual(changedReply)
    const active = { ...record, revision: 7, current: { ...original, disposed_at: '2025-12-31T23:59:59.987654321+01:00' } }
    const equivalentCurrent = { ...disposalReplace, replacement: { ...disposalReplace.replacement, disposed_at: '2025-12-31T22:59:59.987654321Z' } }
    const currentReply = { ...disposalReplacement, before: active, after: { ...disposalReplacement.after, revision: 8, current: { ...original, realized_proceeds_minor: 0, note: '', disposed_at: active.current.disposed_at } } }
    fetchMock.mockResolvedValueOnce(reply(currentReply, '"1"')); expect(await api.correctDisposal(asset.item_unit_id, equivalentCurrent, { ...disposalContext(), disposal: active })).toEqual(currentReply)
    const originalAsChanged = { ...currentReply, after: { ...currentReply.after, current: { ...currentReply.after.current, disposed_at: '2026-11-07T04:00:00.123456789Z' } } }
    fetchMock.mockResolvedValueOnce(reply(originalAsChanged, '"1"')); expect(await api.correctDisposal(asset.item_unit_id, disposalReplace, { ...disposalContext(), disposal: active })).toEqual(originalAsChanged)
  })

  it('respects explicit cancellation without a hidden retry or pending write', async () => {
    const api = await import('./financial-corrections'), aborted = new AbortController(); aborted.abort()
    await expect(api.fetchDisposalRecord(asset.item_unit_id, aborted.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    await expect(api.correctPurchaseLine(purchase.id, lineVoid, { ...purchaseContext(), signal: aborted.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).not.toHaveBeenCalled()
    const controller = new AbortController()
    fetchMock.mockImplementationOnce((request) => new Promise((_resolve, reject) => {
      (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
    }))
    const pending = expect(api.correctDisposal(asset.item_unit_id, disposalVoid, { ...disposalContext(), signal: controller.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort(); await pending; window.dispatchEvent(new Event('online')); expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('freezes the complete original request and reviewed context before awaiting transport', async () => {
    const api = await import('./financial-corrections')
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    fetchMock.mockImplementationOnce(async () => { await gate; return reply(lineReplacement, '"1"') })
    const body = structuredClone(lineReplace), reviewed = replacementContext()
    const pending = api.correctPurchaseLine(purchase.id, body, reviewed)
    Object.assign(body.replacement, { paid_minor: 100 }); Object.assign(reviewed.line, { note: 'Changed later' }); Object.assign(reviewed.replacementItem, { revision: 99 })
    release(); expect(await pending).toEqual(lineReplacement); expect(await captured().json()).toEqual(lineReplace)
    let releaseDisposal!: () => void
    const disposalGate = new Promise<void>((resolve) => { releaseDisposal = resolve })
    fetchMock.mockImplementationOnce(async () => { await disposalGate; return reply(disposalReplacement, '"1"') })
    const money = structuredClone(disposalReplace), old = disposalContext()
    const disposalPending = api.correctDisposal(asset.item_unit_id, money, old)
    Object.assign(money.replacement, { realized_proceeds_minor: 101 }); Object.assign(old.asset, { revision: 100 }); Object.assign(old.disposal.original, { note: 'Changed later' })
    releaseDisposal(); expect(await disposalPending).toEqual(disposalReplacement); expect(await captured().json()).toEqual(disposalReplace)
  })

  it('rejects malformed retained records instead of inferring absence or a correction proof', async () => {
    const api = await import('./financial-corrections')
    const invalid: unknown[] = [
      { ...record, item_unit_id: 'other' }, { ...record, original: { ...original, item_unit_id: 'other' } },
      { ...record, current: { ...original, item_unit_id: 'other' } }, { ...record, current: undefined },
      { ...record, original: null }, { ...record, revision: Number.MAX_SAFE_INTEGER + 1 },
      { ...record, current: { ...original, disposed_at: '2026-02-30T00:00:00Z' } },
      { ...record, current: { ...original, note: '\ud800' } }, { ...record, extra: true },
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(reply(value, '"6"'))
      await expect(api.fetchDisposalRecord(asset.item_unit_id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(record)); await expect(api.fetchDisposalRecord(asset.item_unit_id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply(record, '"1"')); await expect(api.fetchDisposalRecord(asset.item_unit_id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'NOT_FOUND' } }, undefined, 404))
    await expect(api.fetchDisposalRecord(asset.item_unit_id, signal())).rejects.toMatchObject({ code: 'NOT_FOUND', status: 404 })
  })

  it('validates complete owned history pages and rejects malformed continuations and entries', async () => {
    const api = await import('./financial-corrections')
    const badPurchaseEntries: unknown[] = [
      { ...lineEntry, purchase_id: 'other' }, { ...lineEntry, before: { ...line, purchase_id: 'other' } },
      { ...lineEntry, before: { ...line, revision: Number.MAX_SAFE_INTEGER } }, { ...lineEntry, action: 'replace' },
      { ...lineReplacement, after: { ...replacementLine, id: line.id } }, { ...lineReplacement, after: { ...replacementLine, revision: 2 } },
      { ...lineReplacement, after: { ...replacementLine, purchase_id: 'other' } }, { ...lineEntry, reason: '' },
      { ...lineEntry, created_at: 'bad' }, { ...lineEntry, revision: 2 },
    ]
    for (const entry of badPurchaseEntries) {
      fetchMock.mockResolvedValueOnce(reply({ data: [entry], next_cursor: null }))
      await expect(api.fetchPurchaseCorrectionsPage(purchase.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    const changedOriginal = { ...original, note: 'Rewritten original' }
    const badDisposalEntries: unknown[] = [
      { ...disposalEntry, item_unit_id: 'other' }, { ...disposalEntry, before: { ...record, original: { ...original, item_unit_id: 'other' } } },
      { ...disposalEntry, after: { ...voided, item_unit_id: 'other' } }, { ...disposalEntry, after: { ...voided, original: changedOriginal } },
      { ...disposalEntry, after: { ...voided, revision: 8 } }, { ...disposalEntry, before: voided, after: { ...voided, revision: 8 } },
      { ...disposalEntry, before: { ...record, revision: Number.MAX_SAFE_INTEGER } }, { ...disposalEntry, action: 'replace' },
      { ...disposalReplacement, after: { ...disposalReplacement.after, current: { ...original, item_unit_id: 'other' } } },
    ]
    for (const entry of badDisposalEntries) {
      fetchMock.mockResolvedValueOnce(reply({ data: [entry], next_cursor: null }))
      await expect(api.fetchDisposalCorrectionsPage(asset.item_unit_id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const [invoke, entry] of [
      [(query: { limit?: number; cursor?: string }) => api.fetchPurchaseCorrectionsPage(purchase.id, query, signal()), lineEntry],
      [(query: { limit?: number; cursor?: string }) => api.fetchDisposalCorrectionsPage(asset.item_unit_id, query, signal()), disposalEntry],
    ] as const) {
      for (const page of [{ data: [entry] }, { data: [entry, entry], next_cursor: null }, { data: [], next_cursor: 'next' },
        { data: [entry], next_cursor: 'same' }, { data: [entry, entry], next_cursor: 'next' }]) {
        fetchMock.mockResolvedValueOnce(reply(page)); await expect(invoke({ limit: 1, cursor: 'same' })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
      }
      fetchMock.mockResolvedValueOnce(reply({ data: [entry], next_cursor: 'next' }))
      await expect(invoke({ limit: 1, cursor: 'same' })).resolves.toEqual({ data: [entry], next_cursor: 'next' })
    }
  })

  it('rejects frozen Purchase replies that change any reviewed original or replacement field', async () => {
    const api = await import('./financial-corrections')
    const originals: PurchaseLine[] = [
      { ...line, id: 'other' }, { ...line, revision: 4 }, { ...line, purchase_id: 'other' }, { ...line, item_id: 'other' },
      { ...line, item_unit_id: 'other' }, { ...line, quantity_base: 1 }, { ...line, paid_minor: 1 }, { ...line, note: 'different' },
    ]
    const replacements = [
      { ...replacementLine, item_id: 'other' }, { ...replacementLine, item_unit_id: 'other' },
      { ...replacementLine, quantity_base: 2 }, { ...replacementLine, paid_minor: 100 }, { ...replacementLine, note: 'different' },
    ]
    for (const before of originals) {
      fetchMock.mockResolvedValueOnce(reply({ ...lineReplacement, before }, '"1"'))
      await expect(api.correctPurchaseLine(purchase.id, lineReplace, replacementContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const after of replacements) {
      fetchMock.mockResolvedValueOnce(reply({ ...lineReplacement, after }, '"1"'))
      await expect(api.correctPurchaseLine(purchase.id, lineReplace, replacementContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const [entry, invoke] of [
      [{ ...lineReplacement, reason: 'different' }, () => api.correctPurchaseLine(purchase.id, lineReplace, replacementContext())],
      [{ ...lineReplacement, action: 'void', after: null }, () => api.correctPurchaseLine(purchase.id, lineReplace, replacementContext())],
      [{ ...lineEntry, reason: 'different' }, () => api.correctPurchaseLine(purchase.id, lineVoid, purchaseContext())],
    ] as const) {
      fetchMock.mockResolvedValueOnce(reply(entry, '"1"')); await expect(invoke()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('rejects frozen Disposal replies that change reviewed history or any proposed value', async () => {
    const api = await import('./financial-corrections')
    const differentRecords: DisposalRecord[] = [
      { ...record, revision: 5 }, { ...record, original: { ...original, kind: 'other' } },
      { ...record, original: { ...original, disposed_at: '2026-11-07T04:00:00.123456789Z' } },
      { ...record, original: { ...original, realized_proceeds_minor: 0 } }, { ...record, original: { ...original, currency: 'USD' } },
      { ...record, original: { ...original, note: '' } }, { ...record, current: null },
      { ...record, current: { ...original, note: 'different' } },
    ]
    for (const before of differentRecords) {
      const after = { ...disposalReplacement.after, revision: before.revision + 1, original: before.original }
      fetchMock.mockResolvedValueOnce(reply({ ...disposalReplacement, before, after }, '"1"'))
      await expect(api.correctDisposal(asset.item_unit_id, disposalReplace, disposalContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    const correct = disposalReplacement.after.current!
    const changed = [
      { ...correct, kind: 'other' }, { ...correct, disposed_at: '2026-11-07T04:00:00.123456789Z' },
      { ...correct, realized_proceeds_minor: 1 }, { ...correct, currency: 'USD' }, { ...correct, note: 'different' },
    ]
    for (const current of changed) {
      fetchMock.mockResolvedValueOnce(reply({ ...disposalReplacement, after: { ...disposalReplacement.after, current } }, '"1"'))
      await expect(api.correctDisposal(asset.item_unit_id, disposalReplace, disposalContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const [entry, invoke] of [
      [{ ...disposalReplacement, reason: 'different' }, () => api.correctDisposal(asset.item_unit_id, disposalReplace, disposalContext())],
      [{ ...disposalEntry, reason: 'different' }, () => api.correctDisposal(asset.item_unit_id, disposalVoid, disposalContext())],
      [{ ...disposalEntry, action: 'replace', after: disposalReplacement.after }, () => api.correctDisposal(asset.item_unit_id, disposalVoid, disposalContext())],
    ] as const) {
      fetchMock.mockResolvedValueOnce(reply(entry, '"1"')); await expect(invoke()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('surfaces allowlisted server conflicts without retaining arbitrary messages', async () => {
    const api = await import('./financial-corrections')
    for (const [code, invoke] of [
      ['PURCHASE_LINE_VOIDED', () => api.correctPurchaseLine(purchase.id, lineVoid, purchaseContext())],
      ['ASSET_HISTORY_LOCKED', () => api.correctDisposal(asset.item_unit_id, disposalVoid, disposalContext())],
      ['IDEMPOTENCY_CONFLICT', () => api.correctDisposal(asset.item_unit_id, disposalVoid, disposalContext())],
    ] as const) {
      fetchMock.mockResolvedValueOnce(reply({ error: { code, message: 'private server details', details: { secret: 'hidden' } } }, undefined, 409))
      const error = await invoke().catch((value: unknown) => value)
      expect(error).toMatchObject({ code, status: 409, message: 'HomeBox API request failed' }); expect(JSON.stringify(error)).not.toContain('hidden')
    }
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })
})
