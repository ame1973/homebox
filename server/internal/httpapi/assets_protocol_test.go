package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"homebox/internal/store"
)

func assetOperationsTest() []struct{ method, path, body string } {
	return []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/assets", ""},
		{http.MethodGet, "/api/v1/units/unit/asset", ""},
		{http.MethodPut, "/api/v1/units/unit/asset", assetJSONBodies[0].body},
		{http.MethodGet, "/api/v1/units/unit/usage", ""},
		{http.MethodPost, "/api/v1/units/unit/usage", assetJSONBodies[1].body},
		{http.MethodGet, "/api/v1/units/unit/costs", ""},
		{http.MethodPost, "/api/v1/units/unit/costs", assetJSONBodies[2].body},
		{http.MethodPost, "/api/v1/units/unit/retire", assetJSONBodies[3].body},
		{http.MethodGet, "/api/v1/units/unit/disposal", ""},
		{http.MethodPost, "/api/v1/units/unit/disposal", assetJSONBodies[4].body},
		{http.MethodGet, "/api/v1/units/unit/cost-summary", ""},
	}
}

func TestAssetsHTTPAllOperationsRequireCompletedSessionExactOriginHostAndTransport(t *testing.T) {
	for _, state := range []string{"anonymous", "mandatory", "revoked", "wrong origin", "wrong host", "insecure"} {
		t.Run(state, func(t *testing.T) {
			auth := newTestAuth(t)
			setupAccount(t, auth, state != "mandatory")
			password := changedPassword
			if state == "mandatory" {
				password = initialPassword
			}
			cookie, csrf := testCredentials(t, auth, password)
			handler := authHandler(t, auth)
			status, code := 401, "AUTHENTICATION_REQUIRED"
			switch state {
			case "anonymous":
				cookie = nil
			case "mandatory":
				status, code = 403, "PASSWORD_CHANGE_REQUIRED"
			case "revoked":
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-asset-boundary"); err != nil {
					t.Fatal(err)
				}
			default:
				status, code = 403, "ORIGIN_REJECTED"
			}
			for _, operation := range assetOperationsTest() {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "asset-auth-boundary", `"1"`)
				switch state {
				case "wrong origin":
					r.Header.Set("Origin", "https://other.test")
				case "wrong host":
					r.Host = "other.test"
				case "insecure":
					r.TLS = nil
				}
				read := false
				r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() { read = true }})
				expectError(t, respond(handler, r), status, code)
				if read {
					t.Fatal("unauthorized financial operation read its body")
				}
			}
		})
	}
}

func TestAssetsHTTPAllWritersRequireExactWriteHeadersAndStrongOriginalProofs(t *testing.T) {
	for _, operation := range assetOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		t.Run(operation.path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, test := range []struct {
				mutate func(*http.Request)
				status int
				code   string
			}{
				{func(r *http.Request) { r.Header.Del("Origin") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Del("Cookie") }, 401, "AUTHENTICATION_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Add("X-CSRF-Token", csrf) }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "different-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "99.0.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }, 400, "INVALID_REVISION"},
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "asset-header-boundary", `"1"`)
				test.mutate(r)
				read := false
				r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() { read = true }})
				expectError(t, respond(handler, r), test.status, test.code)
				if read {
					t.Fatal("invalid original headers reached financial JSON reading")
				}
			}
			for _, proof := range []string{"1", `"0"`, `"01"`, `W/"1"`, `"1", "2"`, "*", `"-1"`, `"+1"`, `"1.0"`, `"1e0"`, `"9007199254740992"`, `"1" `} {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "asset-invalid-proof", proof)), 400, "INVALID_REVISION")
			}
			expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "asset-missing-proof", "")), 409, "PRECONDITION_REQUIRED")
		})
	}
}

func TestAssetsHTTPMethodsDoNotAddFinancialOverwriteOrDeleteOperations(t *testing.T) {
	for _, route := range []struct{ path, allow string }{
		{"/api/v1/assets", "GET"}, {"/api/v1/units/unit/asset", "GET, PUT"},
		{"/api/v1/units/unit/usage", "GET, POST"}, {"/api/v1/units/unit/costs", "GET, POST"},
		{"/api/v1/units/unit/retire", "POST"}, {"/api/v1/units/unit/disposal", "GET, POST"},
		{"/api/v1/units/unit/cost-summary", "GET"},
	} {
		t.Run(route.path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
				if slices.Contains(strings.Split(route.allow, ", "), method) {
					continue
				}
				reply := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "asset-wrong-method", `"1"`))
				expectError(t, reply, 405, "METHOD_NOT_ALLOWED")
				if reply.Header().Get("Allow") != route.allow {
					t.Fatal("Asset method admission changed", route.path, method)
				}
			}
		})
	}
}

func TestAssetsHTTPPathsQueriesReadBodiesAndImportedIdentitiesRemainExact(t *testing.T) {
	t.Run("paths", func(t *testing.T) {
		_, handler, cookie, csrf := domainHTTPFixture(t)
		for _, path := range []string{"/api/v1/units/./asset", "/api/v1/units/../asset", "/api/v1/units/%75nit/asset", "/api/v1/units/" + strings.Repeat("界", 129) + "/asset", "/api/v1/units/unit/asset?unexpected=1", "/api/v1/units/unit/disposal?"} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_ASSET_URL")
		}
		for _, operation := range assetOperationsTest() {
			if operation.method != http.MethodGet {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path+"?limit=1", operation.body, cookie, csrf, "asset-write-query", `"1"`)), 400, "INVALID_ASSET_URL")
			}
		}
		for _, path := range []string{"/api/v1/assets/", "/api/v1/units/unit/usage/extra", "/api/v1/units//asset", "/api/v1/units/unit/costs/void"} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
		}
		for _, bad := range []string{"bad\\path", "bad\x00path"} {
			r := domainRequest(http.MethodGet, "/api/v1/units/unit/asset", "", cookie, csrf, "", "")
			r.URL.Path = "/api/v1/units/" + bad + "/asset"
			expectError(t, respond(handler, r), 400, "INVALID_ASSET_URL")
		}
		for _, id := range []string{"new", "tree"} {
			for _, tail := range []string{"asset", "usage", "costs", "disposal", "cost-summary"} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/units/"+id+"/"+tail, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
			}
		}
	})
	t.Run("read bodies", func(t *testing.T) {
		_, handler, cookie, csrf := domainHTTPFixture(t)
		for _, operation := range assetOperationsTest() {
			if operation.method != http.MethodGet {
				continue
			}
			for _, unknown := range []bool{false, true} {
				r := domainRequest(operation.method, operation.path, `{}`, cookie, csrf, "", "")
				if unknown {
					r.ContentLength = -1
				}
				expectError(t, respond(handler, r), 400, "INVALID_BODY")
			}
			r := domainRequest(operation.method, operation.path, "", cookie, csrf, "", "")
			r.Header.Set("Content-Encoding", "gzip")
			expectError(t, respond(handler, r), 400, "INVALID_BODY")
		}
	})
}

func TestAssetsHTTPQueriesPreserveAllFiltersDefaultsAndIndependentByteBudgets(t *testing.T) {
	values := url.Values{"from": {"2026-11-05"}, "to": {"2026-11-07"}, "currency": {"TWD"}, "category_id": {"category"}, "location_id": {"location"}, "scope": {"direct"}, "status": {"archived"}, "merchant": {"  Exact é\n  "}, "limit": {"100"}, "cursor": {strings.Repeat("x", 2048)}}
	cursor, size, filters, ok := assetListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/assets?"+values.Encode(), ""), "request")
	if !ok || len(cursor) != 2048 || size != 100 || filters.From != "2026-11-05" || filters.To != "2026-11-07" || filters.Currency != "TWD" || filters.CategoryID != "category" || filters.LocationID != "location" || filters.Scope != "direct" || filters.Status != "archived" || filters.Merchant != "  Exact é\n  " {
		t.Fatal("Asset query dropped or repaired exact filters or adopted Stock cursor width")
	}
	_, size, filters, ok = assetListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/assets", ""), "request")
	if !ok || size != 50 || filters != (store.AssetFilters{}) {
		t.Fatal("Asset list installed undocumented filters or a non-50 default")
	}
	for _, query := range []string{"q=x", "item_id=item", "limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 2049), "merchant=", "scope=", "from=2026", "unknown=1", "%zz=x", "a;b=c", "currency=JPY&currency=JPY", "merchant=%FF"} {
		if _, _, _, ok := assetListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/assets?"+query, ""), "request"); ok {
			t.Fatal("invalid/duplicate/uncontracted Asset query admitted", query)
		}
	}
	// Stay below Go's independent 10,000-parameter guard without weakening it.
	base := "merchant=" + url.QueryEscape(strings.Repeat("😀", 300)) + "&category_id=" + url.QueryEscape(strings.Repeat("界", 128)) + "&location_id=" + url.QueryEscape(strings.Repeat("界", 128)) + "&cursor=" + strings.Repeat("x", 2048)
	for _, extra := range []int{0, 1} {
		query := base + strings.Repeat("&", (16<<10)-len(base)+extra)
		if _, _, _, ok := assetListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/assets?"+query, ""), "request"); ok != (extra == 0) {
			t.Fatal("Asset list raw-byte ceiling changed", extra)
		}
	}
	for _, instant := range []string{"", "2026-11-07T12:00:00.123456789+08:00"} {
		path := "/api/v1/units/unit/cost-summary"
		if instant != "" {
			path += "?as_of=" + url.QueryEscape(instant)
		}
		if value, ok := assetSummaryQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, path, ""), "request"); !ok || value != instant {
			t.Fatal("summary query repaired/truncated an exact instant or guessed omitted as_of")
		}
	}
	for _, query := range []string{"as_of=", "as_of=2026-11-07", "as_of=x&as_of=y", "limit=1", "as_of=2026-11-07T00:00:00Z&currency=JPY", "%zz=x", "a;b=c", "as_of=" + strings.Repeat("x", 36), "as_of=" + strings.Repeat("x", 16<<10)} {
		if _, ok := assetSummaryQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/units/unit/cost-summary?"+query, ""), "request"); ok {
			t.Fatal("summary admitted an undocumented/ambiguous/unbounded query", query)
		}
	}
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, path := range []string{"/api/v1/units/unit/usage", "/api/v1/units/unit/costs"} {
		for _, query := range []string{"merchant=M", "currency=JPY", "q=x", "item_id=item", "cursor=" + strings.Repeat("x", 2049)} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, path+"?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
		}
	}
	for _, query := range []string{"from=2026-11-07&to=2026-11-06", "from=2026-02-30", "currency=jpy", "status=stored", "scope=ALL"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/assets?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, instant := range []string{"2026-11-07T00:00:60Z", "2026-11-07T00:00:00.1234567890Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/units/unit/cost-summary?as_of="+url.QueryEscape(instant), "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
}

func TestAssetsHTTPInvalidBodiesInstantsAndCancellationNeverReleaseResources(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, operation := range assetOperationsTest() {
		if operation.method != http.MethodGet {
			expectError(t, respond(handler, domainRequest(operation.method, operation.path, `{}`, cookie, csrf, "asset-invalid-body", `"1"`)), 422, "VALIDATION_ERROR")
			ctx, cancel := context.WithCancel(t.Context())
			r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "asset-canceled-body", `"1"`).WithContext(ctx)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: cancel})
			response := respond(handler, r)
			cancel()
			if response.Code == 200 || response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), `"revision"`) {
				t.Fatal("canceled financial write returned a resource")
			}
			continue
		}
		ctx, cancel := context.WithCancel(t.Context())
		r := domainRequest(operation.method, operation.path, "", cookie, csrf, "", "").WithContext(ctx)
		r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: cancel})
		response := respond(handler, r)
		cancel()
		if response.Code == 200 || response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), `"data"`) {
			t.Fatal("canceled financial read returned success-shaped source")
		}
	}
	for index, test := range assetJSONBodies {
		for _, instant := range []string{"2026-11-07T00:00:60Z", "2026-11-07T00:00:00.1234567890Z"} {
			field := "occurred_at"
			if index == 0 {
				field = "in_service_at"
			} else if index == 3 {
				field = "retired_at"
			} else if index == 4 {
				field = "disposed_at"
			}
			body := assetJSONMutation(t, test.body, field, fmt.Sprintf("%q", instant))
			tail, method := test.kind, http.MethodPost
			if index == 0 {
				tail, method = "asset", http.MethodPut
			} else if index == 2 {
				tail = "costs"
			}
			expectError(t, respond(handler, domainRequest(method, "/api/v1/units/unit/"+tail, body, cookie, csrf, "asset-invalid-instant", `"1"`)), 422, "VALIDATION_ERROR")
		}
	}
}

func TestAssetErrorsAreStaticSafeAndDoNotChangeCookiesOrResourceETags(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"}, {store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"},
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"}, {store.ErrInvalidCursor, 400, "INVALID_CURSOR"},
		{store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"}, {store.ErrRevisionConflict, 409, "REVISION_CONFLICT"}, {store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"},
		{store.ErrAssetCurrency, 409, "ASSET_CURRENCY_CONFLICT"}, {store.ErrAssetOverflow, 409, "ASSET_TOTAL_OVERFLOW"}, {store.ErrAssetHistory, 409, "ASSET_HISTORY_LOCKED"},
		{store.ErrUnitAssetLifecycle, 409, "ASSET_LIFECYCLE_REQUIRED"}, {store.ErrUnitDisposed, 409, "UNIT_DISPOSED"}, {store.ErrUnitTracking, 409, "TRACKING_MODE_CONFLICT"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"}, {errors.New("private /data/homebox.sqlite password=secret"), 503, "SERVICE_UNAVAILABLE"},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		writeAssetError(w, fmt.Errorf("private wrapper: %w", test.err), "request")
		expectError(t, w, test.status, test.code)
		if strings.Contains(w.Body.String(), "private wrapper") || strings.Contains(w.Body.String(), "/data/homebox.sqlite") || strings.Contains(w.Body.String(), "password=") || w.Header().Get("Set-Cookie") != "" || w.Header().Get("ETag") != "" {
			t.Fatal("financial error exposed diagnostics or changed resource/session state")
		}
	}
}
