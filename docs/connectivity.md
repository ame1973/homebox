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

`navigator.onLine` is advisory: a browser can report online while the home server is unreachable. The current application still uses that signal for its existing UI and handles individual request failures separately; automatic foreground connectivity scheduling, retry/backoff and device offline coverage remain unfinished. This endpoint alone does not enable a replica, drafts, bootstrap, delta sync or automatic mutation replay.
