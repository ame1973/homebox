package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"homebox/internal/password"
	"homebox/migrations"
)

const testAdminPassword = "correct horse battery staple"

func authFixture(t *testing.T) (*Store, *Auth, *time.Time) {
	t.Helper()
	s := openTestStore(t)
	a, err := s.NewAuth(AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	return s, a, &now
}

func provision(t *testing.T, a *Auth) Admin {
	t.Helper()
	token, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := a.Setup(t.Context(), token, "家中管理員", testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	return admin
}

func login(t *testing.T, a *Auth) (AuthSession, string) {
	t.Helper()
	session, token, err := a.Login(t.Context(), "家中管理員", testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	return session, token
}

func TestBootstrapIsHashedRotatableConsumedAndOneTime(t *testing.T) {
	s, a, now := authFixture(t)
	first, expires, err := a.IssueBootstrap(t.Context())
	if err != nil || !validAuthToken(first) || !expires.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("invalid bootstrap metadata: %v", err)
	}
	if scalar[string](t, s.db, "SELECT token_hash FROM auth_bootstrap") != authDigest(first) {
		t.Fatal("bootstrap secret was not hashed")
	}
	second, _, err := a.IssueBootstrap(t.Context())
	if err != nil || first == second {
		t.Fatal("bootstrap rotation failed")
	}
	if _, err = a.Setup(t.Context(), first, "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("superseded bootstrap token worked")
	}
	admin, err := a.Setup(t.Context(), second, "家中管理員", testAdminPassword)
	if err != nil || admin.Revision != 1 || !admin.MustChangePassword || admin.ID == "" {
		t.Fatalf("provisioning failed: %v", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM admin_accounts") != 1 {
		t.Fatal("bootstrap consumption was incomplete")
	}
	if _, _, err = a.IssueBootstrap(t.Context()); !errors.Is(err, ErrSetupClosed) {
		t.Fatal("bootstrap reopened a configured installation")
	}
	if _, err = a.Setup(t.Context(), second, "different-admin", testAdminPassword); !errors.Is(err, ErrSetupClosed) {
		t.Fatal("repeated setup was accepted")
	}
	if _, err = s.db.ExecContext(t.Context(), `INSERT INTO admin_accounts(id,username,password_hash,created_at,updated_at) VALUES('second','second','unused','2026','2026')`); err == nil {
		t.Fatal("database allowed a second administrator")
	}
	encoded := scalar[string](t, s.db, "SELECT password_hash FROM admin_accounts")
	if !strings.HasPrefix(encoded, "$argon2id$") || strings.Contains(encoded, testAdminPassword) {
		t.Fatal("password was not stored as Argon2id")
	}
	body, err := json.Marshal(admin)
	if err != nil || strings.Contains(string(body), "hash") || strings.Contains(string(body), second) {
		t.Fatal("public account metadata leaked a credential")
	}
}

func TestLoginRejectsBadCredentialsAndCreatesIndependentHashedSessions(t *testing.T) {
	s, a, _ := authFixture(t)
	if _, token, err := a.Login(t.Context(), "unknown", testAdminPassword); token != "" || !errors.Is(err, ErrAuthentication) {
		t.Fatal("login before provisioning was accepted")
	}
	admin := provision(t, a)
	for _, pair := range [][2]string{{"unknown", testAdminPassword}, {admin.Username, "wrong password"}} {
		if _, token, err := a.Login(t.Context(), pair[0], pair[1]); token != "" || !errors.Is(err, ErrAuthentication) {
			t.Fatal("invalid credentials created a session")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='login_failed'") != 3 {
		t.Fatal("failed login effects are incorrect")
	}
	first, token1 := login(t, a)
	second, token2 := login(t, a)
	if token1 == token2 || first.ID == second.ID || first.Admin != admin || !validAuthToken(token1) {
		t.Fatal("login reused session identity or returned wrong account metadata")
	}
	if scalar[string](t, s.db, "SELECT token_hash FROM sessions WHERE id=?", first.ID) != authDigest(token1) {
		t.Fatal("session storage did not use a hash")
	}
	for _, invalid := range []string{first.ID, authDigest(token1), token1 + "extra", strings.Repeat("!", 43)} {
		if _, err := a.Authenticate(t.Context(), invalid); !errors.Is(err, ErrAuthentication) {
			t.Fatal("non-token identifier authenticated")
		}
	}
	if current, err := a.Authenticate(t.Context(), token1); err != nil || current.Admin != admin {
		t.Fatalf("valid session rejected: %v", err)
	}
	if details := scalar[string](t, s.db, "SELECT group_concat(details_json) FROM security_audit"); strings.Contains(details, testAdminPassword) || strings.Contains(details, token1) || strings.Contains(details, admin.Username) {
		t.Fatal("security audit disclosed sensitive input")
	}
}

func TestLogoutIsSessionScopedAtomicAndIdempotent(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, first := login(t, a)
	_, second := login(t, a)
	ack, err := a.Logout(t.Context(), first, "logout-key-1")
	if err != nil || !ack.OK || ack.OperationID == "" {
		t.Fatalf("logout failed: %v", err)
	}
	if _, err = a.Authenticate(t.Context(), first); !errors.Is(err, ErrAuthentication) {
		t.Fatal("revoked session still authenticated")
	}
	if _, err = a.Authenticate(t.Context(), second); err != nil {
		t.Fatal("logout revoked an unrelated session")
	}
	replay, err := a.Logout(t.Context(), first, "logout-key-1")
	if err != nil || replay != ack {
		t.Fatal("logout retry did not return its frozen acknowledgement")
	}
	if _, err = a.Logout(t.Context(), first, "different-key"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("revoked credential authorized a new action")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='logout'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 1 {
		t.Fatal("logout replay duplicated its effects")
	}
	other, err := a.Logout(t.Context(), second, "logout-key-1")
	if err != nil || other == ack {
		t.Fatal("idempotency was not scoped to the authenticated session")
	}
}

func TestSessionIdleAndAbsoluteExpiryAndClockRollback(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		s, a, now := authFixture(t)
		provision(t, a)
		session, token := login(t, a)
		*now = now.Add(a.idle - time.Nanosecond)
		if _, err := a.Authenticate(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		seen := scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID)
		*now = now.Add(a.idle)
		if _, err := a.Authenticate(t.Context(), token); !errors.Is(err, ErrAuthentication) {
			t.Fatal("idle boundary did not expire")
		}
		if scalar[string](t, s.db, "SELECT last_seen_at FROM sessions WHERE id=?", session.ID) != seen {
			t.Fatal("rejected session extended its own activity")
		}
	})
	t.Run("absolute", func(t *testing.T) {
		s, a, now := authFixture(t)
		a.lifetime, a.idle = 3*time.Minute, time.Minute
		provision(t, a)
		session, token := login(t, a)
		for range 3 {
			*now = now.Add(50 * time.Second)
			if _, err := a.Authenticate(t.Context(), token); err != nil {
				t.Fatal(err)
			}
		}
		*now = session.ExpiresAt
		if _, err := a.Authenticate(t.Context(), token); !errors.Is(err, ErrAuthentication) {
			t.Fatal("activity extended the absolute lifetime")
		}
		if scalar[string](t, s.db, "SELECT expires_at FROM sessions WHERE id=?", session.ID) != stamp(session.ExpiresAt) {
			t.Fatal("absolute expiry changed")
		}
	})
	t.Run("clock rollback", func(t *testing.T) {
		_, a, now := authFixture(t)
		provision(t, a)
		_, token := login(t, a)
		*now = now.Add(-time.Second)
		if _, err := a.Authenticate(t.Context(), token); !errors.Is(err, ErrAuthentication) {
			t.Fatal("a clock earlier than recorded activity was trusted")
		}
	})
}

func TestBootstrapExpiryIsCheckedAgainAfterHashing(t *testing.T) {
	s, a, now := authFixture(t)
	token, expires, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	a.now = func() time.Time {
		checks++
		if checks >= 2 {
			return expires
		}
		return *now
	}
	if _, err := a.Setup(t.Context(), token, "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("expired password proof provisioned an account")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM admin_accounts") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 1 {
		t.Fatal("failed setup consumed state")
	}
}

func TestConcurrentSetupCannotCreateTwoAdministrators(t *testing.T) {
	s, a, _ := authFixture(t)
	token, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			<-start
			_, err := a.Setup(t.Context(), token, fmt.Sprintf("admin-%d", i), testAdminPassword)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, password.ErrBusy) && !errors.Is(err, ErrSetupClosed) && !errors.Is(err, ErrAuthentication) {
			t.Fatal(err)
		}
	}
	if successes != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM admin_accounts") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='admin_setup'") != 1 {
		t.Fatal("concurrent setup was not atomic")
	}
}

func TestLoginRejectsAStalePasswordProofAtCommit(t *testing.T) {
	for _, update := range []string{"UPDATE admin_accounts SET revision=revision+1", "UPDATE admin_accounts SET password_hash='changed'", "UPDATE admin_accounts SET username='renamed'", "UPDATE admin_accounts SET must_change_password=0"} {
		t.Run(update, func(t *testing.T) {
			s, a, _ := authFixture(t)
			provision(t, a)
			admin, hash, err := loadAdmin(t.Context(), s.db)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, update)
			if _, token, err := a.commitLogin(t.Context(), admin, hash); token != "" || !errors.Is(err, ErrAuthentication) {
				t.Fatal("stale verified credentials created a session")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions") != 0 {
				t.Fatal("stale proof left session state")
			}
		})
	}
}

func TestAuthMutationsRollbackWhenAuditOrIdempotencyFails(t *testing.T) {
	t.Run("bootstrap", func(t *testing.T) {
		s, a, _ := authFixture(t)
		execSQL(t, s.db, "CREATE TRIGGER fail_auth_audit BEFORE INSERT ON security_audit BEGIN SELECT RAISE(ABORT,'audit failure'); END")
		if token, _, err := a.IssueBootstrap(t.Context()); err == nil || token != "" {
			t.Fatal("failed audit returned a bootstrap credential")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 0 {
			t.Fatal("bootstrap survived audit failure")
		}
	})
	t.Run("setup", func(t *testing.T) {
		s, a, _ := authFixture(t)
		token, _, err := a.IssueBootstrap(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.db, "CREATE TRIGGER fail_auth_audit BEFORE INSERT ON security_audit BEGIN SELECT RAISE(ABORT,'audit failure'); END")
		if _, err := a.Setup(t.Context(), token, "admin", testAdminPassword); err == nil {
			t.Fatal("setup ignored audit failure")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM admin_accounts") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap") != 1 {
			t.Fatal("setup rollback lost its original state")
		}
	})
	t.Run("login", func(t *testing.T) {
		s, a, _ := authFixture(t)
		provision(t, a)
		execSQL(t, s.db, "CREATE TRIGGER fail_auth_audit BEFORE INSERT ON security_audit BEGIN SELECT RAISE(ABORT,'audit failure'); END")
		if _, token, err := a.Login(t.Context(), "家中管理員", testAdminPassword); err == nil || token != "" {
			t.Fatal("login ignored audit failure")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions") != 0 {
			t.Fatal("session survived audit failure")
		}
		if _, token, err := a.Login(t.Context(), "wrong", testAdminPassword); err == nil || token != "" {
			t.Fatal("failed login ignored audit storage failure")
		}
	})
	for _, table := range []string{"security_audit", "api_idempotency"} {
		t.Run("logout/"+table, func(t *testing.T) {
			s, a, _ := authFixture(t)
			provision(t, a)
			_, token := login(t, a)
			execSQL(t, s.db, "CREATE TRIGGER fail_auth_write BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
			if _, err := a.Logout(t.Context(), token, "logout-key"); err == nil {
				t.Fatal("logout ignored a dependent write failure")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit WHERE event_type='logout'") != 0 {
				t.Fatal("logout partial state escaped rollback")
			}
		})
	}
}

func TestAuthValidationCancellationAndClosedStore(t *testing.T) {
	s, a, _ := authFixture(t)
	for _, cfg := range []AuthConfig{{IdleTimeout: -time.Minute}, {SessionLifetime: time.Minute}, {SessionLifetime: 8 * 24 * time.Hour}, {PasswordParams: password.Params{MemoryKiB: 1}}} {
		if _, err := s.NewAuth(cfg); err == nil {
			t.Fatal("unsafe authentication configuration accepted")
		}
	}
	if _, err := s.NewAuth(AuthConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Setup(t.Context(), "bad-token", "", testAdminPassword); !errors.Is(err, ErrAuthInput) {
		t.Fatal("empty username accepted")
	}
	for _, pair := range [][2]string{{"", testAdminPassword}, {"admin", ""}, {strings.Repeat("x", 65), testAdminPassword}, {"admin", "\xff"}} {
		if _, _, err := a.Login(t.Context(), pair[0], pair[1]); !errors.Is(err, ErrAuthInput) {
			t.Fatal("invalid login input accepted")
		}
	}
	if _, err := a.Logout(t.Context(), "bad-token", "short"); !errors.Is(err, ErrAuthInput) {
		t.Fatal("invalid idempotency key accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := a.IssueBootstrap(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled bootstrap did not stop")
	}
	if _, err := a.Authenticate(ctx, strings.Repeat("A", 43)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled session validation did not stop")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.IssueBootstrap(t.Context()); err == nil {
		t.Fatal("closed store issued credentials")
	}
	if _, _, err := a.Login(t.Context(), "admin", testAdminPassword); err == nil {
		t.Fatal("closed store authenticated")
	}
}

func TestCorruptSessionAndIdempotentReplyFailClosed(t *testing.T) {
	for _, field := range []string{"created_at", "expires_at", "last_seen_at"} {
		t.Run(field, func(t *testing.T) {
			s, a, _ := authFixture(t)
			provision(t, a)
			_, token := login(t, a)
			execSQL(t, s.db, "UPDATE sessions SET "+field+"='invalid'")
			if _, err := a.Authenticate(t.Context(), token); !errors.Is(err, ErrAuthState) {
				t.Fatal("corrupt session timestamps were accepted")
			}
		})
	}
	s, a, _ := authFixture(t)
	provision(t, a)
	_, token := login(t, a)
	if _, err := a.Logout(t.Context(), token, "logout-key"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE api_idempotency SET response_json='{}'")
	if _, err := a.Logout(t.Context(), token, "logout-key"); !errors.Is(err, ErrAuthState) {
		t.Fatal("corrupt frozen response was acknowledged")
	}
}

func TestBootstrapAndSessionsSurviveReopenWithoutPlaintextCredentials(t *testing.T) {
	s, a, now := authFixture(t)
	token, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next, err := reopened.NewAuth(AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	next.now = func() time.Time { return now.Add(time.Minute) }
	if _, err = next.Setup(t.Context(), token, "家中管理員", testAdminPassword); err != nil {
		t.Fatal("persisted bootstrap did not remain usable", err)
	}
	session, cookie := login(t, next)
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	auth, err := last.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now.Add(2 * time.Minute) }
	if actual, err := auth.Authenticate(t.Context(), cookie); err != nil || actual.ID != session.ID {
		t.Fatalf("persisted session did not remain valid: %v", err)
	}
	if scalar[int](t, last.db, "SELECT COUNT(*) FROM sync_changes") != 0 {
		t.Fatal("private authentication metadata entered the offline outbox")
	}
	stored := scalar[string](t, last.db, "SELECT token_hash FROM sessions")
	if strings.Contains(stored, cookie) || len(stored) != 64 {
		t.Fatal("raw cookie survived in persistent session state")
	}
}

func TestBootstrapMalformedTokenClockAndTimestampFailClosed(t *testing.T) {
	s, a, now := authFixture(t)
	if _, err := a.Setup(t.Context(), strings.Repeat("A", 43), "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("setup worked without an issued bootstrap secret")
	}
	token, _, err := a.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Setup(t.Context(), "malformed", "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("malformed bootstrap token accepted")
	}
	if _, err := a.Setup(t.Context(), token, "admin", "short"); !errors.Is(err, password.ErrPassword) {
		t.Fatal("weak initial password accepted")
	}
	*now = now.Add(-time.Second)
	if _, err := a.Setup(t.Context(), token, "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
		t.Fatal("bootstrap accepted a clock earlier than issuance")
	}
	*now = now.Add(time.Second)
	execSQL(t, s.db, "UPDATE auth_bootstrap SET expires_at='invalid'")
	if _, err := a.Setup(t.Context(), token, "admin", testAdminPassword); !errors.Is(err, ErrAuthState) {
		t.Fatal("corrupt bootstrap timestamp accepted")
	}
}

func TestDatasetEpochRotationInvalidatesSetupAndSessionCredentials(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		s, a, _ := authFixture(t)
		token, _, err := a.IssueBootstrap(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", strings.Repeat("b", 32))
		if _, err = a.Setup(t.Context(), token, "admin", testAdminPassword); !errors.Is(err, ErrAuthentication) {
			t.Fatal("restored setup credential remained usable")
		}
		provision(t, a)
	})
	t.Run("sessions and frozen logout", func(t *testing.T) {
		s, a, _ := authFixture(t)
		provision(t, a)
		_, active := login(t, a)
		_, revoked := login(t, a)
		if _, err := a.Logout(t.Context(), revoked, "logout-key"); err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch=?", strings.Repeat("b", 32))
		if _, err := a.Authenticate(t.Context(), active); !errors.Is(err, ErrAuthentication) {
			t.Fatal("dataset rotation resurrected an old session")
		}
		if _, err := a.Logout(t.Context(), revoked, "logout-key"); !errors.Is(err, ErrAuthentication) {
			t.Fatal("an old dataset served an authenticated frozen response")
		}
		_, fresh := login(t, a)
		if _, err := a.Authenticate(t.Context(), fresh); err != nil {
			t.Fatal("new credentials did not bind to the current dataset", err)
		}
	})
}

func TestAuthCommitFailuresDiscardResultsAndReturnACleanConnection(t *testing.T) {
	for _, operation := range []string{"bootstrap", "setup", "login", "logout"} {
		t.Run(operation, func(t *testing.T) {
			s, a, _ := authFixture(t)
			var run func() (string, error)
			switch operation {
			case "bootstrap":
				run = func() (string, error) { token, _, err := a.IssueBootstrap(t.Context()); return token, err }
			case "setup":
				token, _, err := a.IssueBootstrap(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				run = func() (string, error) {
					admin, err := a.Setup(t.Context(), token, "admin", testAdminPassword)
					return admin.ID, err
				}
			case "login":
				provision(t, a)
				run = func() (string, error) {
					_, token, err := a.Login(t.Context(), "家中管理員", testAdminPassword)
					return token, err
				}
			case "logout":
				provision(t, a)
				_, token := login(t, a)
				run = func() (string, error) {
					ack, err := a.Logout(t.Context(), token, "logout-key")
					return ack.OperationID, err
				}
			}
			counts := func() [6]int {
				return [6]int{
					scalar[int](t, s.db, "SELECT COUNT(*) FROM admin_accounts"),
					scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_bootstrap"),
					scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions"),
					scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL"),
					scalar[int](t, s.db, "SELECT COUNT(*) FROM api_idempotency"),
					scalar[int](t, s.db, "SELECT COUNT(*) FROM security_audit"),
				}
			}
			before := counts()
			// The write succeeds; only COMMIT discovers the deferred FK failure.
			execSQL(t, s.db, "CREATE TABLE commit_guard(admin_id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
			execSQL(t, s.db, "CREATE TRIGGER fail_auth_commit AFTER INSERT ON security_audit BEGIN INSERT INTO commit_guard VALUES('nonexistent'); END")
			if value, err := run(); err == nil || value != "" {
				t.Fatal("a failed commit published credentials or a successful acknowledgement")
			}
			if counts() != before || scalar[int](t, s.db, "SELECT COUNT(*) FROM commit_guard") != 0 {
				t.Fatal("commit failure left partial authentication state")
			}
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal("failed commit left the pooled connection in a transaction", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEpochMigrationInvalidatesUnboundVersionThreeCredentials(t *testing.T) {
	path, old := legacyFixture(t, 2)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Create a genuine checksum-ledger v3 fixture through the runner.
	previous := &Store{db: old, path: path}
	if _, err = previous.migrate(t.Context(), Config{Migrate: true, AllowLegacy: true, ReserveBytes: defaultReserveBytes}, all[:3]); err != nil {
		t.Fatal(err)
	}
	execSQL(t, old, "INSERT INTO admin_accounts(id,username,password_hash,created_at,updated_at) VALUES('old-admin','old-admin','legacy-hash','2026','2026')")
	execSQL(t, old, `INSERT INTO sessions(id,admin_id,token_hash,expires_at,created_at,last_seen_at)
 VALUES('old-session','old-admin',?,'2099-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, strings.Repeat("a", 64))
	execSQL(t, old, "INSERT INTO auth_bootstrap VALUES(1,?,'2026-01-01T00:00:00Z','2099-01-01T00:00:00Z')", strings.Repeat("b", 64))
	if err = previous.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if current.Report().From != 3 || current.Report().To != len(all) {
		t.Fatal("version three upgrade was not recognized")
	}
	if err = VerifySnapshot(t.Context(), current.Report().BackupPath); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, current.db, "SELECT COUNT(*) FROM auth_bootstrap") != 0 || scalar[int](t, current.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL") != 0 {
		t.Fatal("unbound version three credentials survived migration")
	}
}

func TestAuthenticationMigrationRevokesReferenceSessionsAndRejectsMultipleAdmins(t *testing.T) {
	t.Run("reference sessions", func(t *testing.T) {
		path, old := legacyFixture(t, 2)
		execSQL(t, old, "INSERT INTO admin_accounts VALUES('legacy-admin','admin','legacy-hash','2026','2026')")
		execSQL(t, old, "INSERT INTO sessions VALUES('legacy-session','legacy-admin',?,'2099-01-01T00:00:00Z',NULL,'2026-01-01T00:00:00Z')", strings.Repeat("a", 64))
		_ = old.Close()
		s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL") != 0 || !scalar[bool](t, s.db, "SELECT must_change_password FROM admin_accounts") {
			t.Fatal("uncertified reference credentials remained active")
		}
		if scalar[string](t, s.db, "SELECT last_seen_at FROM sessions") != "2026-01-01T00:00:00Z" {
			t.Fatal("reference creation time was not preserved")
		}
	})
	t.Run("ambiguous accounts", func(t *testing.T) {
		path, old := legacyFixture(t, 2)
		execSQL(t, old, "INSERT INTO admin_accounts VALUES('one','one','unused','2026','2026'),('two','two','unused','2026','2026')")
		_ = old.Close()
		if _, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
			t.Fatal("ambiguous administrators were silently accepted")
		}
		db := rawDatabase(t, path)
		if scalar[int](t, db, "SELECT MAX(version) FROM schema_migrations") != 2 || scalar[int](t, db, "SELECT COUNT(*) FROM admin_accounts") != 2 || scalar[int](t, db, "SELECT COUNT(*) FROM pragma_table_info('admin_accounts') WHERE name='revision'") != 0 {
			t.Fatal("failed authentication migration altered legacy data")
		}
	})
}
