import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { AssetError } from '@/features/assets/asset-error'
import { AssetProfileFields } from '@/features/assets/asset-fields'
import { UnitFields } from '@/features/holdings/holding-fields'
import { DisposalCorrectionProofs, DisposalDecision } from './disposal-fields'
import type { CorrectionMutation } from './use-correction-mutation'

const text = messages.disposalCorrections, asset = messages.assets
export function DisposalCorrectionReview({ operation, online, current, onReadCurrent }: { operation: CorrectionMutation; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command || command.kind !== 'correct-disposal') return null
  const { source, body } = command
  const proposed = body.replacement === null ? null : { item_unit_id: source.unit.id, ...body.replacement, note: body.replacement.note ?? '' }
  const label = body.action === 'void' ? text.confirmVoid : source.disposal.current === null ? text.confirmReinstate : text.confirmReplace
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={text.reviewHelp} confirmLabel={label} destructive={body.action === 'void'} online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<AssetError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p><DisposalCorrectionProofs source={source} />
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    <section className="min-w-0 space-y-3"><h2 className="text-lg font-semibold">{asset.profile}</h2><AssetProfileFields asset={source.asset} /><UnitFields unit={source.unit} /></section>
    <DisposalDecision title={text.original} data={source.disposal.original} /><DisposalDecision title={text.current} data={source.disposal.current} /><DisposalDecision title={text.proposed} data={proposed} />
    <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.reason}：{JSON.stringify(body.reason)}</p><p className="max-w-prose text-base">{text.voidHelp}</p>
  </MutationReviewDialog>
}
