# Location Rules

**Implementation status:** the authoritative Go/SQLite location service and seven location HTTP operations are implemented and tested. Browser screens, location contents/summary/QR routes, and full offline synchronization are separate unfinished features.

## HTTP operations

All paths are relative to `/api/v1`, use the configured same-origin session boundary, and return `Cache-Control: no-store`. Domain writes require exact Origin, current-session CSRF, a supported application version, and an explicit stable idempotency key. Mandatory password change blocks all these operations. A domain reply never sets or clears the session cookie.

| Operation | Request | Successful result |
| --- | --- | --- |
| `GET /locations/tree` | Optional `limit` 1–100 (default 100) and signed `cursor`; no body | Flat parent-linked page, including archived but excluding deleted locations |
| `POST /locations` | JSON name/code and optional metadata; conditional parent `If-Match` | Location and quoted aggregate ETag |
| `GET /locations/{id}` | Active completed session; no body | Current location and ETag |
| `PATCH /locations/{id}` | Nonempty JSON patch and target `If-Match` | Updated location and ETag |
| `POST /locations/{id}/move` | Required nullable `target_parent_id` and target `If-Match` | Moved location and ETag |
| `POST /locations/{id}/archive` | JSON object, optional `archive_children`/reason, target `If-Match` | Archived location and ETag |
| `DELETE /locations/{id}` | Target `If-Match`; no body | Frozen committed acknowledgement with operation ID and tombstone revision |

Missing revision proofs return `409 PRECONDITION_REQUIRED`; stale ones return `409 REVISION_CONFLICT`. Root creation must omit unrelated `If-Match`. Revisions use one quoted positive decimal integer without leading zeros, weak tags, lists, or `*`, and may not exceed `9007199254740991`. Signed-list failures return `400 INVALID_CURSOR`; restart the read rather than altering the token. Other safe domain codes distinguish reserved codes, cycles, archived destinations, unreviewed children, and retained inventory references. See [OpenAPI](../contracts/openapi.yaml) for the exact payloads.

Location JSON is unencoded UTF-8 `application/json`, capped at 128 KiB before parsing. Fields are case-sensitive; duplicate names, invalid UTF-8/surrogates, unknown fields, null nonnullable values, and trailing documents are rejected. Integer-valued decimal/exponent forms such as `1000.00` or `1e3` are accepted exactly, never rounded through floating point. Numeric representations are limited to 128 characters with bounded exponent work; nonintegral or unsafe values are rejected. Authentication retains its separate 16 KiB limit. GET/DELETE bodies and query parameters outside the documented tree pagination are rejected; tree query strings are bounded to 2,300 encoded characters.

Location reads and writes share the existing general 60/minute peer and 600/minute global admission windows; they do not bypass the limiter or consume a separate credential-minting allowance. An active-session middleware check does not replace the second transactional authorization: revocation after middleware but before source mutation still rejects the write.

## Typed browser client

`web/src/lib/api/locations.ts` implements the seven operations through generated OpenAPI paths and strict runtime schemas. It validates safe integer/Unicode bounds, exact resource identity, matching strong ETags, allowed mutation revision transitions, deletion acknowledgements, page sizes, and duplicate IDs. Extra success fields are rejected. It returns one explicit page at a time rather than following continuations invisibly or claiming a frozen tree.

Root/child create overloads make their concurrency difference explicit: root context has no expected revision, while a child requires `LocationWriteContext.expectedRevision` for its reviewed parent. Other writes use the target's original expected revision. Callers supply CSRF, stable idempotency key, and AbortSignal; the adapter never fetches a new proof, chooses a fresh key, rebases, retries, queues, persists, or replays a mutation. Empty versus explicit-null PATCH parents remain distinct, and optional server defaults are not silently inserted into the submitted request.

Read deadlines are eight seconds and write deadlines are 30 seconds. Cancellation, network failure, timeout, or malformed success after sending is an uncertain outcome, not evidence of rollback. An explicit retry retains the original key/body/proof. Conflict/expired-claim codes require current-state review, not automatic new-key resubmission. Safe error normalization is shared with account adapters and retains no arbitrary server text, details, response, or transport cause.

These adapters are not a claim that location browser screens, draft storage, or offline replication are complete.

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

These are live metadata pages, not a frozen offline snapshot. Mutations between pages are reflected in later reads; callers must refresh ordinary tree reads when needed rather than treating them as a certified replica. `GET /locations/tree` exposes this signed protocol; follow the cursor with the same page size.

## Projections and verification

Current projections are independent of retained change history. Pruning the outbox cannot remove a live location from a later bootstrap. Outbox updates and historical event updates/deletes are rejected by database triggers; current payload revisions must match their counters at commit.

Migration 0007 adds description/deletion metadata, current materialization, and immutable mutation audit. Its initial backfill covers existing locations and location movements without rescaling inventory or claiming to publish sync. A complete cross-domain backfill, frozen snapshots, signed cursors, deltas, and tombstone retention are still required before the 22-entity offline replica can be enabled.

Run the actual service/transaction tests:

```sh
go -C server test -race -count=1 ./internal/store -run '^(TestLocation|TestDomain)'
go -C server test -race -count=1 ./internal/httpapi -run '^TestLocation'
make check-backend
```

Tests cover Unicode/safe-integer bounds, parent/subtree revisions, a hierarchy deeper than 1,000 levels, concurrent duplicate creation and opposite moves, stale parent proofs, owner/link tombstones, shared-media preservation, immutable/frozen records, retention independence, all dependent write failures, and deferred commit failure. HTTP checks additionally drive all seven operations through verified TLS/cookie jars, exact acknowledgement replay, signed pagination, safe errors, method/URL/Origin/CSRF/version/admission rules, and revocation after middleware but before the SQL mutation. The compiled CLI/server has also been driven through real loopback sockets with a disposable private database. These automated checks are not location-browser-screen or physical NAS/device acceptance.
