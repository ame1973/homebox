# Location Rules

**Implementation status:** the authoritative Go/SQLite service, seven HTTP operations, and responsive location-management screens are implemented and tested. Location contents/summary/QR endpoints, content/photo/history UI, persistent drafts, and full offline synchronization are separate unfinished features.

## Browser usage

Sign in and complete any mandatory password change first. The same routes and controls are available on phone, tablet, and desktop:

| Route | Purpose |
| --- | --- |
| `/locations` | Browse, expand/collapse, search names/codes, and optionally show archived locations |
| `/locations/new` | Create a root location or explicitly choose an active parent |
| `/locations/new?parent=ID` | Start a child under the selected parent, with its reviewed aggregate revision |
| `/locations/ID` | Read metadata/direct children and start move, archive, restore, or delete |
| `/locations/ID/edit` | Edit descriptive metadata without implicitly changing parent or status |
| `/l/ID` | Open a protected relative alias, returning to the location after login |

Supported imported IDs matching the reserved create segment `new` (including case variants) use `/locations/ID/view` for detail, without rewriting their stored identity. Ordinary links and aliases choose this route automatically. URLs preserve only whitelisted relative location destinations across login; they cannot redirect to another host or carry arbitrary query data. QR generation is still pending; the alias is not a claim that printable labels already exist.

### Browse and choose parents

The screen follows every signed page before publishing a complete, validated hierarchy. Duplicate identities, repeated cursors, orphans, and cycles are rejected rather than displayed as invented roots. Traversal is iterative with no hierarchy-depth cutoff. Visual indentation is capped for narrow screens; actual depth, direct parent, and full expandable ancestry remain available. Search preserves matching ancestor paths; its expansion controls are disabled while search determines visibility. Displayed totals count location records, never mixed-unit inventory quantities.

Parent selection uses native controls plus name/code filtering and a full-path disclosure. Only active locations with active ancestors are offered. Moving excludes the selected branch. An unavailable prior choice stays visibly unavailable, not silently changed to root. A changed creation-parent revision requires an explicit recheck before submission. These are live reads, not a frozen/offline replica: inconsistent list/detail revisions disable writes and request a fresh read.

### Create, edit, and review

1. Create a room/container with a unique human code, choose a common or custom kind, and optionally enter signed integer ordering and a description. Original whitespace/Unicode is preserved. The form rejects decimal, exponent, grouped, or unsafe ordering text before numeric conversion.
2. Review the proposed values. The review starts keyboard focus at its visible static heading so a long review does not focus an offscreen footer. No domain request is sent until explicit confirmation.
3. Confirm once. The page obtains current-session CSRF, sends the frozen key/body/original revision, and waits for the authoritative acknowledgement. Repeated activation, reconnection, and timers do not create another write.
4. On success, fresh reads populate the destination page. A frozen replay response is never installed as if it were the latest resource.

Browser location mutations invalidate location and dependent catalog/holding/purchase/Asset read scopes after confirmation or a sent failure: moving a branch can change subtree-filter membership even though actual holding assignments remain unchanged. This refresh never installs a frozen response or broadly invalidates unrelated authentication/taxonomy state.

Metadata editing submits only changed descriptive fields. Moving and activation status use their separate reviewed actions. A concurrent revision does not overwrite input or silently update the form's base: read current data, then explicitly retain only edited logical fields against the new base or discard the draft. Retaining edits also preserves unfinished invalid text for validation; untouched concurrent names/types/order are not overwritten accidentally. An unchanged form sends no mutation.

### Move, archive, restore, and delete

- Moving changes only the branch's parent. Review the original and proposed parent; it never reassigns stock or individual units to the outer destination.
- Archiving a branch with active descendants requires a separately unchecked, explicit subtree selection. The optional reason is preserved. The server still rejects retained live holdings or a stale aggregate proof.
- Restoring activates only the reviewed node, not all descendants. Archived ancestor paths must be restored first.
- Deleting is a separately emphasized confirmation showing the exact name/code/revision. Only an unused leaf is accepted. Shared image bytes, reserved codes/identities, and historical events are not erased.

### Failures, uncertainty, and volatile inputs

Known validation/state conflicts remain editable after dismissing the failed review; new content requires another explicit review. A sent timeout, cancellation, malformed reply, unknown error, or server failure is uncertain: stopping the wait does not cancel a server commit. The original request remains in memory, fields are locked, and its key can be inspected. Read current data, then explicitly retry the exact original request if appropriate; CSRF may be renewed, but body/key/revision are not changed. Closing the dialog retains this uncertain request and exposes read/reopen actions. Expired or conflicting keys cannot be silently reused; inspect current state before explicitly leaving/discarding that volatile screen and reviewing a genuinely new operation.

Dirty browser Back/SPA navigation and reload require a warning. Choosing to stay preserves input and resumes the same reviewed or pending request; choosing to leave abandons that screen and aborts local waiting, not a committed mutation. Escape during a pending review stops local waiting without dismissing the uncertain result or issuing another request. Offline input is volatile only: there is no persistent draft queue or offline replica yet, and reconnection refreshes reads without replaying writes. Backgrounding/offline/unmount interrupts active writes, and session loss removes private cached location data and the form. The protected return URL contains no draft or credentials.

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

The adapters and screens do not provide persistent draft storage or offline replication.

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

Tests cover Unicode/safe-integer bounds, parent/subtree revisions, a hierarchy deeper than 1,000 levels, concurrent duplicate creation and opposite moves, stale parent proofs, owner/link tombstones, shared-media preservation, immutable/frozen records, retention independence, all dependent write failures, and deferred commit failure. HTTP checks drive all seven operations through verified TLS/cookie jars, exact acknowledgement replay, signed pagination, safe errors, method/URL/Origin/CSRF/version/admission rules, and revocation after middleware but before SQL. Frontend tests cover complete/invalid graph reads, original-text forms, explicit reconciliation, frozen command dispatch, dirty navigation, parent selection, protected returns, and interruption without automatic replay. The isolated real-server browser suite exercises actual creation/edit/move/archive/restore/delete, reserved-code rejection, stale-version reconciliation, an actually committed write with a malformed response followed by explicit same-key retry, deep links through real login, light/dark accessibility, long paths and 200% text across five emulated layouts. These are automated checks, not physical NAS/iPhone/Android acceptance.

```sh
npm --prefix web test -- --coverage.enabled=false src/features/locations
make build-web
npm --prefix web run test:e2e -- tests/e2e/locations.spec.ts
```
