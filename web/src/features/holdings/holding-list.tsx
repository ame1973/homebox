import { useId, useState } from 'react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { mutationControl as control } from '@/components/mutation-review'
import { pathIDSchema } from '@/lib/api/domain'
import { stockActions, stockRoute, unitCreateRoute, unitRoute } from '@/lib/holding-navigation'
import { messages } from '@/lib/messages'
import type { LocationTree } from '@/features/locations/tree'
import { baseQuantity, holdingValue } from './display'
import type { ItemHoldings } from './model'

const text = messages.holdings
export function HoldingList({ data, locations, navigationState, canWrite }: { data: ItemHoldings; locations: LocationTree; navigationState: object; canWrite: boolean }) {
  const searchID = useId(), [search, setSearch] = useState('')
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  const rows = data.kind === 'units' ? data.units.filter((unit) => !term || `${unit.id}\n${unit.asset_code}\n${unit.serial_number ?? ''}\n${unit.notes}\n${unit.location_id ?? ''}\n${locations.byID.get(unit.location_id ?? '')?.name ?? ''}`.toLocaleLowerCase('zh-Hant').includes(term))
    : data.stock.filter((stock) => !term || `${stock.location_id}\n${locations.byID.get(stock.location_id)?.name ?? ''}\n${locations.byID.get(stock.location_id)?.code ?? ''}`.toLocaleLowerCase('zh-Hant').includes(term))
  const count = data.kind === 'units' ? data.units.length : data.stock.length
  const label = data.kind === 'units' ? text.createUnit : text.actions.receive
  const route = data.kind === 'units' ? unitCreateRoute(data.item.id) : stockRoute(data.item.id, 'receive')
  return <section className="min-w-0 space-y-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-xl font-semibold">{data.kind === 'units' ? text.units : text.stock}</h2>{canWrite ? <Button asChild className={control}><Link to={route} state={navigationState}>{label}</Link></Button> : <Button disabled className={control}>{label}</Button>}{data.kind === 'stock' && (canWrite ? <Button asChild variant="outline" className={control}><Link to={stockRoute(data.item.id, 'adjust')} state={navigationState}>{text.actions.adjust}</Link></Button> : <Button disabled variant="outline" className={control}>{text.actions.adjust}</Button>)}</div>
    <div className="min-w-0 space-y-2"><label htmlFor={searchID} className="block text-base font-medium">{text.search}</label><Input id={searchID} value={search} onChange={(event) => setSearch(event.target.value)} autoComplete="off" aria-describedby={`${searchID}-help`} className="min-h-12 text-base md:text-base" /><p id={`${searchID}-help`} className="text-sm text-muted-foreground">{text.searchHelp}</p></div>
    <p className="text-sm text-muted-foreground">{data.kind === 'units' ? text.unitCount : text.stockCount}{count}</p>
    {!count ? <section className="space-y-3 rounded-lg border p-6"><h3 className="text-lg font-semibold">{data.kind === 'units' ? text.emptyUnits : text.emptyStock}</h3><p className="text-base leading-relaxed text-muted-foreground">{data.kind === 'units' ? text.emptyUnitsHelp : text.emptyStockHelp}</p></section>
      : !rows.length ? <p role="status">{text.noMatches}</p> : <ul className="divide-y rounded-lg border bg-card">{rows.map((row) => <li key={'id' in row ? row.id : JSON.stringify([row.item_id, row.location_id])} className="min-w-0 space-y-3 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_14rem]">
        {'id' in row ? <>
          {pathIDSchema.safeParse(row.id).success ? <Link to={unitRoute(row.id)} state={navigationState} className="block min-h-12 rounded-sm py-2 text-lg font-semibold underline-offset-4 hover:underline [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{JSON.stringify(row.asset_code)}</Link> : <><p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{JSON.stringify(row.asset_code)}</p><p>{text.unaddressable}</p></>}
          <p className="text-base">{holdingValue('status', row.status)}</p><p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.fields.serial_number}：{holdingValue('serial_number', row.serial_number)}</p>
          <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.fields.location_id}：{row.location_id === null ? text.unknownLocation : locations.byID.has(row.location_id) ? JSON.stringify(locations.byID.get(row.location_id)!.name) : text.unavailable}</p>
          <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{row.id}；{text.unitProof}{row.revision}</p>
        </> : <>
          <h3 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{locations.byID.has(row.location_id) ? JSON.stringify(locations.byID.get(row.location_id)!.name) : text.unavailable}</h3><p className="text-lg font-semibold [overflow-wrap:anywhere]">{baseQuantity(row.quantity_base, row.base_unit)}</p>
          {row.quantity_base === 0 && <p className="text-sm text-muted-foreground">{text.retainedZero}</p>}<p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{row.location_id}<br />{text.stockProof}{row.revision}</p>
          <div className="flex flex-wrap gap-2">{stockActions.map((action) => {
            const allowed = canWrite && locations.byID.has(row.location_id) && (action === 'consume' || action === 'transfer' ? row.quantity_base > 0 : action === 'receive' ? locations.canReceive.get(row.location_id) === true : true)
            return allowed ? <Button key={action} asChild variant="outline" className={control}><Link to={stockRoute(data.item.id, action, row.location_id)} state={navigationState}>{text.actions[action]}</Link></Button> : <Button key={action} disabled variant="outline" className={control}>{text.actions[action]}</Button>
          })}</div>
        </>}
      </li>)}</ul>}
  </section>
}
