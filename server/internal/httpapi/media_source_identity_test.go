package httpapi

import (
	"bytes"
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

	"homebox/internal/store"
)

func TestMediaSourceIdentityHTTPReadsAndAttachesExactRetainedIDs(t *testing.T) {
	ids := []string{".", "..", "media/slash", `media\backslash`, "\x00", "media/\x00😀\r\n", "media&id=other?#%+\\", strings.Repeat("界", 128)}
	f := newMediaHTTPFixtureWithMedia(t, ids)
	for index, id := range ids {
		query := "?id=" + url.QueryEscape(id)
		image := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media-by-identity"+query), id, 1)
		if image.Verification != "unverified" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
			t.Fatal("retained metadata was treated as verified bytes")
		}
		page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media-by-identity/links"+query))
		if len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("newly seeded retained Media did not have an actual empty membership")
		}
		body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d,"is_cover":false,"description":"Exact"}`, f.empty.ID, f.empty.Revision+int64(index))
		key := fmt.Sprintf("exact-media-attach-%02d", index)
		request := func() *http.Request {
			return domainRequest(http.MethodPost, "/api/v1/media-by-identity/links"+query, body, f.cookie, f.csrf, key, `"1"`)
		}
		first := respond(f.handler, request())
		var link store.MediaLink
		if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &link) != nil || link.ID == "" || link.MediaID != id ||
			link.OwnerType != "item" || link.OwnerID != f.empty.ID || link.Revision != 1 || link.Description != "Exact" ||
			first.Header().Get("ETag") != `"1"` || first.Header().Get("Cache-Control") != "no-store" || strings.Contains(first.Body.String(), "private-legacy-media-") {
			t.Fatalf("exact Media attachment %q: %d %s", id, first.Code, first.Body.String())
		}
		replay := respond(f.handler, request())
		if replay.Code != http.StatusOK || replay.Header().Get("ETag") != `"1"` || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
			t.Fatalf("original-key replay changed the frozen attachment for %q", id)
		}
		mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media-by-identity"+query), id, 2)
		page = mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media-by-identity/links"+query))
		if len(page.Data) != 1 || page.NextCursor != nil || page.Data[0].ID != link.ID || page.Data[0].MediaID != id || page.Data[0].Media.ID != id || page.Data[0].Media.Revision != 2 {
			t.Fatalf("attachment did not publish actual exact Media membership for %q", id)
		}
	}
}

func TestMediaSourceIdentityHTTPAttachesEveryOwnerKindWithoutExtraProofs(t *testing.T) {
	for _, kind := range []string{"item", "unit", "location", "legacy-item"} {
		t.Run(kind, func(t *testing.T) {
			id := "legacy/media/\x00😀"
			f := newMediaHTTPFixtureWithMedia(t, []string{id})
			field, ownerID, revision, ownerType := "item_id", f.empty.ID, f.empty.Revision, "item"
			switch kind {
			case "unit":
				field, ownerID, revision, ownerType = "item_unit_id", f.unit.ID, f.unit.Revision, "unit"
			case "location":
				field, ownerID, revision, ownerType = "location_id", f.location.ID, f.location.Revision, "location"
			case "legacy-item":
				ownerID, revision = f.legacyOwner, 7
			}
			body, err := json.Marshal(map[string]any{field: ownerID, "owner_revision": revision})
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/media-by-identity/links?id=" + url.QueryEscape(id)
			response := respond(f.handler, domainRequest(http.MethodPost, path, string(body), f.cookie, f.csrf, "exact-media-owner-kind", `"1"`))
			var link store.MediaLink
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.MediaID != id ||
				link.OwnerType != ownerType || link.OwnerID != ownerID || link.Revision != 1 || response.Header().Get("ETag") != `"1"` {
				t.Fatalf("exact Media owner attachment failed: %d %s", response.Code, response.Body.String())
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, path))
			if len(page.Data) != 1 || page.Data[0].ID != link.ID || page.Data[0].Media.ID != id || page.Data[0].OwnerID != ownerID {
				t.Fatal("exact Media membership lost an actual owner identity")
			}
		})
	}
}

func TestMediaSourceIdentityHTTPPagesShareActualCursorScope(t *testing.T) {
	f := newMediaHTTPFixture(t)
	path := "/api/v1/media/photo/links?limit=1"
	alias := "/api/v1/media-by-identity/links?id=photo&limit=1"
	firstPath := mediaHTTPPage(t, mediaHTTPGet(f, path))
	firstAlias := mediaHTTPPage(t, mediaHTTPGet(f, alias))
	if len(firstPath.Data) != 1 || !reflect.DeepEqual(firstPath.Data, firstAlias.Data) || firstPath.NextCursor == nil || firstAlias.NextCursor == nil {
		t.Fatal("path and exact identity did not read the same actual first page")
	}
	afterPath := mediaHTTPPage(t, mediaHTTPGet(f, alias+"&cursor="+url.QueryEscape(*firstPath.NextCursor)))
	afterAlias := mediaHTTPPage(t, mediaHTTPGet(f, path+"&cursor="+url.QueryEscape(*firstAlias.NextCursor)))
	if len(afterPath.Data) != 1 || !reflect.DeepEqual(afterPath.Data, afterAlias.Data) || afterPath.Data[0].ID == firstPath.Data[0].ID {
		t.Fatal("path and exact identity did not share the same continuation purpose")
	}
	for _, invalid := range []string{
		"/api/v1/media-by-identity/links?id=links&limit=1&cursor=" + url.QueryEscape(*firstPath.NextCursor),
		"/api/v1/media-by-identity/links?id=photo&limit=2&cursor=" + url.QueryEscape(*firstPath.NextCursor),
		"/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.owner.ID) + "&limit=1&cursor=" + url.QueryEscape(*firstPath.NextCursor),
	} {
		expectError(t, mediaHTTPGet(f, invalid), http.StatusBadRequest, "INVALID_CURSOR")
	}
	original := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
	aliasImage := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media-by-identity?id=photo"), "photo", 7)
	if !reflect.DeepEqual(original, aliasImage) {
		t.Fatal("exact identity lookup returned different Media source or revision")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media-by-identity?id=links"), "links", 3)
	reserved := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(reserved.Data) != 1 || reserved.Data[0].ID != "reserved" {
		t.Fatal("exact identity routes changed existing reserved-ID dispatch")
	}
}

func TestMediaSourceIdentityHTTPAttachmentReplaysAcrossFormsAfterOwnerRemoval(t *testing.T) {
	for _, aliasFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(aliasFirst), func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			path, alias := "/api/v1/media/empty-media/links", "/api/v1/media-by-identity/links?id=empty-media"
			if aliasFirst {
				path, alias = alias, path
			}
			body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
			key := "exact-media-cross-form"
			send := func(target, payload, proof string) *http.Request {
				return domainRequest(http.MethodPost, target, payload, f.cookie, f.csrf, key, proof)
			}
			first := respond(f.handler, send(path, body, `"1"`))
			var link store.MediaLink
			if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &link) != nil || link.MediaID != "empty-media" || link.Revision != 1 {
				t.Fatalf("first attachment failed: %d %s", first.Code, first.Body.String())
			}
			replay := respond(f.handler, send(alias, body, `"1"`))
			if replay.Code != http.StatusOK || replay.Header().Get("ETag") != `"1"` || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
				t.Fatal("cross-form attachment did not reuse the original receipt")
			}
			expectError(t, respond(f.handler, send("/api/v1/media-by-identity/links?id=photo", body, `"1"`)), 409, "IDEMPOTENCY_CONFLICT")
			expectError(t, respond(f.handler, send(alias, body, `"2"`)), 409, "IDEMPOTENCY_CONFLICT")
			changed := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d,"is_cover":false}`, f.empty.ID, f.empty.Revision)
			expectError(t, respond(f.handler, send(alias, changed, `"1"`)), 409, "IDEMPOTENCY_CONFLICT")
			deleted := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/items/"+f.empty.ID, "", f.cookie, f.csrf, "exact-media-remove-owner", `"2"`))
			if deleted.Code != http.StatusOK {
				t.Fatalf("owner deletion failed: %d %s", deleted.Code, deleted.Body.String())
			}
			f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
			replay = respond(f.handler, send(alias, body, `"1"`))
			if replay.Code != http.StatusOK || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) || replay.Header().Get("ETag") != `"1"` {
				t.Fatal("fresh-session replay required current owner source")
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media-by-identity/links?id=empty-media"))
			if len(page.Data) != 0 || page.NextCursor != nil {
				t.Fatal("original-key reconciliation recreated a removed owner link")
			}
		})
	}
}

func TestMediaSourceIdentityHTTPRejectsAmbiguousQueries(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/media-by-identity"},
		{http.MethodGet, "/api/v1/media-by-identity/links"},
		{http.MethodPost, "/api/v1/media-by-identity/links"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			body := ""
			if route.method == http.MethodPost {
				body = fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
			}
			invalid := []string{"", "id=", "id=photo&id=links", "id=photo&extra=1", "id=%", "id=%FF", "id=%ED%A0%80", "id=" + strings.Repeat("a", 129), "id=" + strings.Repeat("%41", 5462)}
			if route.path == "/api/v1/media-by-identity/links" && route.method == http.MethodGet {
				invalid = append(invalid, "id=photo&limit=0", "id=photo&limit=101", "id=photo&limit=1&limit=2", "id=photo&cursor=", "id=photo&cursor="+strings.Repeat("x", 2049), "id=photo&cursor=a&cursor=b")
			} else {
				invalid = append(invalid, "id=photo&limit=1", "id=photo&cursor=a")
			}
			for _, raw := range invalid {
				request := domainRequest(route.method, route.path, body, f.cookie, f.csrf, "exact-media-invalid-query", `"1"`)
				request.URL.RawQuery = raw
				expectError(t, respond(f.handler, request), http.StatusBadRequest, "INVALID_QUERY")
			}
			mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
		})
	}
}

func TestMediaIdentityPageQueryHonorsExactRawAndDecodedBudgets(t *testing.T) {
	id := strings.Repeat("😀", 128)
	query := "id=" + url.QueryEscape(id) + "&cursor=" + strings.Repeat("%41", 2048)
	query += strings.Repeat("&", (16<<10)-len(query))
	request := apiRequest(http.MethodGet, "/api/v1/media-by-identity/links", "")
	request.URL.RawQuery = query
	response := httptest.NewRecorder()
	actual, cursor, limit, ok := mediaIdentityPageQuery(response, request, "request")
	if !ok || actual != id || cursor != strings.Repeat("A", 2048) || limit != 50 {
		t.Fatalf("exact 16-KiB raw/2048-byte cursor boundary rejected: %d", response.Code)
	}
	request.URL.RawQuery += "&"
	response = httptest.NewRecorder()
	if _, _, _, ok := mediaIdentityPageQuery(response, request, "request"); ok || response.Code != http.StatusBadRequest {
		t.Fatal("over-budget raw query was accepted", response.Code)
	}
}

func FuzzMediaIdentityPageQuery(f *testing.F) {
	for _, query := range []string{"id=photo", "id=photo&limit=1&cursor=opaque", "id=media%2F%00%F0%9F%98%80", "id=..", "id=%FF", "id=a&id=b", "id=photo&limit=101"} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 20<<10 {
			t.Skip()
		}
		request := apiRequest(http.MethodGet, "/api/v1/media-by-identity/links", "")
		request.URL.RawQuery = raw
		response := httptest.NewRecorder()
		id, cursor, limit, ok := mediaIdentityPageQuery(response, request, "request")
		if !ok {
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid query returned %d", response.Code)
			}
			return
		}
		values, err := url.ParseQuery(raw)
		if err != nil || len(raw) > 16<<10 || len(values["id"]) != 1 || values.Get("id") != id || !boundedString(id, 1, 128) || limit < 1 || limit > 100 {
			t.Fatal("accepted query lost its exact bounded Media identity")
		}
		for name, entries := range values {
			if (name != "id" && name != "limit" && name != "cursor") || len(entries) != 1 {
				t.Fatal("accepted duplicate or unknown query parameter")
			}
		}
		if values.Get("cursor") != cursor || len(cursor) > 2048 {
			t.Fatal("accepted query changed or exceeded its cursor")
		}
	})
}

func TestMediaSourceIdentityHTTPEnforcesAdmissionAndBodyLimits(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
	path := "/api/v1/media-by-identity/links?id=empty-media"
	for _, tc := range []struct {
		name   string
		status int
		code   string
		edit   func(*http.Request)
	}{
		{"missing session", 401, "AUTHENTICATION_REQUIRED", func(r *http.Request) { r.Header.Del("Cookie") }},
		{"missing origin", 403, "ORIGIN_REJECTED", func(r *http.Request) { r.Header.Del("Origin") }},
		{"foreign origin", 403, "ORIGIN_REJECTED", func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }},
		{"missing csrf", 403, "CSRF_REJECTED", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"duplicate csrf", 403, "CSRF_REJECTED", func(r *http.Request) { r.Header.Add("X-CSRF-Token", f.csrf) }},
		{"missing version", 426, "CLIENT_UPGRADE_REQUIRED", func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }},
		{"missing key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"duplicate key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Add("Idempotency-Key", "other-exact-key") }},
		{"missing proof", 409, "PRECONDITION_REQUIRED", func(r *http.Request) { r.Header.Del("If-Match") }},
		{"weak proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Set("If-Match", `W/"1"`) }},
		{"duplicate proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }},
		{"encoded path", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.RawPath = "/api/v1/media-by-identity/%6cinks" }},
		{"encoded body", 415, "UNSUPPORTED_MEDIA_TYPE", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "exact-media-admission", `"1"`)
			tc.edit(request)
			response := respond(f.handler, request)
			expectError(t, response, tc.status, tc.code)
			if response.Header().Get("ETag") != "" {
				t.Fatal("rejected attachment invented a link ETag")
			}
		})
	}
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"item_id":"a","item_id":"b","owner_revision":1}`, 400, "INVALID_JSON"},
		{`{"item_id":"a","owner_revision":1,"extra":true}`, 422, "VALIDATION_ERROR"},
		{`{"item_id":null,"owner_revision":1}`, 422, "VALIDATION_ERROR"},
		{`{"item_id":"a","owner_revision":1.1}`, 422, "VALIDATION_ERROR"},
		{body + strings.Repeat(" ", mediaLinkBodyLimit), 413, "BODY_TOO_LARGE"},
	} {
		expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, tc.body, f.cookie, f.csrf, "exact-media-invalid-json", `"1"`)), tc.status, tc.code)
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "exact-media-stale-proof", `"2"`)), 409, "REVISION_CONFLICT")
	for _, readPath := range []string{"/api/v1/media-by-identity?id=empty-media", path} {
		request := domainRequest(http.MethodGet, readPath, "unexpected", f.cookie, f.csrf, "", "")
		expectError(t, respond(f.handler, request), 400, "INVALID_BODY")
		request = domainRequest(http.MethodGet, readPath, "", f.cookie, f.csrf, "", "")
		request.Header.Set("Content-Encoding", "gzip")
		expectError(t, respond(f.handler, request), 400, "INVALID_BODY")
		request = domainRequest(http.MethodGet, readPath, "", nil, "", "", "")
		expectError(t, respond(f.handler, request), 401, "AUTHENTICATION_REQUIRED")
	}
	for _, target := range []string{"/api/v1/media-by-identity?id=absent", "/api/v1/media-by-identity/links?id=absent"} {
		expectError(t, mediaHTTPGet(f, target), 404, "NOT_FOUND")
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media-by-identity/links?id=absent", body, f.cookie, f.csrf, "exact-media-absent", `"1"`)), 404, "NOT_FOUND")
	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodPost, "/api/v1/media-by-identity?id=empty-media", http.MethodGet},
		{http.MethodPatch, path, "GET, POST"},
		{http.MethodDelete, path, "GET, POST"},
	} {
		response := respond(f.handler, domainRequest(tc.method, tc.path, body, f.cookie, f.csrf, "exact-media-method", `"1"`))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != tc.allow {
			t.Fatal("wrong supported methods", response.Header().Get("Allow"))
		}
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", 1)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("rejected identity requests changed actual membership")
	}
}

func TestMediaSourceIdentityHTTPAttachmentRevalidatesAfterBodyRead(t *testing.T) {
	for _, change := range []string{"revoked", "owner", "media"} {
		t.Run(change, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
			request := domainRequest(http.MethodPost, "/api/v1/media-by-identity/links?id=empty-media", body, f.cookie, f.csrf, "exact-media-body-race", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				switch change {
				case "revoked":
					if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "exact-media-revoke-write"); err != nil {
						t.Fatal(err)
					}
				case "owner":
					catalog, err := store.NewCatalog(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					name := "Changed owner"
					if _, err = catalog.Patch(t.Context(), f.cookie.Value, "exact-media-change-owner", f.empty.ID, f.empty.Revision, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				case "media":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = library.Attach(t.Context(), f.cookie.Value, "exact-media-change-source", "empty-media", 1, store.MediaLinkCreate{ItemUnitID: &f.unit.ID, OwnerRevision: f.unit.Revision}); err != nil {
						t.Fatal(err)
					}
				}
			}})
			response := respond(f.handler, request)
			if change == "revoked" {
				expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
				f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
			} else {
				expectError(t, response, 409, "REVISION_CONFLICT")
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.empty.ID)))
			if len(page.Data) != 0 {
				t.Fatal("stale or revoked attachment modified the reviewed owner")
			}
		})
	}
}

func TestMediaSourceIdentityHTTPRechecksSessionAfterBodyRead(t *testing.T) {
	for _, route := range []string{"/api/v1/media-by-identity?id=empty-media", "/api/v1/media-by-identity/links?id=empty-media"} {
		t.Run(route, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			request := domainRequest(http.MethodGet, route, "", f.cookie, f.csrf, "", "")
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "exact-media-revoke-read"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, request), http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
		})
	}
}
