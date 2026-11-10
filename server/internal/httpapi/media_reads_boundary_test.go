package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

func TestMediaHTTPReadAdmissionMethodsPathsAndPrivacy(t *testing.T) {
	f := newMediaHTTPFixture(t)
	paths := []string{"/api/v1/media/photo", "/api/v1/media/photo/view", "/api/v1/media/photo/links", "/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.owner.ID)}
	for _, path := range paths {
		expectError(t, respond(f.handler, domainRequest(http.MethodGet, path, "", nil, "", "", "")), 401, "AUTHENTICATION_REQUIRED")
		r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		r.Header.Set("Origin", "https://foreign.test")
		expectError(t, respond(f.handler, r), 403, "ORIGIN_REJECTED")
		for _, method := range []string{http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			response := respond(f.handler, domainRequest(method, path, "", f.cookie, f.csrf, "media-unsupported-method", `"1"`))
			expectError(t, response, 405, "METHOD_NOT_ALLOWED")
			if response.Header().Get("Allow") != "GET" {
				t.Fatal("read-only Media route advertised an unimplemented method")
			}
		}
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = -1 },
			func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		} {
			r := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
			mutate(r)
			expectError(t, respond(f.handler, r), 400, "INVALID_BODY")
		}
	}
	for _, path := range []string{
		"/api/v1/media/.", "/api/v1/media/../view", "/api/v1/media/%70hoto", "/api/v1/media/" + strings.Repeat("界", 129),
		"/api/v1/media/photo?unexpected=1", "/api/v1/media/photo/view?", "/api/v1/media/photo\\invalid",
	} {
		expectError(t, mediaHTTPGet(f, path), 400, "INVALID_MEDIA_URL")
	}
	for _, path := range []string{"/api/v1/media/photo/content", "/api/v1/media/photo/thumbnail", "/api/v1/media/links/unimplemented-link"} {
		expectError(t, mediaHTTPGet(f, path), 404, "NOT_FOUND")
	}
	for _, path := range []string{"/api/v1/media/absent", "/api/v1/media/absent/view", "/api/v1/media/absent/links", "/api/v1/media/links?owner_type=item&owner_id=absent"} {
		expectError(t, mediaHTTPGet(f, path), 404, "NOT_FOUND")
	}
}

func TestMediaHTTPReadRequiresCompletedSession(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	handler := authHandler(t, auth)
	for _, path := range []string{"/api/v1/media/photo", "/api/v1/media/photo/view", "/api/v1/media/photo/links", "/api/v1/media/links?owner_type=item&owner_id=owner"} {
		expectError(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
	}
}

func TestMediaHTTPOwnerQueryRejectsInvalidScopesAndBudgets(t *testing.T) {
	f := newMediaHTTPFixture(t)
	owner := url.QueryEscape(f.owner.ID)
	for _, query := range []string{
		"", "owner_type=item", "owner_id=" + owner, "owner_type=&owner_id=" + owner,
		"owner_type=other&owner_id=" + owner, "owner_type=item&owner_id=", "owner_type=item&owner_id=%FF",
		"owner_type=item&owner_id=" + url.QueryEscape(strings.Repeat("界", 129)),
		"owner_type=item&owner_id=" + url.QueryEscape(strings.Repeat("\x00", 129)),
		"owner_type=item&owner_id=" + owner + "&owner_id=other", "owner_type=item&owner_type=unit&owner_id=" + owner,
		"owner_type=item&owner_id=" + owner + "&unknown=x", "owner_type=item&owner_id=" + owner + "&limit=0",
		"owner_type=item&owner_id=" + owner + "&limit=101", "owner_type=item&owner_id=" + owner + "&limit=1&limit=2",
		"owner_type=item&owner_id=" + owner + "&cursor=", "owner_type=item&owner_id=" + owner + "&cursor=" + strings.Repeat("x", 2049),
		"owner_type=item&owner_id=" + owner + "&cursor=" + url.QueryEscape(strings.Repeat("😀", 513)),
		"owner_type=item&owner_id=" + owner + "&limit=%", "owner_type=item&owner_id=" + owner + ";limit=1",
	} {
		expectError(t, mediaHTTPGet(f, "/api/v1/media/links?"+query), 400, "INVALID_QUERY")
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=2", "cursor=", "cursor=" + strings.Repeat("x", 2049), "owner_id=" + owner} {
		expectError(t, mediaHTTPGet(f, "/api/v1/media/photo/links?"+query), 400, "INVALID_QUERY")
	}
	query := "owner_type=item&owner_id=" + url.QueryEscape(strings.Repeat("😀", 128)) + "&cursor=" + strings.Repeat("%41", 2048)
	query += strings.Repeat("&", (16<<10)-len(query))
	request := apiRequest(http.MethodGet, "/api/v1/media/links?"+query, "")
	w := httptest.NewRecorder()
	_, id, cursor, limit, ok := mediaOwnerQuery(w, request, "request")
	if !ok || id != strings.Repeat("😀", 128) || len(cursor) != 2048 || limit != 50 {
		t.Fatal("exact query or cursor byte boundary was rejected")
	}
	request.URL.RawQuery += "&"
	if _, _, _, _, ok := mediaOwnerQuery(httptest.NewRecorder(), request, "request"); ok {
		t.Fatal("oversized raw query was accepted")
	}
}

func TestMediaHTTPOwnerDeletionChangesOnlyActualMembership(t *testing.T) {
	f := newMediaHTTPFixture(t)
	path := "/api/v1/items/" + f.owner.ID
	request := func() *http.Request {
		return domainRequest(http.MethodDelete, path, "", f.cookie, f.csrf, "media-http-owner-deletion", `"1"`)
	}
	first := respond(f.handler, request())
	if first.Code != 200 || first.Header().Get("ETag") != "" {
		t.Fatalf("real owner deletion failed: %d", first.Code)
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/photo/links"))
	if len(page.Data) != 3 || page.NextCursor != nil || page.Data[0].ID != "location" || page.Data[1].ID != "sibling" || page.Data[2].ID != "unit" {
		t.Fatal("owner deletion removed another owner's real link")
	}
	expectError(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.owner.ID)), 404, "NOT_FOUND")
	replay := respond(f.handler, request())
	if replay.Code != 200 || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("explicit original-key deletion retry changed the frozen reply")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
	reserved := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(reserved.Data) != 1 || reserved.Data[0].OwnerID != f.sibling.ID {
		t.Fatal("owner deletion damaged an unrelated Media membership")
	}
}

func TestMediaHTTPThroughVerifiedTLSAndCookieJar(t *testing.T) {
	f := newMediaHTTPFixture(t)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: f.auth, PublicOrigin: origin, Assets: fstest.MapFS{"index.html": {Data: []byte("<main>App</main>")}}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	login, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"管理員","password":"`+changedPassword+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Origin", origin)
	login.Header.Set("Content-Type", "application/json")
	response, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != 200 || readErr != nil || closeErr != nil || len(client.Jar.Cookies(login.URL)) == 0 {
		t.Fatal("verified-TLS login failed", response.StatusCode, readErr, closeErr)
	}
	for _, check := range []struct {
		path, etag string
		count      int
	}{
		{"/api/v1/media/photo", `"7"`, -1},
		{"/api/v1/media/links/view", `"3"`, -1},
		{"/api/v1/media/photo/links?limit=1", "", 1},
		{"/api/v1/media/links?owner_type=unit&owner_id=" + url.QueryEscape(f.unit.ID), "", 1},
	} {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+check.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if response.StatusCode != 200 || readErr != nil || closeErr != nil || response.Header.Get("ETag") != check.etag ||
			response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Strict-Transport-Security") == "" || response.Header.Get("Set-Cookie") != "" {
			t.Fatal("verified-TLS Media read failed", response.StatusCode, readErr, closeErr)
		}
		if check.count < 0 {
			var image store.Media
			if json.Unmarshal(body, &image) != nil || image.Verification != "unverified" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
				t.Fatal("TLS Media read invented a verified thumbnail")
			}
		} else {
			var page store.MediaLinkList
			if json.Unmarshal(body, &page) != nil || len(page.Data) != check.count || page.Data[0].Media.ID != page.Data[0].MediaID {
				t.Fatal("TLS Media link read did not join real source")
			}
		}
	}
}

func TestMediaHTTPErrorMappingDoesNotExposeSource(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"}, {store.ErrInvalidCursor, 400, "INVALID_CURSOR"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {errors.New("private /data/homebox.sqlite secret-credential"), 503, "SERVICE_UNAVAILABLE"},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		writeMediaError(w, fmt.Errorf("private wrapper: %w", test.err), "request")
		expectError(t, w, test.status, test.code)
		if strings.Contains(w.Body.String(), "private wrapper") || strings.Contains(w.Body.String(), "/data/homebox.sqlite") || strings.Contains(w.Body.String(), "secret-credential") || w.Header().Get("ETag") != "" {
			t.Fatal("Media error exposed private source or invented an ETag")
		}
	}
}

func FuzzMediaOwnerQuery(f *testing.F) {
	for _, query := range []string{"owner_type=item&owner_id=owner", "owner_type=unit&owner_id=%00&limit=1", "owner_type=item&owner_id=%FF", "cursor=", "owner_type=item&owner_id=x&limit=1&limit=2"} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		r := apiRequest(http.MethodGet, "/api/v1/media/links", "")
		r.URL.RawQuery = raw
		w := httptest.NewRecorder()
		ownerType, ownerID, cursor, limit, ok := mediaOwnerQuery(w, r, "request")
		if ok && (len(raw) > 16<<10 || ownerType != "item" && ownerType != "unit" && ownerType != "location" || !boundedString(ownerID, 1, 128) || len(cursor) > 2048 || limit < 1 || limit > 100) {
			t.Fatal("accepted invalid Media query")
		}
	})
}
