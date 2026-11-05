package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/store"
)

func catalogReply(t *testing.T, response *httptest.ResponseRecorder) store.Item {
	t.Helper()
	var item store.Item
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &item) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 13 ||
		!boundedString(item.ID, 1, 128) || !boundedString(item.Name, 1, 300) || item.Revision < 1 || item.Revision > maxJSONInteger ||
		response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(item.Revision, 10)) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid catalog resource reply: %d %s", response.Code, response.Body.String())
	}
	for _, name := range []string{"id", "revision", "name", "brand", "model", "category_id", "description", "notes", "tracking_mode", "unit_code", "base_unit", "scale_factor", "status"} {
		if fields[name] == nil {
			t.Fatal("resource omitted a required nullable/default field", name)
		}
	}
	return item
}

func catalogPageReply(t *testing.T, response *httptest.ResponseRecorder) store.ItemList {
	t.Helper()
	var page store.ItemList
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Data == nil ||
		response.Header().Get("ETag") != "" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid catalog page: %d %s", response.Code, response.Body.String())
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 2 || fields["next_cursor"] == nil {
		t.Fatal("page exposed internal continuation data or omitted terminal null")
	}
	return page
}

func TestCatalogHTTPAllSixOperationsUseOriginalProofsExactNullsAndFrozenReplies(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	if page := catalogPageReply(t, call(http.MethodGet, "/api/v1/items", "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("initial catalog fabricated inventory")
	}
	body := `{"name":"  濾芯 😀 é  ","brand":"Brand","model":null,"description":"","notes":"OriginalNote","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1e3}`
	created := call(http.MethodPost, "/api/v1/items", body, "http-create-item", "")
	item := catalogReply(t, created)
	path := "/api/v1/items/" + item.ID
	if item.Revision != 1 || item.Name != "  濾芯 😀 é  " || item.TrackingMode != "quantity" || item.ScaleFactor != 1000 || item.Model != nil || item.Status != "active" {
		t.Fatal("HTTP item creation lost exact fields/defaults or integer precision")
	}
	if current := catalogReply(t, call(http.MethodGet, path, "", "", "")); !reflect.DeepEqual(current, item) {
		t.Fatal("resource read is not the committed current item")
	}
	item = catalogReply(t, call(http.MethodPatch, path, `{"name":"Renamed"}`, "http-name-only-item", `"1"`))
	if item.Revision != 2 || item.Brand == nil || *item.Brand != "Brand" || item.Model != nil || item.Notes != "OriginalNote" || item.ScaleFactor != 1000 {
		t.Fatal("partial edit overwrote omitted nullable/measurement fields")
	}
	item = catalogReply(t, call(http.MethodPatch, path, `{"brand":null,"model":"","notes":""}`, "http-clear-item", `"2"`))
	if item.Revision != 3 || item.Brand != nil || item.Model == nil || *item.Model != "" || item.Notes != "" {
		t.Fatal("explicit null and empty text lost their distinct meanings")
	}
	archived := catalogReply(t, call(http.MethodPost, path+"/archive", `{"reason":"  Keep history  "}`, "http-archive-item", `"3"`))
	if archived.Revision != 4 || archived.Status != "archived" {
		t.Fatal("archive did not update metadata status")
	}
	noop := catalogReply(t, call(http.MethodPost, path+"/archive", `{}`, "http-noop-archive", `"4"`))
	if !reflect.DeepEqual(noop, archived) {
		t.Fatal("repeated archive changed a resource revision")
	}
	item = catalogReply(t, call(http.MethodPatch, path, `{"status":"active"}`, "http-restore-item", `"4"`))
	if item.Status != "active" || item.Revision != 5 {
		t.Fatal("restoration did not remain an explicit metadata patch")
	}
	expectError(t, call(http.MethodPatch, path, `{"name":"Stale"}`, "http-stale-item", `"4"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, path, `{"name":"Changed"}`, "http-name-only-item", `"1"`), 409, "IDEMPOTENCY_CONFLICT")
	replayed := call(http.MethodPost, "/api/v1/items", body, "http-create-item", "")
	if replayed.Code != 200 || replayed.Body.String() != created.Body.String() || replayed.Header().Get("ETag") != `"1"` || catalogReply(t, call(http.MethodGet, path, "", "", "")).Revision != 5 {
		t.Fatal("historical create reply was rebased or installed as current source")
	}
	deleted := call(http.MethodDelete, path, "", "http-delete-item", `"5"`)
	var ack store.ActionAck
	if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 6 || ack.OperationID == "" || deleted.Header().Get("ETag") != "" || deleted.Header().Get("Set-Cookie") != "" {
		t.Fatal("deletion did not return an independent frozen acknowledgement")
	}
	replayed = call(http.MethodDelete, path, "", "http-delete-item", `"5"`)
	if replayed.Code != 200 || replayed.Body.String() != deleted.Body.String() {
		t.Fatal("deleted source could not reconcile its identical original request")
	}
	if replayed := call(http.MethodPost, "/api/v1/items", body, "http-create-item", ""); replayed.Code != 200 || replayed.Body.String() != created.Body.String() {
		t.Fatal("creation acknowledgement became unavailable after deletion")
	}
	expectError(t, call(http.MethodGet, path, "", "", ""), 404, "NOT_FOUND")
	expectError(t, call(http.MethodDelete, path, "", "http-new-delete-item", `"5"`), 404, "NOT_FOUND")
	if page := catalogPageReply(t, call(http.MethodGet, "/api/v1/items", "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("a frozen reply resurrected deleted catalog metadata")
	}
}

func TestCatalogHTTPClassificationMembershipAndMeasurementValidationRemainAuthoritative(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	category := taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", `{"name":"Classification"}`, "catalog-category-create", ""))
	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", fmt.Sprintf(`{"name":"Catalog","category_id":%q}`, category.ID), "catalog-classified-item", ""))
	path, categoryPath := "/api/v1/items/"+item.ID, "/api/v1/categories/"+category.ID
	expectError(t, call(http.MethodDelete, categoryPath, "", "old-classification-proof", `"1"`), 409, "REVISION_CONFLICT")
	if response := call(http.MethodDelete, categoryPath, "", "delete-assigned-class", `"2"`); response.Code != 200 {
		t.Fatal("reviewed category deletion failed")
	}
	item = catalogReply(t, call(http.MethodGet, path, "", "", ""))
	if item.Revision != 2 || item.CategoryID != nil {
		t.Fatal("classification deletion did not update the item aggregate")
	}
	for index, body := range []string{
		`{"name":"Missing","category_id":"missing"}`,
		`{"name":"Incomplete","unit_code":"kg"}`,
		`{"name":"Incomplete","base_unit":"g","scale_factor":1000}`,
		`{"name":"Invalid","unit_code":"kg","base_unit":"g\n","scale_factor":1000}`,
	} {
		status, code := 422, "VALIDATION_ERROR"
		if index == 0 {
			status, code = 404, "NOT_FOUND"
		}
		expectError(t, call(http.MethodPost, "/api/v1/items", body, fmt.Sprintf("invalid-item-domain-%d", index), ""), status, code)
	}
	expectError(t, call(http.MethodPatch, path, `{"category_id":"missing"}`, "missing-item-classification", `"2"`), 404, "NOT_FOUND")
	expectError(t, call(http.MethodPatch, path, `{"base_unit":"克"}`, "invalid-item-base-unit", `"2"`), 422, "VALIDATION_ERROR")
	expectError(t, call(http.MethodPost, "/api/v1/items", `{"name":"Unrelated"}`, "unrelated-item-proof", `"2"`), 400, "INVALID_REVISION")
	if current := catalogReply(t, call(http.MethodGet, path, "", "", "")); !reflect.DeepEqual(current, item) {
		t.Fatal("rejected classification/measurement input changed source metadata")
	}
}

func TestCatalogHTTPListQueriesKeepEveryFilterAndSignedContinuationScope(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, ""))
	}
	category := taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", `{"name":"Category"}`, "list-category"))
	taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", `{"name":"Other"}`, "list-other-category"))
	for index := range 5 {
		status := "active"
		if index == 4 {
			status = "archived"
		}
		catalogReply(t, call(http.MethodPost, "/api/v1/items", fmt.Sprintf(`{"name":"Keep %d","category_id":%q,"status":%q}`, index, category.ID, status), fmt.Sprintf("list-item-%d", index)))
	}
	base := "/api/v1/items?limit=2&status=active&category_id=" + url.QueryEscape(category.ID) + "&q=KEEP"
	first := catalogPageReply(t, call(http.MethodGet, base, "", ""))
	if len(first.Data) != 2 || first.NextCursor == nil {
		t.Fatal("filtered list did not preserve complete signed pagination")
	}
	last := catalogPageReply(t, call(http.MethodGet, base+"&cursor="+url.QueryEscape(*first.NextCursor), "", ""))
	if len(last.Data) != 2 || last.NextCursor != nil || last.Data[0].ID <= first.Data[1].ID {
		t.Fatal("signed continuation skipped/reordered matching items")
	}
	expectError(t, call(http.MethodGet, strings.Replace(base, "active", "archived", 1)+"&cursor="+url.QueryEscape(*first.NextCursor), "", ""), 400, "INVALID_CURSOR")
	expectError(t, call(http.MethodGet, "/api/v1/categories?limit=2&cursor="+url.QueryEscape(*first.NextCursor), "", ""), 400, "INVALID_CURSOR")
	for _, query := range []string{"from=2026-01-01&to=2026-12-31&currency=TWD&merchant=Exact", "location_id=missing&scope=direct", "status=archived&q=does-not-match"} {
		if page := catalogPageReply(t, call(http.MethodGet, "/api/v1/items?"+query, "", "")); len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("a documented filter was silently dropped")
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=1", "cursor=", "cursor=a&cursor=b", "unknown=1", "q=a&q=b", "merchant=", "category_id=", "location_id=", "scope=all", "status=disposed", "currency=usd", "from=2026-02-29", "from=2026-11-06&to=2026-11-05", "q=%ff", "q=%zz"} {
		expectError(t, call(http.MethodGet, "/api/v1/items?"+query, "", ""), 400, "INVALID_QUERY")
	}
	expectError(t, call(http.MethodGet, "/api/v1/items?cursor=unsigned", "", ""), 400, "INVALID_CURSOR")
	r := domainRequest(http.MethodGet, "/api/v1/items", "", cookie, csrf, "", "")
	r.URL.RawQuery = strings.Repeat("x", (16<<10)+1)
	expectError(t, respond(handler, r), 400, "INVALID_QUERY")
}

func TestCatalogHTTPRevalidatesSessionAndOriginalProofAfterBodyReading(t *testing.T) {
	for _, change := range []string{"revoked create", "revoked patch", "revoked archive", "revoked delete", "changed proof"} {
		t.Run(change, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			catalog, err := store.NewCatalog(auth)
			if err != nil {
				t.Fatal(err)
			}
			created := respond(handler, domainRequest(http.MethodPost, "/api/v1/items", `{"name":"Before"}`, cookie, csrf, "before-body-read-item", ""))
			item := catalogReply(t, created)
			method, path, body := http.MethodPatch, "/api/v1/items/"+item.ID, `{"name":"Not rebased"}`
			revision := `"1"`
			switch change {
			case "revoked create":
				method, path, body, revision = http.MethodPost, "/api/v1/items", `{"name":"Not created"}`, ""
			case "revoked archive":
				method, path, body = http.MethodPost, path+"/archive", `{}`
			case "revoked delete":
				method, body = http.MethodDelete, ""
			}
			r := domainRequest(method, path, body, cookie, csrf, "after-body-read-item", revision)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				if change == "changed proof" {
					name := "Concurrent"
					if _, err := catalog.Patch(t.Context(), cookie.Value, "concurrent-item-change", item.ID, 1, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-during-item-body"); err != nil {
					t.Fatal(err)
				}
			}})
			if change == "changed proof" {
				expectError(t, respond(handler, r), 409, "REVISION_CONFLICT")
				current := catalogReply(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items/"+item.ID, "", cookie, csrf, "", "")))
				if current.Name != "Concurrent" || current.Revision != 2 {
					t.Fatal("HTTP mutation silently rebased onto a different proof")
				}
			} else {
				expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
				fresh, freshCSRF := testCredentials(t, auth, changedPassword)
				current := catalogReply(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items/"+item.ID, "", fresh, freshCSRF, "", "")))
				if current.Name != "Before" || current.Revision != 1 || current.Status != "active" {
					t.Fatal("revoked-after-middleware item operation changed source")
				}
			}
		})
	}
}

func TestCatalogErrorsRemainStaticAllowlistedAndNeverChangeCookies(t *testing.T) {
	for _, input := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"},
		{store.ErrInvalidCursor, 400, "INVALID_CURSOR"}, {store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"},
		{store.ErrRevisionConflict, 409, "REVISION_CONFLICT"}, {store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"},
		{store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {store.ErrItemHistory, 409, "ITEM_HAS_HISTORY"},
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"},
		{errors.New("SQL /private/database secret=hidden"), 503, "SERVICE_UNAVAILABLE"},
	} {
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "safe-request")
		writeCatalogError(response, fmt.Errorf("private wrapper: %w", input.err), "safe-request")
		expectError(t, response, input.status, input.code)
		if strings.Contains(response.Body.String(), "secret=") || strings.Contains(response.Body.String(), "/private/") || strings.Contains(response.Body.String(), "private wrapper") {
			t.Fatal("private catalog diagnostic escaped its safe error boundary")
		}
	}
}
