# Catalog Item Rules

**Implementation status:** authoritative Go/SQLite catalog creation, current reads, partial edits, archival/restoration, guarded deletion, internal stable-ID pages, and final-state item/unit approved-search publication are implemented and tested. Public filtered/signed pages, item HTTP handlers, catalog screens, related-link maintenance, holdings, and complete search/sync workflows are still in development. Internal services do not make the HTTP routes below available by themselves.

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

## Authoritative transaction service

`store.NewCatalog(auth)` binds catalog operations to the existing private store; it cannot select another database or expose SQL to HTTP handlers. Current services are:

- `Get`: return validated current metadata and the actual retained aggregate revision. Missing/unsafe counters are corruption, not permission to fabricate revision one.
- `List`: internal, unfiltered stable-ID pages of 1–100 records using an internal `NextID`. Includes archived metadata, rejects corrupt rows/lookahead without a partial prefix, and releases no page/continuation before read/session COMMIT. It is **not** the public filtered/signed item list below.
- `Create`: validate optional defaults and explicit measurement, verify a referenced category, allocate an independent UUIDv4, invalidate its classification ancestors, and publish source-derived item/search data. A new identity must begin at revision one; a retained/tombstoned identity cannot be reused under a new meaning.
- `Patch`: preserve omitted fields and explicit nullable presence, validate the reviewed target revision, reject history-bearing type/measurement changes, and invalidate old/new classification ancestors once. Equal nullable values are no-ops even when supplied through a new pointer; they still receive immutable frozen acknowledgements.
- `Archive`: audited catalog-status change with an optional exact reason. Repeated archival is a value-equal no-op; restoration is an explicit partial status patch.
- `Delete`: verify the original proof and all physical/financial/history guards, collect owned stable IDs, and emit independent item/search/barcode/alias/attribute/media-link/review/item-tag tombstones with category/tag aggregate invalidation. FK cascades remove only owned source rows and associated jobs/derived item FTS, not shared media or another owner's links.

Nullable command presence is an internal `NullableString`/`NullableID` representation, never a wire wrapper. Source, final item/unit approved FTS, current/frozen projections, counters, immutable mutation events and principal/epoch-scoped original responses share one transaction. The active completed session is checked again before COMMIT, including on replay. Statement, later dependent write, exhausted revision/sequence, late authorization, or deferred commit failure returns no successful DTO/acknowledgement and preserves the entire original state.

Deleting catalog metadata is not secure erasure. Immutable audit, retained counters, frozen replies/outbox rows, and shared media remain subject to their own retention rules. OCR tied to retained media stays private; deleting an owned job nulls its OCR job reference instead of copying raw text into publication. Complete history APIs and exact event-to-target/outbox associations are separate unfinished work. Related-link upsert services, unit/stock CRUD, public signed/filter pages and item HTTP/screens are not enabled by this module.

## Required HTTP boundary

All paths below are relative to `/api/v1`. Their contract definitions are not evidence of implemented handlers.

| Operation | Required behavior |
| --- | --- |
| `GET /items` | Live stable-ID pages, all supplied filters applied together, no body |
| `POST /items` | Required name, validated optional metadata/defaults, no unrelated If-Match |
| `GET /items/{id}` | Current metadata, including archived items; no body or query |
| `PATCH /items/{id}` | Nonempty partial metadata with the original item aggregate proof |
| `POST /items/{id}/archive` | Original item proof and a required JSON object with an optional reason of at most 10,000 code points |
| `DELETE /items/{id}` | Original item proof, no body/query, and the reference guards above |

Writes require the exact configured Origin, an active completed administrator session, CSRF, a supported client version, and an original idempotency key. JSON is unencoded UTF-8, bounded to 128 KiB before parsing, with exact field names, no duplicates/unknown fields, no invalid UTF-8/lone surrogate repair, and exact safe-integer parsing. No metadata operation sets a session cookie. Resource replies have a strong quoted revision ETag; live pages and deletion acknowledgements do not invent a resource ETag.

Preserve the original key/body/proof when explicitly reconciling an uncertain result. Never automatically replay on reconnect, refresh a revision, or silently rebase a write. The authoritative transaction must commit source changes, final current/frozen projections, approved FTS, aggregate counters, immutable audit, and the frozen response together. No-op edits keep their resource revision but still receive a replayable acknowledgement. Historical replies invalidate reads rather than overwrite newer state.

## Live list filters

The item list retains all contracted filters: `from`, `to`, `currency`, `category_id`, `location_id`, `scope`, `status`, `merchant`, and `q`, plus `limit`/`cursor`. Do not silently discard a filter or truncate a complete result to its first page.

- Limit is 1–100, default 50. Every page explicitly returns `data` and nullable `next_cursor`; terminal pages use null.
- Omitted status includes active and archived metadata; an explicit status restricts it to `active` or `archived`.
- Scope is `direct` or `subtree`, default subtree, for category/location matching.
- Location matching uses current positive balances or nondisposed units, not historical visits or zero balances.
- Purchase date, currency, and merchant filters must match the same associated purchase. Civil date bounds are inclusive in the configured home timezone; do not match different purchases independently to fabricate a qualifying combination.
- Text filtering uses approved published text only, never raw/unreviewed OCR, stale legacy caches, or rejected candidates.

Signed continuations bind the administrator, dataset epoch, page size, every filter, and a 15-minute validity window. They are read continuations, never credentials; a fresh valid login for the same principal can continue them. They are live pages, not frozen offline snapshots. Full 22-entity bootstrap/delta and search remain separate implementation gates.

## Verification

```sh
uv run --frozen python -m unittest discover -s tests -v
go -C server test -race -count=1 ./internal/store -run '^TestCatalog'
npm --prefix web run api:generate
npm --prefix web run typecheck
```

Contract checks exercise optional defaults, explicit nondefault measurement tuples, safe numeric/Unicode bounds, strict ASCII base units, nullable presence/partial edits, restoration, response/offline shape, original positive proofs, Origin/body limits, strong ETags, and complete bounded pages without removing filters. Generated-type fixtures separately reject missing Origin/proofs, null measurement, internal wrappers, implicit holdings and missing continuations. Store tests additionally drive actual SQLite creation/partial edits/classification/archive/restore/deletion, all mutation/no-op frozen-change replay, original/fresh-login/expired acknowledgements, concurrent duplicate/conflicting requests, retained/imported identities, full Unicode boundaries, disposed/zero/financial/history guards, shared media/product identifiers, owned-link cascades, and source/current/FTS/outbox/audit/reply/session rollback. Read and write COMMIT failures withhold all resources/continuations, while late authorization changes and safe counter/sequence exhaustion never acquire a new acknowledged mutation. Actual item HTTP/UI, full sync and physical-device acceptance remain separate unfinished gates.
