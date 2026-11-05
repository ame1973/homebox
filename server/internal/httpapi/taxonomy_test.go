package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/store"
)

func taxonomyReply[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil {
		t.Fatalf("taxonomy request failed: %d %s", response.Code, response.Body.String())
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(response.Body.Bytes(), &fields) != nil || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("taxonomy reply escaped its response boundary")
	}
	var id, name string
	var revision int64
	switch resource := any(value).(type) {
	case store.Category:
		id, name, revision = resource.ID, resource.Name, resource.Revision
		if len(fields) != 4 || fields["parent_id"] == nil {
			t.Fatal("category wire fields differ from the contract")
		}
	case store.Tag:
		id, name, revision = resource.ID, resource.Name, resource.Revision
		if len(fields) != 3 {
			t.Fatal("tag wire fields differ from the contract")
		}
	default:
		t.Fatal("unsupported resource assertion")
	}
	if !boundedString(id, 1, 128) || !boundedString(name, 1, 200) || revision < 1 || revision > maxJSONInteger || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(revision, 10)) {
		t.Fatal("taxonomy response identity, fields, or strong ETag are invalid")
	}
	return value
}

func TestTaxonomyHTTPCategoryCRUDUsesReviewedProofsAndFrozenReplies(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	rootBody := `{"name":"  居家 😀 é  "}`
	created := call(http.MethodPost, "/api/v1/categories", rootBody, "root-category-key", "")
	root := taxonomyReply[store.Category](t, created)
	if root.ParentID != nil || root.Name != "  居家 😀 é  " || root.Revision != 1 {
		t.Fatal("root creation changed exact source values")
	}
	childBody := fmt.Sprintf(`{"name":"Child","parent_id":%q}`, root.ID)
	expectError(t, call(http.MethodPost, "/api/v1/categories", childBody, "unreviewed-child", ""), 409, "PRECONDITION_REQUIRED")
	child := taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", childBody, "child-category-key", `"1"`))
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatal("reviewed child did not retain its parent")
	}
	rootPath, childPath := "/api/v1/categories/"+root.ID, "/api/v1/categories/"+child.ID
	expectError(t, call(http.MethodPost, "/api/v1/categories", childBody, "stale-child-key", `"1"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, rootPath, `{"name":"Stale"}`, "stale-category-edit", `"1"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, rootPath, fmt.Sprintf(`{"parent_id":%q}`, child.ID), "cycle-category-key", `"2"`), 409, "CATEGORY_CYCLE")
	expectError(t, call(http.MethodDelete, rootPath, "", "nonleaf-category-key", `"2"`), 409, "CATEGORY_HAS_CHILDREN")
	child = taxonomyReply[store.Category](t, call(http.MethodPatch, childPath, `{"name":"Renamed"}`, "rename-category-key", `"1"`))
	if child.Revision != 2 || child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatal("name-only edit changed the parent or failed to advance its proof")
	}
	child = taxonomyReply[store.Category](t, call(http.MethodPatch, childPath, `{"parent_id":null}`, "root-move-category", `"2"`))
	if child.Revision != 3 || child.ParentID != nil || child.Name != "Renamed" {
		t.Fatal("explicit-null partial move lost its meaning")
	}
	renamed := call(http.MethodPatch, rootPath, `{"name":"Updated"}`, "rename-root-category", `"4"`)
	root = taxonomyReply[store.Category](t, renamed)
	if root.Revision != 5 {
		t.Fatal("ancestor proof did not cover descendant rename and movement")
	}
	noop := taxonomyReply[store.Category](t, call(http.MethodPatch, rootPath, `{"name":"Updated"}`, "noop-root-category", `"5"`))
	if noop != root {
		t.Fatal("exact no-op changed the category aggregate")
	}
	replayed := call(http.MethodPost, "/api/v1/categories", rootBody, "root-category-key", "")
	if replayed.Code != 200 || replayed.Body.String() != created.Body.String() || replayed.Header().Get("ETag") != `"1"` {
		t.Fatal("create replay rebased the frozen result onto current metadata")
	}
	expectError(t, call(http.MethodPatch, rootPath, `{"name":"Different"}`, "rename-root-category", `"4"`), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, call(http.MethodPost, "/api/v1/tags", `{"name":"Different operation"}`, "root-category-key", ""), 409, "IDEMPOTENCY_CONFLICT")
	deleted := call(http.MethodDelete, rootPath, "", "delete-root-category", `"5"`)
	var ack store.ActionAck
	if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil || !ack.OK || ack.OperationID == "" || ack.Revision < 1 || deleted.Header().Get("ETag") != "" {
		t.Fatal("category delete did not return the contract acknowledgement")
	}
	replayed = call(http.MethodDelete, rootPath, "", "delete-root-category", `"5"`)
	if replayed.Code != 200 || replayed.Body.String() != deleted.Body.String() {
		t.Fatal("deleted source could not replay its exact frozen acknowledgement")
	}
	expectError(t, call(http.MethodDelete, rootPath, "", "different-delete-key", `"5"`), 404, "NOT_FOUND")
	if response := call(http.MethodDelete, childPath, "", "delete-child-category", `"3"`); response.Code != 200 {
		t.Fatal("reviewed root leaf could not be deleted", response.Body.String())
	}
	var list store.CategoryList
	response := call(http.MethodGet, "/api/v1/categories", "", "", "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || list.Data == nil || len(list.Data) != 0 || list.NextCursor != nil || response.Header().Get("ETag") != "" {
		t.Fatal("category list retained deleted metadata or omitted the empty array")
	}
}

func TestTaxonomyHTTPTagNamesNoOpsAndSamePrincipalReplay(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	body := `{"name":"  標籤 😀 é  "}`
	created := call(http.MethodPost, "/api/v1/tags", body, "tag-create-key", "")
	tag := taxonomyReply[store.Tag](t, created)
	path := "/api/v1/tags/" + tag.ID
	expectError(t, call(http.MethodPost, "/api/v1/tags", body, "duplicate-tag-key", ""), 409, "TAG_NAME_CONFLICT")
	expectError(t, call(http.MethodPost, "/api/v1/tags", `{"name":"Other"}`, "tag-unrelated-proof", `"1"`), 400, "INVALID_REVISION")
	for index, name := range []string{"Tag", "tag", "é", "é", " Tag "} {
		taxonomyReply[store.Tag](t, call(http.MethodPost, "/api/v1/tags", fmt.Sprintf(`{"name":%q}`, name), fmt.Sprintf("distinct-tag-%d", index), ""))
	}
	noop := taxonomyReply[store.Tag](t, call(http.MethodPatch, path, body, "noop-tag-key", `"1"`))
	if noop != tag {
		t.Fatal("exact no-op changed a tag revision or source")
	}
	changed := taxonomyReply[store.Tag](t, call(http.MethodPatch, path, `{"name":"Changed"}`, "rename-tag-key", `"1"`))
	if changed.ID != tag.ID || changed.Revision != 2 || changed.Name != "Changed" {
		t.Fatal("tag rename replaced identity or failed to advance its proof")
	}
	expectError(t, call(http.MethodPatch, path, body, "stale-tag-key", `"1"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, path, `{"name":"Tag"}`, "conflicting-tag-name", `"2"`), 409, "TAG_NAME_CONFLICT")
	if _, err := auth.Logout(t.Context(), cookie.Value, "logout-before-tag-replay"); err != nil {
		t.Fatal(err)
	}
	expectError(t, call(http.MethodPost, "/api/v1/tags", body, "tag-create-key", ""), 401, "AUTHENTICATION_REQUIRED")
	cookie, csrf = testCredentials(t, auth, changedPassword)
	replayed := call(http.MethodPost, "/api/v1/tags", body, "tag-create-key", "")
	if replayed.Code != 200 || replayed.Body.String() != created.Body.String() || replayed.Header().Get("ETag") != `"1"` {
		t.Fatal("fresh session did not retain the principal-scoped frozen response")
	}
	deleted := call(http.MethodDelete, path, "", "delete-tag-key", `"2"`)
	replayed = call(http.MethodDelete, path, "", "delete-tag-key", `"2"`)
	if deleted.Code != 200 || replayed.Code != 200 || replayed.Body.String() != deleted.Body.String() || deleted.Header().Get("Set-Cookie") != "" {
		t.Fatal("tag deletion was not safely acknowledged/replayed")
	}
	replacement := taxonomyReply[store.Tag](t, call(http.MethodPost, "/api/v1/tags", `{"name":"Changed"}`, "replacement-tag-key", ""))
	if replacement.ID == tag.ID || replacement.Revision != 1 {
		t.Fatal("replacement name inherited the deleted tag's identity")
	}
}

func TestTaxonomyHTTPListQueriesAndCrossListCursors(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, ""))
	}
	for _, kind := range []string{"categories", "tags"} {
		for index := range 3 {
			response := call(http.MethodPost, "/api/v1/"+kind, fmt.Sprintf(`{"name":"Value %d"}`, index), fmt.Sprintf("page-%s-%d", kind, index))
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
		}
	}
	for index := range 3 {
		responseLocation(t, call(http.MethodPost, "/api/v1/locations", fmt.Sprintf(`{"name":"Place","code":"PLACE%d"}`, index), fmt.Sprintf("location-page-%d", index)))
	}
	cursors := make(map[string]string)
	for _, path := range []string{"/api/v1/categories", "/api/v1/tags", "/api/v1/locations/tree"} {
		response := call(http.MethodGet, path+"?limit=2", "", "")
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"next_cursor"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Data) == 0 || page.NextCursor == nil || response.Header().Get("ETag") != "" {
			t.Fatal("signed live page was not exposed", response.Code, response.Body.String())
		}
		cursors[path] = *page.NextCursor
		if path != "/api/v1/locations/tree" {
			next := call(http.MethodGet, path+"?limit=2&cursor="+url.QueryEscape(*page.NextCursor), "", "")
			var last struct {
				Data       []json.RawMessage `json:"data"`
				NextCursor *string           `json:"next_cursor"`
			}
			if next.Code != 200 || json.Unmarshal(next.Body.Bytes(), &last) != nil || len(last.Data) != 1 || last.NextCursor != nil {
				t.Fatal("signed continuation lost complete list coverage")
			}
			expectError(t, call(http.MethodGet, path+"?limit=1&cursor="+url.QueryEscape(*page.NextCursor), "", ""), 400, "INVALID_CURSOR")
		}
	}
	for source, cursor := range cursors {
		for _, target := range []string{"/api/v1/categories", "/api/v1/tags", "/api/v1/locations/tree"} {
			if source != target {
				expectError(t, call(http.MethodGet, target+"?limit=2&cursor="+url.QueryEscape(cursor), "", ""), 400, "INVALID_CURSOR")
			}
		}
	}
	for _, path := range []string{"/api/v1/categories", "/api/v1/tags"} {
		for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=1", "cursor=", "cursor=a&cursor=b", "unknown=1", "limit=%zz", "cursor=" + strings.Repeat("x", 2049)} {
			expectError(t, call(http.MethodGet, path+"?"+query, "", ""), 400, "INVALID_QUERY")
		}
		expectError(t, call(http.MethodGet, path+"?cursor=unsigned", "", ""), 400, "INVALID_CURSOR")
		r := domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")
		r.URL.RawQuery = strings.Repeat("x", 2301)
		expectError(t, respond(handler, r), 400, "INVALID_QUERY")
	}
}

func TestTaxonomyHTTPRequiresCompleteSessionAndWriteProofHeaders(t *testing.T) {
	for _, kind := range []string{"categories", "tags"} {
		t.Run(kind, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			path := "/api/v1/" + kind
			for _, input := range []struct {
				change func(*http.Request)
				status int
				code   string
			}{
				{func(r *http.Request) { r.Header.Del("Cookie") }, 401, "AUTHENTICATION_REQUIRED"},
				{func(r *http.Request) { r.AddCookie(cookie) }, 401, "AUTHENTICATION_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Add("X-CSRF-Token", csrf) }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") }, 403, "CSRF_REJECTED"},
				{func(r *http.Request) { r.Header.Del("X-HomeBox-Client-Version") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Add("X-HomeBox-Client-Version", supportedClientVersion) }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Set("X-HomeBox-Client-Version", "0.2.0") }, 426, "CLIENT_UPGRADE_REQUIRED"},
				{func(r *http.Request) { r.Header.Del("Idempotency-Key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Add("Idempotency-Key", "second-key") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Set("Idempotency-Key", "short") }, 400, "INVALID_IDEMPOTENCY_KEY"},
				{func(r *http.Request) { r.Header.Del("Origin") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Add("Origin", "https://homebox.test") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.Host = "foreign.test" }, 403, "ORIGIN_REJECTED"},
				{func(r *http.Request) { r.TLS = nil }, 403, "ORIGIN_REJECTED"},
			} {
				r := domainRequest(http.MethodPost, path, `{"name":"Protected"}`, cookie, csrf, "protected-taxonomy", "")
				input.change(r)
				expectError(t, respond(handler, r), input.status, input.code)
			}
			response := respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", ""))
			if response.Code != 200 || !strings.Contains(response.Body.String(), `"data":[]`) {
				t.Fatal("denied request left taxonomy data behind")
			}
		})
	}
}

func TestTaxonomyHTTPMandatoryChangeGatePrecedesBodyParsing(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, false)
	cookie, csrf := testCredentials(t, auth, initialPassword)
	handler := authHandler(t, auth)
	for _, kind := range []string{"categories", "tags"} {
		for _, input := range []struct{ method, suffix, revision string }{
			{http.MethodGet, "", ""}, {http.MethodPost, "", ""}, {http.MethodPatch, "/resource", `"1"`}, {http.MethodDelete, "/resource", `"1"`},
		} {
			expectError(t, respond(handler, domainRequest(input.method, "/api/v1/"+kind+input.suffix, `{invalid}`, cookie, csrf, "mandatory-taxonomy", input.revision)), 403, "PASSWORD_CHANGE_REQUIRED")
		}
	}
}

func TestTaxonomyHTTPRevisionMethodAndExactURLBoundaries(t *testing.T) {
	for _, kind := range []string{"categories", "tags"} {
		t.Run(kind, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			collection := "/api/v1/" + kind
			path := collection + "/resource"
			call := func(method, target, body, revision string) *httptest.ResponseRecorder {
				return respond(handler, domainRequest(method, target, body, cookie, csrf, "invalid-taxonomy-key", revision))
			}
			for _, revision := range []string{"1", `W/"1"`, `"0"`, `"01"`, `"-1"`, `"+1"`, `"1.0"`, `"1e0"`, `"9007199254740992"`, `"999999999999999999"`, `"1", "2"`, "*", `""`} {
				expectError(t, call(http.MethodPatch, path, `{"name":"No change"}`, revision), 400, "INVALID_REVISION")
			}
			r := domainRequest(http.MethodPatch, path, `{"name":"No change"}`, cookie, csrf, "duplicate-proof-key", `"1"`)
			r.Header.Add("If-Match", `"1"`)
			expectError(t, respond(handler, r), 400, "INVALID_REVISION")
			for _, method := range []string{http.MethodPatch, http.MethodDelete} {
				expectError(t, call(method, path, `{"name":"No change"}`, ""), 409, "PRECONDITION_REQUIRED")
			}
			expectError(t, call(http.MethodPost, collection, `{"name":"Root","parent_id":null}`, `"1"`), 400, "INVALID_REVISION")
			for _, target := range []string{path + "?unexpected=1", path + "?", collection + "/..", collection + "/%5ctarget", collection + "/%00target", collection + "/%61lias", collection + "/" + strings.Repeat("x", 129)} {
				expectError(t, call(http.MethodPatch, target, `{"name":"No change"}`, `"1"`), 400, "INVALID_TAXONOMY_URL")
			}
			expectError(t, call(http.MethodPost, collection+"?limit=1", `{"name":"No change"}`, ""), 400, "INVALID_TAXONOMY_URL")
			for _, input := range []struct{ method, target, allow string }{
				{http.MethodGet, path, "PATCH, DELETE"}, {http.MethodPut, path, "PATCH, DELETE"}, {http.MethodPost, path, "PATCH, DELETE"},
				{http.MethodHead, collection, "GET, POST"}, {http.MethodPatch, collection, "GET, POST"}, {http.MethodDelete, collection, "GET, POST"},
			} {
				response := call(input.method, input.target, "", "")
				expectError(t, response, 405, "METHOD_NOT_ALLOWED")
				if response.Header().Get("Allow") != input.allow {
					t.Fatal("method rejection omitted the exact Allow header")
				}
			}
			for _, target := range []string{collection + "/", path + "/unknown", collection + "-not-real"} {
				expectError(t, call(http.MethodGet, target, "", ""), 404, "NOT_FOUND")
			}
		})
	}
}

func TestTaxonomyHTTPMalformedInputAndBodyFreeMethodsNeverMutate(t *testing.T) {
	for _, kind := range []string{"categories", "tags"} {
		t.Run(kind, func(t *testing.T) {
			_, handler, cookie, csrf := domainHTTPFixture(t)
			collection := "/api/v1/" + kind
			for _, body := range []string{`{}`, `{"name":null}`, `{"Name":"Wrong"}`, `{"name":"Valid","unknown":true}`, `{"name":[]}`, `{"parent_id":null}`} {
				expectError(t, respond(handler, domainRequest(http.MethodPost, collection, body, cookie, csrf, "malformed-create-key", "")), 422, "VALIDATION_ERROR")
			}
			response := respond(handler, domainRequest(http.MethodPost, collection, `{"name":"Original"}`, cookie, csrf, "malformed-create-key", ""))
			var created struct{ ID string }
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID == "" {
				t.Fatal("rejected input claimed the key or left a partial source", response.Body.String())
			}
			path := collection + "/" + created.ID
			expectError(t, respond(handler, domainRequest(http.MethodPatch, path, `{}`, cookie, csrf, "malformed-patch-key", `"1"`)), 422, "VALIDATION_ERROR")
			for _, input := range []struct{ method, target string }{{http.MethodGet, collection}, {http.MethodDelete, path}} {
				for _, chunked := range []bool{false, true} {
					r := domainRequest(input.method, input.target, `{}`, cookie, csrf, "body-free-method-key", `"1"`)
					if chunked {
						r.ContentLength = -1
					}
					expectError(t, respond(handler, r), 400, "INVALID_BODY")
				}
				r := domainRequest(input.method, input.target, "", cookie, csrf, "encoded-body-key", `"1"`)
				r.Header.Set("Content-Encoding", "gzip")
				expectError(t, respond(handler, r), 400, "INVALID_BODY")
			}
			response = respond(handler, domainRequest(http.MethodGet, collection, "", cookie, csrf, "", ""))
			var list struct {
				Data []struct {
					ID, Name string
					Revision int64
				}
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != created.ID || list.Data[0].Name != "Original" || list.Data[0].Revision != 1 {
				t.Fatal("invalid create/edit/delete partially mutated taxonomy")
			}
		})
	}
}

func TestTaxonomyHTTPRevalidatesRevocationAfterMiddleware(t *testing.T) {
	for _, kind := range []string{"categories", "tags"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			t.Run(kind+method, func(t *testing.T) {
				auth, handler, cookie, csrf := domainHTTPFixture(t)
				collection := "/api/v1/" + kind
				created := respond(handler, domainRequest(http.MethodPost, collection, `{"name":"Original"}`, cookie, csrf, "before-revocation-key", ""))
				var source struct{ ID string }
				if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &source) != nil {
					t.Fatal("late-revocation setup failed")
				}
				path, body, revision := collection, `{"name":"Changed"}`, ""
				if method == http.MethodPatch || method == http.MethodDelete {
					path, revision = collection+"/"+source.ID, `"1"`
				}
				if method == http.MethodGet || method == http.MethodDelete {
					body = ""
				}
				r := domainRequest(method, path, body, cookie, csrf, "late-taxonomy-key", revision)
				r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
					if _, err := auth.Logout(t.Context(), cookie.Value, "late-revoke-taxonomy"); err != nil {
						t.Fatal(err)
					}
				}})
				expectError(t, respond(handler, r), 401, "AUTHENTICATION_REQUIRED")
				fresh, freshCSRF := testCredentials(t, auth, changedPassword)
				response := respond(handler, domainRequest(http.MethodGet, collection, "", fresh, freshCSRF, "", ""))
				var list struct{ Data []json.RawMessage }
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Data) != 1 || !strings.Contains(string(list.Data[0]), `"name":"Original"`) || !strings.Contains(string(list.Data[0]), `"revision":1`) {
					t.Fatal("revoked-after-middleware operation changed authoritative data")
				}
			})
		}
	}
}

func TestTaxonomyHTTPRevalidatesReviewedProofAfterBodyRead(t *testing.T) {
	for _, kind := range []string{"categories", "tags"} {
		t.Run(kind, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			taxonomy, err := store.NewTaxonomy(auth)
			if err != nil {
				t.Fatal(err)
			}
			collection := "/api/v1/" + kind
			created := respond(handler, domainRequest(http.MethodPost, collection, `{"name":"Original"}`, cookie, csrf, "before-concurrent-key", ""))
			var subject struct{ ID string }
			if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &subject) != nil {
				t.Fatal("concurrent-proof setup failed")
			}
			method, path, body := http.MethodPatch, collection+"/"+subject.ID, `{"name":"Not rebased"}`
			if kind == "categories" {
				method, path, body = http.MethodPost, collection, fmt.Sprintf(`{"name":"Not rebased","parent_id":%q}`, subject.ID)
			}
			r := domainRequest(method, path, body, cookie, csrf, "original-reviewed-key", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				var err error
				if kind == "categories" {
					_, err = taxonomy.CreateCategory(t.Context(), cookie.Value, "concurrent-child-key", 1, store.CategoryCreate{Name: "Concurrent child", ParentID: &subject.ID})
				} else {
					_, err = taxonomy.PatchTag(t.Context(), cookie.Value, "concurrent-rename-key", subject.ID, 1, "Concurrent name")
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 409, "REVISION_CONFLICT")
			response := respond(handler, domainRequest(http.MethodGet, collection, "", cookie, csrf, "", ""))
			if response.Code != 200 || strings.Contains(response.Body.String(), "Not rebased") || !strings.Contains(response.Body.String(), `"revision":2`) {
				t.Fatal("HTTP command silently adopted a new parent or target proof")
			}
		})
	}
}

func TestTaxonomyHTTPUsesSharedUnmodifiedGeneralRequestBudget(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	for index := range 60 {
		path := "/api/v1/categories"
		if index%2 == 0 {
			path = "/api/v1/tags"
		}
		if response := respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")); response.Code != 200 {
			t.Fatal("taxonomy requests did not share the ordinary domain budget", response.Code)
		}
	}
	for _, path := range []string{"/api/v1/categories", "/api/v1/tags", "/api/v1/locations"} {
		response := respond(handler, domainRequest(http.MethodPost, path, `{"name":"Limited"}`, cookie, csrf, "limited-taxonomy-key", ""))
		expectError(t, response, 429, "RATE_LIMITED")
		if response.Header().Get("Retry-After") == "" {
			t.Fatal("limited taxonomy response omitted Retry-After")
		}
	}
}

func TestTaxonomyErrorsAreAllowlistedSafeAndNeverClearCookies(t *testing.T) {
	for _, input := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrDomainInput, 422, "VALIDATION_ERROR"}, {store.ErrNotFound, 404, "NOT_FOUND"},
		{store.ErrInvalidCursor, 400, "INVALID_CURSOR"}, {store.ErrPreconditionRequired, 409, "PRECONDITION_REQUIRED"},
		{store.ErrRevisionConflict, 409, "REVISION_CONFLICT"}, {store.ErrIdempotencyExpired, 409, "IDEMPOTENCY_EXPIRED"},
		{store.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"}, {store.ErrCategoryCycle, 409, "CATEGORY_CYCLE"},
		{store.ErrCategoryChildren, 409, "CATEGORY_HAS_CHILDREN"}, {store.ErrTagName, 409, "TAG_NAME_CONFLICT"},
		{store.ErrAuthentication, 401, "AUTHENTICATION_REQUIRED"}, {store.ErrPasswordChangeRequired, 403, "PASSWORD_CHANGE_REQUIRED"},
		{store.ErrDomainState, 503, "SERVICE_UNAVAILABLE"}, {store.ErrRevisionOverflow, 503, "SERVICE_UNAVAILABLE"},
		{errors.New("private database failure /private/path secret=fixture"), 503, "SERVICE_UNAVAILABLE"},
	} {
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "fixture-request")
		writeTaxonomyError(response, fmt.Errorf("internal wrapper: %w", input.err), "fixture-request")
		expectError(t, response, input.status, input.code)
		if strings.Contains(response.Body.String(), "/private/path") || strings.Contains(response.Body.String(), "secret=") || strings.Contains(response.Body.String(), "internal wrapper") {
			t.Fatal("unsafe taxonomy diagnostic escaped the static error mapping")
		}
	}
}
