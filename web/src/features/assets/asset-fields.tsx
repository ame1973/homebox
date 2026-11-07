import { useId, useState } from 'react'
import { Link } from 'react-router'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { itemPurchasesRoute } from '@/lib/purchase-navigation'
import { messages } from '@/lib/messages'
import { assetRoute } from '@/lib/asset-navigation'
import { pathIDSchema } from '@/lib/api/domain'
import type { Asset, Disposal } from '@/lib/api/assets'
import { assetValue } from './display'
import type { AssetCostsSource, AssetProfileSource, AssetUsageSource } from './model'

const text = messages.assets
export function AssetEvidence({ itemID, navigationState }: { itemID: string; navigationState: object }) {
  return <section className="min-w-0 space-y-3"><p className="max-w-prose text-base text-muted-foreground">{text.evidenceHelp}</p><Button asChild variant="outline" className={control}><Link to={itemPurchasesRoute(itemID)} state={navigationState}>{text.evidence}</Link></Button></section>
}
export function AssetProofs({ source }: { source: AssetProfileSource }) {
  return <div className="min-w-0 space-y-2 text-sm text-muted-foreground [overflow-wrap:anywhere]"><p>{text.identifier}：{source.unit.id}<br />{text.unitProof}{source.unit.revision}<br />{text.itemProof}{source.item.revision}</p><p>{source.asset === null ? text.absentProof : `${text.assetProof}${source.asset.revision}`}</p><p>{text.independentProofs}</p></div>
}
export function AssetProfileFields({ asset }: { asset: Asset }) {
  return <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['cost_basis', 'currency', 'manual_acquisition_minor', 'usage_unit', 'in_service_at', 'retired_at', 'estimated_residual_minor'] as const).map((name) => <div key={name} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{text.fields[name]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{assetValue(name, asset[name])}{name.endsWith('_minor') && asset[name] !== null ? ` ${asset.currency}` : ''}</dd></div>)}</dl>
}
export function AssetDisposalFields({ disposal }: { disposal: Disposal | null }) {
  if (disposal === null) return <p className="text-base">{text.noDisposal}</p>
  return <section className="min-w-0 space-y-3"><p className="text-sm text-muted-foreground">{text.dispose}；{text.version}：{disposal.revision}</p><dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['kind', 'currency', 'realized_proceeds_minor', 'disposed_at', 'note'] as const).map((name) => <div key={name} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{name === 'kind' ? text.dispose : text.fields[name]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{assetValue(name, disposal[name])}</dd></div>)}</dl></section>
}
export function AssetList({ assets, navigationState }: { assets: readonly Asset[]; navigationState: object }) {
  return <section className="min-w-0 space-y-4"><p className="text-sm text-muted-foreground">{messages.catalog.pageCount}{assets.length}</p><p className="max-w-prose text-base text-muted-foreground">{text.listIdentityHelp}</p>
    {!assets.length ? <div className="space-y-3"><h2 className="text-xl font-semibold">{text.empty}</h2><p>{text.emptyHelp}</p><Link to="/items" className="inline-block min-h-12 rounded-sm py-3 underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-ring">{text.browseItems}</Link></div> : <ul className="divide-y rounded-lg border bg-card">{assets.map((asset) => <li key={asset.item_unit_id} className="min-w-0 space-y-4 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_20rem]">
      {pathIDSchema.safeParse(asset.item_unit_id).success ? <Link to={assetRoute(asset.item_unit_id)} state={navigationState} className="block min-h-12 rounded-sm py-2 text-lg font-semibold underline-offset-4 hover:underline [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{text.identifier}：{asset.item_unit_id}</Link> : <p className="[overflow-wrap:anywhere]">{asset.item_unit_id}；{messages.holdings.unaddressable}</p>}
      <AssetProfileFields asset={asset} /><p className="text-sm text-muted-foreground">{text.assetProof}{asset.revision}</p>
    </li>)}</ul>}
  </section>
}
export function AssetEvents({ source }: { source: AssetUsageSource | AssetCostsSource }) {
  const id = useId(), [search, setSearch] = useState('')
  const rows = source.kind === 'usage' ? source.usage : source.costs
  const visible = search ? rows.filter((row) => JSON.stringify(row).includes(search)) : rows
  return <section className="min-w-0 space-y-5"><p className="max-w-prose text-base text-muted-foreground">{text.immutable}</p><p className="text-sm text-muted-foreground">{text.eventCount}{rows.length}</p>
    {!!rows.length && <div className="min-w-0 space-y-2"><label htmlFor={id} className="text-base font-medium">{text.eventSearch}</label><Input id={id} type="search" value={search} onChange={(event) => setSearch(event.target.value)} className="h-auto min-h-12 py-3 text-base md:text-base" /></div>}
    {!visible.length ? <p role="status">{rows.length ? text.noMatches : text.noEvents}</p> : <ul className="divide-y rounded-lg border bg-card">{visible.map((event) => <li key={event.id} className="min-w-0 space-y-4 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_22rem]">
      <h3 className="text-lg font-semibold [overflow-wrap:anywhere]">{'delta' in event ? assetValue('event_type', event.event_type) : assetValue('kind', event.kind)}</h3><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.eventID}：{event.id}；{text.version}：{event.revision}</p>
      <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{Object.entries(event).filter(([name]) => !['id', 'revision', 'item_unit_id'].includes(name)).map(([name, value]) => <div key={name} className={`min-w-0 space-y-1 ${name === 'note' || name === 'correction_of' ? 'sm:col-span-2' : ''}`}><dt className="text-sm font-medium text-muted-foreground">{text.fields[name as keyof typeof text.fields]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{name === 'correction_of' && value === null ? ('delta' in event ? event.event_type === 'correction' : event.kind === 'adjustment') ? text.legacyReference : text.unknownValue : assetValue(name, value)}</dd></div>)}</dl>
    </li>)}</ul>}
  </section>
}
