package store

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

var ErrCSRF = errors.New("invalid session-bound CSRF token")

// CSRFToken returns a server-keyed token only after validating an active session.
// It is stable within that session and must remain in browser memory, not storage.
func (a *Auth) CSRFToken(ctx context.Context, sessionToken string) (string, error) {
	if _, err := a.Authenticate(ctx, sessionToken); err != nil {
		return "", err
	}
	return a.csrfToken(sessionToken)
}

// VerifyCSRF checks only the request MAC, not session authorization. The mutation
// must still validate its session in its transaction. Keeping these checks separate
// permits exact logout/password acknowledgement replay without authorizing revoked sessions.
func (a *Auth) VerifyCSRF(sessionToken, requestToken string) error {
	expected, err := a.csrfToken(sessionToken)
	if err != nil {
		return err
	}
	if !validAuthToken(requestToken) || subtle.ConstantTimeCompare([]byte(expected), []byte(requestToken)) != 1 {
		return ErrCSRF
	}
	return nil
}

func (a *Auth) csrfToken(sessionToken string) (string, error) {
	if len(a.serverSecret) != 32 {
		return "", ErrAuthState
	}
	if !validAuthToken(sessionToken) {
		return "", ErrAuthentication
	}
	return base64.RawURLEncoding.EncodeToString(a.authMAC(sessionToken, "csrf-token-v1", nil)), nil
}
