# Catalog Item Rules

**Implementation status:** responsive reviewed catalog metadata list/detail/create/edit/archive/restore/delete screens, authoritative Go/SQLite transactions, complete filtered/signed live pages, six item HTTP operations and typed stateless adapters, and final-state item/unit approved-search publication are implemented and tested. Related barcode/alias/attribute/Tag maintenance is available through the [owned catalog screens](catalog-links.md), and separate reviewed [Unit/Stock operating routes](holdings.md) and [Item-associated purchases/allocation screens](purchases.md) are linked from item detail; photo-assisted creation, ranked search, bulk/import/export interfaces, settings writers and complete sync workflows remain in development. Core catalog CRUD requires no AI worker.

## Catalog metadata and holdings

An item is a stable catalog identity, not a physical unit or a stock balance. Its tracking mode is either `individual` or `quantity`, never both. Creating catalog metadata does not implicitly receive stock, create units, post a purchase, run OCR, or require the optional AI worker. Multiple items may share a commercial barcode; product identifiers are not globally unique.

New identities are UUIDv4. Supported imported identities are retained. Item IDs and nonnull category IDs contain 1–128 Unicode code points. Names contain 1–300; nullable brand/model values contain 0–300; descriptions and notes contain 0–10,000. Preserve exact whitespace, case, Unicode normalization, and null versus empty text rather than silently rewriting user data. The item response always contains all its declared fields, including nullable brand, model and category values.

The positive safe-integer revision, up to `9007199254740991`, is an item aggregate proof. Owned catalog/link and inventory mutations must advance it with their own independent projections. A reviewed deletion must not silently include a link or holding added after its original proof. Category assignments also invalidate the old/new ancestor classification aggregates; tag assignments invalidate affected tag membership aggregates.

## Integer measurement and explicit conversion

The measurement tuple is `unit_code`, `base_unit`, and `scale_factor`:

- `unit_code` is the display unit, containing 1–32 Unicode code points.
- `base_unit` is a 1–32-character ASCII identifier beginning with a letter; later characters may be letters, digits, underscore, slash or hyphen. Case is preserved. Whitespace, including a trailing newline, is not part of an identifier.
- `scale_factor` is a positive safe integer giving the number of base units in one display unit. For example, `kg/g/1000` represents one kilogram as 1,000 integer grams.

Stock uses integer `quantity_base`, never floating-point counts, `quantity_minor`, or an unlabeled total across different base units. Conversion is explicit item metadata, not an inferred unit-name dictionary. Neither the database nor a client may silently rescale existing quantities.

A minimal creation request is `{"name":"USB-C cable"}`. It defaults to individual tracking, active status, null brand/model/category, empty description/notes, and `pcs/pcs/1` measurement. When any supplied measurement differs from those defaults, supply the entire tuple. For example:

```json
{
  "name": "Coffee beans",
  "tracking_mode": "quantity",
  "unit_code": "kg",
  "base_unit": "g",
  "scale_factor": 1000
}
```

Sending only `unit_code: "kg"`, only `base_unit: "g"`, or only `scale_factor: 1000` is invalid. Explicit fields equal to their canonical defaults can be omitted independently without inventing a conversion.

A partial edit can change only specified measurement fields against its reviewed item revision, but any actual tracking-mode or measurement change is rejected once a unit, balance, purchase line, or inventory movement exists. Disposed units and zero balances still retain meaning/history. Repeating an unchanged field is not a migration. Changing an established interpretation requires a separate explicitly reviewed migration workflow, not a permissive metadata PATCH.

## Partial edits, archival, and deletion

Omitted fields in a nonempty patch are unchanged. Explicit `null` clears brand, model or category; an empty brand/model string is distinct from null. Other fields are not nullable. Internal presence wrappers such as `{"set":true,"value":null}` never appear on the wire.

Archival changes catalog status, not physical inventory. It never disposes units, consumes stock, erases movement/purchase history, or deletes shared media. Existing holdings remain readable. Use an explicit `status: "active"` patch to restore the catalog metadata. This differs from archiving a physical location, whose empty-live-holdings rules are documented separately.

Hard deletion is restricted to an item without any unit, stock-balance, purchase-line or inventory-movement references, including disposed units and zero balances. Archive referenced items instead. Deletion must remove only the item's owned catalog/link records, emit their independent tombstones, invalidate affected category/tag aggregates, and preserve shared media and other owners. Retained counters and immutable audit identities cannot be reused as a different item.

## Browser workflow

After completing the initial password rotation, open `/items`. Phone, tablet and desktop share the same metadata capabilities:

1. Read actual stable-ID pages and apply text, category/location scope, status and associated-purchase/date/currency/merchant filters. All criteria go to the server before page slicing. Advanced criteria have explicit labels and validation; no client-side first-page filtering or invented whole-catalog total is displayed. Choose 1–100 records per page, default 50, then explicitly navigate next/previous/first pages. A deep signed cursor without local previous-page context can return to the first page. Filters and visited cursor context survive normal detail/edit/return navigation; live expiry or inconsistency requires a fresh read.
2. Use `/items/new` to create catalog metadata, not to receive inventory. Individual tracking and pcs/pcs/1 are the visible defaults; explicit custom measurement remains available. Name is required. Brand/model have separate provided toggles so null and supplied-empty text remain distinct. Classification uses the complete checked category hierarchy, searchable native selection and full ancestry; an unavailable selection is retained visibly, never silently cleared.
3. Open a detail link or `/items/{id}/edit` to inspect/edit exact fields. Imported identities matching `new` use `/items/{id}/view` for detail, without changing the stored ID. Only changed fields are submitted. Current-source or selected-category changes do not rewrite a draft/proof: explicitly compare, keep edited logical fields or discard the draft, and recheck the selected category when required. Tracking/measurement adoption stays one interpretation. The server still rejects type/measurement changes with retained history.
4. Review the original resource identity/revision and before/after metadata, then confirm. Details provide separate archive, restore and delete actions; an unsaved editor draft is not silently included. Archive accepts an exact optional reason and never disposes holdings. Restore enables metadata only. Delete is server-guarded against all unit/balance/purchase/movement references and explains owned/shared-link retention without fabricating impact counts.
5. For an unknown sent result, keep the volatile original request and use read-only current-state reconciliation or explicit identical-key/body/proof retry. A genuinely committed deletion can disappear from reads without erasing that proposal. Neither reconnect nor a successful historical acknowledgement overwrites current state or creates a replacement command.

Loading, missing/invalid/partial data, offline coverage and session loss are explicit. Writes are disabled during unsafe reads; protected login returns accept only implemented same-application paths. Controls compose existing official primitives with visible focus, wrapping labels and enlarged-text reviews. Inline metadata error space is retained so blur validation does not move a mouse/touch target between down/up; generated primitives remain unchanged. Drafts and visited-page history are volatile, not a full offline replica or durable draft queue. Very deep cursor history remains bounded by browser history capacity; bidirectional server continuations would be the upgrade, not silently truncated records.

Catalog photos, camera/scanning, units/stock, purchases, ranked/visual/AI search, bulk actions and import/export are still separate product work. No empty decorative tabs, fake totals, automatic receiving, or worker-dependent metadata CRUD is substituted for them.

From item detail, choose real barcode/alias/attribute/Tag assignment links. The [Catalog Links and Tag Membership](catalog-links.md) guide describes the implemented responsive screens, all fourteen services/HTTP operations and typed stateless adapters, complete owner-scoped collection reads, independent row proofs, original-key reconciliation and complete-set Tag adoption. Normal item-list filters/cursor returns survive these routes as well.

## Authoritative transaction service

`store.NewCatalog(auth)` binds catalog operations to the existing private store; it cannot select another database or expose SQL to HTTP handlers. Current services are:

- `Get`: return validated current metadata and the actual retained aggregate revision. Missing/unsafe counters are corruption, not permission to fabricate revision one.
- `List`: internal, unfiltered stable-ID pages of 1–100 records using an internal `NextID`. Includes archived metadata, rejects corrupt rows/lookahead without a partial prefix, and releases no page/continuation before read/session COMMIT. It is **not** the public filtered/signed item list below.
- `Page`: complete live signed pages using every contracted filter, applied before choosing the returned page and lookahead. Resolve the home timezone and cursor scope in the same authorized transaction, and preserve principal/epoch/page-size/filter/settings binding without changing the existing location/category/tag token codec. No public nested read transaction or unsigned continuation is exposed.
- `Create`: validate optional defaults and explicit measurement, verify a referenced category, allocate an independent UUIDv4, invalidate its classification ancestors, and publish source-derived item/search data. A new identity must begin at revision one; a retained/tombstoned identity cannot be reused under a new meaning.
- `Patch`: preserve omitted fields and explicit nullable presence, validate the reviewed target revision, reject history-bearing type/measurement changes, and invalidate old/new classification ancestors once. Equal nullable values are no-ops even when supplied through a new pointer; they still receive immutable frozen acknowledgements.
- `Archive`: audited catalog-status change with an optional exact reason. Repeated archival is a value-equal no-op; restoration is an explicit partial status patch.
- `Delete`: verify the original proof and all physical/financial/history guards, collect owned stable IDs, and emit independent item/search/barcode/alias/attribute/media-link/review/item-tag tombstones with category/tag aggregate invalidation. FK cascades remove only owned source rows and associated jobs/derived item FTS, not shared media or another owner's links.

Nullable command presence is an internal `NullableString`/`NullableID` representation, never a wire wrapper. Item create/patch snapshot caller-owned optional pointers before validation and fingerprinting so their metadata, measurement tuple, classification, search publication and frozen reply reflect the same original request. Callers must synchronize shared input access during the call; copying does not make concurrent data races safe. Source, final item/unit approved FTS, current/frozen projections, counters, immutable mutation events and principal/epoch-scoped original responses share one transaction. The active completed session is checked again before COMMIT, including on replay. Statement, later dependent write, exhausted revision/sequence, late authorization, or deferred commit failure returns no successful DTO/acknowledgement and preserves the entire original state.

Deleting catalog metadata is not secure erasure. Immutable audit, retained counters, frozen replies/outbox rows, and shared media remain subject to their own retention rules. OCR tied to retained media stays private; deleting an owned job nulls its OCR job reference instead of copying raw text into publication. Complete history APIs and exact event-to-target/outbox associations are separate unfinished work. Owned barcode/alias/attribute writes, tag-set replacement and signed collection reads are implemented through fourteen HTTP operations and reviewed browser maintenance. Separate Unit/Stock and Purchase workflows are also available; media and other remaining multi-domain workflows are pending. The HTTP module binds catalog metadata services to the six routes below.

## Required HTTP boundary

All paths below are relative to `/api/v1`. These six operations are implemented through the real Go/SQLite services and the configured same-origin transport/session boundary. Typed browser adapters and the reviewed metadata screens above use these operations.

| Operation | Required behavior |
| --- | --- |
| `GET /items` | Live stable-ID pages, all supplied filters applied together, no body |
| `POST /items` | Required name, validated optional metadata/defaults, no unrelated If-Match |
| `GET /items/{id}` | Current metadata, including archived items; no body or query |
| `PATCH /items/{id}` | Nonempty partial metadata with the original item aggregate proof |
| `POST /items/{id}/archive` | Original item proof and a required JSON object with an optional reason of at most 10,000 code points |
| `DELETE /items/{id}` | Original item proof, no body/query, and the reference guards above |

Writes require the exact configured Origin, an active completed administrator session, CSRF, a supported client version, and an original idempotency key. JSON is unencoded UTF-8, bounded to 128 KiB before parsing, with exact field names, no duplicates/unknown fields, no invalid UTF-8/lone surrogate repair, and exact safe-integer parsing. Integral decimal/exponent spellings such as `1.0`/`1e3` remain valid without float rounding; a fractional/unsafe value never becomes a scale. No metadata operation sets a session cookie. Resource replies have a strong quoted revision ETag; live pages and deletion acknowledgements do not invent a resource ETag.

Use exact route paths, with no query on resource reads or writes. Lists accept each documented query name once and cap the raw, percent-encoded query at 16 KiB, enough for all field maxima together. GET/DELETE reject any body or content encoding, including chunked bodies. Unsupported methods return `405` with the route's `Allow`; unimplemented media/assisted-creation routes return structured errors, not fake successful data. Unit/Stock routes are documented separately in [Individual Units and Quantity Stock](holdings.md). The fourteen related barcode/alias/attribute/tag operations are documented in [Catalog Links and Tag Membership](catalog-links.md). Supported imported `new`/`tree` IDs are not reserved by this API.

Safe codes include `400 INVALID_CATALOG_URL`, `INVALID_QUERY`, `INVALID_CURSOR` or `INVALID_REVISION`; `409 PRECONDITION_REQUIRED`, `REVISION_CONFLICT`, `ITEM_HAS_HISTORY`, `IDEMPOTENCY_CONFLICT` or `IDEMPOTENCY_EXPIRED`; and the shared validation/authentication/transport/body/media/version/rate/availability codes. A history conflict never resizes or disposes existing quantities. Replies use allowlisted static guidance and request IDs, never SQL, private paths or submitted secrets. Clients localize known codes instead of rendering arbitrary server diagnostics.

Preserve the original key/body/proof when explicitly reconciling an uncertain result. Never automatically replay on reconnect, refresh a revision, or silently rebase a write. The authoritative transaction must commit source changes, final current/frozen projections, approved FTS, aggregate counters, immutable audit, and the frozen response together. No-op edits keep their resource revision but still receive a replayable acknowledgement. Historical replies invalidate reads rather than overwrite newer state.

## Typed browser adapters

`web/src/lib/api/catalog.ts` exposes `fetchItemsPage`, `fetchItem`, `createItem`, `patchItem`, `archiveItem`, and `deleteItem`. These are stateless transport adapters, not catalog screens, a local database, or a background command queue. The reviewed screens above supply foreground review and explicit confirmation before each write.

Creation accepts a name-only request or explicit optional metadata without inserting defaults into the outgoing body. The exported `ItemCreateInput` type and runtime validator both require the complete tuple for nondefault measurement, compensating for the type generator's unsupported JSON Schema `if`/`then`. Category selection does not add an unrelated parent proof to creation. Partial edits preserve omitted/null/empty distinctions and reject empty or internal-wrapper bodies. Restoration is an explicit status patch; archival never invents inventory disposal.

Every write requires caller-owned in-memory CSRF, the original idempotency key, and an AbortSignal; resource mutations also require the original positive safe revision. Reads have an eight-second deadline, writes thirty seconds. Resource replies require the declared fields, exact requested identity, strong matching ETag, and a creation revision of one or an original/no-op-or-next mutation revision. Deletion acknowledgements require a valid operation identity and exactly the original revision plus one. Reject unknown/private response fields, invalid Unicode, unsafe numbers and malformed measurement rather than repairing them.

Each list call returns one explicit page. Forward all filters without silently trimming the caller's query; the server owns effective search/date/timezone semantics and signed scope. Validate actual civil dates in years 1–9999, inclusive bound order, field/page limits, unique response identities, required nullable `next_cursor`, full nonterminal pages, nonrepeated continuations, and explicit status/direct-category consistency. A valid subtree result can belong to a descendant category. Generic imported `new`/`tree` IDs remain valid; unsafe path separators/dot segments cannot become routes.

The adapters never read cookies directly, persist credentials/commands, refresh CSRF, fetch a newer proof, retry, automatically paginate, or replay on reconnect. A canceled or malformed sent result remains uncertain: retain the reviewed original command outside the adapter for an explicit foreground retry or authoritative read. Historical frozen replies must not replace current query state.

## Reviewed metadata state

The metadata screens use the form/command helpers in `web/src/features/catalog`. They reuse the SDK's validator rather than a second set of metadata limits. Form defaults retain whether brand/model is unprovided (`null`) or explicitly provided but empty (`""`); inactive text never enters a proposal. The scale field accepts canonical positive decimal integer text only, rejecting whitespace, leading zeros, fractions, exponents and unsafe values before conversion.

Creation proposals contain explicit reviewed metadata, not holdings, purchases, jobs or an inferred quantity. Partial proposals include only actual changed fields, including explicit nulls, and never implicitly archive/restore metadata or receive stock. Explicit current-state adoption keeps changed logical fields and invalid in-progress text. Tracking mode and the entire measurement tuple are one interpretation during adoption: do not silently combine a concurrent base/scale with a locally changed display unit. A later server validation still enforces retained-history guards.

Review commands copy the original body, resource identity/revision and selected category metadata under one new volatile key. A selected category label must identify the requested category, but its revision is not an unrelated create/parent If-Match. Patch, archive, restore and delete dispatch only against the frozen item proof. Restoration sends only `status: "active"`; an uncertain retry retains the exact original command/key even when a current read no longer contains the deleted item.

The shared foreground controller now invalidates every declared dependent read scope after confirmation or a sent failure, without installing the frozen response. Catalog and taxonomy changes invalidate `catalog`, `taxonomy` and dependent `holdings`/`purchases`/`assets` reads because their source/projection proofs, reviewed Item interpretations and filter scopes depend on one another; unrelated location/auth reads are not broadly invalidated. A pre-send CSRF failure does not become an uncertain domain commit. Backgrounding, cancellation, session loss, duplicate exclusion and explicit retry retain the existing no-replay behavior. These helpers underpin the reviewed metadata pages, not a persistent draft queue.

## Live list filters

The item list retains all contracted filters: `from`, `to`, `currency`, `category_id`, `location_id`, `scope`, `status`, `merchant`, and `q`, plus `limit`/`cursor`. Do not silently discard a filter or truncate a complete result to its first page.

- Limit is 1–100, default 50. Every page explicitly returns `data` and nullable `next_cursor`; terminal pages use null.
- Omitted status includes active and archived metadata; an explicit status restricts it to `active` or `archived`.
- Scope is `direct` or `subtree`, default subtree, for category/location matching.
- Location matching uses current positive balances or nondisposed units, not historical visits or zero balances.
- Purchase date, currency, and merchant filters must match the same associated purchase. Merchant names match exact stored text, including case and whitespace. Multiple matching lines do not duplicate an item. Civil date bounds are inclusive in the configured home timezone; do not match different purchases independently to fabricate a qualifying combination.
- `q` is a trimmed, Unicode-lowercased literal substring, including single-character CJK queries; it is not SQL or FTS operator syntax. Stored text is never normalized or rewritten. Read only validated published search records, never raw/unreviewed OCR, private FTS cache rows, rejected candidates, or historical labels overwritten in source.

The private `app_settings` entry `preferences.timezone` stores a JSON IANA timezone string. An absent entry defaults to UTC, never the server/phone's local timezone. Null, malformed, unknown and `Local` values fail closed instead of silently changing date semantics. A settings HTTP/UI writer remains separate work. The Go executable embeds the standard IANA database so a NAS need not provide host zone files; this is portable code, not physical NAS acceptance.

Classify each RFC3339 purchase instant into its actual local calendar date. Do not approximate a day as 24 hours or convert civil bounds through a nonexistent local midnight: spring/fall DST, repeated hours, nanosecond boundary events and skipped dates such as Pacific/Apia 2011-12-30 must retain their meaning. Unknown legacy date-only/invalid timestamps fail the date-filtered read instead of acquiring a guessed instant/timezone or returning a valid-looking prefix.

If an approved search projection has not been materialized, derive its text read-only from the same manual/realized/approved source fields as publication. Do not copy legacy caches, schedule AI, or write catalog/search/projection/outbox/audit rows to service a read. Normal session activity still commits transactionally. Present but malformed/wrong-scope/duplicate/null/private-field search records fail closed; equivalent JSON payload formatting is not a different identity.

Signed continuations bind the administrator, dataset epoch, page size, every effective filter, home timezone, and a 15-minute validity window. Filter/settings hashes are bound through an independent MAC purpose, not exposed as raw query text in the token; the existing location/category/tag token spelling remains unchanged. A changed filter/timezone/page size, revoked/expired/mandatory session, restored epoch, wrong namespace/key or tampering cannot continue the old read. An explicitly fresh valid login for the same principal can. These are live pages, not frozen offline snapshots, and later deletion/editing may remove matches. Full 22-entity bootstrap/delta and ranked search remain separate implementation gates.

Filtering streams candidates until a complete result page and lookahead are found, rather than filtering a raw first page and pretending nothing else exists. Automated synthetic legacy-source scans of 500/2,500/5,000 candidates verify completeness and absence of read-side publication. Their local timings are not a NAS/physical-phone performance guarantee or a 20,000-photo benchmark.

## Verification

```sh
uv run --frozen python -m unittest discover -s tests -v
go -C server test -race -count=1 ./internal/store -run '^TestCatalog'
go -C server test -race -count=1 ./internal/httpapi -run '^TestCatalog'
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzCatalogJSON -fuzztime=3s -parallel=2
npm --prefix web run api:generate
npm --prefix web run typecheck
npm --prefix web test -- src/lib/api/catalog.test.ts --coverage.include=src/lib/api/catalog.ts
npm --prefix web test -- src/features/catalog --coverage.include='src/features/catalog/*.ts'
npm --prefix web run test:e2e -- tests/e2e/catalog-api.spec.ts --project=desktop
npm --prefix web run test:e2e -- tests/e2e/catalog.spec.ts --project=phone
```

Contract checks exercise optional defaults, explicit nondefault measurement tuples, safe numeric/Unicode bounds, strict ASCII base units, nullable presence/partial edits, restoration, response/offline shape, original positive proofs, Origin/body limits, strong ETags, and complete bounded pages without removing filters. Generated-type fixtures separately reject missing Origin/proofs, null measurement, internal wrappers, implicit holdings and missing continuations. Store tests additionally drive actual SQLite creation/partial edits/classification/archive/restore/deletion, all mutation/no-op frozen-change replay, original/fresh-login/expired acknowledgements, concurrent duplicate/conflicting requests, retained/imported identities, full Unicode boundaries, disposed/zero/financial/history guards, shared media/product identifiers, owned-link cascades, and source/current/FTS/outbox/audit/reply/session rollback. Read and write COMMIT failures withhold all resources/continuations, while late authorization changes and safe counter/sequence exhaustion never acquire a new acknowledged mutation. Filtered-page tests additionally cover every filter and combined scope, exact same-purchase predicates, UTC/IANA defaults, nanosecond/DST/repeated/skipped civil dates, unsupported legacy instant syntax, Unicode/CJK/literal matching, poisoned cache fallback, complete 500/2,500/5,000-item candidate scans, every cursor filter/timezone/auth scope, live deletion, malformed publication and read-COMMIT failure. HTTP tests drive all six operations through verified TLS and real cookie jars, original/fresh-session frozen replies, exact nullable bodies, complete query forwarding, late revocation/reviewed-proof changes and unchanged general admission limits. Negative route/method/header/body/media/Unicode/safe-number/size cases and bounded parser fuzzing keep errors static and cookies untouched. A disposable run of the actual compiled CLI/server additionally drives genuine setup/login/password rotation and validates 22 actual metadata/list/ack/error replies against the executable root schemas before graceful shutdown and integrity checking. Browser adapters add 88 runtime/transport cases plus positive/negative compile fixtures for conditional measurement, original proofs and caller context. Real Chromium tests bundle the actual SDK only into a private copy of the production assets, then drive genuine setup/rotation, all six operations, live signed pages, stale-proof rejection, historical create/patch/deletion replies, and explicit identical-key reconciliation after a real commit with a deliberately malformed response. No test entrypoint enters the production build, and no credentials enter automatic artifacts. Metadata-state tests separately cover exact nullable/default/scale conversion, changed-only patches, explicit logical-field adoption, original category/item context, dispatch/retry proofs, cross-domain read invalidation and interrupted/unknown/session-lost results. Real-server metadata screen tests additionally drive category assignment, reviewed creation/partial edit/archive/restore/delete, protected login return, manual concurrent-field adoption, offline non-replay, every filter and signed-page navigation, normal scoped-list returns, genuinely committed malformed deletion replies and original-key reconciliation. Long hierarchy/metadata reviews run with light/dark axe and 200% text across five emulated layouts. Separate Purchase workflows are available; media/bulk/assisted-creation flows, sync and physical-device acceptance remain unfinished gates.
