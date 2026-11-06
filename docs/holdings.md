# Individual Units and Quantity Stock

**Implementation status:** bounded root contracts/generated TypeScript types, validated private Unit/Stock source loaders, independent current/frozen materializers and additive migrations 0010/0011 are implemented and tested. The private Go `Inventory` service also implements real receive/consume/transfer/absolute-adjust transactions with immutable inventory events and original Item proofs. Unit detail, complete owner-scoped Unit pages and filter-scoped Stock pages are also implemented through authorized Go services. Unit mutations/lifecycle, public history reads, HTTP handlers, typed runtime adapters and responsive operating screens are still being built. The existing catalog and owned-metadata screens do not receive stock or create physical units. Go transaction evidence is not a claim of those HTTP/UI workflows, complete sync or physical-device acceptance.

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

The four quantity commands below are implemented through the private Go `Inventory.Receive`, `Consume`, `Transfer` and `Adjust` methods, using distinct `StockReceive`, `StockConsume`, `StockTransfer` and `StockAdjust` inputs. Construction requires the owning store's `Auth` service; SQL remains private. These are not yet callable HTTP endpoints or browser controls. Their forthcoming HTTP boundary must enforce the additional transport/admission rules below, not bypass them because the service already checks SQL authorization.

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

## Movement direction, placement and no-op receipts

The service records one independent immutable inventory event per actual command, with these meanings:

| Action | `quantity_base` in history | From | To |
| --- | --- | --- | --- |
| `receive` | Positive received quantity | null | Destination |
| `consume` | Negative consumed quantity | Source | null |
| `transfer` | Positive transferred magnitude | Source | Destination |
| `adjust`, increased | Positive actual difference | null | Counted location |
| `adjust`, decreased | Negative actual difference | Counted location | null |
| `adjust`, new zero row | Zero, recording explicit first placement | null | Counted location |

A transfer event is **not** a net Item increase: both endpoints describe the same quantity moving within the Item. Do not sum all action magnitudes as a net stock total. An adjustment computes its difference from the actual retained balance; it never accepts a caller's delta. An existing value-equal adjustment emits no movement, Stock/Item/location counter increment or current/outbox/data-revision change. Its immutable original-key receipt still preserves the exact reason and optional note. A missing balance is not an existing zero: explicitly adjusting a previously absent pair to zero creates retained source and a zero-difference placement event. An explicitly recreated pair continues its retained counter rather than resetting history to one.

Receive, transfer destinations and increased/new adjustment placement require every location ancestor to exist, be undeleted, have real bounded authority and be active. Cycles or invalid/missing ancestor authority fail the whole command; an incomplete path is not silently treated as a new root. Consumption, transfer outflow and lower/equal existing adjustments may leave a valid archived path, but cannot increase stock there. Catalog Item archival is metadata-only and does not silently dispose of or freeze readable physical holdings.

Each actual command touches the Item, affected independent Stock rows, affected location ancestor paths and new event once per identity. Shared transfer ancestors are coalesced; unchanged sibling locations/Units and unrelated category/tag/purchase/media/AI rows remain unchanged. Quantity-only mutations do not republish FTS text that did not change. Safe arithmetic, source availability and both complete paths are checked before either transfer leg is written. SQL, publication, safe-counter/sequence, authorization and COMMIT failure return no partial Stock result and retain neither leg nor an original-key claim. Zero balances remain source, not tombstones.

## Read completeness and admission

Unit and Stock list calls have 1–100 rows, default 50, stable Unit-ID or `(item_id,location_id)` order and required nullable `next_cursor`. Null terminates a page sequence. Cursors are opaque and scoped; they are not frozen sync snapshots. A first page is not a whole collection, and a missing owner is not an invented empty list. Do not synthesize revision one for a live row with missing authority. Source uniqueness, lookahead/cursor consistency and complete scope remain runtime checks beyond schema array limits.

`Inventory.GetUnit` reads one real independently versioned Unit. `Inventory.PageUnits` completes the requested existing Item's live Unit collection in stable ID order, including all retained lifecycle states and exact null/empty serial/location text. A missing Item is an error, not a fabricated empty page; an existing quantity Item with no Units has a real empty collection. Any retained Unit row with invalid owner/tracking/counter state fails the whole page, including an invalid lookahead row. Neither method reads cached projection bytes or publishes read-side state. They are Go services, not yet HTTP routes.

`Inventory.PageStock` implements the documented date/currency/category/location/scope/status/merchant criteria. Filters apply before page slicing: location matches each actual balance row, rather than every balance of an Item also found at another location; category/location scope defaults to subtree. Status is the owning Item's `active`/`archived` metadata, not a Unit lifecycle value. Retained zero balances remain readable. Date/currency/exact merchant must match the same associated purchase, using inclusive civil dates in the configured home timezone, not stock-movement timestamps or a 24-hour approximation. Different measurement tuples remain separate source rows with their own actual quantities/conversions, never a scalar total or a copied first Item's unit.

Both page services use the existing private 15-minute signed continuation boundary, with independent purposes and principal/epoch/page-size binding. Stock additionally binds every normalized filter and the resolved home-timezone string inside the same authorized transaction. Source rows may change/disappear between live pages; a continuation is not a frozen snapshot or authority, and a fresh same-principal login may continue it. Expiry, restored epochs, changed filters/timezone/size or another owner/collection cannot silently reinterpret a cursor.

Unit and existing metadata cursors retain their 2,048-byte limit and exact wire spelling. Stock alone permits 4,096 ASCII bytes for an opaque signed composite position: two full 128-code-point IDs containing controls can exceed 2 KiB after nested JSON/base64 encoding. The server validates canonical pair spelling and both original component bounds, then compares the actual SQL `(item_id,location_id)` tuple, not an escaped JSON string. It never truncates IDs, silently repairs positions, adds server-side cursor storage or changes offline identities. A malformed lookahead or an unencodable continuation withholds both data and cursor. Read/query/session/signing/COMMIT failures similarly return no resource or page prefix, without repairing source/counters or appending receipts/events.

Writes require exact Origin, an active completed administrator session, in-memory CSRF, supported application version, an original idempotency key and the original quoted positive proof. The lexical proof/ETag shape rejects zero, leading zeros, weak/list/wildcard tags and trailing data; the server also enforces the numeric safe bound and exact resource identity. A Unit response revision is independent from the Item proof used for creation. Deletion acknowledgements require the original Unit's next revision and a stable operation identity, never a list ETag.

Unit creation/metadata/status and stock write JSON are bounded to 128 KiB; a Unit move to 16 KiB. Read/delete bodies and content encodings are forbidden. Only the two collection reads accept documented query fields within 16 KiB raw bytes; resource reads/writes accept no query. Exact fields/duplicates/Unicode/integers and authorization must be revalidated at the HTTP and SQL transaction boundaries.

A request canceled after sending may have committed. Retain the original body/key/proof for explicit foreground reconciliation, including after source deletion or a fresh same-principal login. Do not retry, refresh proofs, queue inventory writes or replay them on reconnect. Stock/unit mutation fingerprints, events, projections and responses share the domain transaction boundary, not credential mutation storage. Core physical CRUD must not require an AI worker.

## Retained persistence and final-state publication

`server/internal/store` contains the private Unit/Stock loaders and result validators. They read actual source and independent retained counters, not cached JSON or a synthetic revision one. They also require real bounded Item authority and the correct tracking mode. Unit null/supplied-empty fields remain distinct; all six retained lifecycle states are readable. Stock reads its current owner's complete measurement tuple without rescaling stored quantities. Missing source returns no resource; invalid UTF-8, field/counter/quantity/measurement state, mode mismatch or a failed query returns no partial DTO. These are internal transaction primitives, not a new unauthenticated read service or SQL handle exposed to HTTP.

Migration 0010 appends the Unit owner/stable-ID lookup index, missing independent Unit counters/current payloads and canonical-pair Stock counters/current payloads. Only absent counters begin at one; existing counters and already materialized payload bytes/timestamps/frozen changes remain unchanged. A retained counter without source never recreates the Unit/balance. Zero balances, raw Unicode/control text, nullable serial/location and all source quantities remain exact. The stock primary key remains its real Item/location pair.

Go validates every holding source and retained unit/stock counter after SQL but before migration/checksum commit. SQLite text length alone truncates at native NUL and is not full UTF-8 validation; noncanonical stock counter spelling must not silently become another revision-one identity. Invalid source/authority rolls back every pending DDL/index/backfill/checksum row, retaining the verified private pre-migration snapshot. Existing certified-root upgrades preserve epoch; explicit reference adoption still follows its documented epoch rotation. No pending upgrade repairs tracking modes, guesses conversion or publishes private/legacy FTS/OCR caches. This partial materialization does not enable all 22 sync entities.

The existing private domain transaction can now materialize explicitly staged Unit/Stock upserts and independent null tombstones from final source. Repeated staging publishes one final version per identity, not intermediate values or historical cache text. Source, staged Item/location/search dependencies, retained counters, current/frozen rows, mutation audit and original response commit together; SQL, safe-counter/sequence exhaustion, late authorization or deferred COMMIT failure withholds the result and restores all state. Reconciliation can return an older frozen Stock after later changes/deletion without installing it as current authority.

Migration 0011 adds exact note/reason fields and a deferred audit association to immutable inventory-event history, plus a note field on the existing immutable mutation audit so equal adjustments lose no context. A new movement gets a fresh independent event ID/counter and a `mutation_event_id` association to the same committed domain receipt. The association is not unique: a future real batch may have multiple movements under one audit. New producers leave the old globally unique raw movement `idempotency_key` null; administrator/epoch scoping remains owned by the existing domain receipt. The same caller key in a new dataset epoch must not collide with a global historical movement key.

Legacy movement values, original raw keys, current/frozen bytes, counters and epochs remain unchanged. New columns default to empty notes/reasons and a null association, never an invented mapping. Migration 0011 does not backfill/publish unverified legacy history, guess an instant from an old date-only value or recreate source from a counter. All old/new inventory rows are append-only after upgrade. New events use the same UTC instant as source/current/outbox/audit/receipt; the materializer validates strict RFC3339 spelling, bounded UTF-8/identities/quantities and real independent event authority before publication. Producer context stays server-side; the unchanged eight-field offline event payload has no inner revision, note, reason, audit ID or raw caller key.

Future Unit/lifecycle/batch producers must still validate their own original proof, apply physical/accounting rules, append actual immutable history and stage every required dependency inside the same transaction. The payload builder does not infer or authorize those workflows or generic deletion of zero balances. Public history reads, HTTP dispatch and complete sync remain separate implementation gates.

## Verification and remaining implementation

```sh
uv run --frozen python -m unittest tests.test_holdings_contracts -v
uv run --frozen python -m unittest discover -s tests -v
go -C server test -count=1 ./internal/store -run '^TestHolding'
go -C server test -count=1 ./internal/store -run '^TestInventory'
go -C server test -count=1 ./internal/store -run '^TestHoldingRead'
npm --prefix web run api:generate
npm --prefix web run api:check
npm --prefix web run typecheck
make check-web
```

Schema checks cover complete bounded Unit/Stock resources, unchanged offline payload identities, nullable/empty/omitted serial metadata, explicit creation/location/status shapes, complete live cursors, safe base quantities and measurement tuples, Item-versus-Unit/Stock proof scopes, affected-response identity, body/query limits and a required deletion revision. Strict generated-type fixtures reject missing location/proofs/Origin, caller identity/measurement fields, terminal creation, null notes, numeric serial/quantity text, incomplete transfer/adjustment/measurement/page data and unversioned deletion acknowledgements. Generated TypeScript cannot enforce numeric ranges, Unicode, nonempty partials, database authority or arithmetic by itself.

Real SQLite persistence tests separately cover complete retained resources, source-versus-cache reads, actual owner/row authority, mode/measurement/Unicode/NUL bounds, zero balances, full-length canonical pairs and query plans. Fresh/reference/certified-root upgrades preserve source/counters/current/frozen bytes and roll back invalid rows, noncanonical authority and all pending DDL/ledger writes. Private publication fixtures replay exact deltas, coalesce final source and staged owner/location/search scopes, preserve source-only approved text and independent tombstones, and exercise every relevant source/projection/receipt/FTS failure plus late authorization/deferred COMMIT and exhausted counters/sequences. Those fixtures deliberately use test-only operations, not fictional public inventory services or invented physical-event acceptance.

The actual `Inventory` service tests separately drive all four public Go methods against real SQLite: original Item proofs versus independent Stock replies, exact signed direction/differences, maximum-safe arithmetic, canonical Unicode pairs, complete active/deep/archived paths, retained zero/new-zero/recreated-pair semantics, complete source/current/frozen delta replay and no-op context. They cover all original request fields, changed-proof/operation conflicts, fresh-login/frozen replies after later source changes/removal, expired claims, cancellation, epoch separation without raw-key collisions, transactional session gates, concurrent identical/competing commands and opposite-transfer conservation. Statement/authority/exhaustion/final-event-corruption/late-auth/deferred-COMMIT failures roll back both legs, all history/projections and the original receipt. Migration tests preserve legacy bytes/authority/publication and roll back all pending holding/event DDL/ledger rows; immutable event/audit triggers and nonunique deferred associations are exercised directly. These are real Go service tests, not HTTP or screen evidence.

The read-service suite exercises complete multi-page retained Unit/Stock collections, detail/list equality, all lifecycle/null/zero states, source-versus-cache behavior, real quantity writes and mixed measurement rows, actual location/category/status/purchase/date filtering before lookahead, maximum Unicode/control pair continuations, every filter/timezone/size binding and owner/collection isolation. It checks live source removal, same-principal fresh login, expired/malformed/signed-invalid positions, all read authorization gates, SQL/timezone/signing/session/late-expiry/deferred-COMMIT failures and no read-side publication. Root contract and strict generated-type fixtures separately pin the Stock-only cursor budget and Item metadata status without claiming a runtime browser adapter.

Still pending are public history reads, Unit mutations/lifecycle/asset guards, HTTP dispatch/strict JSON/admission conformance, real runtime adapters, foreground review screens, QR/camera, physical holdings acceptance and complete sync. These are not marked passed by the Go service/persistence/schema/type checks above. Existing metadata/auth/browser coverage remains separate evidence. Physical NAS/phone/operator TLS/VPN/off-host recovery gates remain blocked until actually performed.
