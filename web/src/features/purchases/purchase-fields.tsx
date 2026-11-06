import { Link } from 'react-router'
import type { Item } from '@/lib/api/catalog'
import { pathIDSchema } from '@/lib/api/domain'
import type { Purchase, PurchaseLine } from '@/lib/api/purchases'
import { catalogRoute } from '@/lib/catalog-navigation'
import { unitRoute } from '@/lib/holding-navigation'
import { messages } from '@/lib/messages'
import { Measurement } from '@/features/holdings/holding-fields'

const text = messages.purchases
export function PurchaseFields({ purchase }: { purchase: Purchase }) {
  return <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['merchant_name', 'purchased_at', 'currency', 'receipt_note'] as const).map((key) => <div key={key} className={`min-w-0 space-y-1 ${key === 'receipt_note' ? 'sm:col-span-2' : ''}`}><dt className="text-sm font-medium text-muted-foreground">{text.fields[key]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(purchase[key])}</dd></div>)}</dl>
}
export function PurchaseItem({ item }: { item: Item }) {
  return <section className="min-w-0 space-y-3"><h2 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{text.item}：{JSON.stringify(item.name)}</h2><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{item.id}；{text.itemProof}{item.revision}</p><p>{messages.catalog[item.tracking_mode]}</p><Measurement item={item} /></section>
}
export function PurchaseLines({ lines, currency, navigationState }: { lines: readonly PurchaseLine[]; currency: string; navigationState: object }) {
  return <section className="min-w-0 space-y-5"><h2 className="text-xl font-semibold">{text.lines}</h2><p className="text-sm text-muted-foreground">{text.lineCount}{lines.length}</p><p className="max-w-prose text-base leading-relaxed">{text.immutable}</p>
    {!lines.length ? <section className="space-y-3 rounded-lg border p-5"><h3 className="text-lg font-semibold">{text.emptyLines}</h3><p>{text.emptyLinesHelp}</p></section> : <ul className="divide-y rounded-lg border bg-card">{lines.map((line) => <li key={line.id} className="min-w-0 space-y-4 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_24rem]">
      <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.lineID}：{line.id}；{text.lineVersion}：{line.revision}</p>
      <dl className="grid min-w-0 gap-4 sm:grid-cols-2"><div className="min-w-0"><dt className="text-sm text-muted-foreground">{text.item}</dt><dd className="[overflow-wrap:anywhere]">{pathIDSchema.safeParse(line.item_id).success ? <Link to={catalogRoute(line.item_id)} state={navigationState} className="inline-block min-h-12 rounded-sm py-3 underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-ring">{line.item_id}</Link> : line.item_id}</dd></div>
        <div className="min-w-0"><dt className="text-sm text-muted-foreground">{text.unit}</dt><dd className="[overflow-wrap:anywhere]">{line.item_unit_id === null ? text.noUnit : pathIDSchema.safeParse(line.item_unit_id).success ? <Link to={unitRoute(line.item_unit_id)} state={navigationState} className="inline-block min-h-12 rounded-sm py-3 underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-ring">{line.item_unit_id}</Link> : line.item_unit_id}</dd></div>
        <div className="min-w-0"><dt className="text-sm text-muted-foreground">{text.fields.quantity_base}</dt><dd className="tabular-nums [overflow-wrap:anywhere]">{line.quantity_base}</dd></div><div className="min-w-0"><dt className="text-sm text-muted-foreground">{text.fields.paid_minor}</dt><dd className="tabular-nums [overflow-wrap:anywhere]">{line.paid_minor} {currency}</dd></div><div className="min-w-0 sm:col-span-2"><dt className="text-sm text-muted-foreground">{text.fields.note}</dt><dd className="whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(line.note)}</dd></div></dl>
    </li>)}</ul>}
  </section>
}
