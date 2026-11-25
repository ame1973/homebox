# Public Shell Worker Groundwork

HomeBox's production build creates a versioned service-worker script for the public application shell. **The application does not register it yet.** Offline startup, an installation/update interface, the IndexedDB replica and drafts are unfinished. Do not assume that visiting the current application makes it available offline.

## Build and installation boundary

`npm --prefix web run build` enumerates the Vite entry, lazy chunks, emitted workers and a fixed list of public icons and manifest files. It rejects unsafe or unhashed asset paths, missing dependencies, more than 256 resources, an individual file over 4 MiB or more than 16 MiB of public shell bytes. The worker URL and cache name change when the shell, asset bytes, worker source or build generator changes. The built HTML identifies the root-scoped worker, but does not register it.

The same build writes `/pwa-version.json` with the exact worker URL. The Go server serves only that JSON filename as a public asset with `no-store`; the worker does not cache or answer it. This public descriptor is groundwork for checking the currently deployed shell, not an API compatibility handshake, an atomic deployment guarantee or authorization to replace an existing registration. A successful build must publish the HTML, descriptor, worker and hashed assets together; an incomplete deployment can produce mismatches. The generator refuses to overwrite an existing descriptor in its build directory. The internal browser reader sends one same-origin, cookie-free, non-cached request with an eight-second deadline, validates the status/MIME/cache policy, bounds streamed bytes and accepts only the build's exact versioned path. It does not itself register a worker, retry a request or prove that a version is safe to activate.

When a test explicitly registers the worker, installation downloads only these public resources without credentials. It checks each response's status, MIME type, exact byte length and SHA-256 before putting it in the versioned shell cache. The public `/` supplies the HTML because the Go server redirects `/index.html`; the verified HTML is retained under the offline cache key `/index.html`. Failed installation cannot activate the new worker and does not delete the previous shell. Installation may leave a partial new cache for a later attempt; no automatic cache deletion is performed.

## Request and privacy boundaries

- Authenticated API, account, Media, backup, export, worker, private-data and development namespaces are never cached or answered by a shell fallback. The worker ignores non-GET, cross-origin and Range requests. Signed-in responses and mutation requests are not queued or replayed.
- A cached asset is checked again against the build's exact byte length and SHA-256 before use. Returned cached HTML and assets receive the application's strict CSP, `nosniff`, frame, referrer and permission policies rather than trusting mutable cache headers. A missing or corrupted cached hash-named asset may be fetched from the network, but the worker verifies its status, MIME and bytes before returning it. `no-store`, reload and query-bearing hash-named requests do not use Cache Storage, but still require that network verification.
- Extensionless public document navigation and the unversioned public manifest/icons use network-first behavior. A non-redirected successful network body is limited to 4 MiB and its network request and body are bounded by an eight-second abort; an unreachable or invalid response can fall back to the verified old shell. HTTP errors are not silently converted into successful pages. A canceled navigation is not intentionally completed from a cached shell.
- The exact size limits bound the application's own retained buffers, not every browser, native network, cryptographic or Cache Storage allocation. Browser cache access and native operations may fail or stall independently; storage can be evicted. The shell is not an encrypted vault or a security boundary against arbitrary same-origin code execution.

The worker never calls `skipWaiting` or `clients.claim` and never deletes old shell caches. Tests explicitly register a second version and verify that it waits while two old controlled tabs retain unsaved Item forms. These checks do not supply the missing application-level explicit update approval, old-cache management or client/API compatibility handshake. Cache versions can accumulate until a future reviewed cleanup workflow exists.

## Development checks

```sh
make build-web
npm --prefix web run test:build
npm --prefix web run test:e2e -- tests/e2e/shell-worker.spec.ts --workers=1
```

The browser tests use disposable Go databases, isolated browser storage and Chromium emulation. They exercise real worker installation, offline navigation, actual private API reads, malformed and evicted cache content, unexpected 201/206 responses, delayed network, and two-tab update waiting. Unit tests separately simulate cancellation, oversized responses and a response body that stalls after headers. These are not physical iPhone/Android or QNAP acceptance, a full offline-replica test, or authorization to operate on existing Media files.
