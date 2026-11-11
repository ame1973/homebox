package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"homebox/internal/store"
)

func globalMediaHTTPPage(t *testing.T, response *httptest.ResponseRecorder) store.MediaList {
	t.Helper()
	var page store.MediaList
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil ||
		len(fields) != 2 || fields["next_cursor"] == nil || page.Data == nil || response.Header().Get("ETag") != "" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid global Media HTTP page: status %d", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "private-storage") || strings.Contains(body, "private-legacy") || strings.Contains(body, "NeverPublishUnreviewedMediaOCR") || strings.Contains(body, "storage_key") || strings.Contains(body, "perceptual_hash") {
		t.Fatal("global Media page exposed private source")
	}
	return page
}

func TestGlobalMediaHTTPReadsCompleteActualSourcesAndScopedCursors(t *testing.T) {
	ids := []string{"\x00Media\r\n", ".", "..", "a/b", `back\slash`, "☕😀", strings.Repeat("😀", 128)}
	for index := range 205 {
		ids = append(ids, fmt.Sprintf("global-%03d", index))
	}
	f := newMediaHTTPFixtureWithMedia(t, ids)
	ids = append(ids, "empty-media", "links", "photo")
	sort.Strings(ids)
	cursor, count := "", 0
	for {
		path := "/api/v1/media"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		page := globalMediaHTTPPage(t, mediaHTTPGet(f, path))
		if len(page.Data) == 0 || len(page.Data) > 50 || count == 0 && len(page.Data) != 50 {
			t.Fatal("global page did not apply its documented default", len(page.Data))
		}
		for _, image := range page.Data {
			if count >= len(ids) || image.ID != ids[count] || image.Verification != "unverified" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
				t.Fatal("global page lost an actual linked or unlinked Media identity", count, image)
			}
			if image.ID == "photo" && (image.Revision != 7 || image.MIMEType != "image/heic") {
				t.Fatal("global page changed retained linked Media claims", image)
			}
			if image.ID == "empty-media" && image.Revision != 1 {
				t.Fatal("global page changed an unlinked Media counter", image)
			}
			count++
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != len(ids) {
		t.Fatal("global page omitted Media", count, len(ids))
	}
	max := globalMediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media?limit=100"))
	if len(max.Data) != 100 || max.NextCursor == nil {
		t.Fatal("global maximum page did not return 100 actual records")
	}
	first := globalMediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media?limit=1"))
	if len(first.Data) != 1 || first.Data[0].ID != "\x00Media\r\n" || first.NextCursor == nil {
		t.Fatal("global first page changed the retained control identity")
	}
	second := globalMediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media?limit=1&cursor="+url.QueryEscape(*first.NextCursor)))
	if len(second.Data) != 1 || second.Data[0].ID != "." {
		t.Fatal("global cursor changed the following retained dot identity")
	}
	ownerPath := "/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.owner.ID) + "&limit=1"
	owner := mediaHTTPPage(t, mediaHTTPGet(f, ownerPath))
	membership := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/photo/links?limit=1"))
	if owner.NextCursor == nil || membership.NextCursor == nil {
		t.Fatal("missing independently scoped link cursors")
	}
	for _, path := range []string{
		"/api/v1/media?limit=1&cursor=" + url.QueryEscape(*owner.NextCursor),
		"/api/v1/media?limit=1&cursor=" + url.QueryEscape(*membership.NextCursor),
		"/api/v1/media?limit=2&cursor=" + url.QueryEscape(*first.NextCursor),
		"/api/v1/media?limit=1&cursor=" + url.QueryEscape(*first.NextCursor+"x"),
		ownerPath + "&cursor=" + url.QueryEscape(*first.NextCursor),
		"/api/v1/media/photo/links?limit=1&cursor=" + url.QueryEscape(*first.NextCursor),
	} {
		expectError(t, mediaHTTPGet(f, path), http.StatusBadRequest, "INVALID_CURSOR")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	reserved := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(reserved.Data) != 1 || reserved.Data[0].ID != "reserved" {
		t.Fatal("global root dispatch shadowed the retained links Media membership")
	}
	if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "global-media-fresh-login"); err != nil {
		t.Fatal(err)
	}
	f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	afterLogin := globalMediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media?limit=1&cursor="+url.QueryEscape(*first.NextCursor)))
	if len(afterLogin.Data) != 1 || afterLogin.Data[0].ID != "." {
		t.Fatal("same-principal fresh login could not continue the global page")
	}
}

func TestGlobalMediaHTTPAdmissionBodyAndQueryLimits(t *testing.T) {
	f := newMediaHTTPFixture(t)
	path := "/api/v1/media"
	expectError(t, respond(f.handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
	for _, origin := range []string{"", "https://foreign.test"} {
		r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		r.Header.Set("Origin", origin)
		expectError(t, respond(f.handler, r), 403, "ORIGIN_REJECTED")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodDelete} {
		r := domainRequest(method, path, "", f.cookie, f.csrf, "media-global-method", `"1"`)
		response := respond(f.handler, r)
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatal("global Media route advertised an unimplemented method")
		}
	}
	// The target upload remains an unimplemented protected route, not a GET or
	// an attachment operation. Its existing admission and 404 are preserved.
	post := domainRequest(http.MethodPost, path, "", f.cookie, f.csrf, "media-upload-not-implemented", "")
	expectError(t, respond(f.handler, post), 404, "NOT_FOUND")
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		mutate(r)
		expectError(t, respond(f.handler, r), 400, "INVALID_BODY")
	}
	for _, query := range []string{
		"unknown=x", "owner_id=photo", "limit=0", "limit=101", "limit=1&limit=2", "limit=abc", "limit=%", "limit=1;cursor=x",
		"cursor=", "cursor=a&cursor=b", "cursor=" + strings.Repeat("x", 2049), "cursor=" + url.QueryEscape(strings.Repeat("😀", 513)),
	} {
		r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		r.URL.RawQuery = query
		expectError(t, respond(f.handler, r), 400, "INVALID_QUERY")
	}
	for _, query := range []string{"cursor=" + strings.Repeat("x", 2048), "cursor=" + url.QueryEscape(strings.Repeat("😀", 512))} {
		r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		r.URL.RawQuery = query
		expectError(t, respond(f.handler, r), 400, "INVALID_CURSOR")
	}
	r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	r.URL.RawPath = "/api/v1/%6dedia"
	expectError(t, respond(f.handler, r), 400, "INVALID_MEDIA_URL")

	// Leading zeroes make an allowed integer fill the raw budget without
	// exceeding Go's separate 10,000-query-parameter limit.
	raw := "limit=" + strings.Repeat("0", (16<<10)-len("limit=")-1) + "1"
	r = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	r.URL.RawQuery = raw
	page := globalMediaHTTPPage(t, respond(f.handler, r))
	if len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatal("exact raw-query budget rejected a valid bounded limit")
	}
	r = domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
	r.URL.RawQuery = raw + "0"
	expectError(t, respond(f.handler, r), 400, "INVALID_QUERY")
}

func TestGlobalMediaHTTPRequiresCompletedSessionAndRevalidatesAfterBody(t *testing.T) {
	emptyAuth := newTestAuth(t)
	setupAccount(t, emptyAuth, true)
	emptyCookie, emptyCSRF := testCredentials(t, emptyAuth, changedPassword)
	empty := globalMediaHTTPPage(t, respond(authHandler(t, emptyAuth), domainRequest(http.MethodGet, "/api/v1/media", "", emptyCookie, emptyCSRF, "", "")))
	if len(empty.Data) != 0 || empty.NextCursor != nil {
		t.Fatal("an empty actual Media collection was not an empty page")
	}

	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	expectError(t, respond(authHandler(t, auth), domainRequest(http.MethodGet, "/api/v1/media", "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")

	f := newMediaHTTPFixture(t)
	r := domainRequest(http.MethodGet, "/api/v1/media", "", f.cookie, f.csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "global-media-body-revoke"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(f.handler, r), 401, "AUTHENTICATION_REQUIRED")
}

func FuzzGlobalMediaPageQuery(f *testing.F) {
	for _, query := range []string{"", "limit=1", "limit=100&cursor=opaque", "limit=0", "limit=1&limit=2", "cursor=%FF", "cursor=", "id=photo", "limit=%"} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 20<<10 {
			t.Skip()
		}
		request := apiRequest(http.MethodGet, "/api/v1/media", "")
		request.URL.RawQuery = raw
		response := httptest.NewRecorder()
		cursor, limit, ok := boundedMetadataQuery(response, request, "request", 50, 16<<10)
		if !ok {
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid query returned %d", response.Code)
			}
			return
		}
		values, err := url.ParseQuery(raw)
		if err != nil || len(raw) > 16<<10 || limit < 1 || limit > 100 || len(cursor) > 2048 || values.Get("cursor") != cursor {
			t.Fatal("accepted query changed or exceeded the documented page scope")
		}
		for name, entries := range values {
			if (name != "limit" && name != "cursor") || len(entries) != 1 {
				t.Fatal("accepted duplicate or unknown query parameter")
			}
		}
	})
}

func TestGlobalMediaHTTPWithholdsLookaheadChangedDuringBodyRead(t *testing.T) {
	f := newMediaHTTPFixture(t)
	raw, err := sql.Open("sqlite", f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	r := domainRequest(http.MethodGet, "/api/v1/media?limit=2", "", f.cookie, f.csrf, "", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := raw.ExecContext(t.Context(), `UPDATE media SET verification='verified' WHERE id='photo'`); err != nil {
			t.Fatal(err)
		}
	}})
	response := respond(f.handler, r)
	expectError(t, response, 503, "SERVICE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), "empty-media") || strings.Contains(response.Body.String(), "private-storage") || response.Header().Get("ETag") != "" {
		t.Fatal("corrupt lookahead exposed a successful prefix or private source")
	}
}
