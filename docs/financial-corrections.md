# Audited Financial Corrections

**Availability: private Purchase and Disposal correction persistence/services and executable contracts.** Migrations 0017/0018 and the private `Purchases.CorrectLine/PageLineCorrections` and `Assets.GetDisposalRecord/CorrectDisposal/PageDisposalCorrections` methods implement their financial decisions and authenticated history inside the Go store boundary. They are exercised directly, not exposed through HTTP or browser controls. All five operations below have contracts/generated transport types, but their HTTP handlers, stateless browser adapters and reviewed operating screens remain unfinished. Do not call these paths as working features. The existing [seven purchase operations](purchases.md) and [eleven Asset financial operations](assets.md) remain available; their browser/socket tests do not establish the new correction workflows.

This closes an interface gap in the read-only source requirements without weakening posted-history immutability. The root OpenAPI now contains 142 operations and 177 schemas, preserving all original 103 operation identities and every existing schema, including all version-one offline payloads. Specification revision v0.9.3 is not an application release.

## Financial decisions are not physical inventory

- **Allocation correction:** explicitly void one active posted attribution and optionally post a complete replacement with a new independent Line identity in the same transaction. Never edit the original Line's amount, quantity or owner in place.
- **Financial void:** remove a recorded decision's current accounting effect while retaining the original decision, actual references and immutable correction history. A legitimate zero-valued allocation or disposal is not a void.
- **Disposal financial correction:** replace, void or explicitly reinstate recorded disposal finance. It never disposes of the object again, restores inventory, revives a Unit, moves it or infers retirement/service dates.
- **Cost adjustment:** an expense/credit against an actual extra-cost event. It is not a substitute for correcting acquisition attribution or realized disposal proceeds.

A physical return or movement is a separately reviewed inventory command. Neither financial correction automatically creates stock, decrements a quantity, changes a Unit's operational status or converts between currencies. Retirement-date correction remains separate unfinished work; these operations do not clear or overwrite a recorded retirement.

## Contracted operations

Paths are relative to `/api/v1`. All five require an active completed administrator session and `Cache-Control: no-store`. The two writes additionally require the exact Origin, CSRF token, compatible application version, original idempotency key and original subject proofs. Request JSON is at most 128 KiB, with no duplicate/unknown fields, invalid Unicode, lossy numeric parsing or query parameters. Authorization and source validation must be repeated inside SQL after body reading.

| Method | Path | Original proof or read scope | Response |
| --- | --- | --- | --- |
| GET | `/purchases/{id}/line-corrections` | Actual Purchase; independent owned live page | `PurchaseLineCorrectionList`, no ETag |
| POST | `/purchases/{id}/line-corrections` | Purchase If-Match, original Line and original owning Item body revisions; replacement's own Item proof | `PurchaseLineCorrection`, independent entry ETag |
| GET | `/units/{id}/disposal-record` | Actual retained original disposal, including a financially voided one | `DisposalRecord`, actual independent Disposal ETag |
| GET | `/units/{id}/disposal-corrections` | Actual original disposal; independent owned live page | `DisposalCorrectionList`, no ETag |
| POST | `/units/{id}/disposal-corrections` | DisposalRecord If-Match plus original Asset body revision | `DisposalCorrection`, independent entry ETag |

Correction-entry revision one belongs to an actual newly stored immutable entry. It is never the Purchase, Line, Asset or Disposal proof. A history page, summary `source_revision`, coincident counter or failed read cannot provide a subject proof. Do not compute a new proof by adding one to a reply; read and review the actual subject resource for a different command.

## Purchase allocation correction

The request freezes three distinct existing scopes:

1. The original complete Purchase supplies its `If-Match`.
2. `line_id` plus `line_revision` identify the actual active original Line and its independent revision.
3. `item_revision` binds that original Line's owning Item interpretation.

For replacement, the nested complete `PurchaseLineWrite` additionally supplies the new target Item ID/revision and explicit nullable Unit decision. The old and new Item may differ; neither proof is silently borrowed, fetched or rebased. A named replacement Unit belongs to that target Item and has quantity one. A null Unit supports an explicitly unallocated attribution. `paid_minor` is the whole new allocation, not a per-unit amount, adjustment delta or multiplier.

Illustrative **contract-only** void body, with the separately reviewed Purchase If-Match:

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

Illustrative **contract-only** replacement body:

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

These are private service capabilities, not a CLI financial-write interface or authorization to edit SQLite manually. The five new HTTP handlers, transport parsing and reviewed browser workflows still need separate implementation and evidence.

## Retained disposal record and financial correction

The existing current-disposal GET remains a current effective resource, not a substitute for the retained record. A financial void makes that current resource absent and publishes its independent tombstone. The separate `DisposalRecord` remains readable with:

- `item_unit_id`: the actual subject Unit.
- `revision`: the actual independent Disposal subject revision, also in its ETag.
- `original`: the exact first posted bounded disposal data, without a guessed historical revision.
- `current`: the complete actual effective data, or explicit null after a financial void.

Original and current owners must match the subject. Null means financially voided only when the actual retained record says so; zero proceeds with a nonnull current decision is a different fact. Genuine absence of original source is 404, and a failed/current-disposal-404 read is never a proof for reinstatement. Read actual source/correction history and real counters, never the current projection cache.

The correction request uses the original DisposalRecord `If-Match` and separately required original Asset `asset_revision`. It has no physical Unit or Item body proof. Whole Unit/Item data can remain review context, but their counters do not become additional financial write preconditions.

Illustrative **contract-only** void body:

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

These private capabilities are not a CLI financial-write interface and do not authorize direct database edits. The five new correction HTTP handlers, strict transport parsing and reviewed browser workflows remain separate required work.

## Exact input, history and foreground recovery

All supplied amounts/counts are exact safe integers. Replacement acquisition/proceeds are nonnegative; an allocated base count is positive. There is no negative physical count, grouped/decimal display parsing, multiplication by quantity, exponent guessing, automatic FX or rounding of unsafe values. Reasons are required nonempty bounded text, with exact original whitespace/Unicode retained. Financial instants preserve explicit offsets and up to nine fractional digits; changed instants normalize losslessly while equivalent retained spelling stays unchanged. New correction creation time is server-accepted context, never a backfilled legacy date.

Owned history pages default to 50 and cap at 100, use a 16-KiB query budget and 2048-byte signed continuations, and have required nullable `next_cursor`. Only `limit`/`cursor` are accepted. Independent purposes bind principal, epoch, actual owner, collection and size for 15 minutes. Validate every selected row and lookahead before returning any prefix; these are live pages, not frozen sync snapshots, financial totals or subject proofs.

A future browser workflow must complete and bracket only its selected source/history, retain whole original contexts and raw invalid input, show the actual financial before/after and require explicit foreground confirmation. Original body/key/proofs stay together. Unknown sent outcomes retain explicit identical-key reconciliation after later source changes or fresh login; there is no background queue, automatic retry/rebase/reconnect replay or installation of frozen replies as current Query data. Session loss purges private form/command state instead of forwarding money, bodies, proofs or credentials through login returns.

## Offline and verification boundaries

Current financial effects use the existing `purchase_line` and `disposal` upserts/tombstones, with all normative version-one payloads unchanged. A voided old Line cannot reappear from raw retained history during materialization; a corrected disposal publishes its actual current decision. The normative replica remains 22 kinds. Detailed authenticated correction/audit history is not a second offline authoritative ledger or a new projection kind. Old frozen replies/outbox bytes and retained original source remain exact. See [Offline Replica and Safe Draft Protocol](../contracts/sync.md).

Run the executable contract/type checks and private Purchase and Disposal transaction tests from the repository root:

```sh
uv run --frozen python -m unittest tests.test_financial_corrections_contracts -v
npm --prefix web run api:check
npm --prefix web run typecheck
npm --prefix web test -- --coverage.enabled=false src/lib/api/client.test.ts
go -C server test ./internal/store -run '^TestPurchaseLineCorrection' -count=1
go -C server test ./internal/store -run '^TestDisposalCorrection' -count=1
```

Negative schemas and strict actual-client type fixtures cover required proofs/null/zero, distinct original-versus-replacement intent, one named Unit, independent immutable entry revisions, bounded retained record/history, exact safe numbers/dates/reasons and unchanged offline shapes. Actual private Go Purchase tests separately cover void/replacement, immutable exact context, independent original proofs, complete 205-entry history/current-page gaps, current evidence/totals, zero versus void, old/new Asset effects, safe proposed versus unsafe old totals, original/fresh-login/expired-key behavior and SQL/ignored/rewritten/authorization/COMMIT rollback. Disposal tests exercise 105 actual linked decisions and complete pages, explicit zero/void/reinstatement, broad retained dates and money, source/counter/chain corruption, complete proposed totals, independent original proofs, replay/concurrency and statement/late-authorization/COMMIT rollback. Fresh/certified schema-16/17 upgrades retain exact old source/current/frozen bytes/epoch and verified recovery snapshots; late checksum failures retain the old schema. The shared safe client recognizes `PURCHASE_LINE_VOIDED` without exposing arbitrary server messages/details. Private services and schema checks do not establish the new HTTP/browser workflows.

Still required: all five correction HTTP handlers and strict parser/TLS/socket evidence, stateless adapters, complete reviewed source/form/command/controller composition, responsive actual-Go browser workflows and any remaining retirement-date correction. Full sync/PWA, media, camera, worker/search, analytics, encrypted backup/restore, deployment and physical acceptance also remain separate complete-product gates. No existing automated suite is relabeled as testing these unimplemented handlers, physical devices, hosted CI or a production deployment.
