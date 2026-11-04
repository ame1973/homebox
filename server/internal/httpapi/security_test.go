package httpapi

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/password"
	"homebox/internal/store"
)

func TestPublicOriginMustBeFixedCanonicalAndTransportAppropriate(t *testing.T) {
	for _, invalid := range []string{
		"", "/relative", "ftp://homebox.test", "https://user:password@homebox.test", "https://homebox.test/",
		"https://homebox.test?x=y", "https://homebox.test?", "https://homebox.test#", "https://homebox.test#fragment",
		"https://homebox.test:", "https://homebox.test:0", "https://homebox.test:65536", "https://homebox.test:port",
		"https://-bad.test", "https://bad-.test", "https://bad..test", "https://bad_.test", "https://homebox.test.",
		"https://私有.test", "https://Key.test", "https://[not-an-ip]", "https://[fe80::1%25eth0]", "http://homebox.test", "http://192.168.1.5:8080",
		"https://" + strings.Repeat("a", 254),
	} {
		t.Run(invalid, func(t *testing.T) {
			if _, err := newOriginPolicy(invalid, nil); err == nil {
				t.Fatal("invalid or nonlocal plaintext origin accepted")
			}
		})
	}
	for input, expected := range map[string]string{
		"https://HOMEBOX.test:443":  "https://homebox.test",
		"https://homebox.test:8443": "https://homebox.test:8443",
		"https://[2001:db8::1]":     "https://[2001:db8::1]",
		"http://localhost:80":       "http://localhost",
		"http://127.0.0.1:8080":     "http://127.0.0.1:8080",
		"http://[::1]:8080":         "http://[::1]:8080",
	} {
		policy, err := newOriginPolicy(input, nil)
		if err != nil || policy.origin != expected {
			t.Fatal("valid origin did not canonicalize correctly", input, err)
		}
	}
	for _, prefix := range []netip.Prefix{{}, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")} {
		if _, err := newOriginPolicy("https://homebox.test", []netip.Prefix{prefix}); err == nil {
			t.Fatal("wildcard or invalid proxy trust accepted")
		}
	}
	if _, err := newOriginPolicy("http://localhost", []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}); err == nil {
		t.Fatal("plaintext proxy deployment accepted")
	}
	if _, err := New(Config{Assets: fstest.MapFS{"index.html": {Data: []byte("app")}}, PublicOrigin: "https://homebox.test"}); err == nil {
		t.Fatal("HTTP API accepted a missing authentication service")
	}
}

func TestRequestOriginHostSchemeAndFetchMetadataAreNotInterchangeable(t *testing.T) {
	policy, err := newOriginPolicy("https://homebox.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		modify func(*http.Request)
	}{
		{"absent origin", func(r *http.Request) { r.Header.Del("Origin") }},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"empty origin", func(r *http.Request) { r.Header.Set("Origin", "") }},
		{"other host origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
		{"other port origin", func(r *http.Request) { r.Header.Set("Origin", "https://homebox.test:8443") }},
		{"insecure origin", func(r *http.Request) { r.Header.Set("Origin", "http://homebox.test") }},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", "https://homebox.test") }},
		{"joined origin", func(r *http.Request) { r.Header.Set("Origin", "https://homebox.test, https://evil.test") }},
		{"rebinding host", func(r *http.Request) { r.Host = "evil.test"; r.Header.Set("Origin", "https://evil.test") }},
		{"spoofed forwarded host", func(r *http.Request) { r.Host = "evil.test"; r.Header.Set("X-Forwarded-Host", "homebox.test") }},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"same site is not same origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"duplicate fetch metadata", func(r *http.Request) {
			r.Header.Add("Sec-Fetch-Site", "same-origin")
			r.Header.Add("Sec-Fetch-Site", "same-origin")
		}},
		{"spoofed proxy TLS", func(r *http.Request) {
			r.TLS = nil
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Forwarded", "proto=https;host=homebox.test")
		}},
		{"invalid peer", func(r *http.Request) { r.RemoteAddr = "untrusted" }},
		{"invalid peer IP", func(r *http.Request) { r.RemoteAddr = "hostname:1234" }},
		{"invalid Host", func(r *http.Request) { r.Host = "homebox.test/path" }},
		{"absolute wrong scheme", func(r *http.Request) { r.URL.Scheme = "http" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := apiRequest(http.MethodPost, "/api/v1/auth/login", "")
			tc.modify(r)
			if _, err := policy.check(r); !errors.Is(err, errRequestOrigin) {
				t.Fatal("request crossed the fixed-origin boundary", err)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := apiRequest(method, "/api/v1/auth/me", "")
		r.Header.Del("Origin")
		if _, err := policy.check(r); err != nil {
			t.Fatal("ordinary same-origin browser reads were rejected", err)
		}
	}
	r := apiRequest(http.MethodPost, "/api/v1/auth/login", "")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if peer, err := policy.check(r); err != nil || peer != netip.MustParseAddr("192.0.2.1") {
		t.Fatal("untrusted forwarding claims changed rate-limit identity", peer, err)
	}
}

func TestTrustedProxyTransportAndRightmostUntrustedClient(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
	policy, err := newOriginPolicy("https://homebox.test", prefixes)
	if err != nil {
		t.Fatal(err)
	}
	prefixes[0] = netip.MustParsePrefix("192.0.2.0/24")
	proxyRequest := func() *http.Request {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", "")
		r.TLS = nil
		r.RemoteAddr = "10.0.0.6:3210"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.2, 10.0.0.5")
		return r
	}
	if peer, err := policy.check(proxyRequest()); err != nil || peer != netip.MustParseAddr("198.51.100.2") {
		t.Fatal("proxy did not stop at the nearest untrusted hop or copied config was mutated", peer, err)
	}
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Forwarded-Proto") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") },
		func(r *http.Request) { r.Header.Add("X-Forwarded-Proto", "https") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https, http") },
		func(r *http.Request) { r.RemoteAddr = "192.0.2.1:3210" },
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", "not-an-ip") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", "0.0.0.0") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", "fe80::1%eth0") },
		func(r *http.Request) { r.Header.Add("X-Forwarded-For", "198.51.100.2") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", strings.Repeat("10.0.0.5,", 9)+"198.51.100.2") },
	} {
		r := proxyRequest()
		modify(r)
		if _, err := policy.check(r); err == nil {
			t.Fatal("invalid proxy evidence was accepted")
		}
	}
	r := proxyRequest()
	r.Header.Del("X-Forwarded-For")
	if peer, err := policy.check(r); err != nil || peer.String() != "10.0.0.6" {
		t.Fatal("proxy without a client chain did not retain its shared identity", err)
	}
}

func TestInsecureDevelopmentRequiresActualLoopback(t *testing.T) {
	policy, err := newOriginPolicy("http://localhost:8080", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/v1/auth/login", nil)
	r.RemoteAddr = "[::1]:1234"
	r.Header.Set("Origin", "http://localhost:8080")
	if _, err := policy.check(r); err != nil {
		t.Fatal("direct loopback development was rejected", err)
	}
	r.RemoteAddr = "192.168.1.5:1234"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if _, err := policy.check(r); err == nil {
		t.Fatal("LAN client was accepted as an insecure development peer")
	}
	r.RemoteAddr = "127.0.0.1:1234"
	r.TLS = &tls.ConnectionState{}
	if _, err := policy.check(r); err == nil {
		t.Fatal("HTTP origin was accepted over a different browser scheme")
	}
}

func TestRequestLimiterPerPeerGlobalExpiryCapacityAndConcurrency(t *testing.T) {
	now := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter()
	limiter.now = func() time.Time { return now }
	peer := netip.MustParseAddr("192.0.2.1")
	var wg sync.WaitGroup
	results := make(chan int, 50)
	for range 50 {
		wg.Go(func() { results <- limiter.allow(peer, true) })
	}
	wg.Wait()
	close(results)
	allowed := 0
	for retry := range results {
		if retry == 0 {
			allowed++
		} else if retry != 60 {
			t.Fatal("unexpected retry window")
		}
	}
	if allowed != 5 {
		t.Fatal("concurrent requests bypassed the per-peer credential bound")
	}
	now = now.Add(59*time.Second + time.Nanosecond)
	if retry := limiter.allow(peer, true); retry != 1 {
		t.Fatal("Retry-After was not rounded up to the expiry boundary")
	}
	now = now.Add(time.Second)
	if limiter.allow(peer, true) != 0 {
		t.Fatal("window did not expire")
	}
	for _, credential := range []bool{false, true} {
		limiter = newRequestLimiter()
		limiter.now = func() time.Time { return now }
		perPeer, global := 60, 600
		if credential {
			perPeer, global = 5, 30
		}
		for i := range global {
			address := netip.AddrFrom4([4]byte{192, 0, 2, byte(i/perPeer + 1)})
			if limiter.allow(address, credential) != 0 {
				t.Fatal("global bound rejected an admitted request")
			}
		}
		if limiter.allow(netip.MustParseAddr("198.51.100.1"), credential) != 60 {
			t.Fatal("rotating peer addresses bypassed the global limit")
		}
	}
	limiter = newRequestLimiter()
	limiter.now = func() time.Time { return now }
	for i := range 1050 {
		_ = limiter.allow(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1}), false)
	}
	if len(limiter.peers) != 1024 || limiter.allow(peer, false) != 60 {
		t.Fatal("rate limiter retained unbounded identities or evicted active limits")
	}
	if limiter.allow(netip.MustParseAddr("10.0.0.1"), true) != 0 {
		t.Fatal("capacity limit rejected an existing independent credential budget")
	}
	now = now.Add(time.Minute)
	if limiter.allow(peer, false) != 0 || len(limiter.peers) != 1 {
		t.Fatal("expired identities were not reclaimed")
	}
}

func TestAuthErrorMappingNeverExposesStorageOrCredentialDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"},
		{password.ErrEncoding, 401, "AUTHENTICATION_REQUIRED"},
		{store.ErrCSRF, 403, "CSRF_REJECTED"},
		{store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrPasswordMismatch, 403, "CREDENTIALS_REJECTED"},
		{store.ErrSetupClosed, 409, "SETUP_CLOSED"},
		{store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"},
		{store.ErrAuthInput, 422, "VALIDATION_ERROR"},
		{password.ErrPassword, 422, "VALIDATION_ERROR"},
		{store.ErrPasswordUnchanged, 422, "VALIDATION_ERROR"},
		{password.ErrBusy, 503, "SERVICE_UNAVAILABLE"},
		{errors.New("private /data/homebox.sqlite secret-credential"), 503, "SERVICE_UNAVAILABLE"},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "error-test-request")
		writeAuthError(w, tc.err, "error-test-request")
		expectError(t, w, tc.status, tc.code)
		if strings.Contains(w.Body.String(), "secret-credential") || strings.Contains(w.Body.String(), "/data/") {
			t.Fatal("internal error was returned to the client")
		}
		if tc.err == password.ErrBusy {
			if seconds, _ := strconv.Atoi(w.Header().Get("Retry-After")); seconds != 1 {
				t.Fatal("busy KDF did not request an explicit delayed retry")
			}
		}
	}
}
