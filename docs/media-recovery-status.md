# Media Recovery Status

Open **Photo recovery status** from **Settings** after signing in. The screen shows one live response from the protected `/api/v1/system/media-recovery` read. It does not start recovery, retry a blocked operation, delete files, upload a photo or change inventory. **Read status again** requests another snapshot; it is not a recovery command.

The interface distinguishes:

- **Unavailable:** no safely accessible managed recovery owner was supplied. Check the host and storage configuration; ordinary application operations may still be available.
- **Queued** and **running:** the recovery coordinator is waiting or working. The displayed state may change immediately after the read.
- **Idle:** the coordinator is not currently running. This is not proof that every photo exists, is safe, is verified or can be uploaded or streamed.
- **Blocked:** the coordinator stopped on a bounded failure category. Earlier work may already have completed. Unknown or unproven files must not be deleted blindly; an authenticated retry interface and a verified unknown-file recovery procedure remain unfinished.
- **Stopped:** the coordinator has stopped. Check the server state before reading again.

The acknowledged completion count covers only the current coordinator lifetime. It is neither a photo count nor the number of remaining files. A saturated count is shown as a lower bound; restarting the coordinator resets its count. The timestamp is the browser's local read time, not a server revision or a file-integrity check.

The screen requires a freshly checked, completed administrator session. It hides an earlier snapshot while offline or after a failed read; reconnecting can issue a new read but never a mutation. A delivered session-clear signal removes this screen's cached snapshot, and page suspension redacts it before a possible back-forward-cache restore. A restored page checks the session again and requires explicit continuation before reading a new snapshot. Cross-window session signals remain best-effort when browser transports are unavailable, and this page does not claim to purge unrelated windows or storage.

No file name, private path, raw server error, retry key or credential is displayed. An unavailable or blocked result is not permission to alter files. The feature reports the owner's actual state when one is supplied; it does not itself start a server-side recovery service or establish filesystem, power-loss, QNAP or physical-device acceptance.
