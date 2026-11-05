package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"homebox/internal/store"
)

func ownedWriteOperations() []struct{ method, path, body string } {
	return []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/items/subject/barcodes", `{"symbology":"OTHER","raw_value":"00123"}`},
		{http.MethodPatch, "/api/v1/barcodes/subject", `{"symbology":"OTHER","raw_value":"00123"}`},
		{http.MethodDelete, "/api/v1/barcodes/subject", ""},
		{http.MethodPost, "/api/v1/items/subject/aliases", `{"alias":"Name"}`},
		{http.MethodPatch, "/api/v1/aliases/subject", `{"alias":"Name"}`},
		{http.MethodDelete, "/api/v1/aliases/subject", ""},
		{http.MethodPost, "/api/v1/items/subject/attributes", `{"attribute_key":"Key","attribute_value":""}`},
		{http.MethodPatch, "/api/v1/attributes/subject", `{"attribute_value":""}`},
		{http.MethodDelete, "/api/v1/attributes/subject", ""},
		{http.MethodPut, "/api/v1/items/subject/tags", `{"tag_ids":[]}`},
	}
}

func TestCatalogLinksHTTPAllWritesRequireOriginalCredentialsHeadersAndProofs(t *testing.T) {
	for _, operation := range ownedWriteOperations() {
		t.Run(operation.method+operation.path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, test := range []struct {
				mutate func(*http.Request)
				status int
				code   string
			}{
				{func(r *http.Request) { r.Header.Del("Cookie") }, 401, "AUTHENTICATION_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Add("X-CSRF-Token", csrf) }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.9.3") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("Origin") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Del("If-Match") }, 409, "PRECONDITION_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("If-Match", `W/"1"`) }, 400, "INVALID_REVISION"},
				{func(r *http.Request) { r.Header.Add("If-Match", `"2"`) }, 400, "INVALID_REVISION"},
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "owned-boundary-original", `"1"`)
				test.mutate(r)
				expectError(t, respond(handler, r), test.status, test.code)
			}
			for _, query := range []string{"?unexpected=x", "?"} {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path+query, operation.body, cookie, csrf, "owned-write-query", `"1"`)), 400, "INVALID_CATALOG_URL")
			}
		})
	}
}

func TestCatalogLinksHTTPMethodsExactURLsAndBodyFreeReads(t *testing.T) {
	for _, route := range []struct{ path, allow string }{
		{"/api/v1/items/subject/barcodes", "GET, POST"}, {"/api/v1/items/subject/aliases", "GET, POST"}, {"/api/v1/items/subject/attributes", "GET, POST"}, {"/api/v1/items/subject/tags", "GET, PUT"},
		{"/api/v1/barcodes/subject", "PATCH, DELETE"}, {"/api/v1/aliases/subject", "PATCH, DELETE"}, {"/api/v1/attributes/subject", "PATCH, DELETE"},
	} {
		t.Run(route.path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
				if strings.Contains(route.allow, method) {
					continue
				}
				response := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "owned-method-boundary", `"1"`))
				expectError(t, response, 405, "METHOD_NOT_ALLOWED")
				if response.Header().Get("Allow") != route.allow {
					t.Fatal("owned route advertised an incorrect method set")
				}
			}
		})
	}
	for _, kind := range []string{"barcodes", "aliases", "attributes", "tags"} {
		t.Run(kind, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			base := "/api/v1/items/subject/" + kind
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
				func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
			} {
				r := domainRequest(http.MethodGet, base, "", cookie, csrf, "", "")
				mutate(r)
				expectError(t, respond(handler, r), 400, "INVALID_BODY")
			}
			for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=2", "cursor=", "q=uncontracted", "cursor=" + strings.Repeat("x", 2049), "x=" + strings.Repeat("x", 16<<10)} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, base+"?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
			}
			for _, id := range []string{".", "..", "bad\\id", strings.Repeat("x", 129)} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items/"+id+"/"+kind, "", cookie, csrf, "", "")), 400, "INVALID_CATALOG_URL")
			}
		})
	}
}

func TestCatalogLinksHTTPDuplicateAndChecksumFailuresAreStaticAndAtomic(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, proof string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, proof))
	}
	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Owner"}`, "owned-duplicate-item", ""))
	base := "/api/v1/items/" + item.ID
	code := ownedReply[store.Barcode](t, call(http.MethodPost, base+"/barcodes", `{"symbology":"UPC_A","raw_value":"036000291452"}`, "owned-valid-product", `"1"`), item.ID, 6)
	expectError(t, call(http.MethodPost, base+"/barcodes", `{"symbology":"UPC_A","raw_value":"036000291452"}`, "owned-duplicate-product", `"2"`), 409, "BARCODE_CONFLICT")
	for index, body := range []string{`{"symbology":"UPC_A","raw_value":"036000291453"}`, `{"symbology":"UPC_E","raw_value":"04252615"}`, `{"symbology":"CODE_128","raw_value":"非 ASCII"}`} {
		expectError(t, call(http.MethodPatch, "/api/v1/barcodes/"+code.ID, body, fmt.Sprintf("owned-invalid-checksum-%d", index), `"1"`), 422, "VALIDATION_ERROR")
	}
	alias := ownedReply[store.Alias](t, call(http.MethodPost, base+"/aliases", `{"alias":"Exact"}`, "owned-exact-alias", `"2"`), item.ID, 4)
	expectError(t, call(http.MethodPost, base+"/aliases", `{"alias":"Exact"}`, "owned-duplicate-alias", `"3"`), 409, "ALIAS_CONFLICT")
	expectError(t, call(http.MethodPatch, "/api/v1/aliases/"+alias.ID, `{"alias":"Changed","item_id":"other"}`, "owned-immutable-owner", `"1"`), 422, "VALIDATION_ERROR")
	if current := catalogReply(t, call(http.MethodGet, base, "", "", "")); current.Revision != 3 {
		t.Fatal("rejected owned data claimed an aggregate change")
	}
	other := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Other"}`, "owned-shared-other", ""))
	ownedReply[store.Barcode](t, call(http.MethodPost, "/api/v1/items/"+other.ID+"/barcodes", `{"symbology":"UPC_A","raw_value":"036000291452"}`, "owned-shared-product", `"1"`), other.ID, 6)
	first := ownedPageReply(t, call(http.MethodGet, base+"/barcodes?limit=1", "", "", ""))
	if len(first.Data) != 1 || first.NextCursor != nil {
		t.Fatal("rejected barcode command left a partial source row")
	}
}

func TestCatalogLinksHTTPRevalidatesSessionAndProofAfterBodyReading(t *testing.T) {
	for _, state := range []string{"revoked", "owner changed"} {
		t.Run(state, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			item := catalogReply(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/items", `{"name":"Owner"}`, cookie, csrf, "owned-late-item", "")))
			path := "/api/v1/items/" + item.ID + "/aliases"
			r := domainRequest(http.MethodPost, path, `{"alias":"NeverCommitted"}`, cookie, csrf, "owned-late-body", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if state == "revoked" {
					if _, err := auth.Logout(t.Context(), cookie.Value, "owned-late-logout"); err != nil {
						t.Fatal(err)
					}
				} else {
					catalog, err := store.NewCatalog(auth)
					if err != nil {
						t.Fatal(err)
					}
					name := "ConcurrentOwner"
					if _, err := catalog.Patch(t.Context(), cookie.Value, "owned-late-parent", item.ID, 1, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				}
			}})
			status, code := 401, "AUTHENTICATION_REQUIRED"
			if state == "owner changed" {
				status, code = 409, "REVISION_CONFLICT"
			}
			expectError(t, respond(handler, r), status, code)
			if state == "revoked" {
				cookie, csrf = testCredentials(t, auth, changedPassword)
			}
			if page := ownedPageReply(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", ""))); len(page.Data) != 0 {
				t.Fatal("middleware-only validation allowed a late unauthorized/stale write")
			}
		})
	}
}

func TestCatalogLinksHTTPDefaultPageSizeAndOwnerCollectionCursorBinding(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	catalog, _ := store.NewCatalog(auth)
	item, err := catalog.Create(t.Context(), cookie.Value, "owned-default-page-item", store.ItemCreate{Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	for n := range 51 {
		if _, err := catalog.CreateAlias(t.Context(), cookie.Value, fmt.Sprintf("owned-default-alias-%d", n), item.ID, int64(n+1), fmt.Sprintf("Alias%d", n)); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/items/" + item.ID + "/aliases"
	first := ownedPageReply(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")))
	if len(first.Data) != 50 || first.NextCursor == nil {
		t.Fatal("owned HTTP page changed the documented default 50")
	}
	cursor := url.QueryEscape(*first.NextCursor)
	last := ownedPageReply(t, respond(handler, domainRequest(http.MethodGet, path+"?cursor="+cursor, "", cookie, csrf, "", "")))
	if len(last.Data) != 1 || last.NextCursor != nil {
		t.Fatal("default-sized continuation truncated legacy/large collection metadata")
	}
	for _, target := range []string{"/api/v1/items/other/aliases", "/api/v1/items/" + item.ID + "/barcodes", "/api/v1/tags"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target+"?limit=50&cursor="+cursor, "", cookie, csrf, "", "")), 400, "INVALID_CURSOR")
	}
}
