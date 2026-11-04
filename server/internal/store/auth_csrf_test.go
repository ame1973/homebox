package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCSRFIsSessionBoundServerKeyedAndSeparateFromCapabilities(t *testing.T) {
	s, a, _, _, cookie := completedAuthFixture(t)
	token, err := a.CSRFToken(t.Context(), cookie)
	if err != nil || !validAuthToken(token) || token == cookie {
		t.Fatal("invalid CSRF token", err)
	}
	if err = a.VerifyCSRF(cookie, token); err != nil {
		t.Fatal(err)
	}
	again, err := a.CSRFToken(t.Context(), cookie)
	if err != nil || again != token {
		t.Fatal("CSRF changed within a session", err)
	}
	_, otherCookie := login(t, a)
	other, err := a.CSRFToken(t.Context(), otherCookie)
	if err != nil || other == token || !errors.Is(a.VerifyCSRF(otherCookie, token), ErrCSRF) {
		t.Fatal("CSRF token moved between sessions", err)
	}
	_, grant := issueGrant(t, a, cookie, "csrf-grant-key")
	mac := hmac.New(sha256.New, []byte(cookie))
	_, _ = mac.Write([]byte("csrf-token-v1"))
	for _, forged := range []string{"", "bad", strings.Repeat("!", 43), cookie, authDigest(cookie), grant, base64.RawURLEncoding.EncodeToString(mac.Sum(nil))} {
		if !errors.Is(a.VerifyCSRF(cookie, forged), ErrCSRF) {
			t.Fatal("CSRF accepted a missing, malformed, or cross-purpose token")
		}
	}
	if _, raw, err := a.Reauthenticate(t.Context(), token, "csrf-not-a-cookie", "家中管理員", testAdminPassword); raw != "" || !errors.Is(err, ErrAuthentication) {
		t.Fatal("CSRF token was accepted as a session credential")
	}
	if err = consumeGrant(t.Context(), a, cookie, token); !errors.Is(err, ErrReauthRequired) {
		t.Fatal("CSRF token was accepted as a reauthentication capability")
	}
	for _, query := range []string{"SELECT group_concat(details_json) FROM security_audit", "SELECT group_concat(response_json) FROM api_idempotency"} {
		if strings.Contains(scalar[string](t, s.db, query), token) {
			t.Fatal("CSRF token was persisted in audit or replay data")
		}
	}
	_, independent, _ := authFixture(t)
	if !errors.Is(independent.VerifyCSRF(cookie, token), ErrCSRF) {
		t.Fatal("a different server secret verified the token")
	}
	clone, err := s.NewAuth(AuthConfig{})
	if err != nil || clone.VerifyCSRF(cookie, token) != nil {
		t.Fatal("persisted server key did not preserve CSRF binding", err)
	}
}

func TestCSRFIssuanceRequiresActiveSessionButAllowsMandatoryPasswordChange(t *testing.T) {
	for _, reason := range []string{"logout", "password change", "epoch", "idle", "absolute", "clock"} {
		t.Run(reason, func(t *testing.T) {
			s, a, now := authFixture(t)
			provision(t, a)
			_, cookie := login(t, a)
			token, err := a.CSRFToken(t.Context(), cookie)
			if err != nil {
				t.Fatal("mandatory-change session could not obtain CSRF", err)
			}
			switch reason {
			case "logout":
				if _, err = a.Logout(t.Context(), cookie, "csrf-logout-key"); err != nil {
					t.Fatal(err)
				}
			case "password change":
				if _, err = a.ChangePassword(t.Context(), cookie, "csrf-password-key", testAdminPassword, "a different strong password"); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='replacement'")
			case "idle":
				*now = now.Add(a.idle)
			case "absolute":
				*now = now.Add(a.lifetime)
			case "clock":
				*now = now.Add(-time.Nanosecond)
			}
			if result, err := a.CSRFToken(t.Context(), cookie); result != "" || !errors.Is(err, ErrAuthentication) {
				t.Fatal("inactive session obtained a CSRF token", err)
			}
			// The MAC is not an authorization decision. Every service still rejects
			// new mutations after expiry/revocation, even with the correct old MAC.
			if err = a.VerifyCSRF(cookie, token); err != nil {
				t.Fatal("MAC validation was incorrectly coupled to session authorization", err)
			}
			if _, err = a.Logout(t.Context(), cookie, "csrf-new-logout-key"); !errors.Is(err, ErrAuthentication) {
				t.Fatal("MAC possession revived an inactive session", err)
			}
		})
	}
}

func TestCSRFVerificationPreservesExactRevokedAcknowledgementReplay(t *testing.T) {
	_, a, _ := authFixture(t)
	provision(t, a)
	_, cookie := login(t, a)
	token, err := a.CSRFToken(t.Context(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := a.Logout(t.Context(), cookie, "csrf-replay-key")
	if err != nil || a.VerifyCSRF(cookie, token) != nil {
		t.Fatal(err)
	}
	replay, err := a.Logout(t.Context(), cookie, "csrf-replay-key")
	if err != nil || replay != ack {
		t.Fatal("CSRF prevented safe acknowledgement replay", err)
	}
}

func TestCSRFFailsClosedForInvalidInputKeyAndActivityWrites(t *testing.T) {
	s, a, _ := authFixture(t)
	provision(t, a)
	_, cookie := login(t, a)
	for _, invalid := range []string{"", strings.Repeat("!", 43), strings.Repeat("a", 44)} {
		if _, err := a.CSRFToken(t.Context(), invalid); !errors.Is(err, ErrAuthentication) {
			t.Fatal("invalid cookie obtained a CSRF token", err)
		}
		if err := a.VerifyCSRF(invalid, cookie); !errors.Is(err, ErrAuthentication) {
			t.Fatal("invalid cookie passed CSRF verification", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if token, err := a.CSRFToken(ctx, cookie); token != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled issuance returned a token", err)
	}
	execSQL(t, s.db, "CREATE TRIGGER fail_csrf_activity BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'write failure'); END")
	if token, err := a.CSRFToken(t.Context(), cookie); token != "" || err == nil {
		t.Fatal("failed session validation returned a CSRF token")
	}
	broken := *a
	broken.serverSecret = nil
	if _, err := broken.csrfToken(cookie); !errors.Is(err, ErrAuthState) {
		t.Fatal("missing server key silently derived a token")
	}
	if err := broken.VerifyCSRF(cookie, cookie); !errors.Is(err, ErrAuthState) {
		t.Fatal("missing server key silently verified a token")
	}
}
