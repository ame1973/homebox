import { withRequestDeadline } from '@/lib/api/client'

const workerPath = /^\/pwa-worker-[a-f0-9]{24}\.js$/
const descriptor = /^\{"worker":"(\/pwa-worker-[a-f0-9]{24}\.js)"\}\n$/

export function shellWorkerPath(value: unknown): string | undefined {
  return typeof value === 'string' && workerPath.test(value) ? value : undefined
}

export function documentShellWorker(): string | undefined {
  const elements = document.querySelectorAll<HTMLMetaElement>('meta[name="homebox-shell-worker"]')
  return elements.length === 1 ? shellWorkerPath(elements[0]?.getAttribute('content')) : undefined
}

// This is a public deployment observation, not a client/API compatibility proof.
export async function readShellVersion(signal: AbortSignal): Promise<string> {
  return withRequestDeadline(signal, async (current) => {
    const request = new Request(new URL('/pwa-version.json', window.location.origin), {
      method: 'GET', mode: 'same-origin', cache: 'no-store', credentials: 'omit', redirect: 'error',
      referrerPolicy: 'no-referrer', signal: current,
    })
    const response = await fetch(request)
    const mime = (response.headers.get('Content-Type') ?? '').split(';', 1)[0]?.trim().toLowerCase()
    const cache = (response.headers.get('Cache-Control') ?? '').split(',').map((value) => value.trim().toLowerCase())
    if (response.status !== 200 || response.redirected || mime !== 'application/json' || !cache.includes('no-store') || !response.body) {
      throw new Error('Invalid shell version response')
    }
    const reader = response.body.getReader(), decoder = new TextDecoder('utf-8', { fatal: true })
    let size = 0, text = ''
    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        size += value.byteLength
        if (size > 256) throw new Error('Oversized shell version response')
        text += decoder.decode(value, { stream: true })
      }
      text += decoder.decode()
    } catch (error) {
      void reader.cancel().catch(() => {})
      throw error
    } finally {
      reader.releaseLock()
    }
    const match = descriptor.exec(text)
    if (!match?.[1]) throw new Error('Invalid shell version descriptor')
    return match[1]
  })
}
