package store

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"homebox/migrations"
)

func TestKnownCookieAndRequestKeyCannotReconstructAnIssuedReauthCapability(t *testing.T) {
	_, a, _, _, cookie := completedAuthFixture(t)
	_, capability := issueGrant(t, a, cookie, "known-reauth-key")
	// Reproduce the retired client-derivable protocol against an actually issued row.
	legacy := hmac.New(sha256.New, []byte(cookie))
	_, _ = legacy.Write([]byte("reauth-token-v1"))
	_, _ = legacy.Write([]byte{0})
	_, _ = legacy.Write([]byte("known-reauth-key"))
	forged := base64.RawURLEncoding.EncodeToString(legacy.Sum(nil))
	if forged == capability {
		t.Fatal("reauthentication remained derivable from cookie/key")
	}
	if err := consumeGrant(t.Context(), a, cookie, forged); !errors.Is(err, ErrReauthRequired) {
		t.Fatal("cookie/key derivation consumed an issued capability")
	}
	guess := *a
	guess.serverSecret = make([]byte, 32)
	if guess.reauthToken(cookie, "known-reauth-key") == capability {
		t.Fatal("unknown server secret did not protect derivation")
	}
	if err := consumeGrant(t.Context(), a, cookie, capability); err != nil {
		t.Fatal("real capability was rejected", err)
	}
}

func TestIndependentServerSecretPersistsAndIsNeverPublicMetadata(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	grant, capability := issueGrant(t, a, cookie, "reauth-key")
	if len(a.serverSecret) != 32 || scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 1 {
		t.Fatal("server secret was not initialized")
	}
	again, err := s.NewAuth(AuthConfig{})
	if err != nil || !bytes.Equal(again.serverSecret, a.serverSecret) {
		t.Fatal("server secret changed between instances", err)
	}
	other := openTestStore(t)
	otherAuth, err := other.NewAuth(AuthConfig{})
	if err != nil || bytes.Equal(otherAuth.serverSecret, a.serverSecret) {
		t.Fatal("different databases shared a server secret", err)
	}
	if otherAuth.reauthToken(cookie, "reauth-key") == capability {
		t.Fatal("capability lacked independent server binding")
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
	next, err := reopened.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	next.now = a.now
	replay, raw, err := next.Reauthenticate(t.Context(), cookie, "reauth-key", "家中管理員", testAdminPassword)
	if err != nil || raw != capability || !replay.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatal("restart broke exact capability replay", err)
	}
}

func TestMissingServerSecretWithOutstandingCapabilitiesFailsClosed(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	issueGrant(t, a, cookie, "reauth-key")
	execSQL(t, s.db, "DELETE FROM auth_server_secret")
	if _, err := s.NewAuth(AuthConfig{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("missing secret silently regenerated incompatible authorization state")
	}
	if _, err := new(Auth).authFingerprint(cookie, "reauth-key", reauthOperation, struct{}{}); !errors.Is(err, ErrAuthState) {
		t.Fatal("zero-value authentication used a predictable MAC key")
	}
}

func TestServerSecretCreationRollsBackWhenAuditFails(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "CREATE TRIGGER fail_key_audit BEFORE INSERT ON security_audit BEGIN SELECT RAISE(ABORT,'audit failure'); END")
	if _, err := s.NewAuth(AuthConfig{}); err == nil {
		t.Fatal("unaudited server key was accepted")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 {
		t.Fatal("failed key initialization left material behind")
	}
}

func TestServerSecretMigrationInvalidatesOldCapabilitiesButKeepsDomainReplies(t *testing.T) {
	path, old := legacyFixture(t, 2)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	previous := &Store{db: old, path: path}
	if _, err = previous.migrate(t.Context(), Config{Migrate: true, AllowLegacy: true, ReserveBytes: defaultReserveBytes}, all[:5]); err != nil {
		t.Fatal(err)
	}
	execSQL(t, old, "INSERT INTO admin_accounts(id,username,password_hash,created_at,updated_at,must_change_password) VALUES('old-admin','admin','legacy-hash','2026','2026',0)")
	epoch := scalar[string](t, old, "SELECT dataset_epoch FROM sync_state")
	execSQL(t, old, `INSERT INTO sessions(id,admin_id,token_hash,expires_at,created_at,last_seen_at,dataset_epoch)
 VALUES('old-session','old-admin',?,'2099-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',?)`, strings.Repeat("a", 64), epoch)
	execSQL(t, old, "INSERT INTO auth_reauth_grants VALUES(?,'old-session',1,'2026-01-01T00:00:00Z','2099-01-01T00:00:00Z',NULL)", strings.Repeat("b", 64))
	execSQL(t, old, `INSERT INTO api_idempotency VALUES('old-auth','post_auth_reauth','fingerprint',200,'{}','2026','2099'),
 ('kept-domain','post_items','fingerprint',200,'{}','2026','2099')`)
	if err = previous.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if scalar[int](t, current.db, "SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL") != 0 || scalar[int](t, current.db, "SELECT COUNT(*) FROM auth_reauth_grants") != 0 || scalar[int](t, current.db, "SELECT COUNT(*) FROM api_idempotency WHERE operation='post_auth_reauth'") != 0 {
		t.Fatal("retired authorization state survived migration")
	}
	if scalar[int](t, current.db, "SELECT COUNT(*) FROM api_idempotency WHERE key='kept-domain'") != 1 {
		t.Fatal("auth migration removed a domain acknowledgement")
	}
}
