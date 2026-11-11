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
  if (!command) return null
  if (command.kind === 'attach') {
    const owner = command.owner.owner, media = command.membership.media
    const name = owner.kind === 'unit' ? owner.value.asset_code : owner.value.name
    const fields = [[text.owner, `${text.ownerKinds[owner.kind]}：${JSON.stringify(name)}；${JSON.stringify(owner.value.id)}`],
      [text.version, String(owner.value.revision)], [text.mediaID, JSON.stringify(media.id)], [text.mediaVersion, String(media.revision)],
      [text.selectedMedia, media.verification === 'verified' ? text.verified : text.unverified], [text.ownerLinkCount, String(command.owner.links.length)],
      [text.membershipCount, String(command.membership.links.length)], [text.cover, command.body.is_cover ? text.yes : text.no],
      [text.order, String(command.body.sort_order ?? 0)], [text.description, JSON.stringify(command.body.description ?? '')]]
    return <MutationReviewDialog operation={operation} title={text.attachReviewTitle} help={text.attachReviewHelp} confirmLabel={text.confirmAttach}
      online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<MediaError error={operation.error} />}>
      <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p>
      {command.owner.links.some((link) => link.media_id === media.id) && <Alert className="p-4"><AlertDescription className="text-base">{text.alreadyLinked}</AlertDescription></Alert>}
      {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.attachReviewStale}</AlertDescription></Alert>}
      <dl className="min-w-0 space-y-4">{fields.map(([label, value]) => <div key={label} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>)}</dl>
    </MutationReviewDialog>
  }
  const link = command.source.links.find((value) => value.id === command.linkID)
  if (!link) return null
  const editing = command.kind === 'patch-link'
  const labels = { is_cover: text.cover, sort_order: text.order, description: text.description }
  const display = (key: keyof typeof labels, value: boolean | number | string) => key === 'description' ? JSON.stringify(value) : typeof value === 'boolean' ? value ? text.yes : text.no : String(value)
  return <MutationReviewDialog operation={operation} title={editing ? text.editReviewTitle : text.reviewTitle} help={editing ? text.editReviewHelp : text.removeHelp} confirmLabel={editing ? text.confirmEdit : text.confirmRemove} destructive={!editing}
    online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<MediaError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p>
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    <dl className="min-w-0 space-y-4">
      {[[text.owner, `${text.ownerKinds[command.source.owner.kind]}：${JSON.stringify(command.source.owner.value.id)}`],
        [text.linkID, JSON.stringify(link.id)], [text.linkVersion, String(link.revision)], [text.mediaID, JSON.stringify(link.media_id)]]
        .map(([label, value]) => <div key={label} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>)}
      {command.kind === 'patch-link' ? (Object.keys(command.body) as (keyof typeof labels)[]).map((key) => <div key={key} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-base font-medium">{labels[key]}</dt><dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{display(key, link[key])}</dd><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{display(key, command.body[key]!)}</dd></div>)
        : [[text.cover, link.is_cover ? text.yes : text.no], [text.order, String(link.sort_order)], [text.description, JSON.stringify(link.description)]]
          .map(([label, value]) => <div key={label} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>)}
    </dl>
  </MutationReviewDialog>
}
