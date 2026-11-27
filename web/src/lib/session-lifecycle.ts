const sessionClearedEvent = 'homebox:session-cleared'
const sessionClearedMessage = 'cleared'
let sharedChannel: BroadcastChannel | undefined
let sharedSubscribers = 0

// Page suspension uses the same local cleanup without telling other tabs that
// their session changed. It contains no credentials or private state.
export function notifyPageSuspended() {
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(sessionClearedEvent))
}

// The signal contains no credentials or private state. Storage is only a
// transient fallback for browsers where BroadcastChannel is unavailable.
export function notifySessionCleared() {
  if (typeof window === 'undefined') return
  notifyPageSuspended()
  try {
    // A channel does not receive its own messages. Sharing the receiver with
    // the sender avoids a delayed self-notification after a fresh login.
    if (sharedChannel) sharedChannel.postMessage(sessionClearedMessage)
    else {
      const channel = new BroadcastChannel(sessionClearedEvent)
      try { channel.postMessage(sessionClearedMessage) } finally { channel.close() }
    }
  } catch { /* Other tabs can still receive the storage signal. */ }
  try {
    window.localStorage.removeItem(sessionClearedEvent)
    window.localStorage.setItem(sessionClearedEvent, sessionClearedMessage)
    window.localStorage.removeItem(sessionClearedEvent)
  } catch { /* Same-page and BroadcastChannel delivery remain available. */ }
}

export function subscribeSessionCleared(listener: (source: 'local' | 'remote') => void) {
  const local = () => listener('local')
  const message = (event: MessageEvent) => { if (event.data === sessionClearedMessage) listener('remote') }
  window.addEventListener(sessionClearedEvent, local)
  let channel: BroadcastChannel | undefined
  try {
    channel = sharedChannel ?? new BroadcastChannel(sessionClearedEvent)
    sharedChannel = channel
    channel.addEventListener('message', message)
    sharedSubscribers++
  } catch {
    if (channel && sharedChannel === channel && sharedSubscribers === 0) {
      sharedChannel = undefined
      try { channel.close() } catch { /* Storage delivery remains available. */ }
    }
    channel = undefined
  }
  const storage = (event: StorageEvent) => {
    if (event.key === sessionClearedEvent && event.newValue === sessionClearedMessage) listener('remote')
  }
  window.addEventListener('storage', storage)
  let active = true
  return () => {
    if (!active) return
    active = false
    window.removeEventListener(sessionClearedEvent, local)
    window.removeEventListener('storage', storage)
    if (channel) {
      channel.removeEventListener('message', message)
      sharedSubscribers--
      if (sharedSubscribers === 0 && sharedChannel === channel) {
        sharedChannel = undefined
        channel.close()
      }
    }
  }
}
