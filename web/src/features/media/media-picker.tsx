import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { ApiError } from '@/lib/api/client'
import { fetchMediaPage } from '@/lib/api/media'
import { messages } from '@/lib/messages'
import { MediaError } from './media-error'

const text = messages.media, catalog = messages.catalog
const pageSize = 20

type VisitedPage = Readonly<{ cursor: string | undefined; ids: readonly string[] }>

export function MediaPicker({ enabled, locked, selectedID, loading, onSelect, onSessionLost }: {
  enabled: boolean; locked: boolean; selectedID: string | undefined; loading: boolean
  onSelect: (id: string) => void; onSessionLost: (mandatory: boolean) => void
}) {
  const [open, setOpen] = useState(false), [cursor, setCursor] = useState<string>(), [trail, setTrail] = useState<VisitedPage[]>([])
  const summary = useRef<HTMLElement>(null), active = enabled && open && !locked
  const page = useQuery({ queryKey: ['media', 'discovery', cursor ?? null], enabled: active, staleTime: 0, refetchOnMount: 'always',
    queryFn: ({ signal }) => fetchMediaPage({ limit: pageSize, ...(cursor ? { cursor } : {}) }, signal) })
  const credentialFailure = page.error instanceof ApiError && (page.error.status === 401 || page.error.code === 'PASSWORD_CHANGE_REQUIRED') ? page.error : undefined
  useEffect(() => { if (credentialFailure) onSessionLost(credentialFailure.code === 'PASSWORD_CHANGE_REQUIRED') }, [credentialFailure, onSessionLost])
  const current = page.isFetchedAfterMount && !page.isError ? page.data : undefined
  const previousIDs = new Set(trail.flatMap((visited) => visited.ids))
  const invalid = !!current && (current.data.some((entry) => previousIDs.has(entry.id)) ||
    current.next_cursor !== null && (current.next_cursor === cursor || trail.some((visited) => visited.cursor === current.next_cursor)))
  const pending = active && (page.isPending || page.isFetching || !page.isFetchedAfterMount)
  const canChoose = active && !page.isFetching && !page.isError && !!current && !invalid
  const canPage = active && !page.isFetching

  function first() { if (canPage) { setTrail([]); setCursor(undefined) } }
  function previous() {
    if (!canPage || !trail.length) return
    setCursor(trail.at(-1)?.cursor); setTrail(trail.slice(0, -1))
  }
  function next() {
    if (!canChoose || !current?.next_cursor) return
    setTrail([...trail, { cursor, ids: current.data.map((entry) => entry.id) }]); setCursor(current.next_cursor)
  }
  function select(id: string) {
    if (!canChoose || loading && id === selectedID) return
    onSelect(id); setOpen(false); summary.current?.focus()
  }

  return <details open={open} onToggle={(event) => setOpen(event.currentTarget.open)} className="min-w-0 rounded-lg border p-4">
    <summary ref={summary} className="min-h-12 cursor-pointer rounded-sm py-3 text-base font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.picker}</summary>
    <div className="min-w-0 space-y-4" aria-busy={active && page.isFetching}>
      <p className="max-w-prose text-base leading-relaxed">{text.pickerHelp}</p>
      {!enabled || locked ? <p role="status">{text.pickerUnavailable}</p> : pending ? <p role="status" className="flex items-center gap-3"><LoaderCircle aria-hidden="true" className="size-5 animate-spin motion-reduce:animate-none" />{text.pickerLoading}</p> : <>
        {page.error && <MediaError error={page.error} />}{invalid && <MediaError error={new ApiError('INVALID_RESPONSE')} />}
        {!invalid && current && <><p className="text-sm text-muted-foreground">{catalog.pageCount}{current.data.length}</p>
          {current.data.length === 0 ? <p role="status">{text.pickerEmpty}</p> : <ul aria-label={text.pickerList} className="divide-y border-y">{current.data.map((entry) => <li key={entry.id} className="min-w-0 space-y-2 py-4">
            <p className="whitespace-pre-wrap font-semibold [overflow-wrap:anywhere]">{JSON.stringify(entry.id)}</p>
            <p className="text-sm">{entry.verification === 'verified' ? text.verified : text.unverified}</p>
            <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.mediaClaims}：{JSON.stringify(entry.mime_type)}；{entry.width} × {entry.height}；{entry.bytes} 位元組</p>
            <Button type="button" variant="outline" disabled={!canChoose || loading && entry.id === selectedID} className={`${control} w-full sm:w-auto`} aria-label={`${text.pickerChoose}：${JSON.stringify(entry.id)}`} onClick={() => select(entry.id)}>{text.pickerChoose}</Button>
          </li>)}</ul>}
        </>}
        <nav aria-label={text.pickerPagination} className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" disabled={!canPage || !trail.length} className={control} onClick={previous}>{catalog.previousPage}</Button>
          <Button type="button" variant="outline" disabled={!canPage || !cursor} className={control} onClick={first}>{catalog.firstPage}</Button>
          <Button type="button" variant="outline" disabled={!canChoose || !current?.next_cursor} className={control} onClick={next}>{catalog.nextPage}</Button>
        </nav>
        <Button type="button" variant="outline" disabled={!canPage} className={control} onClick={() => { void page.refetch() }}>{text.pickerRefresh}</Button>
      </>}
    </div>
  </details>
}
