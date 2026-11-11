package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Isolated parallel cases must not compete for the process-wide production KDF
// gate during setup. Each case still provisions and logs in with real hashing.
var completedFixtureAuthSetup sync.Mutex

// Model an existing fully provisioned account without rerunning unrelated setup/password tests.
func completedAuthFixture(t *testing.T) (*Store, *Auth, *time.Time, AuthSession, string) {
	t.Helper()
	s, a, now := authFixture(t)
	completedFixtureAuthSetup.Lock()
	defer completedFixtureAuthSetup.Unlock()
	provision(t, a)
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0,revision=2")
	session, token := login(t, a)
	return s, a, now, session, token
}

func consumeGrant(ctx context.Context, a *Auth, cookie, grant string) error {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = a.consumeReauth(ctx, tx, cookie, grant, a.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func issueGrant(t *testing.T, a *Auth, cookie, key string) (ReauthGrant, string) {
	t.Helper()
	grant, token, err := a.Reauthenticate(t.Context(), cookie, key, "家中管理員", testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	return grant, token
}

func TestReauthRequiresCompletedPasswordChangeAndCorrectCredentials(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	if _, raw, err := a.Reauthenticate(t.Context(), token, "reauth-key", "家中管理員", testAdminPassword); raw != "" || !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("mandatory password change was bypassed")
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0,revision=2")
	for _, input := range [][2]string{{"wrong", testAdminPassword}, {"家中管理員", "wrong password"}} {
		if _, raw, err := a.Reauthenticate(t.Context(), token, "reauth-key", input[0], input[1]); raw != "" || !errors.Is(err, ErrPasswordMismatch) {
			t.Fatal("invalid password proof issued a grant")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='reauth_failed'") != 2 {
		t.Fatal("failed reauthentication effects are incorrect")
	}
}

func TestReauthPersistsOnlyHashesAndReconstructsItsFrozenToken(t *testing.T) {
	s, a, now, session, cookie := completedAuthFixture(t)
	grant, token := issueGrant(t, a, cookie, "reauth-key")
	if !validAuthToken(token) || token == cookie || !grant.ExpiresAt.Equal(now.Add(5*time.Minute)) || grant.Admin != session.Admin {
		t.Fatal("invalid reauthentication metadata")
	}
	if scalar[string](t, s.db, "SELECT token_hash FROM auth_reauth_grants") != authDigest(token) {
		t.Fatal("grant was not stored as a hash")
	}
	body := scalar[string](t, s.db, "SELECT response_json FROM api_idempotency")
	if strings.Contains(body, token) || strings.Contains(body, cookie) || strings.Contains(body, testAdminPassword) || strings.Contains(body, "reauth_token") {
		t.Fatal("frozen metadata persisted a raw credential")
	}
	replay, reconstructed, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword)
	if err != nil || reconstructed != token || replay.Admin != grant.Admin || !replay.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatal("idempotent retry did not reconstruct the original response")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='reauth_succeeded'") != 1 {
		t.Fatal("reauthentication replay minted another grant")
	}
	if _, _, err = a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", "different password"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed reauthentication input reused a key")
	}
	if _, err = a.Logout(t.Context(), cookie, "reauth-key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("another operation reused a reauthentication key")
	}
	_, otherCookie := login(t, a)
	_, otherToken := issueGrant(t, a, otherCookie, "reauth-key")
	if token == otherToken {
		t.Fatal("grant was not session-bound")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 {
		t.Fatal("reauthentication entered offline projections")
	}
}

func TestReauthGrantIsSingleUseAndCannotBeForgedOrMovedBetweenSessions(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	_, token := issueGrant(t, a, cookie, "reauth-key")
	_, otherCookie := login(t, a)
	if err := consumeGrant(t.Context(), a, otherCookie, token); !errors.Is(err, ErrReauthRequired) {
		t.Fatal("another session consumed a grant")
	}
	for _, forged := range []string{"bad", a.reauthToken(cookie, "never-issued-key")} {
		if err := consumeGrant(t.Context(), a, cookie, forged); !errors.Is(err, ErrReauthRequired) {
			t.Fatal("a fabricated capability was accepted without a persisted grant")
		}
	}
	if err := consumeGrant(t.Context(), a, cookie, token); err != nil {
		t.Fatal(err)
	}
	if err := consumeGrant(t.Context(), a, cookie, token); !errors.Is(err, ErrReauthRequired) {
		t.Fatal("grant was consumed twice")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE consumed_at IS NOT NULL") != 1 {
		t.Fatal("grant use was not recorded")
	}
}

func TestConcurrentGrantConsumptionHasExactlyOneWinner(t *testing.T) {
	_, a, _, _, cookie := completedAuthFixture(t)
	_, token := issueGrant(t, a, cookie, "reauth-key")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { results <- consumeGrant(t.Context(), a, cookie, token) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrReauthRequired) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent consumers did not have exactly one winner")
	}
}

func TestGrantConsumptionRollsBackWithThePrivilegedMutation(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	_, token := issueGrant(t, a, cookie, "reauth-key")
	execSQL(t, s.db, "CREATE TABLE grant_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = a.consumeReauth(t.Context(), tx, cookie, token, a.now()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO grant_commit_guard VALUES('missing')"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err == nil {
		t.Fatal("expected a privileged mutation commit failure")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants WHERE consumed_at IS NOT NULL") != 0 {
		t.Fatal("failed privileged mutation consumed its authorization")
	}
	if err = consumeGrant(t.Context(), a, cookie, token); err != nil {
		t.Fatal("rolled-back grant could not be used", err)
	}
}

func TestReauthLifetimeIsBoundedAndReplayNeverRenewsIt(t *testing.T) {
	for _, bound := range []string{"idle", "absolute", "grant"} {
		t.Run(bound, func(t *testing.T) {
			s, a, now, session, cookie := completedAuthFixture(t)
			expected := now.Add(5 * time.Minute)
			switch bound {
			case "idle":
				a.idle = time.Minute
				expected = now.Add(time.Minute)
			case "absolute":
				expected = now.Add(30 * time.Second)
				execSQL(t, s.db, "UPDATE sessions SET expires_at=? WHERE id=?", stamp(expected), session.ID)
			}
			grant, token := issueGrant(t, a, cookie, "reauth-key")
			if !grant.ExpiresAt.Equal(expected) {
				t.Fatal("grant exceeded an authorization lifetime")
			}
			*now = expected
			err := consumeGrant(t.Context(), a, cookie, token)
			if !errors.Is(err, ErrReauthRequired) && !errors.Is(err, ErrAuthentication) {
				t.Fatal("grant was valid at its expiry boundary")
			}
			if bound == "grant" {
				replay, reconstructed, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword)
				if err != nil || reconstructed != token || !replay.ExpiresAt.Equal(grant.ExpiresAt) {
					t.Fatal("replay renewed an expired grant")
				}
				_, fresh := issueGrant(t, a, cookie, "new-reauth-key")
				if fresh == token {
					t.Fatal("new explicit reauthentication reused an old capability")
				}
			}
		})
	}
}

func TestReauthRejectsStaleProofsAndRevokedOrRestoredSessions(t *testing.T) {
	for _, mutation := range []string{"revision", "hash", "revocation", "epoch", "idle", "mandatory change"} {
		t.Run(mutation, func(t *testing.T) {
			s, a, now, _, cookie := completedAuthFixture(t)
			fingerprint, err := a.authFingerprint(cookie, "reauth-key", reauthOperation, reauthRequest{"家中管理員", testAdminPassword})
			if err != nil {
				t.Fatal(err)
			}
			proof, _, err := a.prepareReauth(t.Context(), cookie, "reauth-key", fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "revision":
				execSQL(t, s.db, "UPDATE admin_accounts SET revision=revision+1")
			case "hash":
				execSQL(t, s.db, "UPDATE admin_accounts SET password_hash='concurrent-change'")
			case "revocation":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(*now))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", strings.Repeat("d", 32))
			case "idle":
				*now = now.Add(a.idle)
			case "mandatory change":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
			}
			_, raw, err := a.commitReauth(t.Context(), cookie, "reauth-key", fingerprint, proof)
			if raw != "" || (!errors.Is(err, ErrAuthentication) && !errors.Is(err, ErrPasswordChangeRequired)) {
				t.Fatal("stale credential proof issued a grant")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 {
				t.Fatal("stale proof left reauthentication state")
			}
		})
	}
}

func TestReauthGrantCannotSurviveRevisionChangeLogoutOrEpochRotation(t *testing.T) {
	for _, mutation := range []string{"revision", "logout", "epoch", "mandatory change"} {
		t.Run(mutation, func(t *testing.T) {
			s, a, _, _, cookie := completedAuthFixture(t)
			_, token := issueGrant(t, a, cookie, "reauth-key")
			switch mutation {
			case "revision":
				execSQL(t, s.db, "UPDATE admin_accounts SET revision=revision+1")
			case "logout":
				if _, err := a.Logout(t.Context(), cookie, "logout-key"); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", strings.Repeat("d", 32))
			case "mandatory change":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
			}
			if err := consumeGrant(t.Context(), a, cookie, token); err == nil {
				t.Fatal("changed authorization context retained a grant")
			}
		})
	}
}

func TestReauthWritesAndCommitRollBackWithoutPublishingCapabilities(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"auth_reauth_grants", "INSERT"}, {"sessions", "UPDATE"}, {"security_audit", "INSERT"}, {"api_idempotency", "INSERT"},
	} {
		t.Run(failure.table, func(t *testing.T) {
			s, a, now, session, cookie := completedAuthFixture(t)
			before := scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID)
			*now = now.Add(time.Minute)
			execSQL(t, s.db, "CREATE TRIGGER fail_reauth_write BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			if _, raw, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword); err == nil || raw != "" {
				t.Fatal("failed grant write published a capability")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='reauth_succeeded'") != 0 || scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID) != before {
				t.Fatal("reauthentication partially survived a failed write")
			}
		})
	}
	t.Run("commit", func(t *testing.T) {
		s, a, _, _, cookie := completedAuthFixture(t)
		execSQL(t, s.db, "CREATE TABLE reauth_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
		execSQL(t, s.db, "CREATE TRIGGER fail_reauth_commit AFTER INSERT ON security_audit WHEN new.event_type='reauth_succeeded' BEGIN INSERT INTO reauth_commit_guard VALUES('missing'); END")
		if _, raw, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword); err == nil || raw != "" {
			t.Fatal("failed commit published a capability")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 {
			t.Fatal("commit failure left a grant")
		}
		if _, err := a.Authenticate(t.Context(), cookie); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDuplicateVerifiedReauthAndConsumedReplayNeverMintAnotherGrant(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	fingerprint, err := a.authFingerprint(cookie, "reauth-key", reauthOperation, reauthRequest{"家中管理員", testAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	firstProof, _, err := a.prepareReauth(t.Context(), cookie, "reauth-key", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	secondProof, _, err := a.prepareReauth(t.Context(), cookie, "reauth-key", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	first, firstToken, err := a.commitReauth(t.Context(), cookie, "reauth-key", fingerprint, firstProof)
	if err != nil {
		t.Fatal(err)
	}
	second, secondToken, err := a.commitReauth(t.Context(), cookie, "reauth-key", fingerprint, secondProof)
	if err != nil || firstToken != secondToken || !first.ExpiresAt.Equal(second.ExpiresAt) {
		t.Fatal("duplicate verified proof did not return the first grant")
	}
	if err = consumeGrant(t.Context(), a, cookie, firstToken); err != nil {
		t.Fatal(err)
	}
	_, replayed, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword)
	if err != nil || replayed != firstToken {
		t.Fatal("consumed grant replay changed its capability")
	}
	if err = consumeGrant(t.Context(), a, cookie, replayed); !errors.Is(err, ErrReauthRequired) {
		t.Fatal("replay reset consumption")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 1 {
		t.Fatal("replay created another grant")
	}
}

func TestRealPasswordRotationInvalidatesOutstandingReauthAndItsReplay(t *testing.T) {
	_, a, _, session, cookie := completedAuthFixture(t)
	_, capability := issueGrant(t, a, cookie, "reauth-key")
	if _, err := a.ChangePassword(t.Context(), cookie, "password-key", testAdminPassword, changedAdminPassword); err != nil {
		t.Fatal(err)
	}
	if err := consumeGrant(t.Context(), a, cookie, capability); !errors.Is(err, ErrAuthentication) {
		t.Fatal("password rotation left a usable capability")
	}
	if _, raw, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", session.Admin.Username, testAdminPassword); raw != "" || !errors.Is(err, ErrAuthentication) {
		t.Fatal("revoked session reconstructed a grant")
	}
	_, freshCookie, err := a.Login(t.Context(), session.Admin.Username, changedAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, raw, err := a.Reauthenticate(t.Context(), freshCookie, "fresh-reauth-key", session.Admin.Username, changedAdminPassword); err != nil || raw == "" {
		t.Fatal("new credentials could not reauthenticate", err)
	}
}

func TestReauthCorruptionAndMalformedInputsFailClosed(t *testing.T) {
	for _, field := range []string{"created_at", "expires_at"} {
		t.Run(field, func(t *testing.T) {
			s, a, _, _, cookie := completedAuthFixture(t)
			_, token := issueGrant(t, a, cookie, "reauth-key")
			execSQL(t, s.db, "UPDATE auth_reauth_grants SET "+field+"='invalid'")
			if err := consumeGrant(t.Context(), a, cookie, token); !errors.Is(err, ErrAuthState) {
				t.Fatal("corrupt grant time was accepted")
			}
		})
	}
	s, a, _, _, cookie := completedAuthFixture(t)
	for _, input := range []struct{ key, username, password string }{{"short", "admin", testAdminPassword}, {"reauth-key", "", testAdminPassword}, {"reauth-key", "admin", ""}} {
		if _, raw, err := a.Reauthenticate(t.Context(), cookie, input.key, input.username, input.password); raw != "" || !errors.Is(err, ErrAuthInput) {
			t.Fatal("invalid reauth input accepted")
		}
	}
	issueGrant(t, a, cookie, "reauth-key")
	execSQL(t, s.db, "UPDATE api_idempotency SET response_json='{}'")
	if _, raw, err := a.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword); raw != "" || !errors.Is(err, ErrAuthState) {
		t.Fatal("corrupt metadata reconstructed a grant")
	}
	encoded, err := json.Marshal(ReauthGrant{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeReauthGrant(string(encoded), AuthSession{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("empty metadata was accepted")
	}
}
