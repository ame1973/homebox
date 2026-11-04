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
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

func locationHTTPFixture(t *testing.T) (*store.Auth, http.Handler, *http.Cookie, string) {
	t.Helper()
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	cookie, csrf := testCredentials(t, auth, changedPassword)
	return auth, authHandler(t, auth), cookie, csrf
}

func locationRequest(method, path, body string, cookie *http.Cookie, csrf, key, revision string) *http.Request {
	r := apiRequest(method, path, body)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion+"-dev")
	}
	if revision != "" {
		r.Header.Set("If-Match", revision)
	}
	return r
}

func responseLocation(t *testing.T, response *httptest.ResponseRecorder) store.Location {
	t.Helper()
	var location store.Location
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &location) != nil || location.ID == "" || location.Revision < 1 || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(location.Revision, 10)) || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid real location response: %d %s", response.Code, response.Body.String())
	}
	return location
}

func TestLocationHTTPThroughVerifiedTLSWithRealSessionAndFrozenReplies(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: fstest.MapFS{"index.html": {Data: []byte("<main>App</main>")}}})
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
	csrf := ""
	call := func(method, path string, value any, revision, key string) (*http.Response, []byte) {
		t.Helper()
		var body io.Reader
		if value != nil {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		if value != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if strings.HasPrefix(path, "/api/v1/locations") && method != http.MethodGet {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Idempotency-Key", key)
			r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion+"-dev")
		}
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Strict-Transport-Security") == "" || response.Header.Get("X-Request-ID") == "" {
			t.Fatal("protected verified-TLS response boundary failed", err)
		}
		if strings.HasPrefix(path, "/api/v1/locations") && response.Header.Get("Set-Cookie") != "" {
			t.Fatal("domain response changed its session cookie")
		}
		return response, data
	}
	read := func(response *http.Response, body []byte) store.Location {
		t.Helper()
		var location store.Location
		if response.StatusCode != 200 || json.Unmarshal(body, &location) != nil || location.ID == "" || response.Header.Get("ETag") != strconv.Quote(strconv.FormatInt(location.Revision, 10)) {
			t.Fatalf("location request failed: %d %s", response.StatusCode, body)
		}
		return location
	}
	response, body := call(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "管理員", "password": changedPassword}, "", "")
	if response.StatusCode != 200 || len(response.Cookies()) != 1 || !response.Cookies()[0].Secure || !response.Cookies()[0].HttpOnly {
		t.Fatal("real secure-session login failed")
	}
	_, body = call(http.MethodGet, "/api/v1/auth/csrf", nil, "", "")
	var proof struct{ Token string }
	if json.Unmarshal(body, &proof) != nil || proof.Token == "" {
		t.Fatal("real session proof was not issued")
	}
	csrf = proof.Token
	response, body = call(http.MethodGet, "/api/v1/locations/tree", nil, "", "")
	var list store.LocationList
	if response.StatusCode != 200 || json.Unmarshal(body, &list) != nil || list.Data == nil || len(list.Data) != 0 || list.NextCursor != nil {
		t.Fatal("empty authoritative list was not an empty array")
	}
	createBody := json.RawMessage(`{"name":"  書房 📦  ","code":"ROOT","description":"<notes>","sort_order":1e3}`)
	response, originalBody := call(http.MethodPost, "/api/v1/locations", createBody, "", "tls-create-root")
	root := read(response, originalBody)
	if root.Name != "  書房 📦  " || root.Kind != "other" || root.Status != "active" || root.SortOrder != 1000 {
		t.Fatal("HTTP defaults or exact typed metadata were wrong")
	}
	response, body = call(http.MethodPost, "/api/v1/locations", map[string]any{"name": "Child", "code": "CHILD", "parent_id": root.ID}, `"1"`, "tls-create-child")
	child := read(response, body)
	response, body = call(http.MethodGet, "/api/v1/locations/"+root.ID, nil, "", "")
	root = read(response, body)
	if root.Revision != 2 {
		t.Fatal("child creation did not advance the parent aggregate")
	}
	response, body = call(http.MethodPost, "/api/v1/locations", createBody, "", "tls-create-root")
	if response.StatusCode != 200 || !bytes.Equal(body, originalBody) || response.Header.Get("ETag") != `"1"` {
		t.Fatal("HTTP retry did not return its exact original resource/revision")
	}
	response, body = call(http.MethodPost, "/api/v1/locations/"+root.ID+"/move", map[string]any{"target_parent_id": child.ID}, `"2"`, "tls-cycle-guard")
	if response.StatusCode != 409 || !bytes.Contains(body, []byte("LOCATION_CYCLE")) {
		t.Fatal("HTTP cycle guard failed", string(body))
	}
	response, body = call(http.MethodPatch, "/api/v1/locations/"+child.ID, map[string]any{"name": "Updated child"}, `"1"`, "tls-patch-child")
	child = read(response, body)
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatal("PATCH omitted parent was treated as a move")
	}
	response, body = call(http.MethodPost, "/api/v1/locations/"+child.ID+"/move", map[string]any{"target_parent_id": nil}, `"2"`, "tls-move-to-root")
	child = read(response, body)
	if child.ParentID != nil || child.Revision != 3 {
		t.Fatal("nullable move-to-root failed")
	}
	response, body = call(http.MethodGet, "/api/v1/locations/"+root.ID, nil, "", "")
	root = read(response, body)
	response, body = call(http.MethodPost, "/api/v1/locations/"+root.ID+"/archive", map[string]any{}, strconv.Quote(strconv.FormatInt(root.Revision, 10)), "tls-archive-root")
	root = read(response, body)
	if root.Status != "archived" {
		t.Fatal("empty-location archive did not commit")
	}
	response, body = call(http.MethodPost, "/api/v1/locations", map[string]any{"name": "Denied", "code": "DENIED", "parent_id": root.ID}, strconv.Quote(strconv.FormatInt(root.Revision, 10)), "tls-archived-parent")
	if response.StatusCode != 409 || !bytes.Contains(body, []byte("LOCATION_ARCHIVED")) {
		t.Fatal("archived destination guard was not exposed safely")
	}
	response, body = call(http.MethodPatch, "/api/v1/locations/"+root.ID, map[string]any{"status": "active"}, strconv.Quote(strconv.FormatInt(root.Revision, 10)), "tls-restore-root")
	root = read(response, body)
	deleteRevision := strconv.Quote(strconv.FormatInt(root.Revision, 10))
	response, deletedBody := call(http.MethodDelete, "/api/v1/locations/"+root.ID, nil, deleteRevision, "tls-delete-root")
	var ack store.ActionAck
	if response.StatusCode != 200 || json.Unmarshal(deletedBody, &ack) != nil || !ack.OK || ack.OperationID == "" || ack.Revision != root.Revision+1 {
		t.Fatal("HTTP delete did not acknowledge the committed tombstone")
	}
	response, body = call(http.MethodGet, "/api/v1/locations/"+root.ID, nil, "", "")
	if response.StatusCode != 404 || !bytes.Contains(body, []byte("NOT_FOUND")) {
		t.Fatal("deleted location remained public")
	}
	response, body = call(http.MethodDelete, "/api/v1/locations/"+root.ID, nil, deleteRevision, "tls-delete-root")
	if response.StatusCode != 200 || !bytes.Equal(body, deletedBody) {
		t.Fatal("deleted resource acknowledgement was not replayable")
	}
	response, body = call(http.MethodGet, "/api/v1/locations/tree", nil, "", "")
	if response.StatusCode != 200 || json.Unmarshal(body, &list) != nil || len(list.Data) != 1 || list.Data[0].ID != child.ID {
		t.Fatal("delete did not remove only the intended current location")
	}
}

func TestLocationHTTPParentProofStaleInputAndSubtreeConfirmation(t *testing.T) {
	_, handler, cookie, csrf := locationHTTPFixture(t)
	root := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"Root","code":"ROOT"}`, cookie, csrf, "create-root-key", "")))
	childBody := fmt.Sprintf(`{"name":"Child","code":"CHILD","parent_id":%q}`, root.ID)
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", childBody, cookie, csrf, "missing-parent-proof", "")), 409, "PRECONDITION_REQUIRED")
	child := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", childBody, cookie, csrf, "create-child-key", `"1"`)))
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", strings.ReplaceAll(childBody, "CHILD", "NEXT"), cookie, csrf, "stale-parent-key", `"1"`)), 409, "REVISION_CONFLICT")
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations/"+root.ID+"/archive", `{}`, cookie, csrf, "unreviewed-subtree", `"2"`)), 409, "LOCATION_HAS_CHILDREN")
	archived := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations/"+root.ID+"/archive", `{"archive_children":true,"reason":"Reviewed empty subtree"}`, cookie, csrf, "archive-subtree-key", `"2"`)))
	if archived.Status != "archived" || archived.Revision != 3 {
		t.Fatal("subtree archive did not advance the aggregate once")
	}
	child = responseLocation(t, respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/"+child.ID, "", cookie, csrf, "", "")))
	if child.Status != "archived" || child.Revision != 2 {
		t.Fatal("descendant was not atomically archived")
	}
	expectError(t, respond(handler, locationRequest(http.MethodPatch, "/api/v1/locations/"+child.ID, `{"status":"active"}`, cookie, csrf, "restore-child-first", `"2"`)), 409, "LOCATION_ARCHIVED")
	expectError(t, respond(handler, locationRequest(http.MethodDelete, "/api/v1/locations/"+root.ID, "", cookie, csrf, "delete-with-child", `"3"`)), 409, "LOCATION_HAS_CHILDREN")
	expectError(t, respond(handler, locationRequest(http.MethodPatch, "/api/v1/locations/"+child.ID, `{"name":"Changed"}`, cookie, csrf, "archive-subtree-key", `"2"`)), 409, "IDEMPOTENCY_CONFLICT")
}

func TestLocationHTTPListUsesSignedBoundedCursors(t *testing.T) {
	_, handler, cookie, csrf := locationHTTPFixture(t)
	for _, code := range []string{"FIRST", "SECOND", "THIRD"} {
		responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"Box","code":%q}`, code), cookie, csrf, "create-"+code, "")))
	}
	response := respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree?limit=2", "", cookie, csrf, "", ""))
	var first store.LocationList
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &first) != nil || len(first.Data) != 2 || first.NextCursor == nil {
		t.Fatal("first real HTTP page failed", response.Body.String())
	}
	nextURL := "/api/v1/locations/tree?limit=2&cursor=" + url.QueryEscape(*first.NextCursor)
	response = respond(handler, locationRequest(http.MethodGet, nextURL, "", cookie, csrf, "", ""))
	var second store.LocationList
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &second) != nil || len(second.Data) != 1 || second.NextCursor != nil || second.Data[0].ID <= first.Data[1].ID {
		t.Fatal("signed HTTP continuation did not produce the final stable page")
	}
	expectError(t, respond(handler, locationRequest(http.MethodGet, strings.Replace(nextURL, "limit=2", "limit=1", 1), "", cookie, csrf, "", "")), 400, "INVALID_CURSOR")
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=x", "?limit=1&limit=2", "?cursor=", "?cursor=x&cursor=y", "?unexpected=true", "?limit=1;cursor=x", "?cursor=%zz", "?cursor=" + strings.Repeat("x", 2049), "?" + strings.Repeat("x", 2301)} {
		r := locationRequest(http.MethodGet, "/api/v1/locations/tree", "", cookie, csrf, "", "")
		r.URL.RawQuery = strings.TrimPrefix(query, "?")
		expectError(t, respond(handler, r), 400, "INVALID_QUERY")
	}
	expectError(t, respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree?cursor=unsigned", "", cookie, csrf, "", "")), 400, "INVALID_CURSOR")
}

func TestLocationHTTPRequiresActiveSessionCSRFVersionKeyAndExactOrigin(t *testing.T) {
	auth, handler, cookie, csrf := locationHTTPFixture(t)
	body := `{"name":"Box","code":"BOX"}`
	for _, input := range []struct {
		name   string
		change func(*http.Request)
		status int
		code   string
	}{
		{"cookie", func(r *http.Request) { r.Header.Del("Cookie") }, 401, "AUTHENTICATION_REQUIRED"},
		{"csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
		{"wrong csrf", func(r *http.Request) { r.Header.Set("X-CSRF-Token", strings.Repeat("A", 43)) }, 403, "CSRF_REJECTED"},
		{"version", func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"future version", func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "9.0.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
		{"key", func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"origin", func(r *http.Request) { r.Header.Del("Origin") }, 403, "ORIGIN_REJECTED"},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://untrusted.test") }, 403, "ORIGIN_REJECTED"},
		{"host", func(r *http.Request) { r.Host = "untrusted.test" }, 403, "ORIGIN_REJECTED"},
	} {
		r := locationRequest(http.MethodPost, "/api/v1/locations", body, cookie, csrf, "protected-create-key", "")
		input.change(r)
		expectError(t, respond(handler, r), input.status, input.code)
	}
	root := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", body, cookie, csrf, "protected-create-key", "")))
	if _, err := auth.Logout(t.Context(), cookie.Value, "logout-domain-http"); err != nil {
		t.Fatal(err)
	}
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", body, cookie, csrf, "protected-create-key", "")), 401, "AUTHENTICATION_REQUIRED")
	expectError(t, respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/"+root.ID, "", cookie, csrf, "", "")), 401, "AUTHENTICATION_REQUIRED")
}

func TestLocationHTTPMandatoryChangeGatePrecedesBodyParsing(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	handler := authHandler(t, auth)
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{invalid}`, cookie, csrf, "mandatory-domain-key", "")), 403, "PASSWORD_CHANGE_REQUIRED")
	expectError(t, respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree", "", cookie, csrf, "", "")), 403, "PASSWORD_CHANGE_REQUIRED")
}

func TestLocationHTTPRevisionSyntaxMethodsAndURLBoundaries(t *testing.T) {
	_, handler, cookie, csrf := locationHTTPFixture(t)
	root := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"Box","code":"BOX"}`, cookie, csrf, "syntax-create-key", "")))
	path := "/api/v1/locations/" + root.ID
	for _, revision := range []string{`1`, `"0"`, `"01"`, `"-1"`, `W/"1"`, `*`, `"1", "2"`, `"1.0"`, `"9007199254740992"`, `"99999999999999999"`, `"x"`} {
		expectError(t, respond(handler, locationRequest(http.MethodPatch, path, `{"name":"No change"}`, cookie, csrf, "syntax-invalid-key", revision)), 400, "INVALID_REVISION")
	}
	r := locationRequest(http.MethodPatch, path, `{"name":"No change"}`, cookie, csrf, "duplicate-revision-key", `"1"`)
	r.Header.Add("If-Match", `"1"`)
	expectError(t, respond(handler, r), 400, "INVALID_REVISION")
	expectError(t, respond(handler, locationRequest(http.MethodPatch, path, `{"name":"No change"}`, cookie, csrf, "missing-revision-key", "")), 409, "PRECONDITION_REQUIRED")
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"No change","code":"OTHER"}`, cookie, csrf, "unrelated-parent-key", `"1"`)), 400, "INVALID_REVISION")
	for _, target := range []string{path + "?unexpected=1", path + "?", "/api/v1/locations/..", "/api/v1/locations/%5ctarget", "/api/v1/locations/%00target", "/api/v1/locations/%61lias", "/api/v1/locations/" + strings.Repeat("x", 129)} {
		expectError(t, respond(handler, locationRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 400, "INVALID_LOCATION_URL")
	}
	for _, input := range []struct{ method, path, allow string }{
		{http.MethodPut, path, "GET, PATCH, DELETE"}, {http.MethodHead, path, "GET, PATCH, DELETE"},
		{http.MethodGet, "/api/v1/locations", "POST"}, {http.MethodPost, "/api/v1/locations/tree", "GET"},
		{http.MethodGet, path + "/move", "POST"}, {http.MethodGet, path + "/archive", "POST"},
	} {
		response := respond(handler, locationRequest(input.method, input.path, "", cookie, csrf, "wrong-method-key", ""))
		expectError(t, response, 405, "METHOD_NOT_ALLOWED")
		if response.Header().Get("Allow") != input.allow {
			t.Fatal("method denial omitted its exact Allow header")
		}
	}
	for _, target := range []string{"/api/v1/locations/", path + "/unknown", path + "/move/", "/api/v1/locations-not-real"} {
		expectError(t, respond(handler, locationRequest(http.MethodGet, target, "", cookie, csrf, "", "")), 404, "NOT_FOUND")
	}
}

func TestLocationHTTPDenialsNeverApplyPartialInput(t *testing.T) {
	_, handler, cookie, csrf := locationHTTPFixture(t)
	for _, body := range []string{`{}`, `{"name":"Missing code"}`, `{"code":"Missing name"}`, `{"name":"Box","code":"BOX","sort_order":0.5}`, `{"name":"Box","code":"BOX","description":null}`, `{"name":"Box","code":"BOX","parent_id":{}}`} {
		expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", body, cookie, csrf, "rejected-create-key", "")), 422, "VALIDATION_ERROR")
	}
	response := respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree", "", cookie, csrf, "", ""))
	var list store.LocationList
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Data) != 0 {
		t.Fatal("malformed input left authoritative locations behind")
	}
	root := responseLocation(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"Original","code":"BOX"}`, cookie, csrf, "rejected-create-key", "")))
	expectError(t, respond(handler, locationRequest(http.MethodPatch, "/api/v1/locations/"+root.ID, `{}`, cookie, csrf, "empty-patch-key", `"1"`)), 422, "VALIDATION_ERROR")
	expectError(t, respond(handler, locationRequest(http.MethodDelete, "/api/v1/locations/"+root.ID, `{}`, cookie, csrf, "body-delete-key", `"1"`)), 400, "INVALID_BODY")
	if got := responseLocation(t, respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/"+root.ID, "", cookie, csrf, "", ""))); got.Name != "Original" || got.Revision != 1 {
		t.Fatal("invalid edit/delete changed the resource")
	}
}

type beforeRead struct {
	io.Reader
	before func()
}

func (reader *beforeRead) Read(buffer []byte) (int, error) {
	if reader.before != nil {
		callback := reader.before
		reader.before = nil
		callback()
	}
	return reader.Reader.Read(buffer)
}

func TestLocationHTTPRevalidatesRevocationAfterMiddlewareBeforeMutation(t *testing.T) {
	auth, handler, cookie, csrf := locationHTTPFixture(t)
	r := locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"Not committed","code":"BOX"}`, cookie, csrf, "late-revocation-key", "")
	r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
		if _, err := auth.Logout(t.Context(), cookie.Value, "revoke-before-source-write"); err != nil {
			t.Fatal(err)
		}
	}})
	expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
	fresh, freshCSRF := testCredentials(t, auth, changedPassword)
	response := respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree", "", fresh, freshCSRF, "", ""))
	var list store.LocationList
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Data) != 0 {
		t.Fatal("revoked-after-middleware request committed business data")
	}
}

func TestLocationHTTPUsesTheUnmodifiedGeneralRequestBudget(t *testing.T) {
	_, handler, cookie, csrf := locationHTTPFixture(t)
	for range 60 {
		response := respond(handler, locationRequest(http.MethodGet, "/api/v1/locations/tree", "", cookie, csrf, "", ""))
		if response.Code != 200 {
			t.Fatal("normal list did not use the general read budget", response.Code)
		}
	}
	expectError(t, respond(handler, locationRequest(http.MethodPost, "/api/v1/locations", `{"name":"Limited","code":"BOX"}`, cookie, csrf, "limited-create-key", "")), 429, "RATE_LIMITED")
}

func TestLocationErrorsAreSafeMachineReadableAndNeverClearCookies(t *testing.T) {
	for _, input := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"}, {store.ErrInvalidCursor, 400, "INVALID_CURSOR"},
		{store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"}, {store.ErrRevisionConflict, 409, "REVISION_CONFLICT"},
		{store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"}, {store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"},
		{store.ErrLocationCode, 409, "LOCATION_CODE_CONFLICT"}, {store.ErrLocationCycle, 409, "LOCATION_CYCLE"},
		{store.ErrLocationArchived, 409, "LOCATION_ARCHIVED"}, {store.ErrLocationChildren, 409, "LOCATION_HAS_CHILDREN"}, {store.ErrLocationInUse, 409, "LOCATION_IN_USE"},
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"},
		{errors.New("private database failure /private/path secret=fixture"), 503, "SERVICE_UNAVAILABLE"},
	} {
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "fixture-request")
		writeLocationError(response, input.err, "fixture-request")
		expectError(t, response, input.status, input.code)
		if strings.Contains(response.Body.String(), "/private/path") || strings.Contains(response.Body.String(), "secret=") {
			t.Fatal("domain diagnostic escaped the safe error boundary")
		}
	}
}
