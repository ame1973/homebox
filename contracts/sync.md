# Offline Replica and Safe Draft Protocol

SQLite is authoritative. IndexedDB is a disposable per-device read replica, not a backup or an offline write authority. Protocol projection version is `1` for the first application release.

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

Derived effects matter: changing category/tag/alias/purchase/review data may change item search; moving a unit changes its location evidence; deleting an owner emits media-link tombstones. Paths are derived from parent links rather than copied into every descendant projection. Unreviewed OCR and rejected suggestions never enter published search documents.

## Financial correction effects

The [audited financial correction contracts](../docs/financial-corrections.md) define current accounting effects without another replica kind. Allocation correction retains the original posted Line and may create a new independent replacement, but publishes an old-Line tombstone so retained audit source cannot resurrect a voided current attribution. Disposal correction retains the first posted decision and every immutable correction, publishing only the actual current financial decision or a tombstone for explicit void. An intentional zero-valued current decision remains an upsert, not a void.

Existing `purchase_line`/`disposal` identities, version-one payload shapes and the 22-kind envelope remain unchanged. Source, correction/audit, real independent revisions, affected financial/search scopes, frozen upserts/tombstones and original response must commit atomically. Never rebuild effective values from current/FTS caches or overwrite old frozen bytes. Detailed authenticated correction history is not a second offline authoritative ledger; physical status/placement and movements are not undone by financial void. Migration 0017 and private Purchase correction/history services implement the allocation source/current-effect boundary, including validated void gaps and fail-closed damaged context. Disposal correction services, all five new HTTP/browser workflows and the full frozen sync protocol remain unimplemented; partial publication is not sync readiness.

## Frozen bootstrap

`GET /sync/bootstrap` starts a short-lived materialized snapshot. In one immediate transaction, read server identity, dataset epoch, projection version, and change-log high watermark `H`, then materialize all current eligible projections in deterministic `(entity_type, entity_id)` order. Paginated responses read only these frozen rows, never live joins.

Snapshot cursors bind server instance, epoch, projection version, snapshot ID, next ordinal, and expiry with an HMAC or an equally strong server-side token. The default lifetime is 15 minutes. Snapshot expiry returns `410 SNAPSHOT_EXPIRED`; do not garbage-collect unexpired snapshot pages.

The browser stores each page and its cursor atomically in a staging namespace. Partial bootstrap data is not presented as a complete replica. The final page has `complete=true`, `next_cursor=null`, and a server-issued `delta_cursor` for `H`. Publish staging and this cursor in one IndexedDB transaction. Never construct signed cursors in the browser.

Changes after `H`, including deletions, are consumed through delta sync. An expired bootstrap discards staging only, preserving the previous published replica and all drafts.

## Ordered deltas and retention

`GET /sync/changes?after=...` returns frozen change records in sequence order, a high watermark, a next cursor, and `has_more`. Read the JSON stored at commit time, not current mutable rows. Empty pages retain the cursor's sequence. Apply all rows, dependent invalidations, and cursor advancement in one IndexedDB transaction.

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
