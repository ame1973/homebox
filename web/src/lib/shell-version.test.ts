import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { documentShellWorker, readShellVersion, shellWorkerPath } from './shell-version'

const path = '/pwa-worker-0123456789abcdef01234567.js'
const body = `{"worker":"${path}"}\n`
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const response = (value: BodyInit | null = body, options: ResponseInit = {}) => new Response(value, {
  status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' }, ...options,
})

beforeEach(() => { fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })
afterEach(() => { document.head.querySelectorAll('meta[name="homebox-shell-worker"]').forEach((meta) => meta.remove()) })

describe('public shell version boundary', () => {
  it('accepts only the exact root-scoped versioned worker path', () => {
    expect(shellWorkerPath(path)).toBe(path)
    for (const invalid of ['', `${path}\n`, `${path}\r`, '/pwa-worker-0123456789abcdef01234567.js?x=1', '/pwa-worker-0123456789abcdef01234567.js#x',
      '/pwa-worker-0123456789ABCDEF01234567.js', '/x/pwa-worker-0123456789abcdef01234567.js',
      '//other.test/pwa-worker-0123456789abcdef01234567.js', '__HOMEBOX_SHELL_WORKER__', null, 1]) {
      expect(shellWorkerPath(invalid)).toBeUndefined()
    }
  })

  it('requires exactly one valid worker meta element in the current document', () => {
    expect(documentShellWorker()).toBeUndefined()
    const meta = document.createElement('meta')
    meta.name = 'homebox-shell-worker'; meta.content = path; document.head.append(meta)
    expect(documentShellWorker()).toBe(path)
    meta.content = '__HOMEBOX_SHELL_WORKER__'
    expect(documentShellWorker()).toBeUndefined()
    meta.content = path
    document.head.append(meta.cloneNode())
    expect(documentShellWorker()).toBeUndefined()
  })

  it('reads exactly one network-only public descriptor without cookies or redirects', async () => {
    fetchMock.mockResolvedValueOnce(response())
    expect(await readShellVersion(signal())).toBe(path)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.url).toBe('http://localhost/pwa-version.json')
    expect(request.method).toBe('GET')
    expect(request.credentials).toBe('omit')
    expect(request.cache).toBe('no-store')
    expect(request.redirect).toBe('error')
    expect(request.mode).toBe('same-origin')
    expect(request.referrerPolicy).toBe('no-referrer')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('validates the actual streamed bytes across chunk boundaries', async () => {
    const bytes = new TextEncoder().encode(body)
    fetchMock.mockResolvedValueOnce(response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(bytes.slice(0, 7)); controller.enqueue(bytes.slice(7)); controller.close() },
    })))
    expect(await readShellVersion(signal())).toBe(path)
  })

  it('rejects invalid status, MIME, cache policy and missing body', async () => {
    for (const invalid of [
      response(body, { status: 201 }), response(null, { status: 204 }),
      response(body, { headers: { 'Content-Type': 'text/html', 'Cache-Control': 'no-store' } }),
      response(body, { headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-cache' } }),
      response(null),
    ]) {
      fetchMock.mockResolvedValueOnce(invalid)
      await expect(readShellVersion(signal())).rejects.toThrow()
    }
  })

  it('rejects extra keys, duplicate or invalid worker paths, whitespace variants, malformed UTF-8 and oversized bytes', async () => {
    const invalid = [
      '', `{"worker":"${path}","extra":1}\n`, `{"worker":"${path}","worker":"${path}"}\n`,
      `{"worker":"${path}?x"}\n`, `{"worker": "${path}"}\n`, body.trim(), `${body}\n`, `${body}\r`,
      new Uint8Array([0xff]), new Uint8Array(257),
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(response(value))
      await expect(readShellVersion(signal())).rejects.toThrow()
    }
  })

  it('does not return a reply that settles after caller cancellation', async () => {
    let release!: (value: Response) => void
    fetchMock.mockReturnValueOnce(new Promise((resolve) => { release = resolve }))
    const controller = new AbortController()
    const result = expect(readShellVersion(controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    controller.abort(); release(response())
    await result
  })

  it('uses an eight-second deadline across headers and streamed body and honors cancellation', async () => {
    vi.useFakeTimers()
    fetchMock.mockImplementation((input) => {
      const request = input as Request
      return Promise.resolve(response(new ReadableStream<Uint8Array>({
        start(controller) { request.signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')), { once: true }) },
      })))
    })
    const timed = expect(readShellVersion(signal())).rejects.toMatchObject({ name: 'TimeoutError' })
    await vi.advanceTimersByTimeAsync(7999)
    const first = fetchMock.mock.calls[0]![0] as Request
    expect(first.signal.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1); await timed
    const controller = new AbortController()
    const canceled = expect(readShellVersion(controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    await vi.advanceTimersByTimeAsync(0); controller.abort(); await canceled
    const early = new AbortController(); early.abort()
    await expect(readShellVersion(early.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })
})
