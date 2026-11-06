package httpapi

import (
	"context"
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

type purchaseOperationTest struct{ method, path, body string }

func purchaseOperationsTest() []purchaseOperationTest {
	return []purchaseOperationTest{
		{http.MethodGet, "/api/v1/purchases", ""}, {http.MethodPost, "/api/v1/purchases", `{"merchant_name":"M","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`},
		{http.MethodGet, "/api/v1/purchases/purchase", ""}, {http.MethodPatch, "/api/v1/purchases/purchase", `{"receipt_note":"Reviewed"}`},
		{http.MethodGet, "/api/v1/items/item/purchases", ""}, {http.MethodGet, "/api/v1/purchases/purchase/lines", ""},
		{http.MethodPost, "/api/v1/purchases/purchase/lines", `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`},
	}
}

func TestPurchasesHTTPAllOperationsRequireCompletedSessionExactOriginHostAndTransport(t *testing.T) {
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
			if state == "anonymous" {
				cookie = nil
			} else if state == "mandatory" {
				status, code = 403, "PASSWORD_CHANGE_REQUIRED"
			} else if state == "revoked" {
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-purchase-boundary"); err != nil {
					t.Fatal(err)
				}
			} else {
				status, code = 403, "ORIGIN_REJECTED"
			}
			for _, operation := range purchaseOperationsTest() {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "purchase-auth-boundary", `"1"`)
				switch state {
				case "wrong origin":
					r.Header.Set("Origin", "https://other.test")
				case "wrong host":
					r.Host = "other.test"
				case "insecure":
					r.TLS = nil
				}
				expectError(t, respond(handler, r), status, code)
			}
		})
	}
}

func TestPurchasesHTTPAllWritesRequireOriginalHeadersAndCorrectParentProofScope(t *testing.T) {
	for _, operation := range purchaseOperationsTest() {
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
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "different-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "99.0.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }, 400, "INVALID_REVISION"},
			} {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "purchase-header-boundary", `"1"`)
				test.mutate(r)
				expectError(t, respond(handler, r), test.status, test.code)
			}
			for _, proof := range []string{"1", `"0"`, `"01"`, `W/"1"`, `"1", "2"`, "*", `"-1"`, `"+1"`, `"1.0"`, `"1e0"`, `"9007199254740992"`, `"1" `} {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "purchase-bad-proof", proof)), 400, "INVALID_REVISION")
			}
			if operation.path == "/api/v1/purchases" {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "purchase-unrelated-proof", `"1"`)), 400, "INVALID_REVISION")
			} else {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "purchase-missing-proof", "")), 409, "PRECONDITION_REQUIRED")
			}
		})
	}
}

func TestPurchasesHTTPMethodsPathsReadBodiesAndMissingEndpointsRemainExact(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, route := range []struct {
		path, allow string
		methods     []string
	}{
		{"/api/v1/purchases", "GET, POST", []string{http.MethodPut, http.MethodDelete, http.MethodHead}},
		{"/api/v1/purchases/purchase", "GET, PATCH", []string{http.MethodPost, http.MethodDelete, http.MethodHead}},
		{"/api/v1/items/item/purchases", "GET", []string{http.MethodPost, http.MethodPatch, http.MethodHead}},
		{"/api/v1/purchases/purchase/lines", "GET, POST", []string{http.MethodPatch, http.MethodDelete, http.MethodHead}},
	} {
		for _, method := range route.methods {
			reply := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "purchase-wrong-method", `"1"`))
			expectError(t, reply, 405, "METHOD_NOT_ALLOWED")
			if reply.Header().Get("Allow") != route.allow {
				t.Fatal("purchase method admission changed")
			}
		}
	}
	for _, path := range []string{"/api/v1/purchases/.", "/api/v1/purchases/..", "/api/v1/purchases/%70urchase", "/api/v1/purchases/" + strings.Repeat("界", 129), "/api/v1/purchases/purchase?unexpected=1", "/api/v1/purchases/purchase?"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 400, "INVALID_PURCHASE_URL")
	}
	for _, path := range []string{"/api/v1/purchases/", "/api/v1/purchases/purchase/lines/extra", "/api/v1/items/item/purchases/extra", "/api/v1/purchases/purchase/adjust"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
	for _, bad := range []string{"bad\\path", "bad\x00path"} {
		r := domainRequest(http.MethodGet, "/api/v1/purchases/purchase", "", cookie, csrf, "", "")
		r.URL.Path = "/api/v1/purchases/" + bad
		expectError(t, respond(handler, r), 400, "INVALID_PURCHASE_URL")
	}
	for _, operation := range purchaseOperationsTest() {
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
	for _, id := range []string{"new", "tree"} {
		for _, path := range []string{"/api/v1/purchases/" + id, "/api/v1/items/" + id + "/purchases", "/api/v1/purchases/" + id + "/lines"} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
		}
	}
}

func TestPurchasesHTTPEveryWriterRechecksSQLAuthorizationAfterBodyRead(t *testing.T) {
	for _, kind := range []string{"create", "patch", "line"} {
		t.Run(kind, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			p, err := store.NewPurchases(auth)
			if err != nil {
				t.Fatal(err)
			}
			parent, err := p.Create(t.Context(), cookie.Value, "body-read-parent", store.PurchaseCreate{MerchantName: "Before", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := store.NewCatalog(auth)
			if err != nil {
				t.Fatal(err)
			}
			item, err := catalog.Create(t.Context(), cookie.Value, "body-read-item", store.ItemCreate{Name: "Before"})
			if err != nil {
				t.Fatal(err)
			}
			method, path, body, proof := http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Never committed","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`, ""
			if kind == "patch" {
				method, path, body, proof = http.MethodPatch, "/api/v1/purchases/"+parent.ID, `{"receipt_note":"Never committed"}`, `"1"`
			}
			if kind == "line" {
				path, body, proof = "/api/v1/purchases/"+parent.ID+"/lines", fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, item.ID), `"1"`
			}
			r := domainRequest(method, path, body, cookie, csrf, "purchase-after-body-key", proof)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-purchase-during-body"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
			fresh, _ := testCredentials(t, auth, changedPassword)
			if current, err := p.Get(t.Context(), fresh.Value, parent.ID); err != nil || current != parent {
				t.Fatal("body-time revocation changed source", err)
			}
			if headers, err := p.Page(t.Context(), fresh.Value, "", 50, store.PurchaseFilters{}); err != nil || len(headers.Data) != 1 {
				t.Fatal("body-time revocation created a partial header", err)
			}
			if lines, err := p.PageLines(t.Context(), fresh.Value, parent.ID, "", 50); err != nil || len(lines.Data) != 0 {
				t.Fatal("body-time revocation appended financial history", err)
			}
		})
	}
}

func TestPurchasesHTTPInvalidFieldsRealInstantValidationAndCancellationNeverReturnResources(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, operation := range purchaseOperationsTest() {
		if operation.method == http.MethodGet {
			continue
		}
		proof := `"1"`
		if operation.path == "/api/v1/purchases" {
			proof = ""
		}
		expectError(t, respond(handler, domainRequest(operation.method, operation.path, `{}`, cookie, csrf, "purchase-invalid-body", proof)), 422, "VALIDATION_ERROR")
	}
	for _, instant := range []string{"2026-11-06T00:00:60Z", "2026-11-06T00:00:00.1234567890Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01"} {
		body := fmt.Sprintf(`{"merchant_name":"M","purchased_at":%q,"currency":"JPY"}`, instant)
		expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/purchases", body, cookie, csrf, "purchase-invalid-instant", "")), 422, "VALIDATION_ERROR")
	}
	for _, path := range []string{"/api/v1/purchases", "/api/v1/purchases/purchase", "/api/v1/items/item/purchases", "/api/v1/purchases/purchase/lines"} {
		ctx, cancel := context.WithCancel(t.Context())
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "").WithContext(ctx)
		r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: cancel})
		response := respond(handler, r)
		cancel()
		if response.Code == 200 || strings.Contains(response.Body.String(), `"data"`) || response.Header().Get("ETag") != "" {
			t.Fatal("canceled read exposed success-shaped source")
		}
	}
}

func TestPurchaseErrorsAreStaticSafeAndNeverChangeCookiesOrETags(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"}, {store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"},
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"}, {store.ErrInvalidCursor, 400, "INVALID_CURSOR"},
		{store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"}, {store.ErrRevisionConflict, 409, "REVISION_CONFLICT"}, {store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"},
		{store.ErrPurchaseCurrencyLocked, 409, "PURCHASE_CURRENCY_LOCKED"}, {store.ErrPurchaseCurrency, 409, "PURCHASE_CURRENCY_CONFLICT"}, {store.ErrPurchaseAssetBasis, 409, "ASSET_COST_BASIS_CONFLICT"},
		{store.ErrPurchaseAssetLifecycle, 409, "ASSET_LIFECYCLE_REQUIRED"}, {store.ErrUnitTracking, 409, "TRACKING_MODE_CONFLICT"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"}, {errors.New("private /data/homebox.sqlite password=secret"), 503, "SERVICE_UNAVAILABLE"},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		writePurchaseError(w, fmt.Errorf("private wrapper: %w", test.err), "request")
		expectError(t, w, test.status, test.code)
		if strings.Contains(w.Body.String(), "private wrapper") || strings.Contains(w.Body.String(), "/data/homebox.sqlite") || strings.Contains(w.Body.String(), "password=") || w.Header().Get("Set-Cookie") != "" || w.Header().Get("ETag") != "" {
			t.Fatal("purchase error exposed source diagnostics or changed resource/session state")
		}
	}
}

func TestPurchasesHTTPQueriesRetainExactFieldsAndIndependentDefaultBudgets(t *testing.T) {
	values := url.Values{"from": {"2026-11-05"}, "to": {"2026-11-06"}, "currency": {"TWD"}, "category_id": {"category"}, "location_id": {"location"}, "scope": {"direct"}, "status": {"archived"}, "merchant": {"  Exact é  "}, "limit": {"100"}, "cursor": {strings.Repeat("x", 2048)}}
	cursor, size, filters, ok := purchaseListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/purchases?"+values.Encode(), ""), "request")
	if !ok || len(cursor) != 2048 || size != 100 || filters.From != "2026-11-05" || filters.To != "2026-11-06" || filters.Currency != "TWD" || filters.CategoryID != "category" || filters.LocationID != "location" || filters.Scope != "direct" || filters.Status != "archived" || filters.Merchant != "  Exact é  " {
		t.Fatal("purchase query dropped/repaired filters or adopted Stock cursor width")
	}
	_, size, _, ok = purchaseListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/purchases", ""), "request")
	if !ok || size != 50 {
		t.Fatal("purchase default is not 50")
	}
	for _, query := range []string{"q=x", "item_id=item", "limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 2049), "merchant=", "scope=", "from=2026", "unknown=1", "%zz=x", "a;b=c", "currency=JPY&currency=JPY"} {
		if _, _, _, ok := purchaseListQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/purchases?"+query, ""), "request"); ok {
			t.Fatal("invalid/duplicate/uncontracted purchase query admitted", query)
		}
	}
	// Keep empty padding below Go 1.26's separate 10,000-parameter guard; test
	// the byte ceiling without disabling production URL parser protections.
	base := "merchant=" + url.QueryEscape(strings.Repeat("😀", 300)) + "&category_id=" + url.QueryEscape(strings.Repeat("界", 128)) + "&location_id=" + url.QueryEscape(strings.Repeat("界", 128)) + "&cursor=" + strings.Repeat("x", 2048)
	for _, extra := range []int{0, 1} {
		query := base + strings.Repeat("&", (16<<10)-len(base)+extra)
		request := apiRequest(http.MethodGet, "/api/v1/purchases?"+query, "")
		response := httptest.NewRecorder()
		_, _, _, ok := purchaseListQuery(response, request, "request")
		if ok != (extra == 0) {
			t.Fatalf("purchase raw-query byte budget changed: extra=%d raw=%d accepted=%t status=%d", extra, len(request.URL.RawQuery), ok, response.Code)
		}
	}
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, path := range []string{"/api/v1/items/item/purchases", "/api/v1/purchases/purchase/lines"} {
		for _, query := range []string{"merchant=M", "currency=JPY", "q=x", "item_id=item", "cursor=" + strings.Repeat("x", 2049)} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, path+"?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
		}
	}
	for _, query := range []string{"from=2026-11-07&to=2026-11-06", "from=2026-02-30", "currency=jpy", "status=stored", "scope=ALL"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/purchases?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
}
