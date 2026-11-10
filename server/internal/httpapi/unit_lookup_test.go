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
	"strings"
	"testing"
	"unicode/utf8"

	"homebox/internal/password"
	"homebox/internal/store"
)

type unitLookupFixture struct {
	auth       *store.Auth
	handler    http.Handler
	cookie     *http.Cookie
	csrf       string
	itemID     string
	identities []string
}

// Seed retained identities only with the serving store stopped. The real Unit
// and Item counters are independent of any invented receipt or source event.
func newUnitLookupFixture(t *testing.T) unitLookupFixture {
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
	fixture := unitLookupFixture{
		itemID:     "legacy/item\x00😀",
		identities: []string{"legacy/unit/\x00😀", "\x00", ".", "..", "new", "tree", "lookup", "space +?#", `back\slash`, strings.Repeat("界", 128)},
	}
	const now = "2026-11-06T00:00:00Z"
	exec(`INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Legacy owner','individual',?,?)`, fixture.itemID, now, now)
	exec(`INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('item',?,7)`, fixture.itemID)
	for index, id := range fixture.identities {
		exec(`INSERT INTO item_units(id,item_id,asset_code,notes,status,created_at,updated_at) VALUES(?,?,?,?,'stored',?,?)`, id, fixture.itemID, fmt.Sprintf("LOOKUP-%d", index), "Exact \x00 é\n", now, now)
		exec(`INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('unit',?,5)`, id)
	}
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

func TestUnitLookupHTTPReadsExactRetainedIdentityAndIndependentETag(t *testing.T) {
	f := newUnitLookupFixture(t)
	for _, id := range f.identities {
		t.Run(fmt.Sprintf("length-%d-%x", len(id), []byte(id)[:1]), func(t *testing.T) {
			path := "/api/v1/units?id=" + url.QueryEscape(id)
			response := respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", ""))
			var value store.Unit
			var fields map[string]json.RawMessage
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 8 ||
				value.ID != id || value.ItemID != f.itemID || value.Revision != 5 || value.Notes != "Exact \x00 é\n" || value.LocationID != nil || value.SerialNumber != nil ||
				response.Header().Get("ETag") != `"5"` || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Set-Cookie") != "" {
				t.Fatalf("query lookup changed the actual Unit or proof: status %d body %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-storage") || strings.Contains(response.Body.String(), "session") {
				t.Fatal("lookup exposed private source or session metadata")
			}
			if id == "new" || id == "tree" || id == "lookup" {
				direct := respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units/"+id, "", f.cookie, f.csrf, "", ""))
				if direct.Code != 200 || direct.Body.String() != response.Body.String() || direct.Header().Get("ETag") != `"5"` {
					t.Fatal("query and path reads did not return the same independent Unit")
				}
			}
		})
	}
	for _, raw := range []string{"", "?", "?id=", "?other=unit", "?id=unit&id=other", "?id=unit&other=x", "?id=%FF", "?id=%", "?id=" + url.QueryEscape(strings.Repeat("界", 129)), "?id=unit;extra=x", "?id=unit&" + strings.Repeat("x", 16<<10)} {
		response := respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units"+raw, "", f.cookie, f.csrf, "", ""))
		expectError(t, response, 400, "INVALID_QUERY")
		if response.Header().Get("ETag") != "" {
			t.Fatal("invalid lookup supplied a resource proof")
		}
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units?id=absent", "", f.cookie, f.csrf, "", "")), 404, "NOT_FOUND")
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/units?id=legacy%252Funit", "", f.cookie, f.csrf, "", "")), 404, "NOT_FOUND")
}

func FuzzUnitLookupQuery(f *testing.F) {
	for _, query := range []string{"", "id=unit", "id=legacy%2F%00%F0%9F%98%80", "id=%FF", "id=unit&id=other", "id=unit;bad", "id=" + url.QueryEscape(strings.Repeat("界", 128))} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > (16<<10)+1 {
			t.Skip()
		}
		request := apiRequest(http.MethodGet, "/api/v1/units", "")
		request.URL.RawQuery = query
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		id, ok := unitLookupQuery(response, request, "request")
		if ok {
			values, err := url.ParseQuery(query)
			if err != nil || len(query) > 16<<10 || len(values) != 1 || len(values["id"]) != 1 || values["id"][0] != id ||
				!utf8.ValidString(id) || utf8.RuneCountInString(id) < 1 || utf8.RuneCountInString(id) > 128 {
				t.Fatal("query parser accepted an invalid or changed identity")
			}
		} else if response.Code != 400 || !strings.Contains(response.Body.String(), `"INVALID_QUERY"`) {
			t.Fatal("query parser returned an unsafe failure")
		}
	})
}

func TestUnitLookupHTTPRequiresCompletedSessionAndBodylessExactQuery(t *testing.T) {
	f := newUnitLookupFixture(t)
	path := "/api/v1/units?id=" + url.QueryEscape(f.identities[0])
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		response := respond(f.handler, domainRequest(method, path, "", f.cookie, f.csrf, "unit-lookup-wrong-method", `"5"`))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != "GET" {
			t.Fatal("query lookup accepted a non-GET method")
		}
	}
	for _, unknown := range []bool{false, true} {
		request := domainRequest(http.MethodGet, path, `{}`, f.cookie, f.csrf, "", "")
		if unknown {
			request.ContentLength = -1
		}
		expectError(t, respond(f.handler, request), 400, "INVALID_BODY")
	}
	request := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	request.Header.Set("Content-Encoding", "gzip")
	expectError(t, respond(f.handler, request), 400, "INVALID_BODY")
	request = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	request.URL.RawPath = "/api/v1/%75nits"
	expectError(t, respond(f.handler, request), 400, "INVALID_HOLDING_URL")
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")

	request = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
		if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "unit-lookup-revoke-during-body"); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(f.handler, request)
	expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
	if response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), "LOOKUP-0") {
		t.Fatal("revoked lookup returned Unit data or an ETag")
	}
}
