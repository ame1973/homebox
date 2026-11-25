# Location Rules

**Implementation status:** the authoritative Go/SQLite service, nine HTTP operations, and responsive location-management screens, including the direct/subtree summary and paged contents, are implemented. The QR endpoint, location history UI, saved photo uploads, persistent drafts, and full offline synchronization remain unfinished.

## Browser usage

Sign in and complete any mandatory password change first. The same routes and controls are available on phone, tablet, and desktop:

| Route | Purpose |
| --- | --- |
| `/locations` | Browse, expand/collapse, search names/codes, and optionally show archived locations |
| `/locations/new` | Create a root location or explicitly choose an active parent |
| `/locations/new?parent=ID` | Start a child under the selected parent, with its reviewed aggregate revision |
| `/locations/ID` | Read metadata, direct children, a direct/subtree summary and explicit contents pages; start move, archive, restore, or delete |
| `/locations/ID/edit` | Edit descriptive metadata without implicitly changing parent or status |
| `/l/ID` | Open a protected relative alias, returning to the location after login |

Supported imported IDs matching the reserved create segment `new` (including case variants) use `/locations/ID/view` for detail, without rewriting their stored identity. Ordinary links and aliases choose this route automatically. URLs preserve only whitelisted relative location destinations across login; they cannot redirect to another host or carry arbitrary query data. QR generation is still pending; the alias is not a claim that printable labels already exist.

### Browse and choose parents

The screen follows every signed page before publishing a complete, validated hierarchy. Duplicate identities, repeated cursors, orphans, and cycles are rejected rather than displayed as invented roots. Traversal is iterative with no hierarchy-depth cutoff. Visual indentation is capped for narrow screens; actual depth, direct parent, and full expandable ancestry remain available. Search preserves matching ancestor paths; its expansion controls are disabled while search determines visibility. Displayed totals count location records, never mixed-unit inventory quantities.

Parent selection uses native controls plus name/code filtering and a full-path disclosure. Only active locations with active ancestors are offered. Moving excludes the selected branch. An unavailable prior choice stays visibly unavailable, not silently changed to root. A changed creation-parent revision requires an explicit recheck before submission. These are live reads, not a frozen/offline replica: inconsistent list/detail revisions disable writes and request a fresh read.

### Read the location summary

The location detail page shows direct and subtree individual Unit counts, a subtree count of distinct Items, and separate direct/subtree quantity groups by base unit. These are recorded holdings, not proof of physical presence: lost or retired Units retain their recorded location, while disposed Units and zero Stock do not count as present. A quantity is never added to an unlike unit or silently converted. Open the separate holdings screens for actual Unit or Stock operations; the summary has no inventory write action.

The page reads a live summary after a completed-session check and compares its aggregate Location revision with the separately read current Location. A mismatch, failed read, offline state, or ongoing session/location revalidation hides the numbers rather than presenting a partial or stale total. Use **Refresh location summary** to explicitly recheck the session and location metadata before reading again. These independent live reads are not a frozen snapshot, and the displayed values can change afterward.

A delivered session-clear signal cancels and removes this panel's summary read. Before browser page suspension the panel is redacted; after back-forward-cache restoration, explicitly confirm the session and continue. This cleanup applies to the summary panel, not all other routes or offline storage; cross-window signals may not arrive if both supported browser transports fail.

### Browse location contents

On a location detail, choose **Read location contents** or select a different scope. The direct scope lists Units and Stock assigned to this location plus its immediate child locations; the subtree scope includes holdings at this location and every nondeleted descendant. The screen requests one combined page of at most 20 records at a time. Use **Next contents page** to read another page; **Previous contents page** shows an already-read page held only in this screen's memory. Changing scope or choosing **Restart from the first page** begins a new explicit read. No paging action writes inventory, creates holdings, or automatically opens an Item.

Each Unit shows its exact asset code, independent Unit and Item IDs, recorded location, status, serial and notes. Stock shows its exact Item/location IDs, integer base quantity and the Item's unit conversion; different units are never combined. Sub-location records show their own ID, parent, name, code and status. The contents endpoint does not provide Item names; IDs are not guessed from other data. Quoted text preserves control and invisible-format characters visibly. Safe Item, Unit and Location IDs offer their existing detail routes; legacy IDs that cannot be addressed safely remain visible without a misleading link.

An empty first page means no records were returned for that checked scope; an empty continuation does not erase earlier results. A continuation notice means later records have not yet been read. Pages are live reads, not a frozen or physically verified collection: disposed Units and zero Stock remain as records, and a lost, lent or retired Unit's recorded location does not prove where it is. Before each new page the screen rechecks the completed session and location, and compares the returned aggregate revision with the independently read Location. It withholds a changed, duplicate, cyclic, failed or incomplete page rather than displaying it as empty. A changed or expired continuation requires an explicit restart from page one. Previous in-memory pages are not re-read when navigating back and can become outdated after the latest check.

Offline, parent-read failure, session revalidation and a mismatched aggregate revision hide page data. Reconnection never automatically resumes a contents read: explicitly restart after reconnecting. A delivered session-clear signal aborts the active contents request and clears this panel's in-memory pages. Before page suspension the panel is redacted; after back-forward-cache restoration, explicitly confirm the session and read the first page again. This does not promise memory erasure, clear other routes or offline storage, or guarantee cross-window delivery when both browser signal transports fail.

### Create, edit, and review

1. Create a room/container with a unique human code, choose a common or custom kind, and optionally enter signed integer ordering and a description. Original whitespace/Unicode is preserved. The form rejects decimal, exponent, grouped, or unsafe ordering text before numeric conversion.
2. Review the proposed values. The review starts keyboard focus at its visible static heading so a long review does not focus an offscreen footer. No domain request is sent until explicit confirmation.
3. Confirm once. The page obtains current-session CSRF, sends the frozen key/body/original revision, and waits for the authoritative acknowledgement. Repeated activation, reconnection, and timers do not create another write.
4. On success, fresh reads populate the destination page. A frozen replay response is never installed as if it were the latest resource.

Browser location mutations invalidate location and dependent catalog/holding/purchase/Asset/Media read scopes after confirmation or a sent failure: moving a branch can change subtree-filter membership even though actual holding assignments remain unchanged. This refresh never installs a frozen response or broadly invalidates unrelated authentication/taxonomy state.

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
| `GET /locations/{id}/contents` | Optional `scope` (`direct`/`subtree`, default `subtree`), `limit` 1–100 (default 50) and signed `cursor`; no body | One combined live page of Units, Stock and sub-locations, with the actual Location aggregate ETag |
| `GET /locations/{id}/summary` | Active completed session; no body or query | Current direct/subtree recorded holding counts and base-unit groups, with the actual Location aggregate ETag |
| `PATCH /locations/{id}` | Nonempty JSON patch and target `If-Match` | Updated location and ETag |
| `POST /locations/{id}/move` | Required nullable `target_parent_id` and target `If-Match` | Moved location and ETag |
| `POST /locations/{id}/archive` | JSON object, optional `archive_children`/reason, target `If-Match` | Archived location and ETag |
| `DELETE /locations/{id}` | Target `If-Match`; no body | Frozen committed acknowledgement with operation ID and tombstone revision |

Missing revision proofs return `409 PRECONDITION_REQUIRED`; stale ones return `409 REVISION_CONFLICT`. Root creation must omit unrelated `If-Match`. Revisions use one quoted positive decimal integer without leading zeros, weak tags, lists, or `*`, and may not exceed `9007199254740991`. Signed-list failures return `400 INVALID_CURSOR`; restart the read rather than altering the token. Other safe domain codes distinguish reserved codes, cycles, archived destinations, unreviewed children, and retained inventory references. See [OpenAPI](../contracts/openapi.yaml) for the exact payloads.

Location JSON is unencoded UTF-8 `application/json`, capped at 128 KiB before parsing. Fields are case-sensitive; duplicate names, invalid UTF-8/surrogates, unknown fields, null nonnullable values, and trailing documents are rejected. Integer-valued decimal/exponent forms such as `1000.00` or `1e3` are accepted exactly, never rounded through floating point. Numeric representations are limited to 128 characters with bounded exponent work; nonintegral or unsafe values are rejected. Authentication retains its separate 16 KiB limit. GET/DELETE bodies and undocumented query parameters are rejected. Tree query strings are bounded to 2,300 encoded bytes; contents queries allow at most 16 KiB raw query and 4,096 decoded UTF-8 cursor bytes.

### Current recorded-holdings summary

`GET /locations/{id}/summary` counts the actual target and all nondeleted descendants in one completed-session SQLite transaction after session activity. Direct and subtree Unit counts include retained `stored`, `in_use`, `lent`, `lost` and `retired` Units at their actual locations, but exclude `disposed` Units. Positive quantity Stock is grouped by its exact `base_unit`, without conversion or a mixed-unit scalar. A retained zero balance is validated but does not count as present. The subtree distinct-Item count deduplicates an Item across locations and holdings. Empty quantity groups are explicit arrays, and positive groups are ordered by their ASCII base-unit spelling.

The read validates the actual location ancestry/subtree, all matching Unit and Stock rows, their Item ownership and retained independent counters, including disposed Units and zero balances. A missing or invalid source, cycle, grouped sum above `9007199254740991`, or more than 500 distinct positive base units returns a safe error without a partial summary. The 500-group limit is on each summary, not the number of Items or Stock rows. The response's strong ETag is the actual aggregate Location revision for that live read, not a Unit or Stock write proof; later changes require a fresh read. This is not a frozen snapshot, inventory write, complete offline replica or proof of physical presence. The separately paged contents screen includes retained disposed and zero rows rather than using the summary as its source.

Location reads and writes share the existing general 60/minute peer and 600/minute global admission windows; they do not bypass the limiter or consume a separate credential-minting allowance. An active-session middleware check does not replace the second transactional authorization: revocation after middleware but before source mutation still rejects the write.

### Current location contents

`GET /locations/{id}/contents` returns a bounded combined page of Units, Stock and sub-locations in that order. `scope=direct` selects holdings assigned to the target and its immediate nondeleted children; `scope=subtree` selects the target and all nondeleted descendants. Retained disposed Units and zero Stock remain visible as records, even though they do not count as present in the summary. Every returned row and its lookahead are checked against actual source and independent counters; subtree reads also validate the actual hierarchy. The three required arrays share the requested total page-size bound, and a non-null `next_cursor` indicates a full page with more results.

The signed continuation is scoped to the administrator, dataset epoch, exact Location and scope, page size and aggregate Location revision; a changed branch returns `INVALID_CURSOR` and requires a fresh first page. It is not encrypted and is not a credential. The strong ETag is the Location aggregate revision, not a Unit or Stock proof. SQL reads after session activity and rechecks completed-session authority before commit. This is a live read rather than a frozen snapshot or proof of physical presence. The direct scope is verified against each returned row by the SDK; for a subtree page, ancestry remains server-authoritative and is not independently established by that page alone.

Before claiming a complete collection, a caller must follow and validate every page, handle a changed revision by explicitly restarting, and never treat a failed page as an empty collection. The screen exposes one page at a time with explicit continuation and does not treat retained pages as a frozen snapshot. Large-subtree performance still requires measurement on the target NAS.

## Typed browser client

`web/src/lib/api/locations.ts` implements the nine operations through generated OpenAPI paths and strict runtime schemas. It validates safe integer/Unicode bounds, exact resource identity, matching strong ETags, allowed mutation revision transitions, deletion acknowledgements, page sizes, and duplicate IDs. The stateless `fetchLocationSummary` also checks the exact requested identity, required arrays, canonical safe Location ETag, sorted independent base-unit groups, direct-versus-subtree consistency and counts without retaining unexpected raw keys. `fetchLocationContents` validates one explicit mixed page, exact scope, a combined page bound, unique row identities, required nullable continuation, direct-scope relationships and raw keys including nested `__proto__`. Both return `{ value, revision }` with the actual Location aggregate revision, not a Unit or Stock proof. Extra success fields are rejected. The adapter returns one explicit list page at a time rather than following continuations invisibly or claiming a frozen tree.

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

An exact retry binds operation, typed input, target, and original revision preconditions and returns the original acknowledgement even if the resource later changes or is deleted. Changed requests conflict. The Go location create, patch and move services snapshot caller-owned optional pointers before validation and fingerprinting, so later pointer changes cannot alter the applied decision. Callers must not mutate shared input concurrently with the call without synchronization; copying does not make a Go data race safe. Domain replies last 30 days; an expired claim returns `IDEMPOTENCY_EXPIRED`, is never automatically reused, and requires explicit current-state review before a new key. Removing old response bytes must retain the claim rather than turn a delayed retry into another mutation.

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

Tests cover Unicode/safe-integer bounds, parent/subtree revisions, a hierarchy deeper than 1,000 levels, concurrent duplicate creation and opposite moves, stale parent proofs, owner/link tombstones, shared-media preservation, immutable/frozen records, retention independence, all dependent write failures, and deferred commit failure. HTTP checks drive the seven metadata operations through verified TLS/cookie jars, exact acknowledgement replay, signed pagination, safe errors, method/URL/Origin/CSRF/version/admission rules, and revocation after middleware but before SQL. Separate summary tests use actual Unit/Stock sources, deep paths, retained zeros and disposed Units, group overflow, source/counter corruption and late-session/commit failures; the protected HTTP tests cover methods, bodies and revocation during body reading. Contents tests exercise bounded mixed pages, exact legacy identities, revision/scope-bound cursors, invalid lookahead and query parsing; a private actual-Go browser SDK fixture follows 618 retained Unit/Stock/location rows across seven pages, including a fresh session and path-unaddressable identities. Actual-Go browser tests exercise the SDK, rendered summary and contents screens through real holding changes, scoped mixed pages, exact legacy IDs, altered duplicate pages, concurrent revision mismatches, subtree movement, offline and failed reads, both cross-window logout transports, actual BFCache restoration, focus and enlarged light/dark accessibility. The isolated metadata browser suite also exercises actual creation/edit/move/archive/restore/delete, reserved-code rejection, stale-version reconciliation, an actually committed write with a malformed response followed by explicit same-key retry, deep links through real login and long paths. These are automated checks in five emulated layouts, not physical NAS/iPhone/Android acceptance.

```sh
npm --prefix web test -- --coverage.enabled=false src/features/locations
make build-web
npm --prefix web run test:e2e -- tests/e2e/location-contents.spec.ts tests/e2e/location-contents-bfcache.spec.ts tests/e2e/location-contents-api.spec.ts tests/e2e/location-summary-api.spec.ts tests/e2e/location-summary.spec.ts tests/e2e/location-summary-bfcache.spec.ts tests/e2e/locations.spec.ts --workers=1
```
