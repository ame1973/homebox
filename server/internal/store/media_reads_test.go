package store

import (
	"context"
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

func TestMediaReadsExposeRealUnverifiedClaimsAndIndependentLinkViews(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, `UPDATE media SET mime_type='image/heic' WHERE id='shared-a'`)
	media, err := library.Get(t.Context(), fixture.token, "shared-a")
	if err != nil || media.ID != "shared-a" || media.Revision != 7 || media.Verification != "unverified" ||
		media.MIMEType != "image/heic" || media.ThumbURL != nil || media.ThumbSHA256 != nil {
		t.Fatal("read changed retained source or invented a safe thumbnail", media, err)
	}
	first, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 2)
	if err != nil || len(first.Data) != 2 || first.NextCursor == nil {
		t.Fatal("owner page did not return the actual selected links", first, err)
	}
	second, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, *first.NextCursor, 2)
	if err != nil || len(second.Data) != 1 || second.NextCursor != nil {
		t.Fatal("owner page did not complete the actual selected links", second, err)
	}
	all := append(first.Data, second.Data...)
	if all[0].ID != "owned-a" || all[1].ID != "owned-b" || all[2].ID != "owned-c" || all[0].OwnerType != "item" ||
		all[0].OwnerID != fixture.ownerID || all[0].Media.ID != all[0].MediaID || all[0].Media.Revision != 7 || all[0].Revision != 1 ||
		!all[0].IsCover || all[0].SortOrder != -3 || all[0].Description != "\x00Exact é\n" {
		t.Fatal("link views lost stable IDs, independent authority or exact retained values", all)
	}
	encoded, err := json.Marshal(all[0])
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]json.RawMessage
	if json.Unmarshal(encoded, &view) != nil || len(view) != 9 {
		t.Fatal("link view contains unexpected fields", string(encoded))
	}
	var image map[string]json.RawMessage
	if json.Unmarshal(view["media"], &image) != nil || len(image) != 10 || image["storage_key"] != nil || image["perceptual_hash"] != nil ||
		string(image["thumb_url"]) != "null" || string(image["thumb_sha256"]) != "null" {
		t.Fatal("media view exposed private keys, offline-only data or invented thumbnails", string(encoded))
	}
	byMedia, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", 100)
	if err != nil || len(byMedia.Data) != 3 || byMedia.NextCursor != nil || byMedia.Data[2].ID != "sibling" ||
		byMedia.Data[2].OwnerID != fixture.siblingID || byMedia.Data[2].Revision != 5 {
		t.Fatal("media page omitted an actual other-owner link or its independent revision", byMedia, err)
	}
	// Cache bytes are not authority for live source reads.
	execSQL(t, fixture.store.db, `UPDATE current_projections SET payload_json='{"poison":true}' WHERE entity_type IN('media','media_link')`)
	again, err := library.Get(t.Context(), fixture.token, "shared-a")
	if err != nil || !reflect.DeepEqual(media, again) {
		t.Fatal("media read copied current projection bytes", again, err)
	}
}

func TestMediaPagesBindPurposeOwnerLimitEpochAndCompletedSession(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 1)
	if err != nil || owner.NextCursor == nil {
		t.Fatal("owner cursor missing", err)
	}
	byMedia, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", 1)
	if err != nil || byMedia.NextCursor == nil {
		t.Fatal("media cursor missing", err)
	}
	for _, read := range []func() error{
		func() error {
			_, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, *byMedia.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", *owner.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.siblingID, *owner.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-b", *byMedia.NextCursor, 1)
			return err
		},
		func() error {
			_, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", *byMedia.NextCursor, 2)
			return err
		},
		func() error {
			_, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", *byMedia.NextCursor+"x", 1)
			return err
		},
	} {
		if err := read(); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("a live continuation crossed its signed scope", err)
		}
	}
	parts := strings.Split(*owner.NextCursor, ".")
	for _, invalid := range []string{"unsigned-id", *owner.NextCursor + ".extra", parts[0] + "=." + parts[1], parts[0] + "." + strings.Repeat("A", 43)} {
		if page, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, invalid, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil {
			t.Fatal("noncanonical or tampered continuation returned a page", err)
		}
	}
	base := fixture.auth.now()
	fixture.auth.now = func() time.Time { return base.Add(16 * time.Minute) }
	if _, err = library.PageMediaLinks(t.Context(), fixture.token, "shared-a", *byMedia.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("expired continuation was accepted", err)
	}
	fixture.auth.now = func() time.Time { return base }
	execSQL(t, fixture.store.db, `UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1`)
	if _, err = library.Get(t.Context(), fixture.token, "shared-a"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("dataset rotation did not revoke media reads", err)
	}
}

func TestMediaOwnerPagesCoverUnitsLocationsAndEmptyActualMedia(t *testing.T) {
	for _, ownerType := range []string{"unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture := newMediaCascadeFixture(t, ownerType)
			library, err := NewMediaLibrary(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			page, err := library.PageOwnerLinks(t.Context(), fixture.token, ownerType, fixture.ownerID, "", 100)
			if err != nil || len(page.Data) != 3 || page.NextCursor != nil {
				t.Fatal("selected real owner links were incomplete", page, err)
			}
			for _, link := range page.Data {
				if link.OwnerType != ownerType || link.OwnerID != fixture.ownerID || link.MediaID != link.Media.ID {
					t.Fatal("link page borrowed another owner or Media", link)
				}
			}
			execSQL(t, fixture.store.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('links','opaque-reserved',?,'image/png',1,1,1,'2026')`, strings.Repeat("b", 64))
			seedMediaProjection(t, fixture.store, "links")
			empty, err := library.PageMediaLinks(t.Context(), fixture.token, "links", "", 50)
			if err != nil || empty.Data == nil || len(empty.Data) != 0 || empty.NextCursor != nil {
				t.Fatal("actual reserved-ID Media did not return an explicit empty page", empty, err)
			}
			image, err := library.Get(t.Context(), fixture.token, "links")
			if err != nil || image.ID != "links" || image.Revision != 1 {
				t.Fatal("reserved Media identity was silently rejected", image, err)
			}
		})
	}
}

func TestMediaReadsWithholdCorruptLookaheadAndDoNotInventOwners(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = library.Get(t.Context(), fixture.token, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing Media was treated as retained metadata", err)
	}
	if _, err = library.PageOwnerLinks(t.Context(), fixture.token, "item", "missing", "", 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing owner was treated as an empty collection", err)
	}
	if _, err = library.PageMediaLinks(t.Context(), fixture.token, "missing", "", 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing Media was treated as an empty collection", err)
	}
	if _, err = library.PageOwnerLinks(t.Context(), fixture.token, "other", fixture.ownerID, "", 2); !errors.Is(err, ErrDomainInput) {
		t.Fatal("unsupported owner kind was accepted", err)
	}
	execSQL(t, fixture.store.db, `UPDATE media SET verification='verified',mime_type='image/heic' WHERE id='shared-b'`)
	page, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 2)
	if !errors.Is(err, ErrDomainState) || len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("invalid lookahead published a misleading first page", page, err)
	}
	if _, err = library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, strings.Repeat("a", 2049), 2); !errors.Is(err, ErrDomainInput) {
		t.Fatal("oversized live continuation was accepted", err)
	}
	for _, invalid := range []string{"", strings.Repeat("界", 129), strings.Repeat("\x00", 129), string([]byte{0xff})} {
		if _, err := library.Get(t.Context(), fixture.token, invalid); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid Media identity was accepted", err)
		}
		if _, err := library.PageMediaLinks(t.Context(), fixture.token, invalid, "", 1); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid Media page identity was accepted", err)
		}
		if _, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", invalid, "", 1); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid owner identity was accepted", err)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded owner page was accepted", err)
		}
		if _, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", limit); !errors.Is(err, ErrDomainInput) {
			t.Fatal("unbounded membership page was accepted", err)
		}
	}
	if _, err = library.Get(context.Background(), "invalid", "shared-a"); !errors.Is(err, ErrAuthentication) {
		t.Fatal("unauthenticated source read succeeded", err)
	}
}

func TestMediaReadCursorsFollowFreshLoginButNotRevocationOrRestoredEpoch(t *testing.T) {
	for _, state := range []string{"fresh login", "mandatory", "revoked", "session expired", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			fixture := newMediaCascadeFixture(t, "item")
			library, err := NewMediaLibrary(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 1)
			if err != nil || owner.NextCursor == nil {
				t.Fatal("missing owner continuation", err)
			}
			membership, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", 1)
			if err != nil || membership.NextCursor == nil {
				t.Fatal("missing membership continuation", err)
			}
			for _, cursor := range []string{*owner.NextCursor, *membership.NextCursor} {
				parts := strings.Split(cursor, ".")
				if len(parts) != 2 {
					t.Fatal("signed continuation has an invalid envelope")
				}
				body, err := base64.RawURLEncoding.DecodeString(parts[0])
				if err != nil || strings.Contains(string(body), fixture.token) || strings.Contains(string(body), fixture.ownerID) || strings.Contains(string(body), "shared-a") {
					t.Fatal("continuation exposed credentials or collection scope", err)
				}
				if _, err = library.Get(t.Context(), cursor, "shared-a"); !errors.Is(err, ErrAuthentication) {
					t.Fatal("a continuation authenticated as a session", err)
				}
			}
			token, want := fixture.token, ErrAuthentication
			switch state {
			case "fresh login":
				if _, err = fixture.auth.Logout(t.Context(), token, "media-read-logout"); err != nil {
					t.Fatal(err)
				}
				_, token = login(t, fixture.auth)
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "session expired":
				execSQL(t, fixture.store.db, "UPDATE sessions SET expires_at=?", stamp(fixture.auth.now()))
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='media-restored-epoch'")
				_, token = login(t, fixture.auth)
				want = ErrInvalidCursor
			case "invalid":
				token = "invalid"
			}
			before := domainState(t, fixture.store)
			first, firstErr := library.PageOwnerLinks(t.Context(), token, "item", fixture.ownerID, *owner.NextCursor, 1)
			second, secondErr := library.PageMediaLinks(t.Context(), token, "shared-a", *membership.NextCursor, 1)
			if state == "fresh login" {
				if firstErr != nil || secondErr != nil || len(first.Data) != 1 || len(second.Data) != 1 {
					t.Fatal("same-principal fresh login could not continue its reviewed scope", firstErr, secondErr)
				}
			} else if !errors.Is(firstErr, want) || !errors.Is(secondErr, want) || first.Data != nil || second.Data != nil || first.NextCursor != nil || second.NextCursor != nil {
				t.Fatal("invalid session or dataset exposed a page", firstErr, secondErr)
			}
			if _, err = library.Get(t.Context(), token, "shared-a"); state == "epoch" || state == "fresh login" {
				if err != nil {
					t.Fatal("valid new session could not read actual Media", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatal("invalid session could read Media", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaReadPagesRetainBoundedUnicodeIdentitiesAndCompleteLargeMembership(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	mediaID := strings.Repeat("\x00", 128)
	ownerID := strings.Repeat("\x03", 128)
	seedUnmaterializedCatalogItem(t, fixture.store, ownerID, "Imported owner")
	for _, id := range []string{mediaID, "large-media", "owner-media"} {
		execSQL(t, fixture.store.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,'image/png',1,1,1,'2026')`, id, "opaque-"+id, strings.Repeat("c", 64))
		seedMediaProjection(t, fixture.store, id)
	}
	identities := []string{strings.Repeat("\x00", 128), strings.Repeat("\x01", 128), " <&\" é ", "new", "tree", strings.Repeat("😀", 128), "\U0010ffff"}
	for _, id := range identities {
		execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id) VALUES(?,?,?)`, id, mediaID, fixture.ownerID)
		seedMediaLinkProjection(t, fixture.store, id)
	}
	for _, id := range []string{"imported-1", "imported-2"} {
		execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id) VALUES(?,'owner-media',?)`, id, ownerID)
		seedMediaLinkProjection(t, fixture.store, id)
	}
	for index := range 205 {
		id := fmt.Sprintf("large-link-%03d", index)
		execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id) VALUES(?,?,?)`, id, "large-media", fixture.ownerID)
		seedMediaLinkProjection(t, fixture.store, id)
	}
	before := domainState(t, fixture.store)
	first, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", ownerID, "", 1)
	if err != nil || len(first.Data) != 1 || first.Data[0].ID != "imported-1" || first.Data[0].OwnerID != ownerID || first.NextCursor == nil {
		t.Fatal("bounded imported owner identity was changed", err)
	}
	last, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", ownerID, *first.NextCursor, 1)
	if err != nil || len(last.Data) != 1 || last.Data[0].ID != "imported-2" || last.Data[0].OwnerID != ownerID || last.NextCursor != nil {
		t.Fatal("bounded imported owner continuation was changed", err)
	}
	image, err := library.Get(t.Context(), fixture.token, mediaID)
	if err != nil || image.ID != mediaID {
		t.Fatal("bounded Media identity was changed", err)
	}
	sort.Strings(identities)
	cursor := ""
	for index, id := range identities {
		page, err := library.PageMediaLinks(t.Context(), fixture.token, mediaID, cursor, 1)
		if err != nil || len(page.Data) != 1 || page.Data[0].ID != id || page.Data[0].Media.ID != mediaID {
			t.Fatal("membership cursor lost a bounded Unicode or control identity", index, err)
		}
		if index == len(identities)-1 {
			if page.NextCursor != nil {
				t.Fatal("terminal membership page has a continuation")
			}
		} else {
			if page.NextCursor == nil || len(*page.NextCursor) > 2048 {
				t.Fatal("bounded identity could not be encoded in the live cursor")
			}
			cursor = *page.NextCursor
		}
	}
	cursor, count := "", 0
	for {
		page, err := library.PageMediaLinks(t.Context(), fixture.token, "large-media", cursor, 100)
		if err != nil || len(page.Data) == 0 {
			t.Fatal("large selected Media membership was incomplete", err)
		}
		for _, link := range page.Data {
			if link.ID != fmt.Sprintf("large-link-%03d", count) || link.OwnerID != fixture.ownerID || link.Media.ID != "large-media" {
				t.Fatal("large selected Media membership changed order or authority", count)
			}
			count++
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != 205 {
		t.Fatal("large selected Media membership was truncated", count)
	}
	ownerIDs := append([]string{}, identities...)
	ownerIDs = append(ownerIDs, "owned-a", "owned-b", "owned-c")
	for index := range 205 {
		ownerIDs = append(ownerIDs, fmt.Sprintf("large-link-%03d", index))
	}
	sort.Strings(ownerIDs)
	cursor, count = "", 0
	for {
		page, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, cursor, 100)
		if err != nil || len(page.Data) == 0 {
			t.Fatal("large selected owner membership was incomplete", err)
		}
		for _, link := range page.Data {
			if count >= len(ownerIDs) || link.ID != ownerIDs[count] || link.OwnerID != fixture.ownerID || link.MediaID != link.Media.ID {
				t.Fatal("large selected owner membership changed order or authority", count)
			}
			count++
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != len(ownerIDs) {
		t.Fatal("large selected owner membership was truncated", count)
	}
	requireDomainState(t, fixture.store, before)
}

func TestMediaReadPagesRejectLateSessionChangesWithoutReturningData(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-media-read' WHERE singleton=1", ErrAuthentication},
		{"mandatory", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
	} {
		t.Run(failure.name, func(t *testing.T) {
			execSQL(t, fixture.store.db, "CREATE TRIGGER fail_media_read "+failure.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+failure.statement+"; END")
			defer execSQL(t, fixture.store.db, "DROP TRIGGER fail_media_read")
			before := domainState(t, fixture.store)
			if value, err := library.Get(t.Context(), fixture.token, "shared-a"); !errors.Is(err, failure.want) || value != (Media{}) {
				t.Fatal("late authorization change exposed Media", err)
			}
			requireDomainState(t, fixture.store, before)
			if page, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 1); !errors.Is(err, failure.want) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("late authorization change exposed owner links", err)
			}
			requireDomainState(t, fixture.store, before)
			if page, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", 1); !errors.Is(err, failure.want) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("late authorization change exposed Media links", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaReadPagesValidateRealLookaheadAuthorityAndNeverReturnPrefixes(t *testing.T) {
	for _, corruption := range []struct{ name, statement string }{
		{"link counter", `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='owned-b'; DELETE FROM projection_revisions WHERE entity_type='media_link' AND entity_id='owned-b'`},
		{"link description", `UPDATE media_links SET description=CAST(x'ff' AS TEXT) WHERE id='owned-b'`},
		{"linked media", `UPDATE media SET mime_type=CAST(x'ff' AS TEXT) WHERE id='shared-a'`},
		{"other owner", `UPDATE items SET name=CAST(x'ff' AS TEXT) WHERE id=(SELECT item_id FROM media_links WHERE id='sibling')`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newMediaCascadeFixture(t, "item")
			library, err := NewMediaLibrary(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, fixture.store.db, corruption.statement)
			before := domainState(t, fixture.store)
			limit := 1
			if corruption.name == "other owner" {
				limit = 2
			}
			page, err := library.PageMediaLinks(t.Context(), fixture.token, "shared-a", "", limit)
			if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("invalid source or lookahead leaked a successful page", page, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}
