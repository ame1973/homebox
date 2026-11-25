# Offline Replica and Safe Draft Protocol

SQLite is authoritative. IndexedDB is a disposable per-device read replica, not a backup or an offline write authority. Protocol projection version is `1` for the first application release.

**Current implementation:** the protected, no-store `GET /sync/status` reports materialized projection counts, the durable allocated change watermark, the retained floor and the highest retained change's timestamp. It explicitly returns `bootstrap_ready: false` and `retention_enabled: false`: the diagnostic is not proof of complete backfill, source integrity, retained-history completeness, working bootstrap/delta or automatic pruning. Missing count keys mean zero materialized rows, not zero source records. The 30-day minimum is a retention policy, not a claim that a pruning job exists or that the oldest row has that age. See [Sync Status](../docs/sync-status.md).

The protected [authenticated connectivity probe](../docs/connectivity.md) is available as a minimal no-store read with an eight-second stateless browser adapter. It verifies a completed session without renewing idle activity, but it does not start sync, establish source or storage health, or measure device coverage. The application shell displays foreground probe results with bounded retry/backoff after a completed session check; existing page-specific online controls still treat the browser signal as advisory and handle their own failures.

## Record identity

Every record contains `entity_type`, `entity_id`, `entity_revision`, `operation`, `projection_version`, and `payload`. Upserts contain a full validated projection; deletes contain null. Revisions are monotonic per logical identity, and the durable change-log sequence is globally monotonic.

| Entity | Identity |
| --- | --- |
| location, item, unit, barcode, media, media_link, category, tag | Stable row ID |
| alias, attribute, purchase, purchase_line, usage, cost, ai_review, events | Stable row ID |
| stock | Canonical JSON pair `[item_id, location_id]` |
| item_tag | Canonical JSON pair `[item_id, tag_id]` |
| search_doc | Canonical JSON pair `[entity_type, entity_id]` |
| asset, disposal | `item_unit_id` |

Composite keys use the canonical JSON string-array spelling produced by `JSON.stringify`: no insignificant whitespace, ordinary non-ASCII/HTML/slash/U+2028/U+2029 characters stay literal, quotes/backslashes and control characters are JSON-escaped, and hexadecimal control escapes use lowercase. Bound and validate each original ID independently; do not apply a single 128-code-point ID limit to the encoded pair, normalize Unicode, repair invalid UTF, or accept alternative escaped spellings as another identity.

A barcode's normalized value is a nonunique lookup index, not its primary key. Media has no owner; independent media-link rows provide many-to-many ownership. Removing one link must not delete another owner's image. Quantity projections use integer `quantity_base`, not `quantity_minor`.

## Transactional publishing

Every business mutation runs inside one immediate SQLite transaction:

1. Validate authentication, revisions, idempotency, and business invariants.
2. Modify authoritative domain records and append immutable events.
3. Rebuild affected approved search documents and projections from final transaction state.
4. Increment each changed logical projection revision and append its frozen upsert/tombstone.
5. Save the idempotent reply and invalidate relevant analytics revisions.
6. Commit everything or roll back everything.

Raw SQL writes outside transaction-aware domain services are forbidden. Initial legacy backfill must publish every existing eligible row before enabling sync. Exceptional bulk changes that cannot enumerate every affected projection rotate the epoch atomically; they cannot leave silent replica divergence.

The server's shared strict JSON checks reject invalid UTF-8 and unpaired surrogate escapes, and parse bounded safe integers exactly without floating-point rounding. These checks are available to future frozen-projection readers and preserve the existing HTTP input behavior; they do not by themselves validate projection schemas or authorize publication.

An internal Go payload validator compiles the supported flat scalar definitions for all 22 version-one kinds from an embedded, byte-for-byte copy of [`offline-projection.schema.json`](offline-projection.schema.json). It rejects malformed or lossy JSON, duplicate or unknown properties, unsupported payload-schema grammar and invalid field values without rewriting the supplied bytes. Root and embedded schema copies are compared by the contract suite; a definition that needs a new keyword, type, pattern or format must be reviewed and supported before use. The validator deliberately adds a 128-byte number-token limit and JavaScript-safe integer bounds even where the schema alone is broader. Its date-time check rejects year zero, leap seconds and non-RFC 3339 spellings, including a trailing newline that the Python format checker currently accepts. These stricter rules can reject otherwise schema-valid historical data; callers must fail closed rather than silently change retained bytes.

The payload helper validates a **payload only**. A separate private Store check composes it with a supported kind, version-one marker, safe positive revision, operation and exact row identity. It requires a SQL NULL payload for a tombstone and compares stock, item-tag and search-document identities using the existing canonical pair spelling; other upserts bind their independent ID or Unit-owned identity. Neither helper parses a serialized record envelope, checks an authenticated session, verifies source completeness, or proves that `search_doc.approved_text` actually came from reviewed sources. Callers must bound total payload/page sizes before validation and perform those authority and provenance checks in the same authorized transaction as their frozen reads. Neither helper alone enables public bootstrap or delta; synthetic and selected-producer tests do not make sync ready.

A separate, read-only private check can compare a selected **current** search payload to text freshly derived in the same transaction from actual Item/Unit metadata, scoped purchase evidence, and accepted or corrected, reviewed free-text suggestions. It does not use search or FTS caches; an unreviewed candidate or raw OCR is not a source for this text. The caller must still establish that the supplied bytes and revision are the actual current row and counter, bound payload and page sizes, and authorize and revalidate the session. Source text aggregation may grow with the underlying records. This check must **not** be applied to a historical delta against today's source: earlier frozen text can legitimately differ after an authorized change and must remain byte-for-byte unchanged. The check neither backfills missing documents nor establishes complete source coverage.

Derived effects matter: changing category/tag/alias/purchase/review data may change item search; moving a unit changes its location evidence; deleting an owner emits media-link tombstones. Paths are derived from parent links rather than copied into every descendant projection. Unreviewed OCR and rejected suggestions never enter published search documents.

## Financial correction effects

The [audited financial correction contracts](../docs/financial-corrections.md) define current accounting effects without another replica kind. Allocation correction retains the original posted Line and may create a new independent replacement, but publishes an old-Line tombstone so retained audit source cannot resurrect a voided current attribution. Disposal correction retains the first posted decision and every immutable correction, publishing only the actual current financial decision or a tombstone for explicit void. An intentional zero-valued current decision remains an upsert, not a void.

Existing `purchase_line`/`disposal` identities, version-one payload shapes and the 22-kind envelope remain unchanged. Source, correction/audit, real independent revisions, affected financial/search scopes, frozen upserts/tombstones and original response must commit atomically. Never rebuild effective values from current/FTS caches or overwrite old frozen bytes. Detailed authenticated correction history is not a second offline authoritative ledger; physical status/placement and movements are not undone by financial void. Migrations 0017/0018 and private Purchase and Disposal correction/history services implement these source/current-effect boundaries, including validated void gaps, complete linked Disposal history and fail-closed damaged context. The five correction HTTP operations and reviewed correction browser workflows expose those boundaries, but the full frozen sync protocol remains unimplemented; partial publication is not sync readiness.

## Frozen bootstrap

`GET /sync/bootstrap` starts a short-lived materialized snapshot. In one immediate transaction, read server identity, dataset epoch, projection version, and change-log high watermark `H`, then materialize all current eligible projections in deterministic `(entity_type, entity_id)` order. Paginated responses read only these frozen rows, never live joins.

Snapshot cursors bind server instance, epoch, projection version, snapshot ID, next ordinal, and expiry with an HMAC or an equally strong server-side token. The default lifetime is 15 minutes. Snapshot expiry returns `410 SNAPSHOT_EXPIRED`; do not garbage-collect unexpired snapshot pages.

The internal cursor codec now signs independent snapshot and delta purposes with the private server key. Snapshot tokens bind the principal, exact dataset identity, snapshot ID, ordinal, high watermark and nanosecond-precision 15-minute interval. Delta tokens bind the principal, exact dataset identity and sequence without a wall-clock expiry; a later authorized database read must compare the sequence with the actual retention floor and allocated watermark in the same transaction. The codec authenticates a bounded canonical token but does not authenticate a session, materialize a snapshot, check the stored snapshot row or implement either HTTP route. Tokens are integrity-protected, not encrypted: never put credentials or private record data in them. Incompatible signed dataset scope requires reset; a changed or missing signing key cannot be distinguished from tampering.

The browser stores each page and its cursor atomically in a staging namespace. Partial bootstrap data is not presented as a complete replica. The final page has `complete=true`, `next_cursor=null`, and a server-issued `delta_cursor` for `H`. Publish staging and this cursor in one IndexedDB transaction. Never construct signed cursors in the browser.

Changes after `H`, including deletions, are consumed through delta sync. An expired bootstrap discards staging only, preserving the previous published replica and all drafts.

## Ordered deltas and retention

`GET /sync/changes?after=...` returns frozen change records in sequence order, a high watermark, a next cursor, and `has_more`. Read the JSON stored at commit time, not current mutable rows. Empty pages retain the cursor's sequence. Apply all rows, dependent invalidations, and cursor advancement in one IndexedDB transaction.

A private Store reader now checks a completed session, performs session activity, then verifies the actual database signing secret and any pinned keyed-receipt proof before reading the dataset identity, durable watermark and signed cursor in the same transaction. It validates each retained record and the lookahead, preserves the original payload bytes and SQL-NULL tombstones, and rechecks the session before committing. A trusted caller supplies per-record and per-page source-field byte budgets; the latter is at most 64 MiB for returned records. A lookahead up to the per-record budget, intermediate copies and SQLite allocations are additional, and no future serialized HTTP response is bounded by these limits. A record that cannot fit even on an empty page fails without returning a prefix, and a page may contain fewer than its requested records when the page budget is reached. The reader withholds **every historical search upsert**, including potentially valid ones, until its approved-source provenance can be established; a structurally valid search tombstone contains no text and can be read. It does not prove provenance for other retained kinds, source completeness or a valid bootstrap. This internal reader is not an HTTP endpoint, does not issue a bootstrap cursor or perform retention, and must not be exposed as working synchronization until those gates and wire-size policy are complete.

A separate private version-one HMAC helper binds the exact server instance, dataset epoch, canonical search identity, sequence, revision and original payload bytes using the server secret and a search-specific, length-framed domain. It processes payloads in cancellation-aware chunks, and its caller must retain an unchanged byte slice. The HMAC is only a binding primitive: it does not validate the JSON, prove reviewed source, authenticate a session, check the current database secret, store a proof or authorize a read. No producer or delta reader uses it yet; all historical search upserts remain withheld.

Duplicate/overlapping pages are safe: retain the latest `(epoch, entity_revision)`, including tombstone revisions, so a stale upsert cannot resurrect a deleted row. Deleted or revised media invalidates matching thumbnails/vectors; removing a media link only removes that link.

`min_retained_seq` means the last pruned sequence: a cursor exactly at the floor is valid, while a cursor below it requires bootstrap. Keep at least 30 days of changes by default. Current projections are stored independently of retained history, so log pruning cannot remove records from a future bootstrap.

Wrong epoch, instance, incompatible projection, or an expired retained cursor returns `410 SYNC_RESET_REQUIRED`. A tampered cursor may return 400. Restore/reseed generates a new epoch before accepting any traffic; numeric sequence coincidences never allow an old dataset cursor to pass.

Only one tab coordinates sync, using Web Locks or an expiring IndexedDB lease. Other tabs read committed state. No sync coordinator submits drafts.

## Local privacy, drafts, and models

- Standard mode synchronizes all searchable metadata and approved OCR. Lite mode keeps visited data; complete mode offers opt-in thumbnails/vectors/originals after size estimates.
- Store no authentication/CSRF/worker/cloud secrets in IndexedDB or localStorage. Logout clears private replica/cache state after a draft-loss warning.
- Drafts and attachment blobs are separate stores and survive replica resets, expired snapshots, epoch changes, and compatible schema upgrades. Quota pressure never silently deletes drafts.
- Offline operations are proposals only. On reconnect, show differences, revalidate, and require explicit foreground confirmation through normal domain endpoints with current CSRF, idempotency, and revision preconditions. `/drafts/validate` is advisory and cannot bypass domain validation.
- Model installation is explicit, size-bounded, and SHA-256 verified before promotion. A failed upgrade preserves the previous compatible model. Model key, preprocessing, dimension, normalization, and source image digest must match vectors.
- Local image search reports coverage and cannot assert that an item is absent when vectors/images are missing. Camera capture remains real live capture even offline where the platform permits it.

Service workers cache the app shell and versioned static assets, not protected API responses. Updates wait for explicit approval and preserve unsaved work. Browser storage can be evicted and cannot be remotely wiped while disconnected.
