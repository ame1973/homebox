package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/password"
	"homebox/internal/store"
)

func TestLocationMovementsHTTPReadsRealHistoryAndInterchangesCursors(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	request := func(method, path, body, key, revision string) *http.Request {
		return domainRequest(method, path, body, cookie, csrf, key, revision)
	}
	first := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", `{"name":"First","code":"MOVEMENT-A"}`, "movement-first", "")))
	second := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", `{"name":"Second","code":"MOVEMENT-B"}`, "movement-second", "")))
	child := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"Child","code":"MOVEMENT-C","parent_id":%q}`, first.ID), "movement-child", `"1"`)))
	child = responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations/"+child.ID+"/move", fmt.Sprintf(`{"target_parent_id":%q}`, second.ID), "movement-move-one", strconv.Quote(strconv.FormatInt(child.Revision, 10)))))
	child = responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations/"+child.ID+"/move", `{"target_parent_id":null}`, "movement-move-two", strconv.Quote(strconv.FormatInt(child.Revision, 10)))))
	path := "/api/v1/locations/" + child.ID + "/movements"
	alias := "/api/v1/location-movements?id=" + url.QueryEscape(child.ID)
	read := func(target string) (store.LocationMovements, string) {
		t.Helper()
		response := respond(handler, request(http.MethodGet, target, "", "", ""))
		var page store.LocationMovements
		var fields map[string]json.RawMessage
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil ||
			len(fields) != 4 || fields["revision"] != nil || fields["next_cursor"] == nil || page.LocationID != child.ID || page.Deleted || page.Data == nil ||
			response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(child.Revision, 10)) {
			t.Fatalf("invalid actual movement page %d %s", response.Code, response.Body.String())
		}
		return page, response.Body.String()
	}
	page, _ := read(path + "?limit=1")
	if len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatal("first page lost its continuation", page)
	}
	other, _ := read(alias + "&limit=1")
	if other.NextCursor == nil || !reflect.DeepEqual(other.Data, page.Data) {
		t.Fatal("path and exact identity produced different scoped records")
	}
	continuation, _ := read(alias + "&limit=1&cursor=" + url.QueryEscape(*page.NextCursor))
	if len(continuation.Data) != 1 || continuation.NextCursor != nil || continuation.Data[0].ID <= page.Data[0].ID {
		t.Fatal("exact alias failed to continue the path cursor", continuation)
	}
	continuedPath, _ := read(path + "?limit=1&cursor=" + url.QueryEscape(*other.NextCursor))
	if !reflect.DeepEqual(continuation.Data, continuedPath.Data) {
		t.Fatal("path failed to continue the exact-identity cursor")
	}
	deleted := respond(handler, request(http.MethodDelete, "/api/v1/locations/"+child.ID, "", "movement-delete", strconv.Quote(strconv.FormatInt(child.Revision, 10))))
	var ack store.ActionAck
	if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil {
		t.Fatal("logical Location deletion failed", deleted.Code, deleted.Body.String())
	}
	expectError(t, respond(handler, request(http.MethodGet, path+"?limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", "", "")), 400, "INVALID_CURSOR")
	retained := respond(handler, request(http.MethodGet, alias, "", "", ""))
	var history store.LocationMovements
	if retained.Code != 200 || json.Unmarshal(retained.Body.Bytes(), &history) != nil || !history.Deleted || len(history.Data) != 2 ||
		retained.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(ack.Revision, 10)) {
		t.Fatal("deleted owner's actual retained movements were lost", retained.Code, retained.Body.String())
	}
}

func TestLocationMovementsHTTPEnforcesCompletedSessionAndStrictBoundary(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	root := responseLocation(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"MOVEMENT-BOUNDARY"}`, cookie, csrf, "movement-boundary", "")))
	path, alias := "/api/v1/locations/"+root.ID+"/movements", "/api/v1/location-movements?id="+url.QueryEscape(root.ID)
	for _, target := range []string{path, alias} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
		for _, origin := range []string{"", "https://foreign.test"} {
			r := domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")
			r.Header.Set("Origin", origin)
			expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
		}
		for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			response := respond(handler, domainRequest(method, target, "", cookie, csrf, "movement-method", ""))
			expectError(t, response, 405, "METHOD_NOT_ALLOWED")
			if response.Header().Get("Allow") != http.MethodGet {
				t.Fatal("movement history advertised another method")
			}
		}
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
			func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		} {
			r := domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")
			mutate(r)
			expectError(t, respond(handler, r), 400, "INVALID_BODY")
		}
	}
	for _, target := range []string{path + "?limit=0", path + "?limit=101", path + "?limit=1&limit=2", path + "?cursor=", path + "?unknown=1",
		"/api/v1/location-movements", "/api/v1/location-movements?id=", alias + "&id=other", alias + "&scope=subtree", "/api/v1/location-movements?id=%ff", alias + "&cursor=" + strings.Repeat("x", 2049), alias + "&" + strings.Repeat("x", 16<<10)} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, target := range []string{"/api/v1/locations/%61lias/movements", "/api/v1/locations/%00/movements", "/api/v1/locations/" + strings.Repeat("x", 129) + "/movements"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 400, "INVALID_LOCATION_URL")
	}
	expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/location-movements?id=missing", "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	r := domainRequest(http.MethodGet, alias, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "movement-revoke-during-body"); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(handler, r)
	expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
	if response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), root.ID) {
		t.Fatal("revoked movement read exposed private history or a version")
	}
}

func TestLocationMovementsHTTPRetainsExactLegacyOwnerIdentities(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "homebox.sqlite")
	db, err := store.Open(t.Context(), store.Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	config := store.AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}}
	auth, err := db.NewAuth(config)
	if err != nil {
		t.Fatal(err)
	}
	setupAccount(t, auth, true)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if raw != nil {
			if err := raw.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	raw.SetMaxOpenConns(1)
	tx, err := raw.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ids := []string{"legacy/location\x00😀", "\x00", ".", "..", "new", "tree", `back\slash`, "literal%2F", "name+plus", "space here", "what?now", "unité", "semi;colon", "hash#mark", "quote'group", "star*value", "paren(value)", strings.Repeat("界", 128)}
	// These match the path spelling produced by JavaScript encodeURIComponent.
	encoded := map[string]string{
		"new": "new", "tree": "tree", "literal%2F": "literal%252F", "name+plus": "name%2Bplus",
		"space here": "space%20here", "what?now": "what%3Fnow", "unité": "unit%C3%A9",
		"semi;colon": "semi%3Bcolon", "hash#mark": "hash%23mark", "quote'group": "quote'group",
		"star*value": "star*value", "paren(value)": "paren(value)",
	}
	noncanonical := map[string]bool{"name+plus": true, "semi;colon": true, "quote'group": true, "star*value": true, "paren(value)": true}
	for index, id := range ids {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,?,?,'legacy','legacy')`, []any{id, fmt.Sprintf("LEGACY-HISTORY-%d", index), "Legacy location"}},
			{`INSERT INTO projection_revisions VALUES('location',?,7)`, []any{id}},
			{`INSERT INTO location_events(id,location_id,occurred_at) VALUES(?,?,'legacy time')`, []any{fmt.Sprintf("legacy-event-%d", index), id}},
			{`INSERT INTO projection_revisions VALUES('location_event',?,2)`, []any{fmt.Sprintf("legacy-event-%d", index)}},
		} {
			if _, err := tx.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	raw = nil
	db, err = store.Open(t.Context(), store.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = db.NewAuth(config)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := testCredentials(t, auth, changedPassword)
	handler := authHandler(t, auth)
	for _, id := range ids {
		response := respond(handler, domainRequest(http.MethodGet, "/api/v1/location-movements?id="+url.QueryEscape(id), "", cookie, csrf, "", ""))
		var result store.LocationMovements
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.LocationID != id || len(result.Data) != 1 || result.Data[0].LocationID != id || result.Data[0].OccurredAt != "legacy time" || response.Header().Get("ETag") != `"7"` {
			t.Fatal("exact retained owner or event was altered", id, response.Code, response.Body.String())
		}
		if path, ok := encoded[id]; ok {
			direct := respond(handler, domainRequest(http.MethodGet, "/api/v1/locations/"+path+"/movements", "", cookie, csrf, "", ""))
			if noncanonical[id] {
				expectError(t, direct, http.StatusBadRequest, "INVALID_LOCATION_URL")
			} else if direct.Code != 200 || direct.Body.String() != response.Body.String() {
				t.Errorf("encoded owner subpath changed identity %q: %d %s", id, direct.Code, direct.Body.String())
			}
		}
	}
}
