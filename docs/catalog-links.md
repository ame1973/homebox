# Catalog Links and Tag Membership

**Implementation status:** the root executable contracts, generated browser types, schema/type checks, real Go barcode validator, independent persistence/materialization and all nine authoritative barcode/alias/attribute create/edit/delete service methods are implemented. Full tag-set replacement, signed collection pages, HTTP integration, browser adapters and maintenance screens are not yet implemented. Service tests do not make those routes available. The separate [catalog metadata workflows](catalog.md) are functional without an AI worker.

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

Fixed independent GTIN/UPC-E vectors, every single-digit EAN/UPC-A substitution, every UPC-E check-digit error, all compression branches, Unicode/ASCII/width/unsafe-repair boundaries and fuzz invariants exercise the actual implementation. Checksum validity does not certify GS1 allocation, ownership, a unique item, or a unique physical unit. The authoritative `Catalog.CreateBarcode` and `Catalog.PatchBarcode` services call this validator rather than trusting a client/schema result, derive normalized text and enforce owner-scoped uniqueness inside the write transaction. HTTP integration remains separate.

## Authoritative Go writes

The internal `store.Catalog` provides `CreateBarcode`/`PatchBarcode`/`DeleteBarcode`, `CreateAlias`/`PatchAlias`/`DeleteAlias`, and `CreateAttribute`/`PatchAttribute`/`DeleteAttribute`. Creation validates the original Item aggregate proof, assigns an independent UUIDv4 and requires row revision one; a retained identity cannot be reused. Edits/deletions validate the independent row proof, not an invented second Item header. Unrelated owner edits do not invalidate a still-current row proof, while every actual row change advances the Item aggregate so a stale Item deletion cannot absorb new metadata.

Each changed row, owner/item/unit approved search, current projection, frozen upsert/tombstone, catalog revision, immutable audit and response commits in one transaction. Missing owner/row authority, source/search/projection/receipt failure, safe-counter/sequence exhaustion, late authorization or failed COMMIT returns no resource/acknowledgement. An equal edit leaves source/counters/FTS/outbox unchanged but receives its own immutable replay receipt. These metadata operations neither receive stock nor create physical units, purchases, photos or AI jobs, and metadata remains maintainable on an archived item.

Duplicate exact aliases conflict only within one item; case, whitespace and Unicode normalization remain distinct. Product duplicates conflict only for one owner/symbology/normalized value, not globally or across symbologies. Repeated attribute keys/values remain independent rows; partial edits preserve omitted fields and supplied empty values are valid. The typed Go attribute-create command contains both values; the future HTTP reader must additionally reject an omitted required JSON value or null rather than silently treating it as empty.

Original operation, subject, proof and typed field presence are fingerprinted under the existing principal/epoch-scoped 30-day protocol. A fresh completed login can reconcile the same original response after later edits or source deletion; new keys cannot invent success for missing rows. Different input/operation/target/proof conflicts, and expired claims remain retained rather than reusable. Concurrent identical requests produce one identity/receipt; conflicting Item creation or row-edit proofs have one winner, while different current row proofs can both succeed. The transport still must enforce Origin/CSRF/client-version/body admission before these methods are exposed.

## Retained source and additive materialization

Migration 0009 adds independent barcode/alias/attribute current rows and indexed `(item_id, id)` lookup paths. A live source row with no counter receives revision one during explicit migration; a retained counter is preserved. Existing current payload bytes/timestamps and frozen outbox rows are not rewritten. A counter without a live source does not resurrect a row. Baseline backfill does not append fictional events, advance the catalog revision, rescale quantities or publish legacy search/OCR caches. This is not the full 22-entity sync backfill.

Persisted-data validation and new-command validation are different boundaries. Retained row/owner IDs, supported symbologies, exact UTF-8 text bounds and safe revisions must be valid, but historical raw/normalized strings remain inspectable even when they differ or would fail today's new-write checksum/alphabet policy. Reads and upgrades never call the new-write normalizer to repair those strings. New create/replace services call the real validator and derive the new normalized value. An explicit valid replacement can adopt today's normalization without changing row identity; deletion does not force an invalid historical value through new-write validation. Source fields remain authoritative; cached projection text never supplies a replacement value.

Go validates the complete backfill after its SQL and before the migration/checksum transaction commits. SQLite text length alone does not correctly enforce code-point bounds after native NUL controls, and JSON construction alone does not reject every malformed UTF-8 source. Invalid source or unsafe counters roll back all pending DDL/indexes/current/counter/ledger writes; the verified private pre-migration snapshot remains available. Stop the server and use the explicit commands in [Database Operations](database.md); serving never auto-migrates.

The private row loaders require the actual retained counter and return no partial resource on missing/corrupt state or SQL failure. Final-state builders publish exactly the version-one source payload, with the revision only in its enclosing projection. Transaction tests exercise repeated staging, independent tombstones, owner/item/unit-search dependencies, source preservation, frozen replies after later edits/deletion and complete rollback on statement, authorization or deferred-commit failure. These are real SQLite/transaction tests, not an assertion that public owned-write handlers exist. Each mutating service must explicitly stage its independent row and all owner/search dependencies atomically; the implemented row-write services do so through the existing transaction boundary, not by asking the payload builder to infer writes.

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

## Live pages and request boundary

`GET /items/{id}/barcodes`, `/aliases`, `/attributes`, and `/tags` expose stable-ID live pages. Limit is 1–100, default 50. Every page contains `data` and required nullable `next_cursor`; null terminates the result. Read all pages when reviewing complete membership, including larger legacy tag sets. Do not silently return only 100 legacy links or label a first page as complete.

Signed continuations bind principal, dataset epoch, owning item, collection purpose and page size for 15 minutes. A barcode continuation cannot become an alias/tag continuation or change owner. These are live reads, not frozen offline snapshots. An absent owner is an error rather than an invented empty collection. Lists do not claim a resource ETag.

All writes require exact Origin, an active completed administrator session, CSRF, supported client version, original key and the documented proof scope. Barcode/alias JSON is bounded to 16 KiB; attributes/tag replacement to 128 KiB. Lists accept only one documented limit/cursor within a 16 KiB raw query; writes accept no query. GET/DELETE accept no body or encoding. Exact field names, duplicate checks, valid UTF-8 and safe nonrounding revisions remain server-authoritative. Resource responses and errors never expose submitted credentials or private SQL/path diagnostics.

Source changes, owned/owner/tag/search counters, final current/frozen projections, immutable audit and frozen responses must commit together. Revalidate authorization in SQL after body reading. A late source/FTS/outbox/reply/session/commit failure must withhold the entire result. Preserve original operation/body/key/proof for explicit retries after deletion or a fresh same-principal login; expired claims are not reusable. No hidden rebase, automatic reconnect replay or AI-worker prerequisite is permitted.

## Verification and remaining work

```sh
uv run --frozen python -m unittest tests.test_catalog_links_contracts -v
go -C server test -race -count=1 -cover ./internal/barcode
go -C server test -race -count=1 ./internal/store -run '^TestCatalogLink'
go -C server test -race -count=1 ./internal/store -run '^TestCatalogOwned'
go -C server test ./internal/barcode -run '^$' -fuzz FuzzNormalize -fuzztime=3s -parallel=2
uv run --frozen python -m unittest discover -s tests -v
npm --prefix web run api:generate
npm --prefix web run typecheck
make check-web
```

These checks cover complete bounded DTOs/offline shape, immutable ownership, exact fields, atomic barcode-pair and partial-attribute shapes, barcode ASCII/width gates, required page continuations, body/query limits, explicit owner-versus-row proofs, tag-set membership bounds and the owning-Item response. Strict generated-type fixtures reject missing Origin, numeric/caller-normalized barcodes, owner reassignment, missing alias/value/continuation, null attribute values and a tag-page replacement acknowledgement. Numeric checksum arithmetic is deliberately **not** marked passed by a schema test.

Real Go/SQLite write tests additionally cover all nine service methods, actual checksum/normalization integration, original Item-versus-row proofs, independent UUID/counter retention, owner/symbology duplicates, exact Unicode/NUL text, partial-field fingerprints, no-op receipts, concurrent original/conflicting/independent reviews, new-session/deleted-source/expired reconciliation, and source/current/FTS/outbox/audit/reply/session/overflow/late-auth/deferred-commit rollback. Retained invalid historical values stay readable/deletable; only explicit valid replacements adopt current normalization.

Next implementation gates are full tag-set replacement, owner-scoped signed pages, HTTP conformance, typed no-replay adapters and reviewed maintenance screens. Public builds/tests do not depend on the local specification pack. Full sync, physical devices/NAS and off-host recovery remain separately uncompleted gates.
