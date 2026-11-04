package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homebox/internal/password"
)

func operatorAuthFixture(t *testing.T) (*Store, *Auth) {
	t.Helper()
	s, err := Open(t.Context(), Config{Path: filepath.Join(privateTempDir(t), "homebox.sqlite"), Migrate: true, OfflineAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	a, err := s.NewAuth(AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC) }
	return s, a
}

func TestRecoveryRequiresExplicitOfflineOwnerModeBeforeAnyFileCreation(t *testing.T) {
	_, a, _ := authFixture(t)
	path := filepath.Join(privateTempDir(t), "absent", "recovery.txt")
	if _, err := a.RecoverPasswordFile(t.Context(), path); !errors.Is(err, ErrOfflineOperator) {
		t.Fatal("serving store allowed credential recovery")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused recovery created a directory")
	}
	if _, err := a.recoverPassword(t.Context(), func(string) error { return nil }); !errors.Is(err, ErrOfflineOperator) {
		t.Fatal("private entry bypassed owner mode")
	}
	if _, err := a.commitRecovery(t.Context(), Admin{}, "unused", "unused", func(string) error { return nil }); !errors.Is(err, ErrOfflineOperator) {
		t.Fatal("commit entry bypassed owner mode")
	}
}

func TestRecoveryDeliversRandomPrivatePasswordAndRevokesExistingAuthority(t *testing.T) {
	s, a := operatorAuthFixture(t)
	admin := provision(t, a)
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0,revision=2")
	_, first := login(t, a)
	_, second := login(t, a)
	_, capability := issueGrant(t, a, first, "reauth-key")
	path := filepath.Join(privateTempDir(t), "recovery-password.txt")
	result, err := a.RecoverPasswordFile(t.Context(), path)
	if err != nil || result.Path != path || result.Revision != 3 || result.Username != admin.Username || !result.MustChangePassword || result.OperationID == "" {
		t.Fatal("recovery failed", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	temporary := strings.TrimSuffix(string(content), "\n")
	if len(temporary) != 26 || temporary == testAdminPassword {
		t.Fatal("temporary password was not independently generated")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("temporary password file was not private", err)
	}
	metadata, err := json.Marshal(result)
	if err != nil || strings.Contains(string(metadata), temporary) {
		t.Fatal("recovery metadata disclosed the password")
	}
	for _, cookie := range []string{first, second} {
		if _, err := a.Authenticate(t.Context(), cookie); !errors.Is(err, ErrAuthentication) {
			t.Fatal("recovery retained a prior session")
		}
	}
	if err := consumeGrant(t.Context(), a, first, capability); !errors.Is(err, ErrAuthentication) {
		t.Fatal("recovery retained a prior capability")
	}
	if _, _, err := a.Login(t.Context(), admin.Username, testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("old password remained valid")
	}
	session, cookie, err := a.Login(t.Context(), admin.Username, temporary)
	if err != nil || !session.Admin.MustChangePassword || session.Admin.Revision != result.Revision {
		t.Fatal("temporary login did not require rotation", err)
	}
	if _, raw, err := a.Reauthenticate(t.Context(), cookie, "fresh-reauth-key", admin.Username, temporary); raw != "" || !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("temporary password bypassed mandatory change")
	}
	if _, err := a.ChangePassword(t.Context(), cookie, "password-key", temporary, changedAdminPassword); err != nil {
		t.Fatal("recovered account could not complete rotation", err)
	}
	if _, _, err := a.Login(t.Context(), admin.Username, changedAdminPassword); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_recovered'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 {
		t.Fatal("recovery audit/projection effects were incorrect")
	}
}

func TestRecoveryWorksForUnsupportedLegacyHashesWithoutSilentlyAcceptingThem(t *testing.T) {
	s, a := operatorAuthFixture(t)
	admin := provision(t, a)
	execSQL(t, s.db, "UPDATE admin_accounts SET password_hash='unsupported-legacy-hash'")
	path := filepath.Join(privateTempDir(t), "recovery.txt")
	if _, err := a.RecoverPasswordFile(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Login(t.Context(), admin.Username, strings.TrimSuffix(string(content), "\n")); err != nil {
		t.Fatal("legacy account could not be recovered", err)
	}
}

func TestRecoveryRefusesMissingAccountsAndExistingOutputsWithoutChangingState(t *testing.T) {
	s, a := operatorAuthFixture(t)
	path := filepath.Join(privateTempDir(t), "missing-account.txt")
	if _, err := a.RecoverPasswordFile(t.Context(), path); !errors.Is(err, ErrNotProvisioned) {
		t.Fatal("unconfigured installation was recovered")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused recovery left a file")
	}
	provision(t, a)
	before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
	if err := os.WriteFile(path, []byte("keep this file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecoverPasswordFile(t.Context(), path); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing recovery file was overwritten")
	}
	if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_recovered'") != 0 {
		t.Fatal("refused output still changed credentials")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.RecoverPasswordFile(ctx, filepath.Join(filepath.Dir(path), "canceled.txt")); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled recovery did not stop")
	}
}

func TestRecoveryRejectsStaleAccountPreparationAndDeliveryFailure(t *testing.T) {
	s, a := operatorAuthFixture(t)
	provision(t, a)
	target, _, err := loadAdmin(t.Context(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET revision=revision+1")
	delivered := false
	if _, err := a.commitRecovery(t.Context(), target, "unused", "unused", func(string) error { delivered = true; return nil }); !errors.Is(err, ErrRecoveryChanged) || delivered {
		t.Fatal("stale target activated a new password")
	}
	before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
	planned := errors.New("planned delivery failure")
	if ack, err := a.recoverPassword(t.Context(), func(string) error { return planned }); ack.OperationID != "" || !errors.Is(err, planned) {
		t.Fatal("delivery failure was ignored")
	}
	if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before {
		t.Fatal("failed delivery changed the hash")
	}
}

func TestRecoveryRollsBackDependentWritesAndFailedCommit(t *testing.T) {
	for _, failure := range []struct{ table, event string }{{"admin_accounts", "UPDATE"}, {"sessions", "UPDATE"}, {"security_audit", "INSERT"}, {"auth_bootstrap", "DELETE"}} {
		t.Run(failure.table, func(t *testing.T) {
			s, a := operatorAuthFixture(t)
			provision(t, a)
			_, cookie := login(t, a)
			before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
			if failure.table == "auth_bootstrap" {
				execSQL(t, s.db, "INSERT INTO auth_bootstrap(singleton,token_hash,created_at,expires_at,dataset_epoch) SELECT 1,?,'2026','2099',dataset_epoch FROM sync_state", strings.Repeat("a", 64))
			}
			execSQL(t, s.db, "CREATE TRIGGER fail_recovery_write BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			path := filepath.Join(privateTempDir(t), "failed-recovery.txt")
			if result, err := a.RecoverPasswordFile(t.Context(), path); err == nil || result.Path != "" {
				t.Fatal("failed write reported recovery success")
			}
			if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='password_recovered'") != 0 {
				t.Fatal("failed recovery partially changed authority")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed recovery left a secret file")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_recovery_write")
			if _, err := a.Authenticate(t.Context(), cookie); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("commit after durable delivery", func(t *testing.T) {
		s, a := operatorAuthFixture(t)
		provision(t, a)
		before := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
		execSQL(t, s.db, "CREATE TABLE recovery_commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
		execSQL(t, s.db, "CREATE TRIGGER fail_recovery_commit AFTER INSERT ON security_audit WHEN new.event_type='password_recovered' BEGIN INSERT INTO recovery_commit_guard VALUES('missing'); END")
		path := filepath.Join(privateTempDir(t), "failed-recovery.txt")
		if result, err := a.RecoverPasswordFile(t.Context(), path); err == nil || result.Path != "" {
			t.Fatal("failed commit reported recovery success")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed commit retained a recovery file")
		}
		if scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts") != before {
			t.Fatal("failed commit retained a recovered password")
		}
	})
}
