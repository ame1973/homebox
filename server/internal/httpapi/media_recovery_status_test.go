//go:build linux

package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/password"
	"homebox/internal/store"
)

const recoveryStatusPath = "/api/v1/system/media-recovery"

func recoveryHTTPFixture(t *testing.T, start bool) (*store.Store, *store.Auth, *store.MediaFiles, *http.Cookie, string, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "private.sqlite")
	db, err := store.Open(t.Context(), store.Config{Path: path, Migrate: true})
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
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	var files *store.MediaFiles
	if start {
		files, err = db.StartMediaFiles(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
		waitRecoveryHTTPPhase(t, files, "idle")
	}
	return db, auth, files, cookie, csrf, path
}

func waitRecoveryHTTPPhase(t *testing.T, files *store.MediaFiles, phase string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if files.RecoveryStatus().Phase == phase {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("recovery did not reach %s: %+v", phase, files.RecoveryStatus())
		case <-ticker.C:
		}
	}
}

func recoveryHTTPHandler(t *testing.T, auth *store.Auth, files *store.MediaFiles) http.Handler {
	t.Helper()
	handler, err := New(Config{Auth: auth, MediaFiles: files, PublicOrigin: "https://homebox.test", Assets: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Fixture</main>")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func recoveryHTTPStatus(t *testing.T, response *httptest.ResponseRecorder) store.MediaRecoveryStatus {
	t.Helper()
	var value store.MediaRecoveryStatus
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &value) != nil ||
		json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 4 ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("invalid recovery status response: %d %s", response.Code, response.Body.String())
	}
	return value
}

func TestMediaRecoveryStatusHTTPReturnsAuthorizedLiveUnavailableAndStopped(t *testing.T) {
	_, auth, files, cookie, csrf, _ := recoveryHTTPFixture(t, true)
	for _, candidate := range []*store.MediaFiles{files, nil} {
		handler := recoveryHTTPHandler(t, auth, candidate)
		response := respond(handler, domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", ""))
		status := recoveryHTTPStatus(t, response)
		if candidate == nil {
			if status != (store.MediaRecoveryStatus{Phase: "unavailable", Failure: "storage-unavailable"}) {
				t.Fatal("absent Media service did not report unavailable", status)
			}
		} else if status != files.RecoveryStatus() || status.Phase != "idle" {
			t.Fatal("live status did not reflect the actual worker", status)
		}
		expectError(t, respond(handler, domainRequest(http.MethodGet, recoveryStatusPath, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	status := recoveryHTTPStatus(t, respond(recoveryHTTPHandler(t, auth, files), domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", "")))
	if status.Phase != "stopped" {
		t.Fatal("closed worker did not report stopped", status)
	}
}

func TestMediaRecoveryStatusHTTPDoesNotExposeUnprovenStageNames(t *testing.T) {
	db, auth, files, cookie, csrf, path := recoveryHTTPFixture(t, true)
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(path+".media", "staging", "private-unknown-name")
	if err := os.WriteFile(unknown, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := db.StartMediaFiles(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	waitRecoveryHTTPPhase(t, files, "blocked")
	response := respond(recoveryHTTPHandler(t, auth, files), domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", ""))
	status := recoveryHTTPStatus(t, response)
	if status.Phase != "blocked" || status.Failure != "staging-unproven" || strings.Contains(response.Body.String(), "private-unknown-name") || strings.Contains(response.Body.String(), path) {
		t.Fatal("blocked status exposed or changed private staging details", status)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "retain" {
		t.Fatal("status read or audit changed an unproven stage", err)
	}
}

func TestMediaRecoveryStatusHTTPAdmissionAndBodyRevalidation(t *testing.T) {
	_, auth, files, cookie, csrf, _ := recoveryHTTPFixture(t, true)
	handler := recoveryHTTPHandler(t, auth, files)
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		r := domainRequest(method, recoveryStatusPath, "", cookie, csrf, "status-invalid-method", "")
		response := respond(handler, r)
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("recovery status advertised an unsupported method")
		}
	}
	for _, path := range []string{recoveryStatusPath + "?", recoveryStatusPath + "?unknown=x", "/api/v1/system/%6dedia-recovery"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	for index, candidate := range []*store.MediaFiles{files, nil} {
		r := domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", "")
		r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
			if _, err := auth.Logout(t.Context(), cookie.Value, "status-body-revoke-"+string(rune('a'+index))); err != nil {
				t.Fatal(err)
			}
		}})
		expectError(t, respond(recoveryHTTPHandler(t, auth, candidate), r), 401, "AUTHENTICATION_REQUIRED")
		cookie, csrf = testCredentials(t, auth, changedPassword)
	}
}

func TestMediaRecoveryStatusHTTPThroughVerifiedTLS(t *testing.T) {
	_, auth, files, cookie, _, _ := recoveryHTTPFixture(t, true)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, MediaFiles: files, PublicOrigin: origin, Assets: fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><main>Fixture</main>")},
	}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+recoveryStatusPath, nil)
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
	var status store.MediaRecoveryStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil || response.StatusCode != http.StatusOK || status.Phase != "idle" ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != "" || response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("real TLS status response did not preserve its protected contract", status, err)
	}
}

func TestMediaRecoveryStatusHTTPRequiresCompletedSessionAndMatchingOwner(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	expectError(t, respond(recoveryHTTPHandler(t, auth, nil), domainRequest(http.MethodGet, recoveryStatusPath, "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")

	_, original, files, originalCookie, originalCSRF, _ := recoveryHTTPFixture(t, true)
	_, other, _, otherCookie, otherCSRF, _ := recoveryHTTPFixture(t, false)
	response := respond(recoveryHTTPHandler(t, other, files), domainRequest(http.MethodGet, recoveryStatusPath, "", otherCookie, otherCSRF, "", ""))
	expectError(t, response, 503, "SERVICE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), "idle") || strings.Contains(response.Body.String(), "storage") {
		t.Fatal("mismatched owner disclosed recovery status")
	}
	status := recoveryHTTPStatus(t, respond(recoveryHTTPHandler(t, original, files), domainRequest(http.MethodGet, recoveryStatusPath, "", originalCookie, originalCSRF, "", "")))
	if status.Phase != "idle" {
		t.Fatal("mismatched read changed the original owner", status)
	}
}
