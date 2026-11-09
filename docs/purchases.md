# Purchases and Financial Allocations

**Availability:** all seven Go/HTTP purchase operations below are implemented and tested: authorized header detail/creation/audited descriptive correction, immutable unallocated/unbacked/compatible purchase-basis Asset allocation, complete global filtered headers, Item-associated headers and Purchase-owned independent Lines. The routes use the existing completed-session/Origin/CSRF/client-version/idempotency/admission boundary, strict input readers and real SQL authorization. Retained loaders, migrations 0013/0014, pinned finite ISO currency policy, signed live continuations and atomic publication remain authoritative. All seven operations have verified-TLS/cookie-jar coverage; compiled-server/socket replies are separately validated against executable contracts. All seven stateless purchase browser adapters are implemented with strict runtime/type validation, original whole Purchase/Item contexts, independent ETags and no automatic replay. Complete bracketed source reads, exact form models and volatile foreground commands are also implemented and tested. Reviewed responsive purchase operating screens use that state for all seven operations, with complete selected collections, exact raw input, explicit adoption/confirmation and original-key reconciliation. Compatible purchase-basis profiles now receive atomic acquisition updates through the same reviewed allocation operation; manual basis, currency mismatch, invalid authority and unsafe complete totals remain guarded. Protected allocation and Disposal correction/history HTTP services, stateless adapters, bracketed sources and pure raw form/proposal models are implemented separately; their reviewed commands/screens and receipt/media remain unfinished. Existing [catalog](catalog.md) and [physical Unit/Stock](holdings.md) workflows remain functional without purchase entry or an AI worker.

## Browser routes and usage

Complete administrator setup, mandatory password change and a fresh login first. Open **Purchases** in the main navigation, or **This item's purchases** from a catalog Item. Phone, tablet and desktop share all routes and capabilities below. These are browser routes, not `/api/v1` endpoints:

| Browser route | Purpose |
| --- | --- |
| `/purchases` | One actual global live page with every contracted server filter and explicit previous/first/next navigation |
| `/purchases/new` | Reviewed header creation without an invented amount, date or currency |
| `/purchases/PURCHASE_ID` | Exact header and complete independently versioned Line collection |
| `/purchases/PURCHASE_ID/edit` | Changed-only descriptive correction under the original complete source context |
| `/purchases/PURCHASE_ID/allocate` | Select one Item from the actual paged, fully filtered catalog |
| `/purchases/PURCHASE_ID/allocate/ITEM_ID` | Review the whole original Purchase/Item interpretation and append one explicit allocation |
| `/items/ITEM_ID/purchases` | Complete distinct associated headers bracketed by matching Item reads |

An imported Purchase ID equal to `new` (case-insensitively) uses `/purchases/ID/view` for detail, keeping the original identity. Imported Item IDs such as `new` or `tree` remain independent allocation targets. Only actual implemented paths and validated filter/cursor/optional Item context survive login, including mandatory password-change transitions. Draft money, counts, bodies, original keys and credentials never travel in route state or URLs.

### Create a header and allocate actual money

1. Choose **Add purchase**. Enter the exact merchant name (the multiline editor and exact-name filter retain valid line breaks), a full RFC3339 instant with an explicit offset, the explicit uppercase currency code and optional receipt note. For example, `2026-11-07T10:00:00.123456789+08:00` retains all nine fractional digits; the form is deliberately not a precision-losing `datetime-local` picker. No device timezone or current date is guessed. Review and confirm; this creates only the header.
2. Open **Add financial allocation** and select one actual catalog Item. Item discovery uses real server filters and signed pages, not a partial dropdown presented as the entire catalog. The allocation screen then completes the selected Item/individual-Unit source interval inside matching Purchase reads. Quantity Items do not cause a Stock query. From an Item-associated header, the selected Item context can go directly to this screen; current authority is still read again.
3. Explicitly choose **No named Unit** or an actual owned individual Unit. A quantity Item requires the no-Unit choice. A named Unit requires an explicitly entered base quantity of one. No option, quantity, zero payment or existing Unit is silently selected on the user's behalf.
4. Enter the positive whole `quantity_base` in the displayed original base unit and the whole nonnegative `paid_minor` in the header currency. The money field and review are labeled **integer minor units for the entire allocation**, not a per-unit price. Enter `0` explicitly for zero-paid history. No money is multiplied by count, no display-unit conversion/FX occurs, and the UI does not guess historical exponents with `Intl.NumberFormat` currency defaults. Retained unknown uppercase codes remain visible as exact raw minor units; new-money eligibility is checked by the server's pinned policy.
5. Review the exact amount/count, explicit Unit/null choice, complete original measurement and separate Purchase/Item revisions, then confirm. Selected Unit metadata is context, not another Unit `If-Match`. The returned Line has its own independent revision. Success invalidates reads and opens current detail; it never installs a frozen reply as current source, receives Stock, creates/moves a Unit or opts into assets.

Detail completes every owned Line page before showing its count; Item-associated lists complete every distinct-header page. Global discovery shows only the current page count, not a fictional transaction total. Lines retain exact independent IDs/revisions, raw quantities, header-currency minor amounts, literal notes and safe Item/Unit links. Different base units are not totaled, and a header has no second monetary total. A missing owner is not an empty collection. Posted Lines have no edit/delete controls.

### Correction, conflicts and interruption

Choose **Correct purchase** for merchant, exact instant, currency or receipt-note changes. Unchanged fields are omitted, an unchanged form stays local, and an explicit empty note clears it. Equivalent instant spelling is sent as explicit intent and the server retains its original spelling for a no-op. Complete Line context locks currency after any allocation, including zero-paid history; another label is not a substitute for conversion or financial adjustment.

New editors wait for their first current read rather than adopting stale cached source. Later refetches keep raw drafts. A changed header/Item/selected Unit blocks a new confirmation until the user compares current data and explicitly keeps edited fields or discards the draft. Changed currency/tracking/measurement requires an explicit whole-interpretation acknowledgement first; raw money/count text is retained without conversion, including invalid values. A removed Unit remains visibly unavailable until explicitly reselected or changed to no Unit, never silently null. Every new command still receives authoritative server validation.

Offline, initialized inputs stay volatile in the mounted page, but new writes are disabled and reconnect never submits. Back/link/reload guards protect dirty forms; review focus starts on visible static content and Escape cancels/closes rather than confirming. Long light/dark reviews use one keyboard-accessible outer scroller and wrapping action labels at 200% text. Source-read time is explicitly labeled as device time, not a frozen financial watermark.

If a sent result is uncertain, fields lock and **Reopen original request** retains the original exact key/body/proofs. Read-only reconciliation may show newer data or fail; it never changes that command. **Explicitly retry original request** remains available even after current source disappears and returns only the original result without another allocation. Authentication loss clears volatile financial context and returns through only the validated route/filter scope. Asset basis/currency/unfinished-producer guards remain real conflicts with static safe explanations, not fake success or implicit physical operations.

## Header, allocation and physical receipt

A **Purchase** records one exact merchant name, purchase instant, currency and receipt note. It has no second monetary total. A **PurchaseLine** is an independently identified allocation to one Item, optionally one of that Item's individual Units. Its `paid_minor` is the entire amount allocated to the line, including allocated tax/shipping exactly once. It is not a unit price and is never multiplied by `quantity_base` when reporting spend.

For example, a JPY purchase split between two Units can have two lines of `60000` minor units each. Historical spend is `120000`, not a header total plus those lines, and not another multiplication by a purchase count. A quantity purchase can allocate `120000` to `1500` base grams without creating a floating per-gram price. JPY uses zero decimal places; other currencies require their own minor-unit display policy. No implicit FX or cross-currency scalar is permitted.

A zero-paid allocation is still real financial history. It prevents naive Item measurement changes or deletion of referenced records just as a paid allocation does. Header creation or allocation does **not** receive stock, create/move a Unit, establish a location, install media or enable an asset. Separate explicitly reviewed commands perform those operations; an eventual quick-create workflow must coordinate them atomically rather than replay independent writes.

## Operations and original proofs

All paths are relative to `/api/v1` and require a completed active administrator session. All responses are `Cache-Control: no-store`.

| Operation | Original concurrency proof | Successful result |
| --- | --- | --- |
| `GET /purchases` | None; scoped live continuation | Complete `PurchaseList` page |
| `POST /purchases` | No `If-Match` | New `Purchase`, revision one and its ETag |
| `GET /purchases/{id}` | None | Actual `Purchase` and its own ETag |
| `PATCH /purchases/{id}` | Original Purchase aggregate revision | Frozen corrected `Purchase` and its ETag |
| `GET /items/{id}/purchases` | Actual Item existence/counter, not a write proof | Complete distinct associated `PurchaseList` page |
| `GET /purchases/{id}/lines` | Actual Purchase existence/counter, not a write proof | Complete independent `PurchaseLineList` page |
| `POST /purchases/{id}/lines` | Original **Purchase** aggregate revision plus required original `item_revision` in the body | New independent `PurchaseLine`, revision one and its own ETag |

Line creation uses the Purchase revision as `If-Match`, not an Item/Unit/Line ETag. It also requires the separately reviewed Item revision in `item_revision`; otherwise a concurrent first-allocation measurement/tracking change could reinterpret the same base integer while the Purchase proof stayed unchanged. Either stale proof fails rather than rebasing. The command advances both real Purchase and Item aggregates, but not unchanged physical Unit or existing immutable Line counters. The returned Line counter is independent, not Purchase-proof-plus-one; a caller wanting current resources must read them again. Lists have no ETag and never claim frozen-snapshot isolation.

Protected writes require exact same-origin `Origin`, CSRF, supported application version and an original idempotency key. Proofs are canonical strong quoted positive safe integers. Strict body readers reject duplicate/unknown/case-mismatched fields, invalid UTF-8/surrogates and lossy numeric coercion. JSON writes have a 128 KiB limit and no query. Lists have a 16 KiB raw-query budget and no body; detail has neither body nor query. All seven routes use the existing [transport and authentication](http-authentication.md) boundary, then recheck authorization in SQL; a session revoked during body reading cannot commit a purchase command.

### Calling the implemented APIs

Complete setup, mandatory password change and a fresh login first. Keep the cookie private and obtain a CSRF token for the current session. A same-origin API caller supplies `Content-Type: application/json`, the exact configured `Origin`, `X-CSRF-Token`, a supported `X-HomeBox-Client-Version` and a caller-owned `Idempotency-Key` on each write. Do not put credentials or financial commands in URLs, logs or persistent replay queues. The stateless browser SDK and reviewed operating screens are available; callers outside those screens must preserve the same original-context protocol.

For example, `POST /purchases` accepts this complete header without an `If-Match`, amount or implicit stock receipt:

```json
{
  "merchant_name": "Example merchant",
  "purchased_at": "2026-11-06T10:00:00+08:00",
  "currency": "JPY",
  "receipt_note": "Allocated tax and shipping are included in each line."
}
```

Before allocation, review the actual Purchase and Item, including its tracking/measurement tuple. If their original revisions are respectively 12 and 7, `POST /purchases/{id}/lines` uses `If-Match: "12"` and this body for a quantity Item measured in base grams:

```json
{
  "item_id": "the-reviewed-item-id",
  "item_revision": 7,
  "item_unit_id": null,
  "quantity_base": 1500,
  "paid_minor": 120000,
  "note": "The entire JPY allocation, not a per-gram price."
}
```

Substitute real reviewed IDs/revisions; do not copy sample proofs as current authority. A named Unit instead requires its actual owned ID and quantity one. The returned Line ETag is its own revision, not a new Purchase or Item proof. Read those aggregates again before a different command. For descriptive correction, send only changed allowed header fields with the original Purchase If-Match; `{"receipt_note":""}` explicitly clears the note.

Read all pages of the selected collection until `next_cursor` is null. Keep every original filter/owner/page size with its continuation; never reuse global, Item-associated or Line cursors across scopes. Responses do not set a session cookie, and list responses do not supply ETags.

| Safe conflict code | Meaning and recovery |
| --- | --- |
| `REVISION_CONFLICT` | The reviewed Purchase or Item changed. Refresh and explicitly review; never silently rebase. |
| `PURCHASE_CURRENCY_LOCKED` | An actual currency change is forbidden after any allocation, even zero-paid history. |
| `PURCHASE_CURRENCY_CONFLICT` | New money needs a supported finite-minor-unit code compatible with the financial source; never relabel or convert an existing amount to bypass it. |
| `ASSET_COST_BASIS_CONFLICT` | A manual asset acquisition basis cannot also receive a purchase allocation. |
| `ASSET_TOTAL_OVERFLOW` | The complete proposed Asset totals exceed the exact safe range. Nothing from this command was allocated; review source without automatic splitting, conversion or replay. |

These are static codes, not permission to render arbitrary server messages/details. An uncertain result requires explicit original-key/body/proof reconciliation; later source changes do not authorize a second write. There is no Purchase DELETE or posted-Line PATCH/DELETE API. A reviewed browser workflow for financial adjustment/void remains unfinished.

## Exact values and explicit allocation

- IDs contain 1–128 Unicode code points; retain exact identity, including imported `new`/`tree`, rather than trimming or substituting another record. Each resource has its own revision in `1..9007199254740991`.
- Merchant names contain 1–300 code points. Receipt/line notes contain 0–10,000 code points and preserve supplied whitespace, normalization and line breaks. Empty text is distinct from omission; text is not nullable. Omitted creation notes default to empty on the server, not by modifying outgoing client commands.
- Purchase instants are actual RFC3339 timestamps with uppercase `T`/`Z`, years 0001–9999, valid calendar/time/offset fields and at most nine fractional digits. Reject date-only/guessed instants, comma fractions, leap seconds, invalid offsets and precision that would be truncated. Normalize accepted timestamps to UTC without losing nanosecond precision; reject a canonical UTC result outside years 0001–9999 rather than overflowing an offset-boundary date. HTTP format validation alone is not runtime date-parsing evidence.
- Currency is explicit three-uppercase-ASCII-letter code text, owned by the header rather than a Line override. The actual Go writers additionally require a pinned finite ISO minor-unit definition for new money; spelling alone does not certify registry/exponent/financial compatibility. Retained history stays readable under representation validation. See the policy below; no amount is relabeled or converted.
- `quantity_base` is an exact positive safe JSON integer in the Item's existing base measurement; `paid_minor` is a separate exact nonnegative safe JSON integer in the Purchase currency. Do not use `quantity_minor`, display floats, decimal strings, an inferred price, hidden scale conversion or a mixed total. Booleans and fractional/unsafe numbers are invalid. Mathematically exact integral JSON exponent/decimal spellings remain numbers; the server must parse them without float rounding.
- `item_unit_id` is required in allocation input, including explicit null. Null means no named individual Unit allocation, not an instruction to create one. A supplied Unit must actually belong to the same individual Item and `quantity_base` must be exactly one. Quantity-tracked Items require null. An unallocated individual bulk count remains explicit and does not silently charge each existing Unit.
- Actual retained counters/source, tracking, ownership and asset currency/manual-basis compatibility must be checked transactionally. Schema/type acceptance is not proof of financial eligibility. A manual-basis asset cannot silently gain a second purchase acquisition basis; incompatible currency cannot be repaired by relabeling amounts.

## New-money currency policy and asset integration

`server/internal/currency` pins [SIX's official ISO 4217 List One](https://www.six-group.com/dam/download/financial-information/data-center/iso-currrency/lists/list-one.xml), publication **2026-09-17**, with source SHA-256 `33139b438657d1cee116ba737807ea71d19d6de4b90f799a09c56f0cc6a1b0ff`. It recognizes exactly 165 current codes with finite minor-unit exponents: 17 zero-place, 139 two-place, seven three-place and two four-place codes. No dependency, runtime network lookup, FX or floating decimal conversion is added. Registry changes require a reviewed source/fixture/test update.

New headers, actual unallocated-header currency changes and new allocations require that policy, even for zero-paid lines. Codes marked `N.A.` (including metals/test/accounting codes), retired codes and unknown/lowercase spellings do not receive a guessed exponent. Retained bounded uppercase codes still load exactly and permit descriptive corrections that do not change currency. The official historical list does not supply exponents; this implementation does not pretend to cover historical display conversion or reprice retained amounts. The policy runs inside the mutation callback after original-reply lookup, so current eligibility cannot turn an explicit original-key reconciliation into a second financial command.

The implemented Go service accepts allocations with no named Unit, an actually owned unbacked Unit, or an authoritative matching purchase-basis Asset. A backed allocation reads the full actual profile and retained Asset/Unit/Item counters, rejects manual basis or currency mismatch, inserts a real immutable Line and validates complete proposed exact-Unit acquisition/cost/usage/disposal. It then advances Asset authority/current/outbox and the existing Purchase/Line/coalesced Item/approved search scopes in the same transaction. Source/context is never reconstructed from current/FTS caches, a truncated collection or an old derived summary. A proposed safe net can be accepted even if the previous derived net was unsafe; every final published integer must still be safe.

The original Item proof also freezes the financial interpretation: profile/usage/cost/lifecycle producers already advance that aggregate. No new Unit/Asset request proof, endpoint or migration is added. Zero-paid allocations are real new history and advance Asset authority even when acquisition totals stay equal; only original-key reconciliation deduplicates. Unallocated or sibling spend never changes another Asset. Final Line/profile fields and independent counters must match the intended source, and required writes must affect actual rows. Any source/search/publication/audit/authorization/COMMIT failure withholds the whole result.

This integration never silently opts in, changes basis/currency, repairs a profile, guesses service dates or mutates usage/cost/disposal/physical Unit source. New money still requires the finite matching currency policy, including zero. [Assets, Usage and Cost Accounting](assets.md) documents the separate eleven implemented financial HTTP operations, stateless runtime adapters, complete foreground models and reviewed profile/history/lifecycle/calculation screens. Reviewed browser workflows for audited allocation/disposal adjustment/void remain unfinished.

## Descriptive correction and immutable history

`PurchasePatch` is a nonempty partial descriptive correction. Only merchant name, purchase instant, currency and receipt note can be supplied. Omitted fields remain unchanged; a supplied empty receipt note clears it. Currency cannot change after **any** allocation exists, including when every amount is zero. A value-equal correction retains an audited no-op receipt without inventing a new resource revision. An equivalent instant with another offset/fraction spelling keeps the exact retained date text; creation and actual instant changes use canonical UTC nanosecond text.

Migration 0014 adds private immutable correction context containing the exact original Purchase, resulting Purchase and supplied partial intent. Its event ID has a deferred FK to the real audit inserted by that same transaction, and its owner references the retained header. It is not a raw caller key, new offline entity or extra response field. No-op correction intent is retained without inventing source/projection changes; original-key reconciliation never appends another correction. Old headers receive no invented before-image, timestamp or audit association.

Creation, correction and allocation must use the existing private domain transaction boundary: business source, relevant aggregate/independent counters, current projections, frozen outbox rows, immutable audit and original idempotent response commit together. Publish final source once per identity. Do not return success before COMMIT, seed revision one over retained identity or let projection/FTS caches become authority. Purchase evidence changes must rebuild affected approved Item/Unit text from source, with Unit evidence scoped to its exact Item/Unit, and invalidate dependent analytics in the same transaction; raw/unreviewed OCR remains excluded.

Header source INSERT/UPDATE and immutable correction-context INSERT must each affect one actual row. The service compares the final created/corrected header fields and independent revision with the intended proposal, and rereads the exact private correction context before freezing success. An ignored/rewritten/missing write or reseeded counter cannot become a successful response; the whole source/search/projection/audit/receipt transaction rolls back. A value-equal correction still requires its actual immutable context, while equivalent instant spelling and original-key reconciliation retain their existing semantics.

Posted allocations are not edited/deleted through descriptive header correction. The root contracts now define a separately reviewed financial void/complete replacement plus immutable history, preserving the original posted Line and assigning a new identity to any replacement. [Audited Financial Corrections](financial-corrections.md) specifies the original Purchase/Line/old-Item/new-Item proofs, current tombstones and accounting-versus-physical boundaries. Migration 0017 and private Go correction/history services now implement those source/current effects, with independent original proofs, immutable complete context and proposed old/new Asset totals. Protected Purchase correction/history HTTP operations and stateless SDK adapters now expose those source/current effects; reviewed correction screens remain unfinished. HTTP tests or generated types alone do not establish the browser workflow or a negative physical stock workflow.

Domain keys bind administrator, dataset epoch, operation, exact typed command and original proof. An explicit identical-key retry returns the original committed result after later changes, without installing it as current state. Changed body/proof/operation conflicts. Expired claims must not be removed/reused to duplicate spend. Never queue financial writes for automatic reconnect submission, silently refresh proofs or rebase a proposal. Foreground review and authoritative validation are mandatory.

## Retained persistence and atomic publication

Migration 0013 adds independent Purchase/Line current materialization and bounded owner lookup indexes, preserving source values, retained counters, existing current bytes/timestamps, frozen changes and a certified root's dataset epoch. Missing counters start at one only for actual existing source; counter-only identities are retained without resurrecting a record. Invalid source/UTF-8/real owner authority/unsafe counter rolls back the entire pending migration and checksum transaction. Explicit reference adoption retains its separate epoch-rotation policy and private recovery snapshot.

Private `loadPurchase`/`loadPurchaseLine` read actual source and real independent counters, not current/FTS caches. A Line also requires its real Purchase and Item, and a named Unit must be independently authoritative and belong to that individual Item. Preserve exact bounded retained values, including native controls, offset timestamps, zero amounts and historical bulk Unit counts; do not rewrite old data to today's new-input policy or infer asset eligibility from a schema. The shared currency-code check certifies spelling only, not an ISO registry/exponent or financial policy. Purchase instants reject lossy precision and UTC year overflow; other domains' legacy timestamp policy is not silently changed.

Posted Lines cannot be updated/deleted, and headers cannot be deleted. Existing headers remain descriptively correctable through the actual audited Go service, not a destructive financial-line PATCH. New nullable private Line `created_at`/`mutation_event_id` columns must be supplied together and share a deferred audit FK; one transaction can associate several independent allocations with one immutable audit event. Legacy rows keep both fields null, never a guessed date from their header. New backfill projection `updated_at` is materialization time, not a reconstructed purchase event. Private provenance does not alter the offline payload or become a raw caller key.

The existing domain boundary materializes Purchase/Line payloads from validated final source, coalescing repeated staging once per identity. Independent retained tombstone counters remain supported without a public delete command or erasing posted source. All staged Item/Unit approved search, aggregate/Line/current/frozen revisions, original responses and audit commit atomically; Unit purchase evidence remains scoped to its exact Item/Unit rather than a sibling. `Purchases.Get/Create/Patch/CreateLine` use completed active SQL authorization, not raw HTTP SQL. Header correction completes every actual owned Line/Item/Unit authority check before changing source; a corrupt late row withholds the whole result. Actual command tests are distinct from private publication-fixture tests and do not alone establish complete live-list, HTTP, browser, dedicated Asset-interface or full-sync behavior.

## Complete live reads and filter meaning

All three collections use stable ID order, default page size 50, maximum 100, bounded 2048-byte opaque continuations and an explicit required `next_cursor`, null at the end. Duplicate associated lines do not duplicate a Purchase header. Validate lookahead/owner/counters before exposing a complete page; a corrupt or failed prefix is not a successful list. Signed continuations bind the authenticated principal, dataset epoch, page size and actual collection/owner scope for 15 minutes; they are not authorization or frozen sync cursors.

The implemented `Purchases.Page`, `PageForItem` and `PageLines` reuse the existing private authorized transaction and metadata-cursor codec under independent purchase purposes. Global pages resolve normalized filters and the actual home timezone in that same transaction, then stream supported instants until a validated matching lookahead exists; filtering a raw first page would lose later matches. Header selection uses actual source and bound SQL criteria before scanning. Item-associated headers validate the real Item and all matching Line authority, without duplicating a header or inspecting unrelated owners. Line pages validate every returned/lookahead row's retained Purchase/Item/Unit authority; old bulk Unit counts, zero amounts and broad retained uppercase currency representation are not rewritten to new-money policy.

A nonempty same-Line predicate is shared between selection and complete matching-authority validation; an invalid later matching Line cannot hide behind an earlier valid one. No Item criterion means no hidden Line/holding scan, so unallocated headers remain readable. Complete Item-associated and Line collections have no date filters and do not acquire a home-timezone-setting dependency. Global malformed/unknown timezone settings fail rather than silently switching to UTC. Existing owner/stable-ID indexes are checked through actual query plans; no extra schema/backfill is needed.

These are live continuations: actual header correction, new allocation and current physical placement changes can affect later reads. Neither an owner aggregate revision nor a data-revision watermark is frozen into the cursor. A fresh same-principal login may continue the original valid scope, but a revoked/mandatory/old-epoch session cannot use a cursor as authorization. Reads retain source/counters/current/frozen/audit/FTS and private correction history without repair or publication; normal session activity maintenance remains inside final authorization/COMMIT. Signing, source, lookahead, cancellation, late-session and final-COMMIT failures withhold both data and continuation.

The global purchase collection retains all documented filters: `from`, `to`, `currency`, `category_id`, `location_id`, `scope`, `status`, `merchant`, `limit` and `cursor`. Apply them together before slicing:

- Dates/currency/exact stored merchant name match the Purchase header. Dates are inclusive civil dates of the purchase instant in configured home timezone, UTC only when unset. Validate `from <= to`; use actual local civil dates across DST/repeated/skipped dates rather than fixed 24-hour windows or a guessed midnight. Bind the resolved timezone and every effective normalized filter into the cursor scope.
- Category, Item active/archived metadata status and location must match the **same associated line**, not different qualifying Items in the same purchase. Scope is direct/subtree and defaults to subtree; it does not duplicate headers.
- A named Unit allocation uses that actual nondisposed Unit's current location, not a sibling Unit elsewhere. An unallocated line uses its Item's current positive quantity balances or nondisposed Units. A financial allocation alone does not prove current placement; retained zero balances/disposed Units do not establish it. This is current placement filtering, not historical purchase-location reconstruction.
- Unallocated headers remain visible when no Item-dependent criterion is requested. An existing Item with no allocations has an empty associated page. Missing owners are not invented. Item-associated and Purchase-owned line routes accept only their owner path and paging fields, not silently ignored global filters.

## Stateless browser SDK

`web/src/lib/api/purchases.ts` exposes the same seven operations. The responsive screens compose this stateless boundary; it is not a persistent draft queue:

| Adapter | Caller-owned context |
| --- | --- |
| `fetchPurchase(id, signal)` | Exact resource identity and cancellation |
| `fetchPurchasesPage(query, signal)` | Complete global filter/cursor scope |
| `fetchItemPurchasesPage(itemID, query, signal)` | Exact Item owner and paging only |
| `fetchPurchaseLinesPage(purchaseID, query, signal)` | Exact Purchase owner and paging only |
| `createPurchase(body, context)` | CSRF, original key and signal; no unrelated revision |
| `patchPurchase(id, patch, context)` | The same write context plus the whole original `purchase` |
| `createPurchaseLine(id, body, context)` | The same write context plus whole original `purchase` and `item` |

Read and explicitly review the relevant resources before confirmation. Keep those snapshots and the original key/body together; do not substitute independently refreshed loose revisions. For example, a confirmed quantity allocation can call:

```ts
await createPurchaseLine(reviewedPurchase.id, {
  item_id: reviewedItem.id,
  item_revision: reviewedItem.revision,
  item_unit_id: null,
  quantity_base: 1500,
  paid_minor: 120000,
}, { ...originalWriteContext, purchase: reviewedPurchase, item: reviewedItem })
```

The write context owns the current in-memory CSRF token, original idempotency key and AbortSignal. The adapter verifies path/body/snapshot identity, Item revision/tracking/measurement representation, explicit nullable/single-Unit intent, exact safe counts/amounts, partial text and strict instants before sending one request. It never inserts outgoing note defaults, converts display units/currency, fetches new proofs, automatically paginates, stores commands/tokens, retries or submits on reconnect. Reads have eight-second deadlines and writes thirty seconds. Abort/timeout or an invalid reply after sending is uncertain, not proof of rollback.

Responses require complete exact resource fields and their own strong ETags. Creation/allocation require revision one; header correction permits only the original or next revision and verifies every expected field, preserving omitted source. New/actually changed instants use canonical UTC nanoseconds; an equal instant retains exact original spelling. Date conversion touches whole seconds only, retaining all nine fractional digits separately, so even a one-nanosecond alteration within the same JavaScript millisecond is rejected. Complete pages require distinct identities, bounded rows and required null continuations; owned Lines must retain their actual Purchase. Lists reject ETags, incomplete/repeated continuations and uncontracted filters. Query validation reuses catalog civil/filter policy without `q` and the installed client's exact percent-encoding for the 16 KiB budget, rejecting malformed Unicode before encoding.

Retained responses still accept bounded historical uppercase currency and Unit-associated bulk allocations; new-input discrimination never repairs history. Registry, actual Unit ownership, asset basis/currency, financial references and same-Line/current-placement/home-timezone selection remain server-authoritative. A frozen original reply is only reconciliation evidence, never permission to overwrite current query state.

The shared typed client preserves literal-null request fields using the installed helper's public type-only write marker: helper 0.1.0 otherwise treats `NonNullable<null>` as `never` and wrongly removes a null-only `oneOf` branch. This compatibility mapping affects request typing only, not response types, generated contracts or wire JSON. Strict fixtures accept real null, still require its presence and original proofs, reject named-Unit bulk counts, and reject actual marker objects. No dependency, generated-type edit, cast or runtime wrapper bypasses validation.

## Foreground review state

The source/form/command modules under `web/src/features/purchases/` drive the real responsive operating screens. They are volatile foreground state, not a persistent offline draft implementation.

- `readPurchaseDetail` completes the selected Purchase's actual independent Lines between matching full header reads. Any source/revision/ownership/page failure withholds the whole result. The complete Line set establishes whether currency is locked, including zero-paid history; it never becomes a mixed-unit count or a second monetary total.
- `readItemPurchases` completes distinct associated headers between matching full Item reads without loading every header's Lines or unrelated Items. These are live discovery resources, not a frozen dataset or a replacement for a fresh bracketed detail before correction.
- `readPurchaseAllocation` contains the full Item/individual-Unit selection interval inside matching Purchase reads. Individual selection completes the actual owned Unit pages; quantity selection does not query Stock or fabricate empty Unit/Stock authority. It validates Item interpretation and every returned Unit owner without inferring asset eligibility from Unit status or visible metadata.

Header defaults guess neither a purchase instant nor currency. They preserve exact retained RFC3339 text, whitespace and empty receipt intent. Creation validates the full header; correction emits only changed fields, with no command for an unchanged form. An alternative equivalent-instant spelling remains explicit intent for the server's audited no-op handling, not a local conversion. Manual header adoption changes only untouched fields and retains larger/invalid local text for review.

Allocation defaults leave nullable choice, count and money blank. Even no named Unit and zero-paid history are explicit decisions. `purchaseLineProposal` parses original canonical decimal count/minor-unit text through the shared safe-count parser, requires the actual selected Unit when named, retains whole Purchase/Item/Unit snapshots and never receives Stock or multiplies money. A quantity Item still requires an explicit no-Unit decision; individual unallocated bulk counts remain explicit. `adoptPurchaseAllocation` replaces currency and the entire Item tracking/measurement interpretation as one reviewed source context while retaining all raw draft fields. A disappeared Unit stays an unavailable choice, not silently null; invalid/larger numbers and notes remain unconverted and cannot be submitted until corrected.

`freezePurchaseCommand` validates and copies one create-header, descriptive-correction or allocation proposal with a fresh original key. Correction snapshots retain the complete original Line context for currency locks; allocation retains the actual selected Unit but does not invent a Unit precondition. CSRF/signals are supplied only when sending, not saved into the proposal. `sendPurchaseCommand` uses those original snapshots only, even after newer source or an uncertain result; it never reads/adopts new proofs or substitutes another key. Actual financial ownership, finite currency policy, profiles and references remain authoritative in SQL.

The shared `usePurchaseMutation` controller requires foreground review/explicit single-flight confirmation, aborts on interruption and retains uncertain original context for explicit same-key reconciliation. Refetch, cache eviction, dismissal and reconnect cannot create another command or financial write; session loss clears volatile command context rather than forwarding bodies/amounts through login. A CSRF failure before sending is not a committed uncertain write. Confirmed/sent-failed purchase commands invalidate only purchase/catalog/holding/Asset read roots, never installing a frozen reply as current state. Related catalog/owned/taxonomy/location/holding changes invalidate purchase scopes in return because original Item interpretation or same-Line current-placement selection may have changed. Unrelated account/taxonomy/location data is not overwritten by a purchase result.

These modules have runtime/strict-type and private actual-Go browser evidence, including genuine source changes inside read intervals, explicit whole-interpretation adoption, all three writers and original-key reconciliation after a committed lost reply. The responsive routes above compose the same modules with complete collection views, native exact-text/count controls, explicit foreground reviews and no generated primitive changes. Existing physical/catalog screens stay functional; separate reviewed Asset screens use their own original financial/physical scopes. Reviewed allocation/disposal adjustment/void browser screens and media remain unfinished.

## Verification and remaining work

`tests/test_purchases_contracts.py` exercises bounded complete resources, exact/null/omitted input semantics, single-Unit versus bulk allocation, safe amount/count extrema, strict instants, original Purchase and Item proofs versus independent ETag, complete pages, preserved filters and unchanged offline payload envelopes. `web/tests/contract-types.ts` exercises generated required fields, the separately required Item proof, explicit nullable allocation/one-Unit discrimination, write headers and filter scopes with strict library checking. Generate declarations only through `npm --prefix web run api:generate` and verify drift through `make check-web`.

Go tests cover retained loaders/backfill/publication and the actual authorized Get/three command methods: exact maximum Unicode/amount/count/date values, no implicit physical/asset change, immutable before/after/no-op context, stale dual proofs, financial/tracking/currency guards, source versus poisoned caches, fresh-login original replies after later changes/asset attachment, expired claims, competing/identical-key concurrency, complete late-row authority and complete statement/FTS/counter/identity/late-auth/deferred-COMMIT rollback. The pinned currency test checks every official finite code/exponent and exhaustively rejects all other uppercase triples. Migration tests independently verify preserved source/frozen bytes/epoch, no invented legacy context, restrictive/deferred references, immutable history, indexed owner lookup, late-ledger rollback and private recovery snapshots.

These checks also exercise complete retained collections above 200 rows at page sizes one/fifty/one hundred, maximum Unicode/control IDs and independent counters, actual command-fed source and live continuation after genuine correction/allocation. Same-Line category/status/placement fixtures distinguish named Units from siblings, current positive Stock from retained zero and disposed Units from readable financial history. Civil-date cases cover UTC/Taipei boundaries, short/long/repeated New York days, Samoa's skipped date and a nonexistent Brazilian midnight. Scope/size/filter/timezone/principal/epoch/signature failures, malformed settings/source/lookahead/related authority, indexed owner queries, late session changes and deferred COMMIT all withhold failed prefixes without read-side publication.

HTTP tests exercise all seven actual routes through the existing normal protected boundary and through verified TLS/cookie jars, including mandatory-change/revoked sessions, transport/Origin/CSRF/version/key/method/path/body/query limits and safe errors. Body-time callbacks genuinely change the Item measurement, Purchase currency/metadata or session before the authoritative transaction; stale dual proofs and late revocation cannot commit. Default-50 tests finish all three actual collections above one page without disabling admission, and explicit original keys return frozen replies after later changes/fresh login rather than creating another allocation. The three real JSON readers have bounded media/chunked/read-failure/exact-integer/nullable/UTF fuzz coverage; the shared browser boundary recognizes only static documented conflict codes.

Compiled-server/socket conformance additionally drives genuine setup/login/password rotation, all seven purchase operations, complete selected pages and original-key reconciliation on a disposable private database, validating 39 actual responses against the root executable schemas. It checks independent resource ETags, no list ETags/domain cookies, exact whole-allocation amounts and no implicit Stock/Unit mutation, then performs graceful shutdown and the actual CLI integrity check. This is native loopback HTTP evidence, not browser SDK or physical NAS acceptance; the separate TLS tests verify certificates rather than disabling verification.

```sh
go -C server test ./internal/httpapi -run '^TestPurchase' -count=1
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzPurchasesJSON -fuzztime=3s -parallel=2
make check
make test-go-pure
```

Private actual-SDK/Go browser workflows exercise every adapter, complete signed scopes, genuine first-allocation Item/Purchase changes, whole-amount allocation, no implicit physical mutation and original replies after a fresh login. Adverse-reply tests let real commands commit before corrupting owner/amount/independent revision/nanosecond/currency or losing a response; only an explicit identical original command reconciles, without another allocation/header or historical-state installation. Runtime tests additionally cover exact Unicode/null/partial/safe-integer/date/query extrema, all seven deadlines, cancellation, no storage/reconnect submission and safe conflicts. Strict compile fixtures preserve both generated request constraints and whole-context SDK requirements.

```sh
npm --prefix web test -- --coverage.enabled=false src/lib/api/purchases.test.ts
npm --prefix web test -- --coverage.enabled=false src/features/purchases
npm --prefix web run test:e2e -- tests/e2e/purchases-api.spec.ts
npm --prefix web run test:e2e -- tests/e2e/purchases.spec.ts
make check-web
```

The real operating-screen suite separately drives header creation/descriptive correction, fully filtered explicit pages, actual Item/Unit selection, whole-allocation money, zero-paid locks, genuine first-allocation proof conflicts, explicit complete-interpretation adoption and committed malformed-reply reconciliation after later source changes. It checks dirty/Back/Escape/focus, offline no-replay, safe real-login scope returns and enlarged light/dark wrapping/accessibility. Retained 205-header/Line collections, imported `new` identity, manual/currency/range guards and real zero/positive compatible-Asset allocation use a private offline Node SQLite baseline with real source/counters/current payloads, closed before the real Go CLI validates/provisions/serves it. No fictional HTTP receipts, reset endpoints or weaker production admission are introduced; Node remains development-only.

These purchase checks do not substitute for the separately implemented [Asset operating-screen checks](assets.md). Reviewed allocation/disposal adjustment/void browser workflows, receipts/media, the complete offline replica, physical phones and production NAS acceptance remain separate implementation/testing gates, not dropped product scope. Existing offline `purchase`/`purchase_line` payloads and all original operation identities are preserved; strict HTTP boundaries and real Go commands/collections do not enable the complete 22-entity sync protocol.
