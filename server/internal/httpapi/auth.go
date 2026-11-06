package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homebox/internal/password"
	"homebox/internal/store"
)

type authAPI struct {
	auth      *store.Auth
	locations *store.Locations
	taxonomy  *store.Taxonomy
	catalog   *store.Catalog
	inventory *store.Inventory
	origin    originPolicy
	limiter   *requestLimiter
}

func authMethod(path string) string {
	switch path {
	case "/api/v1/auth/setup", "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/password", "/api/v1/auth/reauth":
		return http.MethodPost
	case "/api/v1/auth/me", "/api/v1/auth/csrf":
		return http.MethodGet
	default:
		return ""
	}
}

func (a *authAPI) serve(w http.ResponseWriter, r *http.Request, requestID string) {
	method := authMethod(r.URL.Path)
	if method != "" && r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return
	}
	peer, err := a.origin.check(r)
	if err != nil {
		writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "Use the configured same-origin application over its trusted transport.", requestID)
		return
	}
	credential := method == http.MethodPost && r.URL.Path != "/api/v1/auth/logout"
	if retry := a.limiter.allow(peer, credential); retry != 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests. Wait before trying again.", requestID)
		return
	}
	if method == "" {
		if a.serveLocations(w, r, requestID) || a.serveTaxonomy(w, r, requestID) || a.serveHoldings(w, r, requestID) || a.serveCatalogLinks(w, r, requestID) || a.serveCatalog(w, r, requestID) {
			return
		}
		// Worker credentials are a separate boundary, never an administrator session.
		if r.URL.Path != "/api/v1/worker" && !strings.HasPrefix(r.URL.Path, "/api/v1/worker/") {
			if !a.requireAdmin(w, r, requestID) {
				return
			}
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", "The API route does not exist.", requestID)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		writeError(w, http.StatusBadRequest, "INVALID_AUTH_URL", "Authentication uses exact paths and never accepts URL parameters.", requestID)
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/setup":
		values, ok := readAuthJSON(w, r, requestID, "bootstrap_token", "username", "password")
		if !ok || !validAuthFields(w, requestID, values, true) {
			return
		}
		admin, err := a.auth.Setup(r.Context(), values["bootstrap_token"], values["username"], values["password"])
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		writeAdmin(w, admin)
	case "/api/v1/auth/login":
		values, ok := readAuthJSON(w, r, requestID, "username", "password")
		if !ok || !validAuthFields(w, requestID, values, false) {
			return
		}
		session, token, err := a.auth.Login(r.Context(), values["username"], values["password"])
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		a.setSessionCookie(w, token, session.ExpiresAt)
		writeAdmin(w, session.Admin)
	case "/api/v1/auth/me", "/api/v1/auth/csrf":
		token, err := sessionToken(r)
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		if r.URL.Path == "/api/v1/auth/csrf" {
			value, err := a.auth.CSRFToken(r.Context(), token)
			if err != nil {
				writeAuthError(w, err, requestID)
				return
			}
			writeJSON(w, map[string]string{"token": value})
			return
		}
		session, err := a.auth.Authenticate(r.Context(), token)
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		writeAdmin(w, session.Admin)
	default:
		a.mutate(w, r, requestID)
	}
}

func (a *authAPI) mutate(w http.ResponseWriter, r *http.Request, requestID string) {
	token, key, ok := a.writeCredentials(w, r, requestID)
	if !ok {
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/logout":
		body, ok := readAuthBody(w, r, requestID)
		if !ok {
			return
		}
		if len(body) != 0 || len(r.Header.Values("Content-Encoding")) != 0 {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "Logout does not accept a request body.", requestID)
			return
		}
		ack, err := a.auth.Logout(r.Context(), token, key)
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		a.clearSessionCookie(w)
		writeJSON(w, ack)
	case "/api/v1/auth/password":
		values, ok := readAuthJSON(w, r, requestID, "old_password", "new_password")
		if !ok {
			return
		}
		ack, err := a.auth.ChangePassword(r.Context(), token, key, values["old_password"], values["new_password"])
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		a.clearSessionCookie(w)
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(ack.Revision, 10)))
		writeJSON(w, ack)
	case "/api/v1/auth/reauth":
		values, ok := readAuthJSON(w, r, requestID, "username", "password")
		if !ok || !validAuthFields(w, requestID, values, false) {
			return
		}
		grant, raw, err := a.auth.Reauthenticate(r.Context(), token, key, values["username"], values["password"])
		if err != nil {
			writeAuthError(w, err, requestID)
			return
		}
		writeJSON(w, struct {
			store.ReauthGrant
			Token string `json:"reauth_token"`
		}{grant, raw})
	}
}

func validAuthFields(w http.ResponseWriter, requestID string, values map[string]string, setup bool) bool {
	minimum := 1
	if setup {
		minimum = 12
	}
	if !boundedString(values["username"], 1, 64) || !boundedString(values["password"], minimum, 256) || setup && !boundedString(values["bootstrap_token"], 32, 256) {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Authentication fields are outside their allowed lengths.", requestID)
		return false
	}
	return true
}

func sessionToken(r *http.Request) (string, error) {
	cookies := r.CookiesNamed(sessionCookieName)
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return "", store.ErrAuthentication
	}
	return cookies[0].Value, nil
}

func (a *authAPI) writeCredentials(w http.ResponseWriter, r *http.Request, requestID string) (string, string, bool) {
	token, err := sessionToken(r)
	if err != nil {
		writeAuthError(w, err, requestID)
		return "", "", false
	}
	csrf := r.Header.Values("X-CSRF-Token")
	if len(csrf) != 1 {
		writeAuthError(w, store.ErrCSRF, requestID)
		return "", "", false
	}
	if err = a.auth.VerifyCSRF(token, csrf[0]); err != nil {
		writeAuthError(w, err, requestID)
		return "", "", false
	}
	versions := r.Header.Values("X-HomeBox-Client-Version")
	if len(versions) != 1 || versions[0] != supportedClientVersion && versions[0] != supportedClientVersion+"-dev" {
		writeError(w, http.StatusUpgradeRequired, "CLIENT_UPGRADE_REQUIRED", "Reload the application before sending this write.", requestID)
		return "", "", false
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !boundedString(keys[0], 8, 128) {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Send one Idempotency-Key containing 8 to 128 characters.", requestID)
		return "", "", false
	}
	return token, keys[0], true
}

// Private API paths share this gate. Each mutation must still revalidate its
// authoritative session and business/concurrency invariants in its transaction.
func (a *authAPI) requireAdmin(w http.ResponseWriter, r *http.Request, requestID string) bool {
	token, err := sessionToken(r)
	if err == nil && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		var ok bool
		token, _, ok = a.writeCredentials(w, r, requestID)
		if !ok {
			return false
		}
	}
	if err != nil {
		writeAuthError(w, err, requestID)
		return false
	}
	session, err := a.auth.Authenticate(r.Context(), token)
	if err == nil && session.Admin.MustChangePassword {
		err = store.ErrPasswordChangeRequired
	}
	if err != nil {
		writeAuthError(w, err, requestID)
		return false
	}
	return true
}

func (a *authAPI) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Expires: expires,
		Secure: a.origin.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (a *authAPI) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		Secure: a.origin.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func writeAdmin(w http.ResponseWriter, admin store.Admin) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(admin.Revision, 10)))
	writeJSON(w, admin)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func writeAuthError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Authentication is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, password.ErrEncoding):
		status, code, message = http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Credentials are invalid or expired. Sign in again."
	case errors.Is(err, store.ErrCSRF):
		status, code, message = http.StatusForbidden, "CSRF_REJECTED", "Refresh the session request token before trying again."
	case errors.Is(err, store.ErrPasswordChangeRequired):
		status, code, message = http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "Change the temporary password before accessing private data."
	case errors.Is(err, store.ErrPasswordMismatch):
		status, code, message = http.StatusForbidden, "CREDENTIALS_REJECTED", "The current credentials could not be verified."
	case errors.Is(err, store.ErrSetupClosed):
		status, code, message = http.StatusConflict, "SETUP_CLOSED", "Initial administrator setup has already completed."
	case errors.Is(err, store.ErrIdempotencyConflict):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_CONFLICT", "This request key was already used for a different action or payload."
	case errors.Is(err, store.ErrAuthInput), errors.Is(err, password.ErrPassword), errors.Is(err, store.ErrPasswordUnchanged):
		status, code, message = http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Authentication fields are outside their allowed bounds or reuse the current password."
	case errors.Is(err, password.ErrBusy):
		w.Header().Set("Retry-After", "1")
	}
	writeError(w, status, code, message, requestID)
}
