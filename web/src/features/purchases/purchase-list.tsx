import { Link } from 'react-router'
import { pathIDSchema } from '@/lib/api/domain'
import type { Purchase } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { purchaseRoute } from '@/lib/purchase-navigation'

const text = messages.purchases
export function PurchaseList({ purchases, navigationState, associated = false }: { purchases: readonly Purchase[]; navigationState: object; associated?: boolean }) {
  return <section className="min-w-0 space-y-5"><p className="text-sm text-muted-foreground">{associated ? text.associatedCount : messages.catalog.pageCount}{purchases.length}</p>
    {!purchases.length ? <section className="space-y-3 rounded-lg border p-5"><h2 className="text-xl font-semibold">{associated ? text.emptyAssociated : text.empty}</h2><p className="text-base leading-relaxed text-muted-foreground">{associated ? text.emptyAssociatedHelp : text.emptyHelp}</p></section> : <ul className="divide-y rounded-lg border bg-card">{purchases.map((purchase) => <li key={purchase.id} className="min-w-0 space-y-2 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_12rem]">
      {pathIDSchema.safeParse(purchase.id).success ? <Link to={purchaseRoute(purchase.id)} state={navigationState} className="block min-h-12 rounded-sm py-2 text-lg font-semibold underline-offset-4 hover:underline [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{JSON.stringify(purchase.merchant_name)}</Link> : <><h2 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{JSON.stringify(purchase.merchant_name)}</h2><p>{messages.holdings.unaddressable}</p></>}
      <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.fields.purchased_at}：{purchase.purchased_at}<br />{text.fields.currency}：{purchase.currency}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.purchaseID}：{purchase.id}；{text.version}：{purchase.revision}</p>
    </li>)}</ul>}
  </section>
}
