# HTTP and Offline Data Contracts

These are executable interface specifications, not a claim that every HTTP handler has been implemented. The initial application API version is `0.1.0`; specification-pack revision `v0.9.3` is a separate identifier.

- [`openapi.yaml`](openapi.yaml): OpenAPI 3.1, including the original 103 operations plus the additional operations needed to complete their workflows.
- [`offline-projection.schema.json`](offline-projection.schema.json): strict, discriminated offline upsert/tombstone records. The embedded OpenAPI copy is checked for exact equivalence.
- [`sync.md`](sync.md): snapshot, delta, retention, offline identity, and draft safety rules.
- [Categories and Tags](../docs/taxonomy.md): explicit taxonomy creation/patch, hierarchy, membership, deletion, and projection rules; implementation availability is tracked separately.
- [Catalog Item Rules](../docs/catalog.md): implemented private Go/SQLite catalog transactions, complete filter-scoped signed live pages, six item HTTP operations, typed adapters and responsive reviewed metadata screens, plus bounded fields, explicit measurement/defaults, nullable edits, archival/history guards and original proofs. Related holding/media/bulk/assisted-creation workflows remain unfinished.

- [Catalog Links and Tag Membership](../docs/catalog-links.md): independent barcode/alias/attribute IDs, immutable owners, explicit Item-versus-row proofs, exact checksum normalization, partial attributes and complete tag sets. All fourteen Go/HTTP operations, typed stateless owned adapters and responsive reviewed maintenance screens are implemented and tested, with independent persistence/materialization, original Item-versus-row proofs and complete owner-scoped signed pages. Schema validation alone is not runtime or HTTP evidence.

- [Individual Units and Quantity Stock](../docs/holdings.md): refined bounded Unit/Stock resources and commands, original Item-versus-Unit proof scopes, independent Stock response revisions, complete live pages and exact safe base quantities. Root schemas/generated types and contract/compile tests are available; authoritative holding services/backfill/HTTP/runtime adapters/screens remain pending. Offline payload shapes and all operation identities are unchanged.

## Transport and authorization

The application serves `/api/v1` on the same trusted HTTPS origin as the PWA. Only login, protected initial setup, and minimal liveness are anonymous. Setup requires a one-time bootstrap token and is disabled once an administrator exists. Login/setup also require an allowed same-origin request; anonymous does not mean unrestricted.

Administrator sessions use the `homebox_session` host-only HttpOnly/SameSite=Strict cookie (`Path=/`, no Domain, Secure on HTTPS). Only direct same-machine loopback development permits insecure transport/cookies. API Host and browser Origin are pinned to the configured public origin, never inferred from untrusted forwarding headers. Every auth POST requires one exact Origin, including anonymous login/setup. Protected browser writes additionally require `X-CSRF-Token`, `Idempotency-Key`, and `X-HomeBox-Client-Version`. The version header contains the semantic application version, not the specification revision; the current protocol accepts `0.1.0` and `0.1.0-dev`. Missing, duplicated, or incompatible versions return `426 CLIENT_UPGRADE_REQUIRED`. Worker routes under `/api/v1/worker/` use a separate scoped bearer credential, never an administrator cookie.

The seven authentication routes, seven location list/detail/create/edit/move/archive/delete operations, eight category/tag list/create/edit/delete operations, six item list/detail/create/partial-edit/archive/delete operations, fourteen owned barcode/alias/attribute/tag collection/write operations, and responsive account/location/taxonomy/catalog-metadata/owned-maintenance browser workflows are implemented. Catalog holding/media/bulk/assisted-creation workflows, location contents/summary/QR reads, privileged reauthentication consumers, and other domains remain pending. Typed category/tag/catalog browser adapters preserve original proofs and never automatically retry writes. Category/tag commands and their dependent publication use the same authoritative transaction boundary; see [Categories and Tags](../docs/taxonomy.md). Authentication JSON is unencoded UTF-8, limited to 16 KiB, with exact case-sensitive field names and no duplicates, unknown fields, null strings, invalid UTF-8, lone surrogate escapes, or trailing documents. Setup does not create a session; successful login returns only administrator metadata in JSON and delivers the opaque credential exclusively as a cookie. Password change/logout expire that cookie. See [HTTP Authentication and Transport](../docs/http-authentication.md) for operational configuration, limits, and retry behavior.

All API responses are `Cache-Control: no-store`. Service workers must not cache API/authentication, backup, or export responses. Explicitly installed model assets and budgeted thumbnails are copied into separately managed per-device storage only through the documented opt-in flow.

Errors use `{ "error": { "code", "message", "request_id", "details" } }`. Details are safe structured diagnostics, never credentials, raw storage paths, or database internals.

## Mutation and concurrency rules

- `If-Match` is a quoted numeric revision, for example `"12"`. PATCH/PUT/DELETE and concurrency-sensitive actions reject stale revisions rather than silently overwriting data.
- Stock operations check the catalog item's aggregate revision. Every stock mutation advances that revision as well as affected stock projections.
- Child creation checks its parent revision. Location/category child creation requires the parent's `If-Match` only when `parent_id` is non-null; root creation omits it. Branch revisions cover descendant metadata/membership changes, so a subtree confirmation cannot silently include new children. Category classification and tag-link membership also invalidate their reviewed aggregate scopes. Initial asset creation checks the unit revision; later asset-profile/usage/cost/lifecycle mutations check the asset aggregate revision.
- Idempotency binds the authenticated principal, operation, request body, and concurrency preconditions. The same key with the same request returns the original committed response; a changed request conflicts. The response and business effects commit in the same transaction. Domain keys also bind the dataset epoch and require an active completed session even for replay; a fresh login can reconcile the same principal's earlier result. The implemented domain reply lifetime is 30 days, after which `IDEMPOTENCY_EXPIRED` requires explicit review and a new key. Expired claims must never be deleted/reused as if their mutation had not committed.
- Authentication mutation keys are scoped to the current administrator session and bind the operation and typed request. Reusing a key for another operation or payload conflicts. Credential request fingerprints are keyed, not unkeyed password digests. Password change requires a different new password, revokes every session, and returns an acknowledgement rather than a new cookie; an exact retry may retrieve only that frozen acknowledgement from the original session context.
- Posted usage/cost events are immutable. Corrections append a new event referencing the original. Neither usage nor quantity stock may become negative.
- Quick create defaults to individual tracking. Commercial barcode strings retain leading zeros and are not globally unique. Location/unit QR identifiers remain stable after edits and moves.

## Files and privileged operations

`POST /media` and `POST /imports/upload` accept bounded multipart files. The server derives content type, size, dimensions, and digests from actual bytes; client metadata is not authoritative. Media links require exactly one owner and have independent IDs. Unlinking a shared image does not delete its other links.

Media content and thumbnails use authenticated stream endpoints. Imports require upload, preview, explicit confirmation, and transactional commit. Exports use authenticated expiring download tokens. Backups include credential hashes and must be encrypted with an operator-configured recipient; ordinary exports must not contain these hashes.

Reauthentication returns a single-use capability with a maximum five-minute lifetime, further bounded by session idle/absolute limits. It requires an independent server secret, so knowing a session cookie and request key is not enough to compute the capability. It is tied to the session, account revision, and dataset epoch; the privileged mutation or job authorization consumes it in the same SQLite transaction. A retry never renews an expired/consumed capability. Raw grants are delivered only in the authenticated response, not saved in audit or idempotency payloads.

Restore requires recent reauthentication, a successful unexpired dry-run ID, the typed confirmation `RESTORE`, and explicit dataset-reset acknowledgment. Restoring invalidates sessions and rotates the dataset epoch. The server does not execute a destructive release deployment from a browser request.

## Money, quantities, and search

Money uses integer minor units in an explicit ISO currency. Stock uses integer base quantities, with item-level `unit_code`, `base_unit`, and `scale_factor`. No endpoint mixes different base units or currencies into an unlabeled scalar. Realized and projected asset cost fields remain separate. Per-day allocation uses inclusive civil days in the configured home timezone; absent denominators produce null.

All `_at` timestamps on the HTTP interface are RFC3339 timestamps; analytics `from`/`to` filters are civil dates. The server derives local civil dates for cost calculations and inclusive date-window filtering. There is no implicit foreign-exchange conversion.

Published text search contains manual fields and accepted/corrected AI text only. Unreviewed/rejected values cannot be effective text in offline review records. Image searches accept fingerprints or a model-compatible vector; similarity is not an identity probability. Model manifests identify exact revision, preprocessing, dimension, normalization, asset sizes, SHA-256 values, and same-origin URLs.

## Pagination and validation

Normal paginated lists cap pages at 100; sync pages cap at 500. Cursors are scoped, opaque, and tamper-resistant. Lists use stable ID ordering, or `(occurred_at, id)` for chronological events. Analytics aggregate the complete filtered dataset rather than a silently truncated first page.

Run the contract and reference-migration suite from the repository root:

```sh
uv sync --frozen
uv run --frozen python -m unittest discover -s tests -v
uv run --frozen python -m unittest discover -s tests -p test_contracts.py -v
```

Schema validation does not prove authentication behavior, business transactions, HTTP conformance, IndexedDB replay, or physical-device compatibility. Those are separate implementation and release gates.
