import { afterEach, expect, it, vi } from 'vitest'
import { notifyPageSuspended, notifySessionCleared, subscribeSessionCleared } from './session-lifecycle'

class FakeChannel {
  static instances: FakeChannel[] = []
  onmessage: ((event: { data: unknown }) => void) | null = null
  postMessage = vi.fn()
  close = vi.fn()
  readonly name: string
  constructor(name: string) { this.name = name; FakeChannel.instances.push(this) }
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
  channel.onmessage?.({ data: 'unrelated' })
  window.dispatchEvent(new StorageEvent('storage', { key: 'other', newValue: 'cleared' }))
  expect(listener).not.toHaveBeenCalled()
  channel.onmessage?.({ data: 'cleared' })
  window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
  expect(listener).toHaveBeenCalledTimes(2)
  expect(listener).toHaveBeenNthCalledWith(1, 'remote')
  expect(listener).toHaveBeenNthCalledWith(2, 'remote')
  unsubscribe()
  window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
  expect(listener).toHaveBeenCalledTimes(2)
})

it('broadcasts only the static cleared marker on a real session-clear notification', () => {
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const set = vi.spyOn(Storage.prototype, 'setItem'), listener = vi.fn()
  const unsubscribe = subscribeSessionCleared(listener)
  notifySessionCleared()
  expect(listener).toHaveBeenCalledExactlyOnceWith('local')
  expect(FakeChannel.instances[1]?.postMessage).toHaveBeenCalledExactlyOnceWith('cleared')
  expect(FakeChannel.instances[1]?.close).toHaveBeenCalledOnce()
  expect(set).toHaveBeenCalledExactlyOnceWith('homebox:session-cleared', 'cleared')
  expect(localStorage.getItem('homebox:session-cleared')).toBeNull()
  unsubscribe()
})
