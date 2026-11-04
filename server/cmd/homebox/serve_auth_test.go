package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func startServe(t *testing.T, args ...string) (map[string]string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	output := make(startupOutput, 1)
	result := make(chan error, 1)
	go func() { result <- serve(ctx, args, output, io.Discard) }()
	var ready map[string]string
	select {
	case data := <-output:
		if err := json.Unmarshal(data, &ready); err != nil {
			cancel()
			t.Fatal(err)
		}
	case err := <-result:
		cancel()
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not report its listener")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return ready, stop
}

func testCertificateFiles(t *testing.T, directory string) (certificate, key string, roots *x509.CertPool) {
	t.Helper()
	// Reuse Go's loopback test certificate, trusting its root explicitly instead of
	// disabling certificate verification in the real CLI listener test.
	source := httptest.NewTLSServer(http.NotFoundHandler())
	pair := source.TLS.Certificates[0]
	source.Close()
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, key = filepath.Join(directory, "test-cert.pem"), filepath.Join(directory, "test-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	if err := os.WriteFile(certificate, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots = x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("test root certificate was invalid")
	}
	return certificate, key, roots
}

func TestServeDirectTLSAuthenticatesUsingVerifiedCertificateAndSecureCookie(t *testing.T) {
	database, web := serverFixture(t)
	directory := filepath.Dir(database)
	bootstrapFile := filepath.Join(directory, "setup.token")
	if _, err := invoke(t, "auth", "bootstrap", "--database", database, "--output", bootstrapFile); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(bootstrapFile)
	if err != nil {
		t.Fatal(err)
	}
	cert, key, roots := testCertificateFiles(t, directory)
	ready, stop := startServe(t, "--listen", "127.0.0.1:0", "--database", database, "--web-dir", web, "--tls-cert", cert, "--tls-key", key)
	origin := ready["public_origin"]
	if origin != "https://"+ready["listening"] {
		t.Fatal("direct TLS did not derive a fixed HTTPS browser origin")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	call := func(path, body, requestOrigin string, cookie *http.Cookie) (*http.Response, []byte) {
		t.Helper()
		method := http.MethodGet
		if body != "" {
			method = http.MethodPost
		}
		r, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", requestOrigin)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.TLS == nil || response.TLS.Version < tls.VersionTLS12 || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Strict-Transport-Security") == "" {
			t.Fatal("real TLS transport or private response policy failed", err)
		}
		return response, data
	}
	setup := `{"bootstrap_token":"` + strings.TrimSpace(string(token)) + `","username":"owner","password":"verified CLI HTTP password"}`
	response, body := call("/api/v1/auth/setup", setup, origin, nil)
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), strings.TrimSpace(string(token))) {
		t.Fatal("CLI TLS setup failed or exposed the bootstrap token")
	}
	response, body = call("/api/v1/auth/login", `{"username":"owner","password":"verified CLI HTTP password"}`, origin, nil)
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 || !response.Cookies()[0].Secure || !response.Cookies()[0].HttpOnly {
		t.Fatalf("CLI TLS login failed: %d %s", response.StatusCode, body)
	}
	cookie := response.Cookies()[0]
	response, _ = call("/api/v1/auth/me", "", origin, cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatal("CLI HTTP wiring did not authenticate the issued cookie")
	}
	response, body = call("/api/v1/auth/password", `{"old_password":"verified CLI HTTP password","new_password":"uncommitted password change"}`, "https://evil.test", cookie)
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "ORIGIN_REJECTED") || response.Header.Get("Set-Cookie") != "" {
		t.Fatal("cross-origin CLI listener request changed authentication state")
	}
	response, _ = call("/api/v1/auth/me", "", origin, cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatal("rejected cross-origin request revoked the session")
	}
	stop()
	if _, err := invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatal("TLS shutdown retained database ownership", err)
	}
}

func TestServeRejectsUnsafeTransportAndProxyConfiguration(t *testing.T) {
	database, web := serverFixture(t)
	cert, key, _ := testCertificateFiles(t, filepath.Dir(database))
	for _, extra := range [][]string{
		{"--tls-cert", cert}, {"--tls-key", key},
		{"--tls-cert", cert, "--tls-key", filepath.Join(web, "missing")},
		{"--tls-cert", cert, "--tls-key", key, "--public-origin", "http://127.0.0.1"},
		{"--public-origin", "https://homebox.test"},
		{"--public-origin", "http://192.168.1.5"},
		{"--listen", "0.0.0.0:0"},
		{"--listen", "0.0.0.0:0", "--public-origin", "http://127.0.0.1"},
		{"--public-origin", "https://homebox.test", "--trusted-proxy", "0.0.0.0/0"},
		{"--public-origin", "https://homebox.test", "--trusted-proxy", "not-a-prefix"},
		{"--public-origin", "http://127.0.0.1", "--trusted-proxy", "127.0.0.1/32"},
		{"--public-origin", "https://homebox.test/path", "--trusted-proxy", "127.0.0.1/32"},
	} {
		args := append([]string{"--listen", "127.0.0.1:0", "--database", database, "--web-dir", web}, extra...)
		if err := serve(t.Context(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("unsafe configuration was accepted: %v", extra)
		}
	}
	if _, err := invoke(t, "db", "check", "--database", database); err != nil {
		t.Fatal("configuration failure leaked database ownership", err)
	}
}

func TestServeAcceptsForwardingOnlyFromExplicitTrustedProxyPrefix(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		t.Run(map[bool]string{true: "trusted", false: "untrusted"}[trusted], func(t *testing.T) {
			database, web := serverFixture(t)
			prefix := "127.0.0.1/32"
			if !trusted {
				prefix = "192.0.2.1/32"
			}
			ready, _ := startServe(t, "--listen", "127.0.0.1:0", "--database", database, "--web-dir", web,
				"--public-origin", "https://homebox.test", "--trusted-proxy", prefix)
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ready["listening"]+"/api/v1/auth/me", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Host = "homebox.test"
			r.Header.Set("Origin", "https://homebox.test")
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-For", "198.51.100.2")
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			status, code := http.StatusUnauthorized, "AUTHENTICATION_REQUIRED"
			if !trusted {
				status, code = http.StatusForbidden, "ORIGIN_REJECTED"
			}
			if err != nil || response.StatusCode != status || !strings.Contains(string(data), code) {
				t.Fatalf("proxy trust boundary failed: %d %s %v", response.StatusCode, data, err)
			}
			if trusted != (response.Header.Get("Strict-Transport-Security") != "") {
				t.Fatal("untrusted forwarding affected HSTS")
			}
		})
	}
}
