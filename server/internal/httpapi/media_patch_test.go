package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestMediaLinkPatchHTTPUsesIndependentOriginalRowProof(t *testing.T) {
	f := newMediaHTTPFixture(t)
	path, body := "/api/v1/media/links/sibling", `{"description":"Reviewed caption"}`
	request := func(key, proof, body string) *http.Request {
		return domainRequest(http.MethodPatch, path, body, f.cookie, f.csrf, key, proof)
	}
	first := respond(f.handler, request("media-http-link-patch", `"5"`, body))
	var link store.MediaLink
	var fields map[string]json.RawMessage
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &link) != nil || json.Unmarshal(first.Body.Bytes(), &fields) != nil || len(fields) != 8 ||
		link.ID != "sibling" || link.MediaID != "photo" || link.OwnerID != f.sibling.ID || link.OwnerType != "item" ||
		link.Revision != 6 || !link.IsCover || link.SortOrder != -3 || link.Description != "Reviewed caption" || first.Header().Get("ETag") != `"6"` ||
		first.Header().Get("Cache-Control") != "no-store" || first.Header().Get("X-Content-Type-Options") != "nosniff" || first.Header().Get("Set-Cookie") != "" {
		t.Fatalf("actual link edit was not exposed under its independent proof: %d %s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "private-storage-key") || strings.Contains(first.Body.String(), "NeverPublishUnreviewedMediaOCR") || fields["media"] != nil {
		t.Fatal("link edit disclosed private data or embedded a separately versioned Media")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.sibling.ID)))
	found := false
	for _, member := range page.Data {
		if member.ID == link.ID {
			found = member.Revision == 6 && member.Description == link.Description && member.Media.Revision == 8
		}
	}
	if !found {
		t.Fatal("actual owner page did not reflect the independent edited link")
	}
	replay := respond(f.handler, request("media-http-link-patch", `"5"`, body))
	if replay.Code != 200 || replay.Header().Get("ETag") != `"6"` || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("original-key retry lost its frozen link reply")
	}
	expectError(t, respond(f.handler, request("media-http-link-patch", `"5"`, `{"description":"Reviewed caption","is_cover":true}`)), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, respond(f.handler, request("media-link-stale", `"5"`, body)), 409, "REVISION_CONFLICT")
	equal := respond(f.handler, request("media-link-equal", `"6"`, body))
	if equal.Code != 200 || equal.Header().Get("ETag") != `"6"` || !bytes.Equal(first.Body.Bytes(), equal.Body.Bytes()) {
		t.Fatal("equal intent changed the independent row revision")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
}

func TestMediaLinkPatchHTTPUpdatesEachActualOwnerKind(t *testing.T) {
	for _, kind := range []string{"item", "unit", "location"} {
		t.Run(kind, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			id, ownerID := "item-000", f.owner.ID
			switch kind {
			case "unit":
				id, ownerID = "unit", f.unit.ID
			case "location":
				id, ownerID = "location", f.location.ID
			}
			response := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/"+id,
				`{"is_cover":false,"sort_order":0,"description":""}`, f.cookie, f.csrf, "media-edit-owner-kind", `"1"`))
			var link store.MediaLink
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.ID != id || link.OwnerType != kind || link.OwnerID != ownerID ||
				link.Revision != 2 || link.IsCover || link.SortOrder != 0 || link.Description != "" || response.Header().Get("ETag") != `"2"` {
				t.Fatalf("explicit false/zero/empty edit failed for %s: %d %s", kind, response.Code, response.Body.String())
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type="+kind+"&owner_id="+url.QueryEscape(ownerID)))
			found := false
			for _, member := range page.Data {
				if member.ID == id {
					found = member.Revision == 2 && !member.IsCover && member.SortOrder == 0 && member.Description == "" && member.Media.Revision == 8
				}
			}
			if !found {
				t.Fatal("selected owner page did not show the actual changed link")
			}
		})
	}
}

func TestMediaLinkPatchHTTPCoverConflictsAndOriginalKeyAfterOwnerDeletion(t *testing.T) {
	f := newMediaHTTPFixture(t)
	clear := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/item-000", `{"is_cover":false}`, f.cookie, f.csrf, "media-cover-clear", `"1"`))
	if clear.Code != 200 || clear.Header().Get("ETag") != `"2"` {
		t.Fatal("explicit legacy cover clearing failed", clear.Code)
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/item-000", `{"is_cover":true}`, f.cookie, f.csrf, "media-cover-conflict", `"2"`)), 409, "MEDIA_COVER_CONFLICT")
	existing := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/item-001", `{"description":"Existing cover"}`, f.cookie, f.csrf, "media-existing-cover-edit", `"1"`))
	if existing.Code != 200 || existing.Header().Get("ETag") != `"2"` {
		t.Fatal("editing an existing legacy duplicate cover was rejected", existing.Code)
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 9)

	attachBody := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
	attachment := respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", attachBody, f.cookie, f.csrf, "media-edit-replay-attach", `"1"`))
	var created store.MediaLink
	if attachment.Code != 200 || json.Unmarshal(attachment.Body.Bytes(), &created) != nil || created.Revision != 1 {
		t.Fatal("attachment for deletion replay failed", attachment.Code)
	}
	path, body := "/api/v1/media/links/"+created.ID, `{"description":"Retained receipt"}`
	request := func(key, input string) *http.Request {
		return domainRequest(http.MethodPatch, path, input, f.cookie, f.csrf, key, `"1"`)
	}
	first := respond(f.handler, request("media-edit-after-delete", body))
	if first.Code != 200 || first.Header().Get("ETag") != `"2"` {
		t.Fatal("original edit failed", first.Code)
	}
	deleted := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/items/"+f.empty.ID, "", f.cookie, f.csrf, "media-edited-owner-delete", `"3"`))
	if deleted.Code != 200 || deleted.Header().Get("ETag") != "" {
		t.Fatal("actual owner deletion failed", deleted.Code)
	}
	f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	replay := respond(f.handler, request("media-edit-after-delete", body))
	if replay.Code != 200 || replay.Header().Get("ETag") != `"2"` || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("fresh-login original-key edit replay required the deleted link")
	}
	expectError(t, respond(f.handler, request("media-edit-after-delete", `{"description":"Different"}`)), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, respond(f.handler, request("media-new-edit-after-delete", body)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", 4)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("frozen edit replay recreated deleted membership")
	}
}

func TestMediaLinkPatchHTTPRevalidatesAfterBodyReading(t *testing.T) {
	for _, change := range []string{"revoked", "link", "owner", "media"} {
		t.Run(change, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			request := domainRequest(http.MethodPatch, "/api/v1/media/links/item-000", `{"description":"Reviewed"}`, f.cookie, f.csrf, "media-edit-body-proof", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				switch change {
				case "revoked":
					if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "media-edit-body-revoke"); err != nil {
						t.Fatal(err)
					}
				case "link":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					description := "Concurrent edit"
					if _, err = library.PatchLink(t.Context(), f.cookie.Value, "media-edit-body-concurrent", "item-000", 1, store.MediaLinkPatch{Description: &description}); err != nil {
						t.Fatal(err)
					}
				case "owner":
					catalog, err := store.NewCatalog(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					name := "Concurrent owner"
					if _, err = catalog.Patch(t.Context(), f.cookie.Value, "media-edit-body-owner", f.owner.ID, f.owner.Revision, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				case "media":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					input := store.MediaLinkCreate{ItemID: &f.empty.ID, OwnerRevision: f.empty.Revision}
					if _, err = library.Attach(t.Context(), f.cookie.Value, "media-edit-body-media", "photo", 7, input); err != nil {
						t.Fatal(err)
					}
				}
			}})
			response := respond(f.handler, request)
			revision := int64(8)
			switch change {
			case "revoked":
				expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
				f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
				revision = 7
			case "link":
				expectError(t, response, 409, "REVISION_CONFLICT")
			case "owner", "media":
				var link store.MediaLink
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.ID != "item-000" || link.Description != "Reviewed" || link.Revision != 2 || response.Header().Get("ETag") != `"2"` {
					t.Fatal("an unrelated aggregate change was incorrectly used as a link proof", response.Code)
				}
				if change == "media" {
					revision = 9
				}
			}
			mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", revision)
		})
	}
}

func TestMediaLinkPatchHTTPDispatchesReservedLinkIDsByMethod(t *testing.T) {
	f := newMediaHTTPFixture(t, "links", "view")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(page.Data) != 1 || page.Data[0].ID != "reserved" {
		t.Fatal("reserved Media membership did not select its actual links")
	}
	for _, id := range []string{"links", "view"} {
		response := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/"+id,
			fmt.Sprintf(`{"description":%q}`, "Edited "+id), f.cookie, f.csrf, "media-reserved-link-"+id, `"1"`))
		var link store.MediaLink
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.ID != id || link.MediaID != "photo" || link.OwnerID != f.empty.ID ||
			link.Description != "Edited "+id || link.Revision != 2 || response.Header().Get("ETag") != `"2"` {
			t.Fatal("reserved link ID was dispatched as a Media read or attachment", id, response.Code)
		}
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 9)
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	body := fmt.Sprintf(`{"location_id":%q,"owner_revision":%d}`, f.location.ID, f.location.Revision)
	attached := respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/links/links", body, f.cookie, f.csrf, "media-reserved-after-patch", `"3"`))
	var newLink store.MediaLink
	if attached.Code != 200 || json.Unmarshal(attached.Body.Bytes(), &newLink) != nil || newLink.MediaID != "links" || newLink.OwnerID != f.location.ID || newLink.Revision != 1 {
		t.Fatal("reserved Media attachment was shadowed by link PATCH", attached.Code)
	}
	page = mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(page.Data) != 2 || page.Data[0].MediaID != "links" || page.Data[1].MediaID != "links" {
		t.Fatal("reserved Media membership changed meaning after PATCH and POST")
	}
	for _, tc := range []struct{ path, allow string }{
		{"/api/v1/media/links/view", "GET, PATCH, DELETE"},
		{"/api/v1/media/links/links", "GET, POST, PATCH, DELETE"},
		{"/api/v1/media/links/item-000", "PATCH, DELETE"},
		{"/api/v1/media/photo/links", "GET, POST"},
	} {
		response := respond(f.handler, domainRequest(http.MethodHead, tc.path, "", f.cookie, f.csrf, "", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != tc.allow {
			t.Fatal("incorrect methods for ambiguous Media path", tc.path, response.Header().Get("Allow"))
		}
	}
	expectError(t, mediaHTTPGet(f, "/api/v1/media/links/item-000"), 404, "NOT_FOUND")
}

func TestMediaLinkPatchHTTPAdmissionAndCompletedSession(t *testing.T) {
	f := newMediaHTTPFixture(t)
	for _, tc := range []struct {
		name   string
		status int
		code   string
		edit   func(*http.Request)
	}{
		{"missing cookie", 401, "AUTHENTICATION_REQUIRED", func(r *http.Request) { r.Header.Del("Cookie") }},
		{"missing origin", 403, "ORIGIN_REJECTED", func(r *http.Request) { r.Header.Del("Origin") }},
		{"foreign origin", 403, "ORIGIN_REJECTED", func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }},
		{"duplicate origin", 403, "ORIGIN_REJECTED", func(r *http.Request) { r.Header.Add("Origin", "https://homebox.test") }},
		{"missing csrf", 403, "CSRF_REJECTED", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
		{"invalid csrf", 403, "CSRF_REJECTED", func(r *http.Request) { r.Header.Set("X-CSRF-Token", "invalid") }},
		{"duplicate csrf", 403, "CSRF_REJECTED", func(r *http.Request) { r.Header.Add("X-CSRF-Token", f.csrf) }},
		{"missing version", 426, "CLIENT_UPGRADE_REQUIRED", func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }},
		{"unsupported version", 426, "CLIENT_UPGRADE_REQUIRED", func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "9.0.0") }},
		{"missing key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"duplicate key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-media-key") }},
		{"missing proof", 409, "PRECONDITION_REQUIRED", func(r *http.Request) { r.Header.Del("If-Match") }},
		{"weak proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Set("If-Match", `W/"1"`) }},
		{"zero proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Set("If-Match", `"0"`) }},
		{"duplicate proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }},
		{"query", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.RawQuery = "limit=1" }},
		{"empty query", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.ForceQuery = true }},
		{"encoded path", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.RawPath = "/api/v1/media/links/item-%30%30%30" }},
		{"encoded body", 415, "UNSUPPORTED_MEDIA_TYPE", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := domainRequest(http.MethodPatch, "/api/v1/media/links/item-000", `{"description":"Never committed"}`, f.cookie, f.csrf, "media-edit-admission", `"1"`)
			tc.edit(request)
			response := respond(f.handler, request)
			expectError(t, response, tc.status, tc.code)
			if response.Header().Get("ETag") != "" {
				t.Fatal("rejected edit invented a link ETag")
			}
		})
	}
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{}`, 422, "VALIDATION_ERROR"},
		{`{"owner_revision":1}`, 422, "VALIDATION_ERROR"},
		{`{"is_cover":null}`, 422, "VALIDATION_ERROR"},
		{`{"sort_order":9007199254740992}`, 422, "VALIDATION_ERROR"},
		{`{"description":"x","description":"y"}`, 400, "INVALID_JSON"},
		{`{"description":"\ud800"}`, 400, "INVALID_JSON"},
		{`{"description":"x"} trailing`, 400, "INVALID_JSON"},
		{`{"description":"x"}` + strings.Repeat(" ", mediaLinkBodyLimit), 413, "BODY_TOO_LARGE"},
	} {
		request := domainRequest(http.MethodPatch, "/api/v1/media/links/item-000", tc.body, f.cookie, f.csrf, "media-edit-invalid-json", `"1"`)
		response := respond(f.handler, request)
		expectError(t, response, tc.status, tc.code)
		if response.Header().Get("ETag") != "" {
			t.Fatal("invalid link JSON invented an ETag")
		}
	}
	for _, path := range []string{"/api/v1/media/links/.", "/api/v1/media/links/..", "/api/v1/media/links/item\\bad", "/api/v1/media/links/" + strings.Repeat("界", 129)} {
		expectError(t, respond(f.handler, domainRequest(http.MethodPatch, path, `{"description":"x"}`, f.cookie, f.csrf, "media-edit-invalid-path", `"1"`)), 400, "INVALID_MEDIA_URL")
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/missing", `{"description":"x"}`, f.cookie, f.csrf, "media-edit-missing", `"1"`)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)))
	if len(page.Data) != 50 || page.Data[0].ID != "item-000" || page.Data[0].Revision != 1 || page.Data[0].Description != "\x00Exact é\n" {
		t.Fatal("rejected edits changed the actual link")
	}

	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	request := domainRequest(http.MethodPatch, "/api/v1/media/links/link", `{"description":"x"}`, cookie, csrf, "media-edit-incomplete", `"1"`)
	expectError(t, respond(authHandler(t, auth), request), 403, "PASSWORD_CHANGE_REQUIRED")
}
