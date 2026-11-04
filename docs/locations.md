# Location Rules

**Implementation status:** the authoritative Go/SQLite location service is implemented and tested. Location HTTP handlers and browser screens are not connected yet. The routes in OpenAPI specify their eventual interface; they are not working HTTP examples. Full offline synchronization is also a separate unfinished feature.

## Identity and hierarchy

A location has a stable UUID, a unique human-readable code, a name, a parent or root position, a kind, a signed sort order, a description, and an active/archived state. New locations default to `kind=other`, `status=active`, and `sort_order=0`. Names, descriptions, whitespace, and Unicode normalization are preserved rather than silently rewritten.

Bounds count Unicode code points: name 1–200, code 1–100, kind 1–32, and description 0–10,000. Sort order is an integer between `-9007199254740991` and `9007199254740991`. IDs may also represent supported imported rows; newly generated IDs are UUIDv4.

The parent graph is arbitrarily nested and must remain acyclic. A location cannot contain itself or one of its ancestors. Paths are derived from parent links, never copied into every descendant.

Moving a box changes that box's parent link. It does not change its own identity, the locations assigned to its contents, stock quantities, or individual units' locations. A separately identified, immutable movement event preserves the original parent, destination parent, and time. Moving back appends another event; it does not rewrite the first one.

New children and moved/restored locations require active destination ancestors. Archived locations remain readable, and their descriptive metadata can still be maintained without silently restoring them.

## Optimistic concurrency

Each location exposes an aggregate revision. Editing a descendant or changing branch membership advances the affected ancestors as well as the changed location. Shared ancestors advance only once per transaction. A moved location does not force unchanged descendants to receive new projections.

This distinction protects subtree confirmations: someone viewing a branch cannot accidentally archive a child added after that branch was reviewed. The original branch revision is now stale and the operation conflicts.

- Root creation has no parent precondition.
- Child creation requires the current parent's quoted aggregate revision in `If-Match`.
- Edit, move, archive, and delete require the target location's current quoted revision.
- Omitted PATCH fields are unchanged. An explicit `parent_id: null` moves to the root.
- An actual no-op does not invent a projection revision, movement, or catalog invalidation. Its accepted request still has an immutable audit and idempotent acknowledgement.

Conflicts require a fresh read and explicit review. A stale request is never silently rebased.

## Archival and restoration

Archival hides an empty location from ordinary active workflows without destroying identity or history.

- A subtree containing positive stock or any nondisposed individual unit cannot be archived. Retired, lost, or lent units still count as retained inventory; move or resolve them first.
- Active descendants require explicit `archive_children=true`. PATCH with `status=archived` never implies that confirmation.
- Already archived descendants need not be archived again.
- A disposed unit or zero balance permits archival, because historical/zero metadata can remain attached to the archived location.
- Restore with `status=active` only beneath active ancestors. Restoring a parent does not implicitly restore all children.
- The optional archival reason is preserved in immutable mutation audit.

## Deletion and shared photos

Delete is limited to unused leaves. All nondeleted children block it. Any unit or stock row also blocks deletion, including disposed units and zero balances, because those rows still publish a location reference. Archive such locations instead of creating dangling offline metadata.

An accepted deletion removes the location from ordinary reads, lists, and current projections, and emits a revisioned tombstone. Its source row is retained privately for immutable historical references. The ID and code remain reserved; a printed label must not silently acquire another location's meaning.

Owner photo links are removed with their own stable-ID tombstones in the same transaction. Shared media and other owners' links remain intact. Deleting an owner is not permission to delete the shared image bytes.

## Atomicity and explicit retries

A mutation validates an active, completed, epoch-bound administrator session inside its immediate SQLite transaction. The source changes, immutable movement/mutation events, projection revisions, independently stored current payloads, frozen outbox rows, catalog invalidation, and acknowledged response either commit together or all roll back. Data and acknowledgements are not returned before successful commit. Deferred commit failures are tested separately from statement failures.

Domain idempotency keys are scoped to the administrator and dataset epoch, rather than one login session. A fresh valid session can reconcile an earlier result. Revoked, expired, wrong-epoch, and mandatory-password-change sessions cannot retrieve domain acknowledgements. Authentication actions retain their separate session-scoped security protocol.

An exact retry binds operation, typed input, target, and original revision preconditions and returns the original acknowledgement even if the resource later changes or is deleted. Changed requests conflict. Domain replies last 30 days; an expired claim returns `IDEMPOTENCY_EXPIRED`, is never automatically reused, and requires explicit current-state review before a new key. Removing old response bytes must retain the claim rather than turn a delayed retry into another mutation.

Timeout or cancellation after sending does not prove rollback. No client should automatically replay, retry with a new key, or infer success from a local optimistic view.

## Ordinary list pagination

The location-list service returns stable-ID pages of at most 100 records and an opaque HMAC-signed continuation. Each cursor binds the administrator, dataset epoch, page size, final ID, and a 15-minute expiry in a dedicated signing namespace. Changing a token, page size, principal, or dataset invalidates it. A fresh active session for the same principal may continue the read; the cursor is not a credential and cannot reactivate a revoked session. It contains no raw cookie/password, and private signing key material never leaves SQLite/server memory.

These are live metadata pages, not a frozen offline snapshot. Mutations between pages are reflected in later reads; callers must refresh ordinary tree reads when needed rather than treating them as a certified replica. HTTP integration remains pending at this stage.

## Projections and verification

Current projections are independent of retained change history. Pruning the outbox cannot remove a live location from a later bootstrap. Outbox updates and historical event updates/deletes are rejected by database triggers; current payload revisions must match their counters at commit.

Migration 0007 adds description/deletion metadata, current materialization, and immutable mutation audit. Its initial backfill covers existing locations and location movements without rescaling inventory or claiming to publish sync. A complete cross-domain backfill, frozen snapshots, signed cursors, deltas, and tombstone retention are still required before the 22-entity offline replica can be enabled.

Run the actual service/transaction tests:

```sh
go -C server test -race -count=1 ./internal/store -run '^(TestLocation|TestDomain)'
make check-backend
```

Tests cover Unicode/safe-integer bounds, parent/subtree revisions, a hierarchy deeper than 1,000 levels, concurrent duplicate creation and opposite moves, stale parent proofs, owner/link tombstones, shared-media preservation, immutable/frozen records, retention independence, all dependent write failures, and deferred commit failure. These are automated database/service checks, not location HTTP/browser or physical NAS/device acceptance.
