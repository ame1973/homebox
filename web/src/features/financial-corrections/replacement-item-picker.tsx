import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchItemsPage, type ItemPageQuery } from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { messages } from '@/lib/messages'
import { catalogDisplay } from '@/features/catalog/display'
import { CatalogFilters } from '@/features/catalog/filters'
import { readLocationTree } from '@/features/locations/tree'
import { PurchaseError } from '@/features/purchases/purchase-error'
import { readCategoryTree } from '@/features/taxonomy/model'

const text = messages.purchaseCorrections, catalog = messages.catalog

export function ReplacementItemPicker({ enabled, locked, selected, onSelect, onSessionLost }: {
  enabled: boolean; locked: boolean; selected: string | null; onSelect: (id: string | null) => void; onSessionLost: (mandatory: boolean) => void
}) {
  const [open, setOpen] = useState(false), [query, setQuery] = useState<ItemPageQuery>({}), [trail, setTrail] = useState<(string | null)[]>([]), [filterVersion, setFilterVersion] = useState(0)
  const summary = useRef<HTMLElement>(null), active = enabled && open && !locked
  const categories = useQuery({ queryKey: ['taxonomy', 'categories'], enabled: active, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => ({ kind: 'categories' as const, value: await readCategoryTree(signal) }) })
  const locations = useQuery({ queryKey: ['locations', 'tree'], enabled: active, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const items = useQuery({ queryKey: ['catalog', 'items', query], enabled: active, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchItemsPage(query, signal) })
  const errors = [categories.error, locations.error, items.error], readError = errors.find((error) => error != null)
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  useEffect(() => { if (active && credentialFailure) onSessionLost(credentialFailure.code === 'PASSWORD_CHANGE_REQUIRED') }, [active, credentialFailure, onSessionLost])
  const reading = categories.isFetching || locations.isFetching || items.isFetching
  const pending = active && (categories.isPending || locations.isPending || items.isPending || !items.isFetchedAfterMount)
  const inconsistent = !!categories.data && items.data?.data.some((item) => item.category_id !== null && !categories.data.value.byID.has(item.category_id))
  const invalidContinuation = items.data?.next_cursor != null && trail.includes(items.data.next_cursor)
  const canChoose = active && !reading && !readError && !inconsistent && !invalidContinuation && items.isFetchedAfterMount
  function page(cursor: string | null, cursors: (string | null)[]) {
    const { cursor: _previous, ...scope } = query
    setQuery({ ...scope, ...(cursor ? { cursor } : {}) }); setTrail(cursors)
  }
  function select(id: string | null) { if (!active || id !== null && !canChoose) return; onSelect(id); setOpen(false); summary.current?.focus() }
  return <details open={open} onToggle={(event) => setOpen(event.currentTarget.open)} className="min-w-0 space-y-4 rounded-lg border p-4"><summary ref={summary} className="min-h-12 cursor-pointer rounded-sm py-3 text-base font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.picker}</summary>
    <p className="max-w-prose text-base">{text.pickerHelp}</p>{selected !== null && <><p className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.selectedItem}：{JSON.stringify(selected)}</p><Button type="button" variant="outline" disabled={!active} className={control} onClick={() => select(null)}>{text.clearItem}</Button></>}
    {!enabled ? <p role="status">{text.targetUnavailable}</p> : pending ? <p role="status" className="flex items-center gap-3"><LoaderCircle aria-hidden="true" className="size-5 animate-spin motion-reduce:animate-none" />{text.targetLoading}</p> : <>
      {readError && <PurchaseError error={readError} />}{(inconsistent || invalidContinuation) && <PurchaseError error={new ApiError('INVALID_RESPONSE')} />}
      {categories.data && locations.data && <CatalogFilters key={filterVersion} query={query} categories={categories.data.value} locations={locations.data} disabled={!active || reading} onApply={(values) => { setQuery(values); setTrail([]); setFilterVersion((value) => value + 1) }} />}
      {!invalidContinuation && items.data && <><p className="text-sm text-muted-foreground">{catalog.pageCount}{items.data.data.length}</p>{!items.data.data.length ? <p role="status">{catalog.empty}</p> : <ul className="divide-y rounded-lg border bg-card">{items.data.data.map((item) => <li key={item.id} className="min-w-0 space-y-3 p-4 [content-visibility:auto] [contain-intrinsic-size:auto_12rem]">
        <h3 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{JSON.stringify(item.name)}</h3><p>{catalogDisplay('tracking_mode', item.tracking_mode)}；{catalogDisplay('status', item.status)}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{JSON.stringify(item.id)}</p>
        <Button type="button" variant="outline" disabled={!canChoose || !pathIDSchema.safeParse(item.id).success} className={control} aria-label={`${text.chooseItem}：${JSON.stringify(item.id)}`} onClick={() => select(item.id)}>{text.chooseItem}</Button>
        {!pathIDSchema.safeParse(item.id).success && <p className="text-sm">{messages.holdings.unaddressable}</p>}
      </li>)}</ul>}
        <nav aria-label={text.selectionPagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canChoose || !trail.length} className={control} onClick={() => page(trail.at(-1) ?? null, trail.slice(0, -1))}>{catalog.previousPage}</Button><Button type="button" variant="outline" disabled={!canChoose || !query.cursor} className={control} onClick={() => page(null, [])}>{catalog.firstPage}</Button><Button type="button" variant="outline" disabled={!canChoose || !items.data.next_cursor} className={control} onClick={() => page(items.data!.next_cursor, [...trail, query.cursor ?? null])}>{catalog.nextPage}</Button></nav>
      </>}
    </>}
  </details>
}
