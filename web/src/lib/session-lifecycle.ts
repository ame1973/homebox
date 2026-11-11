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

export function subscribeSessionCleared(listener: () => void) {
  window.addEventListener(sessionClearedEvent, listener)
  let channel: BroadcastChannel | undefined
  try {
    channel = new BroadcastChannel(sessionClearedEvent)
    channel.onmessage = (event) => { if (event.data === sessionClearedMessage) listener() }
  } catch { channel?.close(); channel = undefined }
  const storage = (event: StorageEvent) => {
    if (event.key === sessionClearedEvent && event.newValue === sessionClearedMessage) listener()
  }
  window.addEventListener('storage', storage)
  return () => {
    window.removeEventListener(sessionClearedEvent, listener)
    window.removeEventListener('storage', storage)
    channel?.close()
  }
}
