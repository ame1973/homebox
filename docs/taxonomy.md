# Categories and Tags

**Implementation status:** executable contracts, the authoritative Go/SQLite category/tag transaction services, signed live metadata pages, and dependent item/link/approved-search publication are implemented and tested. HTTP handlers, browser adapters, and settings screens remain pending; the serving router does not expose these taxonomy operations yet. Complete item/stock/media/search/worker workflows and 22-entity sync remain in progress.

## Identity and names

Categories organize items in a parent-linked, acyclic hierarchy. They have independent stable IDs, a nullable parent, and a name. Names need not be unique: identity never depends on a display path or label. Tag names are unique by exact stored text, matching the SQLite binary uniqueness rule. Whitespace, case, and Unicode normalization are preserved rather than silently merged. New IDs are UUIDv4; supported imported IDs are retained.

Category/tag names contain 1–200 Unicode code points; IDs and nonnull parent IDs contain 1–128. Revisions are positive safe integers up to `9007199254740991`. A category response always includes its parent, including explicit null for roots. The version-one offline payloads contain source metadata only; the sync envelope carries their revision.

## Request boundaries

All routes below are relative to `/api/v1` and protected by the same configured Origin, session, mandatory-password-change, CSRF, idempotency, client-version, and admission policy as other domain operations. Describing the contract does not claim a running handler.

| Operation | Contract |
| --- | --- |
| `GET /categories`, `GET /tags` | Live stable-ID pages; limit 1–100, default 100; optional signed cursor up to 2,048 characters; no body |
| `POST /categories` | Required name and optional nullable parent; a child requires the reviewed parent's quoted aggregate revision; a root omits unrelated If-Match |
| `PATCH /categories/{id}` | Nonempty partial name/parent patch and original target proof; omitted parent is unchanged, explicit null moves to root |
| `DELETE /categories/{id}` | Original target proof and no body; delete a leaf, clearing its item classifications rather than deleting inventory |
| `POST /tags` | Required exact unique name |
| `PATCH /tags/{id}` | Required replacement name and original tag proof |
| `DELETE /tags/{id}` | Original tag proof and no body; detach item-tag links and preserve inventory |

Taxonomy write JSON is bounded to 16 KiB, with exact field names, duplicate/Unicode checks, and no null nonnullable fields. Proofs use one quoted positive decimal safe integer, not weak tags, leading zeros, lists, wildcards, or silently rounded values. Lists use principal/epoch/entity-type/page-size-scoped signed continuations, not a frozen/offline snapshot. Authentication and authorization must still be rechecked inside each SQL transaction.

## Hierarchy and concurrency

A category cannot point to itself or a descendant. Reparenting changes a stable parent link, not item identity or holdings. Category aggregate revisions cover changes in the branch and its classification membership. Tag revisions also cover item-tag membership, protecting a reviewed detach/delete scope. Touch affected ancestors once, not every unchanged descendant. New child creation verifies the original parent proof; rename, move, and delete verify the original target proof. Concurrent membership changes must not silently expand an old deletion/reclassification review.

Deletion never implicitly deletes a category subtree. Remove or reparent children explicitly first. Deleting a category leaf clears category references on assigned items atomically. Deleting a tag removes only that tag's item links. Counters/tombstones retain old identities so a later same-name tag with a different ID does not acquire an earlier tag's meaning.

## Transaction service

`store.NewTaxonomy(auth)` binds the module to the existing private authentication store. It offers authenticated category/tag detail and internal bounded stable-ID pages plus create/partial-edit/delete transaction methods. `PageCategories` and `PageTags` return public `data`/`next_cursor` lists; unsigned `NextID` stays internal. HTTP still owns Origin/CSRF/version/admission. Category commands preserve omitted versus explicit-null parents through the internal presence wrapper, never a wire wrapper.

Location, category, and tag pages share one private cursor codec with distinct MAC purposes. Each continuation binds the administrator, dataset epoch, entity/list kind, page size, last ID, and a 15-minute validity window. It contains no session credential, never authorizes itself, and can continue after a fresh completed login by the same principal. Revoked/expired sessions, mandatory password changes, restored epochs, cross-list tokens, altered page sizes, tampering, and noncanonical encodings fail closed. The read and session update commit before a page or continuation is released; a failed read/commit never returns partial metadata. These are live lists: concurrent edits/deletions may change later pages. They are not a substitute for the separate frozen-sync protocol.

The shared mutation runner freezes principal/epoch-scoped replies and immutable events only after every source write, current/outbox projection, and derived FTS write succeeds. Each identity is staged once; final-state search materialization occurs during flush, not when an intermediate source field first changes. Failed statements, late authorization changes, exhausted counters/sequences, or deferred commit failure return no successful DTO/acknowledgement and roll back all dependent rows.

Search materialization reads manual item fields, category ancestry, live tag links, aliases, attributes, barcode identifiers, and permitted purchase notes/merchants. Explicitly reviewed `keyword`/`specification` candidates contribute accepted values or corrected replacements. Pending/rejected entries, missing review decisions, raw OCR/jobs, stale cached text, historical realized-field candidates, and unit-specific identifiers do not become item-level published terms. Confirmed title/brand/model/category/tag values come from their realized source fields, preserving later manual overrides. The worker and query API are not required or implicitly made available by this publisher.

## Projections and approved search

Every source mutation, immutable mutation event, frozen idempotent reply, projection/counter update, dependent search rebuild, and outbox row commits together. No-op metadata changes keep their projection revisions while still receiving a frozen acknowledgement. A successful replay returns the original result, not a new mutation or necessarily the latest resource. Expired claims are never silently reused.

Category changes update category projections and affected item/search projections. Tag renames update their own metadata and affected search text; deleting a tag emits independent item-tag tombstones before rebuilding search. Composite identities are canonical JSON pairs, not delimiter concatenations. No unreviewed OCR, rejected candidate, model job error, or unapproved suggestion may enter a search projection. Confirmed category/tag/title/brand/model values are read from realized source fields, not resurrected from historical candidate labels after a later user override. The AI worker is not required for these ordinary mutations.

Migration 0008 materializes existing category, tag and item-tag metadata under their retained counters, adds parent/reverse-membership indexes, and seeds existing item/search counters. It does not copy unverified legacy search-cache text, emit fabricated mutation changes, rescale stock, or make a complete replica available. Dependent mutations still need authoritative source-derived item/search publication.

A taxonomy backfill is only one part of the required 22-entity frozen-sync implementation. It must not enable an incomplete replica or advertise a full search/worker feature before those services are implemented and tested.

## Verification

```sh
uv run --frozen python -m unittest discover -s tests -v
npm --prefix web run api:generate
npm --prefix web run typecheck
```

Contract tests exercise separate create/partial-patch shapes, nullable parent presence, code-point/identity/revision bounds, exact offline payload compatibility, positive quoted proofs, Origin/body limits, and bounded live pagination. Strict generated-type fixtures reject missing creation names, missing response parents, internal command wrappers, missing Origin, and null tag names. Store tests replay every mutation's frozen changes onto the old current state and require exact equality, verify deep hierarchies/concurrent retries/stale proofs, inject dependent-write/deferred-commit/late-auth failures, and assert approved-only FTS publication without modifying inventory or AI source evidence. HTTP/browser acceptance remains pending; physical NAS/phone acceptance is not implied.

```sh
go -C server test -race -count=1 ./internal/store -run '^TestTaxonomy'
make check-backend
```
