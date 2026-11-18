//go:build linux

package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const connectivityPath = "/api/v1/system/connectivity"

func TestConnectivityHTTPIsProtectedMinimalAndDoesNotRenewSession(t *testing.T) {
	db, auth, _, cookie, csrf, path := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout%285000%29"}
	raw, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	var conn *sql.Conn
	// Retain this extra SQLite descriptor until the owning Store is closed:
	// closing it early can release process-scoped SQLite locks.
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if conn != nil {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	})
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	conn, err = raw.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	seen := func() string {
		t.Helper()
		var value string
		if err := conn.QueryRowContext(t.Context(), "SELECT last_seen_at FROM sessions WHERE token_hash=?", hex.EncodeToString(hash[:])).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := seen()
	for range 5 {
		response := respond(handler, domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", ""))
		var body map[string]any
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body) != 1 || body["status"] != "ok" ||
			response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" || response.Header().Get("Set-Cookie") != "" ||
			response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("invalid connectivity result: %d %s", response.Code, response.Body.String())
		}
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost} {
		response := respond(handler, domainRequest(method, connectivityPath, "", cookie, csrf, "probe-method-key", ""))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatal("unsupported method did not fail safely", method, response.Code)
		}
	}
	if after := seen(); after != before {
		t.Fatal("HTTP connectivity probes extended session activity", before, after)
	}
}

func TestConnectivityHTTPAdmissionAndBodyTimeRevalidation(t *testing.T) {
	_, auth, _, cookie, csrf, _ := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	expectError(t, respond(handler, domainRequest(http.MethodGet, connectivityPath, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	noOrigin := domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")
	noOrigin.Header.Del("Origin")
	if response := respond(handler, noOrigin); response.Code != http.StatusOK {
		t.Fatal("same-origin browser GET without an Origin header was rejected", response.Code)
	}
	crossSite := domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	expectError(t, respond(handler, crossSite), 403, "ORIGIN_REJECTED")
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, connectivityPath, "", cookie, csrf, "probe-method-key", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("connectivity advertised an unsupported method")
		}
	}
	badWrite := domainRequest(http.MethodPost, connectivityPath, "", cookie, "", "probe-method-key", "")
	expectError(t, respond(handler, badWrite), 403, "CSRF_REJECTED")
	for _, path := range []string{connectivityPath + "?", connectivityPath + "?secret=x", "/api/v1/system/%63onnectivity"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "connectivity-body-revoke"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
}

func TestConnectivityHTTPUsesTheGeneralPeerRateLimit(t *testing.T) {
	_, auth, _, cookie, csrf, _ := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	for i := range 60 {
		response := respond(handler, domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", ""))
		if response.Code != http.StatusOK {
			t.Fatalf("probe %d unexpectedly failed: %d", i, response.Code)
		}
	}
	response := respond(handler, domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", ""))
	expectError(t, response, http.StatusTooManyRequests, "RATE_LIMITED")
	if response.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited connectivity probe omitted retry guidance")
	}
}

func TestConnectivityHTTPRequiresCompletedPasswordChange(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	handler := authHandler(t, auth)
	expectError(t, respond(handler, domainRequest(http.MethodGet, connectivityPath, "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}

func TestConnectivityHTTPThroughVerifiedTLS(t *testing.T) {
	_, auth, _, cookie, _, _ := recoveryHTTPFixture(t, false)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Fixture</main>")},
	}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+connectivityPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.AddCookie(cookie)
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&body) != nil || len(body) != 1 || body["status"] != "ok" ||
		response.Header.Get("ETag") != "" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("verified TLS connectivity probe failed", response.StatusCode, body)
	}
}
