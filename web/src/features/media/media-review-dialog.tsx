import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { MediaError } from './media-error'
import type { useMediaMutation } from './use-media-mutation'

const text = messages.media
export function MediaReviewDialog({ operation, online, current, onReadCurrent }: {
  operation: ReturnType<typeof useMediaMutation>; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown>
}) {
  const command = operation.command
  if (!command || command.kind !== 'unlink') return null
  const link = command.source.links.find((value) => value.id === command.linkID)
  if (!link) return null
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={text.removeHelp} confirmLabel={text.confirmRemove} destructive
    online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<MediaError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p>
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    <dl className="min-w-0 space-y-4">
      {[[text.owner, `${text.ownerKinds[command.source.owner.kind]}：${JSON.stringify(command.source.owner.value.id)}`],
        [text.linkID, JSON.stringify(link.id)], [text.linkVersion, String(link.revision)], [text.mediaID, JSON.stringify(link.media_id)],
        [text.cover, link.is_cover ? text.yes : text.no], [text.order, String(link.sort_order)], [text.description, JSON.stringify(link.description)]]
        .map(([label, value]) => <div key={label} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>)}
    </dl>
  </MutationReviewDialog>
}
