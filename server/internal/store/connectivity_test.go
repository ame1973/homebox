package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConnectivityProbeNeverExtendsAnIdleSessionOrWritesDomainState(t *testing.T) {
	s, auth, now, session, token := completedAuthFixture(t)
	started := *now
	seen := scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID)
	audits := scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit")
	execSQL(t, s.db, `CREATE TRIGGER forbid_probe_activity BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'probe changed session'); END`)
	before := domainState(t, s)
	for _, elapsed := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, auth.idle - time.Nanosecond} {
		*now = started.Add(elapsed)
		if err := auth.ProbeConnectivity(t.Context(), token); err != nil {
			t.Fatalf("a completed session was rejected after %s: %v", elapsed, err)
		}
	}
	*now = started.Add(auth.idle)
	if err := auth.ProbeConnectivity(t.Context(), token); !errors.Is(err, ErrAuthentication) {
		t.Fatal("repeated probes extended the original idle deadline", err)
	}
	if scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID) != seen ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit") != audits {
		t.Fatal("probe changed activity or audit state")
	}
	requireDomainState(t, s, before)
}

func TestConnectivityProbeRequiresCurrentCompletedSession(t *testing.T) {
	for _, failure := range []string{"invalid", "revoked", "mandatory", "epoch", "idle", "absolute", "clock rollback", "corrupt time", "late expiry", "late cancellation"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			s, auth, now, session, token := completedAuthFixture(t)
			ctx := t.Context()
			want := ErrAuthentication
			switch failure {
			case "invalid":
				token = "not-a-session-token"
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=? WHERE id=?", stamp(*now), session.ID)
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='another-dataset'")
			case "idle":
				*now = now.Add(auth.idle)
			case "absolute":
				execSQL(t, s.db, "UPDATE sessions SET last_seen_at=? WHERE id=?", stamp(session.ExpiresAt.Add(-time.Second)), session.ID)
				*now = session.ExpiresAt
			case "clock rollback":
				*now = now.Add(-time.Nanosecond)
			case "corrupt time":
				execSQL(t, s.db, "UPDATE sessions SET last_seen_at='invalid' WHERE id=?", session.ID)
				want = ErrAuthState
			case "late expiry", "late cancellation":
				base, calls := *now, 0
				if failure == "late cancellation" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					want = context.Canceled
					auth.now = func() time.Time {
						calls++
						if calls > 1 {
							cancel()
						}
						return base
					}
				} else {
					auth.now = func() time.Time {
						calls++
						if calls > 1 {
							return base.Add(auth.idle)
						}
						return base
					}
				}
			}
			before := domainState(t, s)
			if err := auth.ProbeConnectivity(ctx, token); !errors.Is(err, want) {
				t.Fatal("invalid or expired session passed connectivity probe", failure, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestConnectivityProbeConcurrentLogoutNeverAuthorizesAfterRevocation(t *testing.T) {
	_, auth, _, _, token := completedAuthFixture(t)
	start := make(chan struct{})
	probe := make(chan error, 1)
	logout := make(chan error, 1)
	go func() {
		<-start
		probe <- auth.ProbeConnectivity(t.Context(), token)
	}()
	go func() {
		<-start
		_, err := auth.Logout(t.Context(), token, "connectivity-concurrent-logout")
		logout <- err
	}()
	close(start)
	if err := <-probe; err != nil && !errors.Is(err, ErrAuthentication) {
		t.Fatal("probe returned an unexpected concurrent result", err)
	}
	if err := <-logout; err != nil {
		t.Fatal("concurrent logout failed", err)
	}
	if err := auth.ProbeConnectivity(t.Context(), token); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a committed logout remained probeable", err)
	}
}

func TestConnectivityProbeHandlesCancellationAndMissingAuthority(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	var absent *Auth
	if err := absent.ProbeConnectivity(t.Context(), token); !errors.Is(err, ErrAuthInput) {
		t.Fatal("nil authentication was accepted", err)
	}
	if err := auth.ProbeConnectivity(nil, token); !errors.Is(err, ErrAuthInput) {
		t.Fatal("nil context was accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := auth.ProbeConnectivity(ctx, token); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled probe was accepted", err)
	}
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
	err = auth.ProbeConnectivity(ctx, token)
	cancel()
	if rollback := held.Rollback(); rollback != nil {
		t.Fatal(rollback)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("probe ignored cancellation while waiting for SQLite", err)
	}
	execSQL(t, s.db, "ALTER TABLE sessions RENAME TO unavailable_sessions")
	defer execSQL(t, s.db, "ALTER TABLE unavailable_sessions RENAME TO sessions")
	if err := auth.ProbeConnectivity(t.Context(), token); err == nil {
		t.Fatal("missing authoritative session table was accepted")
	}
}
