package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func fixture(t *testing.T) http.Handler {
	t.Helper()
	handler, err := New(Config{Auth: newTestAuth(t), PublicOrigin: "https://homebox.test", Assets: fstest.MapFS{
		"index.html":                 {Data: []byte("<!doctype html><title>HomeBox fixture</title><main>App shell</main>")},
		"assets/app-a1b2c3d4.js":     {Data: []byte("console.log('public asset')")},
		"assets/site-a1b2c3d4.css":   {Data: []byte("body { color: black }")},
		"favicon.svg":                {Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")},
		"manifest.webmanifest":       {Data: []byte(`{"name":"HomeBox"}`)},
		"assets/private.sqlite":      {Data: []byte("must not be served")},
		"assets/app-a1b2c3d4.js.map": {Data: []byte("must not be served")},
		"data/private.css":           {Data: []byte("must not be served")},
		"secrets/private.js":         {Data: []byte("must not be served")},
		"nested/private.html":        {Data: []byte("must not be served")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func request(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, "https://homebox.test"+target, nil))
	return response
}

func TestLivenessIsMinimalPublicAndNetworkOnly(t *testing.T) {
	handler := fixture(t)
	response := request(t, handler, http.MethodGet, "/api/v1/health/live")
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(body) != 1 || body["status"] != "ok" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected liveness response: %d %s", response.Code, response.Body.String())
	}
	if request(t, handler, http.MethodHead, "/api/v1/health/live").Body.Len() != 0 {
		t.Fatal("HEAD liveness returned a response body")
	}
}

func TestSPADeepLinksNeverBecomeDirectoryListings(t *testing.T) {
	handler := fixture(t)
	for _, target := range []string{"/", "/settings/system", "/settings/system/", "/settings/backup", "/l/location-id", "/u/unit-id", "/items/item-id", "/assets", "/assets/", "/photos?owner_type=item&owner_id=example", "/photos/attach?owner_type=item&owner_id=example", "/photos/edit?owner_type=item&owner_id=example&link_id=photo"} {
		t.Run(target, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "App shell") || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("deep link failed: %d %s", response.Code, response.Body.String())
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
				t.Fatal("shell response has the wrong content type")
			}
		})
	}
}

func TestPrivateNamespacesTraversalAndUnknownAssetsNeverFallBackToHTML(t *testing.T) {
	handler := fixture(t)
	for _, target := range []string{
		"/api", "/api/v1/items", "/api/v1/missing", "/auth/login", "/media", "/media?owner_type=item&owner_id=example", "/media/originals/photo.png",
		"/backups/backup.sqlite", "/exports/data.csv", "/worker/claim", "/data/private.css",
		"/secrets/private.js", "/dev/README.md", "/.git/config", "/.env", "/assets/../favicon.svg",
		"/assets/%2e%2e/favicon.svg", "/assets/%5cprivate.js", "/assets/%00private.js",
		"/assets/private.sqlite", "/assets/app-a1b2c3d4.js.map", "/nested/private.html",
		"/assets/missing.js", "//api/v1/items",
	} {
		t.Run(target, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target)
			expectedStatus, expectedCode := http.StatusNotFound, "NOT_FOUND"
			if strings.HasPrefix(target, "/api/v1/") {
				expectedStatus, expectedCode = http.StatusUnauthorized, "AUTHENTICATION_REQUIRED"
			}
			if response.Code != expectedStatus || strings.Contains(response.Body.String(), "App shell") || strings.Contains(response.Body.String(), "must not be served") {
				t.Fatalf("unsafe route response: %d %s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code      string         `json:"code"`
					RequestID string         `json:"request_id"`
					Details   map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != expectedCode || body.Error.RequestID != response.Header().Get("X-Request-ID") || body.Error.Details == nil {
				t.Fatal("error does not match the structured API envelope")
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("error response can be cached")
			}
		})
	}
}

func TestOnlyContentHashedPublicAssetsReceiveImmutableCaching(t *testing.T) {
	handler := fixture(t)
	for target, cache := range map[string]string{
		"/assets/app-a1b2c3d4.js":   "public, max-age=31536000, immutable",
		"/assets/site-a1b2c3d4.css": "public, max-age=31536000, immutable",
		"/favicon.svg":              "no-cache",
		"/manifest.webmanifest":     "no-cache",
	} {
		response := request(t, handler, http.MethodGet, target)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != cache {
			t.Fatalf("asset policy %s: %d, %q", target, response.Code, response.Header().Get("Cache-Control"))
		}
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app-a1b2c3d4.js", nil)
	req.Header.Set("Range", "bytes=0-6")
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusPartialContent || response.Body.String() != "console" {
		t.Fatalf("stream/range handling failed: %d %s", response.Code, response.Body.String())
	}
}

func TestNonReadMethodsAreRejectedWithAnAllowHeader(t *testing.T) {
	handler := fixture(t)
	for _, target := range []string{"/", "/settings/system", "/api/v1/health/live", "/assets/app-a1b2c3d4.js"} {
		response := request(t, handler, http.MethodPost, target)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" || !strings.Contains(response.Body.String(), "METHOD_NOT_ALLOWED") {
			t.Fatalf("unexpected method handling: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestSecurityHeadersAndUntrustedProxyHeaders(t *testing.T) {
	handler := fixture(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Request-ID", "attacker-controlled")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	for name, value := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
		if response.Header().Get(name) != value {
			t.Fatalf("missing header %s", name)
		}
	}
	if response.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("untrusted proxy header enabled HSTS")
	}
	if response.Header().Get("X-Request-ID") == "attacker-controlled" || len(response.Header().Get("X-Request-ID")) < 16 {
		t.Fatal("request identifier was accepted from untrusted input")
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'self';") || !strings.Contains(policy, "object-src 'none'") || strings.Contains(policy, "https:") {
		t.Fatal("CSP allows unexpected remote content")
	}
	if response.Header().Get("Permissions-Policy") != "camera=(self), microphone=(), geolocation=()" {
		t.Fatal("camera/privacy permission policy is missing")
	}
	req.TLS = &tls.ConnectionState{}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Fatal("trusted TLS did not enable HSTS")
	}
}

func TestFrontendMustBeBuiltAndRegular(t *testing.T) {
	for _, assets := range []fstest.MapFS{nil, {}, {"index.html": {Data: nil}}, {"index.html": {Mode: os.ModeDir}}} {
		if _, err := New(Config{Assets: assets}); err == nil {
			t.Fatal("invalid frontend build accepted")
		}
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("nil frontend accepted")
	}
}

func TestRootedFilesystemCannotServeAnEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	web := filepath.Join(directory, "web")
	if err := os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("app shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "secret.js"), []byte("private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret.js", filepath.Join(web, "escape.js")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(web)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	handler, err := New(Config{Assets: root.FS(), Auth: newTestAuth(t), PublicOrigin: "https://homebox.test"})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodGet, "/escape.js")
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "private data") {
		t.Fatal("public filesystem escaped through a symlink")
	}
}

func TestActualSocketSupportsGETAndHEADWithoutCachingAPIResponses(t *testing.T) {
	server := httptest.NewServer(fixture(t))
	defer server.Close()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/api/v1/health/live", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("socket request failed: %v, %d", err, response.StatusCode)
		}
		if method == http.MethodHead && len(body) != 0 {
			t.Fatal("HEAD sent a body on the wire")
		}
	}
}
