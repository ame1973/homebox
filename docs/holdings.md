# Individual Units and Quantity Stock

**Implementation status:** all twelve Unit/quantity-stock HTTP operations are implemented through the private Go `Inventory` service, with strict JSON/query admission, original proofs, independent resource ETags, completed-session/Origin/CSRF/version/key protection and authoritative transactional revalidation. Unit/Stock source/counter loaders, current/frozen publication and migrations 0010–0012 are tested. All twelve typed stateless browser adapters, complete live read/form/volatile reviewed-command state and responsive foreground Unit/Stock screens are implemented with strict runtime/response checks and original proof/measurement contexts. Asset-backed lifecycle producers and public history reads are still being built. Catalog detail links these separate physical workflows and the [reviewed purchase screens](purchases.md). Purchase allocation remains a separate financial command: it never receives Stock, creates/moves a Unit or opts into assets. Ordinary catalog/owned-metadata editing never implicitly receives stock or creates a Unit. HTTP/service/browser emulation evidence is not complete-sync, physical-device or production acceptance.

## Browser routes and usage

Sign in, finish the required initial password change, then open an actual catalog Item at `/items/ID`. Its Unit list or quantity-stock link opens the complete physical holdings for that Item. Every viewport shares these browser routes and capabilities; they are not `/api/v1` endpoints:

| Browser route | Purpose |
| --- | --- |
| `/items/ID/holdings` | Complete actual Unit collection or position-by-position quantity balances |
| `/items/ID/units/new` | Explicitly reviewed creation for an individual-tracked Item |
| `/units/UNIT_ID` | Exact independently versioned Unit detail and guarded record deletion |
| `/units/UNIT_ID/edit` | Changed-only serial/notes metadata |
| `/units/UNIT_ID/move` | One Unit's explicitly selected destination |
| `/units/UNIT_ID/status` | Operational status, non-asset retirement or explicit physical disposal |
| `/items/ID/stock/receive` | Positive integer base-count receipt |
| `/items/ID/stock/consume` | Positive integer base-count outflow |
| `/items/ID/stock/transfer` | Two-leg relocation within one Item |
| `/items/ID/stock/adjust` | Reasoned absolute nonnegative base count, including explicit new-zero placement |
| `/u/UNIT_ID` | Protected canonical Unit alias; not a claim of implemented QR printing |

Stock action routes accept only one optional `location=ID` prefill, for the source or target appropriate to that action. They do not accept quantity, price, proof or command data in a URL. Imported `new`/`tree` IDs remain identities, not invented creation aliases. Invalid/duplicate/external return paths are rejected rather than repaired. The validated catalog filter/cursor return survives normal maintenance and a login round-trip; only that safe navigation context travels, never credentials or unsent form data.

### Individual Units

Choose **Add Unit**, explicitly select a known active destination or the unknown-location option, and review its serial/notes and initial status. The server assigns the immutable Unit ID/asset code. The serial-provided checkbox distinguishes null from supplied-empty text; text areas retain exact whitespace, Unicode and newlines rather than silently shortening a legacy value. Creation does not create a purchase, price or asset basis.

Unit detail shows its independent identity/status/placement/proof. Edit only actual metadata changes, move only that Unit rather than its whole container, or choose a supported status. Non-asset retirement retains placement. Disposal requires sold/donated/recycled/discarded; ordinary status never infers proceeds, currency or service dates. Disposed Units cannot ordinarily move/reactivate. Financially backed lifecycle/record deletion remains guarded by the Go service; its separate asset workflow is still pending. Record deletion is a separately emphasized review, not a disposal shortcut, and preserves immutable history/shared media.

### Quantity balances

Every amount entry and review names the Item's exact **base unit** and complete measurement tuple. For `kg/g/1000`, enter `1000` to act on 1,000 grams, not `1` or a fractional display amount. Source, target and proposed leg counts remain separate, with original Item proof versus independent displayed Stock revisions. No mixed-unit total or automatic conversion is presented.

Receive increases an active placement; consume/transfer require actual sufficient positive source. Zero depletion retains the source row. Transfer review shows both source and destination before/after even though the API acknowledgement returns only the target. Absolute adjustment requires an exact reason and can keep/lower a retained archived balance; a missing zero placement still needs an active path. The list's separate adjustment action can create that explicit new-zero placement without pretending a balance already exists. Equal existing counts remain no-op receipts rather than fabricated movements.

Selectors use complete validated location hierarchies, searchable native controls and full ancestry. Unavailable original selections remain visible. Changed selected paths require an explicit recheck; neither the first available location nor a newly renamed/reparented path is silently installed into the proposal.

### Review, conflicts and uncertain outcomes

A write starts with a visible original-context review, then one explicit confirmation. Both proof scopes and request key remain inspectable. A new editor waits for its first current read instead of adopting stale cached data; later refetches retain its raw inputs. Known Item/Unit changes require an explicit current-versus-draft comparison/adoption. Unit row writes keep their independent proof rather than acquiring an extra Item-counter precondition. Quantity adoption retains raw count text; a changed measurement requires explicit whole-interpretation acknowledgement, never a partial tuple or automatic conversion.

A sent timeout/abort/malformed reply may have committed. Stop waiting does not undo it. The page retains and locks that original command, provides read-only current-state checks and an explicit same-key/body/proof retry, even after the current Unit disappeared. A frozen historical reply is never installed as current holdings; successful confirmation is followed by authoritative reads. Expired/conflicting keys cannot silently become new writes.

Dirty Back/navigation/reload has a stay/leave warning; Escape closes an unsent review or stops local pending waiting. Inputs remain volatile, not persistent drafts. An already initialized editor can retain input while offline, but a new offline visit does not invent a current authority from cached data. Reconnection refreshes reads only. Session loss clears private query/mutation/form context and returns to a whitelisted path after login; unsent amounts/secrets do not travel through history state. Long reviews use visible static focus, wrapping text and one keyboard-accessible outer scroller at enlarged text sizes.

QR printing, camera, photos, public history, dedicated Asset HTTP/browser workflows, offline replica/persistent drafts and full sync remain separate implementation/acceptance gates. No placeholder controls claim those capabilities.

## Distinct physical identities

A catalog Item describes a product. Its tracking mode is exclusively `individual` or `quantity`:

- A Unit is one individually tracked physical instance. It has its own stable ID/revision, immutable owning Item and server-assigned globally unique asset code. Commercial product barcodes can be shared; asset-code uniqueness does not turn a product barcode into a unique physical identity. Serial numbers are exact nullable text, not globally unique keys.
- A Stock balance belongs to one quantity-tracked Item at one location. Its identity is the canonical Unicode JSON pair `[item_id,location_id]`, not a new row UUID, displayed unit, cursor or Item revision. It has an independent retained positive revision. A zero balance remains meaningful referenced source/history, not an absent row.
- A movement is immutable history, not a replacement current Unit or Stock row. Source changes, required Item/location/search dependencies, retained counters, final current/frozen projections, immutable events and the original response must commit together. A correction appends a new authorized event; it never rewrites a posted event.

Item/Unit/location IDs contain 1–128 Unicode code points. Unit and Stock revisions are exact positive safe integers through `9007199254740991`. Imported identities remain stable; do not invent UUID-only input normalization. The retained asset-code representation is 1–10,000 code points without a new short-format restriction. The server, not a caller, assigns a new code. Notes contain 0–10,000 code points; serial numbers are null or exact strings of 0–10,000. Preserve whitespace, case, normalization and null versus supplied-empty serial text. Runtime input boundaries must additionally reject invalid UTF-8/lone-surrogate repair; JSON Schema length checks alone do not certify that behavior.

## Unit commands

Paths are relative to `/api/v1`. All seven Unit operations below are implemented HTTP routes delegating to `Inventory.GetUnit/PageUnits/CreateUnit/PatchUnit/MoveUnit/SetUnitStatus/DeleteUnit`. SQL and physical policy remain in the private transaction service, not handlers or a second database. The stateless typed adapters and responsive foreground screens use those same routes and original proof scopes.

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

Omitted serial/notes/status default to null/empty/`stored`; clients are not required to install those defaults. New Units may start as `stored`, `in_use`, `lent` or `lost`, not already `retired`/`disposed`. A new independent Unit must start at revision one and must not reuse a retained/tombstoned identity. The server assigns a fresh UUID and its `AST-<UUID>` asset code, never a client-selected code or a guessed short sequence; retained imported codes are not rewritten.

Ordinary metadata patches are nonempty and contain only `serial_number` and/or `notes`. Omission leaves a field unchanged; `serial_number: null` clears it, `serial_number: ""` preserves supplied-empty text, and `notes: ""` clears notes. A metadata patch cannot reassign the owner, change the asset code/location/status or post quantities, purchases or costs.

Movement names a required nonnull `target_location_id`; it does not move an entire location container. Moving a container uses the separate location operation and preserves each holding's actual assigned location. A Unit may leave a valid archived source path, but new placement requires complete active destination ancestry with real counters; missing/cyclic/deleted authority cannot become a new root. A value-equal destination is a no-op receipt, not a new movement.

Operational status (`stored`, `in_use`, `lent`, `lost`) never guesses an asset in-service date. Non-asset retirement retains physical placement and may be explicitly changed back to an operational state; a retired stored Unit can still move. Non-asset disposal requires one explicit physical action, with no implicit proceeds or currency:

```json
{
  "status": "disposed",
  "disposal_kind": "recycled",
  "note": "Explicitly reviewed physical removal"
}
```

The four accepted kinds are `sold`, `donated`, `recycled` and `discarded`. A kind is required only for `disposed` and forbidden on other states; financial fields are not accepted here. The discriminated root schema/generated type enforces this shape. Actual asset-backed retirement/disposal must instead use the separate asset lifecycle commands, with their dates/proceeds/accounting validation. Ordinary status cannot reactivate a retired asset or bypass an existing asset-disposal record. A disposed Unit cannot move to a different location or return to another lifecycle state through this operation; its last assigned location remains inspectable, while current-inventory membership excludes disposed Units. Exact serial/notes corrections remain metadata, not lifecycle restoration.

Repeating current status preserves an immutable receipt/note but does not increment counters, publish source/search/outbox changes or append another inventory event. Supplying another kind for an already disposed state is not correction/reclassification of its historical event. Every actual Unit mutation advances its independent counter, the Item aggregate and affected location ancestors once, and publishes only that Unit's approved search dependency; its identifiers/notes never leak into Item/sibling text. Original row edits do not acquire an extra Item proof.

Unit deletion removes an unbacked record, not an asset disposal. Any retained purchase line, asset profile, usage/cost or asset-disposal reference blocks it, including zero-valued acquisition. Successful deletion retains independent counters, immutable history and original acknowledgements, tombstones the Unit/search/owned media links and removes derived FTS without deleting shared media or another owner's links. Original creation/deletion keys still reconcile after source removal or a fresh same-principal login. Ordinary source deletion does not authorize erasing its history, resetting identity or changing Item tracking/measurement over retained events.

## Exact base quantities

Stock uses integer `quantity_base` in the owning Item's `base_unit`, never `quantity_minor`, a floating display amount or an unlabeled mixed-unit total. The read response always includes the Item's actual `unit_code`, `base_unit` and `scale_factor`. Their bounds/patterns match catalog measurement: 1–32-code-point display unit, a strict 1–32-character ASCII base identifier and a positive safe-integer scale.

For `kg/g/1000`, `quantity_base: 1500` means 1,500 grams. A display may show 1.5 kilograms, but a write still sends the integer 1500; the service does not guess or install conversion from a unit label. Quantity bounds are 0–`9007199254740991` for retained balances and absolute adjustment, and 1–that maximum for receive/consume/transfer. Exact mathematically integral JSON spellings can represent an integer; unsafe or fractional values must never be rounded into one. Runtime addition/subtraction checks are mandatory even when each input is individually safe.

## Stock commands and proof scopes

The four quantity commands below are implemented HTTP endpoints delegating to private Go `Inventory.Receive`, `Consume`, `Transfer` and `Adjust` with distinct typed inputs; `GET /inventory` delegates to the filter-scoped read service. Construction requires the owning store's `Auth` service; SQL remains private. Their HTTP boundary enforces the transport/admission rules below, and every service still revalidates authorization/proofs inside SQL. No inventory browser control or automatic submission queue is inferred from these endpoints.

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

## Stateless browser adapters

`web/src/lib/api/holdings.ts` exposes the actual same-origin browser SDK, not a separate database, background queue or production test endpoint:

- Reads: `fetchUnit`, `fetchItemUnitsPage`, `fetchStockPage`.
- Independent Unit commands: `createUnit`, `patchUnit`, `moveUnit`, `setUnitStatus`, `deleteUnit`.
- Quantity commands: `receiveStock`, `consumeStock`, `transferStock`, `adjustStock`.

Every call requires the caller's `AbortSignal`. Writes additionally take an in-memory CSRF token and original idempotency key. Unit creation accepts an original Item `expectedRevision`; existing Unit writes require the independent original Unit `expectedRevision` and retained `itemID`. That owner is validated against the response, not added as an invented concurrency header. Empty Unit patches, omitted creation-location decisions and ambiguous disposal are rejected at both the TypeScript and runtime boundaries. Optional defaults are not inserted into outgoing requests.

Stock writes take `StockWriteContext` with one complete reviewed `item: Item`, not a loose `expectedRevision` or Stock row. The adapter snapshots that Item, verifies quantity tracking/body ownership, and uses its revision for `If-Match`. It validates the affected response pair and full `unit_code`/`base_unit`/`scale_factor` interpretation against the same snapshot. Catalog and Stock share measurement validation; no unit-name conversion or independently refreshed proof is installed. The returned Stock counter can be lower than, equal to or higher than the submitted Item proof. Receive/transfer responses include at least the submitted quantity, consumption remains within safe subtraction bounds, and adjustment returns exactly its reviewed absolute count.

For a browser caller that already owns the reviewed Item, chosen location, CSRF token, original key and signal:

```ts
import { receiveStock } from './lib/api/holdings'

const body = {
  item_id: reviewedItem.id,
  target_location_id: reviewedLocation.id,
  quantity_base: 1000,
}
const original = { item: reviewedItem, csrfToken, idempotencyKey: originalKey, signal }

// Send only after foreground review and explicit confirmation.
const receipt = await receiveStock(body, original)
```

For `kg/g/1000`, that exact request receives 1,000 grams; it never sends a fractional display quantity. A timeout, abort or malformed reply after sending is uncertain, not proof of rollback. Keep the same volatile `body`/`original` for an explicitly chosen reconciliation attempt; do not fetch another Item merely to replace that command's proof. A retry can return an old frozen receipt after current holdings changed. Use fresh authoritative reads after confirmation, never install `receipt` as a current Item or balance. Logout/unmount/draft lifecycle remains the foreground caller's responsibility; the SDK itself persists nothing.

Resource responses require a matching strong ETag and exact identity/owner/requested fields. New Units require revision one; existing Unit replies may be unchanged no-ops or the independent next revision. Deletion requires the independent next acknowledgement and no ETag. Complete list responses require distinct identities, a nullable continuation, the requested/default page bound and no ETag; a continuation requires a full page and cannot repeat the submitted cursor. Unit cursors remain 2 KiB, Stock 4 KiB. `fetchStockPage({ item_id }, signal)` additionally rejects any row for another Item instead of silently filtering it away. Each adapter reads exactly one live page; collection completion and consistency review belong to its caller, not hidden pagination.

Reads have eight-second deadlines and writes thirty seconds. Caller cancellation discards late success; errors retain only shared allowlisted codes/status/retry metadata/request IDs, never arbitrary server messages, details or causes. No adapter reads credentials from storage, refreshes CSRF/proofs, retries, rebases, queues a mutation or submits on `online`.

## Complete reads and foreground review state

The state layer and responsive route composition in `web/src/features/holdings` are implemented and tested. They add no second public endpoint; physical actions remain separate from ordinary catalog metadata edits.

- `readItemHoldings` reads the actual Item, completes only its Unit collection or exact Item-scoped Stock collection, then checks the Item again. It rejects revision/field/owner/measurement inconsistencies, duplicate identities, repeated cursors and cancellation rather than returning a partial prefix. Stock keeps every real zero row. The discriminated result never invents an empty unqueried collection or a frozen/offline snapshot.
- `readUnitHolding` brackets one Item metadata read with matching independent Unit reads. It checks the same Unit/owner/tracking interpretation without reading all siblings or inventing another Item precondition for a Unit edit.
- Unit form helpers preserve nullable versus supplied-empty serial intent, exact Unicode/notes, an explicit nullable creation location and changed-only patches. Explicit metadata adoption treats serial text/provided state together, retains active invalid edits and adopts only untouched fields. A disposal kind is never inferred from a Unit's status, because that classification belongs to history; physical lifecycle bodies exclude proceeds, prices and asset dates.
- Stock forms accept canonical safe decimal **base-count text**, not display-unit floats, grouping, signed values, exponents, whitespace repair or unsafe numbers. The same parser checks positive catalog scale text. Receive/consume/transfer start without a guessed delta; adjustment may show an actual existing absolute count, but an absent row is not prefilled as zero. Inactive location/reason form fields never leak into another operation. No conflict/refetch automatically converts a draft or replaces its measurement/proof interpretation.

`freezeHoldingCommand` validates and clones one volatile original proposal/key for each of the five Unit or four Stock writers. It retains the complete original Item interpretation, independent Unit proof when applicable, actual reviewed balances and selected root-to-leaf location metadata. Full paths must match the selected identity without orphans, cycles, unrelated nodes or fake revisions. New/increased placement requires active ancestry; valid archived outflow, existing equal/lower adjustment and same-location Unit no-ops retain their distinct rules. Disposed Units cannot ordinarily move/reactivate. Financial deletion/lifecycle eligibility remains server-authoritative, not inferred by a browser preview.

`stockPreview` describes each source/target separately, with its actual prior Stock or null for an absent pair and an exact proposed count. It checks source sufficiency and safe target addition, preserves zero depletion, and distinguishes a retained zero from explicit new-zero placement. It never synthesizes a Stock revision/resource, rescales quantities, changes either balance or emits a mixed-unit total. The Go transaction must still validate every current source/path/proof and commit both legs atomically.

`sendHoldingCommand` dispatches only the original typed body, key and Item-versus-Unit proof. It never reads a newer Item, refreshes a balance, adopts a path, guesses financial input or rebases an uncertain command. Explicit reconciliation can therefore return an older frozen receipt after current balances changed or source was deleted.

`useHoldingMutation` reuses the shared foreground controller: review first, explicit single-flight confirmation, caller-visible request-token acquisition, cancel/unknown/session-loss handling and no retry/offline queue/reconnect submission. Confirmed or sent failed writes invalidate only holding, catalog, location and purchase dependent reads, never installing the receipt as current state. Catalog/owned/taxonomy mutations also invalidate affected holding/purchase reads; location changes invalidate location-dependent catalog/holding/purchase scopes. Purchase writes refresh purchase/catalog/holding interpretations without changing physical source. Auth and unrelated taxonomy roots are not refreshed by holding writes. The responsive screen composition provides visible reviews, dirty/Back guards, current-versus-original comparisons and explicit draft/path/measurement adoption; this does not imply persistent drafts, financial lifecycle or full sync.

## Movement direction, placement and no-op receipts

For quantity commands, the service records one independent immutable inventory event per actual command, with these meanings:

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

Unit history additionally carries the exact `item_unit_id`. `unit_create` records one physical instance, `unit_move` a conserved magnitude of one, `unit_edit` and `unit_status_*` a zero-count metadata/status change, and `unit_dispose_<kind>` a removal of one. `unit_delete` removes one only if the record was not already disposed, otherwise zero. These are physical-instance counts when a Unit subject is present, not conversions of the Item's optional measurement label. Metadata/history actions are not stock inflows; never sum all action magnitudes as a mixed-unit or net inventory scalar.

## Read completeness and admission

Unit and Stock list calls have 1–100 rows, default 50, stable Unit-ID or `(item_id,location_id)` order and required nullable `next_cursor`. Null terminates a page sequence. Cursors are opaque and scoped; they are not frozen sync snapshots. A first page is not a whole collection, and a missing owner is not an invented empty list. Do not synthesize revision one for a live row with missing authority. Source uniqueness, lookahead/cursor consistency and complete scope remain runtime checks beyond schema array limits.

`Inventory.GetUnit` reads one real independently versioned Unit. `Inventory.PageUnits` completes the requested existing Item's live Unit collection in stable ID order, including all retained lifecycle states and exact null/empty serial/location text. A missing Item is an error, not a fabricated empty page; an existing quantity Item with no Units has a real empty collection. Any retained Unit row with invalid owner/tracking/counter state fails the whole page, including an invalid lookahead row. Neither method reads cached projection bytes or publishes read-side state. These services are exposed by the exact Unit detail/collection HTTP routes above; both return private no-store responses, with a strong ETag only on the single Unit.

`Inventory.PageStock` implements the documented exact Item/date/currency/category/location/scope/status/merchant criteria. Optional `item_id` selects one exact 1–128-code-point Item identity through the existing balance-pair primary key before scanning/slicing unrelated rows. It is not widened by direct/subtree scope, normalized, interpreted as text search or implemented by filtering a first page in the browser. An unmatched identity or an Item without balances returns a real empty match; it does not prove that an Item exists or authorize synthesizing a zero row. Read the actual Item separately when preparing a review. Filters apply before page slicing: location matches each actual balance row, rather than every balance of an Item also found at another location; category/location scope defaults to subtree. Status is the owning Item's `active`/`archived` metadata, not a Unit lifecycle value. Retained zero balances remain readable. Date/currency/exact merchant must match the same associated purchase, using inclusive civil dates in the configured home timezone, not stock-movement timestamps or a 24-hour approximation. Different measurement tuples remain separate source rows with their own actual quantities/conversions, never a scalar total or a copied first Item's unit.

Both page services use the existing private 15-minute signed continuation boundary, with independent purposes and principal/epoch/page-size binding. Stock additionally binds every normalized filter, including optional exact `item_id`, and the resolved home-timezone string inside the same authorized transaction. Adding, changing or removing Item selection invalidates the continuation; absent `item_id` retains the original unfiltered scope spelling/MAC. Source rows may change/disappear between live pages; a continuation is not a frozen snapshot or authority, and a fresh same-principal login may continue it. Expiry, restored epochs, changed filters/timezone/size or another owner/collection cannot silently reinterpret a cursor.

Unit and existing metadata cursors retain their 2,048-byte limit and exact wire spelling. Stock alone permits 4,096 ASCII bytes for an opaque signed composite position: two full 128-code-point IDs containing controls can exceed 2 KiB after nested JSON/base64 encoding. The server validates canonical pair spelling and both original component bounds, then compares the actual SQL `(item_id,location_id)` tuple, not an escaped JSON string. It never truncates IDs, silently repairs positions, adds server-side cursor storage or changes offline identities. A malformed lookahead or an unencodable continuation withholds both data and cursor. Read/query/session/signing/COMMIT failures similarly return no resource or page prefix, without repairing source/counters or appending receipts/events.

Writes require exact Origin, an active completed administrator session, in-memory CSRF, supported application version, an original idempotency key and the original quoted positive proof. The lexical proof/ETag shape rejects zero, leading zeros, weak/list/wildcard tags and trailing data; the server also enforces the numeric safe bound and exact resource identity. A Unit response revision is independent from the Item proof used for creation. Deletion acknowledgements require the original Unit's next revision and a stable operation identity, never a list ETag.

Unit creation/metadata/status and stock write JSON are bounded to 128 KiB; a Unit move to 16 KiB. Read/delete bodies and content encodings are forbidden. Only the two collection reads accept documented query fields within 16 KiB raw bytes; resource reads/writes accept no query. Exact fields/duplicates/Unicode/integers and authorization are validated at the HTTP and SQL transaction boundaries. Unit creation requires the actual `location_id` member, even when null. Omission cannot silently install a null location. Serial omission/null/empty and partial-note presence remain exact. Stock queries do not accept catalog `q`; status is Item `active`/`archived`, with civil/filter semantics validated before pagination.

JSON must be unencoded `application/json` (optional UTF-8 charset), with exact case-sensitive names. Unknown fields, duplicate names, invalid UTF-8/lone surrogate escapes, trailing documents, null nonnullable fields and unsafe/fractional quantities are rejected without coercion. Mathematically integral decimal/exponent quantities are parsed exactly, not through float64. Path aliases, weak/list/wildcard/leading-zero proofs, duplicate write headers and unsupported versions are rejected. Imported `new`/`tree` IDs are not reserved by holding routes. All operations share the unchanged general 60/minute effective-peer budget; there is no privileged test/reset path.

A request canceled after sending may have committed. Retain the original body/key/proof for explicit foreground reconciliation, including after source deletion or a fresh same-principal login. Do not retry, refresh proofs, queue inventory writes or replay them on reconnect. Stock/unit mutation fingerprints, events, projections and responses share the domain transaction boundary, not credential mutation storage. Core physical CRUD must not require an AI worker.

## HTTP errors and explicit recovery

All responses are `Cache-Control: no-store`; holding replies never issue/change a session cookie. Unit/Stock bodies carry their own exact strong ETag. Collection and Unit-deletion replies have no ETag. Error bodies use only safe codes/messages/request IDs, never arbitrary SQL/source diagnostics; browser code allowlists codes and discards server details.

| Code | HTTP | Meaning and recovery |
| --- | --- | --- |
| `INVALID_HOLDING_URL`, `INVALID_QUERY`, `INVALID_CURSOR`, `INVALID_REVISION` | 400 | Use an exact route/original proof and restart only the affected read scope. |
| `VALIDATION_ERROR` | 422 | Correct exact field presence, Unicode, lifecycle shape or safe quantity; no silent conversion. |
| `TRACKING_MODE_CONFLICT` | 409 | Use the actual Item's individual/quantity workflow. |
| `INSUFFICIENT_STOCK`, `QUANTITY_OVERFLOW` | 409 | Read authoritative source/target and explicitly review a valid new quantity. |
| `LOCATION_ARCHIVED` | 409 | New placement requires complete active destination ancestry. |
| `UNIT_HAS_HISTORY` | 409 | Preserve referenced financial history; record deletion is not disposal. |
| `UNIT_DISPOSED`, `ASSET_LIFECYCLE_REQUIRED` | 409 | Do not move/reactivate disposed records or bypass asset lifecycle/accounting. |
| `PRECONDITION_REQUIRED`, `REVISION_CONFLICT`, `IDEMPOTENCY_CONFLICT`, `IDEMPOTENCY_EXPIRED` | 409 | Preserve original uncertain commands; explicitly read/review rather than refreshing proofs or retrying automatically. |

Session/CSRF/Origin/version/media/body/rate failures retain the shared [authentication and transport](http-authentication.md) behavior. An unknown/internal failure is a safe `503 SERVICE_UNAVAILABLE`, not success-shaped partial data. A timeout after sending can still mean COMMIT succeeded: explicit same-key reconciliation returns the historical resource/ETag, not current state. Re-read after confirmation; never install that frozen reply as a current Item/Unit/Stock proof.

## Retained persistence and final-state publication

`server/internal/store` contains the private Unit/Stock loaders and result validators. They read actual source and independent retained counters, not cached JSON or a synthetic revision one. They also require real bounded Item authority and the correct tracking mode. Unit null/supplied-empty fields remain distinct; all six retained lifecycle states are readable. Stock reads its current owner's complete measurement tuple without rescaling stored quantities. Missing source returns no resource; invalid UTF-8, field/counter/quantity/measurement state, mode mismatch or a failed query returns no partial DTO. These are internal transaction primitives, not a new unauthenticated read service or SQL handle exposed to HTTP.

Migration 0010 appends the Unit owner/stable-ID lookup index, missing independent Unit counters/current payloads and canonical-pair Stock counters/current payloads. Only absent counters begin at one; existing counters and already materialized payload bytes/timestamps/frozen changes remain unchanged. A retained counter without source never recreates the Unit/balance. Zero balances, raw Unicode/control text, nullable serial/location and all source quantities remain exact. The stock primary key remains its real Item/location pair.

Go validates every holding source and retained unit/stock counter after SQL but before migration/checksum commit. SQLite text length alone truncates at native NUL and is not full UTF-8 validation; noncanonical stock counter spelling must not silently become another revision-one identity. Invalid source/authority rolls back every pending DDL/index/backfill/checksum row, retaining the verified private pre-migration snapshot. Existing certified-root upgrades preserve epoch; explicit reference adoption still follows its documented epoch rotation. No pending upgrade repairs tracking modes, guesses conversion or publishes private/legacy FTS/OCR caches. This partial materialization does not enable all 22 sync entities.

The existing private domain transaction can now materialize explicitly staged Unit/Stock upserts and independent null tombstones from final source. Repeated staging publishes one final version per identity, not intermediate values or historical cache text. Source, staged Item/location/search dependencies, retained counters, current/frozen rows, mutation audit and original response commit together; SQL, safe-counter/sequence exhaustion, late authorization or deferred COMMIT failure withholds the result and restores all state. Reconciliation can return an older frozen Stock after later changes/deletion without installing it as current authority.

Migration 0011 adds exact note/reason fields and a deferred audit association to immutable inventory-event history, plus a note field on the existing immutable mutation audit so equal adjustments lose no context. A new movement gets a fresh independent event ID/counter and a `mutation_event_id` association to the same committed domain receipt. The association is not unique: a future real batch may have multiple movements under one audit. New producers leave the old globally unique raw movement `idempotency_key` null; administrator/epoch scoping remains owned by the existing domain receipt. The same caller key in a new dataset epoch must not collide with a global historical movement key.

Legacy movement values, original raw keys, current/frozen bytes, counters and epochs remain unchanged. New columns default to empty notes/reasons and a null association, never an invented mapping. Migration 0011 does not backfill/publish unverified legacy history, guess an instant from an old date-only value or recreate source from a counter. All old/new inventory rows are append-only after upgrade. New events use the same UTC instant as source/current/outbox/audit/receipt; the materializer validates strict RFC3339 spelling, bounded UTF-8/identities/quantities and real independent event authority before publication. Producer context stays server-side; the unchanged eight-field offline event payload has no inner revision, note, reason, audit ID or raw caller key.

Migration 0012 rebuilds only inventory-event storage to remove its live Unit FK while preserving the original nullable Unit ID as immutable historical identity. A Unit's own creation event must not make an explicitly unbacked record forever undeletable. The migration copies every original field/note/reason/audit association and keeps item/location/audit FKs, raw-key uniqueness, indexes, immutability triggers, current/frozen bytes, counters and epoch. Purchase/asset/ownership FKs remain restrictive. Pre-upgrade integrity rejects an already invalid old live reference; dropping the FK must not launder corrupt source. A failed ledger write rolls back the complete table/index/trigger rebuild and retains the verified recovery snapshot. No history is nulled, guessed, republished or erased, and no schema change is made to offline payloads.

Shared Unit search publication uses the same retained asset-code/serial/notes bounds as the Unit source (10,000 code points), not an obsolete 128-character asset-code restriction. Unit and catalog invalidators rebuild from final approved source, preserve exact valid wide/control text and withhold every partial result on invalid UTF-8/bounds or materialization failure. They never copy a poisoned legacy cache or broaden a Unit's private identifiers into its owner/siblings.

Financial and batch producers must still validate their own original proof, apply physical/accounting rules, append actual immutable history and stage every required dependency inside the same transaction. The payload builder does not infer or authorize those workflows or generic deletion of zero balances. Public history reads, foreground review UI and complete sync remain separate implementation gates.

## Verification and remaining implementation

```sh
uv run --frozen python -m unittest tests.test_holdings_contracts -v
uv run --frozen python -m unittest discover -s tests -v
go -C server test -count=1 ./internal/store -run '^TestHolding'
go -C server test -count=1 ./internal/store -run '^TestInventory'
go -C server test -count=1 ./internal/store -run '^TestHoldingRead'
go -C server test -count=1 ./internal/store -run '^TestUnit'
go -C server test -count=1 ./internal/httpapi -run '^TestHolding'
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzHoldingsJSON -fuzztime=3s -parallel=2
npm --prefix web run api:generate
npm --prefix web run api:check
npm --prefix web run typecheck
npm --prefix web test -- --coverage.enabled=false src/lib/api/holdings.test.ts
npm --prefix web test -- --coverage.enabled=false src/features/holdings
npm --prefix web run test:e2e -- tests/e2e/holdings-api.spec.ts --project=phone
make check-web
```

Schema checks cover complete bounded Unit/Stock resources, unchanged offline payload identities, nullable/empty/omitted serial metadata, explicit creation/location/status shapes, complete live cursors, safe base quantities and measurement tuples, Item-versus-Unit/Stock proof scopes, affected-response identity, body/query limits and a required deletion revision. Strict generated-type fixtures reject missing location/proofs/Origin, caller identity/measurement fields, terminal creation, null notes, numeric serial/quantity text, incomplete transfer/adjustment/measurement/page data and unversioned deletion acknowledgements. Generated TypeScript cannot enforce numeric ranges, Unicode, nonempty partials, database authority or arithmetic by itself.

Real SQLite persistence tests separately cover complete retained resources, source-versus-cache reads, actual owner/row authority, mode/measurement/Unicode/NUL bounds, zero balances, full-length canonical pairs and query plans. Fresh/reference/certified-root upgrades preserve source/counters/current/frozen bytes and roll back invalid rows, noncanonical authority and all pending DDL/ledger writes. Private publication fixtures replay exact deltas, coalesce final source and staged owner/location/search scopes, preserve source-only approved text and independent tombstones, and exercise every relevant source/projection/receipt/FTS failure plus late authorization/deferred COMMIT and exhausted counters/sequences. Those fixtures deliberately use test-only operations, not fictional public inventory services or invented physical-event acceptance.

The actual `Inventory` service tests separately drive all four public Go methods against real SQLite: original Item proofs versus independent Stock replies, exact signed direction/differences, maximum-safe arithmetic, canonical Unicode pairs, complete active/deep/archived paths, retained zero/new-zero/recreated-pair semantics, complete source/current/frozen delta replay and no-op context. They cover all original request fields, changed-proof/operation conflicts, fresh-login/frozen replies after later source changes/removal, expired claims, cancellation, epoch separation without raw-key collisions, transactional session gates, concurrent identical/competing commands and opposite-transfer conservation. Statement/authority/exhaustion/final-event-corruption/late-auth/deferred-COMMIT failures roll back both legs, all history/projections and the original receipt. Migration tests preserve legacy bytes/authority/publication and roll back all pending holding/event DDL/ledger rows; immutable event/audit triggers and nonunique deferred associations are exercised directly. These are real Go service tests, not HTTP or screen evidence.

The read-service suite exercises complete multi-page retained Unit/Stock collections, detail/list equality, all lifecycle/null/zero states, source-versus-cache behavior, real quantity writes and mixed measurement rows, actual location/category/status/purchase/date filtering before lookahead, maximum Unicode/control pair continuations, every filter/timezone/size binding and owner/collection isolation. It checks live source removal, same-principal fresh login, expired/malformed/signed-invalid positions, all read authorization gates, SQL/timezone/signing/session/late-expiry/deferred-COMMIT failures and no read-side publication. Root contract and strict generated-type fixtures separately pin the Stock-only cursor budget and Item metadata status; they do not by themselves certify the runtime browser adapter.

Exact Item-selection tests additionally cover maximum Unicode/control identities, combined filters, complete signed pages, old unfiltered MAC compatibility and bidirectional selection-scope rejection. A real SQLite query-plan assertion requires the pair-primary-key lookup; unrelated corrupt balances are excluded by SQL, while invalid selected authority still withholds every row. HTTP/SDK/browser cases preserve exact `item_id`, reject invalid/duplicate criteria and mismatched owners, and read only the selected Item even when another Item has balances in the same location. No new endpoint, fake zero placement or offline payload is introduced.

The Unit mutation suite drives all five public Go methods, independent original proofs, UUID/code assignment, null/empty/omitted metadata, all initial/operational/retired/disposed rules, explicit physical kinds and real asset/purchase guards. It covers complete source/current/frozen replay, Unit-only approved search, shared-media preservation, immutable history after source deletion, all original fields/operations/proofs, fresh login/epoch/expiry/cancellation, identical/competing concurrent requests and SQL/cascade/authority/counter/FTS/late-auth/deferred-COMMIT failures. Upgrade tests preserve every event value/current/frozen byte, old constraints and rollback the whole rebuild on failure; they reject invalid pre-upgrade live references instead of laundering them. Wide retained Unit search is checked through both Unit and catalog producers, including byte-preserving failure on invalid UTF-8/native-NUL bounds. These are Go/SQLite tests, not browser operating-screen or financial asset-lifecycle acceptance.

HTTP tests drive all twelve operations through real services and verified TLS/cookie jars, exact body/query/proof/header/path/method budgets, default-50 complete collections, independent Item-versus-row ETags, retained zeros, runtime tracking/arithmetic guards and original replies after changed/deleted source/fresh login. Every writer is revoked after middleware/body reading to verify SQL authorization; changed Item/Unit proofs are not rebased. Strict actual JSON readers have bounded fuzz coverage; browser-safe errors discard private details. The compiled CLI/server socket smoke additionally provisions a genuine account/password rotation, validates actual replies against executable root schemas, reconciles original keys, shuts down cleanly and checks private DB integrity. Those HTTP/socket checks alone are not browser operating-screen or physical-device acceptance.

SDK tests separately drive all twelve adapters with exact one-request transport, original proofs, in-flight snapshot isolation, strict inputs/resources/ETags/owner/measurement/quantity bounds, live-page completeness/uniqueness/cursor rules, no storage/reconnect replay, safe errors and explicit retry. Every adapter has its actual eight/thirty-second deadline checked; caller cancellation rejects late replies and cleans up timers. Strict compile fixtures require explicit nullable creation, nonempty Unit edits, original owner/proof contexts and one complete reviewed Item for stock, excluding loose proofs and caller financial/conversion fields. Private browser bundles import the real SDK under genuine Go setup/password rotation/session protection, exercise every operation, signed pages, stale proofs, retained zeros/no-ops and original frozen receipts after later changes/deletion. Malformed-owner/measurement and lost-reply cases first let the real mutation commit, then verify only an explicit identical-key/body/proof retry reconciles it without duplicating or rebasing effects. No SDK test asset enters the production build; credential artifacts remain disabled.

Responsive route/component tests additionally exercise complete Item collections, all nine reviewed writers, nullable/partial fields, no-op suppression, original independent proofs, selected paths, manual count/measurement/Unit adoption, missing source, safe login returns, stale-cache initialization, read/session failures, offline input and original-key reconciliation. Real Go screen workflows cover every operation, genuine concurrent changes, altered location labels, changed measurement acknowledgement, archived equal counts/new-zero placement and committed malformed receipt/deletion replies. Back/Escape, actual logout/login/scoped-list returns and 200% long reviews with light/dark axe are separate browser checks; screenshot inspection is not treated as physical-device evidence.

Still pending are public history reads, dedicated Asset lifecycle HTTP/browser workflows, QR/camera/media, physical holdings acceptance and complete sync. These are not marked passed by the Go/HTTP/SDK/screen tests above. Physical NAS/phone/operator TLS/VPN/off-host recovery gates remain blocked until actually performed.
