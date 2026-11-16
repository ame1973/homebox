const sessionClearedEvent = 'homebox:session-cleared'
const sessionClearedMessage = 'cleared'

// The signal contains no credentials or private state. Storage is only a
// transient fallback for browsers where BroadcastChannel is unavailable.
export function notifySessionCleared() {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new Event(sessionClearedEvent))
  try {
    const channel = new BroadcastChannel(sessionClearedEvent)
    try { channel.postMessage(sessionClearedMessage) } finally { channel.close() }
  } catch { /* Other tabs can still receive the storage signal. */ }
  try {
    window.localStorage.removeItem(sessionClearedEvent)
    window.localStorage.setItem(sessionClearedEvent, sessionClearedMessage)
    window.localStorage.removeItem(sessionClearedEvent)
  } catch { /* Same-page and BroadcastChannel delivery remain available. */ }
}

export function subscribeSessionCleared(listener: (source: 'local' | 'remote') => void) {
  const local = () => listener('local')
  window.addEventListener(sessionClearedEvent, local)
  let channel: BroadcastChannel | undefined
  try {
    channel = new BroadcastChannel(sessionClearedEvent)
    channel.onmessage = (event) => { if (event.data === sessionClearedMessage) listener('remote') }
  } catch { channel?.close(); channel = undefined }
  const storage = (event: StorageEvent) => {
    if (event.key === sessionClearedEvent && event.newValue === sessionClearedMessage) listener('remote')
  }
  window.addEventListener('storage', storage)
  return () => {
    window.removeEventListener(sessionClearedEvent, local)
    window.removeEventListener('storage', storage)
    channel?.close()
  }
}
