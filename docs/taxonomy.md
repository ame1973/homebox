# Categories and Tags

**Implementation status:** executable request/response contracts are defined and validated. The authoritative transaction services, HTTP handlers, browser adapters, and settings screens are being implemented separately; the schemas alone do not make those operations available. Core item/stock/media/search workflows remain in progress.

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

## Projections and approved search

Every source mutation, immutable mutation event, frozen idempotent reply, projection/counter update, dependent search rebuild, and outbox row commits together. No-op metadata changes keep their projection revisions while still receiving a frozen acknowledgement. A successful replay returns the original result, not a new mutation or necessarily the latest resource. Expired claims are never silently reused.

Category changes update category projections and affected item/search projections. Tag renames update their own metadata and affected search text; deleting a tag emits independent item-tag tombstones before rebuilding search. Composite identities are canonical JSON pairs, not delimiter concatenations. No unreviewed OCR, rejected candidate, model job error, or unapproved suggestion may enter a search projection. Confirmed category/tag/title/brand/model values are read from realized source fields, not resurrected from historical candidate labels after a later user override. The AI worker is not required for these ordinary mutations.

A taxonomy backfill is only one part of the required 22-entity frozen-sync implementation. It must not enable an incomplete replica or advertise a full search/worker feature before those services are implemented and tested.

## Verification

```sh
uv run --frozen python -m unittest discover -s tests -v
npm --prefix web run api:generate
npm --prefix web run typecheck
```

Contract tests exercise separate create/partial-patch shapes, nullable parent presence, code-point/identity/revision bounds, exact offline payload compatibility, positive quoted proofs, Origin/body limits, and bounded live pagination. Strict generated-type fixtures reject missing creation names, missing response parents, internal command wrappers, missing Origin, and null tag names. Service/HTTP/browser acceptance must be recorded separately when executed; physical NAS/phone acceptance is not implied.
