//go:build linux

package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

const syncStatusPath = "/api/v1/sync/status"

func TestSyncStatusHTTPReportsPartialMaterializationWithoutEnablingSync(t *testing.T) {
	_, auth, _, cookie, csrf, _ := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	read := func() store.SyncStatus {
		t.Helper()
		response := respond(handler, domainRequest(http.MethodGet, syncStatusPath, "", cookie, csrf, "", ""))
		var status store.SyncStatus
		var fields map[string]json.RawMessage
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil ||
			json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 11 ||
			response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
			response.Header().Get("Set-Cookie") != "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("invalid sync status response: %d %s", response.Code, response.Body.String())
		}
		return status
	}
	status := read()
	if status.ProjectionVersion != 1 || status.HighWatermark != 0 || status.MinRetainedSeq != 0 ||
		status.LastChangeAt != nil || len(status.Counts) != 0 || status.CountsScope != "materialized_projections" ||
		status.BootstrapReady || status.RetentionEnabled || status.RetentionDays != 30 {
		t.Fatal("empty status claimed a complete replica", status)
	}
	locations, err := store.NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locations.Create(t.Context(), cookie.Value, "sync-status-location", 0, store.LocationCreate{Code: "STATUS", Name: "Status"}); err != nil {
		t.Fatal(err)
	}
	status = read()
	if status.HighWatermark != 1 || status.LastChangeAt == nil || status.Counts["location"] != 1 || status.BootstrapReady {
		t.Fatal("status did not reflect the actual committed projection", status)
	}
	for _, path := range []string{"/api/v1/sync/bootstrap", "/api/v1/sync/changes?after=unavailable"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
}

func TestSyncStatusHTTPThroughVerifiedTLS(t *testing.T) {
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
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+syncStatusPath, nil)
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
	var status store.SyncStatus
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&status) != nil ||
		status.BootstrapReady || status.CountsScope != "materialized_projections" || response.Header.Get("ETag") != "" ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("verified TLS status failed or claimed sync readiness", response.StatusCode, status)
	}
}

func TestSyncStatusHTTPRequiresAuthorizationAndStrictRead(t *testing.T) {
	_, auth, _, cookie, csrf, _ := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	expectError(t, respond(handler, domainRequest(http.MethodGet, syncStatusPath, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, syncStatusPath, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, syncStatusPath, "", cookie, csrf, "sync-status-method", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("status advertised an unsupported method")
		}
	}
	for _, path := range []string{syncStatusPath + "?", syncStatusPath + "?unknown=x", "/api/v1/sync/%73tatus"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, syncStatusPath, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, syncStatusPath, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "sync-status-body-revoke"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
}
