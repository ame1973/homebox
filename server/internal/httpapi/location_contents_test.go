package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"homebox/internal/store"
)

func TestLocationContentsHTTPPagesRealSourcesAndBindsRevision(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	request := func(method, path, body, key, revision string) *http.Request {
		return domainRequest(method, path, body, cookie, csrf, key, revision)
	}
	root := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"CONTENTS-ROOT"}`, "contents-root", "")))
	child := responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"Child","code":"CONTENTS-CHILD","parent_id":%q}`, root.ID), "contents-child", `"1"`)))
	quantity := catalogReply(t, respond(handler, request(http.MethodPost, "/api/v1/items", `{"name":"Quantity","tracking_mode":"quantity"}`, "contents-quantity", "")))
	stockHTTPReply(t, respond(handler, request(http.MethodPost, "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":3}`, quantity.ID, child.ID), "contents-stock", `"1"`)), quantity.ID, child.ID)
	owner := catalogReply(t, respond(handler, request(http.MethodPost, "/api/v1/items", `{"name":"Individual"}`, "contents-owner", "")))
	unit := ownedReply[store.Unit](t, respond(handler, request(http.MethodPost, "/api/v1/items/"+owner.ID+"/units", fmt.Sprintf(`{"location_id":%q}`, root.ID), "contents-unit", `"1"`)), owner.ID, 8)
	current := responseLocation(t, respond(handler, request(http.MethodGet, "/api/v1/locations/"+root.ID, "", "", "")))
	path := "/api/v1/locations/" + root.ID + "/contents"
	cursor, count := "", 0
	var units []string
	var stocks []store.Stock
	var children []string
	for {
		values := url.Values{"scope": {"subtree"}, "limit": {"1"}}
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		response := respond(handler, request(http.MethodGet, path+"?"+values.Encode(), "", "", ""))
		var page store.LocationContents
		var fields map[string]json.RawMessage
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil ||
			response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(current.Revision, 10)) || response.Header().Get("Cache-Control") != "no-store" ||
			len(fields) != 6 || fields["revision"] != nil || fields["next_cursor"] == nil || page.LocationID != root.ID || page.Scope != "subtree" ||
			page.Units == nil || page.Stock == nil || page.SubLocations == nil || len(page.Units)+len(page.Stock)+len(page.SubLocations) != 1 {
			t.Fatalf("invalid contents page: %d %s", response.Code, response.Body.String())
		}
		for _, value := range page.Units {
			units = append(units, value.ID)
		}
		stocks = append(stocks, page.Stock...)
		for _, value := range page.SubLocations {
			children = append(children, value.ID)
		}
		count++
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor || count > 3 {
			t.Fatal("contents continuation did not advance")
		}
		cursor = *page.NextCursor
	}
	if count != 3 || !reflect.DeepEqual(units, []string{unit.ID}) || len(stocks) != 1 || stocks[0].ItemID != quantity.ID || stocks[0].LocationID != child.ID || !reflect.DeepEqual(children, []string{child.ID}) {
		t.Fatal("real contents were truncated, mixed or duplicated", units, stocks, children)
	}
	direct := respond(handler, request(http.MethodGet, path+"?scope=direct", "", "", ""))
	var directPage store.LocationContents
	if direct.Code != 200 || json.Unmarshal(direct.Body.Bytes(), &directPage) != nil || len(directPage.Units) != 1 || len(directPage.Stock) != 0 || len(directPage.SubLocations) != 1 || directPage.NextCursor != nil {
		t.Fatal("direct scope included child stock or omitted a direct child", direct.Code, direct.Body.String())
	}
	first := respond(handler, request(http.MethodGet, path+"?limit=1", "", "", ""))
	var firstPage store.LocationContents
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || firstPage.Scope != "subtree" || firstPage.NextCursor == nil {
		t.Fatal("default subtree page failed", first.Code, first.Body.String())
	}
	responseLocation(t, respond(handler, request(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"New","code":"CONTENTS-NEW","parent_id":%q}`, root.ID), "contents-new", strconv.Quote(strconv.FormatInt(current.Revision, 10)))))
	expectError(t, respond(handler, request(http.MethodGet, path+"?limit=1&cursor="+url.QueryEscape(*firstPage.NextCursor), "", "", "")), 400, "INVALID_CURSOR")
}

func TestLocationContentsQueryUsesBoundedExactScopeAndCursor(t *testing.T) {
	for _, query := range []string{"", "scope=direct", "scope=subtree&limit=100&cursor=" + strings.Repeat("a", 4096)} {
		cursor, limit, scope, ok := locationContentsQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/locations/root/contents?"+query, ""), "request")
		if !ok || limit < 1 || limit > 100 || scope != "direct" && scope != "subtree" || query == "" && (cursor != "" || limit != 50 || scope != "subtree") {
			t.Fatal("valid contents query failed", query, cursor, limit, scope)
		}
	}
	utf8Cursor := strings.Repeat("😀", 1024)
	cursor, _, _, ok := locationContentsQuery(httptest.NewRecorder(), apiRequest(http.MethodGet, "/api/v1/locations/root/contents?cursor="+url.QueryEscape(utf8Cursor), ""), "request")
	if !ok || cursor != utf8Cursor || len(cursor) != 4096 {
		t.Fatal("contents cursor byte boundary was not preserved")
	}
	for _, query := range []string{"scope=", "scope=unknown", "scope=direct&scope=subtree", "limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 4097), "cursor=" + url.QueryEscape(strings.Repeat("😀", 1025)), "cursor=%ff", "unknown=1", "%zz=1", "cursor=" + strings.Repeat("x", 16<<10)} {
		w := httptest.NewRecorder()
		if _, _, _, ok := locationContentsQuery(w, apiRequest(http.MethodGet, "/api/v1/locations/root/contents?"+query, ""), "request"); ok || w.Code != 400 {
			t.Fatal("invalid contents query was admitted", query, w.Code)
		}
	}
}

func FuzzLocationContentsQuery(f *testing.F) {
	for _, query := range []string{"", "scope=direct&limit=1", "scope=subtree&limit=100&cursor=opaque", "scope=direct&scope=subtree", "cursor=%ff", "cursor=" + url.QueryEscape(strings.Repeat("😀", 1024)), "scope=direct;limit=1"} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > (16<<10)+1 {
			t.Skip()
		}
		request := apiRequest(http.MethodGet, "/api/v1/locations/root/contents", "")
		request.URL.RawQuery = query
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		cursor, limit, scope, ok := locationContentsQuery(response, request, "request")
		if !ok {
			if response.Code != 400 || !strings.Contains(response.Body.String(), `"INVALID_QUERY"`) {
				t.Fatal("invalid query returned an unsafe failure")
			}
			return
		}
		values, err := url.ParseQuery(query)
		if err != nil || len(query) > 16<<10 || limit < 1 || limit > 100 || scope != "direct" && scope != "subtree" ||
			!utf8.ValidString(cursor) || len(cursor) > 4096 || values.Get("cursor") != cursor {
			t.Fatal("accepted contents query changed or exceeded its bounded scope")
		}
		for name, entries := range values {
			if len(entries) != 1 || name != "scope" && name != "limit" && name != "cursor" {
				t.Fatal("accepted an unknown or repeated contents parameter")
			}
			switch name {
			case "scope":
				if entries[0] != scope {
					t.Fatal("scope changed")
				}
			case "limit":
				parsed, err := strconv.Atoi(entries[0])
				if err != nil || parsed != limit {
					t.Fatal("page size changed")
				}
			case "cursor":
				if cursor == "" {
					t.Fatal("empty continuation was accepted")
				}
			}
		}
		if _, present := values["scope"]; !present && scope != "subtree" {
			t.Fatal("omitted scope changed its default")
		}
		if _, present := values["limit"]; !present && limit != 50 {
			t.Fatal("omitted page size changed its default")
		}
	})
}

func TestLocationContentsHTTPRequiresPasswordChangeBeforeReading(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	expectError(t, respond(authHandler(t, auth), domainRequest(http.MethodGet, "/api/v1/locations/any/contents", "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}

func TestLocationContentsHTTPRequiresCompletedSessionAndBodylessRead(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	root := responseLocation(t, respond(handler, domainRequest(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"CONTENTS-BOUNDARY"}`, cookie, csrf, "contents-boundary", "")))
	path := "/api/v1/locations/" + root.ID + "/contents"
	expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, query := range []string{"scope=unknown", "scope=direct&scope=subtree", "limit=101", "cursor=", "unknown=1"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path+"?"+query, "", cookie, csrf, "", "")), 400, "INVALID_QUERY")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response := respond(handler, domainRequest(method, path, "", cookie, csrf, "contents-method", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("contents advertised an unsupported method")
		}
	}
	for _, target := range []string{"/api/v1/locations/%61lias/contents", "/api/v1/locations/%00/contents", "/api/v1/locations/" + strings.Repeat("x", 129) + "/contents"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 400, "INVALID_LOCATION_URL")
	}
	expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/locations/missing/contents", "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
		mutate(r)
		expectError(t, respond(handler, r), 400, "INVALID_BODY")
	}
	r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "contents-revoke-during-body"); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(handler, r)
	expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
	if response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), "CONTENTS-BOUNDARY") {
		t.Fatal("revoked contents read exposed private metadata or an ETag")
	}
}
