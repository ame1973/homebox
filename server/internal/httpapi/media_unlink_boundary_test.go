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

func TestMediaLinkUnlinkHTTPUpdatesEachActualOwnerKind(t *testing.T) {
	for _, kind := range []string{"item", "unit", "location"} {
		t.Run(kind, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			id, ownerID, remaining := "item-000", f.owner.ID, 50
			switch kind {
			case "unit":
				id, ownerID, remaining = "unit", f.unit.ID, 0
			case "location":
				id, ownerID, remaining = "location", f.location.ID, 0
			}
			response := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/media/links/"+id, "", f.cookie, f.csrf, "media-unlink-owner-kind", `"1"`))
			var ack store.ActionAck
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &ack) != nil || !ack.OK || ack.OperationID == "" || ack.Revision != 2 || response.Header().Get("ETag") != "" {
				t.Fatal("actual owner link was not independently removed", kind, response.Code)
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type="+kind+"&owner_id="+url.QueryEscape(ownerID)+"&limit=100"))
			if len(page.Data) != remaining || page.NextCursor != nil {
				t.Fatal("unlink removed or invented another owner's membership", kind, len(page.Data))
			}
			for _, member := range page.Data {
				if member.ID == id || member.Media.Revision != 8 {
					t.Fatal("selected link survived or remaining Media revision was stale", kind)
				}
			}
			mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
			reserved := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
			if len(reserved.Data) != 1 || reserved.Data[0].ID != "reserved" {
				t.Fatal("unlink removed an unrelated shared Media membership")
			}
		})
	}
}

func TestMediaLinkUnlinkHTTPOriginalKeySurvivesOwnerDeletionAndFreshLogin(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
	attached := respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", body, f.cookie, f.csrf, "media-unlink-replay-attach", `"1"`))
	var link store.MediaLink
	if attached.Code != 200 || json.Unmarshal(attached.Body.Bytes(), &link) != nil || link.Revision != 1 {
		t.Fatal("actual attachment for unlink replay failed", attached.Code)
	}
	path := "/api/v1/media/links/" + link.ID
	request := func(key string) *http.Request {
		return domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, key, `"1"`)
	}
	first := respond(f.handler, request("media-unlink-after-owner-delete"))
	var ack store.ActionAck
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 2 || first.Header().Get("ETag") != "" {
		t.Fatal("original unlink did not commit", first.Code)
	}
	deleted := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/items/"+f.empty.ID, "", f.cookie, f.csrf, "media-unlinked-owner-delete", `"3"`))
	if deleted.Code != 200 || deleted.Header().Get("ETag") != "" {
		t.Fatal("actual unlinked owner deletion failed", deleted.Code)
	}
	f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	replay := respond(f.handler, request("media-unlink-after-owner-delete"))
	if replay.Code != 200 || replay.Header().Get("ETag") != "" || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("fresh-login original-key replay required the deleted link or owner")
	}
	expectError(t, respond(f.handler, request("media-unlink-new-after-owner-delete")), 404, "NOT_FOUND")
	image := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", 3)
	if image.Verification != "unverified" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
		t.Fatal("unlink or replay invented image verification")
	}
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("original-key replay recreated the deleted membership")
	}
}

func TestMediaLinkUnlinkHTTPRevalidatesAfterEmptyBodyReading(t *testing.T) {
	for _, change := range []string{"revoked", "link", "owner", "media"} {
		t.Run(change, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			request := domainRequest(http.MethodDelete, "/api/v1/media/links/item-000", "", f.cookie, f.csrf, "media-unlink-body-proof", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				switch change {
				case "revoked":
					if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "media-unlink-body-revoke"); err != nil {
						t.Fatal(err)
					}
				case "link":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					description := "Concurrent edit"
					if _, err = library.PatchLink(t.Context(), f.cookie.Value, "media-unlink-body-link", "item-000", 1, store.MediaLinkPatch{Description: &description}); err != nil {
						t.Fatal(err)
					}
				case "owner":
					catalog, err := store.NewCatalog(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					name := "Concurrent owner"
					if _, err = catalog.Patch(t.Context(), f.cookie.Value, "media-unlink-body-owner", f.owner.ID, f.owner.Revision, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				case "media":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					input := store.MediaLinkCreate{ItemID: &f.empty.ID, OwnerRevision: f.empty.Revision}
					if _, err = library.Attach(t.Context(), f.cookie.Value, "media-unlink-body-media", "photo", 7, input); err != nil {
						t.Fatal(err)
					}
				}
			}})
			response := respond(f.handler, request)
			revision, shouldRemain := int64(8), false
			switch change {
			case "revoked":
				expectError(t, response, 401, "AUTHENTICATION_REQUIRED")
				f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
				revision, shouldRemain = 7, true
			case "link":
				expectError(t, response, 409, "REVISION_CONFLICT")
				shouldRemain = true
			case "owner", "media":
				var ack store.ActionAck
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 2 || response.Header().Get("ETag") != "" {
					t.Fatal("an unrelated aggregate change was incorrectly used as an unlink proof", response.Code)
				}
				if change == "media" {
					revision = 9
				}
			}
			mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", revision)
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)+"&limit=100"))
			found := false
			for _, member := range page.Data {
				if member.ID == "item-000" {
					found = true
					if change == "link" && (member.Revision != 2 || member.Description != "Concurrent edit") {
						t.Fatal("stale unlink overwrote the concurrent link edit")
					}
				}
			}
			if found != shouldRemain {
				t.Fatal("body-time authorization or proof check changed the wrong link", change)
			}
		})
	}
}

func TestMediaLinkUnlinkHTTPDispatchesReservedLinkIDsByMethod(t *testing.T) {
	f := newMediaHTTPFixture(t, "links", "view")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	first := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/media/links/links", "", f.cookie, f.csrf, "media-unlink-reserved-links", `"1"`))
	var firstAck store.ActionAck
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &firstAck) != nil || !firstAck.OK || firstAck.Revision != 2 || first.Header().Get("ETag") != "" {
		t.Fatal("DELETE reserved link ID was dispatched as Media membership", first.Code)
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(page.Data) != 1 || page.Data[0].ID != "reserved" {
		t.Fatal("DELETE link ID links damaged reserved Media membership")
	}
	patch := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links/view", `{"description":"Reviewed view link"}`, f.cookie, f.csrf, "media-patch-reserved-before-unlink", `"1"`))
	var link store.MediaLink
	if patch.Code != 200 || json.Unmarshal(patch.Body.Bytes(), &link) != nil || link.ID != "view" || link.MediaID != "photo" || link.Revision != 2 {
		t.Fatal("reserved view link PATCH was shadowed", patch.Code)
	}
	second := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/media/links/view", "", f.cookie, f.csrf, "media-unlink-reserved-view", `"2"`))
	var secondAck store.ActionAck
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &secondAck) != nil || !secondAck.OK || secondAck.Revision != 3 || second.Header().Get("ETag") != "" {
		t.Fatal("DELETE reserved view link was dispatched as a Media read", second.Code)
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 10)
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	body := fmt.Sprintf(`{"location_id":%q,"owner_revision":%d}`, f.location.ID, f.location.Revision)
	attached := respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/links/links", body, f.cookie, f.csrf, "media-attach-after-reserved-unlink", `"3"`))
	var created store.MediaLink
	if attached.Code != 200 || json.Unmarshal(attached.Body.Bytes(), &created) != nil || created.MediaID != "links" || created.OwnerID != f.location.ID || created.Revision != 1 {
		t.Fatal("reserved Media attachment was shadowed by unlink", attached.Code)
	}
	page = mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(page.Data) != 2 || page.Data[0].MediaID != "links" || page.Data[1].MediaID != "links" {
		t.Fatal("reserved Media membership changed meaning after unlink and attachment")
	}
	for _, tc := range []struct{ path, allow string }{
		{"/api/v1/media/links/view", "GET, PATCH, DELETE"},
		{"/api/v1/media/links/links", "GET, POST, PATCH, DELETE"},
		{"/api/v1/media/links/item-000", "PATCH, DELETE"},
		{"/api/v1/media/photo", "GET"},
		{"/api/v1/media/photo/links", "GET, POST"},
	} {
		response := respond(f.handler, domainRequest(http.MethodHead, tc.path, "", f.cookie, f.csrf, "", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != tc.allow {
			t.Fatal("incorrect methods for ambiguous Media path", tc.path, response.Header().Get("Allow"))
		}
	}
	for _, path := range []string{"/api/v1/media/photo", "/api/v1/media/photo/links"} {
		expectError(t, respond(f.handler, domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, "media-unimplemented-global-delete", `"1"`)), 405, "METHOD_NOT_ALLOWED")
	}
}

func TestMediaLinkUnlinkHTTPAdmissionAndCompletedSession(t *testing.T) {
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
		{"announced body", 400, "INVALID_BODY", func(r *http.Request) { r.ContentLength = 1 }},
		{"unknown-length body", 400, "INVALID_BODY", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 }},
		{"encoded body", 400, "INVALID_BODY", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := domainRequest(http.MethodDelete, "/api/v1/media/links/item-000", "", f.cookie, f.csrf, "media-unlink-admission", `"1"`)
			tc.edit(request)
			response := respond(f.handler, request)
			expectError(t, response, tc.status, tc.code)
			if response.Header().Get("ETag") != "" {
				t.Fatal("rejected unlink invented an ETag")
			}
		})
	}
	for _, path := range []string{"/api/v1/media/links/.", "/api/v1/media/links/..", "/api/v1/media/links/item\\bad", "/api/v1/media/links/" + strings.Repeat("界", 129)} {
		expectError(t, respond(f.handler, domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, "media-unlink-invalid-path", `"1"`)), 400, "INVALID_MEDIA_URL")
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/media/links/missing", "", f.cookie, f.csrf, "media-unlink-missing", `"1"`)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)))
	if len(page.Data) != 50 || page.Data[0].ID != "item-000" || page.Data[0].Revision != 1 {
		t.Fatal("rejected unlinks changed the actual owner membership")
	}

	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	request := domainRequest(http.MethodDelete, "/api/v1/media/links/link", "", cookie, csrf, "media-unlink-incomplete", `"1"`)
	expectError(t, respond(authHandler(t, auth), request), 403, "PASSWORD_CHANGE_REQUIRED")
}
