package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestLocationSummaryHTTPUsesActualPhysicalSourceAndAggregateETag(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	request := func(method, path, body, key, revision string) *http.Request {
		return domainRequest(method, path, body, cookie, csrf, key, revision)
	}
	root := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"SUMMARY-ROOT"}`, "summary-root", "")))
	child := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"Child","code":"SUMMARY-CHILD","parent_id":%q}`, root.ID), "summary-child", `"1"`)))
	quantity := catalogReply(t, respond(handler, request(http.MethodPost, "/api/v1/items", `{"name":"Mass","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1000}`, "summary-mass", "")))
	individual := catalogReply(t, respond(handler, request(http.MethodPost, "/api/v1/items", `{"name":"Individual"}`, "summary-individual", "")))
	stockHTTPReply(t, respond(handler, request(http.MethodPost, "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1500}`, quantity.ID, child.ID), "summary-receive", `"1"`)), quantity.ID, child.ID)
	unitPath := "/api/v1/items/" + individual.ID + "/units"
	unit := ownedReply[store.Unit](t, respond(handler, request(http.MethodPost, unitPath, fmt.Sprintf(`{"location_id":%q}`, root.ID), "summary-unit", `"1"`)), individual.ID, 8)
	childUnit := ownedReply[store.Unit](t, respond(handler, request(http.MethodPost, unitPath, fmt.Sprintf(`{"location_id":%q}`, child.ID), "summary-child-unit", `"2"`)), individual.ID, 8)
	ownedReply[store.Unit](t, respond(handler, request(http.MethodPost, "/api/v1/units/"+childUnit.ID+"/status", `{"status":"disposed","disposal_kind":"recycled"}`, "summary-child-dispose", `"1"`)), individual.ID, 8)
	current := responseLocation(t, respond(handler, request(http.MethodGet, "/api/v1/locations/"+root.ID, "", "", "")))
	response := respond(handler, request(http.MethodGet, "/api/v1/locations/"+root.ID+"/summary", "", "", ""))
	var value store.LocationSummary
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 6 ||
		response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(current.Revision, 10)) || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid authoritative summary boundary: %d %s", response.Code, response.Body.String())
	}
	if value.LocationID != root.ID || value.DirectUnits != 1 || value.SubtreeUnits != 1 || value.DistinctItemCount != 2 ||
		!reflect.DeepEqual(value.DirectQuantities, []store.QuantityGroup{}) || !reflect.DeepEqual(value.SubtreeQuantities, []store.QuantityGroup{{BaseUnit: "g", QuantityBase: 1500}}) {
		t.Fatal("summary mixed physical status, item identities, hierarchy or units", value)
	}
	if fields["revision"] != nil || unit.LocationID == nil || *unit.LocationID != root.ID {
		t.Fatal("summary invented a public revision or lost the real Unit owner")
	}
}

func TestLocationSummaryHTTPRequiresPasswordChangeBeforeReading(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	expectError(t, respond(authHandler(t, auth), domainRequest(http.MethodGet, "/api/v1/locations/any/summary", "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}

func TestLocationSummaryHTTPRequiresCompletedSessionAndExactBodylessRoute(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	root := responseLocation(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"SUMMARY-BOUNDARY"}`, cookie, csrf, "summary-boundary", "")))
	path := "/api/v1/locations/" + root.ID + "/summary"
	expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, path, "", cookie, csrf, "summary-method", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("summary advertised an unsupported method")
		}
	}
	for _, target := range []string{path + "?", path + "?scope=direct", "/api/v1/locations/%61lias/summary", "/api/v1/locations/%00/summary", "/api/v1/locations/" + strings.Repeat("x", 129) + "/summary"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 400, "INVALID_LOCATION_URL")
	}
	expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/locations/missing/summary", "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "summary-revoke-during-body"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
}
