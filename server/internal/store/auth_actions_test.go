package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"homebox/internal/password"
)

const changedAdminPassword = "new correct horse battery staple"

func TestPasswordChangeRevokesAllSessionsAndReplaysOnlyItsFrozenAck(t *testing.T) {
	s, a, _ := authFixture(t)
	admin := provision(t, a)
	_, first := login(t, a)
	_, second := login(t, a)
	ack, err := a.ChangePassword(t.Context(), first, "password-key", testAdminPassword, changedAdminPassword)
	if err != nil || !ack.OK || ack.Revision != admin.Revision+1 {
		t.Fatalf("password change failed: %v", err)
	}
	if scalar[bool](t, s.db, "SELECT must_change_password FROM admin_accounts") || scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL") != 0 {
		t.Fatal("password change left mandatory-change state or live sessions")
	}
	for _, token := range []string{first, second} {
		if _, err = a.Authenticate(t.Context(), token); !errors.Is(err, ErrAuthentication) {
			t.Fatal("a pre-change session still authenticated")
		}
	}
	replayed, err := a.ChangePassword(t.Context(), first, "password-key", testAdminPassword, changedAdminPassword)
	if err != nil || replayed != ack {
		t.Fatal("exact password-change retry did not return the frozen acknowledgement")
	}
	if _, err = a.ChangePassword(t.Context(), first, "password-key", testAdminPassword, changedAdminPassword+"different"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed credential request reused an idempotency key")
	}
	if _, err = a.Logout(t.Context(), first, "password-key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("a different auth operation reused the same key")
	}
	if _, err = a.ChangePassword(t.Context(), second, "password-key", testAdminPassword, changedAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("another revoked session retrieved a scoped acknowledgement")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_changed'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 1 {
		t.Fatal("password replay duplicated effects")
	}
	if _, _, err = a.Login(t.Context(), admin.Username, testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("the old password still worked")
	}
	session, token, err := a.Login(t.Context(), admin.Username, changedAdminPassword)
	if err != nil || token == "" || session.Admin.MustChangePassword || session.Admin.Revision != ack.Revision {
		t.Fatalf("new password did not complete initial credential rotation: %v", err)
	}
	body := scalar[string](t, s.db, "SELECT response_json FROM api_idempotency")
	fingerprint := scalar[string](t, s.db, "SELECT request_sha256 FROM api_idempotency")
	if len(fingerprint) != 64 || strings.Contains(body, testAdminPassword) || strings.Contains(body, changedAdminPassword) || strings.Contains(body, first) || fingerprint == authDigest(changedAdminPassword) {
		t.Fatal("persistent idempotency state exposed credential material")
	}
}

func TestPasswordPolicyAndWrongProofLeaveCredentialsUnchanged(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
	cases := []struct {
		key, old, next string
		want           error
	}{
		{"short", testAdminPassword, changedAdminPassword, ErrAuthInput},
		{"valid-key", "", changedAdminPassword, ErrAuthInput},
		{"valid-key", testAdminPassword, "short", password.ErrPassword},
		{"valid-key", testAdminPassword, testAdminPassword, ErrPasswordUnchanged},
		{"valid-key", "wrong old password", changedAdminPassword, ErrPasswordMismatch},
	}
	for _, input := range cases {
		if _, err := a.ChangePassword(t.Context(), token, input.key, input.old, input.next); !errors.Is(err, input.want) {
			t.Fatalf("incorrect validation result: %v", err)
		}
	}
	if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_change_failed'") != 1 {
		t.Fatal("a failed password proof changed account state")
	}
	if _, err := a.Authenticate(t.Context(), token); err != nil {
		t.Fatal("a wrong password proof revoked the otherwise-valid session", err)
	}
}

func TestPasswordChangeRevalidatesItsProofAndLifetimeAtCommit(t *testing.T) {
	for _, mutation := range []string{"revision", "hash", "revocation", "epoch", "idle expiry"} {
		t.Run(mutation, func(t *testing.T) {
			s, a, now := authFixture(t)
			provision(t, a)
			_, token := login(t, a)
			fingerprint, err := authFingerprint(token, "password-key", passwordOperation, passwordChangeRequest{testAdminPassword, changedAdminPassword})
			if err != nil {
				t.Fatal(err)
			}
			proof, _, err := a.preparePasswordChange(t.Context(), token, "password-key", fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := a.hasher.Hash(t.Context(), changedAdminPassword)
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
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", strings.Repeat("c", 32))
			case "idle expiry":
				*now = now.Add(a.idle)
			}
			before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
			if ack, err := a.commitPasswordChange(t.Context(), token, "password-key", fingerprint, proof, encoded); ack.OK || !errors.Is(err, ErrAuthentication) {
				t.Fatal("stale credential proof changed a password")
			}
			if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 {
				t.Fatal("stale proof left partial state")
			}
		})
	}
}

func TestConcurrentVerifiedPasswordChangeUsesTheCommittedResponse(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	fingerprint, err := authFingerprint(token, "password-key", passwordOperation, passwordChangeRequest{testAdminPassword, changedAdminPassword})
	if err != nil {
		t.Fatal(err)
	}
	proof1, _, err := a.preparePasswordChange(t.Context(), token, "password-key", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	proof2, _, err := a.preparePasswordChange(t.Context(), token, "password-key", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := a.hasher.Hash(t.Context(), changedAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.commitPasswordChange(t.Context(), token, "password-key", fingerprint, proof1, encoded)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.commitPasswordChange(t.Context(), token, "password-key", fingerprint, proof2, encoded)
	if err != nil || first != second || scalar[int](t, s.db, "SELECT revision FROM admin_accounts") != 2 {
		t.Fatal("a duplicate in-flight proof did not replay the original commit")
	}
}

func TestPasswordChangeRollsBackEveryDependentWrite(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"admin_accounts", "UPDATE"}, {"sessions", "UPDATE"}, {"security_audit", "INSERT"}, {"api_idempotency", "INSERT"},
	} {
		t.Run(failure.table, func(t *testing.T) {
			s, a, _ := authFixture(t)
			provision(t, a)
			_, token := login(t, a)
			before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
			execSQL(t, s.db, "CREATE TRIGGER fail_password_write BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			if ack, err := a.ChangePassword(t.Context(), token, "password-key", testAdminPassword, changedAdminPassword); err == nil || ack.OK {
				t.Fatal("failed password mutation returned success")
			}
			if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT revision FROM admin_accounts") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_changed'") != 0 {
				t.Fatal("dependent write failure leaked password-change effects")
			}
		})
	}
}

func TestPasswordChangeCommitFailureDiscardsTheAcknowledgement(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
	execSQL(t, s.db, "CREATE TABLE password_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER fail_password_commit AFTER INSERT ON security_audit WHEN new.event_type='password_changed' BEGIN INSERT INTO password_commit_guard VALUES('missing'); END")
	if ack, err := a.ChangePassword(t.Context(), token, "password-key", testAdminPassword, changedAdminPassword); err == nil || ack.OK {
		t.Fatal("failed commit returned a successful password acknowledgement")
	}
	if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 {
		t.Fatal("deferred commit failure did not roll back password and sessions")
	}
	if _, err := a.Authenticate(t.Context(), token); err != nil {
		t.Fatal("pooled connection was not reusable", err)
	}
}

func TestAuthFingerprintsAreKeyedScopedAndRejectInvalidCookies(t *testing.T) {
	_, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	payload := passwordChangeRequest{testAdminPassword, changedAdminPassword}
	first, err := authFingerprint(token, "password-key", passwordOperation, payload)
	if err != nil || len(first) != 64 {
		t.Fatal("invalid credential fingerprint", err)
	}
	otherKey, _ := authFingerprint(token, "other-key", passwordOperation, payload)
	otherOperation, _ := authFingerprint(token, "password-key", logoutOperation, payload)
	otherCookie, _ := authFingerprint(strings.Repeat("A", 43), "password-key", passwordOperation, payload)
	if first == otherKey || first == otherOperation || first == otherCookie || first == authDigest(changedAdminPassword) {
		t.Fatal("credential fingerprint lost its key, operation, or session binding")
	}
	if _, err := authFingerprint("bad", "password-key", passwordOperation, payload); !errors.Is(err, ErrAuthentication) {
		t.Fatal("invalid cookie reached credential fingerprinting")
	}
	if _, err := authFingerprint(token, "password-key", passwordOperation, make(chan int)); err == nil {
		t.Fatal("unencodable request reached idempotency state")
	}
}

func TestPasswordChangeDoesNotAuthorizeAnExpiredSession(t *testing.T) {
	_, a, now := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	*now = now.Add(24 * time.Hour)
	if _, err := a.ChangePassword(t.Context(), token, "password-key", testAdminPassword, changedAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("expired session changed credentials")
	}
}
