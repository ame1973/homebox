# Purchases and Financial Allocations

**Availability:** the root OpenAPI contract, generated browser types and executable wire/type tests cover the seven purchase operations below. Authoritative purchase services/materialization, HTTP handlers, stateless runtime adapters and reviewed operating screens are not implemented yet. These are implementation rules, not instructions for working endpoints. Existing [catalog](catalog.md) and [physical Unit/Stock](holdings.md) operations remain functional without purchase entry or an AI worker.

## Header, allocation and physical receipt

A **Purchase** records one exact merchant name, purchase instant, currency and receipt note. It has no second monetary total. A **PurchaseLine** is an independently identified allocation to one Item, optionally one of that Item's individual Units. Its `paid_minor` is the entire amount allocated to the line, including allocated tax/shipping exactly once. It is not a unit price and is never multiplied by `quantity_base` when reporting spend.

For example, a JPY purchase split between two Units can have two lines of `60000` minor units each. Historical spend is `120000`, not a header total plus those lines, and not another multiplication by a purchase count. A quantity purchase can allocate `120000` to `1500` base grams without creating a floating per-gram price. JPY uses zero decimal places; other currencies require their own minor-unit display policy. No implicit FX or cross-currency scalar is permitted.

A zero-paid allocation is still real financial history. It prevents naive Item measurement changes or deletion of referenced records just as a paid allocation does. Header creation or allocation does **not** receive stock, create/move a Unit, establish a location, install media or enable an asset. Separate explicitly reviewed commands perform those operations; an eventual quick-create workflow must coordinate them atomically rather than replay independent writes.

## Operations and original proofs

All paths are relative to `/api/v1` and require a completed active administrator session. All responses are `Cache-Control: no-store`.

| Operation | Original concurrency proof | Contract result |
| --- | --- | --- |
| `GET /purchases` | None; scoped live continuation | Complete `PurchaseList` page |
| `POST /purchases` | No `If-Match` | New `Purchase`, revision one and its ETag |
| `GET /purchases/{id}` | None | Actual `Purchase` and its own ETag |
| `PATCH /purchases/{id}` | Original Purchase aggregate revision | Frozen corrected `Purchase` and its ETag |
| `GET /items/{id}/purchases` | Actual Item existence/counter, not a write proof | Complete distinct associated `PurchaseList` page |
| `GET /purchases/{id}/lines` | Actual Purchase existence/counter, not a write proof | Complete independent `PurchaseLineList` page |
| `POST /purchases/{id}/lines` | Original **Purchase** aggregate revision | New independent `PurchaseLine`, revision one and its own ETag |

Line creation does not use the Item revision, Unit revision or another Line's ETag as its `If-Match`. It advances the real owning Purchase aggregate. The returned Line counter is independent, not Purchase-proof-plus-one; a caller wanting the current header must read it again. Lists have no ETag and never claim frozen-snapshot isolation.

Protected writes require exact same-origin `Origin`, CSRF, supported application version and an original idempotency key. Proofs are canonical strong quoted positive safe integers. Body parsing must reject duplicate/unknown/case-mismatched fields, invalid UTF-8/surrogates and lossy numeric coercion. JSON writes have a 128 KiB limit and no query. Lists have a 16 KiB raw-query budget and no body; detail has neither body nor query. See [transport and authentication](http-authentication.md) for the existing shared security boundary; it does not imply purchase handlers exist.

## Exact values and explicit allocation

- IDs contain 1–128 Unicode code points; retain exact identity, including imported `new`/`tree`, rather than trimming or substituting another record. Each resource has its own revision in `1..9007199254740991`.
- Merchant names contain 1–300 code points. Receipt/line notes contain 0–10,000 code points and preserve supplied whitespace, normalization and line breaks. Empty text is distinct from omission; text is not nullable. Omitted creation notes default to empty on the server, not by modifying outgoing client commands.
- Purchase instants are actual RFC3339 timestamps with uppercase `T`/`Z`, years 0001–9999, valid calendar/time/offset fields and at most nine fractional digits. Reject date-only/guessed instants, comma fractions, leap seconds, invalid offsets and precision that would be truncated. Normalize accepted timestamps to UTC without losing nanosecond precision; reject a canonical UTC result outside years 0001–9999 rather than overflowing an offset-boundary date. HTTP format validation alone is not runtime date-parsing evidence.
- Currency is explicit three-uppercase-ASCII-letter code text; code spelling does not by itself certify an ISO registry entry, currency exponent or financial compatibility. Runtime currency policy and exact display conversion must be tested separately. Currency belongs to the header, not a caller-supplied Line override.
- `quantity_base` is an exact positive safe JSON integer in the Item's existing base measurement; `paid_minor` is a separate exact nonnegative safe JSON integer in the Purchase currency. Do not use `quantity_minor`, display floats, decimal strings, an inferred price, hidden scale conversion or a mixed total. Booleans and fractional/unsafe numbers are invalid. Mathematically exact integral JSON exponent/decimal spellings remain numbers; the server must parse them without float rounding.
- `item_unit_id` is required in allocation input, including explicit null. Null means no named individual Unit allocation, not an instruction to create one. A supplied Unit must actually belong to the same individual Item and `quantity_base` must be exactly one. Quantity-tracked Items require null. An unallocated individual bulk count remains explicit and does not silently charge each existing Unit.
- Actual retained counters/source, tracking, ownership and asset currency/manual-basis compatibility must be checked transactionally. Schema/type acceptance is not proof of financial eligibility. A manual-basis asset cannot silently gain a second purchase acquisition basis; incompatible currency cannot be repaired by relabeling amounts.

## Descriptive correction and immutable history

`PurchasePatch` is a nonempty partial descriptive correction. Only merchant name, purchase instant, currency and receipt note can be supplied. Omitted fields remain unchanged; a supplied empty receipt note clears it. Currency cannot change after **any** allocation exists, including when every amount is zero. A value-equal correction retains an audited no-op receipt without inventing a new resource revision.

Creation, correction and allocation must use the existing private domain transaction boundary: business source, relevant aggregate/independent counters, current projections, frozen outbox rows, immutable audit and original idempotent response commit together. Publish final source once per identity. Do not return success before COMMIT, seed revision one over retained identity or let projection/FTS caches become authority. Purchase evidence changes must rebuild affected approved Item/Unit text from source, with Unit evidence scoped to its exact Item/Unit, and invalidate dependent analytics in the same transaction; raw/unreviewed OCR remains excluded.

Posted allocations are not edited/deleted through descriptive header correction. The seven operations do not yet provide a separate financial adjustment/void workflow; that remaining work must preserve prior audit/history and financial/asset invariants, not introduce a silent destructive line PATCH or negative physical stock. The contract does not make that unfinished workflow available.

Domain keys bind administrator, dataset epoch, operation, exact typed command and original proof. An explicit identical-key retry returns the original committed result after later changes, without installing it as current state. Changed body/proof/operation conflicts. Expired claims must not be removed/reused to duplicate spend. Never queue financial writes for automatic reconnect submission, silently refresh proofs or rebase a proposal. Foreground review and authoritative validation are mandatory.

## Complete live reads and filter meaning

All three collections use stable ID order, default page size 50, maximum 100, bounded 2048-byte opaque continuations and an explicit required `next_cursor`, null at the end. Duplicate associated lines do not duplicate a Purchase header. Validate lookahead/owner/counters before exposing a complete page; a corrupt or failed prefix is not a successful list. Signed continuations bind the authenticated principal, dataset epoch, page size and actual collection/owner scope for 15 minutes; they are not authorization or frozen sync cursors.

The global purchase collection retains all documented filters: `from`, `to`, `currency`, `category_id`, `location_id`, `scope`, `status`, `merchant`, `limit` and `cursor`. Apply them together before slicing:

- Dates/currency/exact stored merchant name match the Purchase header. Dates are inclusive civil dates of the purchase instant in configured home timezone, UTC only when unset. Validate `from <= to`; use actual local civil dates across DST/repeated/skipped dates rather than fixed 24-hour windows or a guessed midnight. Bind the resolved timezone and every effective normalized filter into the cursor scope.
- Category, Item active/archived metadata status and location must match the **same associated line**, not different qualifying Items in the same purchase. Scope is direct/subtree and defaults to subtree; it does not duplicate headers.
- A named Unit allocation uses that actual nondisposed Unit's current location, not a sibling Unit elsewhere. An unallocated line uses its Item's current positive quantity balances or nondisposed Units. A financial allocation alone does not prove current placement; retained zero balances/disposed Units do not establish it. This is current placement filtering, not historical purchase-location reconstruction.
- Unallocated headers remain visible when no Item-dependent criterion is requested. An existing Item with no allocations has an empty associated page. Missing owners are not invented. Item-associated and Purchase-owned line routes accept only their owner path and paging fields, not silently ignored global filters.

## Verification and remaining work

`tests/test_purchases_contracts.py` exercises bounded complete resources, exact/null/omitted input semantics, single-Unit versus bulk allocation, safe amount/count extrema, strict instants, original proof versus independent ETag, complete pages, preserved filters and unchanged offline payload envelopes. `web/tests/contract-types.ts` exercises generated required fields, explicit nullable allocation/one-Unit discrimination, write headers and filter scopes with strict library checking. Generate declarations only through `npm --prefix web run api:generate` and verify drift through `make check-web`.

These tests do not establish private Go purchase loaders/backfills/services, immutable SQL history, actual HTTP/SDK/UI behavior, asset adjustment math, receipts/media, the complete offline replica, physical phones or production NAS acceptance. Those remain separate implementation/testing gates, not dropped product scope. Existing offline `purchase`/`purchase_line` payloads and all original operation identities are preserved; tightening their HTTP boundaries does not enable the complete 22-entity sync protocol.
