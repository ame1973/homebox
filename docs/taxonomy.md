# Categories and Tags

**Implementation status:** responsive settings/category/tag screens, the eight category/tag HTTP operations below, stateless typed browser adapters, signed live metadata pages, authoritative Go/SQLite transaction services, and dependent item/link/approved-search publication are implemented and tested. Complete item/stock/media/search/worker workflows and 22-entity sync remain in progress.

## Identity and names

Categories organize items in a parent-linked, acyclic hierarchy. They have independent stable IDs, a nullable parent, and a name. Names need not be unique: identity never depends on a display path or label. Tag names are unique by exact stored text, matching the SQLite binary uniqueness rule. Whitespace, case, and Unicode normalization are preserved rather than silently merged. New IDs are UUIDv4; supported imported IDs are retained.

Category/tag names contain 1–200 Unicode code points; IDs and nonnull parent IDs contain 1–128. Revisions are positive safe integers up to `9007199254740991`. A category response always includes its parent, including explicit null for roots. The version-one offline payloads contain source metadata only; the sync envelope carries their revision.

## Settings workflow

Open `/settings` after completing the initial password rotation. The same category/tag capabilities are available on phone, tablet and desktop:

1. Choose categories (`/settings/categories`) or tags (`/settings/tags`). Lists read every signed page before displaying a checked hierarchy/index. Search changes only the view; category matches retain their ancestor path. Expand/collapse is explicit, without a depth limit or silently truncated first page.
2. Create a root category or tag from the list. To create a child category, use its parent's add-child action or choose a native searchable parent in the form. Nonroot creation retains the reviewed parent's aggregate revision; a live parent change requires explicit rechecking before submission.
3. Open an edit link to rename a tag or change a category name/parent. Self/descendant category destinations are excluded, full selected ancestry remains available in native details, and unavailable selections never become root silently. Only changed fields are sent; choosing root sends explicit `parent_id: null`. Imported IDs such as `new` and `tree` use an explicit `/edit` suffix, distinct from creation routes.
4. Review the exact proposed fields, original target/parent proof and stable identity before confirming. Category deletion is enabled only for known leaves and clears item classifications, not inventory. Tag deletion detaches only its links. The UI explains these effects but does not invent assignment/holding impact counts. Category assignment is available in the [catalog metadata editor](catalog.md), and complete item Tag membership in the [owned maintenance screens](catalog-links.md).
5. When current source metadata changes, the form retains its original draft/proof. Explicitly choose either to retain only fields actually edited while adopting untouched current fields, or discard the draft and load the current record. Neither refresh nor reconnect rebases/submits a write automatically.

A pending action excludes duplicates. Backgrounding/offline/route exit can abort local waiting, not undo a server commit. An unknown sent result locks the original proposal and exposes its request key, a read-only current-state check and an explicit identical-request retry. If a deletion committed but its reply was malformed, disappearance from the live list does not erase that retained proposal or fabricate a new acknowledgement. A successful frozen reply invalidates taxonomy and dependent catalog/holding/purchase/Asset reads and is never installed as current metadata; sent failures also refresh those read scopes without modifying the original command. Unrelated location/auth scopes are not broadly invalidated. Escape interrupts pending waiting or closes an unsent review; long reviews start at visible static content and use one keyboard-accessible outer scrolling region.

Drafts and retained proposals are still volatile, not persistent offline drafts. Dirty/Back/reload guards warn before leaving. Do not reload/close an uncertain request assuming it failed; reconcile it explicitly first. Offline-first visits do not invent empty data, cached read errors disable writes, and active/mandatory-password-change/session checks remain server-authoritative. Known session loss clears private read/mutation state; login returns only to whitelisted implemented settings/location/catalog routes.

Settings currently links the real account, category and tag workflows and displays the actual client version. Item classification and complete Tag membership are available through the catalog screens, with explicit original Item proofs and selected-label review. System/backup administration, installable/offline behavior and persistent draft recovery remain separate product work; no fake counts, disabled decorative routes or worker dependency is added.

## Request boundaries

All routes below are relative to `/api/v1` and protected by the same configured Origin, session, mandatory-password-change, CSRF, idempotency, client-version, and admission policy as other domain operations. They are available through the Go server and the settings workflows above. Resource writes return a strong quoted revision ETag. Live lists and deletion acknowledgements do not invent a resource ETag, and no taxonomy response changes the session cookie.

| Operation | Contract |
| --- | --- |
| `GET /categories`, `GET /tags` | Live stable-ID pages; limit 1–100, default 100; optional signed cursor up to 2,048 characters; no body |
| `POST /categories` | Required name and optional nullable parent; a child requires the reviewed parent's quoted aggregate revision; a root omits unrelated If-Match |
| `PATCH /categories/{id}` | Nonempty partial name/parent patch and original target proof; omitted parent is unchanged, explicit null moves to root |
| `DELETE /categories/{id}` | Original target proof and no body; delete a leaf, clearing its item classifications rather than deleting inventory |
| `POST /tags` | Required exact unique name; omit unrelated If-Match |
| `PATCH /tags/{id}` | Required replacement name and original tag proof |
| `DELETE /tags/{id}` | Original tag proof and no body; detach item-tag links and preserve inventory |

Taxonomy write JSON is bounded to 16 KiB, with exact field names, duplicate/Unicode checks, and no null nonnullable fields. Proofs use one quoted positive decimal safe integer, not weak tags, leading zeros, lists, wildcards, or silently rounded values. Lists use principal/epoch/entity-type/page-size-scoped signed continuations, not a frozen/offline snapshot. Authentication and authorization are rechecked inside each SQL transaction, including after HTTP admission and body reading.

Use exact documented paths. Collection reads accept only one `limit` and one nonempty `cursor`; writes never accept query parameters. GET and DELETE reject bodies and content encodings. Unsupported methods return `405` with `Allow`; there is deliberately no single-category/tag GET operation in this contract. Read the full live list for metadata, rather than assuming a successful historical write acknowledgement describes current state.

Safe error codes include `400 INVALID_TAXONOMY_URL`, `INVALID_QUERY`, `INVALID_CURSOR`, or `INVALID_REVISION`; `409 PRECONDITION_REQUIRED`, `REVISION_CONFLICT`, `CATEGORY_CYCLE`, `CATEGORY_HAS_CHILDREN`, `TAG_NAME_CONFLICT`, `IDEMPOTENCY_CONFLICT`, or `IDEMPOTENCY_EXPIRED`; and the shared authentication, validation, body/media, rate, upgrade, and availability errors. Messages/details never contain submitted secrets, private paths, or SQL diagnostics. Clients should localize allowlisted codes, not render arbitrary server messages. Failed/uncertain writes require explicit foreground reconciliation with the original key/body/proof; never silently retry, refresh a proof, or rebase.

For example, after authenticating and obtaining an in-memory CSRF token, send a root category body `{"name":"Household"}` without If-Match. To create its child, read the current parent from the live category list and send `{"name":"Tools","parent_id":"<reviewed-parent-id>"}` with that parent's original quoted revision. To move an existing category to root, send only `{"parent_id":null}` with the category's original target revision. These examples are request shapes, not shell commands containing reusable credentials.

## Hierarchy and concurrency

A category cannot point to itself or a descendant. Reparenting changes a stable parent link, not item identity or holdings. Category aggregate revisions cover changes in the branch and its classification membership. Tag revisions also cover item-tag membership, protecting a reviewed detach/delete scope. Touch affected ancestors once, not every unchanged descendant. New child creation verifies the original parent proof; rename, move, and delete verify the original target proof. Concurrent membership changes must not silently expand an old deletion/reclassification review.

Deletion never implicitly deletes a category subtree. Remove or reparent children explicitly first. Deleting a category leaf clears category references on assigned items atomically. Deleting a tag removes only that tag's item links. Counters/tombstones retain old identities so a later same-name tag with a different ID does not acquire an earlier tag's meaning.

## Browser adapter boundary

`web/src/lib/api/taxonomy.ts` exposes explicit category/tag page, create, patch and delete functions backed by the generated contract. It deliberately provides no uncontracted single-resource GET. Schemas reject unknown/private fields, invalid Unicode, unsafe revisions, self-parent resources, duplicate page identities, invalid nullable-parent shapes and empty patches. Names preserve exact case, whitespace and Unicode normalization. Resource replies require the requested identity, matching strong ETag and original/no-op-or-next revision; new resources start at revision one and deletion acknowledgements advance the original target once.

Category-create overloads require a reviewed parent proof for a child and exclude unrelated proofs for roots; tag creation likewise excludes unrelated proofs. Runtime validation also protects JavaScript callers. The caller supplies the original key, CSRF token, revision and AbortSignal. Each call makes one request, with eight-second reads and thirty-second writes, no implicit pagination, token refresh, rebase, retry, queue, or storage. Opaque cursors are encoded as query values, never interpreted as extra parameters. Safe errors retain only allowlisted metadata, not server messages/details. A frozen acknowledgement is not a current-state cache update.

Location/category/tag adapters share the same Unicode, path, revision, acknowledgement, root/child-header and versioned-response primitives. Location's reserved `tree` read path remains location-specific; supported taxonomy IDs such as `new` and `tree` are not rewritten. Settings forms use these adapters and the same shared volatile review/leave/session boundaries as locations. Persistent/offline drafts are still separate unfinished work.

## Transaction service

`store.NewTaxonomy(auth)` binds the module to the existing private authentication store. It offers authenticated category/tag detail and internal bounded stable-ID pages plus create/partial-edit/delete transaction methods. `PageCategories` and `PageTags` return public `data`/`next_cursor` lists; unsigned `NextID` stays internal. HTTP still owns Origin/CSRF/version/admission. Category commands preserve omitted versus explicit-null parents through the internal presence wrapper, never a wire wrapper.

Location, category, and tag pages share one private cursor codec with distinct MAC purposes. Each continuation binds the administrator, dataset epoch, entity/list kind, page size, last ID, and a 15-minute validity window. It contains no session credential, never authorizes itself, and can continue after a fresh completed login by the same principal. Revoked/expired sessions, mandatory password changes, restored epochs, cross-list tokens, altered page sizes, tampering, and noncanonical encodings fail closed. The read and session update commit before a page or continuation is released; a failed read/commit never returns partial metadata. These are live lists: concurrent edits/deletions may change later pages. They are not a substitute for the separate frozen-sync protocol.

The shared mutation runner freezes principal/epoch-scoped replies and immutable events only after every source write, current/outbox projection, and derived FTS write succeeds. Each identity is staged once; final-state search materialization occurs during flush, not when an intermediate source field first changes. Failed statements, late authorization changes, exhausted counters/sequences, or deferred commit failure return no successful DTO/acknowledgement and roll back all dependent rows.

Search materialization reads manual item fields, category ancestry, live tag links, aliases, attributes, barcode identifiers, and permitted purchase notes/merchants. Explicitly reviewed `keyword`/`specification` candidates contribute accepted values or corrected replacements. Pending/rejected entries, missing review decisions, raw OCR/jobs, stale cached text, historical realized-field candidates, and unit-specific identifiers do not become item-level published terms. Confirmed title/brand/model/category/tag values come from their realized source fields, preserving later manual overrides.

The same final-state publication stages every dependent unit's independently keyed search document. Unit text adds only that unit's asset code, nullable serial and notes; it never puts those identifiers into the item or sibling-unit text. Shared curated metadata and approved free-text candidates remain inherited. Purchase merchants/receipts/line notes must be attached to that exact unit and item, rather than another unit or an unrelated purchase. Multiple matching lines do not duplicate their purchase's merchant/receipt contribution. The unit FTS row and frozen/current search payload use the retained authoritative projection counter, not an untrusted cached text/revision. A unit-only rebuild likewise reads source fields directly, without copying the item search cache.

Category renames/moves/deletions and tag renames/detachments therefore cannot leave stale unit terms behind. Every dependent FTS/current/outbox row, audit/reply and source mutation shares one transaction; a later unit failure, exhausted counter, revoked session or deferred commit failure withholds the entire result. Old frozen rows remain immutable and repeated staging emits one final revision per identity. This publisher does not implement unit CRUD, search queries, the worker, or a complete sync backfill by itself.

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

Contract tests exercise separate create/partial-patch shapes, nullable parent presence, code-point/identity/revision bounds, exact offline payload compatibility, positive quoted proofs, Origin/body limits, and bounded live pagination. Strict generated-type fixtures reject missing creation names, missing response parents, internal command wrappers, missing Origin, and null tag names. Store tests replay every mutation's frozen changes onto the old current state and require exact equality, verify deep hierarchies/concurrent retries/stale proofs, inject dependent-write/deferred-commit/late-auth failures, and assert approved-only FTS publication without modifying inventory or AI source evidence. HTTP tests drive all eight operations through verified TLS and a real cookie jar, strict JSON/URL/header boundaries, cross-list cursors, unchanged admission limits, post-middleware revocation and changed proofs, no-op and fresh-session frozen replies. A bounded fuzz target exercises both taxonomy JSON readers. Browser tests exercise actual settings create/rename/reparent/delete, exact tag-name conflicts, dirty navigation, protected login return, explicit changed-field adoption, reconnect without replay, genuinely committed malformed deletion replies, light/dark accessibility, long paths and 200% text. Physical NAS/phone acceptance is not implied.

```sh
go -C server test -race -count=1 ./internal/store -run '^TestTaxonomy'
go -C server test -race -count=1 ./internal/httpapi -run '^TestTaxonomy'
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzTaxonomyJSON -fuzztime=5s -parallel=2
make check-backend
```
