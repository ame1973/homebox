import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/client'

type CandidatePage<T> = Readonly<{ data: readonly T[]; next_cursor: string | null }>
type VisitedPage = Readonly<{ cursor?: string; ids: readonly string[] }>
type CandidateScope = Readonly<{ cursor?: string; sequence: number; trail: readonly VisitedPage[] }>

type CandidateOptions<T> = {
  root: 'barcode-review' | 'item-name-review'; commandKey: string; searchable?: boolean; available: boolean
  read: (cursor: string | undefined, signal: AbortSignal) => Promise<CandidatePage<T>>
  identity: (record: T) => string; onSessionLost: (mandatory: boolean) => void
}

// These are advisory live pages, never mutation proofs. Only an explicit retry
// changes a failed/offline scope; ordinary catalog invalidation cannot refetch it.
export function useReviewCandidates<T>({ root, commandKey, searchable = true, available, read, identity, onSessionLost }: CandidateOptions<T>) {
  const [scope, setScope] = useState<CandidateScope>({ sequence: 0, trail: [] })
  const [skipped, setSkipped] = useState(false), [offlineStale, setStale] = useState(() => !navigator.onLine)
  const stale = searchable && offlineStale
  const client = useQueryClient()
  useEffect(() => {
    const offline = () => {
      setStale(true); setSkipped(false)
      void client.cancelQueries({ queryKey: [root, commandKey] })
    }
    window.addEventListener('offline', offline)
    return () => window.removeEventListener('offline', offline)
  }, [client, commandKey, root])
  const page = useQuery({ queryKey: [root, commandKey, scope.cursor ?? null, scope.sequence], enabled: searchable && !stale,
    gcTime: 0, staleTime: Infinity, refetchOnMount: false, refetchOnWindowFocus: false, refetchOnReconnect: false,
    queryFn: async ({ signal }) => {
      try { return await read(scope.cursor, signal) }
      catch (error) {
        if (!signal.aborted && error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) onSessionLost(error.code === 'PASSWORD_CHANGE_REQUIRED')
        throw error
      }
    } })
  const current = available && !stale && !page.isFetching && !page.isError ? page.data : undefined
  const ids = current?.data.map(identity) ?? []
  const seen = new Set(scope.trail.flatMap((entry) => entry.ids))
  const invalid = !!current && (new Set(ids).size !== ids.length || ids.some((id) => seen.has(id)) ||
    current.next_cursor !== null && (current.next_cursor === scope.cursor || scope.trail.some((entry) => entry.cursor === current.next_cursor)))
  const credentialFailure = page.error instanceof ApiError && (page.error.status === 401 || page.error.code === 'PASSWORD_CHANGE_REQUIRED')
  const failed = available && !stale && !page.isFetching && !credentialFailure && (!searchable || !!page.error || invalid)
  const canPage = available && !stale && !page.isFetching
  const canChoose = canPage && !!current && !invalid && !page.isError
  function restart() { if (searchable) { setStale(false); setSkipped(false); setScope((value) => ({ sequence: value.sequence + 1, trail: [] })) } }
  function previous() {
    if (!canPage || !scope.trail.length) return
    setSkipped(false)
    setScope({ cursor: scope.trail.at(-1)?.cursor, sequence: scope.sequence + 1, trail: scope.trail.slice(0, -1) })
  }
  function next() {
    if (!canChoose || !current?.next_cursor) return
    setSkipped(false)
    setScope({ cursor: current.next_cursor, sequence: scope.sequence + 1, trail: [...scope.trail, { cursor: scope.cursor, ids }] })
  }
  return { scope, page, current, invalid, failed, canPage, canChoose, skipped, stale, searchable,
    proceed: canChoose || failed && skipped, skip: () => setSkipped(true), restart, previous, next }
}
