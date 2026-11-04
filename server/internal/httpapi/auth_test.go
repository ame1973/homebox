package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/password"
	"homebox/internal/store"
)

const initialPassword = "initial HTTP fixture password"
const changedPassword = "different HTTP fixture password"

func newTestAuth(t *testing.T) *store.Auth {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), store.Config{Path: filepath.Join(directory, "homebox.sqlite"), Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	auth, err := db.NewAuth(store.AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func authHandler(t *testing.T, auth *store.Auth) http.Handler {
	t.Helper()
	handler, err := New(Config{Auth: auth, PublicOrigin: "https://homebox.test", Assets: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Actual build fixture</main>")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func setupAccount(t *testing.T, auth *store.Auth, complete bool) {
	t.Helper()
	bootstrap, _, err := auth.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.Setup(t.Context(), bootstrap, "管理員", initialPassword); err != nil {
		t.Fatal(err)
	}
	if complete {
		_, token, err := auth.Login(t.Context(), "管理員", initialPassword)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = auth.ChangePassword(t.Context(), token, "fixture-password-key", initialPassword, changedPassword); err != nil {
			t.Fatal(err)
		}
	}
}

func testCredentials(t *testing.T, auth *store.Auth, secret string) (*http.Cookie, string) {
	t.Helper()
	_, token, err := auth.Login(t.Context(), "管理員", secret)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := auth.CSRFToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: token}, csrf
}

func apiRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://homebox.test"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://homebox.test")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func writeRequest(path, body string, cookie *http.Cookie, csrf, key string) *http.Request {
	r := apiRequest(http.MethodPost, path, body)
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion)
	r.Header.Set("Idempotency-Key", key)
	return r
}

func respond(handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	return response
}

func expectError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			RequestID string         `json:"request_id"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
	}
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error.Code != code || body.Error.Message == "" || body.Error.RequestID != response.Header().Get("X-Request-ID") || body.Error.Details == nil {
		t.Fatalf("unexpected error response: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Set-Cookie") != "" {
		t.Fatal("rejected request cached data or changed the session cookie")
	}
}

func TestAllAuthenticationRoutesThroughRealTLSAndCookieJar(t *testing.T) {
	auth := newTestAuth(t)
	bootstrap, _, err := auth.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: fstest.MapFS{"index.html": {Data: []byte("<main>App</main>")}}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Timeout = 5 * time.Second
	call := func(method, path string, value any, csrf, key string) (*http.Response, []byte) {
		t.Helper()
		var body io.Reader
		if value != nil {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		if value != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Idempotency-Key", key)
			r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion+"-dev")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Strict-Transport-Security") == "" {
			t.Fatal("TLS/no-store boundary failed", err)
		}
		return response, data
	}
	response, body := call(http.MethodPost, "/api/v1/auth/setup", map[string]string{"bootstrap_token": bootstrap, "username": "管理員", "password": initialPassword}, "", "")
	if response.StatusCode != http.StatusOK || response.Header.Get("Set-Cookie") != "" || bytes.Contains(body, []byte(bootstrap)) {
		t.Fatalf("setup failed or leaked a credential: %d %s", response.StatusCode, body)
	}
	var admin store.Admin
	if json.Unmarshal(body, &admin) != nil || !admin.MustChangePassword || admin.Revision != 1 || admin.Username != "管理員" || response.Header.Get("ETag") != `"1"` {
		t.Fatal("setup metadata does not match the contract")
	}
	response, body = call(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "管理員", "password": initialPassword}, "", "")
	cookies := response.Cookies()
	if response.StatusCode != http.StatusOK || len(cookies) != 1 {
		t.Fatalf("login failed: %d %s", response.StatusCode, body)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || len(cookie.Value) != 43 || cookie.Expires.IsZero() || bytes.Contains(body, []byte(cookie.Value)) {
		t.Fatal("opaque session cookie attributes or response secrecy are invalid")
	}
	response, body = call(http.MethodGet, "/api/v1/auth/me", nil, "", "")
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &admin) != nil || !admin.MustChangePassword {
		t.Fatal("secure cookie jar did not authenticate the current account")
	}
	response, body = call(http.MethodGet, "/api/v1/auth/csrf", nil, "", "")
	var csrf struct {
		Token string `json:"token"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &csrf) != nil || len(csrf.Token) != 43 || csrf.Token == cookie.Value {
		t.Fatal("CSRF response was not a separate active-session token")
	}
	response, body = call(http.MethodGet, "/api/v1/items", nil, "", "")
	if response.StatusCode != http.StatusForbidden || !bytes.Contains(body, []byte("PASSWORD_CHANGE_REQUIRED")) {
		t.Fatal("mandatory password change did not gate private domain access")
	}
	response, body = call(http.MethodPost, "/api/v1/auth/password", map[string]string{"old_password": initialPassword, "new_password": changedPassword}, csrf.Token, "network-password-key")
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 || response.Cookies()[0].MaxAge != -1 || response.Header.Get("ETag") != `"2"` {
		t.Fatalf("password change did not clear the secure cookie: %d %s", response.StatusCode, body)
	}
	response, _ = call(http.MethodGet, "/api/v1/auth/me", nil, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("password change left a usable browser session")
	}
	response, body = call(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "管理員", "password": changedPassword}, "", "")
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &admin) != nil || admin.MustChangePassword || admin.Revision != 2 || response.Cookies()[0].Value == cookie.Value {
		t.Fatal("fresh login did not rotate the session or finish mandatory change")
	}
	freshCookie := response.Cookies()[0]
	_, body = call(http.MethodGet, "/api/v1/auth/csrf", nil, "", "")
	if json.Unmarshal(body, &csrf) != nil {
		t.Fatal("fresh CSRF response is invalid")
	}
	response, body = call(http.MethodPost, "/api/v1/auth/reauth", map[string]string{"username": "管理員", "password": changedPassword}, csrf.Token, "network-reauth-key")
	var capability struct {
		store.ReauthGrant
		Token string `json:"reauth_token"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &capability) != nil || len(capability.Token) != 43 || capability.Token == csrf.Token || capability.Token == freshCookie.Value || capability.Admin != admin || !capability.ExpiresAt.After(time.Now()) {
		t.Fatalf("reauthentication contract failed: %d %s", response.StatusCode, body)
	}
	_, replay, err := auth.Reauthenticate(t.Context(), freshCookie.Value, "network-reauth-key", "管理員", changedPassword)
	if err != nil || replay != capability.Token {
		t.Fatal("HTTP capability did not match the frozen transactional grant", err)
	}
	response, body = call(http.MethodPost, "/api/v1/auth/logout", nil, csrf.Token, "network-logout-key")
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"ok":true`)) || response.Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not acknowledge and clear the cookie")
	}
	response, _ = call(http.MethodGet, "/api/v1/auth/csrf", nil, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("cookie jar remained authenticated after logout")
	}
}

func TestHTTPRevokedCookieReplaysOnlyExactAcknowledgements(t *testing.T) {
	for _, action := range []string{"logout", "password"} {
		t.Run(action, func(t *testing.T) {
			auth := newTestAuth(t)
			setupAccount(t, auth, false)
			handler := authHandler(t, auth)
			cookie, csrf := testCredentials(t, auth, initialPassword)
			other, _ := testCredentials(t, auth, initialPassword)
			path, body := "/api/v1/auth/"+action, ""
			if action == "password" {
				body = `{"old_password":"` + initialPassword + `","new_password":"` + changedPassword + `"}`
			}
			first := respond(handler, writeRequest(path, body, cookie, csrf, "replay-action-key"))
			if first.Code != http.StatusOK {
				t.Fatal(first.Body.String())
			}
			replay := respond(handler, writeRequest(path, body, cookie, csrf, "replay-action-key"))
			if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() || replay.Result().Cookies()[0].MaxAge != -1 {
				t.Fatal("exact revoked-cookie retry did not return only the frozen acknowledgement")
			}
			for _, target := range []string{"/api/v1/auth/me", "/api/v1/auth/csrf", "/api/v1/items"} {
				r := apiRequest(http.MethodGet, target, "")
				r.AddCookie(cookie)
				expectError(t, respond(handler, r), http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
			}
			expectError(t, respond(handler, writeRequest(path, body, cookie, csrf, "different-action-key")), http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
			if action == "password" {
				if _, err := auth.Authenticate(t.Context(), other.Value); !errors.Is(err, store.ErrAuthentication) {
					t.Fatal("password change left another session active")
				}
				changed := `{"old_password":"` + initialPassword + `","new_password":"a third different password"}`
				expectError(t, respond(handler, writeRequest(path, changed, cookie, csrf, "replay-action-key")), http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			} else if _, err := auth.Authenticate(t.Context(), other.Value); err != nil {
				t.Fatal("logout revoked an unrelated session", err)
			}
		})
	}
}

func TestHTTPWriteHeadersCannotBeMissingDuplicatedOrSubstituted(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	other, otherCSRF := testCredentials(t, auth, changedPassword)
	cases := []struct {
		name   string
		modify func(*http.Request)
		status int
		code   string
	}{
		{"missing cookie", func(r *http.Request) { r.Header.Del("Cookie") }, 401, "AUTHENTICATION_REQUIRED"},
		{"duplicate cookie", func(r *http.Request) { r.AddCookie(cookie) }, 401, "AUTHENTICATION_REQUIRED"},
		{"other cookie", func(r *http.Request) { r.Header.Del("Cookie"); r.AddCookie(other) }, 403, "CSRF_REJECTED"},
		{"invalid cookie", func(r *http.Request) { r.Header.Set("Cookie", sessionCookieName+"=invalid") }, 401, "AUTHENTICATION_REQUIRED"},
		{"missing csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
		{"duplicate csrf", func(r *http.Request) { r.Header.Add("X-CSRF-Token", csrf) }, 403, "CSRF_REJECTED"},
		{"other csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", otherCSRF) }, 403, "CSRF_REJECTED"},
		{"cookie as csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", cookie.Value) }, 403, "CSRF_REJECTED"},
		{"missing version", func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"duplicate version", func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"old version", func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.0.9") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"future version", func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.2.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"spec version", func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.9.3") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"missing key", func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"duplicate key", func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-valid-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"short key", func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"long key", func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.Repeat("k", 129)) }, 400, "INVALID_IDEMPOTENCY_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := writeRequest("/api/v1/auth/password", `{"old_password":"`+changedPassword+`","new_password":"never committed replacement"}`, cookie, csrf, "header-test-key")
			tc.modify(r)
			expectError(t, respond(authHandler(t, auth), r), tc.status, tc.code)
		})
	}
	if _, err := auth.Authenticate(t.Context(), cookie.Value); err != nil {
		t.Fatal("rejected requests mutated the session", err)
	}
	for _, version := range []string{supportedClientVersion, supportedClientVersion + "-dev"} {
		r := writeRequest("/api/v1/items", "", cookie, csrf, "unsupported-route-key")
		r.Header.Set("X-HomeBox-Client-Version", version)
		expectError(t, respond(authHandler(t, auth), r), http.StatusNotFound, "NOT_FOUND")
	}
}

func TestHTTPMandatoryChangeReauthProofAndUnknownRoutes(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	handler := authHandler(t, auth)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	r := writeRequest("/api/v1/auth/reauth", `{"username":"管理員","password":"`+initialPassword+`"}`, cookie, csrf, "mandatory-reauth-key")
	expectError(t, respond(handler, r), http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED")
	for _, path := range []string{"/api/v1/items", "/api/v1/media/private-id/content", "/api/v1/sync/bootstrap", "/api/v1/unknown"} {
		r := apiRequest(http.MethodGet, path, "")
		r.AddCookie(cookie)
		expectError(t, respond(handler, r), http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED")
	}
	for _, path := range []string{"/api/v1/worker/claim", "/api/v1/worker", "/api/v1/auth/reset-password"} {
		r := apiRequest(http.MethodGet, path, "")
		r.AddCookie(cookie)
		status, code := http.StatusNotFound, "NOT_FOUND"
		if strings.HasPrefix(path, "/api/v1/auth/") {
			status, code = http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED"
		}
		expectError(t, respond(handler, r), status, code)
	}
	if _, err := auth.ChangePassword(t.Context(), cookie.Value, "complete-http-key", initialPassword, changedPassword); err != nil {
		t.Fatal(err)
	}
	cookie, csrf = testCredentials(t, auth, changedPassword)
	r = writeRequest("/api/v1/auth/reauth", `{"username":"管理員","password":"wrong password"}`, cookie, csrf, "wrong-reauth-key")
	expectError(t, respond(handler, r), http.StatusForbidden, "CREDENTIALS_REJECTED")
	if _, err := auth.Authenticate(t.Context(), cookie.Value); err != nil {
		t.Fatal("failed reauthentication destroyed the ordinary session", err)
	}
}

func TestHTTPAuthMethodsExactPathsAndAnonymousValidation(t *testing.T) {
	auth := newTestAuth(t)
	handler := authHandler(t, auth)
	for _, path := range []string{"setup", "login", "logout", "password", "reauth", "me", "csrf"} {
		method := http.MethodGet
		if path == "me" || path == "csrf" {
			method = http.MethodPost
		}
		response := respond(handler, apiRequest(method, "/api/v1/auth/"+path, ""))
		expectError(t, response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != authMethod("/api/v1/auth/"+path) {
			t.Fatal("method rejection omitted the route-specific Allow header")
		}
	}
	for _, path := range []string{"/api/v1/auth/login?password=must-not-be-used", "/api/v1/auth/login?", "/api/v1/auth/%6cogin"} {
		expectError(t, respond(authHandler(t, auth), apiRequest(http.MethodPost, path, `{"username":"管理員","password":"unused"}`)), http.StatusBadRequest, "INVALID_AUTH_URL")
	}
	for _, body := range []string{`{"username":"","password":"x"}`, `{"username":"a","password":""}`, `{"username":"` + strings.Repeat("a", 65) + `","password":"x"}`} {
		expectError(t, respond(authHandler(t, auth), apiRequest(http.MethodPost, "/api/v1/auth/login", body)), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	}
	setup := `{"bootstrap_token":"short","username":"管理員","password":"` + initialPassword + `"}`
	expectError(t, respond(handler, apiRequest(http.MethodPost, "/api/v1/auth/setup", setup)), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	setupAccount(t, auth, false)
	expectError(t, respond(handler, apiRequest(http.MethodPost, "/api/v1/auth/setup", `{"bootstrap_token":"`+strings.Repeat("a", 43)+`","username":"another","password":"`+initialPassword+`"}`)), http.StatusConflict, "SETUP_CLOSED")
}

func TestHTTPLoginAndAdministratorRateLimitsIgnoreUntrustedForwardingClaims(t *testing.T) {
	auth := newTestAuth(t)
	handler := authHandler(t, auth)
	for i := range 6 {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"missing","password":"wrong password"}`)
		r.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1))
		response := respond(handler, r)
		if i < 5 {
			expectError(t, response, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
		} else {
			expectError(t, response, http.StatusTooManyRequests, "RATE_LIMITED")
			if seconds, err := strconv.Atoi(response.Header().Get("Retry-After")); err != nil || seconds < 1 || seconds > 60 {
				t.Fatal("rate rejection omitted a bounded Retry-After")
			}
		}
	}
	handler = authHandler(t, auth)
	for i := range 61 {
		response := respond(handler, apiRequest(http.MethodGet, "/api/v1/auth/me", ""))
		if i < 60 {
			expectError(t, response, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
		} else {
			expectError(t, response, http.StatusTooManyRequests, "RATE_LIMITED")
		}
	}
}
