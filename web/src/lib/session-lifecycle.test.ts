import { afterEach, expect, it, vi } from 'vitest'
import { notifyPageSuspended, notifySessionCleared, subscribeSessionCleared } from './session-lifecycle'

class FakeChannel extends EventTarget {
  static instances: FakeChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  readonly name: string
  constructor(name: string) { super(); this.name = name; FakeChannel.instances.push(this) }
  emit(data: unknown) { this.dispatchEvent(new MessageEvent('message', { data })) }
}

afterEach(() => { FakeChannel.instances = []; vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('cleans only this page on suspension without notifying other tabs', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const set = vi.spyOn(Storage.prototype, 'setItem'), remove = vi.spyOn(Storage.prototype, 'removeItem')
  const listener = vi.fn(), unsubscribe = subscribeSessionCleared(listener)
  notifyPageSuspended()
  expect(listener).toHaveBeenCalledExactlyOnceWith('local')
  expect(FakeChannel.instances).toHaveLength(1)
  expect(FakeChannel.instances[0]?.postMessage).not.toHaveBeenCalled()
  expect(set).not.toHaveBeenCalled()
  expect(remove).not.toHaveBeenCalled()
  unsubscribe()
  notifyPageSuspended()
  expect(listener).toHaveBeenCalledOnce()
  expect(FakeChannel.instances[0]?.close).toHaveBeenCalledOnce()
})

it('identifies valid remote transport signals and removes both listeners on unsubscribe', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const listener = vi.fn(), unsubscribe = subscribeSessionCleared(listener), channel = FakeChannel.instances[0]!
  channel.emit('unrelated')
  window.dispatchEvent(new StorageEvent('storage', { key: 'other', newValue: 'cleared' }))
  expect(listener).not.toHaveBeenCalled()
  channel.emit('cleared')
  window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
  expect(listener).toHaveBeenCalledTimes(2)
  expect(listener).toHaveBeenNthCalledWith(1, 'remote')
  expect(listener).toHaveBeenNthCalledWith(2, 'remote')
  unsubscribe()
  window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
  expect(listener).toHaveBeenCalledTimes(2)
})

it('does not redeliver its own asynchronous broadcast as a remote session clear', async () => {
  class LoopbackChannel extends EventTarget {
    static instances: LoopbackChannel[] = []
    closed = false
    readonly name: string
    constructor(name: string) { super(); this.name = name; LoopbackChannel.instances.push(this) }
    postMessage(data: unknown) {
      for (const target of LoopbackChannel.instances) {
        if (target !== this && target.name === this.name && !target.closed) queueMicrotask(() => { if (!target.closed) target.dispatchEvent(new MessageEvent('message', { data })) })
      }
    }
    close() { this.closed = true }
  }
  vi.stubGlobal('BroadcastChannel', LoopbackChannel)
  const first = vi.fn(), second = vi.fn()
  const unsubscribeFirst = subscribeSessionCleared(first), unsubscribeSecond = subscribeSessionCleared(second)
  expect(LoopbackChannel.instances).toHaveLength(1)
  const shared = LoopbackChannel.instances[0]!
  notifySessionCleared()
  expect(first).toHaveBeenCalledExactlyOnceWith('local')
  expect(second).toHaveBeenCalledExactlyOnceWith('local')
  await Promise.resolve()
  expect(first).toHaveBeenCalledExactlyOnceWith('local')
  expect(second).toHaveBeenCalledExactlyOnceWith('local')

  const foreign = new LoopbackChannel('homebox:session-cleared')
  foreign.postMessage('cleared')
  await Promise.resolve()
  expect(first).toHaveBeenNthCalledWith(2, 'remote')
  expect(second).toHaveBeenNthCalledWith(2, 'remote')
  foreign.postMessage('cleared')
  unsubscribeFirst()
  expect(shared.closed).toBe(false)
  await Promise.resolve()
  expect(first).toHaveBeenCalledTimes(2)
  expect(second).toHaveBeenCalledTimes(3)
  unsubscribeSecond()
  expect(shared.closed).toBe(true)
  unsubscribeSecond()
  foreign.close()
})

it('retains local and storage signals when a shared channel cannot subscribe', () => {
  const created: RefusingChannel[] = []
  class RefusingChannel {
    close = vi.fn()
    postMessage = vi.fn()
    constructor() { created.push(this) }
    addEventListener() { throw new Error('listener unavailable') }
  }
  vi.stubGlobal('BroadcastChannel', RefusingChannel)
  const listener = vi.fn(), unsubscribe = subscribeSessionCleared(listener)
  expect(created[0]?.close).toHaveBeenCalledOnce()
  window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
  notifySessionCleared()
  expect(listener).toHaveBeenNthCalledWith(1, 'remote')
  expect(listener).toHaveBeenNthCalledWith(2, 'local')
  expect(created[1]?.postMessage).toHaveBeenCalledExactlyOnceWith('cleared')
  expect(created[1]?.close).toHaveBeenCalledOnce()
  unsubscribe()
})

it('preserves the storage signal if posting through the shared channel fails', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const listener = vi.fn(), set = vi.spyOn(Storage.prototype, 'setItem')
  const unsubscribe = subscribeSessionCleared(listener), channel = FakeChannel.instances[0]!
  channel.postMessage.mockImplementation(() => { throw new Error('transport unavailable') })
  notifySessionCleared()
  expect(listener).toHaveBeenCalledExactlyOnceWith('local')
  expect(set).toHaveBeenCalledExactlyOnceWith('homebox:session-cleared', 'cleared')
  expect(channel.close).not.toHaveBeenCalled()
  channel.emit('cleared')
  expect(listener).toHaveBeenNthCalledWith(2, 'remote')
  unsubscribe()
  expect(channel.close).toHaveBeenCalledOnce()
})

it('uses and closes a temporary sender when this document has no subscribers', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  notifySessionCleared()
  expect(FakeChannel.instances).toHaveLength(1)
  expect(FakeChannel.instances[0]?.postMessage).toHaveBeenCalledExactlyOnceWith('cleared')
  expect(FakeChannel.instances[0]?.close).toHaveBeenCalledOnce()
  expect(localStorage.getItem('homebox:session-cleared')).toBeNull()
})

it('broadcasts only the static cleared marker on a real session-clear notification', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const set = vi.spyOn(Storage.prototype, 'setItem'), listener = vi.fn()
  const unsubscribe = subscribeSessionCleared(listener)
  notifySessionCleared()
  expect(listener).toHaveBeenCalledExactlyOnceWith('local')
  expect(FakeChannel.instances).toHaveLength(1)
  expect(FakeChannel.instances[0]?.postMessage).toHaveBeenCalledExactlyOnceWith('cleared')
  expect(FakeChannel.instances[0]?.close).not.toHaveBeenCalled()
  expect(set).toHaveBeenCalledExactlyOnceWith('homebox:session-cleared', 'cleared')
  expect(localStorage.getItem('homebox:session-cleared')).toBeNull()
  unsubscribe()
  expect(FakeChannel.instances[0]?.close).toHaveBeenCalledOnce()
})
