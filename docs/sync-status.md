# Sync Status

The protected `GET /api/v1/sync/status` is a **read-only diagnostic**, not an offline sync service. A completed administrator session and the normal same-origin transport are required. The response is `Cache-Control: no-store` and has no ETag. It accepts no query, body or content encoding, and session authority is rechecked in the SQLite transaction after the request body is read.

## Browser diagnostic

After completing administrator setup and signing in, open **Settings → Sync diagnostics** (`/settings/sync-status`). The same read-only screen is available on phone, tablet and desktop. It shows the server's bootstrap and retention claims, change positions, sparse materialized projection counts, dataset context and any reported model identifiers. It does **not** report this device's synchronized-record, cached-image, embedding or ready-model coverage; those offline capabilities remain unfinished. Even a future server readiness claim does not itself establish that this browser has an offline replica.

Choose **Refresh status** to revalidate the session and read a new snapshot. An offline or failed read hides the prior snapshot; reconnecting reads status again but does not start synchronization or prune records. A delivered session-clear signal removes this screen's cached diagnostic and requires sign-in. Cross-window notification is best effort: it may not arrive if both browser transports are blocked, and it does not purge other routes or offline storage. Before a possible back-forward-cache suspension the screen is redacted; restoration requires a fresh session check and explicit continuation before another status read.

## API diagnostic

An administrator can also inspect the diagnostic from the configured origin. For example, in the browser's developer console:

```js
const response = await fetch('/api/v1/sync/status', {
  credentials: 'same-origin',
  cache: 'no-store',
})
if (!response.ok) throw new Error(`Status request failed: ${response.status}`)
console.log(await response.json())
```

Do not paste authentication cookies or other credentials into commands, logs or issue reports. The status is an instantaneous snapshot, not a continuing health guarantee. The stateless browser adapter `fetchSyncStatus(signal)` performs one bounded, cancelable read; it does not persist, retry, queue or initiate sync.

## Interpreting the response

- `bootstrap_ready` is currently `false`: complete cross-domain backfill, frozen bootstrap and ordered delta reads are not yet available. Do not construct a cursor or treat the diagnostic as a replica.
- `counts_scope` is `materialized_projections`. `counts` contains only positive row counts for the 22 defined projection kinds; an omitted kind has zero **materialized rows**. These counts do not validate every payload or establish how many authoritative source records exist. A missing projection is not proof that a source record is absent.
- `server_instance_id`, `dataset_epoch` and `projection_version` identify this snapshot's dataset context. They do not authorize a read or guarantee later state. A restore must still rotate the epoch.
- `high_watermark` is SQLite's last allocated change sequence. It remains monotonic after retained changes are pruned and can exceed the highest retained sequence because sequence positions can have gaps. It is not the number of changes or a signed cursor.
- `min_retained_seq` is the last pruned position. In the future delta protocol, a cursor exactly at that floor remains valid; a cursor below it needs a bootstrap. The status does not certify that retained history is complete.
- `last_change_at` is the timestamp of the highest **retained** change, or `null` when none remains. After complete pruning, `null` does not mean the dataset has never changed.
- `retention_days` reports the minimum 30-day policy. `retention_enabled` is currently `false`: no automatic pruning job runs, so the diagnostic does not promise any particular age of the oldest retained row. Plan disk capacity accordingly; do not manually delete log rows as a substitute for the unfinished retention service.

Invalid sequence/floor bounds, unsupported projection kinds or an invalid highest-retained timestamp fail closed instead of returning partial diagnostics. This endpoint does not inspect every source, validate every change or frozen payload, repair data, create a snapshot, write a draft or delete any log or Media file. See the [offline protocol](../contracts/sync.md) for the eventual bootstrap, retention, epoch and draft requirements.
