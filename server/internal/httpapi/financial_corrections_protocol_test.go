package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"homebox/internal/store"
)

func financialCorrectionOperations() []struct{ method, path, body string } {
	return []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/purchases/purchase/line-corrections", ""},
		{http.MethodPost, "/api/v1/purchases/purchase/line-corrections", correctionJSONBodies[0].body},
		{http.MethodGet, "/api/v1/units/unit/disposal-record", ""},
		{http.MethodGet, "/api/v1/units/unit/disposal-corrections", ""},
		{http.MethodPost, "/api/v1/units/unit/disposal-corrections", correctionJSONBodies[1].body},
	}
}

func TestFinancialCorrectionsHTTPAllFiveOperationsRequireCompletedSessionExactOriginHostAndTransport(t *testing.T) {
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
				if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-financial-correction-boundary"); err != nil {
					t.Fatal(err)
				}
			default:
				status, code = 403, "ORIGIN_REJECTED"
			}
			for _, operation := range financialCorrectionOperations() {
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "financial-correction-boundary", `"1"`)
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
					t.Fatal("unauthorized correction read its body")
				}
			}
		})
	}
}

func TestFinancialCorrectionsHTTPBothWritersRequireExactWriteHeadersAndOriginalProof(t *testing.T) {
	for _, operation := range financialCorrectionOperations() {
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
				r := domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "correction-header-boundary", `"1"`)
				test.mutate(r)
				read := false
				r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() { read = true }})
				expectError(t, respond(handler, r), test.status, test.code)
				if read {
					t.Fatal("invalid original headers reached correction JSON reading")
				}
			}
			for _, proof := range []string{"1", `"0"`, `"01"`, `W/"1"`, `"1", "2"`, "*", `"-1"`, `"+1"`, `"1.0"`, `"1e0"`, `"9007199254740992"`, `"1" `} {
				expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "correction-invalid-proof", proof)), 400, "INVALID_REVISION")
			}
			expectError(t, respond(handler, domainRequest(operation.method, operation.path, operation.body, cookie, csrf, "correction-missing-proof", "")), 409, "PRECONDITION_REQUIRED")
		})
	}
}

func TestFinancialCorrectionsHTTPMethodsExactPathsAndReadBodies(t *testing.T) {
	t.Run("methods", func(t *testing.T) {
		_, handler, cookie, csrf := domainHTTPFixture(t)
		for _, route := range []struct{ path, allow string }{
			{"/api/v1/purchases/purchase/line-corrections", "GET, POST"},
			{"/api/v1/units/unit/disposal-record", "GET"},
			{"/api/v1/units/unit/disposal-corrections", "GET, POST"},
		} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
				if slices.Contains(strings.Split(route.allow, ", "), method) {
					continue
				}
				reply := respond(handler, domainRequest(method, route.path, "", cookie, csrf, "correction-invalid-method", `"1"`))
				expectError(t, reply, 405, "METHOD_NOT_ALLOWED")
				if reply.Header().Get("Allow") != route.allow {
					t.Fatal("correction method admission widened", route.path, method)
				}
			}
		}
	})
	t.Run("paths and bodies", func(t *testing.T) {
		_, handler, cookie, csrf := domainHTTPFixture(t)
		for _, test := range []struct{ path, code string }{
			{"/api/v1/purchases/./line-corrections", "INVALID_PURCHASE_URL"},
			{"/api/v1/purchases/%70urchase/line-corrections", "INVALID_PURCHASE_URL"},
			{"/api/v1/purchases/" + strings.Repeat("界", 129) + "/line-corrections", "INVALID_PURCHASE_URL"},
			{"/api/v1/units/../disposal-record", "INVALID_ASSET_URL"},
			{"/api/v1/units/%75nit/disposal-record", "INVALID_ASSET_URL"},
			{"/api/v1/units/unit/disposal-record?limit=1", "INVALID_ASSET_URL"},
			{"/api/v1/units/unit/disposal-record?", "INVALID_ASSET_URL"},
		} {
			expectError(t, respond(handler, domainRequest(http.MethodGet, test.path, "", cookie, csrf, "", "")), 400, test.code)
		}
		for _, operation := range financialCorrectionOperations() {
			if operation.method != http.MethodGet {
				code := "INVALID_PURCHASE_URL"
				if strings.Contains(operation.path, "/units/") {
					code = "INVALID_ASSET_URL"
				}
				expectError(t, respond(handler, domainRequest(operation.method, operation.path+"?limit=1", operation.body, cookie, csrf, "correction-write-query", `"1"`)), 400, code)
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
			for _, path := range []string{"/api/v1/purchases/" + id + "/line-corrections", "/api/v1/units/" + id + "/disposal-record", "/api/v1/units/" + id + "/disposal-corrections"} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
			}
		}
	})
}

func TestFinancialCorrectionsHTTPOwnedQueriesRejectOtherFiltersDuplicateAndOversizedTokens(t *testing.T) {
	for _, path := range []string{"/api/v1/purchases/purchase/line-corrections", "/api/v1/units/unit/disposal-corrections"} {
		t.Run(path, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			for _, query := range []string{"q=x", "item_id=item", "limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 2049), "unknown=1", "%zz=x", "a;b=c"} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, path+"?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
			}
			for _, query := range []string{"", "?limit=1", "?limit=100"} {
				expectError(t, respond(handler, domainRequest(http.MethodGet, path+query, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
			}
		})
	}
}

func TestFinancialCorrectionsHTTPRejectsInvalidNestedCommandsBeforeAccessingAnySource(t *testing.T) {
	for _, test := range correctionJSONBodies {
		t.Run(test.kind, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			path := "/api/v1/purchases/missing/line-corrections"
			if test.kind == "disposal" {
				path = "/api/v1/units/missing/disposal-corrections"
			}
			for _, invalid := range []struct {
				body   string
				status int
				code   string
			}{
				{assetJSONMutation(t, test.body, "replacement", ""), 422, "VALIDATION_ERROR"},
				{assetJSONMutation(t, test.body, "replacement", "null"), 422, "VALIDATION_ERROR"},
				{assetJSONMutation(t, test.body, "reason", `"\ud800"`), 400, "INVALID_JSON"},
				{assetJSONMutation(t, test.body, "replacement", test.replacement[:len(test.replacement)-1]+`,"note":"A","note":"B"}`), 400, "INVALID_JSON"},
				{assetJSONMutation(t, test.body, "replacement", test.replacement[:len(test.replacement)-1]+`,"unexpected":1}`), 422, "VALIDATION_ERROR"},
			} {
				expectError(t, respond(handler, domainRequest(http.MethodPost, path, invalid.body, cookie, csrf, "invalid-nested-correction", `"1"`)), invalid.status, invalid.code)
			}
			void := assetJSONMutation(t, assetJSONMutation(t, test.body, "action", `"void"`), "replacement", "null")
			expectError(t, respond(handler, domainRequest(http.MethodPost, path, void, cookie, csrf, "valid-but-missing-correction-owner", `"1"`)), 404, "NOT_FOUND")
		})
	}
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for _, instant := range []string{"2026-11-07T00:00:60Z", "2026-11-07T00:00:00.1234567890Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01"} {
		body := assetJSONMutation(t, correctionJSONBodies[1].body, "replacement", assetJSONMutation(t, disposalCorrectionReplacement, "disposed_at", purchaseJSONQuoted(t, instant)))
		expectError(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/units/missing/disposal-corrections", body, cookie, csrf, "correction-invalid-instant", `"1"`)), 422, "VALIDATION_ERROR")
	}
}

func TestFinancialCorrectionVoidedLineErrorIsStaticAndDoesNotExposeStorageOrCookies(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "request")
	writePurchaseError(w, errors.Join(store.ErrPurchaseLineVoided, errors.New("private storage path")), "request")
	expectError(t, w, 409, "PURCHASE_LINE_VOIDED")
	if w.Header().Get("ETag") != "" || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "private storage path") {
		t.Fatal("voided-source conflict leaked storage or claimed a resource proof")
	}
}
