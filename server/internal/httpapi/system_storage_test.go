//go:build linux

package httpapi

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/sys/unix"
)

const systemStoragePath = "/api/v1/system/storage"

type storageResponse struct {
	DatabaseBytes                int64  `json:"database_bytes"`
	MediaBytes                   *int64 `json:"media_bytes"`
	FreeBytes                    int64  `json:"free_bytes"`
	BackupBytes                  *int64 `json:"backup_bytes"`
	DatabaseFilesystemTotalBytes int64  `json:"database_filesystem_total_bytes"`
	MeasuredAt                   string `json:"measured_at"`
}

func TestSystemStorageHTTPReadsActualDatabaseFilesystem(t *testing.T) {
	var database string
	_, handler, cookie, csrf := domainHTTPFixture(t, &database)
	main, err := os.Lstat(database)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(database, &stat); err != nil {
		t.Fatal(err)
	}
	unit := stat.Frsize
	if unit == 0 {
		unit = stat.Bsize
	}
	if unit <= 0 || stat.Blocks > uint64(math.MaxInt64)/uint64(unit) {
		t.Fatal("test filesystem cannot be represented", stat)
	}
	response := respond(handler, domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", ""))
	var got storageResponse
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil ||
		json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 6 ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("invalid storage response: %d %s", response.Code, response.Body.String())
	}
	if got.DatabaseBytes < main.Size() || got.FreeBytes < 0 || got.FreeBytes > got.DatabaseFilesystemTotalBytes ||
		got.DatabaseFilesystemTotalBytes != int64(stat.Blocks*uint64(unit)) || got.MediaBytes != nil || got.BackupBytes != nil ||
		string(fields["media_bytes"]) != "null" || string(fields["backup_bytes"]) != "null" {
		t.Fatalf("storage response did not use the actual database filesystem or distinguish unknown scopes: %+v", got)
	}
	if _, err = time.Parse(time.RFC3339Nano, got.MeasuredAt); err != nil {
		t.Fatal("invalid observation time", err)
	}
	if strings.Contains(response.Body.String(), database) || strings.Contains(response.Body.String(), cookie.Value) {
		t.Fatal("storage response leaked a private path or credential")
	}
}

func TestSystemStorageHTTPAdmissionAndBodyTimeRevalidation(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	expectError(t, respond(handler, domainRequest(http.MethodGet, systemStoragePath, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, systemStoragePath, "", cookie, csrf, "storage-method-key", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("storage observation advertised an unsupported method")
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
		r := domainRequest(http.MethodPost, systemStoragePath, "", cookie, csrf, "storage-method-key", "")
		r.Header.Del(test.header)
		expectError(t, respond(handler, r), test.status, test.code)
	}
	for _, path := range []string{systemStoragePath + "?", systemStoragePath + "?secret=x", "/api/v1/system/%73torage"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "storage-body-revoke"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
}

func TestSystemStorageHTTPRejectsIncompleteSessionAndUnsafeSidecar(t *testing.T) {
	incomplete := newTestAuth(t)
	setupAccount(t, incomplete, false)
	cookie, csrf := testCredentials(t, incomplete, initialPassword)
	expectError(t, respond(authHandler(t, incomplete), domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")

	var database string
	_, handler, cookie, csrf := domainHTTPFixture(t, &database)
	if err := os.Symlink("private-sidecar-target", database+"-journal"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(database + "-journal"); err != nil {
			t.Error(err)
		}
	})
	response := respond(handler, domainRequest(http.MethodGet, systemStoragePath, "", cookie, csrf, "", ""))
	expectError(t, response, 503, "SERVICE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), database) || strings.Contains(response.Body.String(), "private-sidecar-target") || strings.Contains(response.Body.String(), cookie.Value) {
		t.Fatal("unsafe sidecar error exposed private filesystem information")
	}
}

func TestSystemStorageHTTPThroughVerifiedTLS(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, _ := testCredentials(t, auth, changedPassword)
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
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+systemStoragePath, nil)
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
	var got storageResponse
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&got) != nil ||
		got.DatabaseBytes <= 0 || got.FreeBytes > got.DatabaseFilesystemTotalBytes || got.MediaBytes != nil || got.BackupBytes != nil ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("ETag") != "" || response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("verified TLS storage read failed: %d %+v", response.StatusCode, got)
	}
}
