package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestMediaAttachHTTPHandlesRealOwnerKindsAndReservedMediaID(t *testing.T) {
	for _, kind := range []string{"item", "unit", "location"} {
		t.Run(kind, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			mediaID, field, ownerID := "empty-media", "item_id", f.empty.ID
			mediaRevision, ownerRevision := int64(1), f.empty.Revision
			cover := true
			switch kind {
			case "unit":
				mediaID, field, ownerID = "photo", "item_unit_id", f.unit.ID
				mediaRevision, ownerRevision, cover = 7, f.unit.Revision, false
			case "location":
				mediaID, field, ownerID = "links", "location_id", f.location.ID
				mediaRevision, ownerRevision, cover = 3, f.location.Revision, false
			}
			body := fmt.Sprintf(`{"%s":%q,"owner_revision":%d,"is_cover":%t,"sort_order":9,"description":"Exact é"}`, field, ownerID, ownerRevision, cover)
			path := "/api/v1/media/" + mediaID + "/links"
			proof := strconv.Quote(strconv.FormatInt(mediaRevision, 10))
			request := func(key string) *http.Request {
				return domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, key, proof)
			}
			first := respond(f.handler, request("media-owner-kind-original"))
			var link store.MediaLink
			var fields map[string]json.RawMessage
			if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &link) != nil || json.Unmarshal(first.Body.Bytes(), &fields) != nil || len(fields) != 8 ||
				link.ID == "" || link.MediaID != mediaID || link.OwnerType != kind || link.OwnerID != ownerID || link.Revision != 1 ||
				link.IsCover != cover || link.SortOrder != 9 || link.Description != "Exact é" || first.Header().Get("ETag") != `"1"` ||
				first.Header().Get("Cache-Control") != "no-store" || first.Header().Get("X-Content-Type-Options") != "nosniff" || first.Header().Get("Set-Cookie") != "" {
				t.Fatalf("actual Media attachment returned an invalid independent resource: %d %s", first.Code, first.Body.String())
			}
			if strings.Contains(first.Body.String(), "private-storage-key") || strings.Contains(first.Body.String(), "NeverPublishUnreviewedMediaOCR") || fields["media"] != nil {
				t.Fatal("attachment disclosed private data or embedded a separately versioned Media")
			}
			metadataPath := "/api/v1/media/" + mediaID
			if mediaID == "links" {
				metadataPath += "/view"
			}
			image := mediaHTTPResource(t, mediaHTTPGet(f, metadataPath), mediaID, mediaRevision+1)
			if image.Verification != "unverified" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
				t.Fatal("attaching retained metadata invented file verification")
			}
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type="+kind+"&owner_id="+url.QueryEscape(ownerID)))
			found := false
			for _, member := range page.Data {
				if member.ID == link.ID {
					found = member.MediaID == mediaID && member.Media.Revision == mediaRevision+1 && member.Revision == 1
				}
			}
			if !found {
				t.Fatal("owner page omitted the actual independently versioned link")
			}
			replayed := respond(f.handler, request("media-owner-kind-original"))
			if replayed.Code != 200 || replayed.Header().Get("ETag") != `"1"` || !bytes.Equal(first.Body.Bytes(), replayed.Body.Bytes()) {
				t.Fatal("explicit original-key replay lost its frozen response")
			}
			expectError(t, respond(f.handler, request("media-owner-kind-new-key")), 409, "REVISION_CONFLICT")
		})
	}
}

func TestMediaAttachHTTPPreservesPathUnaddressableLegacyOwner(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body, err := json.Marshal(map[string]any{"item_id": f.legacyOwner, "owner_revision": 7})
	if err != nil {
		t.Fatal(err)
	}
	response := respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", string(body), f.cookie, f.csrf, "media-legacy-owner-attach", `"1"`))
	var link store.MediaLink
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.OwnerID != f.legacyOwner || link.OwnerType != "item" || link.Revision != 1 {
		t.Fatal("attachment lost a retained body-only owner ID or counter", response.Code)
	}
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.legacyOwner)))
	if len(page.Data) != 1 || page.Data[0].ID != link.ID || page.Data[0].OwnerID != f.legacyOwner {
		t.Fatal("owner-scoped query changed a retained slash/NUL/Unicode identity")
	}
}

func TestMediaAttachHTTPEnforcesBothProofsCoverAndOriginalKeyAfterOwnerDeletion(t *testing.T) {
	f := newMediaHTTPFixture(t)
	path := "/api/v1/media/photo/links"
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.owner.ID, f.owner.Revision)
	check := func(path, body, key, proof string) {
		t.Helper()
		expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, key, proof)), 409, "REVISION_CONFLICT")
	}
	check(path, body, "media-stale-media-proof", `"6"`)
	check(path, fmt.Sprintf(`{"item_id":%q,"owner_revision":2}`, f.owner.ID), "media-stale-owner-proof", `"7"`)
	cover := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d,"is_cover":true}`, f.owner.ID, f.owner.Revision)
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, cover, f.cookie, f.csrf, "media-existing-cover", `"7"`)), 409, "MEDIA_COVER_CONFLICT")
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, "/api/v1/media/missing/links", body, f.cookie, f.csrf, "media-missing-image", `"1"`)), 404, "NOT_FOUND")
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, `{"item_id":"missing","owner_revision":1}`, f.cookie, f.csrf, "media-missing-owner", `"7"`)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)

	path = "/api/v1/media/empty-media/links"
	body = fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
	first := respond(f.handler, domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "media-owner-delete-replay", `"1"`))
	var original store.MediaLink
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &original) != nil || original.Revision != 1 {
		t.Fatal("original attachment did not commit", first.Code)
	}
	changedPresence := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d,"is_cover":false}`, f.empty.ID, f.empty.Revision)
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, changedPresence, f.cookie, f.csrf, "media-owner-delete-replay", `"1"`)), 409, "IDEMPOTENCY_CONFLICT")
	deleted := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/items/"+f.empty.ID, "", f.cookie, f.csrf, "media-remove-attached-owner", `"2"`))
	if deleted.Code != 200 || deleted.Header().Get("ETag") != "" {
		t.Fatal("actual owner deletion did not remove only its link", deleted.Code)
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", 3)
	f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	replay := respond(f.handler, domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "media-owner-delete-replay", `"1"`))
	if replay.Code != 200 || replay.Header().Get("ETag") != `"1"` || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("fresh-login original-key replay required current source or changed its original response")
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPost, path, body, f.cookie, f.csrf, "media-new-key-after-delete", `"3"`)), 404, "NOT_FOUND")
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("replay recreated the deleted owner's membership")
	}
}

func TestMediaAttachHTTPRevalidatesAuthorizationAndProofsAfterBodyReading(t *testing.T) {
	for _, change := range []string{"revoked", "owner", "media"} {
		t.Run(change, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d}`, f.empty.ID, f.empty.Revision)
			request := domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", body, f.cookie, f.csrf, "media-body-reviewed-proof", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				switch change {
				case "revoked":
					if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "media-revoke-during-body"); err != nil {
						t.Fatal(err)
					}
				case "owner":
					catalog, err := store.NewCatalog(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					name := "Changed owner"
					if _, err = catalog.Patch(t.Context(), f.cookie.Value, "media-owner-during-body", f.empty.ID, f.empty.Revision, store.ItemPatch{Name: &name}); err != nil {
						t.Fatal(err)
					}
				case "media":
					library, err := store.NewMediaLibrary(f.auth)
					if err != nil {
						t.Fatal(err)
					}
					input := store.MediaLinkCreate{LocationID: &f.location.ID, OwnerRevision: f.location.Revision}
					if _, err = library.Attach(t.Context(), f.cookie.Value, "media-other-link-during-body", "empty-media", 1, input); err != nil {
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
			if len(page.Data) != 0 || page.NextCursor != nil {
				t.Fatal("rejected request created a link for its reviewed owner")
			}
			revision := int64(1)
			if change == "media" {
				revision = 2
				members := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
				if len(members.Data) != 1 || members.Data[0].OwnerID != f.location.ID {
					t.Fatal("actual concurrent attachment was lost or duplicated")
				}
			}
			mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", revision)
		})
	}
}

func TestMediaAttachHTTPAdmissionRejectsMissingOrAmbiguousProofsAndTransport(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":1}`, f.empty.ID)
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
		{"encoded path", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.RawPath = "/api/v1/media/%65mpty-media/links" }},
		{"encoded body", 415, "UNSUPPORTED_MEDIA_TYPE", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", body, f.cookie, f.csrf, "media-admission-key", `"1"`)
			tc.edit(r)
			response := respond(f.handler, r)
			expectError(t, response, tc.status, tc.code)
			if response.Header().Get("ETag") != "" {
				t.Fatal("rejected attachment invented a link ETag")
			}
		})
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/empty-media"), "empty-media", 1)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/empty-media/links"))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("rejected boundary input changed actual membership")
	}
}

func TestMediaAttachHTTPRequiresCompletedSession(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	request := domainRequest(http.MethodPost, "/api/v1/media/photo/links", `{"item_id":"owner","owner_revision":1}`, cookie, csrf, "media-incomplete-session", `"1"`)
	expectError(t, respond(authHandler(t, auth), request), 403, "PASSWORD_CHANGE_REQUIRED")
}

func TestMediaAttachHTTPOriginalMetadataRemainsExactAcrossFrozenReplay(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":1,"is_cover":false,"sort_order":0,"description":""}`, f.empty.ID)
	request := func() *http.Request {
		return domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", body, f.cookie, f.csrf, "media-explicit-fields-replay", `"1"`)
	}
	first, second := respond(f.handler, request()), respond(f.handler, request())
	var a, b store.MediaLink
	if first.Code != 200 || second.Code != 200 || json.Unmarshal(first.Body.Bytes(), &a) != nil || json.Unmarshal(second.Body.Bytes(), &b) != nil ||
		!reflect.DeepEqual(a, b) || a.IsCover || a.SortOrder != 0 || a.Description != "" || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("explicit empty values or original frozen response were lost")
	}
}
