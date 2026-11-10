# Shared Images and Media Links

**Availability: private database groundwork, authenticated store reads and target contracts only.** Migration 0019 preserves retained Media/MediaLink source and builds source-only current projections; existing Item, Unit and Location deletion transactions update affected Media membership. The private Go store can read actual Media and page links for an actual owner or Media, but the Go image-file storage services, media HTTP handlers, browser SDK, gallery, upload controls and camera are not implemented yet. Do not send these example requests to an incomplete installation or treat schemas/database metadata as proof of file safety. Existing catalog, physical holding, purchase and Asset workflows do not depend on an AI worker.

## Identity, ownership and review

A **Media** resource describes one independently versioned image record. New uploads are verified and sanitized; historical records without file-validation evidence are explicitly `unverified`. It is not owned by an Item, Unit or Location. A **MediaLink** is a separate stable identity associating that Media with exactly one actual owner of one of those three kinds. The same Media can have links to several owners. Removing one link must not remove the image, its bytes or another owner's link. Purchase receipt-media workflows are still separate required work; the current three-way owner contract does not pretend that a Purchase is an existing media-link owner.

An `unverified` record preserves its bounded historical MIME, dimensions, byte count and digest as **claims**, not proof that the file exists, matches those values, has a safe format or has had location metadata removed. It may describe a format outside the three supported upload formats. Both thumbnail fields are null, and neither original nor thumbnail content may be streamed on the strength of that record. Migration must not silently mark retained records verified, invent thumbnails or rewrite claims. A later explicit, audited verification/sanitization workflow must inspect the actual file and publish a new Media revision before safe content can be served. A `verified` upload response can contain only JPEG, PNG or WebP, with metadata derived from its actual sanitized bytes. File integrity must still be checked before streaming; a past verification status is not evidence that a file has not since been replaced or corrupted.

A Media revision covers its metadata **and link membership**. Attaching, changing or unlinking a link, including an owner-deletion cascade, must advance the affected Media membership and owner aggregate in the same transaction. This lets a complete Media-before/pages/Media-after review detect an intervening link before global deletion. Existing Item, Unit and Location deletion transactions now validate the complete actual membership of every affected Media, remove only the selected owner's links, emit their independent tombstones and advance each affected Media once. Other owners' links, private bytes and OCR source remain untouched; failure rolls back the owner, counters, current/frozen projections, audit and original reply. Media attach/edit/unlink/global-delete services are still target behavior, not implemented operations. A future global Media delete must remove its actual links and emit their independent tombstones, without immediately erasing a deduplicated object still referenced by another Media.

Link writes retain separate original proofs:

| Target operation | Original review proof | Result |
| --- | --- | --- |
| Attach one owner | Media `If-Match` plus required `owner_revision` in the body | New independent `MediaLink` and its own revision-one ETag |
| Change link metadata | That link's original `If-Match` | Actual independent `MediaLink` and its own ETag |
| Unlink | That link's original `If-Match` | Frozen `ActionAck`; its revision is the link tombstone revision |
| Delete Media globally | Original Media membership `If-Match` after reviewing its complete links | Frozen `MediaDeleteResult`; no claim that background file cleanup finished |

All writes require the configured Origin, an active completed session, CSRF, supported client version and an original idempotency key. Authorization and actual source/proofs must be rechecked inside SQL after body reading. A retry uses the identical original key/body/proofs and returns its original committed result; it never silently fetches another proof, rebases or replays on reconnect. The result is reconciliation evidence, not permission to install an old response as current state. A fresh key against changed membership must fail rather than absorb an unseen owner.

For example, the **future** attach body for an Item, with the separately reviewed Media `If-Match`, is:

```json
{
  "item_id": "reviewed-item-id",
  "owner_revision": 12,
  "is_cover": true,
  "sort_order": 0,
  "description": "Front view"
}
```

Exactly one of `item_id`, `item_unit_id` or `location_id` is required and the others must be absent, not null. Owner identity cannot be reassigned by a link patch. New sort positions are nonnegative safe integers; retained negative positions remain readable. Setting a new cover while another link for that owner is already a cover returns `MEDIA_COVER_CONFLICT`: the user can explicitly clear the earlier cover first. Existing multiple legacy covers remain visible and are never silently rewritten; a value-equal patch is still a no-op receipt. Descriptions are bounded plain text, not executable markup or automatically approved OCR.

## Selected reads and reserved IDs

The private `MediaLibrary` store reads actual Media source and independent counters, not current-projection payloads. It pages actual link rows for one existing Item, Unit, Location or Media, validates related Media and owner authority including lookahead, and returns an explicit empty array and null continuation for a real empty collection. Failed source, authorization or cursor checks return no partial page. A joined entry retains the independently versioned Media and link values; neither a private storage key nor raw OCR is returned. These are store methods, not public HTTP routes, and they neither inspect nor verify image files.

The target HTTP read contract includes:

- `GET /media/{id}` for actual Media metadata and its independent membership ETag.
- `GET /media/{id}/view` for the identical resource, including a retained ID equal to `links`: the path `/media/links` otherwise names the owner-scoped list. This alias is not another resource or proof.
- `GET /media/links?owner_type=item|unit|location&owner_id=...` for that real owner's link page.
- `GET /media/{id}/links` for all owners linked to one real Media, needed before global deletion.
- `GET /media/{id}/content` and `/media/{id}/thumbnail` for authenticated, `no-store`, `nosniff` image streams.

The store's two lists use stable link-ID order, a maximum of 100, required nullable `next_cursor`, and 2048-byte opaque continuations; callers explicitly supply the page size. Their independent signatures bind principal, dataset epoch, collection/owner and page size for 15 minutes. Cursor bodies are signed, not encrypted: they contain the continuation link ID and other paging metadata, but not session credentials or the owner scope. The target HTTP boundary will apply a default of 50 and a 16-KiB raw query limit. Real source, related owners and lookahead are validated before returning any prefix. These are live pages, not frozen sync snapshots or collection ETags. Complete a selected Media's link pages between matching Media reads before deletion. For an owner list, matching owner reads bracket the link collection only: embedded Media metadata can change independently and must be reread under its own proof before a write.

An ETagged `MediaLink` resource includes only the independently versioned link row. List pages instead contain `MediaLinkView` entries: the same flat link fields plus actual separately versioned `media`. This distinction avoids assigning one strong row ETag to response bytes that can change merely because the shared Media changed. Runtime code must check that the embedded Media ID matches `media_id`; JSON Schema cannot compare those fields. A verified thumbnail has both a same-origin derived URL and digest; unavailable or unverified thumbnail metadata has explicit null for both. A schema cannot verify actual bytes, URL identity or that a thumbnail exists. The version-one offline Media payload is unchanged and has no verification field: its retained metadata alone must never be treated as proof of safe bytes. The new source-only backfill sets both thumbnail fields to null; previously frozen or already materialized bytes are preserved exactly, not silently rewritten. Full sync must explicitly validate and reconcile such retained projections before it can expose them, and remains unimplemented.

## File and privacy boundary

The intended upload accepts exactly one JPEG, PNG or WebP multipart file, no client storage path, owner, dimensions or digest. The hard wire ceilings are 32 MiB per file, 32 MiB plus 64 KiB for the complete multipart body, 16,384 pixels per dimension and 64 million pixels; an operator may configure stricter limits. Enforce limits for both declared lengths and streaming/chunked bodies, compare declared MIME to magic bytes, and validate a complete supported decode before publication. Unsupported or unsafe animated variants must be rejected rather than treating an unchecked first frame as validation of the whole file.

Strip GPS and other sensitive location metadata before storage. Visible text or coordinates photographed into pixels are not removed by metadata stripping. The server derives MIME, dimensions, size and SHA-256 from the actual sanitized bytes; a client filename or digest is never authority, and the stored digest need not equal a raw upload's digest. Bind the original idempotency key to a server-keyed digest of the exact raw file bytes and normalized declared MIME, so files with different private metadata cannot become the same request merely because sanitization yields the same bytes. Do not persist raw file metadata or the caller filename in the receipt. A fresh upload of already verified sanitized content can reuse an existing Media with its actual revision, without implicitly attaching it to an owner. A new identity starts at revision one. Thumbnail JPEG bytes have their own digest; perceptual hashes and visual indexing remain separately validated features, not proof that two photos depict the same object.

A safe implementation must use a private staging area, durable file/directory synchronization, atomic promotion under a server-controlled content key, and recovery for crashes on either side of SQLite COMMIT. Source, counters, current/outbox, audit, idempotent response and any pending-cleanup record commit together. File cleanup runs only after commit and rechecks references, so failed writes, original-key reconciliation, duplicate content or a shared object cannot lose data. Metadata, thumbnails, private originals, receipts and credentials must not enter logs or public caches. A service worker must not cache protected API responses or originals; any future opt-in thumbnail cache has its own privacy and purge policy.

## Verification and remaining work

```sh
uv run --frozen python -m unittest tests.test_media_contracts -v
go -C server test ./internal/store -run '^TestMedia' -count=1
npm --prefix web run api:check
npm --prefix web run typecheck
npm --prefix web test -- --coverage.enabled=false src/lib/api/client.test.ts
```

These checks validate the executable schemas, original independent proofs, authenticated private Media/link source reads and complete live pages, safe error-code allowlist, typed owner exclusivity, source-only migration and shared-owner deletion/rollback/replay without changing version-one offline payload shapes. They do **not** decode images, create files, validate GPS removal, exercise a media HTTP boundary, prove file crash recovery or run a real camera. Media file storage, HTTP/SDK/browser workflows, receipt attachments, offline thumbnails, search/AI integration and physical-device acceptance remain required work.
