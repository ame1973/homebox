import { MutationReviewDialog } from '@/components/mutation-review'
import type { Tag } from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import type { CatalogRowKind } from '@/lib/catalog-navigation'
import { CatalogError } from './catalog-error'
import { OwnedFields } from './owned-fields'
import { ownedFields } from './display'
import type { OwnedMutation } from './use-owned-mutation'

const text = messages.owned
export function TagSet({ tags, label }: { tags: readonly Tag[]; label: string }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-base font-semibold">{label}（{tags.length}）</h3>{tags.length ? <ul className="space-y-2">{tags.map((tag) => <li key={tag.id} className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(tag.name)}<span className="block text-sm text-muted-foreground">{tag.id}；{text.version}：{tag.revision}</span></li>)}</ul> : <p>{text.emptyTags}</p>}</section>
}

export function OwnedReviewDialog({ operation, online, onReadCurrent }: { operation: OwnedMutation; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const deleting = command.kind.startsWith('delete-')
  const kind: CatalogRowKind = command.kind.endsWith('barcode') ? 'barcodes' : command.kind.endsWith('alias') ? 'aliases' : 'attributes'
  const confirmLabel = command.kind === 'replace-tags' ? text.confirmTags : deleting ? text.confirmDelete[kind] : command.kind.startsWith('create-') ? text.confirmCreate[kind] : text.confirmSave
  const help = command.kind === 'replace-tags' ? text.tagHelp : deleting ? text.deleteHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={confirmLabel} destructive={deleting || command.kind === 'replace-tags' && !command.after.length && !!command.before.length} online={online} onReadCurrent={onReadCurrent} error={<CatalogError error={operation.error} />}>
    <div className="min-w-0 space-y-2"><p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{text.owner}：{JSON.stringify(command.owner.name)}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{command.owner.id}<br />{'subject' in command ? `${text.identifier}：${command.subject.id}` : `${text.ownerProof}${command.owner.revision}`}</p></div>
    {'subject' in command && <p className="text-sm text-muted-foreground">{text.rowProof}{command.subject.revision}</p>}
    {command.kind === 'replace-tags' ? <><TagSet tags={command.before} label={text.beforeTags} /><TagSet tags={command.after} label={text.afterTags} />{!command.after.length && <p className="text-base leading-relaxed">{text.clearHelp}</p>}</>
      : 'body' in command ? <dl className="space-y-4">{ownedFields(command.body).map((field) => <div key={field.key} className="min-w-0 space-y-1 border-b pb-3"><dt className="text-base font-medium">{field.label}</dt>{'subject' in command && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{ownedFields(command.subject).find((original) => original.key === field.key)!.value}</dd>}<dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{field.value}</dd></div>)}</dl>
        : <OwnedFields value={command.subject} />}
    {'subject' in command && 'symbology' in command.subject && <p className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.fields.normalized_value}：{JSON.stringify(command.subject.normalized_value)}</p>}
  </MutationReviewDialog>
}
