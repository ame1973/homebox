# Catalog Links and Tag Membership

**Implementation status:** all fourteen owned-catalog Go services and HTTP operations, typed stateless adapters and responsive reviewed maintenance screens are implemented and tested: nine barcode/alias/attribute create/edit/delete actions, complete tag-set replacement and four owner-scoped signed collection reads. Executable contracts, generated types, real barcode validation and independent persistence/materialization are also implemented. The screens use real Go/SQLite data and the same routes on phone, tablet and desktop, not mock collections. The separate [catalog metadata workflows](catalog.md) are functional without an AI worker. Holdings, camera/scanning, media, full sync and remaining product workflows are still in development.

## Independent identity and immutable ownership

Product barcodes, aliases and attributes have their own stable IDs and positive safe-integer revisions. A row is not identified by its displayed value, its owner/value pair, or an array position. New rows use independent UUIDv4 identities; supported imported IDs remain stable. IDs and owner item IDs contain 1–128 Unicode code points, and revisions range from 1 to `9007199254740991`.

The owning item is selected by the collection path on creation and retained in source thereafter. Write bodies never accept `id`, `item_id`, `revision`, `normalized_value`, private OCR, or implicit inventory/financial fields. Editing a row cannot reassign it to another item. Moving meaning between owners requires an explicit unlink/create workflow with new identity, not hidden mutation of history.

The HTTP resource adds its revision to the unchanged version-one offline payload. Each owned row has independent current/outbox updates and tombstones; deleting one does not delete its item, another owner's row, or a shared image. Item deletion must still stage every owned identity's tombstone before its source cascade. Retained counters/audit prevent identity reuse.

## Exact values and barcode policy

| Record | Value rules |
| --- | --- |
| Alias | 1–300 code points, exact case/whitespace/normalization; duplicate exact aliases within one owner conflict |
| Attribute | Key 1–100 code points, value 0–10,000; empty value is explicit and valid; duplicate keys remain independent rows, not implicit upserts |
| Barcode | Supported symbology and raw/derived normalized strings of 1–512 code points; shared commercial values across different items are valid |

Keep raw product barcode text and leading zeros. Never convert digits to a number. Current normalization preserves the exact validated string within its symbology; there is no silent trimming, case folding, Unicode normalization, expansion or cross-symbology identity merge. The server derives `normalized_value`, never trusts a submitted normalized string, and checks any per-owner duplicate inside the transaction. The lookup service's cross-format candidate behavior remains separate work.

The supported symbologies retain their existing names:

- `EAN_13` includes JAN: exactly 13 ASCII digits and a valid GTIN check digit.
- `EAN_8`: exactly eight ASCII digits and a valid check digit.
- `UPC_A`: exactly 12 ASCII digits and a valid check digit, preserving a leading zero.
- `UPC_E`: eight ASCII digits including number system 0 or 1 and the check digit. Validate the check digit through the standard UPC-A expansion, but retain the original compressed string. Do not guess omitted number-system/check digits from a six-digit input.
- `CODE_128`: 1–512 ASCII characters, including decoder-provided ASCII controls; preserve the exact decoded string.
- `QR_CODE` and `OTHER`: preserve valid exact Unicode text. A stored product QR is not a HomeBox location/unit QR or permission to navigate an external URL/execute an inventory action.

JSON Schema enforces enum, type, width and ASCII shape. It does **not** perform GTIN arithmetic, compare derived strings, reject lone UTF-16 surrogate repair at the HTTP boundary, or certify authoritative normalization. The `x-runtime-validation` annotation records the required authoritative integration gate; the real Go validator is tested separately below, not certified by a schema vector.

### Go validator

`server/internal/barcode.Normalize(symbology, raw)` validates and returns the exact input string or the static `barcode.ErrInvalid` error with no retained value. It bounds raw bytes before UTF-8/rune scanning, supports the seven contracted symbologies, checks EAN/JAN/UPC-A arithmetic, and validates all UPC-E expansion cases plus number systems 0/1 without replacing the compressed string. Code128 ASCII controls and QR/OTHER Unicode remain exact. No database, network, UI navigation, unique-item lookup, stock mutation or AI worker is involved.

Fixed independent GTIN/UPC-E vectors, every single-digit EAN/UPC-A substitution, every UPC-E check-digit error, all compression branches, Unicode/ASCII/width/unsafe-repair boundaries and fuzz invariants exercise the actual implementation. Checksum validity does not certify GS1 allocation, ownership, a unique item, or a unique physical unit. The authoritative `Catalog.CreateBarcode` and `Catalog.PatchBarcode` services call this validator rather than trusting a client/schema result, derive normalized text and enforce owner-scoped uniqueness inside the write transaction. The HTTP boundary preserves those same rules rather than trusting JSON shape alone.

## Authoritative Go writes

The internal `store.Catalog` provides `CreateBarcode`/`PatchBarcode`/`DeleteBarcode`, `CreateAlias`/`PatchAlias`/`DeleteAlias`, and `CreateAttribute`/`PatchAttribute`/`DeleteAttribute`. Creation validates the original Item aggregate proof, assigns an independent UUIDv4 and requires row revision one; a retained identity cannot be reused. Edits/deletions validate the independent row proof, not an invented second Item header. Unrelated owner edits do not invalidate a still-current row proof, while every actual row change advances the Item aggregate so a stale Item deletion cannot absorb new metadata.

Each changed row, owner/item/unit approved search, current projection, frozen upsert/tombstone, catalog revision, immutable audit and response commits in one transaction. Missing owner/row authority, source/search/projection/receipt failure, safe-counter/sequence exhaustion, late authorization or failed COMMIT returns no resource/acknowledgement. An equal edit leaves source/counters/FTS/outbox unchanged but receives its own immutable replay receipt. These metadata operations neither receive stock nor create physical units, purchases, photos or AI jobs, and metadata remains maintainable on an archived item.

Duplicate exact aliases conflict only within one item; case, whitespace and Unicode normalization remain distinct. Product duplicates conflict only for one owner/symbology/normalized value, not globally or across symbologies. Repeated attribute keys/values remain independent rows; partial edits preserve omitted fields and supplied empty values are valid. The typed Go attribute-create command contains both values; the HTTP reader also rejects an omitted required JSON value or null rather than silently treating it as empty.

Original operation, subject, proof and typed field presence are fingerprinted under the existing principal/epoch-scoped 30-day protocol. A fresh completed login can reconcile the same original response after later edits or source deletion; new keys cannot invent success for missing rows. Different input/operation/target/proof conflicts, and expired claims remain retained rather than reusable. Concurrent identical requests produce one identity/receipt; conflicting Item creation or row-edit proofs have one winner, while different current row proofs can both succeed. The HTTP transport enforces Origin/CSRF/client-version/body admission before dispatch, and the domain transaction independently revalidates the active completed session after body reading.

## Retained source and additive materialization

Migration 0009 adds independent barcode/alias/attribute current rows and indexed `(item_id, id)` lookup paths. A live source row with no counter receives revision one during explicit migration; a retained counter is preserved. Existing current payload bytes/timestamps and frozen outbox rows are not rewritten. A counter without a live source does not resurrect a row. Baseline backfill does not append fictional events, advance the catalog revision, rescale quantities or publish legacy search/OCR caches. This is not the full 22-entity sync backfill.

Persisted-data validation and new-command validation are different boundaries. Retained row/owner IDs, supported symbologies, exact UTF-8 text bounds and safe revisions must be valid, but historical raw/normalized strings remain inspectable even when they differ or would fail today's new-write checksum/alphabet policy. Reads and upgrades never call the new-write normalizer to repair those strings. New create/replace services call the real validator and derive the new normalized value. An explicit valid replacement can adopt today's normalization without changing row identity; deletion does not force an invalid historical value through new-write validation. Source fields remain authoritative; cached projection text never supplies a replacement value.

Go validates the complete backfill after its SQL and before the migration/checksum transaction commits. SQLite text length alone does not correctly enforce code-point bounds after native NUL controls, and JSON construction alone does not reject every malformed UTF-8 source. Invalid source or unsafe counters roll back all pending DDL/indexes/current/counter/ledger writes; the verified private pre-migration snapshot remains available. Stop the server and use the explicit commands in [Database Operations](database.md); serving never auto-migrates.

The private row loaders require the actual retained counter and return no partial resource on missing/corrupt state or SQL failure. Final-state builders publish exactly the version-one source payload, with the revision only in its enclosing projection. Transaction tests exercise repeated staging, independent tombstones, owner/item/unit-search dependencies, source preservation, frozen replies after later edits/deletion and complete rollback on statement, authorization or deferred-commit failure. These real SQLite/transaction tests are complemented by the HTTP/TLS and compiled-server checks below. Each mutating service must explicitly stage its independent row and all owner/search dependencies atomically; the implemented row-write services do so through the existing transaction boundary, not by asking the payload builder to infer writes.

## Proof scopes and acknowledgements

| Operation | Original If-Match scope | Successful response |
| --- | --- | --- |
| `POST /items/{id}/barcodes`, `/aliases`, `/attributes` | Owning item aggregate | New independent row, revision one, matching strong row ETag |
| `PATCH /barcodes/{id}`, `/aliases/{id}`, `/attributes/{id}` | Independent row revision | Same row/owner, no-op or next row revision, matching strong row ETag |
| `DELETE /barcodes/{id}`, `/aliases/{id}`, `/attributes/{id}` | Independent row revision | Action acknowledgement whose revision is the deleted row's revision plus one; no fabricated ETag |
| `PUT /items/{id}/tags` | Owning item aggregate | Frozen owning `Item` and its matching strong ETag, not a truncated tag page |

Actual row changes also advance the owner's item aggregate and dependent item/unit approved search in the same transaction. Thus a reviewed item deletion cannot silently absorb links created or edited after its original item proof, while a row edit/delete does not need an invented second revision header. A row's proof and returned revision must never be confused with the separately touched owner revision.

Barcode writes replace the complete symbology/raw pair atomically. Alias writes replace only the exact alias. Attribute creation requires both key and value; `AttributePatch` changes only specified fields, with a nonempty body and no null values. Repeating equal values is a no-op with the original resource revision but an immutable replay receipt.

Tag replacement is one atomic complete-set operation: at most 100 unique bounded tag IDs, each of which must exist. An empty array clears membership. Reject duplicate/missing IDs without partial changes or implicit tag creation. Touch each affected tag membership aggregate, the item's aggregate/search dependencies, and each independent canonical item-tag upsert/tombstone together. Existing tag deletion proofs must become stale when new members are linked. Read current membership separately; a historical response never replaces a newer cache.

`Catalog.ReplaceTags` enforces this against the original Item proof and returns the complete frozen Item, not a bounded tag page. It copies the caller's array and fingerprints its original order; a different order with the same key is a changed request, while a new key/current proof for an equal set is a no-op. A nil slice/null input is not an explicit empty array. Existing larger legacy sets are read completely before replacement, so clearing 205 members removes all 205 independent links without deleting Tag identities. Explicit relinking advances the retained composite counter rather than resetting it. Only affected Tag/link counters advance; unchanged membership and other owners remain unchanged.

## Live pages and request boundary

The implemented `GET /items/{id}/barcodes`, `/aliases`, `/attributes`, and `/tags` operations return stable-ID live pages. Limit is 1–100, default 50. Every page contains `data` and required nullable `next_cursor`; null terminates the result. Read all pages when reviewing complete membership, including larger legacy tag sets. Do not silently return only 100 legacy links or label a first page as complete.

Signed continuations bind principal, dataset epoch, owning item, collection purpose and page size for 15 minutes. A barcode continuation cannot become an alias/tag continuation or change owner. These are live reads, not frozen offline snapshots. An absent owner is an error rather than an invented empty collection. Lists do not claim a resource ETag.

The implemented Go entrypoints are `Catalog.PageBarcodes`, `PageAliases`, `PageAttributes` and `PageTags`. They reuse the existing authorized metadata cursor codec with an independent canonical collection/owner MAC purpose, without changing location/category/global-Tag cursor spelling. They read source and actual retained counters, validate the owner even for an empty page, check the lookahead row before returning a prefix, and release no data or cursor before read/session COMMIT. A fresh same-principal session may continue; expired/restored/differently scoped cursors cannot authorize a read. Edits and deletion of the last-seen row do not turn these live pages into snapshots. The HTTP lists above expose these services with the documented admission/defaults and no list ETag.

Existing item-tag membership also requires its canonical independent counter. Reads/replacements reject missing, unsafe or noncanonical link authority rather than silently inventing revision one, including on removal. Canonical Go pairs and SQLite `json_array` remain equivalent for full-length Unicode IDs and separator characters. The current assigned-tag page validates the owner's complete legacy link authority in O(members) before paging; bounded counter joins are a measured-latency optimization for unusually large legacy sets, not permission to truncate membership.

All writes require exact Origin, an active completed administrator session, CSRF, supported client version, original key and the documented proof scope. Barcode/alias JSON is bounded to 16 KiB; attributes/tag replacement to 128 KiB. Lists accept only one documented limit/cursor within a 16 KiB raw query; writes accept no query. GET/DELETE accept no body or encoding. Exact field names, duplicate checks, valid UTF-8 and safe nonrounding revisions remain server-authoritative. Resource responses and errors never expose submitted credentials or private SQL/path diagnostics.

Source changes, owned/owner/tag/search counters, final current/frozen projections, immutable audit and frozen responses must commit together. Revalidate authorization in SQL after body reading. A late source/FTS/outbox/reply/session/commit failure must withhold the entire result. Preserve original operation/body/key/proof for explicit retries after deletion or a fresh same-principal login; expired claims are not reusable. No hidden rebase, automatic reconnect replay or AI-worker prerequisite is permitted.

## Implemented HTTP operations

Paths below are relative to `/api/v1`. All fourteen operations use the existing same-origin, completed-session and general admission boundary; writes also require original CSRF/client-version/key/proof headers. There is no worker dependency.

| Path | Methods | Result/proof |
| --- | --- | --- |
| `/items/{id}/barcodes` | `GET`, `POST` | Required-cursor BarcodeList; create Barcode against Item proof |
| `/barcodes/{id}` | `PATCH`, `DELETE` | Complete-pair edit or deletion against independent Barcode proof |
| `/items/{id}/aliases` | `GET`, `POST` | Required-cursor AliasList; create Alias against Item proof |
| `/aliases/{id}` | `PATCH`, `DELETE` | Exact replacement or deletion against independent Alias proof |
| `/items/{id}/attributes` | `GET`, `POST` | Required-cursor AttributeList; complete key/value creation against Item proof |
| `/attributes/{id}` | `PATCH`, `DELETE` | Nonempty partial edit or deletion against independent Attribute proof |
| `/items/{id}/tags` | `GET`, `PUT` | Required-cursor ItemTagList; complete replacement returns owning Item/ETag against Item proof |

There is no single-resource GET for an owned barcode/alias/attribute; unsupported methods return `405` and the exact `Allow`. URLs must be exact, with only one documented `limit`/`cursor` on collection GETs, within 16 KiB raw query bytes. Default page size is 50, unlike the preserved location/taxonomy default of 100. Writes accept no query, including a bare `?`; GET/DELETE accept no body or content encoding. Imported `new`/`tree` write identities are not reserved routes.

Barcode/alias bodies are capped at 16 KiB; attribute/tag-set bodies at 128 KiB. Exact field names, duplicate fields, UTF-8, lone surrogate escapes, null/omitted types and byte/code-point bounds are checked before the authoritative service. `attribute_value: ""` is valid explicit creation/clearing; omission on creation is not. Body/query limits and error handling never expose storage paths, submitted credentials or raw SQL.

`BARCODE_CONFLICT` and `ALIAS_CONFLICT` are static `409` business codes for the documented owner-local duplicates, not permission to rebase or transfer a row. Invalid GTIN arithmetic/alphabet or invalid supplied fields return `422 VALIDATION_ERROR`; stale proofs/keys retain their existing conflict codes. Browser transport allowlists these codes but discards arbitrary server messages/details. Resource replies have the matching independent strong ETag (owning Item for tag replacement); list/deletion replies have none.

## Typed stateless browser adapters

`web/src/lib/api/catalog-links.ts` exposes all fourteen operations: `fetchItemBarcodesPage`, `fetchItemAliasesPage`, `fetchItemAttributesPage`, `fetchItemTagsPage`; `createBarcode`/`patchBarcode`/`deleteBarcode`; `createAlias`/`patchAlias`/`deleteAlias`; `createAttribute`/`patchAttribute`/`deleteAttribute`; and `replaceItemTags`. These functions are transport adapters, not a local authoritative store or a maintenance screen.

Creation and tag replacement accept the original Item `expectedRevision`. Independent row edits/deletions require `OwnedRowWriteContext` with the original row `expectedRevision` and retained `itemID`; the owner is used for local identity/response validation, never an invented second revision header. Every write also requires caller-held CSRF, the original key and AbortSignal. Header authority remains server-owned: the shared transport passes the caller's original credentials/key without refresh or substitution. Responses bind exact resource/owner identity, strong matching ETag and creation-one/original-no-op-or-next revisions; deletion acknowledgements require the exact independent next revision and no ETag. Tag replacement reuses the exact Item validator, never a Tag page masquerading as a current proof.

Input schemas preserve exact text/leading zeros/Unicode form and complete barcode pairs, require explicit attribute creation values, enforce nonempty partial attributes at compile time and runtime, and preserve ordered unique tag arrays. They mirror the executable barcode enum/ASCII/width conditions without pretending to certify GTIN arithmetic; the server performs actual checksum validation. Retained barcode responses use bounded persisted representation, not today's new-input shape, so old differing raw/normalized strings remain inspectable without repair.

All four collection replies require nullable `next_cursor`, unique bounded row IDs, the requested owner where present, maximum requested/default-50 page size, full nonterminal pages, a different next cursor and no list ETag. Each call returns only one explicit live page. Read all pages to review a complete set; never silently paginate or treat a live continuation as a frozen snapshot.

Read deadlines are eight seconds and writes thirty seconds. Pre-canceled requests are not sent; aborted/timed-out/late replies do not become success or imply rollback. No cookie access, token refresh, newer-proof lookup, automatic retry/rebase/reconnect submission, persistent credential/draft storage or mutation queue is added. Reconcile an uncertain committed reply explicitly with its original body/key/proof; invalidate current reads rather than installing historical responses. Real-browser SDK tests use private temporary composed entries and web roots only, never a production test route.

## Browser maintenance workflow

Open an item from `/items`, then choose barcodes, aliases, attributes or tag assignments. All devices share these application paths, distinct from the `/api/v1` HTTP paths above:

| Screen | Application route |
| --- | --- |
| Complete owned collection | `/items/{itemID}/barcodes`, `/items/{itemID}/aliases`, `/items/{itemID}/attributes` |
| Add an independent row | Append `/new` to its collection route |
| Edit/delete an independent row | Append `/{rowID}/edit` to its collection route |
| Complete Tag membership editor | `/items/{itemID}/tags` |

IDs are encoded without changing their stored identity. Imported Item and row IDs such as `new` remain distinct from static creation routes; an imported `new` Item's detail return uses `/items/new/view`. Normal navigation preserves the original item-list filters and visited cursor context. Protected login returns accept only implemented same-application paths.

1. Read the complete selected collection before opening a new proposal. Local search filters this complete view, not a raw first page, and never changes membership. Rows show their independent IDs/revisions so repeated attribute keys or similar aliases cannot merge implicitly. An imported identity that cannot safely become a path remains visible with an explanation rather than an invented edit URL.
2. For a new barcode, explicitly choose its format and enter the original string. No format, digits or checksum are guessed. Barcode pairs replace atomically; the server performs GTIN arithmetic and duplicate checks. Alias text remains exact. Attribute creation includes both fields, even an empty value; edits submit only changed fields. The direct text controls do not decode JSON escapes. Source/review string representations show quotes and escapes so leading zeros, whitespace, newlines and controls remain visible; stored URLs/HTML are text, never navigation or executable content.
3. Review the original Item proof for creation, or the original independent row proof for edits/deletion. A separately changed owner does not invent a second row precondition. Known changed source requires an explicit comparison and either retained logical edits or discarded input. Barcode format/raw adoption stays one pair; invalid local text is retained for correction. Deletion reviews the original row, not an unsaved form, and does not delete the catalog item or shared identifiers elsewhere.
4. For tags, select the entire intended set. The complete available index and original assigned labels remain review context; changed labels/revisions require an explicit recheck, and removed local selections remain visible until explicitly deselected/adopted. A new set is limited to 100, while larger legacy sets such as 205 members remain fully visible and are never truncated. Clearing selection changes only the volatile draft. The final review shows complete before/after identities, labels and counts; only confirmation replaces the server set.
5. If current membership changes, explicitly adopt it. Retaining edits applies only local additions/removals to the reviewed current baseline, preserving concurrent additions instead of silently overwriting them. Review the resulting complete set again. Tag assignment does not create/delete global Tag identities; the editor links to the real global Tag settings.
6. For an unknown sent result, retain the original key/body/proof and use the read-only check or explicit identical-request retry. A genuinely deleted row or owner does not erase that volatile original command. Reconnect only refreshes reads; historical acknowledgements never overwrite current state. Stopping waiting, backgrounding or leaving does not prove rollback. Explicit dirty/Back/reload guards warn before discarding volatile inputs and request context.

Loading, invalid/missing/partial reads, safe duplicate/checksum errors, offline coverage and session loss are explicit. New writes remain disabled while reads are incomplete, failed or unauthorized. General rate limits remain unchanged; honor displayed Retry-After guidance and explicitly re-read rather than automatically replaying a write. Forms wait for the first current read before initializing from a route's cache, then retain drafts across later refetches. Controls use existing official primitives, localized stable validation space, visible focus, wrapping metadata and one keyboard-accessible outer review scroller at enlarged text. These screens do not substitute file input for a camera or claim a persistent offline replica/draft queue.

## Complete reads and volatile review state

`readOwnedMetadata` completes the selected signed collection under a discriminated collection/Item cache scope, not every unrelated collection on every route. Tag editing also completes the available Tag index and checks assigned labels/revisions against it. All selected pages are bracketed with actual Item reads; changed fields/revisions, duplicate/cyclic continuations or inconsistent Tag metadata fail without a prefix. Unqueried collections are absent from the type, not fabricated empty arrays. This reduces redundant private requests without weakening general admission, omitting records, or treating a checked live read as the frozen offline snapshot protocol. A 205-member legacy assignment is complete data, not a silently capped first page. No newer command proof is installed automatically.

`owned-form` reuses the actual SDK schemas for outgoing values, preserves exact text/empty values and emits only changed row fields. New barcode forms start without an invented namespace. Explicit adoption keeps a locally edited symbology/raw pair together, retains invalid drafts and never moves edits to another row/owner. Attribute key/value adoption is field-specific. Tag adoption applies explicitly retained local additions/removals to the reviewed current set, preserving concurrent additions and order; an oversized legacy/invalid draft is not truncated or silently repaired. A new review must show that resulting complete set before confirmation.

`owned-commands` copies the original Item, independent row, before/after labels and exact body under one volatile key. Creations/tag replacement dispatch only the Item proof; row edits/deletions dispatch only the retained row proof and owner. Tag requests derive one original ordered ID array from validated after-label snapshots; old before-sets can exceed 100, new sets cannot. Mismatched owners, invalid/duplicate reviewed Tags and empty typed attribute edits do not dispatch. Do not persist these proposals as offline write queues.

`useOwnedMutation` reuses the shared foreground-only reviewed controller: review is read-only, confirmation sends once with a fresh caller-held CSRF, retry remains disabled and network mode is always. Confirmed or sent failed writes invalidate catalog/taxonomy and dependent holding/purchase/Asset reads only; frozen replies never replace newer query data. A pre-send failure does not invalidate those roots. Cancellation/unknown replies preserve uncertainty and original context even after current reads remove the target; only explicit original-key reconciliation can retry. Session loss clears volatile commands. The responsive maintenance composition above uses these same helpers.

## Verification and remaining work

```sh
uv run --frozen python -m unittest tests.test_catalog_links_contracts -v
go -C server test -race -count=1 -cover ./internal/barcode
go -C server test -race -count=1 ./internal/store -run '^TestCatalogLink'
go -C server test -race -count=1 ./internal/store -run '^TestCatalogOwned'
go -C server test -race -count=1 ./internal/store -run '^TestCatalogTags'
go -C server test -race -count=1 ./internal/httpapi -run '^TestCatalogLinks'
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzCatalogLinksJSON -fuzztime=3s -parallel=2
go -C server test ./internal/barcode -run '^$' -fuzz FuzzNormalize -fuzztime=3s -parallel=2
uv run --frozen python -m unittest discover -s tests -v
npm --prefix web run api:generate
npm --prefix web run typecheck
npm --prefix web test -- --coverage.enabled=false src/lib/api/catalog-links.test.ts
npm --prefix web run test:e2e -- tests/e2e/catalog-links-api.spec.ts --project=desktop
npm --prefix web test -- --coverage.enabled=false src/features/catalog/owned-page.test.tsx src/features/catalog/owned-components.test.tsx
npm --prefix web run test:e2e -- tests/e2e/catalog-links.spec.ts --project=desktop
make check-web
```

These checks cover complete bounded DTOs/offline shape, immutable ownership, exact fields, atomic barcode-pair and partial-attribute shapes, barcode ASCII/width gates, required page continuations, body/query limits, explicit owner-versus-row proofs, tag-set membership bounds and the owning-Item response. Strict generated-type fixtures reject missing Origin, numeric/caller-normalized barcodes, owner reassignment, missing alias/value/continuation, null attribute values and a tag-page replacement acknowledgement. Numeric checksum arithmetic is deliberately **not** marked passed by a schema test.

Real Go/SQLite write tests additionally cover all nine service methods, actual checksum/normalization integration, original Item-versus-row proofs, independent UUID/counter retention, owner/symbology duplicates, exact Unicode/NUL text, partial-field fingerprints, no-op receipts, concurrent original/conflicting/independent reviews, new-session/deleted-source/expired reconciliation, and source/current/FTS/outbox/audit/reply/session/overflow/late-auth/deferred-commit rollback. Retained invalid historical values stay readable/deletable; only explicit valid replacements adopt current normalization.

Tag-set/page tests additionally exercise original-array fingerprints and copy isolation, no-op/equal-set order, same-principal/deleted-owner/expired reconciliation, single-winner concurrency, 205-member complete legacy reads/clears, retained relinks, full Unicode pair equivalence, all changed/unchanged Tag/Item/unit-search scopes and current link authority. Four-way read tests cover exact public fields, terminal/null cursors, cross-owner/collection/global-purpose rejection, tampering, live edits/deletion, lookahead corruption, active/fresh/expired/restored sessions, private-key/cancel/SQL/COMMIT failure and no read-side publication.

HTTP tests drive all fourteen operations through real services and independently verified TLS/cookie jars, including strong row-versus-Item ETags, signed pages, exact nullable/empty field presence, true checksum rejection, original/fresh-session frozen replies and no current-state overwrite. All ten writes have credential/CSRF/version/key/proof/header checks; method/URL/query/media/chunked-body/Unicode/field/limit negatives and after-body session/proof races are covered. Bounded JSON fuzzing exercises the actual readers. A disposable run of the compiled CLI/server additionally performs genuine setup/login/password rotation, drives all fourteen operations and validates 40 real replies against executable schemas before graceful shutdown/integrity checking. Existing account/location/taxonomy/catalog browser workflows are regression-tested separately; the owned maintenance screen evidence is described below.

Owned SDK checks additionally exercise all fourteen exact requests, caller-owned proof/owner/key/body snapshots, retained legacy response boundaries, input alphabets/null/partial-field bounds, complete page/resource/ETag/deletion invariants, safe errors and distinct abort/deadline behavior without storage or replay. Strict fixtures cover every call plus missing Item proofs, ownerless row contexts, empty/incomplete attributes, caller normalization/owner changes, null membership and uncontracted filters. Actual Chromium drives the real adapters/Go server and a genuinely committed malformed wrong-owner reply followed only by an explicit identical original-key retry.

Maintenance screen checks additionally cover all nine row actions and complete Tag assignment/clearing, exact text and repeated attribute keys, original Item-versus-row proofs, safe actual checksum/duplicate rejection, protected login return, manual source/selected-label/member-delta adoption, unavailable selections, cached-first-read initialization, offline non-replay and cache/session purge. Real Go browser workflows include genuinely committed malformed deletion replies with identical-key reconciliation and Back/Escape interruption after an actual commit. A private baseline fixture uses Node's development-only built-in SQLite before the Go server starts, closes it and passes the real CLI check, then reads and clears all 205 legacy memberships through genuine authenticated routes without relaxing the production limiter or adding a test endpoint. Light/dark axe, keyboard/static focus, 200% long reviews and five responsive layouts remain emulation, not physical-device evidence.

The remaining purchase/media/asset lifecycle, camera, search/AI, full sync/offline/PWA, analytics/finance, backup/import/export and deployment workflows are still separate complete-product gates. Public builds/tests do not depend on the local specification pack. Physical devices/NAS and off-host recovery remain separately uncompleted gates.
