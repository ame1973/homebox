package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"homebox/internal/barcode"
	"homebox/internal/password"
	"homebox/internal/store"
)

func barcodeSearchHTTPPage(t *testing.T, response *httptest.ResponseRecorder) store.BarcodeMatches {
	t.Helper()
	var page store.BarcodeMatches
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil ||
		len(fields) != 2 || fields["next_cursor"] == nil || page.Matches == nil || response.Header().Get("ETag") != "" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid barcode search page: status %d", response.Code)
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(fields["matches"], &rows) != nil {
		t.Fatal("barcode matches were not an array")
	}
	for _, row := range rows {
		if len(row) != 4 || row["item_id"] == nil || row["barcode_id"] == nil || row["symbology"] == nil || row["normalized_value"] == nil {
			t.Fatal("search returned an incomplete row or private source fields")
		}
	}
	if strings.Contains(response.Body.String(), "retained raw differs") || strings.Contains(response.Body.String(), "Private owner name") {
		t.Fatal("search exposed private source metadata")
	}
	return page
}

func exerciseBarcodeSearchHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	first := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"First shared product"}`, "barcode-search-item-first", ""))
	second := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Second shared product"}`, "barcode-search-item-second", ""))
	add := func(owner, proof, body, key string) store.Barcode {
		t.Helper()
		return ownedReply[store.Barcode](t, call(http.MethodPost, "/api/v1/items/"+owner+"/barcodes", body, key, proof), owner, 6)
	}
	a := add(first.ID, `"1"`, `{"symbology":"UPC_A","raw_value":"036000291452"}`, "barcode-search-upc-first")
	b := add(second.ID, `"1"`, `{"symbology":"UPC_A","raw_value":"036000291452"}`, "barcode-search-upc-second")
	c := add(first.ID, `"2"`, `{"symbology":"OTHER","raw_value":"036000291452"}`, "barcode-search-other")
	archived := catalogReply(t, call(http.MethodPost, "/api/v1/items/"+second.ID+"/archive", `{}`, "barcode-search-archive", `"2"`))
	if archived.Status != "archived" {
		t.Fatal("fixture did not archive the second owner")
	}
	base := "/api/v1/search/barcodes?value=036000291452"
	page := barcodeSearchHTTPPage(t, call(http.MethodGet, base, "", "", ""))
	found := make(map[string]string)
	for _, row := range page.Matches {
		if row.NormalizedValue != "036000291452" {
			t.Fatal("search changed the leading zero")
		}
		found[row.BarcodeID] = row.ItemID
	}
	if page.NextCursor != nil || !reflect.DeepEqual(found, map[string]string{a.ID: first.ID, b.ID: second.ID, c.ID: first.ID}) {
		t.Fatal("search collapsed shared, archived or independent matches", found)
	}
	filtered := barcodeSearchHTTPPage(t, call(http.MethodGet, base+"&symbology=UPC_A", "", "", ""))
	if len(filtered.Matches) != 2 || filtered.NextCursor != nil {
		t.Fatal("exact namespace search lost a shared product")
	}
	empty := barcodeSearchHTTPPage(t, call(http.MethodGet, "/api/v1/search/barcodes?value=36000291452", "", "", ""))
	if len(empty.Matches) != 0 || empty.NextCursor != nil {
		t.Fatal("lookup silently converted a different number")
	}
	firstPage := barcodeSearchHTTPPage(t, call(http.MethodGet, base+"&limit=1", "", "", ""))
	if len(firstPage.Matches) != 1 || firstPage.NextCursor == nil {
		t.Fatal("shared matches did not produce a live cursor")
	}
	if response := call(http.MethodPost, "/api/v1/auth/logout", "", "barcode-search-logout", ""); response.Code != http.StatusOK {
		t.Fatal("logout failed")
	}
	freshLogin()
	next := barcodeSearchHTTPPage(t, call(http.MethodGet, base+"&limit=1&cursor="+url.QueryEscape(*firstPage.NextCursor), "", "", ""))
	if len(next.Matches) != 1 || next.Matches[0].BarcodeID == firstPage.Matches[0].BarcodeID {
		t.Fatal("a fresh same-principal session could not continue")
	}
	expectError(t, call(http.MethodGet, base+"&symbology=UPC_A&limit=1&cursor="+url.QueryEscape(*firstPage.NextCursor), "", "", ""), 400, "INVALID_CURSOR")
}

func TestBarcodeSearchHTTPRealWritesAndFreshSession(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	exerciseBarcodeSearchHTTP(t, call, func() { cookie, csrf = testCredentials(t, auth, changedPassword) })
}

func TestBarcodeSearchHTTPThroughVerifiedTLS(t *testing.T) {
	call, login := domainTLSFixture(t)
	exerciseBarcodeSearchHTTP(t, call, login)
}

type barcodeSearchHTTPFixture struct {
	auth         *store.Auth
	handler      http.Handler
	cookie       *http.Cookie
	csrf         string
	databasePath string
	value        string
	matches      []store.BarcodeMatch
}

// Seed retained source offline, without inventing mutation receipts or events.
func newBarcodeSearchHTTPFixture(t *testing.T) barcodeSearchHTTPFixture {
	t.Helper()
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
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err = raw.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{strings.Repeat("\x00", 128), "\x00barcode\r\n", ".", "..", `a/b\`, "☕😀", strings.Repeat("😀", 128), "\U0010ffff"}
	for index := range 205 {
		ids = append(ids, fmt.Sprintf("barcode-%03d", index))
	}
	fixture := barcodeSearchHTTPFixture{databasePath: path, value: "\x00legacy + é\r\n00123"}
	for index, id := range ids {
		owner, symbology, status := fmt.Sprintf("barcode-owner-%03d", index), "QR_CODE", "active"
		if index == 0 {
			owner = "legacy/item\x00😀"
		}
		if index == 1 {
			status = "archived"
		}
		if index%2 == 0 {
			symbology = "EAN_13"
		}
		exec(`INSERT INTO items(id,name,tracking_mode,status,created_at,updated_at) VALUES(?,'Private owner name','individual',?,'2026','2026')`, owner, status)
		exec(`INSERT INTO projection_revisions VALUES('item',?,7)`, owner)
		exec(`INSERT INTO item_barcodes VALUES(?,?,?,'retained raw differs',?,'2026')`, id, owner, symbology, fixture.value)
		exec(`INSERT INTO projection_revisions VALUES('barcode',?,5)`, id)
		fixture.matches = append(fixture.matches, store.BarcodeMatch{ItemID: owner, BarcodeID: id, Symbology: symbology, NormalizedValue: fixture.value})
	}
	sort.Slice(fixture.matches, func(i, j int) bool { return fixture.matches[i].BarcodeID < fixture.matches[j].BarcodeID })
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(t.Context(), store.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	fixture.auth, err = db.NewAuth(config)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cookie, fixture.csrf = testCredentials(t, fixture.auth, changedPassword)
	fixture.handler = authHandler(t, fixture.auth)
	return fixture
}

func TestBarcodeSearchHTTPPagesAdmissionAndLateRevalidation(t *testing.T) {
	f := newBarcodeSearchHTTPFixture(t)
	get := func(path string) *httptest.ResponseRecorder {
		return respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", ""))
	}
	base := "/api/v1/search/barcodes?value=" + url.QueryEscape(f.value)
	var actual []store.BarcodeMatch
	cursor := ""
	for {
		path := base
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := barcodeSearchHTTPPage(t, get(path))
		if len(page.Matches) == 0 || len(page.Matches) > 50 {
			t.Fatal("default page did not preserve its bound")
		}
		actual = append(actual, page.Matches...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(actual, f.matches) {
		t.Fatal("live HTTP pages changed retained values, independent IDs or owner identity")
	}
	max := barcodeSearchHTTPPage(t, get(base+"&limit=100&symbology=EAN_13"))
	if len(max.Matches) != 100 || max.NextCursor == nil {
		t.Fatal("maximum namespace page omitted retained legacy values")
	}
	for _, row := range max.Matches {
		if row.Symbology != "EAN_13" || row.NormalizedValue != f.value {
			t.Fatal("legacy checksum policy changed an exact retained match")
		}
	}
	first := barcodeSearchHTTPPage(t, get(base+"&limit=1"))
	if len(first.Matches) != 1 || first.Matches[0].BarcodeID != strings.Repeat("\x00", 128) || first.NextCursor == nil {
		t.Fatal("first retained control identity was not preserved")
	}
	second := barcodeSearchHTTPPage(t, get(base+"&limit=1&cursor="+url.QueryEscape(*first.NextCursor)))
	if len(second.Matches) != 1 || second.Matches[0].BarcodeID != "\x00barcode\r\n" {
		t.Fatal("continuation changed the next exact identity")
	}
	for _, path := range []string{
		base + "&limit=2&cursor=" + url.QueryEscape(*first.NextCursor),
		base + "&limit=1&symbology=EAN_13&cursor=" + url.QueryEscape(*first.NextCursor),
		"/api/v1/search/barcodes?value=different&limit=1&cursor=" + url.QueryEscape(*first.NextCursor),
	} {
		expectError(t, get(path), 400, "INVALID_CURSOR")
	}
	for _, query := range []string{
		"", "value=", "value=x&value=y", "value=%FF", "value=%", "value=x;limit=1", "value=" + url.QueryEscape(strings.Repeat("😀", 513)),
		"value=x&symbology=", "value=x&symbology=JAN", "value=x&symbology=OTHER&symbology=OTHER", "value=x&unknown=x",
		"value=x&limit=0", "value=x&limit=101", "value=x&limit=1&limit=2", "value=x&cursor=", "value=x&cursor=%FF",
		"value=x&cursor=" + url.QueryEscape(strings.Repeat("😀", 513)),
	} {
		r := domainRequest(http.MethodGet, "/api/v1/search/barcodes", "", f.cookie, f.csrf, "", "")
		r.URL.RawQuery = query
		expectError(t, respond(f.handler, r), 400, "INVALID_QUERY")
	}
	for _, query := range []string{"value=x&cursor=" + strings.Repeat("x", 2048), "value=x&cursor=" + url.QueryEscape(strings.Repeat("😀", 512))} {
		r := domainRequest(http.MethodGet, "/api/v1/search/barcodes", "", f.cookie, f.csrf, "", "")
		r.URL.RawQuery = query
		expectError(t, respond(f.handler, r), 400, "INVALID_CURSOR")
	}
	prefix := "value=x&limit="
	bounded := prefix + strings.Repeat("0", (16<<10)-len(prefix)-1) + "1"
	r := domainRequest(http.MethodGet, "/api/v1/search/barcodes", "", f.cookie, f.csrf, "", "")
	r.URL.RawQuery = bounded
	if page := barcodeSearchHTTPPage(t, respond(f.handler, r)); len(page.Matches) != 0 || page.NextCursor != nil {
		t.Fatal("exact raw-query budget changed the query")
	}
	r = domainRequest(http.MethodGet, "/api/v1/search/barcodes", "", f.cookie, f.csrf, "", "")
	r.URL.RawQuery = bounded + "0"
	expectError(t, respond(f.handler, r), 400, "INVALID_QUERY")
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(f.handler, domainRequest(method, base, "", f.cookie, f.csrf, "barcode-search-wrong-method", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("search advertised a write method")
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, base, "", f.cookie, f.csrf, "", "")
		mutate(r)
		expectError(t, respond(f.handler, r), 400, "INVALID_BODY")
	}
	r = domainRequest(http.MethodGet, base, "", f.cookie, f.csrf, "", "")
	r.URL.RawPath = "/api/v1/search/%62arcodes"
	expectError(t, respond(f.handler, r), 400, "INVALID_QUERY")
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, base, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, base, "", f.cookie, f.csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(f.handler, r), 403, "ORIGIN_REJECTED")
	}
	expectError(t, get("/api/v1/search/barcodes/other"), 404, "NOT_FOUND")

	raw, err := sql.Open("sqlite", f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	r = domainRequest(http.MethodGet, base+"&limit=1", "", f.cookie, f.csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := raw.ExecContext(t.Context(), `UPDATE item_barcodes SET raw_value=CAST(x'ff' AS TEXT) WHERE id=?`, f.matches[1].BarcodeID); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(f.handler, r)
	expectError(t, response, 503, "SERVICE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), "barcode_id") || response.Header().Get("ETag") != "" {
		t.Fatal("invalid lookahead exposed a prefix")
	}
	if _, err = raw.ExecContext(t.Context(), `UPDATE item_barcodes SET raw_value='retained raw differs' WHERE id=?`, f.matches[1].BarcodeID); err != nil {
		t.Fatal(err)
	}
	barcodeSearchHTTPPage(t, get(base+"&limit=1"))
	r = domainRequest(http.MethodGet, base, "", f.cookie, f.csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "barcode-search-body-revoke"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(f.handler, r), 401, "AUTHENTICATION_REQUIRED")
}

func TestBarcodeSearchHTTPRequiresCompletedSession(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	expectError(t, respond(authHandler(t, auth), domainRequest(http.MethodGet, "/api/v1/search/barcodes?value=001", "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}

func FuzzBarcodeSearchQuery(f *testing.F) {
	for _, query := range []string{"", "value=001", "value=%00GS1%1D&symbology=CODE_128", "value=%FF", "value=x&value=y", "value=x&limit=1&cursor=opaque", "value=x&cursor=%FF", "value=x;bad", "value=" + url.QueryEscape(strings.Repeat("😀", 512))} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > 20<<10 {
			t.Skip()
		}
		request := apiRequest(http.MethodGet, "/api/v1/search/barcodes", "")
		request.URL.RawQuery = query
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		value, symbology, cursor, limit, ok := barcodeSearchQuery(response, request, "request")
		if !ok {
			if response.Code != 400 || !strings.Contains(response.Body.String(), `"INVALID_QUERY"`) {
				t.Fatal("invalid query returned an unsafe failure")
			}
			return
		}
		values, err := url.ParseQuery(query)
		if err != nil || len(query) > 16<<10 || len(values["value"]) != 1 || values.Get("value") != value ||
			!utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 512 ||
			values.Get("symbology") != symbology || symbology != "" && !barcode.ValidSymbology(symbology) ||
			values.Get("cursor") != cursor || !utf8.ValidString(cursor) || len(cursor) > 2048 || limit < 1 || limit > 100 {
			t.Fatal("accepted query changed or exceeded its exact scope")
		}
		for name, entries := range values {
			if (name != "value" && name != "symbology" && name != "limit" && name != "cursor") || len(entries) != 1 {
				t.Fatal("accepted an unknown or repeated query field")
			}
		}
	})
}
