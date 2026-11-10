package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"homebox/internal/store"
)

type holdingOperationTest struct{ method, path, body string }

func holdingOperationsTest() []holdingOperationTest {
	return []holdingOperationTest{
		{http.MethodGet, "/api/v1/items/item/units", ""}, {http.MethodPost, "/api/v1/items/item/units", `{"location_id":null}`},
		{http.MethodGet, "/api/v1/units/unit", ""}, {http.MethodGet, "/api/v1/units?id=unit", ""}, {http.MethodPatch, "/api/v1/units/unit", `{"notes":"Changed"}`}, {http.MethodDelete, "/api/v1/units/unit", ""},
		{http.MethodPost, "/api/v1/units/unit/move", `{"target_location_id":"target"}`}, {http.MethodPost, "/api/v1/units/unit/status", `{"status":"lost"}`},
		{http.MethodGet, "/api/v1/inventory", ""}, {http.MethodPost, "/api/v1/stock/receive", `{"item_id":"item","target_location_id":"target","quantity_base":1}`},
		{http.MethodPost, "/api/v1/stock/consume", `{"item_id":"item","source_location_id":"source","quantity_base":1}`},
		{http.MethodPost, "/api/v1/stock/transfer", `{"item_id":"item","source_location_id":"source","target_location_id":"target","quantity_base":1}`},
		{http.MethodPost, "/api/v1/stock/adjust", `{"item_id":"item","target_location_id":"target","quantity_base":0,"reason":"Counted"}`},
	}
}

func TestHoldingsHTTPAllOperationsRequireCompletedSessionsAndExactTransport(t *testing.T) {
	for _, state := range []string{"anonymous", "mandatory", "revoked", "wrong origin", "wrong host", "insecure transport"} {
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
			if state == "anonymous" {
				cookie = nil
			} else if state == "mandatory" {
				status, code = 403, "PASSWORD_CHANGE_REQUIRED"
			} else if state == "revoked" {
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-holding-boundary"); err != nil {
					t.Fatal(err)
				}
			} else {
				status, code = 403, "ORIGIN_REJECTED"
			}
			for _, operation := range holdingOperationsTest() {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "holding-auth-boundary", `"1"`)
				switch state {
				case "wrong origin":
					r.Header.Set("Origin", "https://other.test")
				case "wrong host":
					r.Host = "other.test"
				case "insecure transport":
					r.TLS = nil
				}
				expectError(t, respond(handler, r), status, code)
			}
		})
	}
}

func TestHoldingsHTTPAllWritesRequireOriginalWriteHeadersAndStrongProofs(t *testing.T) {
	for _, operation := range holdingOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		t.Run(operation.method+operation.path, func(t *testing.T) {
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
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "99.0.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("If-Match") }, 409, "PRECONDITION_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }, 400, "INVALID_REVISION"},
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "holding-header-boundary", `"1"`)
				test.mutate(r)
				expectError(t, respond(handler, r), test.status, test.code)
			}
			for _, proof := range []string{"1", `"0"`, `"01"`, `W/"1"`, `"1", "2"`, "*", `"-1"`, `"+1"`, `"1.0"`, `"1e0"`, `"9007199254740992"`, `"1" `} {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "holding-bad-original-proof", proof)), 400, "INVALID_REVISION")
			}
		})
	}
}

func TestHoldingsHTTPMethodsPathsBodylessReadsAndAbsentRoutesRemainExact(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, route := range []struct {
		path, allow string
		methods     []string
	}{
		{"/api/v1/items/item/units", "GET, POST", []string{http.MethodPut, http.MethodDelete, http.MethodHead}},
		{"/api/v1/units/unit", "GET, PATCH, DELETE", []string{http.MethodPost, http.MethodPut, http.MethodHead}},
		{"/api/v1/units/unit/move", "POST", []string{http.MethodGet, http.MethodPatch}},
		{"/api/v1/units/unit/status", "POST", []string{http.MethodGet, http.MethodDelete}},
		{"/api/v1/inventory", "GET", []string{http.MethodPost, http.MethodHead, http.MethodOptions}},
		{"/api/v1/stock/receive", "POST", []string{http.MethodGet, http.MethodPut}},
		{"/api/v1/stock/consume", "POST", []string{http.MethodGet, http.MethodDelete}},
		{"/api/v1/stock/transfer", "POST", []string{http.MethodGet, http.MethodPatch}},
		{"/api/v1/stock/adjust", "POST", []string{http.MethodGet, http.MethodOptions}},
	} {
		for _, method := range route.methods {
			response := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "holding-wrong-method", `"1"`))
			expectError(t, response, 405, "METHOD_NOT_ALLOWED")
			if response.Header().Get("Allow") != route.allow {
				t.Fatal("holding route changed its exact method boundary")
			}
		}
	}
	for _, path := range []string{"/api/v1/units/.", "/api/v1/units/..", "/api/v1/units/%75nit", "/api/v1/units/" + strings.Repeat("界", 129), "/api/v1/units/unit?unexpected=1", "/api/v1/items/item/units/extra", "/api/v1/stock/unknown", "/api/v1/units/unit/qr"} {
		status, code := 400, "INVALID_HOLDING_URL"
		if strings.HasSuffix(path, "extra") || strings.HasSuffix(path, "unknown") || strings.HasSuffix(path, "/qr") {
			status, code = 404, "NOT_FOUND"
		}
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), status, code)
	}
	for _, id := range []string{"bad\\path", "bad\x00path"} {
		r := domainRequest(http.MethodGet, "/api/v1/units/unit", "", cookie, csrf, "", "")
		r.URL.Path = "/api/v1/units/" + id
		expectError(t, respond(handler, r), 400, "INVALID_HOLDING_URL")
	}
	for _, operation := range []holdingOperationTest{{http.MethodGet, "/api/v1/inventory", ""}, {http.MethodGet, "/api/v1/items/item/units", ""}, {http.MethodGet, "/api/v1/units/unit", ""}, {http.MethodDelete, "/api/v1/units/unit", ""}} {
		for _, unknown := range []bool{false, true} {
			r := domainRequest(operation.method, operation.path, `{}`, cookie, csrf, "holding-forbidden-body", `"1"`)
			if unknown {
				r.ContentLength = -1
			}
			expectError(t, respond(handler, r), 400, "INVALID_BODY")
		}
		r := domainRequest(operation.method, operation.path, "", cookie, csrf, "holding-forbidden-encoding", `"1"`)
		r.Header.Set("Content-Encoding", "gzip")
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	for _, id := range []string{"new", "tree"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/units/"+id, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
}

func TestHoldingsHTTPRejectsBadBodyAtEveryWriterAndCancelledReadsNeverReturnData(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, operation := range holdingOperationsTest() {
		if operation.method == http.MethodGet || operation.method == http.MethodDelete {
			continue
		}
		expectError(t, respond(handler, domainRequest(operation.method, operation.path, `{}`, cookie, csrf, "holding-invalid-json", `"1"`)), 422, "VALIDATION_ERROR")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := domainRequest(http.MethodGet, "/api/v1/inventory", "", cookie, csrf, "", "").WithContext(ctx)
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: cancel})
	response := respond(handler, r)
	if response.Code == 200 || strings.Contains(response.Body.String(), `"data"`) || response.Header().Get("ETag") != "" {
		t.Fatal("cancelled inventory read returned success-shaped data")
	}
}

func TestHoldingErrorsAreStaticAllowlistedAndNeverExposeStorageOrChangeCookies(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"},
		{store.ErrInvalidCursor, 400, "INVALID_CURSOR"}, {store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"}, {store.ErrRevisionConflict, 409, "REVISION_CONFLICT"}, {store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"},
		{store.ErrLocationArchived, 409, "LOCATION_ARCHIVED"}, {store.ErrStockTracking, 409, "TRACKING_MODE_CONFLICT"}, {store.ErrUnitTracking, 409, "TRACKING_MODE_CONFLICT"},
		{store.ErrStockInsufficient, 409, "INSUFFICIENT_STOCK"}, {store.ErrStockOverflow, 409, "QUANTITY_OVERFLOW"}, {store.ErrUnitHistory, 409, "UNIT_HAS_HISTORY"},
		{store.ErrUnitDisposed, 409, "UNIT_DISPOSED"}, {store.ErrUnitAssetLifecycle, 409, "ASSET_LIFECYCLE_REQUIRED"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"}, {errors.New("private /data/homebox.sqlite password=secret"), 503, "SERVICE_UNAVAILABLE"},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "safe-request")
		writeHoldingError(w, fmt.Errorf("private wrapper: %w", test.err), "safe-request")
		expectError(t, w, test.status, test.code)
		if strings.Contains(w.Body.String(), "private wrapper") || strings.Contains(w.Body.String(), "/data/homebox.sqlite") || strings.Contains(w.Body.String(), "password=") || w.Header().Get("Set-Cookie") != "" || w.Header().Get("ETag") != "" {
			t.Fatal("holding error leaked private diagnostics or changed resource/session state")
		}
	}
}

func TestHoldingStockQueryBudgetsPreserveEveryExactFieldAndIndependentCursorWidth(t *testing.T) {
	values := url.Values{"item_id": {"物品 +?# é"}, "from": {"2026-11-05"}, "to": {"2026-11-06"}, "currency": {"TWD"}, "category_id": {"category"}, "location_id": {"location"}, "scope": {"direct"}, "status": {"archived"}, "merchant": {"  Exact é  "}, "limit": {"100"}, "cursor": {strings.Repeat("x", 4096)}}
	cursor, size, filters, ok := stockListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/inventory?"+values.Encode(), ""), "request")
	if !ok || filters.ItemID != "物品 +?# é" || len(cursor) != 4096 || size != 100 || filters.Merchant != "  Exact é  " || filters.From != "2026-11-05" || filters.To != "2026-11-06" || filters.Currency != "TWD" || filters.CategoryID != "category" || filters.LocationID != "location" || filters.Scope != "direct" || filters.Status != "archived" {
		t.Fatal("inventory query lost exact filters, page size or its composite budget")
	}
	for _, id := range []string{"new", "tree", strings.Repeat("😀", 128), strings.Repeat("\x00", 128)} {
		_, _, selected, ok := stockListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/inventory?item_id="+url.QueryEscape(id), ""), "request")
		if !ok || selected.ItemID != id {
			t.Fatal("Item query repaired or rejected a full retained identity")
		}
	}
	if cursor, size, _, ok := stockListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/inventory", ""), "request"); !ok || cursor != "" || size != 50 {
		t.Fatal("inventory default live page changed")
	}
	for _, query := range []string{"q=uncontracted", "unknown=1", "limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 4097), "merchant=", "category_id=", "item_id=", "item_id=x&item_id=y", "item_id=%ff", "item_id=" + url.QueryEscape(strings.Repeat("😀", 129)), "from=2026", "to=%ff", "currency=TW", "scope=invalid-value", "%zz=1", "cursor=" + strings.Repeat("x", 16<<10)} {
		w := httptest.NewRecorder()
		if _, _, _, ok := stockListQuery(w, apiRequest(http.MethodGet, "/api/v1/inventory?"+query, ""), "request"); ok || w.Code != 400 {
			t.Fatal("invalid query escaped inventory admission", query, w.Code)
		}
	}
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, query := range []string{"status=stored", "scope=unknown", "from=2026-02-30", "from=2026-11-06&to=2026-11-05", "currency=twd"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/inventory?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, route := range []string{"/api/v1/inventory?cursor=" + strings.Repeat("x", 3000), "/api/v1/items/item/units?cursor=" + strings.Repeat("x", 2049)} {
		code := "INVALID_CURSOR"
		if strings.Contains(route, "/units?") {
			code = "INVALID_QUERY"
		}
		expectError(t, respond(handler, domainRequest(http.MethodGet, route, "", cookie, csrf, "", "")), 400, code)
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(respond(handler, domainRequest(http.MethodGet, "/api/v1/inventory", "", cookie, csrf, "", "")).Body.Bytes(), &data) != nil || data["next_cursor"] == nil {
		t.Fatal("live Stock response omitted required nullable continuation")
	}
}
