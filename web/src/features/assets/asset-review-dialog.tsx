import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { UnitFields } from '@/features/holdings/holding-fields'
import { AssetError } from './asset-error'
import { AssetDisposalFields, AssetProfileFields, AssetProofs } from './asset-fields'
import { assetValue } from './display'
import type { AssetMutation } from './use-asset-mutation'

const text = messages.assets
export function AssetReviewDialog({ operation, online, current, onReadCurrent }: { operation: AssetMutation; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const lifecycle = command.kind === 'retire-asset' || command.kind === 'dispose-asset'
  const target = command.kind === 'record-usage' ? command.source.usage.find((row) => row.id === command.body.correction_of) : command.kind === 'record-cost' ? command.source.costs.find((row) => row.id === command.body.correction_of) : undefined
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={text.reviewHelp} confirmLabel={text.confirms[command.kind]} destructive={lifecycle} online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<AssetError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p><AssetProofs source={command.source} /><p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(command.source.item.name)}；{JSON.stringify(command.source.unit.asset_code)}</p>
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    {command.source.asset && <section className="min-w-0 space-y-3"><h3 className="text-base font-semibold">{text.previous}：{text.profile}</h3><AssetProfileFields asset={command.source.asset} /></section>}
    {lifecycle && <><UnitFields unit={command.source.unit} /><AssetDisposalFields disposal={command.source.disposal} /><p className="text-base">{text.proposed}：{messages.holdings.statuses[command.kind === 'retire-asset' && command.source.unit.status === 'disposed' ? 'disposed' : command.kind === 'retire-asset' ? 'retired' : 'disposed']}</p><p className="text-base">{command.kind === 'dispose-asset' ? text.disposalHelp : text.retirementHelp}</p></>}
    <dl className="min-w-0 space-y-4">{Object.entries(command.body).map(([name, value]) => <div key={name} className="min-w-0 space-y-2 border-b pb-3"><dt className="text-base font-medium">{name === 'kind' && command.kind === 'dispose-asset' ? text.dispose : text.fields[name as keyof typeof text.fields]}</dt>
      {command.kind === 'put-profile' && command.source.asset !== null && Object.hasOwn(command.source.asset, name) && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{assetValue(name, command.source.asset[name as keyof typeof command.source.asset])}</dd>}
      <dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{assetValue(name, value)}{name.endsWith('_minor') && value !== null ? ` ${'currency' in command.body ? command.body.currency : command.source.asset?.currency}` : name === 'delta' ? ` ${command.source.asset?.usage_unit}` : ''}</dd>
    </div>)}</dl>
    {target && <section className="min-w-0 space-y-3"><h3 className="text-base font-semibold">{text.fields.correction_of}</h3><dl className="min-w-0 space-y-3">{Object.entries(target).map(([name, value]) => <div key={name} className="min-w-0"><dt className="text-sm text-muted-foreground">{name === 'id' ? text.eventID : name === 'revision' ? text.version : name === 'item_unit_id' ? text.identifier : text.fields[name as keyof typeof text.fields]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{assetValue(name, value)}</dd></div>)}</dl></section>}
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.immutable}</p>
  </MutationReviewDialog>
}
