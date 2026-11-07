# Assets, Usage and Cost Accounting

**Availability: all eleven Go/HTTP financial operations below are implemented and tested.** Migrations 0015/0016, retained Asset/Usage/Cost/Disposal loaders, atomic source/publication, complete original-proof profile decisions, immutable usage/cost appends, physical/financial retirement/disposal, signed live collections, detail reads and current-posted calculations are available through the protected same-origin API. Strict readers, SQL revalidation after body reading, independent resource ETags, verified TLS/cookie jars, parser fuzz and actual compiled-server/socket response-schema checks cover these boundaries. Generated browser types are available; stateless runtime Asset adapters and reviewed responsive operating screens are **not implemented yet**. HTTP/service/schema tests are not browser or physical-device acceptance. Existing [physical Unit workflows](holdings.md) and [purchase workflows](purchases.md) remain functional. The existing [Purchase allocation operation](purchases.md) atomically integrates an authoritative matching purchase-basis Asset; manual basis, currency mismatch, invalid authority and unsafe complete totals remain guarded. It does not implicitly create or reinterpret a profile.

## Domain boundaries

An Item is shared catalog metadata. A Unit is one individually tracked physical object. An Asset is an explicit financial profile for one actual Unit; it is not created by receiving stock, purchasing an Item, editing Unit notes or changing an operational status.

- **Acquisition:** sum each `PurchaseLine.paid_minor` naming this exact Unit once, or use an explicit manual fallback. Never add a Purchase header total, multiply a Line amount by its quantity, borrow sibling/unallocated Item spend, or combine manual and purchase bases. A zero-paid Line remains financial history.
- **Usage:** immutable nonzero signed deltas in the profile's single `times`, `minutes` or `cycles` interpretation. A positive use and a referenced correction are distinct. Corrections never overwrite an earlier event.
- **Extra costs:** positive integer minor-unit magnitudes with explicit expense/credit direction. A real refund can be an ordinary credit; an adjustment must reference an actual earlier event of the same asset/currency.
- **Retirement:** end of service for day allocation. It does not remove the Unit from its stored location, sell it or create proceeds.
- **Disposal:** one explicit physical departure plus an explicit proceeds decision. It does not infer a service/retirement date or convert an estimated residual into realized money. Ordinary Unit status must not bypass a backed financial lifecycle.

Retained source is not a new-input proposal. Complete reads preserve bounded uppercase currency, exact offset instants, old absent correction provenance and historical non-sale proceeds without guessing missing facts or repricing history. New commands must satisfy authoritative current eligibility. No financial correction permits destructive Usage/Cost UPDATE/DELETE or a silent disposal overwrite.

## Contracted operations

Paths are relative to `/api/v1`. All eleven routes delegate to the private `Assets` transaction/read services; handlers neither expose SQL nor implement a second financial authority. A completed active session is required for every route. Writes additionally use the [normal same-origin protected headers](http-authentication.md) and the distinct original proofs below.

| Method | Path | Original preconditions | Returned resource |
| --- | --- | --- | --- |
| GET | `/assets` | Completed session; signed live filters/page | `AssetList`, no ETag |
| GET | `/units/{id}/asset` | Completed session; real Unit/Item authority | `Asset`, independent Asset ETag |
| PUT | `/units/{id}/asset` | Unit If-Match, Item `item_revision`, explicit nullable `asset_revision` | `Asset`, independent Asset ETag |
| GET | `/units/{id}/usage` | Completed session; signed owned page | `UsageList`, no ETag |
| POST | `/units/{id}/usage` | Asset If-Match | New `Usage`, independent event ETag |
| GET | `/units/{id}/costs` | Completed session; signed owned page | `CostList`, no ETag |
| POST | `/units/{id}/costs` | Asset If-Match | New `Cost`, independent event ETag |
| POST | `/units/{id}/retire` | Asset If-Match plus original `unit_revision` | `Unit`, independent Unit ETag |
| GET | `/units/{id}/disposal` | Completed session; actual retained disposal | `Disposal`, independent Disposal ETag |
| POST | `/units/{id}/disposal` | Asset If-Match plus original `unit_revision` | New `Disposal`, independent Disposal ETag |
| GET | `/units/{id}/cost-summary` | Completed session; optional exact `as_of` | `AssetCostSummary`, no resource ETag |

An Asset, Unit, Usage, Cost and Disposal have independent retained revision scopes. New immutable event/disposal identities start at one; a reply is never validated as an unrelated parent proof plus one. Missing actual counters or owners are invalid authority, not permission to fabricate revision one.

### Explicit profile intent

`AssetWrite` is a complete decision, not a PATCH. Every profile field is required, including nullable manual acquisition, service/retirement dates and residual. It additionally requires:

- Original Unit revision in `If-Match`.
- Original owning Item revision in `item_revision`, freezing the reviewed interpretation and acquisition dependencies.
- `asset_revision: null` for reviewed absence/opt-in, or the original independent Asset revision for replacement.

These are distinct proofs, not interchangeable numbers. If Unit and Asset both happen to have revision `7`, a create request with `asset_revision: null` must still reject a concurrently created profile. An allocation that changes the Item after an opt-in review must also be detected, even if it leaves the Unit's physical fields unchanged.

Example manual opt-in body for `PUT /units/{id}/asset`, sent with the reviewed original Unit `If-Match` and normal protected write headers:

```json
{
  "item_revision": 11,
  "asset_revision": null,
  "cost_basis": "manual",
  "currency": "JPY",
  "manual_acquisition_minor": 0,
  "in_service_at": null,
  "retired_at": null,
  "estimated_residual_minor": null,
  "usage_unit": "times"
}
```

For purchase basis, `manual_acquisition_minor` is explicitly null. For manual basis it is a nonnegative safe integer, including deliberate zero. A replacement supplies the original numeric `asset_revision`, retains the reviewed retirement date and cannot revive a disposed Unit. Retirement uses its separate explicit lifecycle command. Financial profile changes require immutable exact before/after/supplied-input audit; missing old history is never reconstructed from a later edit.

## Private Go profile command

`Assets.PutProfile` implements the complete opt-in/replacement decision behind the private store boundary and the protected profile PUT route; a browser operating screen remains pending. It checks the original Unit proof, actual owner's original Item proof and explicit nullable Asset proof together inside the authorized transaction. Reviewed absence cannot overwrite a concurrently created profile, even when Unit and Asset counters happen to be equal. New Asset identity must really begin at one; a retained counter-only identity, orphan financial history or failed absence query is not permission to repair/adopt source.

The command snapshots input pointers before fingerprinting and validates all actual financial source against the proposed interpretation through the same exact totals helper as summary reads. Complete manual/purchase exclusion includes zero-valued references; currency and usage interpretation must match all retained dependencies. New creation or changed monetary basis/currency/manual/residual requires the pinned finite ISO policy, even for zero money. An unchanged retained code can receive an eligible no-op or service-date correction without repricing or guessing an exponent. Explicit new currency and manual amount are an audited replacement decision, not automatic FX.

New/changed service instants normalize losslessly to the existing nine-digit UTC policy. Equal instants keep the original retained spelling. A replacement cannot change/clear retirement; its lifecycle command owns that decision. Initial historical retirement may be supplied only for an already retired/disposed Unit; null remains unknown. Profile opt-in/correction never changes Unit status, revives it, creates a movement or infers a disposal/proceeds record.

Migration **0016** adds private indexed immutable `asset_profile_changes`, tied to the actual profile and deferred mutation event. Each accepted new key stores nullable exact before-resource, resulting resource and complete original request/proofs, including equal intent. Legacy profiles receive no fabricated earlier change. Original public/frozen/offline payloads remain plain Asset data; private context never enters them. Every actual source/context write must affect one real row, and the final source must match the proposal; ignored, rewritten, failed, late-unauthorized or deferred-COMMIT work returns no partial success.

A real change advances only independent Asset and Item financial aggregates/timestamps, not unchanged Unit/location/search/history counters. An eligible equal decision is audited without new projections or source timestamps. Exact original-key replay returns the frozen result before current eligibility/proofs; later moves, current currency changes and fresh login never silently rebase or post another change. Expired claims remain retained and require explicit new review. The existing Purchase Line producer separately integrates compatible purchase-basis acquisition through the same authoritative transaction; profile PUT itself never posts an allocation.

## Purchase-backed acquisition integration

The existing [reviewed Purchase Line operation](purchases.md) now accepts a real authoritative purchase-basis Asset in the same currency. It validates actual profile/owner/counters, immutable acquisition/cost/usage/disposal and complete proposed exact totals after inserting the new Line. Original Purchase If-Match plus body Item revision remain the proofs: every financial/profile change already advances Item. The Line reply is still independent revision one, not an Asset/Purchase next proof.

A fresh zero-paid attribution is history and advances Asset authority even without changing its amount. Asset/Item/Purchase/Line/approved search/current/outbox/audit/original reply commit together; unallocated or sibling money never touches another Asset. Manual basis, mismatched currency, invalid source and unsafe totals remain guarded. No automatic opt-in, basis/currency conversion, service/retirement inference, Unit movement or private profile-decision before-image is added. Exact original-key reconciliation precedes current eligibility, while ASSET_TOTAL_OVERFLOW is a safe explicit conflict with no partial allocation. Purchase allocation remains a separate operation from the dedicated Asset routes and does not complete their pending management screens.

## Immutable event input

A normal use is positive. A correction has a nonzero signed delta and a required real original-event reference, not a replacement count:

```json
{
  "event_type": "correction",
  "delta": -1,
  "usage_unit": "times",
  "occurred_at": "2026-11-07T04:00:00.123456789Z",
  "correction_of": "original-use-id",
  "note": "Correct an overcount"
}
```

A new use may omit `correction_of` (server null) and `note` (server empty), but may not name a correction target. SQL must validate the target's actual owner/usage interpretation. Check chronological running totals as well as the lifetime total: an otherwise positive final sum must not hide a negative prefix from a backdated correction. Deltas at the same exact instant form one chronological step; arbitrary UUID ordering must not determine financial eligibility. Keep exact safe arithmetic; do not sum through floating-point counts.

Costs use positive `amount_minor`; `direction` supplies the sign. An adjustment requires a real earlier same-asset/same-currency event:

```json
{
  "kind": "adjustment",
  "direction": "credit",
  "amount_minor": 5000,
  "currency": "JPY",
  "occurred_at": "2026-11-07T04:00:00.123456789Z",
  "correction_of": "original-cost-id",
  "note": "Reverse an incorrectly posted expense"
}
```

Ordinary maintenance/repair/accessory/fee/refund input cannot supply an adjustment reference. Omitted ordinary `correction_of` becomes null; note omission becomes empty. A credit can make extra/net cost negative; never clamp it. New monetary input uses the pinned finite ISO 4217 policy and must match the actual profile currency; spelling alone is not registry, exponent, FX or historical eligibility evidence.

## Private Go event commands

`Assets.RecordUsage` and `Assets.RecordCost` append through the completed-session domain transaction. Both require the original independent Asset revision, not a Unit/Item proof or another event's counter. A correction or adjustment must name an already stored event of the same actual owner and usage-unit/currency interpretation, with real retained counters and a valid correction chain. Imported target IDs such as `validation` are not reserved. New events receive independent revision one; unchanged posted events and physical Unit counters are never advanced.

The command snapshots nullable reference pointers before fingerprinting. Positive uses have no correction reference; corrections are nonzero signed deltas, not replacement counts. Costs retain a positive whole minor-unit magnitude and explicit expense/credit direction; descriptive kind never supplies a sign or infers FX. New costs require the finite supported currency policy and the actual profile currency. Nonmonetary usage does not reprice an unchanged retained currency. New event instants preserve all nanoseconds in canonical nine-digit UTC, while existing source bytes, unknown provenance and exact bounded notes remain unchanged.

Before publication, the shared source-only financial helper completes all actual acquisition, costs, usage and disposal against the proposed history. This includes zero-paid acquisition locks, safe signed totals, same-instant usage grouping and every chronological nonnegative safe prefix. No current/FTS cache, truncated live page, local day denominator or unrelated timezone configuration becomes write authority. Signed cost cancellation may exceed an intermediate int64 magnitude without losing precision, but every published integer and usage prefix must be safe. Negative extra/net cost remains valid.

Source insertion, new event counter, Asset/Item financial revisions and timestamps, current/frozen publication, actual immutable audit and original response commit together. Each required source/publication/audit/session statement must affect its real row; ignored, rewritten, unsafe or late-unauthorized work returns no resource and rolls back all effects. New rows use real deferred audit references and leave old globally unique raw-key columns null. Events do not alter Unit status/placement, Stock, location/search counters, Purchase source, disposal proceeds or profile decision before-images.

Every accepted **fresh reviewed key** appends, even when its event fields equal a previous event. Only exact original-key reconciliation returns the frozen event before current proofs/eligibility; a later move, fresh login or currently invalid source must not silently fetch/rebase a proof or append again. Expired claims remain retained. These private services back the protected event POST routes; their separate signed history GETs do not imply a browser/offline replay queue.

## Explicit lifecycle review

Retirement and disposal require original Asset If-Match plus a required original physical `unit_revision`. A move can change Unit placement without changing the financial Asset revision, so one proof cannot silently replace the other. Retirement validates actual instants, preserves storage and returns the independent Unit resource.

Disposal requires kind, instant, currency and **explicit** `realized_proceeds_minor`, including zero. Sold permits nonnegative proceeds; donated/recycled/discarded/other require zero on new commands. The service must commit terminal Unit status, actual disposal, affected owner/path/financial projections, immutable audit and original reply together. It must not infer retirement dates, revise historical disposal in place or fabricate an Asset from ordinary Unit status.

A new session can read the exact disposal through the additional GET operation, including its note/currency/kind/proceeds. It must not reconstruct the decision from a volatile POST reply, Unit status or a guessed zero. Financial adjustment/void workflows for posted allocations and disposal remain separate unfinished work; these schemas do not implement them.

## Private Go lifecycle commands

`Assets.Retire` and `Assets.Dispose` check the original independent Asset revision and required original Unit revision in one completed-session domain transaction. They read actual source, complete current location ancestry and real counters, not cached financial totals or an extra Item proof. A later Unit move invalidates the physical proof even when the Asset revision is unchanged. Archived paths and unknown placement are not new placement; missing/cyclic authority still fails closed.

Retirement preserves placement and validates the exact end-of-service instant against any known start. An equivalent recorded instant retains its spelling; a different recorded retirement cannot be overwritten. Disposal preserves the inspectable last location while terminal status excludes current inventory. It never infers, clears or changes service/retirement dates, and no extra disposal-versus-service ordering rule is imposed. New disposal source requires supported matching currency, explicit proceeds including zero and zero non-sale proceeds. A different existing disposal decision requires the separate audited correction/void workflow.

A real physical status change publishes the independent Unit, affected Item/ancestor paths, Unit-only search and immutable movement together with Asset/Disposal financial dependencies. Physical movement uses the server-accepted instant; the separately supplied financial date remains explicit source/context. An already disposed retained Unit can receive an explicitly reviewed missing financial disposal without another physical removal, Unit/location/search revision or invented historical movement. Proposed complete financial totals are validated after insertion, so a safe proposed result is not rejected merely because the previous derived net was unsafe. Every published result must still be safe.

The existing immutable owner ledger stores full exact Asset and Unit before/after, nullable Disposal before/after and the original complete request/proofs under the actual deferred audit reference. No migration, legacy before-image or public/frozen/offline shape is added. Eligible equal lifecycle intent records context/audit/receipt without new projections or repricing retained money; fresh keys still validate complete current source. Exact original-key reconciliation returns its frozen independent Unit/Disposal result before current proofs/eligibility. Ignored or rewritten source, missing/reseeded counters, failed search/publication, late authorization and deferred COMMIT return no partial success. The protected retirement/disposal routes use these same commands; dedicated browser workflows remain pending, and the existing Purchase Line operation separately integrates compatible acquisition.

## Calculation policy

The contract defines current posted financial totals, not formal accounting depreciation, fair-market value or a historical financial snapshot. `as_of` bounds only elapsed service time; it does not undo a later correction or disposal. The response exposes the resolved home timezone, `day_policy: "inclusive_civil_days"` and same-transaction catalog `source_revision`; none is an Asset write proof.

| Field | Definition |
| --- | --- |
| `acquisition_minor` | Exact-Unit allocated paid amounts once, or explicit manual fallback |
| `extra_minor` | Expense magnitudes minus credit magnitudes |
| `realized_proceeds_minor` | Actual disposal proceeds; zero when no disposal exists |
| `net_cost_minor` | Acquisition + extra − actual proceeds; negative is valid |
| `projected_net_cost_minor` | Acquisition + extra − explicit residual; null without residual |
| `elapsed_days` | Inclusive home-civil dates from service start through min(as_of, retirement); null before/no service |
| `total_usage` | Exact signed immutable usage sum in the one declared usage unit |
| `per_day_minor` / `per_use_minor` | Realized net divided by a positive day/usage denominator; otherwise null |
| `projected_per_day_minor` / `projected_per_use_minor` | Explicitly projected net divided by the same positive denominators; otherwise null |

Never subtract both proceeds and residual in one net figure. Calendar days are not 24-hour durations: DST, repeated/skipped dates and exact chronological comparisons require runtime tests. A supplied service date does not imply purchase, retirement or disposal dates. Ratios are finite numerical cost allocations in declared minor units, not independently posted/rounded financial amounts. Integers and aggregate arithmetic must remain exact within the safe range; overflow fails closed instead of rounding or returning a partial result.

Example: JPY 120,000 acquisition + 5,000 repair − 20,000 actual sale = 105,000 realized-basis net; with 730 inclusive service days and 250 uses, approximately 143.84 JPY/day and exactly 420 JPY/use. Before an actual sale, realized-basis net is 125,000; an assumed residual of 20,000 belongs only to the separately labeled 105,000 projected figure. These formulas are implemented by the private Go calculation service and protected cost-summary GET; a browser calculation screen remains pending.

## Transport, lists and retained data

- All five writes require exact Origin, completed active session, CSRF, original key, compatible application version and the documented original proofs. JSON is at most 128 KiB with exact field names, no duplicate/unknown fields, strict UTF-8 and exact safe integer parsing. Writes accept no query parameters. Authorization and source validation must be repeated inside SQL after body reading.
- Financial instants use the same strict RFC3339/nanosecond policy as purchases: uppercase T/Z, years 0001–9999, valid calendar/offset and at most nine fractional digits. New/changed instants normalize losslessly to UTC; equal retained instants preserve original spelling. UTC year overflow and actual chronology remain runtime validation, not lexical comparison or schema evidence.
- Live lists default to 50, cap at 100, use 16 KiB query budgets and 2048-byte signed continuations. Every page has required nullable `next_cursor`; selected authority and lookahead must be validated before returning any prefix. Asset pages use stable Unit IDs; owned event pages use stable event IDs, not an unsafe lexical comparison of retained offset spellings.
- Global filters preserve category, Item active/archived status, profile currency, actual nondisposed Unit placement and direct/subtree scope. Purchase merchant/date criteria must match one purchase naming the **exact Unit**, not sibling or unallocated Item history. Civil dates use home timezone; cursors bind normalized criteria/timezone and principal/epoch/owner/size as appropriate. They are live pages, not frozen sync manifests.
- Retained responses expose complete source with independent resource ETags; lists/calculations have no ETag. Private provenance/keys/revision proofs do not enter offline payloads. All four original asset/usage/cost/disposal offline shapes and the full 22-entity envelope stay unchanged.
- Source, immutable events, counters, current/frozen projections, derived invalidation, audit and original response must commit atomically. Equal usage/cost fields under a **fresh key** append another event; only exact original-key replay deduplicates them. Eligible equal profile/lifecycle state can be a receipt-only no-op after its original proofs are checked. Exact original-key reconciliation happens before new eligibility; no background queue, automatic retry/rebase/reconnect replay or installation of historical replies as current data is permitted.

### HTTP usage and safe failures

Read the actual Unit, its owner Item and the Asset's present-or-absent state before reviewing profile opt-in/replacement. Only an actual `404 NOT_FOUND` means absence; a failed, canceled or unavailable read is not a null profile. Retain the original proof scopes through confirmation. Profile replacement is a complete PUT, not a changed-fields PATCH; never refresh or derive a proof inside a writer. Usage/cost writes need only the reviewed Asset proof. Retirement/disposal also need the reviewed original Unit proof because a later physical move can leave the Asset unchanged.

Global `GET /assets` accepts `from`, `to`, `currency`, `category_id`, `location_id`, `scope`, `status`, `merchant`, `limit` and `cursor`, once each. It does not accept `q` or `item_id`. `status` selects Item `active`/`archived`; `scope` is `direct`/`subtree` with the existing default-subtree policy. Merchant text is exact, including retained line breaks. Owned usage/cost GETs accept only `limit` and `cursor`. Summary GET accepts only optional `as_of`; URL-encode offsets containing `+`, retain all nine fractional digits and do not use its source revision as a write proof. Other resource reads and all writes accept no query parameters. Every GET forbids a request body or content encoding.

| Status / code | Meaning and safe response |
| --- | --- |
| `400 INVALID_ASSET_URL` / `INVALID_QUERY` / `INVALID_CURSOR` | Use an exact documented path/query; restart the appropriate live page when its signed continuation no longer matches. |
| `409 PRECONDITION_REQUIRED` / `REVISION_CONFLICT` | Supply the original required proofs, or explicitly refresh and review a new command; never silently rebase. |
| `409 IDEMPOTENCY_CONFLICT` / `IDEMPOTENCY_EXPIRED` | A key cannot be repurposed, and an expired claim cannot be treated as uncommitted. Review authoritative source before a new key. |
| `409 ASSET_CURRENCY_CONFLICT` / `ASSET_TOTAL_OVERFLOW` | New money is ineligible or complete proposed totals are unsafe. Do not convert, split, round or replay automatically. |
| `409 ASSET_HISTORY_LOCKED` / `UNIT_DISPOSED` / `ASSET_LIFECYCLE_REQUIRED` | Retain the recorded lifecycle; ordinary overwrite/revival/profile editing cannot bypass its dedicated review or unfinished audited correction/void workflow. |
| `422 VALIDATION_ERROR` | Correct complete nullable decisions, exact safe values, actual references, matching currency/usage interpretation or financial instants before confirming a new proposal. |
| `503 SERVICE_UNAVAILABLE` | Source/counter/proposed-history authority or storage is unavailable/inconsistent; no resource or private diagnostics are returned. Inspect and review source rather than repairing it from a cache. |

The normal authentication, Origin/CSRF, version, admission, media and size errors remain distinct. Error responses do not set a resource ETag or change the session cookie. Only bounded allowlisted codes/request metadata reach the shared browser error adapter, never arbitrary server messages or storage details.

## Private Go reads and calculation

The `Assets` service implements actual `Get`, `GetDisposal` and `CostSummary` reads through the existing completed-session transaction boundary. These read methods do not expose the SQL handle, create a profile, post financial history or update a frozen response into current authority; the HTTP layer only delegates to them. A missing Asset/disposal is not a synthesized zero/default resource. Final authorization, session-activity write and COMMIT failures withhold the complete result and roll back normal read activity.

`CostSummary` completes all actual source for the selected Unit in one transaction:

- Sum each exact-Unit Purchase Line's whole paid amount once, independently of its retained quantity. Validate real Line/Purchase/Item/Unit counters and ownership. Never borrow sibling/unallocated Item spend, header totals, cached projections or Stock. An explicit purchase basis with a fully read empty allocation collection totals zero; missing authority is not evidence of an empty collection. Manual fallback rejects any actual exact-Unit allocation, even zero-paid history.
- Validate every owned cost/usage representation and actual correction graph, matching currency and usage interpretation with the profile. Preserve readable unknown uppercase currency and historical correction provenance; new-money registry eligibility is not imposed retroactively. Signed extra costs use exact complete accumulation, including cancellation beyond intermediate int64 range. Published money and every chronological usage prefix must remain safe; unsafe results fail rather than wrap/round or return a prefix.
- Group equal **nanosecond** instants after exact UTC interpretation, including equivalent offset spellings. Validate every chronological usage step and final sum, not arbitrary event-ID order or only the lifetime result. Do not truncate to whole seconds/milliseconds or silently paginate history at 50/100 rows.
- Read actual disposal proceeds independently of residual. Preserve negative realized/projected net; return required null fields for absent residual/service and zero usage. Convert only already-safe totals to finite per-day/per-use ratios; these are allocations, not posted money.
- Interpret `1 + days_between` as an inclusive proleptic Gregorian **home-date-label range** after exact start/end/retirement comparisons. A skipped calendar label remains in that range; a repeated label does not add another day. Use no elapsed 24-hour division, nonexistent local-midnight normalization or saturated long `time.Duration`. Before service gives null. A historical backward dateline jump that reverses the civil date range fails closed instead of clamping a nonpositive divisor. The supported tests include years 0001–9999 and home-year boundary crossings.
- A supplied `as_of` retains nanosecond precision in canonical UTC; omission uses the server clock. Only the day denominator is bounded by it. Complete current posted money/usage/proceeds remain included, even when their declared dates are later. Resolve the actual `preferences.timezone` setting (UTC only when absent), validate same-transaction catalog `source_revision` including real zero, and never install a calculation as an Asset write proof.

Tests cover large complete histories, exact signed cancellation, zero/manual/purchase conflicts, cross-currency/usage incompatibility, negative/unsafe prefixes, actual correction cycles, missing own/related counters, corrupt scans, late SQLite iterator failures, configured timezone/revision failures and final authorization/deferred-COMMIT rollback. These are private Go/SQLite service checks, distinct from the additional HTTP checks below and from hardware acceptance. Dedicated browser workflows and audited financial adjustment/void remain unfinished.

## Private Go live collections

`Assets.Page`, `Assets.PageUsage` and `Assets.PageCosts` reuse the existing completed-session metadata paging boundary. All return complete `data` plus required nullable `next_cursor`, with stable Unit-ID or event-ID ordering, actual independent counters and validated lookahead. A missing real owner/counter, invalid selected row/reference, failed query/signature, late authorization or failed activity/COMMIT withholds the entire result rather than returning a prefix. Empty owned history requires an actual Asset/Unit/Item, not a synthesized profile.

Global pages apply current Item category/active-or-archived status, profile currency and that exact nondisposed Unit's current direct/subtree placement before slicing. Profile currency is not a Purchase-header currency filter. Exact stored merchant and inclusive home-civil purchase dates must match the same actual Purchase naming this Unit; sibling and unallocated Item history cannot qualify it. Validate every matching Line/header/owner/counter, even after one successful match. Stream candidates until a validated matching lookahead exists rather than filtering a raw first page. These are purchase dates, not service/retirement dates or elapsed 24-hour windows. Resolve the configured home timezone in the same transaction, using UTC only when absent.

Owned event pages validate retained representation, real owner authority and complete same-owner/interpretation correction tails, including targets outside the returned page. Reuse verified tails within a page. Preserve exact offsets, bounded native text, unknown legacy provenance and readable retained currencies; do not reprice, impose new positive-use/reference policy, calculate current totals or require unrelated home-timezone settings. A retained page is history for review, not financial eligibility or a current usage total.

Independent 15-minute/2048-byte cursor purposes bind principal, epoch, size, collection/owner and normalized global filters/timezone. Default subtree is equivalent to explicit subtree; a changed effective scope is not. Existing metadata/Stock/Purchase purposes and token spellings remain untouched. Continuations are live across valid current changes and same-principal login, not revision-frozen snapshots, authorization or the full offline sync protocol. Reads never publish/repair source, install frozen data or fabricate audit/provenance. These methods back the three protected collection GETs; browser review models and screens remain separate work.

## Retained persistence foundation

Migration **0015** and the private store loaders/materializers implement storage authority, not the financial commands listed above:

- Rebuild the profile and all three referencing history tables as one group with foreign keys continuously enabled. New child tables reference the new temporary parent, old children are dropped before the old parent, and final renames retain the real Unit/profile/self/audit references. Preflight rejects invalid old references before any rebuild; it never launders them by dropping constraints or deleting history.
- Replace the reference profile's lexical retirement/service CHECK with exact Go instant validation inside the same migration/checksum transaction. Differently offset RFC3339 strings are not chronologically comparable as text. Preserve valid exact spellings; reject reversed, malformed, overprecision or UTC-unrepresentable source rather than normalizing it during migration.
- Preserve every original profile/event/disposal value, raw legacy key and private creation spelling. Add nullable `correction_of` for Usage/Cost and nullable deferred `mutation_event_id` for Usage/Cost/Disposal. Unknown historical provenance remains null; no original event, date or audit is invented. Explicit new producer associations require valid creation instants and real committed audit references. Audit indexes are nonunique; old raw-key uniqueness is retained, while new scoped producers leave raw keys null.
- Posted usage/cost/disposal rows are immutable, including value-equal UPDATE. Profile ownership is immutable and profile deletion is prohibited. The separate migration-0016/profile producer adds exact before-image audit; migration 0015 alone does not make profile changes a public audited command.
- Materialize only existing source into independent Asset/Usage/Cost/Disposal identities. Keep real prior counters, current bytes/timestamps, frozen outbox rows and a certified root's epoch; counter-only identities do not resurrect source. Only an actual newly backfilled source identity can begin at one. Explicit historical-reference adoption retains its separate epoch-rotation policy.
- Validate bounded UTF-8/native-control text, exact safe amounts/counts, dates, actual individual Unit/Item source and all real parent counters. Correction chains require actual same-owner/same-interpretation targets and reject cycles without recursion. Backfill memoizes verified tails; a 5,000-event algorithm test verifies linear traversal, not a NAS workload measurement. Retained representation is not proof of new-money, acquisition-basis or aggregate financial eligibility.
- Private publication rebuilds each payload once from validated final source, with independent counters and exact unchanged offline shapes. Source/counters/current/frozen rows/derived invalidation/audit/original reply use the existing atomic boundary; failed statements, unsafe source/counters, late authorization or deferred COMMIT return no partial resource. Counter-only tombstones never erase posted source. Approved-search/physical dependencies are explicitly staged by their producer, not inferred from a cache or automatically created by a loader.

The native CLI has been driven on disposable fresh and certified schema-14 databases: actual schema-15 upgrade/check, independently verified original snapshots, exact source/counter/current/frozen/epoch preservation, five offline record-schema validations and rollback of a lexically acceptable but chronologically invalid service interval. Private transaction tests are separate from public financial command/HTTP/browser acceptance. No old rows are republished as new receipts/events, no unreviewed cache enters publication, and this partial backfill alone never authorizes financial commands or complete sync.

## Verification boundary

Executable negative contracts cover complete bounded resources, explicit proof/null intent, machine-discriminated bases/corrections/disposal, exact money/count/instant inputs, body/query/admission, full live pages and unchanged offline envelopes. Strict generated-type fixtures also exercise the real typed transport, including required null without actual type-marker objects.

All eleven routes are exercised against actual transaction services and a certificate-verified TLS server/cookie jar. Boundary tests cover anonymous/mandatory/revoked sessions, exact Origin/Host/transport, every write header, strong independent proofs, methods/path/query/read-body admission and all five body-time revocations. Concurrent Item/profile/Unit changes are rechecked inside SQL; unrelated physical metadata does not become an extra event proof. Original keys bind owner/operation/full typed intent/proofs and preserve frozen replies after later state and a fresh login. Real services create 51 profiles and 51 events in each owned collection to check default-50 completion, distinct identities and cross-scope/size/owner cursor rejection without weaker admission or test endpoints.

The five production JSON readers additionally exercise required null/zero presence, exact integral numeric forms versus unsafe/fractional input, discriminated references/kinds, bounded native Unicode notes, duplicate/unknown fields, ambiguous media, chunked exact/over budgets and failed reads. Run their actual parser fuzz campaign with:

```sh
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzAssetsJSON -fuzztime=3s -parallel=2
```

The compiled native CLI/server has separately been driven on a private disposable loopback database through genuine bootstrap/setup/login/password rotation, all eleven financial operations and strict root response-schema checks. That run includes public purchase-basis opt-in plus compatible allocation, independent ETags, signed terminal global/owned pages, exact negative-net/separate-residual/service-day calculation, original-key reconciliation after a fresh login, graceful shutdown, CLI integrity and private-data cleanup. This is app/socket evidence, not the private service-serialization probe, browser UI or physical acceptance.

Still required: stateless runtime browser adapters, reviewed responsive screens, allocation/disposal adjustment/void integration, analytics and device/operator acceptance. Implemented persistence/services/HTTP do not establish those remaining gates. No hardware, hosted CI or completed-product claim follows from this document.
