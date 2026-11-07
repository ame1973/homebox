package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

// Each caller receives an independent private store, limiter, TLS socket and
// cookie jar. Credentials never enter diagnostic captures or request URLs.
func domainTLSFixture(t *testing.T, captureSession ...func(*store.Auth, string)) (func(method, path, body, key, revision string) *httptest.ResponseRecorder, func()) {
	t.Helper()
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: fstest.MapFS{"index.html": {Data: []byte("<main>App</main>")}}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client() // Trust only this server's generated certificate.
	client.Timeout = 5 * time.Second
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	csrf := ""
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet && path != "/api/v1/auth/login" {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Idempotency-Key", key)
			r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion)
		}
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || response.Header.Get("Strict-Transport-Security") == "" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Request-ID") == "" {
			t.Fatal("socket reply escaped its verified transport/private response boundary", err)
		}
		if !strings.HasPrefix(path, "/api/v1/auth/") && response.Header.Get("Set-Cookie") != "" {
			t.Fatal("domain reply changed its session cookie")
		}
		captured := httptest.NewRecorder()
		captured.Code = response.StatusCode
		for name, values := range response.Header {
			captured.Header()[name] = values
		}
		_, _ = captured.Body.Write(data)
		return captured
	}
	login := func() {
		t.Helper()
		response := call(http.MethodPost, "/api/v1/auth/login", fmt.Sprintf(`{"username":"管理員","password":%q}`, changedPassword), "", "")
		cookies := response.Result().Cookies()
		if response.Code != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatal("verified TLS login did not deliver a secure opaque session")
		}
		response = call(http.MethodGet, "/api/v1/auth/csrf", "", "", "")
		var token struct{ Token string }
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &token) != nil || token.Token == "" {
			t.Fatal("actual session could not obtain its in-memory CSRF proof")
		}
		csrf = token.Token
		for _, capture := range captureSession {
			capture(auth, cookies[0].Value)
		}
	}
	login()
	return call, login
}
