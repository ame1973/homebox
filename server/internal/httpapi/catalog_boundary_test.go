package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestCatalogListQueryForwardsEveryExactBoundedFilterAndDefaultPageSize(t *testing.T) {
	query := url.Values{
		"from": {"2026-01-01"}, "to": {"2026-12-31"}, "currency": {"TWD"}, "category_id": {"category:<&"},
		"location_id": {"location:😀"}, "scope": {"direct"}, "status": {"archived"}, "merchant": {" Exact Merchant "}, "q": {"  濾芯  "},
		"limit": {"12"}, "cursor": {"opaque_-/cursor"},
	}
	r := apiRequest(http.MethodGet, "/api/v1/items?"+query.Encode(), "")
	cursor, limit, filters, ok := catalogListQuery(httptest.NewRecorder(), r, "request")
	want := store.ItemFilters{From: "2026-01-01", To: "2026-12-31", Currency: "TWD", CategoryID: "category:<&", LocationID: "location:😀",
		Scope: "direct", Status: "archived", Merchant: " Exact Merchant ", Query: "  濾芯  "}
	if !ok || cursor != "opaque_-/cursor" || limit != 12 || !reflect.DeepEqual(filters, want) {
		t.Fatal("query parser normalized/dropped a documented raw filter")
	}
	cursor, limit, filters, ok = catalogListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/items", ""), "request")
	if !ok || cursor != "" || limit != 50 || filters != (store.ItemFilters{}) {
		t.Fatal("catalog query defaults changed")
	}
	query.Set("category_id", strings.Repeat("😀", 128))
	query.Set("location_id", strings.Repeat("😀", 128))
	query.Set("merchant", strings.Repeat("😀", 300))
	query.Set("q", strings.Repeat("😀", 500))
	query.Set("cursor", strings.Repeat("x", 2048))
	r = apiRequest(http.MethodGet, "/api/v1/items?"+query.Encode(), "")
	if len(r.URL.RawQuery) > 16<<10 {
		t.Fatal("maximum valid query fixture exceeded its documented raw budget")
	}
	if _, _, _, ok := catalogListQuery(httptest.NewRecorder(), r, "request"); !ok {
		t.Fatal("percent-encoded Unicode maxima were rejected by an undersized query cap")
	}
}

func TestCatalogHTTPAllOperationsRequireCompletedSessionAndExactTransport(t *testing.T) {
	operations := []struct{ method, path, body, revision string }{
		{http.MethodGet, "/api/v1/items", "", ""}, {http.MethodGet, "/api/v1/items/id", "", ""},
		{http.MethodPost, "/api/v1/items", `{"name":"Item"}`, ""}, {http.MethodPatch, "/api/v1/items/id", `{"name":"Item"}`, `"1"`},
		{http.MethodPost, "/api/v1/items/id/archive", `{}`, `"1"`}, {http.MethodDelete, "/api/v1/items/id", "", `"1"`},
	}
	for _, operation := range operations {
		t.Run(operation.method+operation.path, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, nil, csrf, "no-item-session", operation.revision)), 401, "AUTHENTICATION_REQUIRED")
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") },
				func(r *http.Request) { r.Host = "foreign.test" },
				func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "wrong-item-transport", operation.revision)
				mutate(r)
				expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
			}
			if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-item-session"); err != nil {
				t.Fatal(err)
			}
			expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "revoked-item-session", operation.revision)), 401, "AUTHENTICATION_REQUIRED")
		})
	}
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	handler := authHandler(t, auth)
	for _, operation := range operations {
		expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "mandatory-item-session", operation.revision)), 403, "PASSWORD_CHANGE_REQUIRED")
	}
}

func TestCatalogHTTPWritesKeepCSRFKeyVersionOriginAndOriginalProofRequirements(t *testing.T) {
	for _, operation := range []struct{ method, path, body, revision string }{
		{http.MethodPost, "/api/v1/items", `{"name":"Item"}`, ""},
		{http.MethodPatch, "/api/v1/items/id", `{"name":"Item"}`, `"1"`},
		{http.MethodPost, "/api/v1/items/id/archive", `{}`, `"1"`},
		{http.MethodDelete, "/api/v1/items/id", "", `"1"`},
	} {
		t.Run(operation.method+operation.path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, invalid := range []struct {
				mutate func(*http.Request)
				status int
				code   string
			}{
				{func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Add("X-CSRF-Token", csrf) }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.9.3") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("Origin") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Set("Origin", "null") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.AddCookie(cookie) }, 401, "AUTHENTICATION_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("If-Match", `W/"1"`) }, 400, "INVALID_REVISION"},
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "invalid-item-write", operation.revision)
				invalid.mutate(r)
				expectError(t, respond(handler, r), invalid.status, invalid.code)
			}
			if operation.revision != "" {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "missing-item-proof", "")), 409, "PRECONDITION_REQUIRED")
				for _, proof := range []string{`"0"`, `"01"`, `*`, `"1", "2"`, `"9007199254740992"`} {
					expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "invalid-item-proof", proof)), 400, "INVALID_REVISION")
				}
			}
		})
	}
}

func TestCatalogHTTPMethodsExactPathsBodiesAndUnimplementedRoutesAreNotFaked(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, route := range []struct {
		path, allow string
		methods     []string
	}{
		{"/api/v1/items", "GET, POST", []string{http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodOptions}},
		{"/api/v1/items/target", "GET, PATCH, DELETE", []string{http.MethodPost, http.MethodPut, http.MethodHead, http.MethodOptions}},
		{"/api/v1/items/target/archive", "POST", []string{http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodOptions}},
	} {
		for _, method := range route.methods {
			response := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "unsupported-item-method", `"1"`))
			expectError(t, response, 405, "METHOD_NOT_ALLOWED")
			if response.Header().Get("Allow") != route.allow {
				t.Fatal("catalog route omitted its exact Allow boundary")
			}
		}
	}
	for _, path := range []string{"/api/v1/it%65ms", "/api/v1/items/.", "/api/v1/items/..", "/api/v1/items/%74ree", "/api/v1/items/target?unexpected=1", "/api/v1/items/" + strings.Repeat("x", 129)} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_CATALOG_URL")
	}
	for _, id := range []string{"bad\\path", "bad\x00path"} {
		r := domainRequest(http.MethodGet, "/api/v1/items/target", "", cookie, csrf, "", "")
		r.URL.Path = "/api/v1/items/" + id
		expectError(t, respond(handler, r), 400, "INVALID_CATALOG_URL")
	}
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/api/v1/items"}, {http.MethodGet, "/api/v1/items/target"}, {http.MethodDelete, "/api/v1/items/target"}} {
		for _, unknownLength := range []bool{false, true} {
			r := domainRequest(route.method, route.path, `{}`, cookie, csrf, "body-forbidden-item", `"1"`)
			if unknownLength {
				r.ContentLength = -1
			}
			expectError(t, respond(handler, r), 400, "INVALID_BODY")
		}
		r := domainRequest(route.method, route.path, "", cookie, csrf, "encoding-forbidden-item", `"1"`)
		r.Header.Set("Content-Encoding", "gzip")
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	for _, suffix := range []string{"units", "barcodes", "aliases", "attributes", "tags", "unexpected"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items/target/"+suffix, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
	for _, id := range []string{"new", "tree"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items/"+id, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
}

func TestCatalogHTTPRejectsBadJSONAtEveryWriterAndWithholdsCancelledReadData(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, operation := range []struct{ method, path, body, revision string }{
		{http.MethodPost, "/api/v1/items", `{}`, ""},
		{http.MethodPatch, "/api/v1/items/target", `{}`, `"1"`},
		{http.MethodPost, "/api/v1/items/target/archive", `{"reason":null}`, `"1"`},
	} {
		expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "invalid-catalog-json", operation.revision)), 422, "VALIDATION_ERROR")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := domainRequest(http.MethodGet, "/api/v1/items", "", cookie, csrf, "", "").WithContext(ctx)
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: cancel})
	response := respond(handler, r)
	expectError(t, response, 503, "SERVICE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), `"data"`) || strings.Contains(response.Body.String(), `"next_cursor"`) {
		t.Fatal("cancelled catalog read exposed a successful-looking page")
	}
	expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/not-a-catalog-route", "", cookie, csrf, "", "")), 404, "NOT_FOUND")
}

func TestCatalogHTTPUsesTheExistingUnmodifiedSharedGeneralBudget(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for index := range 60 {
		path := "/api/v1/items"
		if index%2 == 0 {
			path = "/api/v1/categories"
		}
		if response := respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")); response.Code != 200 {
			t.Fatal("catalog did not share the normal household read budget", response.Code)
		}
	}
	for index, path := range []string{"/api/v1/items", "/api/v1/tags", "/api/v1/locations"} {
		response := respond(handler, domainRequest(http.MethodPost, path, `{"name":"Limited"}`, cookie, csrf, fmt.Sprintf("limited-item-write-%d", index), ""))
		expectError(t, response, 429, "RATE_LIMITED")
		if response.Header().Get("Retry-After") == "" {
			t.Fatal("limited catalog reply omitted Retry-After")
		}
	}
}
