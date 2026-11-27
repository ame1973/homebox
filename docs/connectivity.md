# Authenticated Connectivity

`GET /api/v1/system/connectivity` is a minimal, same-origin reachability probe for a **completed administrator session**. A successful response is currently exactly `{"status":"ok"}`. It means that this request reached the server and its session passed validation; it is not a guarantee that a subsequent request will succeed, that storage or source data are healthy, or that offline synchronization is ready. The contract also reserves the bounded `degraded` state, which the current server does not emit.

The endpoint accepts an exact path with no query, request body or content encoding. It uses the normal configured Host/transport, browser fetch-origin and peer-rate policies, checks the actual session before and after body reading, and returns `Cache-Control: no-store` with no ETag or session cookie. A missing, revoked, idle-expired, absolute-expired or old-epoch session is rejected; a mandatory-password-change session cannot use the probe. Unsupported unsafe methods retain the normal Origin, CSRF, client-version and idempotency admission checks.

Unlike an interactive authenticated request, this passive probe does **not** update `last_seen_at` and cannot keep an otherwise idle session alive. It does not create a receipt, grant or authorization proof for another action. The client must handle an expired session explicitly and revalidate every actual read or mutation through its own server boundary.

After signing in at the exact configured origin, an administrator can try one probe from the browser console:

```js
const response = await fetch('/api/v1/system/connectivity', {
  credentials: 'same-origin',
  cache: 'no-store',
  redirect: 'error',
})
if (!response.ok) throw new Error(`Connectivity request failed: ${response.status}`)
console.log(await response.json())
```

Never paste cookies or other credentials into commands, logs or issue reports. Rate-limited responses include bounded `Retry-After` guidance. The stateless `probeConnectivity(signal)` browser adapter performs one cancelable request with an eight-second deadline, validates the bounded response and does not persist, queue or retry. It preserves authentication and rate-limit errors without displaying arbitrary server text.

`navigator.onLine` is advisory: a browser can report online while the home server is unreachable, or report offline while a local server remains reachable. The internal `monitorForegroundConnectivity` helper may be started only after its caller has checked a current completed session. It probes immediately while visible, normally rechecks every 60 seconds, and backs off failures from 15 seconds to at most five minutes. It honors a bounded server `Retry-After`, including for an explicit recheck, and does not use `navigator.onLine` to suppress the probe. It stops on a delivered session-clear signal or pagehide, cancels pending reads while hidden, and discards late results. A 401 or mandatory-password-change response stops the monitor; the caller must establish a new session before creating another. If an aborted transport never settles, the monitor waits rather than overlapping another attempt. Multiple tabs are not coordinated and share the server's peer rate limit.

After an existing application session check or successful login confirms a completed administrator session, the application shell displays the latest bounded foreground observation. Routine repeat probes leave the last result visible until a new result arrives, avoiding repetitive screen-reader announcements. It hides the old observation while the same session is being revalidated, stops and hides it when session clearing is delivered, and cancels the pending account read. Page suspension replaces the last result with a stopped state before the browser can freeze the page. After restoration, a new completed-session check is required before monitoring starts again; when no page is checking the account, use the **recheck account** link. A probe that requires authentication does not itself log out, clear unrelated pages or redirect: follow the account link to revalidate. A cross-window clear is best effort; other windows' caches are not generally purged.

Within the application document, session-clear subscribers share the BroadcastChannel used to send the static clear marker. The browser does not deliver a channel's own broadcast back to it, so the same local clear is not delivered again as a delayed remote clear after a new login. Other same-origin documents can still receive that marker, and transient storage signaling remains a fallback. Delivery is not guaranteed, and another document's delayed signal can still cause conservative local cleanup.

The indicator is informational, not authorization for a subsequent write or evidence of local data coverage. Existing page-specific online controls still use the browser signal and handle their own request errors; this indicator does not change their eligibility or automatically retry mutations. Offline replicas, drafts, bootstrap and delta sync remain unfinished.
