# Assets, Usage and Cost Accounting

**Availability: tested persistence, private Go reads/calculations and executable contracts.** Migration 0015, retained Asset/Usage/Cost/Disposal loaders, final-state publication and the authorized Go `Assets.Get`, `Assets.GetDisposal` and `Assets.CostSummary` services are implemented. The eleven operations below have executable root schemas and generated browser types, but no public Asset HTTP route, runtime browser adapter or operating screen is implemented yet; financial writes and signed collections also remain unfinished. Passing private service/schema/type tests is not public financial transaction, browser or device acceptance. Existing [physical Unit workflows](holdings.md) and [purchase workflows](purchases.md) remain functional. The purchase service deliberately rejects new allocations to an existing asset profile until its dedicated financial producer can update Asset authority/current/outbox atomically.

## Domain boundaries

An Item is shared catalog metadata. A Unit is one individually tracked physical object. An Asset is an explicit financial profile for one actual Unit; it is not created by receiving stock, purchasing an Item, editing Unit notes or changing an operational status.

- **Acquisition:** sum each `PurchaseLine.paid_minor` naming this exact Unit once, or use an explicit manual fallback. Never add a Purchase header total, multiply a Line amount by its quantity, borrow sibling/unallocated Item spend, or combine manual and purchase bases. A zero-paid Line remains financial history.
- **Usage:** immutable nonzero signed deltas in the profile's single `times`, `minutes` or `cycles` interpretation. A positive use and a referenced correction are distinct. Corrections never overwrite an earlier event.
- **Extra costs:** positive integer minor-unit magnitudes with explicit expense/credit direction. A real refund can be an ordinary credit; an adjustment must reference an actual earlier event of the same asset/currency.
- **Retirement:** end of service for day allocation. It does not remove the Unit from its stored location, sell it or create proceeds.
- **Disposal:** one explicit physical departure plus an explicit proceeds decision. It does not infer a service/retirement date or convert an estimated residual into realized money. Ordinary Unit status must not bypass a backed financial lifecycle.

Retained source is not a new-input proposal. Complete reads preserve bounded uppercase currency, exact offset instants, old absent correction provenance and historical non-sale proceeds without guessing missing facts or repricing history. New commands must satisfy authoritative current eligibility. No financial correction permits destructive Usage/Cost UPDATE/DELETE or a silent disposal overwrite.

## Contracted operations

Paths are relative to `/api/v1`. **These are not available HTTP endpoints yet.** The matching private Go `Assets.Get`, `Assets.GetDisposal` and `Assets.CostSummary` methods are implemented; financial writers and signed collection methods are not.

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

Schema-valid proposed manual opt-in body (not an executable HTTP example):

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

## Explicit lifecycle review

Retirement and disposal require original Asset If-Match plus a required original physical `unit_revision`. A move can change Unit placement without changing the financial Asset revision, so one proof cannot silently replace the other. Retirement validates actual instants, preserves storage and returns the independent Unit resource.

Disposal requires kind, instant, currency and **explicit** `realized_proceeds_minor`, including zero. Sold permits nonnegative proceeds; donated/recycled/discarded/other require zero on new commands. The service must commit terminal Unit status, actual disposal, affected owner/path/financial projections, immutable audit and original reply together. It must not infer retirement dates, revise historical disposal in place or fabricate an Asset from ordinary Unit status.

A new session can read the exact disposal through the additional GET operation, including its note/currency/kind/proceeds. It must not reconstruct the decision from a volatile POST reply, Unit status or a guessed zero. Financial adjustment/void workflows for posted allocations and disposal remain separate unfinished work; these schemas do not implement them.

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

Example: JPY 120,000 acquisition + 5,000 repair − 20,000 actual sale = 105,000 realized-basis net; with 730 inclusive service days and 250 uses, approximately 143.84 JPY/day and exactly 420 JPY/use. Before an actual sale, realized-basis net is 125,000; an assumed residual of 20,000 belongs only to the separately labeled 105,000 projected figure. These formulas are implemented by the private Go calculation service; no public summary HTTP route or browser screen is available yet.

## Transport, lists and retained data

- All five writes require exact Origin, completed active session, CSRF, original key, compatible application version and the documented original proofs. JSON is at most 128 KiB with exact field names, no duplicate/unknown fields, strict UTF-8 and exact safe integer parsing. Writes accept no query parameters. Authorization and source validation must be repeated inside SQL after body reading.
- Financial instants use the same strict RFC3339/nanosecond policy as purchases: uppercase T/Z, years 0001–9999, valid calendar/offset and at most nine fractional digits. New/changed instants normalize losslessly to UTC; equal retained instants preserve original spelling. UTC year overflow and actual chronology remain runtime validation, not lexical comparison or schema evidence.
- Live lists default to 50, cap at 100, use 16 KiB query budgets and 2048-byte signed continuations. Every page has required nullable `next_cursor`; selected authority and lookahead must be validated before returning any prefix. Asset pages use stable Unit IDs; owned event pages use stable event IDs, not an unsafe lexical comparison of retained offset spellings.
- Global filters preserve category, Item active/archived status, profile currency, actual nondisposed Unit placement and direct/subtree scope. Purchase merchant/date criteria must match one purchase naming the **exact Unit**, not sibling or unallocated Item history. Civil dates use home timezone; cursors bind normalized criteria/timezone and principal/epoch/owner/size as appropriate. They are live pages, not frozen sync manifests.
- Retained responses expose complete source with independent resource ETags; lists/calculations have no ETag. Private provenance/keys/revision proofs do not enter offline payloads. All four original asset/usage/cost/disposal offline shapes and the full 22-entity envelope stay unchanged.
- Source, immutable events, counters, current/frozen projections, derived invalidation, audit and original response must commit atomically. Equal usage/cost fields under a **fresh key** append another event; only exact original-key replay deduplicates them. Eligible equal profile/lifecycle state can be a receipt-only no-op after its original proofs are checked. Exact original-key reconciliation happens before new eligibility; no background queue, automatic retry/rebase/reconnect replay or installation of historical replies as current data is permitted.

## Private Go reads and calculation

The `Assets` service implements actual `Get`, `GetDisposal` and `CostSummary` reads through the existing completed-session transaction boundary. It does not expose the SQL handle, create a profile, post financial history, add an HTTP route or update a frozen response into current authority. A missing Asset/disposal is not a synthesized zero/default resource. Final authorization, session-activity write and COMMIT failures withhold the complete result and roll back normal read activity.

`CostSummary` completes all actual source for the selected Unit in one transaction:

- Sum each exact-Unit Purchase Line's whole paid amount once, independently of its retained quantity. Validate real Line/Purchase/Item/Unit counters and ownership. Never borrow sibling/unallocated Item spend, header totals, cached projections or Stock. An explicit purchase basis with a fully read empty allocation collection totals zero; missing authority is not evidence of an empty collection. Manual fallback rejects any actual exact-Unit allocation, even zero-paid history.
- Validate every owned cost/usage representation and actual correction graph, matching currency and usage interpretation with the profile. Preserve readable unknown uppercase currency and historical correction provenance; new-money registry eligibility is not imposed retroactively. Signed extra costs use exact complete accumulation, including cancellation beyond intermediate int64 range. Published money and every chronological usage prefix must remain safe; unsafe results fail rather than wrap/round or return a prefix.
- Group equal **nanosecond** instants after exact UTC interpretation, including equivalent offset spellings. Validate every chronological usage step and final sum, not arbitrary event-ID order or only the lifetime result. Do not truncate to whole seconds/milliseconds or silently paginate history at 50/100 rows.
- Read actual disposal proceeds independently of residual. Preserve negative realized/projected net; return required null fields for absent residual/service and zero usage. Convert only already-safe totals to finite per-day/per-use ratios; these are allocations, not posted money.
- Interpret `1 + days_between` as an inclusive proleptic Gregorian **home-date-label range** after exact start/end/retirement comparisons. A skipped calendar label remains in that range; a repeated label does not add another day. Use no elapsed 24-hour division, nonexistent local-midnight normalization or saturated long `time.Duration`. Before service gives null. A historical backward dateline jump that reverses the civil date range fails closed instead of clamping a nonpositive divisor. The supported tests include years 0001–9999 and home-year boundary crossings.
- A supplied `as_of` retains nanosecond precision in canonical UTC; omission uses the server clock. Only the day denominator is bounded by it. Complete current posted money/usage/proceeds remain included, even when their declared dates are later. Resolve the actual `preferences.timezone` setting (UTC only when absent), validate same-transaction catalog `source_revision` including real zero, and never install a calculation as an Asset write proof.

Tests cover large complete histories, exact signed cancellation, zero/manual/purchase conflicts, cross-currency/usage incompatibility, negative/unsafe prefixes, actual correction cycles, missing own/related counters, corrupt scans, late SQLite iterator failures, configured timezone/revision failures and final authorization/deferred-COMMIT rollback. These are real private Go/SQLite service checks, not public HTTP or hardware acceptance. Financial writers/profile before-image audit, signed collections and purchase integration remain gated.

## Retained persistence foundation

Migration **0015** and the private store loaders/materializers implement storage authority, not the financial commands listed above:

- Rebuild the profile and all three referencing history tables as one group with foreign keys continuously enabled. New child tables reference the new temporary parent, old children are dropped before the old parent, and final renames retain the real Unit/profile/self/audit references. Preflight rejects invalid old references before any rebuild; it never launders them by dropping constraints or deleting history.
- Replace the reference profile's lexical retirement/service CHECK with exact Go instant validation inside the same migration/checksum transaction. Differently offset RFC3339 strings are not chronologically comparable as text. Preserve valid exact spellings; reject reversed, malformed, overprecision or UTC-unrepresentable source rather than normalizing it during migration.
- Preserve every original profile/event/disposal value, raw legacy key and private creation spelling. Add nullable `correction_of` for Usage/Cost and nullable deferred `mutation_event_id` for Usage/Cost/Disposal. Unknown historical provenance remains null; no original event, date or audit is invented. Explicit new producer associations require valid creation instants and real committed audit references. Audit indexes are nonunique; old raw-key uniqueness is retained, while new scoped producers leave raw keys null.
- Posted usage/cost/disposal rows are immutable, including value-equal UPDATE. Profile ownership is immutable and profile deletion is prohibited. Financial profile before-image/correction producers remain unfinished; these guards do not make profile changes a public audited command.
- Materialize only existing source into independent Asset/Usage/Cost/Disposal identities. Keep real prior counters, current bytes/timestamps, frozen outbox rows and a certified root's epoch; counter-only identities do not resurrect source. Only an actual newly backfilled source identity can begin at one. Explicit historical-reference adoption retains its separate epoch-rotation policy.
- Validate bounded UTF-8/native-control text, exact safe amounts/counts, dates, actual individual Unit/Item source and all real parent counters. Correction chains require actual same-owner/same-interpretation targets and reject cycles without recursion. Backfill memoizes verified tails; a 5,000-event algorithm test verifies linear traversal, not a NAS workload measurement. Retained representation is not proof of new-money, acquisition-basis or aggregate financial eligibility.
- Private publication rebuilds each payload once from validated final source, with independent counters and exact unchanged offline shapes. Source/counters/current/frozen rows/derived invalidation/audit/original reply use the existing atomic boundary; failed statements, unsafe source/counters, late authorization or deferred COMMIT return no partial resource. Counter-only tombstones never erase posted source. Approved-search/physical dependencies are explicitly staged by their producer, not inferred from a cache or automatically created by a loader.

The native CLI has been driven on disposable fresh and certified schema-14 databases: actual schema-15 upgrade/check, independently verified original snapshots, exact source/counter/current/frozen/epoch preservation, five offline record-schema validations and rollback of a lexically acceptable but chronologically invalid service interval. Private transaction tests are separate from public financial command/HTTP/browser acceptance. No old rows are republished as new receipts/events, no unreviewed cache enters publication, and this partial backfill does not enable complete sync or remove the purchase integration gate.

## Verification boundary

Executable negative contracts cover complete bounded resources, explicit proof/null intent, machine-discriminated bases/corrections/disposal, exact money/count/instant inputs, body/query/admission, full live pages and unchanged offline envelopes. Strict generated-type fixtures also exercise the real typed transport, including required null without actual type-marker objects.

Still required: authoritative financial writers with exact profile before-image audit and new-input basis/currency/usage/lifecycle/concurrency rules, signed collection reads, public HTTP/TLS/fuzz conformance, stateless runtime browser adapters, reviewed responsive screens, allocation/disposal adjustment/void integration, analytics and device/operator acceptance. Implemented private reads/calculations and persistence/source/materialization tests do not establish those remaining gates. No hardware, CI, live financial endpoint or completed-product claim follows from this document.
