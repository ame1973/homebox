# HTTP and Offline Data Contracts

These are executable interface specifications, not a claim that every HTTP handler has been implemented. The initial application API version is `0.1.0`; specification-pack revision `v0.9.3` is a separate identifier.

- [`openapi.yaml`](openapi.yaml): OpenAPI 3.1, including the original 103 operations plus the additional operations needed to complete their workflows.
- [`offline-projection.schema.json`](offline-projection.schema.json): strict, discriminated offline upsert/tombstone records. The embedded OpenAPI copy is checked for exact equivalence.
- [`sync.md`](sync.md): snapshot, delta, retention, offline identity, and draft safety rules.

## Transport and authorization

The application serves `/api/v1` on the same trusted HTTPS origin as the PWA. Only login, protected initial setup, and minimal liveness are anonymous. Setup requires a one-time bootstrap token and is disabled once an administrator exists. Login/setup also require an allowed same-origin request; anonymous does not mean unrestricted.

Administrator sessions use the `homebox_session` HttpOnly/Secure/SameSite cookie. Protected browser writes require `X-CSRF-Token`, `Idempotency-Key`, and `X-HomeBox-Client-Version`. The version header contains the semantic application version, not the specification revision. Incompatible writes return `426 CLIENT_UPGRADE_REQUIRED`. Worker routes under `/api/v1/worker/` use a separate scoped bearer credential, never an administrator cookie.

All API responses are `Cache-Control: no-store`. Service workers must not cache API/authentication, backup, or export responses. Explicitly installed model assets and budgeted thumbnails are copied into separately managed per-device storage only through the documented opt-in flow.

Errors use `{ "error": { "code", "message", "request_id", "details" } }`. Details are safe structured diagnostics, never credentials, raw storage paths, or database internals.

## Mutation and concurrency rules

- `If-Match` is a quoted numeric revision, for example `"12"`. PATCH/PUT/DELETE and concurrency-sensitive actions reject stale revisions rather than silently overwriting data.
- Stock operations check the catalog item's aggregate revision. Every stock mutation advances that revision as well as affected stock projections.
- Child creation checks its parent revision. Initial asset creation checks the unit revision; later asset-profile/usage/cost/lifecycle mutations check the asset aggregate revision.
- Idempotency binds the authenticated principal, operation, request body, and concurrency preconditions. The same key with the same request returns the original committed response; a changed request conflicts. The response and business effects commit in the same transaction.
- Authentication mutation keys are scoped to the current administrator session and bind the operation and typed request. Reusing a key for another operation or payload conflicts. Credential request fingerprints are keyed, not unkeyed password digests. Password change requires a different new password, revokes every session, and returns an acknowledgement rather than a new cookie; an exact retry may retrieve only that frozen acknowledgement from the original session context.
- Posted usage/cost events are immutable. Corrections append a new event referencing the original. Neither usage nor quantity stock may become negative.
- Quick create defaults to individual tracking. Commercial barcode strings retain leading zeros and are not globally unique. Location/unit QR identifiers remain stable after edits and moves.

## Files and privileged operations

`POST /media` and `POST /imports/upload` accept bounded multipart files. The server derives content type, size, dimensions, and digests from actual bytes; client metadata is not authoritative. Media links require exactly one owner and have independent IDs. Unlinking a shared image does not delete its other links.

Media content and thumbnails use authenticated stream endpoints. Imports require upload, preview, explicit confirmation, and transactional commit. Exports use authenticated expiring download tokens. Backups include credential hashes and must be encrypted with an operator-configured recipient; ordinary exports must not contain these hashes.

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
