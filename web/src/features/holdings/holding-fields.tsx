import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import type { Location } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import type { LocationTree } from '@/features/locations/tree'
import { holdingPath, holdingValue } from './display'

const text = messages.holdings
export function HoldingPath({ path, label }: { path: readonly Location[] | undefined; label: string }) {
  return <details className="min-w-0 rounded-md border px-3"><summary className="min-h-12 cursor-pointer py-3 text-base font-medium [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{label}：{path === undefined ? text.unavailable : !path.length ? text.unknownLocation : `${JSON.stringify(path.at(-1)!.name)} (${JSON.stringify(path.at(-1)!.code)})`}</summary>
    {path === undefined ? <p className="py-3 text-base">{text.unavailable}</p> : !path.length ? <p className="py-3 text-base">{text.unknownLocation}</p>
      : <ol className="min-w-0 space-y-3 pb-4">{path.map((location) => <li key={location.id} className="min-w-0 border-l-2 pl-3"><p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(location.name)} ({JSON.stringify(location.code)})</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{location.id}；{text.version}：{location.revision}；{messages.locations[location.status]}</p></li>)}</ol>}
  </details>
}
export function Measurement({ item }: { item: Item }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-base font-semibold">{text.measurement}</h3><dl className="grid min-w-0 gap-3 sm:grid-cols-3">
    {(['unit_code', 'base_unit', 'scale_factor'] as const).map((key) => <div key={key} className="min-w-0"><dt className="text-sm text-muted-foreground">{key === 'unit_code' ? text.displayUnit : key === 'base_unit' ? text.baseUnit : text.scale}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{holdingValue(key, item[key])}</dd></div>)}
  </dl></section>
}
export function UnitFields({ unit, locations }: { unit: Unit; locations?: LocationTree }) {
  return <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['asset_code', 'serial_number', 'notes', 'status', 'location_id'] as const).map((key) => <div key={key} className={`min-w-0 space-y-1 ${key === 'notes' || key === 'location_id' ? 'sm:col-span-2' : ''}`}><dt className="text-sm font-medium text-muted-foreground">{text.fields[key]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{holdingValue(key, unit[key])}
    {key === 'location_id' && locations && unit.location_id !== null && <p>{locations.byID.has(unit.location_id) ? JSON.stringify(locations.byID.get(unit.location_id)!.name) : text.unavailable}</p>}
    {key === 'location_id' && locations && <HoldingPath path={holdingPath(locations, unit.location_id)} label={text.selectedPath} />}
  </dd></div>)}</dl>
}
