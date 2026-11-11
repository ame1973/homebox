package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestGlobalMediaPageIncludesLinkedAndUnlinkedActualSources(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := library.Page(t.Context(), token, "", 50)
	if err != nil || empty.Data == nil || len(empty.Data) != 0 || empty.NextCursor != nil {
		t.Fatal("empty Media collection was not an explicit empty page", empty, err)
	}

	ids := []string{"shared-a", "shared-b", ".", "..", "a/b", `back\slash`, "links", "new", "\x00Media\r\n", strings.Repeat("\x00", 128), "☕😀", strings.Repeat("😀", 128), "\U0010ffff"}
	for index := range 205 {
		ids = append(ids, fmt.Sprintf("global-%03d", index))
	}
	// A single transaction avoids a filesystem synchronization for each fixture
	// row while retaining real source, counters, and current projection bytes.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,'image/png',10,20,30,'2026')`, id, fmt.Sprintf("private-storage-%03d", index), strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO projection_revisions VALUES('media',?,1)`, id); err != nil {
			t.Fatal(err)
		}
		value, err := loadMedia(t.Context(), tx, id)
		if err != nil {
			t.Fatal("invalid Media fixture source", err)
		}
		payload, err := json.Marshal(value.mediaData)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('media',?,1,?,'2026')`, id, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Linked owner"})
	execSQL(t, s.db, `INSERT INTO media_links(id,media_id,item_id) VALUES('global-linked','shared-a',?)`, item.ID)
	seedMediaLinkProjection(t, s, "global-linked")
	setCatalogProjectionRevision(t, s, "media", "shared-a", 7)
	execSQL(t, s.db, `UPDATE media SET mime_type='image/heic' WHERE id='shared-a'`)
	execSQL(t, s.db, `UPDATE media SET verification='verified' WHERE id='shared-b'`)
	execSQL(t, s.db, `INSERT INTO ocr_results(id,media_id,raw_text,engine,engine_version,created_at) VALUES('global-raw','shared-a','NeverPublishUnreviewedMediaOCR','fixture','1','2026')`)
	execSQL(t, s.db, `UPDATE current_projections SET payload_json='{"poison":true}' WHERE entity_type='media'`)
	before := domainState(t, s)

	sort.Strings(ids)
	cursor, count := "", 0
	var all []Media
	for {
		page, err := library.Page(t.Context(), token, cursor, 100)
		if err != nil || page.Data == nil || len(page.Data) == 0 || len(page.Data) > 100 {
			t.Fatal("global page did not contain actual Media", page, err)
		}
		for _, image := range page.Data {
			if count >= len(ids) || image.ID != ids[count] || image.Width != 10 || image.Height != 20 || image.Bytes != 30 ||
				image.SHA256 != strings.Repeat("a", 64) || image.ThumbURL != nil || image.ThumbSHA256 != nil {
				t.Fatal("global page changed source order, identity, or claims", count, image)
			}
			switch image.ID {
			case "shared-a":
				if image.Revision != 7 || image.MIMEType != "image/heic" || image.Verification != "unverified" {
					t.Fatal("linked retained claims or independent counter changed", image)
				}
			case "shared-b":
				if image.Revision != 1 || image.MIMEType != "image/png" || image.Verification != "verified" {
					t.Fatal("unlinked verified representation changed", image)
				}
			default:
				if image.Revision != 1 || image.MIMEType != "image/png" || image.Verification != "unverified" {
					t.Fatal("unlinked retained claims changed", image)
				}
			}
			count++
		}
		all = append(all, page.Data...)
		if page.NextCursor == nil {
			break
		}
		if len(*page.NextCursor) > 2048 {
			t.Fatal("global continuation exceeded its byte budget")
		}
		cursor = *page.NextCursor
	}
	if count != len(ids) {
		t.Fatal("global pages omitted actual linked or unlinked Media", count, len(ids))
	}
	first, err := library.Page(t.Context(), token, "", 1)
	if err != nil || len(first.Data) != 1 || first.Data[0].ID != strings.Repeat("\x00", 128) || first.NextCursor == nil || len(*first.NextCursor) > 2048 {
		t.Fatal("a maximum-length NUL identity could not be a cursor position", first, err)
	}
	second, err := library.Page(t.Context(), token, *first.NextCursor, 1)
	if err != nil || len(second.Data) != 1 || second.Data[0].ID != "\x00Media\r\n" {
		t.Fatal("the NUL cursor lost the following exact identity", second, err)
	}
	// With 218 source rows, a page size of 31 places the 128-emoji ID at
	// the end of a page and the highest Unicode ID on the following page.
	cursor = ""
	for pageNumber := 0; pageNumber < 7; pageNumber++ {
		page, err := library.Page(t.Context(), token, cursor, 31)
		if err != nil || len(page.Data) != 31 || page.NextCursor == nil {
			t.Fatal("the bounded Unicode page sequence was incomplete", pageNumber, err)
		}
		if pageNumber == 6 && (page.Data[30].ID != strings.Repeat("😀", 128) || len(*page.NextCursor) > 2048) {
			t.Fatal("a maximum-length Unicode identity could not be a cursor position", err)
		}
		cursor = *page.NextCursor
	}
	last, err := library.Page(t.Context(), token, cursor, 31)
	if err != nil || len(last.Data) != 1 || last.Data[0].ID != "\U0010ffff" || last.NextCursor != nil {
		t.Fatal("the Unicode cursor lost the final exact identity", last, err)
	}
	for _, id := range []string{"shared-a", "shared-b", "\x00Media\r\n", strings.Repeat("😀", 128)} {
		actual, err := library.Get(t.Context(), token, id)
		if err != nil {
			t.Fatal(err)
		}
		index := sort.SearchStrings(ids, id)
		if !reflect.DeepEqual(all[index], actual) {
			t.Fatal("global Media differs from the exact source read", id)
		}
	}
	encoded, err := json.Marshal(all)
	if err != nil || strings.Contains(string(encoded), "private-storage") || strings.Contains(string(encoded), "NeverPublishUnreviewedMediaOCR") || strings.Contains(string(encoded), "poison") || strings.Contains(string(encoded), "storage_key") || strings.Contains(string(encoded), "perceptual_hash") {
		t.Fatal("global Media page exposed private or unpublished source", err)
	}
	var fields map[string]json.RawMessage
	if value, err := json.Marshal(all[0]); err != nil || json.Unmarshal(value, &fields) != nil || len(fields) != 10 || string(fields["thumb_url"]) != "null" || string(fields["thumb_sha256"]) != "null" {
		t.Fatal("global Media page has unexpected public fields", fields, err)
	}
	requireDomainState(t, s, before)
}

func TestGlobalMediaCursorsBindPurposeLimitAndLifetime(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	first, err := library.Page(t.Context(), fixture.token, "", 1)
	if err != nil || len(first.Data) != 1 || first.NextCursor == nil {
		t.Fatal("missing global continuation", first, err)
	}
	owner, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 1)
	if err != nil || owner.NextCursor == nil {
		t.Fatal("missing owner continuation", err)
	}
	membership, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", 1)
	if err != nil || membership.NextCursor == nil {
		t.Fatal("missing membership continuation", err)
	}
	parts := strings.Split(*first.NextCursor, ".")
	if len(parts) != 2 {
		t.Fatal("invalid signed continuation envelope")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || strings.Contains(string(body), fixture.token) || strings.Contains(string(body), "opaque-") {
		t.Fatal("continuation exposed a credential or private storage key", err)
	}
	if _, err = library.Get(t.Context(), *first.NextCursor, "shared-a"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("a continuation authenticated as a session", err)
	}
	var scoped metadataCursor
	if err = json.Unmarshal(body, &scoped); err != nil {
		t.Fatal(err)
	}
	scoped.AdminID = "different-principal"
	otherPrincipal, err := encodeMetadataCursor(fixture.auth, "media-list-cursor-v1", scoped)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := library.Page(t.Context(), fixture.token, otherPrincipal, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("a correctly signed cursor from another principal returned a page", page, err)
	}
	for _, read := range []func() error{
		func() error { _, err := library.Page(t.Context(), fixture.token, *owner.NextCursor, 1); return err },
		func() error {
			_, err := library.Page(t.Context(), fixture.token, *membership.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, *first.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", *first.NextCursor, 1)
			return err
		},
		func() error { _, err := library.Page(t.Context(), fixture.token, *first.NextCursor, 2); return err },
		func() error { _, err := library.Page(t.Context(), fixture.token, *first.NextCursor+"x", 1); return err },
		func() error {
			_, err := library.Page(t.Context(), fixture.token, parts[0]+"=."+parts[1], 1)
			return err
		},
		func() error { _, err := library.Page(t.Context(), fixture.token, "unsigned-id", 1); return err },
	} {
		if err := read(); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("global continuation crossed a signed scope or accepted tampering", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if page, err := library.Page(t.Context(), fixture.token, "", limit); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid limit returned a global page", page, err)
		}
	}
	if page, err := library.Page(t.Context(), fixture.token, strings.Repeat("a", 2049), 1); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("oversized continuation returned a global page", page, err)
	}
	base := fixture.auth.now()
	fixture.auth.now = func() time.Time { return base.Add(16 * time.Minute) }
	if page, err := library.Page(t.Context(), fixture.token, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("expired continuation returned a global page", page, err)
	}
}

func TestGlobalMediaPageWithholdsCorruptSourceAndLookahead(t *testing.T) {
	for _, corruption := range []struct{ name, statement string }{
		{"missing counter", `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-b'; DELETE FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-b'`},
		{"invalid digest", `UPDATE media SET sha256='not-a-digest' WHERE id='shared-b'`},
		{"invalid UTF-8", `UPDATE media SET mime_type=CAST(x'ff' AS TEXT) WHERE id='shared-b'`},
		{"unsupported verified MIME", `UPDATE media SET verification='verified',mime_type='image/heic' WHERE id='shared-b'`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newMediaCascadeFixture(t, "item")
			library, err := NewMediaLibrary(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, fixture.store.db, corruption.statement)
			before := domainState(t, fixture.store)
			for _, limit := range []int{1, 2} {
				page, err := library.Page(t.Context(), fixture.token, "", limit)
				if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("invalid source or lookahead exposed a page", page, err)
				}
				requireDomainState(t, fixture.store, before)
			}
		})
	}
}
