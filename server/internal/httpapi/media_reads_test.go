package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"homebox/internal/password"
	"homebox/internal/store"
)

type mediaHTTPFixture struct {
	auth                  *store.Auth
	handler               http.Handler
	cookie                *http.Cookie
	csrf                  string
	owner, sibling, empty store.Item
	unit                  store.Unit
	location              store.Location
}

// Seed retained source offline because no upload or MediaLink writer exists yet.
// Real owner services create their own source, counters, projections and events.
func newMediaHTTPFixture(t *testing.T) mediaHTTPFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "homebox.sqlite")
	db, err := store.Open(t.Context(), store.Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	auth, err := db.NewAuth(store.AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	setupAccount(t, auth, true)
	cookie, _ := testCredentials(t, auth, changedPassword)
	catalog, err := store.NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	createItem := func(key, name string) store.Item {
		t.Helper()
		item, err := catalog.Create(t.Context(), cookie.Value, key, store.ItemCreate{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	fixture := mediaHTTPFixture{
		owner:   createItem("media-http-owner", "Media owner"),
		sibling: createItem("media-http-sibling", "Other owner"),
		empty:   createItem("media-http-empty", "Empty owner"),
	}
	inventory, err := store.NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	fixture.unit, err = inventory.CreateUnit(t.Context(), cookie.Value, "media-http-unit", fixture.sibling.ID, fixture.sibling.Revision, store.UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	locations, err := store.NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	fixture.location, err = locations.Create(t.Context(), cookie.Value, "media-http-location", 0, store.LocationCreate{Code: "MEDIA", Name: "Media location"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err = raw.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	projection := func(kind, id string, revision int64, value any) {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,?)", kind, id, revision)
		exec("INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,?,?,?)", kind, id, revision, string(payload), "2026-11-06T00:00:00Z")
	}
	type mediaProjection struct {
		ID             string  `json:"id"`
		SHA256         string  `json:"sha256"`
		ThumbURL       *string `json:"thumb_url"`
		Width          int64   `json:"width"`
		Height         int64   `json:"height"`
		ThumbSHA256    *string `json:"thumb_sha256"`
		MIMEType       string  `json:"mime_type"`
		Bytes          int64   `json:"bytes"`
		PerceptualHash *string `json:"perceptual_hash"`
	}
	for _, image := range []struct {
		id, mime string
		revision int64
	}{{"photo", "image/heic", 7}, {"links", "image/png", 3}, {"empty-media", "image/png", 1}} {
		digest := strings.Repeat("a", 64)
		exec(`INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,?,10,20,30,?)`, image.id, "private-storage-key-"+image.id, digest, image.mime, "2026-11-06T00:00:00Z")
		projection("media", image.id, image.revision, mediaProjection{ID: image.id, SHA256: digest, Width: 10, Height: 20, MIMEType: image.mime, Bytes: 30})
	}
	type linkProjection struct {
		ID          string `json:"id"`
		MediaID     string `json:"media_id"`
		OwnerType   string `json:"owner_type"`
		OwnerID     string `json:"owner_id"`
		IsCover     bool   `json:"is_cover"`
		SortOrder   int64  `json:"sort_order"`
		Description string `json:"description"`
	}
	seedLink := func(id, mediaID, kind, ownerID string, revision int64) {
		t.Helper()
		column := map[string]string{"item": "item_id", "unit": "item_unit_id", "location": "location_id"}[kind]
		if column == "" {
			t.Fatal("invalid test owner")
		}
		description := "\x00Exact é\n"
		exec("INSERT INTO media_links(id,media_id,"+column+",is_cover,sort_order,description) VALUES(?,?,?,1,-3,?)", id, mediaID, ownerID, description)
		projection("media_link", id, revision, linkProjection{id, mediaID, kind, ownerID, true, -3, description})
	}
	for index := range 51 {
		seedLink(fmt.Sprintf("item-%03d", index), "photo", "item", fixture.owner.ID, 1)
	}
	seedLink("sibling", "photo", "item", fixture.sibling.ID, 5)
	seedLink("unit", "photo", "unit", fixture.unit.ID, 1)
	seedLink("location", "photo", "location", fixture.location.ID, 1)
	seedLink("reserved", "links", "item", fixture.sibling.ID, 1)
	exec(`INSERT INTO ocr_results(id,media_id,raw_text,engine,engine_version,created_at) VALUES('private-ocr','photo','NeverPublishUnreviewedMediaOCR','fixture','1','2026-11-06T00:00:00Z')`)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(t.Context(), store.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	fixture.auth, err = db.NewAuth(store.AuthConfig{PasswordParams: password.Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.cookie, fixture.csrf = testCredentials(t, fixture.auth, changedPassword)
	fixture.handler = authHandler(t, fixture.auth)
	return fixture
}

func mediaHTTPGet(f mediaHTTPFixture, path string) *httptest.ResponseRecorder {
	return respond(f.handler, domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", ""))
}

func mediaHTTPResource(t *testing.T, response *httptest.ResponseRecorder, id string, revision int64) store.Media {
	t.Helper()
	var image store.Media
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &image) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 10 ||
		image.ID != id || image.Revision != revision || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(revision, 10)) ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid Media HTTP resource: status %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "private-storage-key") || strings.Contains(response.Body.String(), "NeverPublishUnreviewedMediaOCR") || fields["perceptual_hash"] != nil {
		t.Fatal("Media HTTP resource exposed private source")
	}
	return image
}

func mediaHTTPPage(t *testing.T, response *httptest.ResponseRecorder) store.MediaLinkList {
	t.Helper()
	var page store.MediaLinkList
	var fields map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil ||
		len(fields) != 2 || fields["next_cursor"] == nil || page.Data == nil || response.Header().Get("ETag") != "" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid Media link HTTP page: status %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "private-storage-key") || strings.Contains(response.Body.String(), "NeverPublishUnreviewedMediaOCR") {
		t.Fatal("Media link page exposed private source")
	}
	return page
}

func TestMediaHTTPReadsActualSourceCompleteScopedPagesAndReservedIdentity(t *testing.T) {
	f := newMediaHTTPFixture(t)
	image := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 7)
	if image.Verification != "unverified" || image.MIMEType != "image/heic" || image.ThumbURL != nil || image.ThumbSHA256 != nil {
		t.Fatal("historical claims became verified bytes or an invented thumbnail")
	}
	alias := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo/view"), "photo", 7)
	if !reflect.DeepEqual(image, alias) {
		t.Fatal("Media view alias changed source or revision")
	}
	reserved := mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	if reserved.Verification != "unverified" {
		t.Fatal("reserved Media ID changed its verification")
	}
	reservedLinks := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links/links"))
	if len(reservedLinks.Data) != 1 || reservedLinks.Data[0].ID != "reserved" || reservedLinks.Data[0].MediaID != "links" || reservedLinks.NextCursor != nil {
		t.Fatal("reserved Media ID could not select its own real membership")
	}
	ownerPath := "/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.owner.ID)
	first := mediaHTTPPage(t, mediaHTTPGet(f, ownerPath))
	if len(first.Data) != 50 || first.NextCursor == nil {
		t.Fatal("owner page did not apply the documented default")
	}
	last := mediaHTTPPage(t, mediaHTTPGet(f, ownerPath+"&cursor="+url.QueryEscape(*first.NextCursor)))
	if len(last.Data) != 1 || last.NextCursor != nil {
		t.Fatal("owner continuation did not complete actual links")
	}
	for index, link := range append(first.Data, last.Data...) {
		if link.ID != fmt.Sprintf("item-%03d", index) || link.OwnerID != f.owner.ID || link.OwnerType != "item" || link.Revision != 1 ||
			link.MediaID != "photo" || link.Media.ID != link.MediaID || link.Media.Revision != 7 || link.SortOrder != -3 || link.Description != "\x00Exact é\n" {
			t.Fatal("owner page lost a real source, link proof or retained value", index)
		}
	}
	byMedia := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/photo/links"))
	if len(byMedia.Data) != 50 || byMedia.NextCursor == nil {
		t.Fatal("Media membership did not apply the documented default")
	}
	remaining := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/photo/links?cursor="+url.QueryEscape(*byMedia.NextCursor)))
	if len(remaining.Data) != 4 || remaining.NextCursor != nil || remaining.Data[2].ID != "sibling" || remaining.Data[2].Revision != 5 {
		t.Fatal("Media membership omitted another owner's independent link")
	}
	for _, owner := range []struct{ kind, id string }{{"unit", f.unit.ID}, {"location", f.location.ID}} {
		page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type="+owner.kind+"&owner_id="+url.QueryEscape(owner.id)))
		if len(page.Data) != 1 || page.Data[0].OwnerType != owner.kind || page.Data[0].OwnerID != owner.id || page.NextCursor != nil {
			t.Fatal("HTTP page changed actual owner kind or identity", owner.kind)
		}
	}
	for _, path := range []string{"/api/v1/media/empty-media/links", "/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.empty.ID)} {
		page := mediaHTTPPage(t, mediaHTTPGet(f, path))
		if len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("actual empty scope did not return an explicit empty page")
		}
	}
	for _, path := range []string{
		"/api/v1/media/photo/links?cursor=" + url.QueryEscape(*first.NextCursor),
		ownerPath + "&cursor=" + url.QueryEscape(*byMedia.NextCursor),
		"/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.sibling.ID) + "&cursor=" + url.QueryEscape(*first.NextCursor),
	} {
		expectError(t, mediaHTTPGet(f, path), http.StatusBadRequest, "INVALID_CURSOR")
	}
}

func TestMediaHTTPRevalidatesAuthorizationAfterReadingEmptyBody(t *testing.T) {
	f := newMediaHTTPFixture(t)
	for index, path := range []string{"/api/v1/media/photo", "/api/v1/media/photo/view", "/api/v1/media/photo/links", "/api/v1/media/links?owner_type=item&owner_id=" + url.QueryEscape(f.owner.ID)} {
		request := domainRequest(http.MethodGet, path, "", f.cookie, f.csrf, "", "")
		request.Body = io.NopCloser(&beforeRead{Reader: request.Body, before: func() {
			if _, err := f.auth.Logout(t.Context(), f.cookie.Value, fmt.Sprintf("media-read-revoke-%d", index)); err != nil {
				t.Fatal(err)
			}
		}})
		expectError(t, respond(f.handler, request), http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
		f.cookie, f.csrf = testCredentials(t, f.auth, changedPassword)
	}
}
