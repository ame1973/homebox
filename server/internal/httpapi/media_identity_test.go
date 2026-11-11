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

func TestMediaLinkIdentityHTTPPreservesExactRetainedIDsAndIndependentProofs(t *testing.T) {
	identities := []string{"a/b", "a\\b", "\x00", ".", "..", "line\r\n😀", "percent%2F", "plus+space ", strings.Repeat("界", 128)}
	f := newMediaHTTPFixture(t, identities...)
	for index, id := range identities {
		t.Run(fmt.Sprintf("identity-%d", index), func(t *testing.T) {
			path := "/api/v1/media/links?id=" + url.QueryEscape(id)
			body := `{"description":"Exact reviewed caption"}`
			edit := respond(f.handler, domainRequest(http.MethodPatch, path, body, f.cookie, f.csrf, fmt.Sprintf("media-exact-edit-%d", index), `"1"`))
			var link store.MediaLink
			if edit.Code != 200 || json.Unmarshal(edit.Body.Bytes(), &link) != nil || link.ID != id || link.OwnerID != f.empty.ID ||
				link.MediaID != "photo" || link.Revision != 2 || link.Description != "Exact reviewed caption" || link.SortOrder != -3 ||
				edit.Header().Get("ETag") != `"2"` || edit.Header().Get("Cache-Control") != "no-store" || edit.Header().Get("Set-Cookie") != "" {
				t.Fatalf("exact identity edit changed the source or proof: status %d body %s", edit.Code, edit.Body.String())
			}
			expectError(t, respond(f.handler, domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, fmt.Sprintf("media-exact-stale-%d", index), `"1"`)), 409, "REVISION_CONFLICT")
			removed := respond(f.handler, domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, fmt.Sprintf("media-exact-unlink-%d", index), `"2"`))
			var ack store.ActionAck
			if removed.Code != 200 || json.Unmarshal(removed.Body.Bytes(), &ack) != nil || !ack.OK || ack.OperationID == "" || ack.Revision != 3 ||
				removed.Header().Get("ETag") != "" || removed.Header().Get("Cache-Control") != "no-store" || removed.Header().Get("Set-Cookie") != "" {
				t.Fatalf("exact identity unlink changed the independent acknowledgement: status %d body %s", removed.Code, removed.Body.String())
			}
		})
	}
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.empty.ID)))
	if len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("exact unlink left or invented an owner link")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7+int64(2*len(identities)))
}

func TestMediaLinkIdentityHTTPReplaysAcrossURLFormsAfterSourceRemoval(t *testing.T) {
	f := newMediaHTTPFixture(t, "simple", "other", "third", "fourth")
	body := `{"description":"Original caption"}`
	type command struct {
		method, id, key, body string
		queryFirst            bool
		original              []byte
		etag                  string
	}
	commands := []command{
		{method: http.MethodPatch, id: "simple", key: "media-cross-path-patch", body: body},
		{method: http.MethodPatch, id: "third", key: "media-cross-query-patch", body: body, queryFirst: true},
		{method: http.MethodDelete, id: "other", key: "media-cross-query-unlink", queryFirst: true},
		{method: http.MethodDelete, id: "fourth", key: "media-cross-path-unlink"},
	}
	request := func(command command, query bool) *http.Request {
		path := "/api/v1/media/links/" + command.id
		if query {
			path = "/api/v1/media/links?id=" + url.QueryEscape(command.id)
		}
		return domainRequest(command.method, path, command.body, f.cookie, f.csrf, command.key, `"1"`)
	}
	for i := range commands {
		command := &commands[i]
		first := respond(f.handler, request(*command, command.queryFirst))
		if first.Code != 200 {
			t.Fatalf("original %s failed: %d %s", command.method, first.Code, first.Body.String())
		}
		command.original, command.etag = bytes.Clone(first.Body.Bytes()), first.Header().Get("ETag")
		if command.method == http.MethodPatch && command.etag != `"2"` || command.method == http.MethodDelete && command.etag != "" {
			t.Fatal("wrong independent reply ETag", command.method, command.etag)
		}
		replay := respond(f.handler, request(*command, !command.queryFirst))
		if replay.Code != 200 || replay.Header().Get("ETag") != command.etag || !bytes.Equal(replay.Body.Bytes(), command.original) {
			t.Fatal("equivalent URL changed the frozen receipt", command.method, replay.Code, replay.Body.String())
		}
	}
	changedID := commands[0]
	changedID.id = "third"
	expectError(t, respond(f.handler, request(changedID, true)), 409, "IDEMPOTENCY_CONFLICT")
	changedBody := commands[0]
	changedBody.body = `{"description":"Different"}`
	expectError(t, respond(f.handler, request(changedBody, true)), 409, "IDEMPOTENCY_CONFLICT")
	changedProof := request(commands[1], false)
	changedProof.Header.Set("If-Match", `"2"`)
	expectError(t, respond(f.handler, changedProof), 409, "IDEMPOTENCY_CONFLICT")
	changedMethod := commands[0]
	changedMethod.method, changedMethod.body = http.MethodDelete, ""
	expectError(t, respond(f.handler, request(changedMethod, true)), 409, "IDEMPOTENCY_CONFLICT")

	deleted := respond(f.handler, domainRequest(http.MethodDelete, "/api/v1/items/"+f.empty.ID, "", f.cookie, f.csrf, "media-cross-delete-owner", `"5"`))
	if deleted.Code != 200 {
		t.Fatalf("deleting the reviewed owner failed: %d %s", deleted.Code, deleted.Body.String())
	}
	f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	for _, command := range commands {
		for _, query := range []bool{false, true} {
			replay := respond(f.handler, request(command, query))
			if replay.Code != 200 || replay.Header().Get("ETag") != command.etag || !bytes.Equal(replay.Body.Bytes(), command.original) {
				t.Fatal("fresh-session replay required deleted source or changed the frozen receipt", command.method, replay.Code)
			}
		}
	}
	expectError(t, respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links?id=simple", body, f.cookie, f.csrf, "media-cross-fresh-key", `"1"`)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 12)
}

func TestMediaLinkIdentityHTTPRejectsAmbiguousQueriesAndAdmissionFailures(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		f := newMediaHTTPFixture(t)
		body := ""
		if method == http.MethodPatch {
			body = `{"description":"Not committed"}`
		}
		for _, raw := range []string{
			"", "id=", "other=item-000", "id=item-000&id=item-001", "id=item-000&%69d=item-000",
			"id=item-000&unknown=x", "id=%FF", "id=%ED%A0%80", "id=%", "id=item-000;other=x",
			"id=" + url.QueryEscape(strings.Repeat("界", 129)), "id=" + url.QueryEscape(strings.Repeat("\x00", 129)),
			"id=item-000&" + strings.Repeat("&", 16<<10),
		} {
			request := domainRequest(method, "/api/v1/media/links", body, f.cookie, f.csrf, "media-exact-invalid-query", `"1"`)
			request.URL.RawQuery = raw
			response := respond(f.handler, request)
			expectError(t, response, 400, "INVALID_QUERY")
			if response.Header().Get("ETag") != "" {
				t.Fatal("invalid query invented a link proof")
			}
		}
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
			{"duplicate version", 426, "CLIENT_UPGRADE_REQUIRED", func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }},
			{"missing key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
			{"duplicate key", 400, "INVALID_IDEMPOTENCY_KEY", func(r *http.Request) { r.Header.Add("Idempotency-Key", "another-media-key") }},
			{"missing proof", 409, "PRECONDITION_REQUIRED", func(r *http.Request) { r.Header.Del("If-Match") }},
			{"weak proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Set("If-Match", `W/"1"`) }},
			{"duplicate proof", 400, "INVALID_REVISION", func(r *http.Request) { r.Header.Add("If-Match", `"1"`) }},
			{"encoded route", 400, "INVALID_MEDIA_URL", func(r *http.Request) { r.URL.RawPath = "/api/v1/media/%6cinks" }},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				request := domainRequest(method, "/api/v1/media/links?id=item-000", body, f.cookie, f.csrf, "media-exact-admission", `"1"`)
				tc.edit(request)
				expectError(t, respond(f.handler, request), tc.status, tc.code)
			})
		}
		page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)))
		if len(page.Data) != 50 || page.Data[0].ID != "item-000" || page.Data[0].Revision != 1 || page.Data[0].Description != "\x00Exact é\n" {
			t.Fatal("a rejected query or admission changed the actual owner membership")
		}
		mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
	}
	f := newMediaHTTPFixture(t)
	for _, tc := range []struct {
		body, code string
		status     int
	}{
		{`{}`, "VALIDATION_ERROR", 422},
		{`{"description":null}`, "VALIDATION_ERROR", 422},
		{`{"description":"x","description":"y"}`, "INVALID_JSON", 400},
		{`{"owner_id":"other"}`, "VALIDATION_ERROR", 422},
		{`{"description":"\ud800"}`, "INVALID_JSON", 400},
	} {
		response := respond(f.handler, domainRequest(http.MethodPatch, "/api/v1/media/links?id=item-000", tc.body, f.cookie, f.csrf, "media-exact-invalid-body", `"1"`))
		expectError(t, response, tc.status, tc.code)
	}
	patch := domainRequest(http.MethodPatch, "/api/v1/media/links?id=item-000", `{"description":"x"}`+strings.Repeat(" ", mediaLinkBodyLimit), f.cookie, f.csrf, "media-exact-oversized", `"1"`)
	expectError(t, respond(f.handler, patch), 413, "BODY_TOO_LARGE")
	patch = domainRequest(http.MethodPatch, "/api/v1/media/links?id=item-000", `{"description":"x"}`, f.cookie, f.csrf, "media-exact-encoded", `"1"`)
	patch.Header.Set("Content-Encoding", "gzip")
	expectError(t, respond(f.handler, patch), 415, "UNSUPPORTED_MEDIA_TYPE")
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.ContentLength = 1 },
		func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		request := domainRequest(http.MethodDelete, "/api/v1/media/links?id=item-000", "", f.cookie, f.csrf, "media-exact-body", `"1"`)
		mutate(request)
		expectError(t, respond(f.handler, request), 400, "INVALID_BODY")
	}
	for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost} {
		response := respond(f.handler, domainRequest(method, "/api/v1/media/links?id=item-000", "", f.cookie, f.csrf, "media-exact-method", `"1"`))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != "GET, PATCH, DELETE" {
			t.Fatal("incorrect exact identity methods", response.Header().Get("Allow"))
		}
	}
	expectError(t, mediaHTTPGet(f, "/api/v1/media/links?id=item-000"), 400, "INVALID_QUERY")
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)))
	if len(page.Data) != 50 || page.Data[0].ID != "item-000" || page.Data[0].Revision != 1 || page.Data[0].Description != "\x00Exact é\n" {
		t.Fatal("a rejected query mutation changed the actual owner membership")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)

	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		request := domainRequest(method, "/api/v1/media/links?id=item-000", `{"description":"x"}`, cookie, csrf, "media-exact-incomplete", `"1"`)
		expectError(t, respond(authHandler(t, auth), request), 403, "PASSWORD_CHANGE_REQUIRED")
	}
}

func TestMediaLinkIdentityHTTPRevalidatesAfterReadingBody(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newMediaHTTPFixture(t)
			body := ""
			if method == http.MethodPatch {
				body = `{"description":"Reviewed"}`
			}
			request := domainRequest(method, "/api/v1/media/links?id=item-000", body, f.cookie, f.csrf, "media-exact-stale-body", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				library, err := store.NewMediaLibrary(f.auth)
				if err != nil {
					t.Fatal(err)
				}
				description := "Concurrent edit"
				if _, err = library.PatchLink(t.Context(), f.cookie.Value, "media-exact-concurrent", "item-000", 1, store.MediaLinkPatch{Description: &description}); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, request), 409, "REVISION_CONFLICT")
			request = domainRequest(method, "/api/v1/media/links?id=item-001", body, f.cookie, f.csrf, "media-exact-revoked-body", `"1"`)
			request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
				if _, err := f.auth.Logout(t.Context(), f.cookie.Value, "media-exact-revoke"); err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(f.handler, request), 401, "AUTHENTICATION_REQUIRED")
			f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
			page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)))
			if len(page.Data) != 50 || page.Data[0].ID != "item-000" || page.Data[0].Revision != 2 || page.Data[0].Description != "Concurrent edit" ||
				page.Data[1].ID != "item-001" || page.Data[1].Revision != 1 {
				t.Fatal("body-time authorization or proof checks changed the wrong link")
			}
		})
	}
}
