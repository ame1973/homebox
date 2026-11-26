package httpapi

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

const systemVersionPath = "/api/v1/system/version"

type versionResponse struct {
	APIVersion                   string   `json:"api_version"`
	ProjectionVersion            int64    `json:"projection_version"`
	OfflineProjectionVersion     int64    `json:"offline_projection_version"`
	DatasetEpoch                 string   `json:"dataset_epoch"`
	ServerInstanceID             string   `json:"server_instance_id"`
	WebBuildID                   string   `json:"web_build_id"`
	WebVersion                   string   `json:"web_version"`
	SchemaVersion                int      `json:"schema_version"`
	MinSupportedClient           *string  `json:"min_supported_client"`
	MinWebBuildID                *string  `json:"min_web_build_id"`
	ModelIndexVersions           []string `json:"model_index_versions"`
	SupportedWriteClientVersions []string `json:"supported_write_client_versions"`
}

func versionHTTPHandler(t *testing.T, auth *store.Auth, files fstest.MapFS) http.Handler {
	t.Helper()
	files["index.html"] = &fstest.MapFile{Data: []byte("<!doctype html><main>App</main>")}
	handler, err := New(Config{Auth: auth, PublicOrigin: "https://homebox.test", Assets: files})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestSystemVersionHTTPUsesActualStateAndExactWritePolicy(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	handler := versionHTTPHandler(t, auth, webBuildFixture("0.1.0-dev"))
	response := respond(handler, domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", ""))
	var got versionResponse
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 12 ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("invalid version response: %d %s", response.Code, response.Body.String())
	}
	state, err := auth.ReadVersionState(t.Context(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != "1" || got.ProjectionVersion != state.ProjectionVersion ||
		got.OfflineProjectionVersion != state.ProjectionVersion || got.DatasetEpoch != state.DatasetEpoch ||
		got.ServerInstanceID != state.ServerInstanceID || got.SchemaVersion != state.SchemaVersion ||
		got.WebBuildID != "0123456789abcdef01234567" || got.WebVersion != "0.1.0-dev" ||
		!reflect.DeepEqual(got.SupportedWriteClientVersions, supportedWriteClientVersions()) {
		t.Fatalf("version did not use the actual sources: %+v", got)
	}
	for _, field := range []string{"min_supported_client", "min_web_build_id", "model_index_versions"} {
		if string(fields[field]) != "null" {
			t.Fatalf("unknown %s was presented as known: %s", field, fields[field])
		}
	}
}

func TestSystemVersionHTTPThroughVerifiedTLS(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, _ := testCredentials(t, auth, changedPassword)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	files := webBuildFixture("0.1.0-dev")
	files["index.html"] = &fstest.MapFile{Data: []byte("<!doctype html><main>App</main>")}
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: files})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+systemVersionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", origin)
	r.AddCookie(cookie)
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var got versionResponse
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&got) != nil ||
		got.WebBuildID != "0123456789abcdef01234567" || got.ModelIndexVersions != nil ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != "" ||
		response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("verified TLS version read failed: %d %+v", response.StatusCode, got)
	}
}

func TestSystemVersionHTTPRequiresSessionAndStrictRead(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	handler := versionHTTPHandler(t, auth, webBuildFixture("0.1.0-dev"))
	expectError(t, respond(handler, domainRequest(http.MethodGet, systemVersionPath, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, systemVersionPath, "", cookie, csrf, "version-method-key", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("version response advertised an unsupported method")
		}
	}
	for _, test := range []struct {
		header string
		status int
		code   string
	}{
		{"X-CSRF-Token", 403, "CSRF_REJECTED"},
		{"X-HomeBox-Client-Version", 426, "CLIENT_UPGRADE_REQUIRED"},
		{"Idempotency-Key", 400, "INVALID_IDEMPOTENCY_KEY"},
	} {
		r := domainRequest(http.MethodPost, systemVersionPath, "", cookie, csrf, "version-method-key", "")
		r.Header.Del(test.header)
		expectError(t, respond(handler, r), test.status, test.code)
	}
	for _, path := range []string{systemVersionPath + "?", systemVersionPath + "?unknown=x", "/api/v1/system/%76ersion"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "version-body-revocation"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
}

type beforeBuildOpen struct {
	fs.FS
	before func()
}

func (f beforeBuildOpen) Open(name string) (fs.File, error) {
	if name == "web-build.json" {
		f.before()
	}
	return f.FS.Open(name)
}

func TestSystemVersionHTTPRevalidatesSessionAfterBuildRead(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	files := webBuildFixture("0.1.0-dev")
	files["index.html"] = &fstest.MapFile{Data: []byte("<!doctype html><main>App</main>")}
	called := false
	assets := beforeBuildOpen{FS: files, before: func() {
		called = true
		if _, err := auth.Logout(t.Context(), cookie.Value, "version-build-revocation"); err != nil {
			t.Fatal(err)
		}
	}}
	handler, err := New(Config{Auth: auth, PublicOrigin: "https://homebox.test", Assets: assets})
	if err != nil {
		t.Fatal(err)
	}
	response := respond(handler, domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", ""))
	expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
	if !called {
		t.Fatal("build observation did not run before session revalidation")
	}
}

func TestSystemVersionHTTPDoesNotGuessMissingBuildOrAuthorizeIncompleteSessions(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	for name, damage := range map[string]func(fstest.MapFS){
		"missing metadata": func(files fstest.MapFS) { delete(files, "web-build.json") },
		"invalid metadata": func(files fstest.MapFS) { files["web-build.json"].Data = []byte("secret invalid metadata") },
		"mismatched descriptor": func(files fstest.MapFS) {
			files["pwa-version.json"].Data = []byte("{\"worker\":\"/pwa-worker-ffffffffffffffffffffffff.js\"}\n")
		},
		"missing worker": func(files fstest.MapFS) { delete(files, strings.TrimPrefix(testWebWorker, "/")) },
	} {
		t.Run(name, func(t *testing.T) {
			files := webBuildFixture("0.1.0-dev")
			handler := versionHTTPHandler(t, auth, files)
			damage(files)
			response := respond(handler, domainRequest(http.MethodGet, systemVersionPath, "", cookie, csrf, "", ""))
			expectError(t, response, 503, "SERVICE_UNAVAILABLE")
			if strings.Contains(response.Body.String(), "secret invalid metadata") || strings.Contains(response.Body.String(), testWebWorker) {
				t.Fatal("private build metadata was exposed in an error")
			}
		})
	}

	incomplete := newTestAuth(t)
	setupAccount(t, incomplete, false)
	incompleteCookie, incompleteCSRF := testCredentials(t, incomplete, initialPassword)
	incompleteHandler := versionHTTPHandler(t, incomplete, webBuildFixture("0.1.0-dev"))
	expectError(t, respond(incompleteHandler, domainRequest(http.MethodGet, systemVersionPath, "", incompleteCookie, incompleteCSRF, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}
