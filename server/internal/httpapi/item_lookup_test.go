package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homebox/internal/password"
	"homebox/internal/store"
)

type itemLookupFixture struct {
	auth       *store.Auth
	handler    http.Handler
	cookie     *http.Cookie
	csrf       string
	identities []string
}

// Retained source is seeded with the serving store stopped, without inventing
// a mutation receipt or using the disposable current projection as authority.
func newItemLookupFixture(t *testing.T) itemLookupFixture {
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
	fixture := itemLookupFixture{identities: []string{"legacy/item\x00😀", "\x00", ".", "..", "new", "tree", "lookup", "space +?#", `back\slash`, "percent%2F", "line\r\nbreak", strings.Repeat("界", 128)}}
	const now = "2026-11-06T00:00:00Z"
	for _, id := range fixture.identities {
		exec(`INSERT INTO items(id,name,tracking_mode,brand,model,notes,status,created_at,updated_at) VALUES(?,'Actual owner','individual',NULL,'',?,'archived',?,?)`, id, "Exact \x00 é\n", now, now)
		exec(`INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES('item',?,7)`, id)
		exec(`INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('item',?,7,'{"id":"poisoned projection"}',?)`, id, now)
	}
	exec(`INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('broken/counter','Invalid owner','individual',?,?)`, now, now)
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

func TestItemLookupHTTPReadsExactRetainedAuthority(t *testing.T) {
	f := newItemLookupFixture(t)
	for _, id := range f.identities {
		path := "/api/v1/items-by-identity?id=" + url.QueryEscape(id)
		response := respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", ""))
		item := catalogReply(t, response)
		if item.ID != id || item.Name != "Actual owner" || item.Revision != 7 || item.Status != "archived" || item.Brand != nil || item.Model == nil || *item.Model != "" || item.Notes != "Exact \x00 é\n" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("query lookup changed the actual Item or aggregate proof for %q: %d %s", id, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "poisoned projection") || strings.Contains(response.Body.String(), "session") {
			t.Fatal("lookup returned a disposable projection or private session metadata")
		}
		if id == "new" || id == "tree" || id == "lookup" {
			direct := respond(f.handler, domainRequest(http.MethodGet, "/api/v1/items/"+id, "", f.cookie, f.csrf, "", ""))
			if direct.Code != http.StatusOK || direct.Body.String() != response.Body.String() || direct.Header().Get("ETag") != `"7"` {
				t.Fatal("query and path reads did not return the same actual Item")
			}
		}
	}
	for _, raw := range []string{"", "?", "?id=", "?other=item", "?id=item&id=other", "?id=item&other=x", "?id=%FF", "?id=%", "?id=" + url.QueryEscape(strings.Repeat("界", 129)), "?id=item;extra=x", "?id=item&" + strings.Repeat("x", 16<<10)} {
		response := respond(f.handler, domainRequest(http.MethodGet, "/api/v1/items-by-identity"+raw, "", f.cookie, f.csrf, "", ""))
		expectError(t, response, 400, "INVALID_QUERY")
		if response.Header().Get("ETag") != "" {
			t.Fatal("invalid lookup supplied a resource proof")
		}
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, "/api/v1/items-by-identity?id=missing", "", f.cookie, f.csrf, "", "")), 404, "NOT_FOUND")
	corrupt := respond(f.handler, domainRequest(http.MethodGet, "/api/v1/items-by-identity?id=broken%2Fcounter", "", f.cookie, f.csrf, "", ""))
	expectError(t, corrupt, 503, "SERVICE_UNAVAILABLE")
	if corrupt.Header().Get("ETag") != "" || strings.Contains(corrupt.Body.String(), "Invalid owner") {
		t.Fatal("lookup returned corrupt source or invented a revision")
	}
}

func TestItemLookupHTTPRequiresCompletedSessionAndBodylessExactQuery(t *testing.T) {
	f := newItemLookupFixture(t)
	path := "/api/v1/items-by-identity?id=" + url.QueryEscape(f.identities[0])
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		response := respond(f.handler, domainRequest(method, path, "", f.cookie, f.csrf, "item-lookup-wrong-method", `"7"`))
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
	request.URL.RawPath = "/api/v1/%69tems-by-identity"
	expectError(t, respond(f.handler, request), 400, "INVALID_CATALOG_URL")
	request = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	request.Header.Set("Origin", "https://other.example")
	expectError(t, respond(f.handler, request), 403, "ORIGIN_REJECTED")
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")

	request = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
		if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "item-lookup-revoke-during-body"); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(f.handler, request)
	expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
	if response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), "Actual owner") {
		t.Fatal("revoked lookup returned Item data or an ETag")
	}
}

func TestItemLookupHTTPRequiresPasswordChangeBeforeReading(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	response := respond(authHandler(t, auth), domainRequest(http.MethodGet, "/api/v1/items-by-identity?id=any", "", cookie, csrf, "", ""))
	expectError(t, response, 403, "PASSWORD_CHANGE_REQUIRED")
	if response.Header().Get("ETag") != "" {
		t.Fatal("incomplete session received an Item proof")
	}
}

func TestItemLookupHTTPThroughVerifiedTLS(t *testing.T) {
	call, login := domainTLSFixture(t)
	created := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"TLS Item"}`, "item-identity-tls-create", ""))
	path := "/api/v1/items/" + created.ID
	alias := "/api/v1/items-by-identity?id=" + url.QueryEscape(created.ID)
	fromPath := call(http.MethodGet, path, "", "", "")
	fromAlias := call(http.MethodGet, alias, "", "", "")
	if fromPath.Code != 200 || fromAlias.Code != 200 || fromPath.Body.String() != fromAlias.Body.String() || fromPath.Header().Get("ETag") != fromAlias.Header().Get("ETag") {
		t.Fatal("verified-TLS path and identity reads returned different source or proofs")
	}
	archived := catalogReply(t, call(http.MethodPost, path+"/archive", `{}`, "item-identity-tls-archive", `"1"`))
	login()
	current := catalogReply(t, call(http.MethodGet, alias, "", "", ""))
	if current.ID != created.ID || current.Status != "archived" || current.Revision != archived.Revision || current.Revision != 2 {
		t.Fatal("verified-TLS identity read failed to use current archived source after fresh login")
	}
	expectError(t, call(http.MethodGet, alias+"&id=other", "", "", ""), 400, "INVALID_QUERY")
	expectError(t, call(http.MethodGet, "/api/v1/items-by-identity?id=missing", "", "", ""), 404, "NOT_FOUND")
}

func TestItemLookupHTTPSeesActualSourceChangesAfterBodyRead(t *testing.T) {
	f := newItemLookupFixture(t)
	catalog, err := store.NewCatalog(f.auth)
	if err != nil {
		t.Fatal(err)
	}
	id := f.identities[0]
	request := domainRequest(http.MethodGet, "/api/v1/items-by-identity?id="+url.QueryEscape(id), "", f.cookie, f.csrf, "", "")
	request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
		name := "Changed before SQL read"
		if _, err := catalog.Patch(t.Context(), f.cookie.Value, "item-lookup-change-during-body", id, 7, store.ItemPatch{Name: &name}); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(f.handler, request)
	var item store.Item
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &item) != nil || item.ID != id || item.Name != "Changed before SQL read" || item.Revision != 8 || response.Header().Get("ETag") != `"8"` {
		t.Fatalf("lookup did not use current SQL source after body reading: %d %s", response.Code, response.Body.String())
	}
}
