import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { LoaderCircle } from 'lucide-react'
import { MutationReviewDialog, mutationControl as control } from '@/components/mutation-review'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { fetchItemsPage } from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { catalogRoute } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import type { ScanBarcodeDraft } from '@/lib/scan-handoff'
import { quoteExactText } from '@/lib/text-literal'
import { readBarcodeChoices } from '@/features/scan/barcode-model'
import { commandName, type CatalogCommand } from './commands'
import { CatalogError } from './catalog-error'
import { catalogDisplay } from './display'
import type { CatalogMutation } from './use-catalog-mutation'
import { useReviewCandidates } from './use-review-candidates'

const text = messages.catalog, scan = messages.scan
const labels = { create: text.confirmCreate, patch: text.confirmSave, archive: text.confirmArchive, restore: text.confirmRestore, delete: text.confirmDelete }
function reviewRows(command: CatalogCommand) {
  const rows: { label: string; before?: string; after: string }[] = []
  if (command.kind === 'create' || command.kind === 'patch') {
    for (const key of Object.keys(text.fields) as (keyof typeof text.fields)[]) {
      const value = command.body[key]
      if (value === undefined) continue
      const after = key === 'category_id' && command.category ? `「${command.category.name}」 (${command.category.id})` : catalogDisplay(key, value)
      rows.push({ label: text.fields[key], ...(command.kind === 'patch' ? { before: catalogDisplay(key, command.subject[key]) } : {}), after })
    }
  } else if (command.kind === 'archive' || command.kind === 'restore') {
    rows.push({ label: text.fields.status, before: catalogDisplay('status', command.subject.status), after: command.kind === 'archive' ? text.archived : text.active })
    if (command.kind === 'archive' && command.body.reason !== undefined) rows.push({ label: text.reason, after: catalogDisplay('reason', command.body.reason) })
  }
  return rows
}

type ReviewProps = {
  operation: CatalogMutation; online: boolean; networkOnline: boolean; sessionChecking: boolean; onReadCurrent: () => Promise<unknown>; onSessionLost: (mandatory: boolean) => void
  scanDraft?: ScanBarcodeDraft; scanHandoffRequested?: boolean
}
type ActiveReviewProps = ReviewProps & { command: CatalogCommand; candidates?: ReactNode; confirmDisabled?: boolean }

function CatalogReview({ operation, command, online, onReadCurrent, scanDraft, scanHandoffRequested = false, candidates, confirmDisabled }: ActiveReviewProps) {
  const help = command.kind === 'delete' ? text.deleteHelp : command.kind === 'archive' ? text.archiveHelp : command.kind === 'restore' ? text.restoreHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={labels[command.kind]} destructive={command.kind === 'delete'} confirmDisabled={confirmDisabled} online={online} onReadCurrent={onReadCurrent} error={<CatalogError error={operation.error} />}>
    <p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">「{commandName(command)}」</p>
    {command.kind === 'create' && scanHandoffRequested && (scanDraft ? <section className="min-w-0 space-y-2 rounded-lg border p-4"><h3 className="font-semibold">{scan.handoffTitle}</h3><p className="text-base leading-relaxed">{scan.reviewHandoffHelp}</p><p className="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{scan.input}：{quoteExactText(scanDraft.rawValue)}<br />{scan.symbology}：{scanDraft.symbology ? messages.owned.symbologies[scanDraft.symbology] : scan.chooseFormatLater}</p></section>
      : <Alert role="alert" className="p-4"><AlertDescription className="text-base">{scan.handoffLost}</AlertDescription></Alert>)}
    {candidates}
    {'subject' in command && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{command.subject.id}<br />{text.originalVersion}{command.subject.revision}</p>}
    <dl className="space-y-4">{reviewRows(command).map((row) => <div key={row.label} className="space-y-1 border-b pb-3"><dt className="text-base font-medium">{row.label}</dt>
      {row.before !== undefined && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{row.before}</dd>}
      <dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{row.after}</dd>
    </div>)}</dl>
  </MutationReviewDialog>
}

function BarcodeReview({ command, scanDraft, onSessionLost, sessionChecking, candidates: otherCandidates, confirmDisabled: otherDisabled, ...props }: ActiveReviewProps & { scanDraft: ScanBarcodeDraft }) {
  const available = props.online && !sessionChecking
  const review = useReviewCandidates({ root: 'barcode-review', commandKey: command.key, available, onSessionLost,
    read: async (cursor, signal) => {
      const result = await readBarcodeChoices({ value: scanDraft.rawValue, ...(cursor ? { cursor } : {}) }, signal)
      return { data: result.matches, next_cursor: result.next_cursor }
    }, identity: ({ barcode }) => barcode.barcode_id })
  const { scope, page, current, failed, canPage, canChoose, skipped, stale } = review
  const candidates = <section aria-labelledby="barcode-review-candidates" className="min-w-0 space-y-4 rounded-lg border p-4" aria-busy={available && page.isFetching}>
    <h3 id="barcode-review-candidates" className="font-semibold">{scan.reviewCandidates}</h3>
    <p className="text-base leading-relaxed">{scan.reviewCandidatesHelp}</p>
    {!available ? <p role="status">{sessionChecking ? scan.checking : props.networkOnline ? scan.reviewSessionError : scan.offline}</p>
      : stale ? <p role="status">{scan.reviewCandidatesStale}</p>
        : page.isFetching || page.isPending ? <p role="status" className="flex items-center gap-3"><LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" aria-hidden="true" />{scan.searching}</p>
        : failed ? <CatalogError error={page.error ?? new ApiError('INVALID_RESPONSE')} />
          : current && <>
            <p role="status" className="text-sm text-muted-foreground">{text.pageCount}{current.data.length}</p>
            {current.data.length === 0 ? <p>{scope.cursor ? scan.reviewCandidatesEmptyPage : scan.reviewCandidatesEmpty}</p>
              : <ul aria-label={scan.reviewCandidates} className="divide-y border-y">{current.data.map(({ barcode, item }) => {
                const path = pathIDSchema.safeParse(item.id).success && !item.id.includes('%2F') ? catalogRoute(item.id) : undefined
                return <li key={barcode.barcode_id} className="min-w-0 space-y-2 py-4"><p className="whitespace-pre-wrap font-semibold [overflow-wrap:anywhere]">{quoteExactText(item.name)}</p>
                  <p className="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{text.fields.status}：{catalogDisplay('status', item.status)}；{text.fields.tracking_mode}：{catalogDisplay('tracking_mode', item.tracking_mode)}<br />{scan.symbology}：{messages.owned.symbologies[barcode.symbology]}<br />{scan.itemID}：{quoteExactText(item.id)}<br />{scan.barcodeID}：{quoteExactText(barcode.barcode_id)}</p>
                  {path ? <Button asChild variant="outline" className={control}><Link to={path} aria-label={`${scan.reviewUseItem}：${quoteExactText(item.name)} ${quoteExactText(item.id)}`}>{scan.reviewUseItem}</Link></Button> : <p className="text-sm leading-relaxed">{scan.unavailableItem}</p>}
                </li>
              })}</ul>}
            {current.next_cursor && <p className="text-sm text-muted-foreground">{scan.reviewCandidatesMore}</p>}
          </>}
    {skipped && failed && <p role="status" className="text-sm leading-relaxed">{scan.reviewCandidatesSkipped}</p>}
    <nav aria-label={scan.reviewCandidatesPagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canPage || !scope.trail.length} className={control} onClick={review.previous}>{text.previousPage}</Button><Button type="button" variant="outline" disabled={!canChoose || !current?.next_cursor} className={control} onClick={review.next}>{text.nextPage}</Button></nav>
    <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!available || page.isFetching} className={control} onClick={review.restart}>{scan.reviewCandidatesRetry}</Button>{failed && !skipped && <Button type="button" variant="outline" className={control} onClick={review.skip}>{scan.reviewCandidatesSkip}</Button>}</div>
  </section>
  const confirmDisabled = !!otherDisabled || !available || props.operation.phase !== 'uncertain' && !review.proceed
  return <CatalogReview {...props} command={command} scanDraft={scanDraft} onSessionLost={onSessionLost} sessionChecking={sessionChecking} candidates={<>{otherCandidates}{candidates}</>} confirmDisabled={confirmDisabled} />
}

type CreateCommand = Extract<CatalogCommand, { kind: 'create' }>
function CreateReview({ command, onSessionLost, sessionChecking, ...props }: ActiveReviewProps & { command: CreateCommand }) {
  const available = props.online && !sessionChecking
  // Go's list filter trims Unicode White_Space. Do not turn a whitespace-only
  // name into an unfiltered catalog query or describe that page as a match.
  const searchable = /[^\p{White_Space}]/u.test(command.body.name)
  const review = useReviewCandidates({ root: 'item-name-review', commandKey: command.key, searchable, available, onSessionLost,
    read: (cursor, signal) => fetchItemsPage({ q: command.body.name, limit: 5, ...(cursor ? { cursor } : {}) }, signal), identity: (item) => item.id })
  const { scope, page, current, failed, canPage, canChoose, skipped, stale } = review
  const candidates = <section aria-labelledby="item-name-review-candidates" className="min-w-0 space-y-4 rounded-lg border p-4" aria-busy={available && page.isFetching}>
    <h3 id="item-name-review-candidates" className="font-semibold">{text.relatedTitle}</h3>
    <p className="text-base leading-relaxed">{text.relatedHelp}</p>
    <p className="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{text.fields.name}：{quoteExactText(command.body.name)}</p>
    {!available ? <p role="status">{sessionChecking ? scan.checking : props.networkOnline ? scan.reviewSessionError : scan.offline}</p>
      : stale ? <p role="status">{text.relatedStale}</p>
        : !searchable ? <p role="status">{text.relatedWhitespace}</p>
          : page.isFetching || page.isPending ? <p role="status" className="flex items-center gap-3"><LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" aria-hidden="true" />{text.loading}</p>
            : failed ? <CatalogError error={page.error ?? new ApiError('INVALID_RESPONSE')} />
              : current && <>
                <p role="status" className="text-sm text-muted-foreground">{text.pageCount}{current.data.length}</p>
                {current.data.length === 0 ? <p>{scope.cursor ? text.relatedEmptyPage : text.relatedEmpty}</p>
                  : <ul aria-label={text.relatedTitle} className="divide-y border-y">{current.data.map((item) => {
                    const path = pathIDSchema.safeParse(item.id).success && !item.id.includes('%2F') ? catalogRoute(item.id) : undefined
                    return <li key={item.id} className="min-w-0 space-y-2 py-4"><p className="whitespace-pre-wrap font-semibold [overflow-wrap:anywhere]">{quoteExactText(item.name)}</p>
                      <p className="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{text.fields.status}：{catalogDisplay('status', item.status)}；{text.fields.tracking_mode}：{catalogDisplay('tracking_mode', item.tracking_mode)}<br />{text.identifier}：{quoteExactText(item.id)}</p>
                      {path ? <Button asChild variant="outline" className={control}><Link to={path} aria-label={`${text.relatedOpen}：${quoteExactText(item.name)} ${quoteExactText(item.id)}`}>{text.relatedOpen}</Link></Button> : <p className="text-sm leading-relaxed">{scan.unavailableItem}</p>}
                    </li>
                  })}</ul>}
                {current.next_cursor && <p className="text-sm text-muted-foreground">{text.relatedMore}</p>}
              </>}
    {skipped && failed && <p role="status" className="text-sm leading-relaxed">{text.relatedSkipped}</p>}
    {searchable && <nav aria-label={text.relatedPagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canPage || !scope.trail.length} className={control} onClick={review.previous}>{text.previousPage}</Button><Button type="button" variant="outline" disabled={!canChoose || !current?.next_cursor} className={control} onClick={review.next}>{text.nextPage}</Button></nav>}
    <div className="flex flex-wrap gap-2">{searchable && <Button type="button" variant="outline" disabled={!available || page.isFetching} className={control} onClick={review.restart}>{text.relatedRetry}</Button>}{failed && !skipped && <Button type="button" variant="outline" className={control} onClick={review.skip}>{text.relatedSkip}</Button>}</div>
  </section>
  const confirmDisabled = !available || props.operation.phase !== 'uncertain' && !review.proceed
  const content = { ...props, command, onSessionLost, sessionChecking, candidates, confirmDisabled }
  return props.scanDraft ? <BarcodeReview {...content} scanDraft={props.scanDraft} /> : <CatalogReview {...content} />
}

export function CatalogReviewDialog(props: ReviewProps) {
  const command = props.operation.command
  if (!command) return null
  return command.kind === 'create'
    ? <CreateReview key={command.key} {...props} command={command} />
    : <CatalogReview {...props} command={command} />
}
