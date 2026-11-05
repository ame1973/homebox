# Individual Units and Quantity Stock

**Implementation status:** bounded root contracts/generated TypeScript types, validated private Unit/Stock source loaders, independent current/frozen materializers and additive migration 0010 are implemented and tested. Authoritative unit/stock read/write/lifecycle services, HTTP handlers, typed runtime adapters and responsive operating screens are still being built. The existing catalog and owned-metadata screens do not receive stock or create physical units. Persistence/publication primitives and schema checks are not a claim of those complete workflows or physical-device acceptance.

## Distinct physical identities

A catalog Item describes a product. Its tracking mode is exclusively `individual` or `quantity`:

- A Unit is one individually tracked physical instance. It has its own stable ID/revision, immutable owning Item and server-assigned globally unique asset code. Commercial product barcodes can be shared; asset-code uniqueness does not turn a product barcode into a unique physical identity. Serial numbers are exact nullable text, not globally unique keys.
- A Stock balance belongs to one quantity-tracked Item at one location. Its identity is the canonical Unicode JSON pair `[item_id,location_id]`, not a new row UUID, displayed unit, cursor or Item revision. It has an independent retained positive revision. A zero balance remains meaningful referenced source/history, not an absent row.
- A movement is immutable history, not a replacement current Unit or Stock row. Source changes, required Item/location/search dependencies, retained counters, final current/frozen projections, immutable events and the original response must commit together. A correction appends a new authorized event; it never rewrites a posted event.

Item/Unit/location IDs contain 1–128 Unicode code points. Unit and Stock revisions are exact positive safe integers through `9007199254740991`. Imported identities remain stable; do not invent UUID-only input normalization. The retained asset-code representation is 1–10,000 code points without a new short-format restriction. The server, not a caller, assigns a new code. Notes contain 0–10,000 code points; serial numbers are null or exact strings of 0–10,000. Preserve whitespace, case, normalization and null versus supplied-empty serial text. Runtime input boundaries must additionally reject invalid UTF-8/lone-surrogate repair; JSON Schema length checks alone do not certify that behavior.

## Unit commands

Paths are relative to `/api/v1`. These contracts do not imply the handlers already exist.

| Operation | Original proof | Result |
| --- | --- | --- |
| `GET /items/{id}/units` | Completed session/read scope | Complete bounded live Unit page, no list ETag |
| `POST /items/{id}/units` | Owning Item revision | New independent Unit and its strong Unit ETag |
| `GET /units/{id}` | Completed session/read scope | Current Unit with matching strong Unit ETag |
| `PATCH /units/{id}` | Independent Unit revision | Same Unit/owner; exact partial serial/notes metadata |
| `DELETE /units/{id}` | Independent Unit revision | Action acknowledgement with its independent next revision; no ETag |
| `POST /units/{id}/move` | Independent Unit revision | Same Unit at the explicitly reviewed destination |
| `POST /units/{id}/status` | Independent Unit revision | Same Unit after a server-authorized lifecycle transition |

Creation requires an explicit `location_id`, including null when no location is known. A nonnull destination must exist and have an active path. The minimal request is:

```json
{
  "location_id": null
}
```

Omitted serial/notes/status default to null/empty/`stored`; clients are not required to install those defaults. New Units may start as `stored`, `in_use`, `lent` or `lost`, not already `retired`/`disposed`. A new independent Unit must start at revision one and must not reuse a retained/tombstoned identity.

Ordinary metadata patches are nonempty and contain only `serial_number` and/or `notes`. Omission leaves a field unchanged; `serial_number: null` clears it, `serial_number: ""` preserves supplied-empty text, and `notes: ""` clears notes. A metadata patch cannot reassign the owner, change the asset code/location/status or post quantities, purchases or costs.

Movement names a required nonnull `target_location_id`; it does not move an entire location container. Moving a container uses the separate location operation and preserves each holding's actual assigned location. Lifecycle input accepts the six declared Unit states plus an optional note, but shape acceptance alone does not authorize retirement/disposal or bypass asset-accounting guards. The authoritative service must coordinate those guards and immutable history. Repeating current status must not invent a second movement.

## Exact base quantities

Stock uses integer `quantity_base` in the owning Item's `base_unit`, never `quantity_minor`, a floating display amount or an unlabeled mixed-unit total. The read response always includes the Item's actual `unit_code`, `base_unit` and `scale_factor`. Their bounds/patterns match catalog measurement: 1–32-code-point display unit, a strict 1–32-character ASCII base identifier and a positive safe-integer scale.

For `kg/g/1000`, `quantity_base: 1500` means 1,500 grams. A display may show 1.5 kilograms, but a write still sends the integer 1500; the service does not guess or install conversion from a unit label. Quantity bounds are 0–`9007199254740991` for retained balances and absolute adjustment, and 1–that maximum for receive/consume/transfer. Exact mathematically integral JSON spellings can represent an integer; unsafe or fractional values must never be rounded into one. Runtime addition/subtraction checks are mandatory even when each input is individually safe.

## Stock commands and proof scopes

All four stock writes require the **original Item aggregate revision**, not the separately displayed Stock revision. Each actual stock mutation advances the relevant Item aggregate and affected Stock identities atomically. This protects the complete Item interpretation and reviewed holdings scope without inventing another precondition header.

| Operation | Required body | Frozen response |
| --- | --- | --- |
| `POST /stock/receive` | Item, positive base quantity, target location | Target Stock and its independent ETag |
| `POST /stock/consume` | Item, positive base quantity, source location | Source Stock and its independent ETag |
| `POST /stock/transfer` | Item, positive base quantity, both locations | Target Stock and its independent ETag |
| `POST /stock/adjust` | Item, absolute nonnegative base quantity, target location, nonempty reason | Target Stock and its independent ETag |

An optional note contains at most 10,000 exact code points. Adjustment requires an exact nonempty 1–10,000-code-point reason. It sets an absolute quantity, not a signed delta; the service derives and records the actual difference. A value-equal adjustment is a no-op receipt, not a new movement or counter increment.

Transfer's source and target must differ. The authoritative transaction must check quantity tracking, source sufficiency, an active destination/path and safe target addition before changing either balance. Both legs, the original Item scope, immutable history, independent projections and frozen target response commit or roll back together. Depleting a balance to zero retains that row. The target-only response does not describe a current source balance, authorize mixed-unit sums or permit a partial transfer.

For example, an Item proof of `"7"` can validly produce a target Stock with revision 2: these are different counters. Never validate a Stock result by assuming it is the Item proof plus one, reinterpret its ETag as a newer Item proof, or install a frozen historical response as current source. Re-read authoritative state after confirmation.

## Read completeness and admission

Unit and Stock list calls have 1–100 rows, default 50, stable Unit-ID or `(item_id,location_id)` order and required nullable `next_cursor`. Null terminates a page sequence. Cursors are opaque and scoped; they are not frozen sync snapshots. A first page is not a whole collection, and a missing owner is not an invented empty list. Do not synthesize revision one for a live row with missing authority. Source uniqueness, lookahead/cursor consistency and complete scope remain runtime checks beyond schema array limits.

`GET /inventory` retains its documented date/currency/category/location/scope/status/merchant criteria; no field is removed by this contract refinement. Dates/associated-purchase/timezone semantics and signed filter binding need authoritative implementation, not client-side filtering of an already sliced first page. Different measurement tuples remain separate rows, not a scalar total.

Writes require exact Origin, an active completed administrator session, in-memory CSRF, supported application version, an original idempotency key and the original quoted positive proof. The lexical proof/ETag shape rejects zero, leading zeros, weak/list/wildcard tags and trailing data; the server also enforces the numeric safe bound and exact resource identity. A Unit response revision is independent from the Item proof used for creation. Deletion acknowledgements require the original Unit's next revision and a stable operation identity, never a list ETag.

Unit creation/metadata/status and stock write JSON are bounded to 128 KiB; a Unit move to 16 KiB. Read/delete bodies and content encodings are forbidden. Only the two collection reads accept documented query fields within 16 KiB raw bytes; resource reads/writes accept no query. Exact fields/duplicates/Unicode/integers and authorization must be revalidated at the HTTP and SQL transaction boundaries.

A request canceled after sending may have committed. Retain the original body/key/proof for explicit foreground reconciliation, including after source deletion or a fresh same-principal login. Do not retry, refresh proofs, queue inventory writes or replay them on reconnect. Stock/unit mutation fingerprints, events, projections and responses share the domain transaction boundary, not credential mutation storage. Core physical CRUD must not require an AI worker.

## Retained persistence and final-state publication

`server/internal/store` contains the private Unit/Stock loaders and result validators. They read actual source and independent retained counters, not cached JSON or a synthetic revision one. They also require real bounded Item authority and the correct tracking mode. Unit null/supplied-empty fields remain distinct; all six retained lifecycle states are readable. Stock reads its current owner's complete measurement tuple without rescaling stored quantities. Missing source returns no resource; invalid UTF-8, field/counter/quantity/measurement state, mode mismatch or a failed query returns no partial DTO. These are internal transaction primitives, not a new unauthenticated read service or SQL handle exposed to HTTP.

Migration 0010 appends the Unit owner/stable-ID lookup index, missing independent Unit counters/current payloads and canonical-pair Stock counters/current payloads. Only absent counters begin at one; existing counters and already materialized payload bytes/timestamps/frozen changes remain unchanged. A retained counter without source never recreates the Unit/balance. Zero balances, raw Unicode/control text, nullable serial/location and all source quantities remain exact. The stock primary key remains its real Item/location pair.

Go validates every holding source and retained unit/stock counter after SQL but before migration/checksum commit. SQLite text length alone truncates at native NUL and is not full UTF-8 validation; noncanonical stock counter spelling must not silently become another revision-one identity. Invalid source/authority rolls back every pending DDL/index/backfill/checksum row, retaining the verified private pre-migration snapshot. Existing certified-root upgrades preserve epoch; explicit reference adoption still follows its documented epoch rotation. No pending upgrade repairs tracking modes, guesses conversion or publishes private/legacy FTS/OCR caches. This partial materialization does not enable all 22 sync entities.

The existing private domain transaction can now materialize explicitly staged Unit/Stock upserts and independent null tombstones from final source. Repeated staging publishes one final version per identity, not intermediate values or historical cache text. Source, staged Item/location/search dependencies, retained counters, current/frozen rows, mutation audit and original response commit together; SQL, safe-counter/sequence exhaustion, late authorization or deferred COMMIT failure withholds the result and restores all state. Reconciliation can return an older frozen Stock after later changes/deletion without installing it as current authority.

Each future producer must still validate its original proof, apply physical/lifecycle rules, append the actual immutable inventory events and explicitly stage every required dependency inside that transaction. The payload builder does not infer a complete receive/transfer/move workflow, create units/stock implicitly, or authorize a generic deletion of zero balances. Core services remain a separate implementation gate.

## Verification and remaining implementation

```sh
uv run --frozen python -m unittest tests.test_holdings_contracts -v
uv run --frozen python -m unittest discover -s tests -v
go -C server test -count=1 ./internal/store -run '^TestHolding'
npm --prefix web run api:generate
npm --prefix web run api:check
npm --prefix web run typecheck
make check-web
```

Schema checks cover complete bounded Unit/Stock resources, unchanged offline payload identities, nullable/empty/omitted serial metadata, explicit creation/location/status shapes, complete live cursors, safe base quantities and measurement tuples, Item-versus-Unit/Stock proof scopes, affected-response identity, body/query limits and a required deletion revision. Strict generated-type fixtures reject missing location/proofs/Origin, caller identity/measurement fields, terminal creation, null notes, numeric serial/quantity text, incomplete transfer/adjustment/measurement/page data and unversioned deletion acknowledgements. Generated TypeScript cannot enforce numeric ranges, Unicode, nonempty partials, database authority or arithmetic by itself.

Real SQLite persistence tests separately cover complete retained resources, source-versus-cache reads, actual owner/row authority, mode/measurement/Unicode/NUL bounds, zero balances, full-length canonical pairs and query plans. Fresh/reference/certified-root upgrades preserve source/counters/current/frozen bytes and roll back invalid rows, noncanonical authority and all pending DDL/ledger writes. Private publication fixtures replay exact deltas, coalesce final source and staged owner/location/search scopes, preserve source-only approved text and independent tombstones, and exercise every relevant source/projection/receipt/FTS failure plus late authorization/deferred COMMIT and exhausted counters/sequences. Those fixtures deliberately use test-only operations, not fictional public inventory services or invented physical-event acceptance.

Still pending are authoritative read/write/event/lifecycle services and their complete original-proof/concurrency/physical-policy tests, HTTP dispatch/strict JSON/admission conformance, real runtime adapters, foreground review screens, QR/camera, physical holdings acceptance and complete sync. These are not marked passed by the persistence/schema/type checks above. Existing metadata/auth/browser coverage remains separate evidence. Physical NAS/phone/operator TLS/VPN/off-host recovery gates remain blocked until actually performed.
