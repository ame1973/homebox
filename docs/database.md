# Database Operations

HomeBox currently provides a tested database-management CLI. The HTTP application, PWA, full encrypted media-backup workflow, and physical QNAP acceptance are not yet complete. Do not mistake a successful database migration for a production deployment.

## Build and initialize

Run from the repository root on Linux with Go 1.26.2:

```sh
make build
./bin/homebox version
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox db check --database ./data/homebox.sqlite
```

`migrate` is an explicit operator action. Normal store startup and `check` refuse a missing, outdated, unrecognized, or newer database; they never initialize or upgrade it silently. Commands emit JSON on stdout and return a nonzero exit status on failure. Use `--help` on a command for its flags.

The default command timeout is two minutes. Set a larger positive timeout when measured data size requires it:

```sh
./bin/homebox db migrate --database ./data/homebox.sqlite --timeout 10m
```

## Storage and ownership

- The data directory must be private: mode `0700`; database, WAL, shared-memory, lock, and snapshot files must not grant group/other access. New files use `0600`.
- Existing permissive directories/files are rejected rather than silently changing ownership or permissions. Verify the intended path and correct its permissions as the operator before retrying.
- Use a local POSIX filesystem. The Linux preflight rejects known SMB/CIFS/NFS filesystem types. This does not replace verification of the actual NAS storage configuration.
- A process-lifetime lock allows one HomeBox owner per database. Stop the owning process before using these offline maintenance commands. Do not bypass this lock with another SQLite writer.
- The database uses WAL, `synchronous=FULL`, a 5-second busy timeout, and foreign keys on every connection. Write transactions begin immediately, before reads that validate a mutation.
- Mandatory domain current/outbox publication, reply/audit insertion and active-session activity updates must affect exactly one actual row. A nil driver error or stale insert identity cannot prove publication after a skipped write. Failure withholds the whole result and rolls back source/counters/publication/receipts, including no-op acknowledgements, reads and original-key replay. Cache deletion for a retained tombstone may legitimately affect zero rows; it is not a missing required insert.
- Database paths are treated as literal filesystem paths, not user-provided SQLite DSNs. Database and lock-file symlinks are rejected.

Do not store the database in the public web directory, a network share, or a container's ephemeral writable layer. Keep private data out of Git.

## Forward migrations and rollback behavior

Migration files are immutable once applied. The runner records their original SHA-256 values and filenames in a transactional checksum ledger. A modified checksum, missing history entry, unexpected gap, or newer schema blocks startup. Compatible changes must be new sequential migration files, never edits to an applied file.

Migration 0003 adds private authentication metadata. It revokes reference sessions without idle-expiry history, marks existing accounts for password change, and enforces a single administrator. More than one legacy administrator causes the whole upgrade to roll back; resolve the intended account ownership explicitly rather than deleting accounts automatically. The [HTTP authentication workflow](http-authentication.md) is implemented. Migration 0004 binds new session/bootstrap credentials to the dataset epoch and invalidates older unbound credentials; rotating the epoch then invalidates existing credentials as well.

Migration 0007 adds location descriptions/logical-deletion metadata, independent current projections with commit-time revision integrity, and immutable mutation audit. Its backfill covers existing locations and movements, preserving source IDs, revisions, and quantity counts. See [Location Rules](locations.md) for the implemented service/HTTP/browser workflows and remaining contents/QR/sync limits.

Migration 0008 adds indexed category-parent/tag-membership lookups and current category/tag/item-tag projections, preserving existing counters and canonical composite identities. It seeds item/search counters but does not copy unverified legacy search-cache text into published projections. [Categories and Tags](taxonomy.md) describes the implemented transaction/publication, HTTP, typed-adapter and reviewed settings workflows alongside real catalog classification/Tag assignment; complete sync remains separate.

Migration 0009 adds owner/stable-ID lookup indexes and independent barcode/alias/attribute current projections. Missing counters begin at one; retained counters, existing current payload bytes/timestamps and frozen changes remain unchanged. Historical raw/normalized barcode text is not silently repaired or revalidated as a new write. UTF-8, field/identity bounds and safe counters are validated inside the same migration transaction before checksum publication; an invalid row rolls back every pending migration/index/backfill/ledger write. See [Catalog Links and Tag Membership](catalog-links.md) for the distinction between retained reads and new-write validation. A normal root upgrade preserves the dataset epoch; explicit reference adoption still rotates it as documented below.

Migration 0010 adds the Unit owner/stable-ID index and independent Unit/canonical-pair Stock current projections, preserving retained counters, current bytes/timestamps, frozen history and zero balances. Unit nullable serial/location text remains exact, and Stock measurement comes from its real Item without rescaling quantities. Go validates source, tracking, actual Item/holding authority, safe quantities/measurement and canonical counter identities after SQL inside the migration transaction. Missing source is not recreated from a retained counter; malformed or unsafe source rolls back all pending DDL/index/backfill/checksum writes. See [Individual Units and Quantity Stock](holdings.md) for implemented Unit/Stock services, all twelve HTTP operations, stateless typed adapters and responsive reviewed physical screens versus pending asset-lifecycle workflows.

Migration 0011 adds exact server-side movement note/reason and a deferred association to immutable mutation audit, plus audit notes for no-op receipts. It protects all inventory history from update/deletion while preserving old values, raw keys, counters, current/frozen bytes and dataset epoch. It neither guesses legacy timestamps nor publishes old/unverified events. The real private quantity service uses fresh movement identities, original Item proofs and principal/epoch receipts; both transfer legs, Item/location/Stock/event projections, immutable context and response commit atomically. New movement rows leave the old globally unique raw caller-key column null. See [Individual Units and Quantity Stock](holdings.md) for implemented Go/HTTP/SDK/reviewed physical operations versus pending asset lifecycle.

Migration 0012 rebuilds inventory-event storage to preserve the original nullable Unit ID as immutable historical identity rather than a live Unit FK. This permits guarded deletion of an unbacked Unit without deleting/nulling its own creation/movement history. Every source field, exact note/reason/raw key/audit association, item/location/audit FK, raw-key uniqueness, index, immutability trigger and current/frozen byte is retained; purchase/asset/ownership FKs remain restrictive. Preflight rejects invalid old live references before the change, and any migration/ledger failure rolls the whole rebuild back. It does not infer financial disposal, relax Unit service guards, republish old history or rotate a certified-root epoch. [Individual Units and Quantity Stock](holdings.md) describes the working Go/HTTP/SDK/foreground physical workflows and remaining asset-lifecycle/history gates.

Migration 0013 adds independent retained Purchase/Line materialization, stable owner lookup indexes and immutable allocation/header retention. It preserves exact source amounts/counts/text, existing counters/current/frozen bytes and certified-root epoch; invalid UTF-8/bounds/financial instant/actual owner authority/unsafe counters roll back the complete pending migration/checksum transaction. New private nullable Line creation/audit columns stay null for legacy rows, not guessed from header dates; projection timestamps describe materialization only. The deferred audit association is nonunique so one atomic command can own several independent allocations. Private loaders and final-source publication are implemented.

Migration 0014 adds immutable, indexed header-correction context tied to the real mutation event through a deferred FK and to the retained Purchase through a restrictive owner FK. Each actual correction preserves its exact original/resulting resource and supplied partial intent, including no-ops, without changing the public resource or offline payload. Legacy headers receive no invented correction/timestamp/audit association. Existing source, counters, current/frozen bytes and epoch remain unchanged; a late checksum failure rolls back the whole table/index/trigger upgrade and retains the independently verified private snapshot. The Go `Purchases` service creates headers, corrects descriptive source and appends unbacked allocations with original Purchase and Item proofs; supported new-money currency policy is distinct from permissive bounded retained representation. Complete actual-source global/Item-associated/Line collections are also implemented, with real owner/counter/lookahead checks and independently scoped signed live continuations. Reads never republish source, restore missing counters, infer totals or enable full sync. Purchase HTTP handlers now expose those seven services under the existing protected boundary with strict inputs, original dual proofs, independent ETags and verified TLS/socket contract checks. All seven stateless browser adapters are also implemented with original whole Purchase/Item context, exact nullable/money/date inputs, independent ETags and private actual-SDK/Go browser checks; they never turn frozen replies into current authority. Complete bracketed source/form/foreground-command models consume those APIs without inventing financial totals, physical receipt or current state from frozen replies. Responsive reviewed purchase screens now consume that state without hidden database writes, implicit physical receipt or frozen-reply installation. Asset-backed financial producers, audited financial adjustments and receipt/media remain unfinished. See [Purchases and Financial Allocations](purchases.md).

Migration 0015 rebuilds Asset profiles/usage/cost/disposal as one FK-enabled parent/child group, retaining every old field/raw key/creation spelling, actual Unit/profile references and certified-root epoch. It removes only the incorrect lexical service/retirement comparison; exact Go instant/UTF/bounds/owner/counter validation runs after SQL before the checksum transaction commits. Nullable correction/audit provenance stays unknown for old rows, with real deferred self/audit FKs and nonunique audit indexes for future producers. Posted usage/cost/disposal and profile identity/retention are guarded. Source-only independent current materialization preserves existing counters/current/frozen bytes and counter-only identities without new receipts/history/FTS. Statement/Go validation/late checksum failures roll the entire group/indices/triggers/ledger back, retaining the verified original snapshot. Private loaders/final-source publication are implemented; the separate private Go `Assets` service now provides authorized Asset/disposal detail and complete exact current-posted cost summaries from actual source in one transaction. These reads do not publish/repair history, create profiles, relax new-money policy or enable financial/lifecycle writers, public HTTP or purchase integration. See [Assets, Usage and Cost Accounting](assets.md).

Migration 0016 adds indexed immutable Asset profile change context with the exact nullable before-resource, resulting resource and complete original request/proofs, including opt-in and value-equal decisions. The real profile owner and actual mutation event have restrictive/deferred references, and JSON context remains private, not an offline payload. Existing source/current/frozen bytes/counters/epoch receive no invented old context. Any DDL/ledger failure rolls back the entire audit addition and retains the independently verified original snapshot. `Assets.PutProfile` now binds original Unit, Item and explicit nullable Asset proofs, validates complete financial source, applies finite policy only to new monetary interpretation and commits profile/Item publication, exact context, audit and original reply together. It never changes Unit state or enables the separate purchase-backed acquisition producer. See [Assets, Usage and Cost Accounting](assets.md).

These partial backfills do not copy legacy search/OCR caches, fabricate mutation/outbox events, alter stock quantities or enable the full 22-entity frozen sync protocol. The linked catalog guide describes implemented Go/HTTP row writes, complete tag-set replacement, owned signed pages and reviewed browser maintenance; the holding guide keeps incomplete physical workflows separate.

Before changing an existing database, the runner:

1. Validates known history, checksums, SQLite integrity, and foreign keys.
2. For legacy adoption, compares actual schema definitions with the exact supported reference, checks tree cycles/tracking modes, and requires any necessary unit mapping.
3. Checks free space on both the database and snapshot filesystems. Each check requires at least twice the logical database size plus a configurable reserve (default 16 MiB).
4. Takes a consistent snapshot through SQLite's Online Backup API, including committed WAL contents. It does **not** copy the active main database file.
5. Converts the completed snapshot to a self-contained rollback-journal database, verifies integrity/FKs, computes SHA-256, and publishes a checksum marker.
6. Applies all pending migrations and checksum records in one transaction. A failure rolls back every pending migration, including preceding migrations from the same attempt.

A failed upgrade retains its verified recovery snapshot. A fresh installation has no previous database to back up. Re-running migration on an already current database is a no-op after history/integrity validation.

This transaction rollback is not a promise of arbitrary application downgrade. Once a new application has written data under a new schema, an older binary must refuse it unless compatibility is explicitly supported. A schema-incompatible recovery requires a matching verified database **and media** set, a stopped application, and the full restore procedure; never overwrite a live SQLite file or discard WAL files to force a downgrade.

## Legacy reference adoption

The supported historical inputs are the exact reference schema version 1 (v0.7) and version 2 (v0.9), with no checksum ledger. Adoption is never automatic:

```sh
./bin/homebox db migrate \
  --database ./data/homebox.sqlite \
  --allow-legacy \
  --backup-dir ./data/migration-backups
```

The actual DDL must match the known reference. Custom tables, missing indexes, cyclic location/category trees, invalid foreign keys, and unit/quantity tracking mismatches are rejected. The original data is not "repaired" by dropping or recreating it.

A legacy item with `unit_code` other than `pcs` requires explicit operator-certified precision metadata. Example `legacy-units.json`:

```json
{
  "the-existing-item-id": {
    "base_unit": "g",
    "scale_factor": 1000
  }
}
```

```sh
./bin/homebox db migrate \
  --database ./data/homebox.sqlite \
  --allow-legacy \
  --legacy-units ./legacy-units.json
```

This certifies that existing integer counts are already expressed in grams and display as kilograms. A stored `quantity_base` of 1500 remains 1500 and displays as 1.5 kg. The runner never multiplies existing stock automatically. If old data uses a different meaning, stop and plan an audited conversion instead of inventing a mapping. Every non-`pcs` item needs a mapping; unknown/extra IDs, nonpositive scale factors, duplicate item IDs, unknown JSON fields, and oversized files are rejected.

Adoption rotates the dataset epoch while preserving the server-instance identity. Projection backfill and HTTP sync are separate implementation gates; adoption alone does not make a legacy offline replica ready.

## Private migration snapshots

A successful existing-database migration returns `backup_path` and `backup_sha256`. By default the files are placed in `migration-backups/` beside the database:

```text
migration-<random>.sqlite
migration-<random>.sqlite.sha256
```

Verify a snapshot without modifying it:

```sh
./bin/homebox db verify-snapshot --snapshot ./data/migration-backups/migration-EXAMPLE.sqlite
```

Use the actual returned filename, not the example name. Verification checks the stored SHA-256, SQLite integrity, foreign keys, and self-contained journal mode. Missing/tampered markers or changed bytes fail verification.

**These are sensitive local recovery snapshots, not full disaster-recovery backups.** They can contain credential hashes, contain no media archive, and are not encrypted. Keep them inside the private data volume; do not expose them through HTTP or copy them to an unprotected destination. Full user-exportable/off-host backups must use the encrypted backup-set workflow when it is implemented. Never delete the only verified recovery copy automatically.

## Architecture builds and tests

```sh
make check
make test-go-pure
make build-linux
```

`build-linux` produces CGO-free Linux amd64 and arm64 executables under `bin/`. Cross-compilation proves that the source builds for those architectures; it does not prove that a particular QNAP model, Container Station version, CPU, or filesystem has passed runtime acceptance.
