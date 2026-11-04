import { onlineManager } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { createQueryClient } from './query'

it('fails an offline mutation immediately and never replays it on reconnect', async () => {
  const client = createQueryClient()
  const mutationFn = vi.fn().mockRejectedValue(new Error('Offline'))
  client.mount()
  onlineManager.setOnline(false)
  try {
    const mutation = client.getMutationCache().build(client, { mutationFn })
    await expect(mutation.execute(undefined)).rejects.toThrow('Offline')
    expect(mutation.state.isPaused).toBe(false)
    onlineManager.setOnline(true)
    await client.resumePausedMutations()
    expect(mutationFn).toHaveBeenCalledTimes(1)
    expect(client.getDefaultOptions().queries?.refetchOnReconnect).toBe(true)
  } finally {
    onlineManager.setOnline(true)
    client.unmount()
    client.clear()
  }
})
