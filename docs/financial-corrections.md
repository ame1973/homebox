# Audited Financial Corrections

**Availability: all five protected financial-correction HTTP operations, stateless browser adapters and bracketed correction source models are implemented.** Migrations 0017/0018 and the private `Purchases.CorrectLine/PageLineCorrections` and `Assets.GetDisposalRecord/CorrectDisposal/PageDisposalCorrections` services provide their authoritative decisions and authenticated history. The HTTP handlers enforce the existing protected session/Origin/CSRF/version/key boundary, strict nested JSON, original independent proofs and final SQL authorization. Verified TLS/cookie-jar tests and a compiled-server/socket contract check exercise the actual endpoints. The adapters validate complete original contexts, exact replies and independent proofs without hidden refresh or replay; strict type/runtime and private actual-SDK/Go browser tests cover them. Complete correction form/command/controller composition and operating screens remain unfinished; the existing [seven purchase operations](purchases.md) and [eleven Asset financial operations](assets.md) do not provide those correction screens.

This closes an interface gap in the read-only source requirements without weakening posted-history immutability. The root OpenAPI now contains 142 operations and 177 schemas, preserving all original 103 operation identities and every existing schema, including all version-one offline payloads. Specification revision v0.9.3 is not an application release.

## Financial decisions are not physical inventory

- **Allocation correction:** explicitly void one active posted attribution and optionally post a complete replacement with a new independent Line identity in the same transaction. Never edit the original Line's amount, quantity or owner in place.
- **Financial void:** remove a recorded decision's current accounting effect while retaining the original decision, actual references and immutable correction history. A legitimate zero-valued allocation or disposal is not a void.
- **Disposal financial correction:** replace, void or explicitly reinstate recorded disposal finance. It never disposes of the object again, restores inventory, revives a Unit, moves it or infers retirement/service dates.
- **Cost adjustment:** an expense/credit against an actual extra-cost event. It is not a substitute for correcting acquisition attribution or realized disposal proceeds.

A physical return or movement is a separately reviewed inventory command. Neither financial correction automatically creates stock, decrements a quantity, changes a Unit's operational status or converts between currencies. Retirement-date correction remains separate unfinished work; these operations do not clear or overwrite a recorded retirement.

## Protected HTTP operations

Paths are relative to `/api/v1`. All five require an active completed administrator session and return `Cache-Control: no-store`. The two writes additionally require `Content-Type: application/json`, the exact configured Origin, CSRF token, compatible application version, original idempotency key and original subject proofs. Request JSON is at most 128 KiB, with no duplicate/unknown fields at either object level, invalid Unicode, lossy numeric parsing or query parameters. Authorization and source validation are repeated inside SQL after body reading. Reads accept no body or content encoding. See [HTTP Authentication and Transport](http-authentication.md) for session establishment and transport configuration.

| Method | Path | Original proof or read scope | Response |
| --- | --- | --- | --- |
| GET | `/purchases/{id}/line-corrections` | Actual Purchase; independent owned live page | `PurchaseLineCorrectionList`, no ETag |
| POST | `/purchases/{id}/line-corrections` | Purchase If-Match, original Line and original owning Item body revisions; replacement's own Item proof | `PurchaseLineCorrection`, independent entry ETag |
| GET | `/units/{id}/disposal-record` | Actual retained original disposal, including a financially voided one | `DisposalRecord`, actual independent Disposal ETag |
| GET | `/units/{id}/disposal-corrections` | Actual original disposal; independent owned live page | `DisposalCorrectionList`, no ETag |
| POST | `/units/{id}/disposal-corrections` | DisposalRecord If-Match plus original Asset body revision | `DisposalCorrection`, independent entry ETag |

### Review and send

1. For a Purchase correction, read the actual Purchase and its current Lines, and read each applicable owning/replacement Item. Retain the Purchase ETag, the original Line's `revision` and the original Item `revision`; replacement has its own reviewed Item revision and explicit nullable Unit decision. Send the complete request under the original Purchase `If-Match`.
2. For Disposal finance, read `/units/{id}/disposal-record` and `/units/{id}/asset`. Retain the actual DisposalRecord ETag and the Asset `revision`. Send both original proofs with the complete replacement or explicit null void. After a void, read the retained record again before explicitly reinstating; the current `/disposal` 404 is not a proof.
3. For either POST, send `Origin`, `X-CSRF-Token`, `X-HomeBox-Client-Version`, a new 8–128-character `Idempotency-Key` and the quoted original `If-Match`. Preserve the exact body and key for explicit reconciliation if the outcome is uncertain; never automatically resubmit, refresh proofs or install a frozen response as current state.
4. After success, reread the relevant current resources and complete history. A response ETag belongs to the new immutable correction entry, not to its Purchase or Disposal subject. History lists do not return an ETag. A fresh double void with current prerequisite proofs is rejected; `PURCHASE_LINE_VOIDED` identifies an already voided allocation, while an already voided disposal returns `ASSET_HISTORY_LOCKED`.

Correction-entry revision one belongs to an actual newly stored immutable entry. It is never the Purchase, Line, Asset or Disposal proof. A history page, summary `source_revision`, coincident counter or failed read cannot provide a subject proof. Do not compute a new proof by adding one to a reply; read and review the actual subject resource for a different command.

## Stateless browser adapters

`web/src/lib/api/financial-corrections.ts` exposes `fetchPurchaseCorrectionsPage`, `correctPurchaseLine`, `fetchDisposalRecord`, `fetchDisposalCorrectionsPage` and `correctDisposal`. They issue exactly one request per call using the shared same-origin transport, caller-owned CSRF/key/AbortSignal, eight-second read and 30-second write deadlines. They do not install cache state, fetch another proof, automatically paginate, retry, rebase, store or replay a mutation. A canceled or malformed response after sending does not prove rollback.

- Purchase correction retains the whole original Purchase, original Line and its owning Item, plus explicit `replacementItem: null` for a void or the whole reviewed replacement Item for replacement. Body IDs/revisions must match those snapshots; a single Item used for both roles must be the same complete snapshot. The original Purchase revision becomes `If-Match`; Line/Item and replacement-Item proofs stay in their documented body fields.
- Disposal correction retains the whole original Asset and actual `DisposalRecord`. The record revision becomes `If-Match`; the Asset revision must match the body. A void requires a nonnull original current decision; reinstatement uses the explicit null current record with a complete replacement. No Unit, Item, summary or history-entry proof is added.
- Strict response validation checks complete original and proposed data, independent entry revision/strong ETag, owner identities, action, reason and actual subject-revision progression. Page results require distinct entries, the selected owner, bounded scoped continuations and no list ETag. Retained data keeps broad valid historical values; new inputs enforce explicit nullable Unit intent, named-Unit quantity one, non-sale zero proceeds and exact safe integers. Finite new-money eligibility and complete proposed totals remain server-authoritative.
- Changed financial instants normalize to UTC without losing nanoseconds; a time equal to the current retained disposal keeps its exact spelling, or the original spelling when reinstating after a void. This is a comparison of reviewed context and reply, not an inferred current source.

For example, after the caller has actually read and reviewed the complete original resources, a Purchase void is explicit:

```ts
import { correctPurchaseLine } from '@/lib/api/financial-corrections'

await correctPurchaseLine(purchase.id, {
  line_id: line.id, line_revision: line.revision, item_revision: item.revision,
  action: 'void', replacement: null, reason: 'Incorrect allocation',
}, {
  csrfToken, idempotencyKey: originalConfirmedKey, signal,
  purchase, line, item, replacementItem: null,
})
```

The caller owns review, confirmation, refreshing current reads and preserving the original command for an explicit same-key reconciliation. These adapters are not yet wired to correction forms or screens.

## Complete correction review sources

`readPurchaseCorrectionHistory` completes the selected Purchase's actual current Lines and correction-entry pages between matching full Purchase reads, including when no active Line remains. It validates that corrected originals are no longer current and that every actual replacement leads to either the matching current Line or another retained correction, without treating entry IDs as chronological. Duplicate, missing, foreign or cyclic links withhold the whole source. This is a live consistency-checked read, not a frozen sync snapshot or a reconstructed financial total.

`readPurchaseCorrection` additionally selects one actual active Line and brackets its owning Item and any addressable original Unit inside the Purchase interval. An explicit null replacement target reads no replacement scope. A selected quantity Item has no invented Unit or Stock collection; a selected individual Item completes its actual Unit pages between matching Item reads. A target equal to the original Item shares the same complete snapshot. Original Unit data is review context, not another wire proof; unallocated and path-unaddressable retained Unit identities are represented distinctly rather than silently converted to null. A missing selected Line does not become a new allocation.

`readDisposalCorrection` brackets the actual Unit, owning Item, Asset and retained DisposalRecord around the complete selected correction history. It validates the actual subject-revision chain and immutable original across unordered entry IDs, without assuming the first retained revision was one. An actual 404 or failed read remains an error, not a fabricated null current decision. A voided record remains reviewable; zero proceeds and void remain distinct. Neither a current-disposal 404 nor a summary or correction-entry revision is treated as a subject proof. Legacy retained currency/proceeds are not repriced or turned into new-money eligibility.

These sources do not query unrelated histories, unselected Items, Stock or summary totals. Tests complete 205-entry scopes, reject mixed/failed intervals and exercise real concurrent Purchase and Disposal changes through the protected Go server. Raw editable forms, explicit adoption, frozen commands, review controls and operating screens are still required.

## Purchase allocation correction

The request freezes three distinct existing scopes:

1. The original complete Purchase supplies its `If-Match`.
2. `line_id` plus `line_revision` identify the actual active original Line and its independent revision.
3. `item_revision` binds that original Line's owning Item interpretation.

For replacement, the nested complete `PurchaseLineWrite` additionally supplies the new target Item ID/revision and explicit nullable Unit decision. The old and new Item may differ; neither proof is silently borrowed, fetched or rebased. A named replacement Unit belongs to that target Item and has quantity one. A null Unit supports an explicitly unallocated attribution. `paid_minor` is the whole new allocation, not a per-unit amount, adjustment delta or multiplier.

Example void request body, with the separately reviewed Purchase If-Match:

```json
{
  "line_id": "original-line",
  "line_revision": 9,
  "item_revision": 11,
  "action": "void",
  "replacement": null,
  "reason": "The attribution belongs to another purchase"
}
```

Example replacement request body:

```json
{
  "line_id": "original-line",
  "line_revision": 9,
  "item_revision": 11,
  "action": "replace",
  "replacement": {
    "item_id": "correct-item",
    "item_revision": 17,
    "item_unit_id": null,
    "quantity_base": 2,
    "paid_minor": 0,
    "note": "Corrected complete attribution"
  },
  "reason": "Correct the reviewed Item attribution"
}
```

Both original proof scopes and the explicit replacement decision are required even for zero money. A fresh correction of an already voided original returns `PURCHASE_LINE_VOIDED`; an identical original-key retry first retrieves its already committed result. To correct a replacement later, review that actual active Line under its own identity and proofs, not its predecessor's key or revision.

The original posted Line remains unchanged. Void removes it from current acquisition, current Item-associated/filter/search evidence and current Line projections, emitting its independent tombstone. A replacement is a real new Line with revision one. Detailed immutable history retains the original and nullable replacement together with the reason and actual server-accepted creation time. Do not invent a legacy Line creation instant from its Purchase date.

Voided attribution has no current acquisition effect. Retained historical records still protect identity, original Purchase currency and deletion references; an active zero-paid attribution continues to participate in current financial authority. All affected old/new Asset histories must be validated completely after the proposed void/replacement, including exact safe totals and acquisition-basis/currency rules. A void does not reprice retained money; new replacement money requires the finite supported currency policy, even for zero.

Source, actual correction/audit, old-Line tombstone, optional new Line, Purchase and affected old/new Item/Asset/search/current/outbox/original reply must commit together. A late invalid owner, ignored write, failed publication, revoked session or failed COMMIT must expose neither a partial void nor a partial replacement.

## Implemented private Purchase boundary

Migration 0017 adds one immutable, indexed correction journal with actual Purchase/original-Line/optional-replacement references and a deferred reference to the real mutation event. Each entry has its own stored revision one and exact complete original/input/replacement/Purchase/Item context. Existing posted rows, old current/frozen bytes, counters and epoch are not backfilled or rewritten. Native text bytes are bounded without treating embedded NUL as an end-of-string; Go validates complete UTF-8/code-point bounds and preserves exact reasons.

`Purchases.CorrectLine` snapshots the whole replacement and nullable Unit before fingerprinting, checks all original scopes and uses the existing completed-session domain transaction. It posts a new independent Line only for replacement, voids the original current effect and validates complete proposed old/new Asset histories before publication. Shared Items/Assets advance once; quantities, Unit state, placement and movement history do not change. Original-key replay precedes current proofs/eligibility but never completed authorization; expired claims remain reserved. A failed/ignored required write, altered context/source, unsafe total, late authorization or COMMIT failure returns no successful result and rolls everything back.

Current Line pages skip validated voided rows before slicing, including large raw gaps. Item-associated Purchase, catalog/Stock purchase filters, exact-Unit Asset filters/acquisition and approved search publication classify actual retained source rather than blindly excluding journal matches in SQL. Invalid/missing referenced context fails closed instead of resurrecting the old attribution. The original Line counter advances to its independent tombstone; actual Purchase/old/new Item counters must have advanced beyond their stored original proofs. Detailed history completes its own live pages and never becomes a new offline entity or subject proof. Global Purchase headers and historical currency/deletion guards remain retained even when no allocation is currently active.

The protected Purchase correction/history handlers and stateless adapters delegate to these services. They do not create a CLI financial-write interface or authorize direct database edits. Reviewed correction screens remain separate required work.

## Retained disposal record and financial correction

The existing current-disposal GET remains a current effective resource, not a substitute for the retained record. A financial void makes that current resource absent and publishes its independent tombstone. The separate `DisposalRecord` remains readable with:

- `item_unit_id`: the actual subject Unit.
- `revision`: the actual independent Disposal subject revision, also in its ETag.
- `original`: the exact first posted bounded disposal data, without a guessed historical revision.
- `current`: the complete actual effective data, or explicit null after a financial void.

Original and current owners must match the subject. Null means financially voided only when the actual retained record says so; zero proceeds with a nonnull current decision is a different fact. Genuine absence of original source is 404, and a failed/current-disposal-404 read is never a proof for reinstatement. Read actual source/correction history and real counters, never the current projection cache.

The existing Asset lifecycle screen reads only the effective current disposal. When that read is absent, it says there is no currently effective financial decision, not that no history exists. It warns that the screen cannot reinstate a voided disposal; the ordinary disposal command is rejected when retained history exists. The separate retained-record/correction operating screens remain unfinished; the protected HTTP interface and stateless adapters are available for explicitly reviewed integrations.

The correction request uses the original DisposalRecord `If-Match` and separately required original Asset `asset_revision`. It has no physical Unit or Item body proof. Whole Unit/Item data can remain review context, but their counters do not become additional financial write preconditions.

Example void request body, with the separately reviewed retained DisposalRecord If-Match:

```json
{
  "asset_revision": 13,
  "action": "void",
  "replacement": null,
  "reason": "The recorded sale proceeds were attributed incorrectly"
}
```

A replace body instead supplies a complete `DisposalFinancialWrite`: explicit kind, currency, exact financial date and proceeds including deliberate zero. New non-sale decisions require zero. A replacement can correct an active decision or explicitly reinstate a previously voided financial record under its actual retained subject proof. Ordinary disposal creation cannot bypass that history or reseed revision one after a void.

Every fresh eligible correction is a real immutable decision and advances financial subject/history authority, even when replacement values equal the current decision. Void requires an actually active decision. Retain complete before/after records, exact original data, supplied input/reason and actual audit. New replacement money must match the Asset and finite supported currency policy; a void preserves legacy representation without turning it into a new monetary input. Proposed complete totals must be safe, including negative net where valid; do not reject a corrective proposal only because an older derived net was unsafe.

Financial corrections do not touch Unit status/placement, unchanged physical/location/search scopes, Stock or movement history. The Unit can remain disposed while its financial proceeds decision is voided. No service/retirement date is inferred, and residual remains a separately labeled estimate rather than proceeds. The existing summary's `realized_proceeds_minor` remains an integer: it is zero when there is no current disposal effect as well as for an active zero-proceeds decision. Distinguish those cases using the retained record's nullable `current`, not the summary amount.

## Implemented private Disposal boundary

Migration 0018 adds an indexed immutable journal tied to the actual original Disposal and real deferred audit. Each entry has its own stored revision one, the actual next subject revision and a nullable link to its actual predecessor; native constraints prevent multiple roots, branches and duplicate subject revisions. The original row and all previous decisions, current/frozen bytes, retained counters and certified epoch are not rewritten or backfilled. Before/after records, original Asset and owning Item context, original input, predecessor and reason are retained exactly in canonical private producer JSON. No legacy original revision, creation time or audit is invented.

`Assets.GetDisposalRecord` and the existing current-disposal/summary/materialization reads validate the entire retained correction chain against actual source and independent Disposal, Asset, Unit and Item authority. A missing or corrupt predecessor, field, source or counter withholds the result rather than restoring the first posted financial decision. `Assets.PageDisposalCorrections` validates the complete chain and uses an independently scoped signed live continuation over stable entry IDs. Source validation does not turn the owning Item or physical Unit into an additional correction proof, use a projection cache as authority or retroactively apply new-money policy to retained source.

`Assets.CorrectDisposal` snapshots the complete replacement before fingerprinting and binds the original retained subject revision plus Asset revision. Each fresh eligible decision advances Disposal/Asset/Item once, validates the proposed complete history and atomically publishes the current disposal upsert or tombstone, audit and frozen original reply. Equivalent current dates retain their exact spelling; after a void, an equivalent original date retains the original spelling. A changed date is normalized without losing nanoseconds. Original-key reconciliation follows completed authorization but precedes later source/proof/policy checks; an expired claim remains reserved. An ignored or rewritten required write, corrupt final context, unsafe proposed total, late authorization or failed COMMIT withholds success and rolls back every effect. A finance void cannot authorize ordinary disposal creation or physical retirement through the retained historical reference.

The protected retained-record/correction/history handlers and stateless adapters delegate to these services. They do not create a CLI financial-write interface or authorize direct database edits. Reviewed correction screens remain separate required work.

## Exact input, history and foreground recovery

All supplied amounts/counts are exact safe integers. Replacement acquisition/proceeds are nonnegative; an allocated base count is positive. There is no negative physical count, grouped/decimal display parsing, multiplication by quantity, exponent guessing, automatic FX or rounding of unsafe values. Reasons are required nonempty bounded text, with exact original whitespace/Unicode retained. Financial instants preserve explicit offsets and up to nine fractional digits; changed instants normalize losslessly while equivalent retained spelling stays unchanged. New correction creation time is server-accepted context, never a backfilled legacy date.

Owned history pages default to 50 and cap at 100, use a 16-KiB query budget and 2048-byte signed continuations, and have required nullable `next_cursor`. Only `limit`/`cursor` are accepted. Independent purposes bind principal, epoch, actual owner, collection and size for 15 minutes. Validate every selected row and lookahead before returning any prefix; these are live pages, not frozen sync snapshots, financial totals or subject proofs.

A future browser workflow must complete and bracket only its selected source/history, retain whole original contexts and raw invalid input, show the actual financial before/after and require explicit foreground confirmation. Original body/key/proofs stay together. Unknown sent outcomes retain explicit identical-key reconciliation after later source changes or fresh login; there is no background queue, automatic retry/rebase/reconnect replay or installation of frozen replies as current Query data. Session loss purges private form/command state instead of forwarding money, bodies, proofs or credentials through login returns.

## Offline and verification boundaries

Current financial effects use the existing `purchase_line` and `disposal` upserts/tombstones, with all normative version-one payloads unchanged. A voided old Line cannot reappear from raw retained history during materialization; a corrected disposal publishes its actual current decision. The normative replica remains 22 kinds. Detailed authenticated correction/audit history is not a second offline authoritative ledger or a new projection kind. Old frozen replies/outbox bytes and retained original source remain exact. See [Offline Replica and Safe Draft Protocol](../contracts/sync.md).

Run the executable contract/type, private transaction and protected HTTP checks from the repository root:

```sh
uv run --frozen python -m unittest tests.test_financial_corrections_contracts -v
npm --prefix web run api:check
npm --prefix web run typecheck
npm --prefix web test -- --coverage.enabled=false src/lib/api/financial-corrections.test.ts
npm --prefix web test -- --coverage.enabled=false src/features/purchases/correction-model.test.ts src/features/assets/correction-model.test.ts
npm --prefix web run test:e2e -- --project=phone tests/e2e/financial-corrections-api.spec.ts
go -C server test ./internal/store -run '^TestPurchaseLineCorrection' -count=1
go -C server test ./internal/store -run '^TestDisposalCorrection' -count=1
go -C server test ./internal/httpapi -run '^(TestFinancialCorrection|TestPurchaseCorrectionHTTP|TestDisposalCorrectionHTTP)' -count=1
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzFinancialCorrectionsJSON -fuzztime=3s -parallel=2
```

Negative schemas and strict actual-client type fixtures cover required proofs/null/zero, distinct original-versus-replacement intent, one named Unit, independent immutable entry revisions, bounded retained record/history, exact safe numbers/dates/reasons and unchanged offline shapes. Actual private Go Purchase tests separately cover void/replacement, immutable exact context, independent original proofs, complete 205-entry history/current-page gaps, current evidence/totals, zero versus void, old/new Asset effects, safe proposed versus unsafe old totals, original/fresh-login/expired-key behavior and SQL/ignored/rewritten/authorization/COMMIT rollback. Disposal tests exercise 105 actual linked decisions and complete pages, explicit zero/void/reinstatement, broad retained dates and money, source/counter/chain corruption, complete proposed totals, independent original proofs, replay/concurrency and statement/late-authorization/COMMIT rollback. Fresh/certified schema-16/17 upgrades retain exact old source/current/frozen bytes/epoch and verified recovery snapshots; late checksum failures retain the old schema. The shared safe client recognizes `PURCHASE_LINE_VOIDED` without exposing arbitrary server messages/details. Verified TLS/cookie-jar HTTP tests exercise all five operations, exact original proofs, zero/void/reinstatement, fresh-login reconciliation, body-time revocation/source races and actual 51-entry default pages. Strict nested-reader tests and fuzzing cover required null/zero, duplicate/unknown members, Unicode and exact safe integers. A separately compiled actual server on disposable private loopback validates real replies against root schemas. Runtime/type tests and private actual-SDK/Go browser tests separately cover original contexts, malformed replies after real commits, explicit same-key reconciliation, current-state rereads and physical invariance; these are not physical-device tests or reviewed correction screens.

Still required: complete reviewed correction form/command/controller composition, responsive operating screens and any remaining retirement-date correction. Full sync/PWA, media, camera, worker/search, analytics, encrypted backup/restore, deployment and physical acceptance also remain separate complete-product gates. No automated suite is relabeled as testing these unimplemented operating workflows, physical devices, hosted CI or a production deployment.
