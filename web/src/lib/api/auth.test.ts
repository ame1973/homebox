import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type AuthAPI = typeof import('./auth')
const fetchMock = vi.fn<typeof fetch>()
const admin = { id: 'admin-1', revision: 1, username: '管理員', must_change_password: true }
const csrf = 'A'.repeat(43)
const grant = 'C'.repeat(42) + 'A'
const credentials = { username: '管理員', password: '  private 密碼 😀  ' }
const passwordChange = { old_password: credentials.password, new_password: '  a different 密碼 😀  ' }
const ack = { ok: true, operation_id: 'event-1', revision: 2 }
const reauth = { admin, reauth_token: grant, expires_at: '2026-11-05T00:00:00Z' }
const signal = () => new AbortController().signal
const writeContext = () => ({ csrfToken: csrf, idempotencyKey: 'explicit-request-key', signal: signal() })

beforeEach(() => {
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

function abortableFetch() {
  fetchMock.mockImplementation((input) => new Promise((_, reject) => {
    const requestSignal = (input as Request).signal
    const abort = () => reject(new DOMException('Aborted', 'AbortError'))
    if (requestSignal.aborted) abort()
    else requestSignal.addEventListener('abort', abort, { once: true })
  }))
}

describe('typed authentication transport', () => {
  const cases: { name: string; path: string; method: string; invoke: (api: AuthAPI) => Promise<unknown>; body?: unknown; result: unknown; protected?: boolean }[] = [
    { name: 'current account', path: '/auth/me', method: 'GET', invoke: (api) => api.fetchAdmin(signal()), result: admin },
    { name: 'CSRF', path: '/auth/csrf', method: 'GET', invoke: (api) => api.fetchCSRF(signal()), result: { token: csrf } },
    { name: 'setup', path: '/auth/setup', method: 'POST', body: { ...credentials, bootstrap_token: 'B'.repeat(42) + 'A' }, invoke: (api) => api.setupAdmin({ ...credentials, bootstrap_token: 'B'.repeat(42) + 'A' }, signal()), result: admin },
    { name: 'login', path: '/auth/login', method: 'POST', body: credentials, invoke: (api) => api.login(credentials, signal()), result: admin },
    { name: 'password change', path: '/auth/password', method: 'POST', body: passwordChange, invoke: (api) => api.changePassword(passwordChange, writeContext()), result: ack, protected: true },
    { name: 'reauthentication', path: '/auth/reauth', method: 'POST', body: credentials, invoke: (api) => api.reauthenticate(credentials, writeContext()), result: reauth, protected: true },
    { name: 'logout', path: '/auth/logout', method: 'POST', invoke: (api) => api.logout(writeContext()), result: { ok: true, operation_id: 'event-2' }, protected: true },
  ]

  it.each(cases)('sends one same-origin $name request with exact body/header boundaries', async (test) => {
    fetchMock.mockResolvedValue(Response.json(test.result))
    const api = await import('./auth')
    const result = await test.invoke(api)
    expect(result).toEqual(test.name === 'CSRF' ? csrf : test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.url).toBe(`http://localhost/api/v1${test.path}`)
    expect(request.method).toBe(test.method)
    expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin')
    expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false)
    expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'POST' ? 'http://localhost' : null)
    expect(request.headers.get('X-CSRF-Token')).toBe(test.protected ? csrf : null)
    expect(request.headers.get('Idempotency-Key')).toBe(test.protected ? 'explicit-request-key' : null)
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.protected ? version : null)
    if (test.body === undefined) {
      expect(await request.text()).toBe('')
      expect(request.headers.has('Content-Type')).toBe(false)
    } else {
      expect(request.headers.get('Content-Type')).toBe('application/json')
      expect(await request.json()).toEqual(test.body)
    }
  })

  it('returns tokens only to the caller and never reads/writes browser credential storage', async () => {
    const localRead = vi.spyOn(window.localStorage, 'getItem')
    const localWrite = vi.spyOn(window.localStorage, 'setItem')
    const sessionRead = vi.spyOn(window.sessionStorage, 'getItem')
    const sessionWrite = vi.spyOn(window.sessionStorage, 'setItem')
    const cookieRead = vi.spyOn(document, 'cookie', 'get')
    const cookieWrite = vi.spyOn(document, 'cookie', 'set')
    const databaseOpen = vi.fn()
    const cacheOpen = vi.fn()
    vi.stubGlobal('indexedDB', { open: databaseOpen })
    vi.stubGlobal('caches', { open: cacheOpen })
    fetchMock.mockResolvedValueOnce(Response.json({ token: csrf })).mockResolvedValueOnce(Response.json({ token: grant })).mockResolvedValueOnce(Response.json(reauth))
    const api = await import('./auth')
    expect(await api.fetchCSRF(signal())).toBe(csrf)
    expect(await api.fetchCSRF(signal())).toBe(grant)
    expect(await api.reauthenticate(credentials, writeContext())).toEqual(reauth)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    for (const spy of [localRead, localWrite, sessionRead, sessionWrite, cookieRead, cookieWrite, databaseOpen, cacheOpen]) expect(spy).not.toHaveBeenCalled()
  })

  it('keeps the caller-supplied key on explicit retries without minting, refreshing, or queueing', async () => {
    fetchMock.mockImplementation(async () => Response.json(ack))
    const { changePassword } = await import('./auth')
    const context = writeContext()
    expect(await changePassword(passwordChange, context)).toEqual(ack)
    expect(await changePassword(passwordChange, context)).toEqual(ack)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const first = fetchMock.mock.calls[0]?.[0] as Request
    const second = fetchMock.mock.calls[1]?.[0] as Request
    expect(second.headers.get('Idempotency-Key')).toBe(first.headers.get('Idempotency-Key'))
    expect(second.headers.get('X-CSRF-Token')).toBe(first.headers.get('X-CSRF-Token'))
    expect(await second.text()).toBe(await first.text())
  })

  it('does not renew or reject a legitimately frozen but expired reauthentication result', async () => {
    fetchMock.mockResolvedValue(Response.json(reauth))
    const { reauthenticate } = await import('./auth')
    expect(await reauthenticate(credentials, writeContext())).toEqual(reauth)
  })
})

describe('runtime response validation', () => {
  it.each([
    null, {}, { ...admin, id: '' }, { ...admin, revision: 0 }, { ...admin, revision: 1.2 },
    { ...admin, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...admin, username: 'x'.repeat(65) },
    { ...admin, must_change_password: 'true' }, { ...admin, session_token: 'must not be accepted' },
    { ...admin, password_hash: 'must not be accepted' }, { ...admin, username: '\uD800' },
  ])('rejects invalid or secret-bearing account data %j', async (body) => {
    fetchMock.mockResolvedValue(Response.json(body))
    const { fetchAdmin } = await import('./auth')
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ name: 'ApiError', code: 'INVALID_RESPONSE', status: 200 })
  })

  it.each([201, 204])('does not treat unexpected successful status %i as authentication', async (status) => {
    fetchMock.mockResolvedValue(status === 204 ? new Response(null, { status }) : Response.json(admin, { status }))
    const { fetchAdmin } = await import('./auth')
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status })
  })

  it('accepts code-point-bounded Unicode account names and safe integer revisions', async () => {
    const body = { ...admin, username: '😀'.repeat(64), revision: Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValue(Response.json(body))
    const { fetchAdmin } = await import('./auth')
    expect(await fetchAdmin(signal())).toEqual(body)
  })

  it.each([{ token: 'short' }, { token: csrf, extra: 'x' }, { token: 123 }, { token: '!'.repeat(43) }, { token: ' '.repeat(43) }])('rejects an invalid CSRF envelope %j', async (body) => {
    fetchMock.mockResolvedValue(Response.json(body))
    const { fetchCSRF } = await import('./auth')
    await expect(fetchCSRF(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each([{ ok: false, operation_id: 'event' }, { ok: true }, { ok: true, operation_id: '' }, { ...ack, revision: 0 }, { ...ack, session_token: 'not allowed' }])('rejects invalid acknowledgements %j', async (body) => {
    fetchMock.mockResolvedValue(Response.json(body))
    const { changePassword } = await import('./auth')
    await expect(changePassword(passwordChange, writeContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each([{ ...reauth, expires_at: '2026-11-05' }, { ...reauth, expires_at: '2026-11-05T00:00Z' }, { ...reauth, reauth_token: 'short' }, { ...reauth, admin: {} }, { ...reauth, session_token: 'not allowed' }])('rejects invalid capabilities %j', async (body) => {
    fetchMock.mockResolvedValue(Response.json(body))
    const { reauthenticate } = await import('./auth')
    await expect(reauthenticate(credentials, writeContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
})

describe('safe errors and no automatic replay', () => {
  it.each([
    [401, 'AUTHENTICATION_REQUIRED'], [403, 'CSRF_REJECTED'], [403, 'PASSWORD_CHANGE_REQUIRED'],
    [403, 'CREDENTIALS_REJECTED'], [403, 'ORIGIN_REJECTED'], [409, 'IDEMPOTENCY_CONFLICT'],
    [409, 'SETUP_CLOSED'], [422, 'VALIDATION_ERROR'], [426, 'CLIENT_UPGRADE_REQUIRED'],
    [429, 'RATE_LIMITED'], [503, 'SERVICE_UNAVAILABLE'],
  ])('keeps safe %i/%s metadata without arbitrary diagnostics or replay', async (status, code) => {
    vi.useFakeTimers()
    const secret = 'private credential must not escape'
    fetchMock.mockResolvedValue(Response.json({ error: { code, message: secret, details: { password: secret }, request_id: 'request-123' } }, { status: Number(status), headers: { 'Retry-After': '15' } }))
    const { logout, ApiError } = await import('./auth')
    let caught: unknown
    try { await logout(writeContext()) } catch (error) { caught = error }
    expect(caught).toBeInstanceOf(ApiError)
    expect(caught).toMatchObject({ code, status, retryAfterSeconds: 15, requestId: 'request-123' })
    expect(String(caught)).not.toContain(secret)
    expect(JSON.stringify(caught)).not.toContain(secret)
    expect(caught).not.toHaveProperty('cause')
    expect(caught).not.toHaveProperty('response')
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each([null, {}, { error: { code: '<script>not a code</script>' } }, { error: { code: 'AUTHENTICATION_REQUIRED', request_id: 'invalid diagnostic with spaces' } }])('discards malformed error bodies %j while preserving status', async (body) => {
    fetchMock.mockResolvedValue(Response.json(body, { status: 401 }))
    const { fetchAdmin } = await import('./auth')
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'UNKNOWN_ERROR', status: 401, requestId: undefined })
  })

  it.each(['-1', '3601', '999999999999', 'NaN', '1 second', 'Wed, 05 Nov 2026 00:00:00 GMT', ''])('ignores unsupported or unbounded Retry-After %s', async (retry) => {
    fetchMock.mockResolvedValue(Response.json({ error: { code: 'RATE_LIMITED' } }, { status: 429, headers: { 'Retry-After': retry } }))
    const { fetchAdmin } = await import('./auth')
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'RATE_LIMITED', retryAfterSeconds: undefined })
  })

  it('handles absent Retry-After, malformed JSON, and private network exceptions safely', async () => {
    const { fetchAdmin } = await import('./auth')
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'AUTHENTICATION_REQUIRED' } }, { status: 401 }))
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'AUTHENTICATION_REQUIRED', retryAfterSeconds: undefined })
    fetchMock.mockResolvedValueOnce(new Response('<html>private proxy output</html>'))
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockRejectedValueOnce(new TypeError('private hostname or transport diagnostics'))
    await expect(fetchAdmin(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR', message: 'HomeBox API request failed' })
  })
})

describe('cancellation and explicit deadlines', () => {
  it('never sends a pre-canceled write or retains its private cancellation reason', async () => {
    const controller = new AbortController()
    controller.abort('private cancellation reason')
    const { login } = await import('./auth')
    await expect(login(credentials, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED', message: 'HomeBox API request failed' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('cancels in flight and removes deadline/listener resources', async () => {
    vi.useFakeTimers()
    abortableFetch()
    const controller = new AbortController()
    const remove = vi.spyOn(controller.signal, 'removeEventListener')
    const { login } = await import('./auth')
    const result = login(credentials, controller.signal)
    const rejection = expect(result).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort()
    await rejection
    expect(vi.getTimerCount()).toBe(0)
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it.each([['read', 8_000], ['write', 30_000]] as const)('gives a %s its bounded deadline, never a replay', async (kind, deadline) => {
    vi.useFakeTimers()
    abortableFetch()
    const api = await import('./auth')
    const result = kind === 'read' ? api.fetchAdmin(signal()) : api.login(credentials, signal())
    const rejection = expect(result).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
    await vi.advanceTimersByTimeAsync(deadline - 1)
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(1)
    await rejection
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('discards a late success even if the transport ignores cancellation', async () => {
    vi.useFakeTimers()
    let complete: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { complete = resolve }))
    const controller = new AbortController()
    const { fetchAdmin } = await import('./auth')
    const result = fetchAdmin(controller.signal)
    const rejection = expect(result).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort()
    complete?.(Response.json(admin))
    await rejection
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('Unicode contract bounds', () => {
  it.each([
    ['a', 1, 1, true], ['', 1, 64, false], ['😀'.repeat(6), 12, 256, false],
    ['😀'.repeat(12), 12, 256, true], ['😀'.repeat(256), 12, 256, true], ['😀'.repeat(257), 12, 256, false],
    ['\uD800', 1, 64, false], ['\uDC00', 1, 64, false], ['é', 1, 1, true], ['é', 1, 1, false],
    ['  a  ', 5, 5, true], ['abc', 1, 2, false],
  ] as const)('validates %j with code-point bounds %i..%i', async (value, minimum, maximum, valid) => {
    const { validAuthText } = await import('./auth')
    expect(validAuthText(value, minimum, maximum)).toBe(valid)
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
