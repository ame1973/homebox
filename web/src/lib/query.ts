import { QueryClient } from '@tanstack/react-query'

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false, staleTime: 30_000, networkMode: 'always',
        refetchOnReconnect: true,
      },
      // Offline writes must fail, not pause and silently resume on reconnect.
      mutations: { retry: false, networkMode: 'always' },
    },
  })
}
