package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mediaAttachInput(fixture mediaCascadeFixture, ownerType string) MediaLinkCreate {
	input := MediaLinkCreate{OwnerRevision: fixture.revision}
	switch ownerType {
	case "item":
		input.ItemID = &fixture.ownerID
	case "unit":
		input.ItemUnitID = &fixture.ownerID
	case "location":
		input.LocationID = &fixture.ownerID
	default:
		panic("unknown fixture owner")
	}
	return input
}

func mediaLibraryFixture(t *testing.T, ownerType string) (mediaCascadeFixture, *MediaLibrary, MediaLinkCreate) {
	t.Helper()
	fixture := newMediaCascadeFixture(t, ownerType)
	library, err := NewMediaLibrary(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, library, mediaAttachInput(fixture, ownerType)
}

func TestMediaAttachSnapshotsWholeCallerInputBeforeFingerprintAndClock(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, ownerType)
			var owner *string
			switch ownerType {
			case "item":
				owner = input.ItemID
			case "unit":
				owner = input.ItemUnitID
			case "location":
				owner = input.LocationID
			}
			originalOwner := *owner
			cover, position, description := false, int64(7), "Original reviewed caption"
			input.IsCover, input.SortOrder, input.Description = &cover, &position, &description
			clock := fixture.auth.now
			fixture.auth.now = func() time.Time {
				*owner, cover, position, description = "different-owner", true, 9, "Changed after fingerprint"
				return clock()
			}
			link, err := library.Attach(t.Context(), fixture.token, "media-attach-input-snapshot", "shared-a", 7, input)
			if err != nil || link.OwnerID != originalOwner || link.OwnerType != ownerType || link.IsCover || link.SortOrder != 7 || link.Description != "Original reviewed caption" {
				t.Fatal("caller pointers changed the reviewed/fingerprinted attachment", link, err)
			}
			fixture.auth.now = clock
			*owner, cover, position, description = originalOwner, false, 7, "Original reviewed caption"
			replay, err := library.Attach(t.Context(), fixture.token, "media-attach-input-snapshot", "shared-a", 7, input)
			if err != nil || !reflect.DeepEqual(link, replay) {
				t.Fatal("frozen response and original request fingerprint diverged", err)
			}
		})
	}
}

func TestMediaAttachPublishesIndependentLinkAndActualOwnerScopes(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, ownerType)
			s := fixture.store
			input.SortOrder = testPointer(int64(7))
			input.Description = testPointer("Exact é\n\x00caption")
			before := domainState(t, s)
			var unit Unit
			if ownerType == "unit" {
				var err error
				unit, err = loadUnit(t.Context(), s.db, fixture.ownerID)
				if err != nil {
					t.Fatal(err)
				}
			}
			var link MediaLink
			changed := requireProjectionReplay(t, s, func() {
				var err error
				link, err = library.Attach(t.Context(), fixture.token, "attach-shared-owner", "shared-a", 7, input)
				if err != nil {
					t.Fatal("attach failed", err)
				}
			})
			if link.Revision != 1 || link.MediaID != "shared-a" || link.OwnerType != ownerType || link.OwnerID != fixture.ownerID ||
				link.IsCover || link.SortOrder != 7 || link.Description != *input.Description {
				t.Fatalf("wrong independent link result: %+v", link)
			}
			for _, key := range []projectionKey{{"media", "shared-a"}, {"media_link", link.ID}, {ownerType, fixture.ownerID}} {
				if !changed[key] {
					t.Fatalf("missing changed aggregate: %+v", key)
				}
			}
			if ownerType == "unit" && !changed[projectionKey{"item", unit.ItemID}] {
				t.Fatal("Unit membership did not advance the real owning Item")
			}
			for key := range changed {
				if key.kind == "search_doc" || key.kind == "inventory_event" || key.kind == "media" && key.id == "shared-b" ||
					key.kind == "media_link" && key.id != link.ID {
					t.Fatalf("unrelated source was republished: %+v", key)
				}
			}
			after := domainState(t, s)
			for _, table := range []string{"media", "inventory_events", "stock_balances", "ocr_results", "search_documents", "unit_search_documents"} {
				if after[table] != before[table] {
					t.Fatalf("attachment changed unrelated %s", table)
				}
			}
			if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 8 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id IN('owned-a','owned-b','owned-c','sibling')") != 4 {
				t.Fatal("attachment lost siblings or failed to advance membership")
			}
			page, err := library.PageOwnerLinks(t.Context(), fixture.token, ownerType, fixture.ownerID, "", 50)
			if err != nil || len(page.Data) != 4 {
				t.Fatal("owner page did not include the new link", err)
			}
			events := scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events")
			replay, err := library.Attach(t.Context(), fixture.token, "attach-shared-owner", "shared-a", 7, input)
			if err != nil || !reflect.DeepEqual(replay, link) || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != events {
				t.Fatal("original-key replay used current proofs or duplicated the event", err)
			}
		})
	}
}

func TestMediaAttachRequiresBothOriginalProofsAndConflictsOnCover(t *testing.T) {
	fixture, library, input := mediaLibraryFixture(t, "item")
	ctx := t.Context()
	before := domainState(t, fixture.store)
	if _, err := library.Attach(ctx, fixture.token, "wrong-media-proof", "shared-a", 6, input); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale Media proof was not rejected", err)
	}
	wrongOwner := input
	wrongOwner.OwnerRevision++
	if _, err := library.Attach(ctx, fixture.token, "wrong-owner-proof", "shared-a", 7, wrongOwner); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale owner proof was not rejected", err)
	}
	cover := input
	cover.IsCover = testPointer(true)
	if _, err := library.Attach(ctx, fixture.token, "conflicting-cover", "shared-a", 7, cover); !errors.Is(err, ErrMediaCoverConflict) {
		t.Fatal("existing cover was silently replaced", err)
	}
	for _, invalid := range []MediaLinkCreate{
		{}, {ItemID: input.ItemID}, {ItemID: input.ItemID, LocationID: input.ItemID, OwnerRevision: input.OwnerRevision},
		{ItemID: testPointer(""), OwnerRevision: input.OwnerRevision},
		{ItemID: input.ItemID, OwnerRevision: input.OwnerRevision, SortOrder: testPointer(int64(-1))},
		{ItemID: input.ItemID, OwnerRevision: input.OwnerRevision, SortOrder: testPointer(maxSafeInteger + 1)},
		{ItemID: input.ItemID, OwnerRevision: input.OwnerRevision, Description: testPointer(strings.Repeat("a", 10001))},
		{ItemID: input.ItemID, OwnerRevision: input.OwnerRevision, Description: testPointer(string([]byte{0xff}))},
	} {
		if _, err := library.Attach(ctx, fixture.token, "invalid-media-input", "shared-a", 7, invalid); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid attachment input was accepted", err)
		}
	}
	requireDomainState(t, fixture.store, before)
	link, err := library.Attach(ctx, fixture.token, "same-key-presence", "shared-a", 7, input)
	if err != nil {
		t.Fatal(err)
	}
	explicitFalse := input
	explicitFalse.IsCover = testPointer(false)
	if _, err = library.Attach(ctx, fixture.token, "same-key-presence", "shared-a", 7, explicitFalse); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed optional-field presence reused the frozen claim", err)
	}
	if _, err = library.Attach(ctx, fixture.token, "new-key-stale-both", "shared-a", 7, input); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("fresh key silently rebased stale proofs", err)
	}
	replay, err := library.Attach(ctx, fixture.token, "same-key-presence", "shared-a", 7, input)
	if err != nil || !reflect.DeepEqual(replay, link) {
		t.Fatal("original intent did not reconcile", err)
	}
}

func TestMediaAttachConcurrentRequestsRespectBothOriginalProofs(t *testing.T) {
	for _, scenario := range []string{"same key", "shared media", "shared owner"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, "item")
			type request struct {
				key, media string
				revision   int64
				input      MediaLinkCreate
			}
			requests := []request{
				{"concurrent-media-one", "shared-a", 7, input},
				{"concurrent-media-two", "shared-a", 7, input},
			}
			switch scenario {
			case "same key":
				requests[1] = requests[0]
			case "shared media":
				sibling, err := loadItem(t.Context(), fixture.store.db, fixture.siblingID)
				if err != nil {
					t.Fatal(err)
				}
				requests[1].input = MediaLinkCreate{ItemID: &sibling.ID, OwnerRevision: sibling.Revision}
			case "shared owner":
				requests[1].media, requests[1].revision = "shared-b", 13
			}
			type response struct {
				link MediaLink
				err  error
			}
			start, results := make(chan struct{}), make(chan response, len(requests))
			for _, request := range requests {
				go func() {
					<-start
					link, err := library.Attach(t.Context(), fixture.token, request.key, request.media, request.revision, request.input)
					results <- response{link, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			if scenario == "same key" {
				if first.err != nil || second.err != nil || first.link != second.link || first.link.Revision != 1 {
					t.Fatal("identical concurrent request did not reconcile one link", first, second)
				}
			} else if !((first.err == nil && errors.Is(second.err, ErrRevisionConflict)) ||
				(second.err == nil && errors.Is(first.err, ErrRevisionConflict))) {
				t.Fatal("concurrent attachment bypassed an original Media or owner proof", first, second)
			}
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links") != 5 ||
				scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media_id_links'") != 1 {
				t.Fatal("concurrent requests created multiple links or audit entries")
			}
		})
	}
}

func TestMediaAttachAdvancesLocationAncestorsWithoutTouchingDescendants(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	root := createLocation(t, locations, token, "MEDIA-ROOT", nil)
	child := createLocation(t, locations, token, "MEDIA-CHILD", &root.ID)
	grandchild := createLocation(t, locations, token, "MEDIA-GRANDCHILD", &child.ID)
	root, err := locations.Get(t.Context(), token, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err = locations.Get(t.Context(), token, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err = locations.Get(t.Context(), token, grandchild.ID)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('image','private',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "image")
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	changed := requireProjectionReplay(t, s, func() {
		if _, err = library.Attach(t.Context(), token, "attach-location-tree", "image", 1, MediaLinkCreate{LocationID: &child.ID, OwnerRevision: child.Revision}); err != nil {
			t.Fatal(err)
		}
	})
	if !changed[projectionKey{"location", root.ID}] || !changed[projectionKey{"location", child.ID}] || changed[projectionKey{"location", grandchild.ID}] {
		t.Fatal("location attachment changed the wrong path")
	}
	rootAfter, _ := locations.Get(t.Context(), token, root.ID)
	childAfter, _ := locations.Get(t.Context(), token, child.ID)
	grandchildAfter, _ := locations.Get(t.Context(), token, grandchild.ID)
	if rootAfter.Revision != root.Revision+1 || childAfter.Revision != child.Revision+1 || grandchildAfter.Revision != grandchild.Revision {
		t.Fatal("ancestor or descendant location proof is incorrect")
	}
}

func TestMediaAttachReplaySurvivesActualOwnerDeletion(t *testing.T) {
	fixture, library, input := mediaLibraryFixture(t, "item")
	link, err := library.Attach(t.Context(), fixture.token, "attach-before-owner-delete", "shared-a", 7, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.delete(t.Context(), fixture.token, "delete-owner-after-attach", fixture.revision+1); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links WHERE id=?", link.ID) != 0 {
		t.Fatal("owner deletion did not remove its actual link")
	}
	replay, err := library.Attach(t.Context(), fixture.token, "attach-before-owner-delete", "shared-a", 7, input)
	if err != nil || !reflect.DeepEqual(replay, link) {
		t.Fatal("frozen attachment could not reconcile after owner deletion", err)
	}
	if _, err = library.Attach(t.Context(), fixture.token, "new-key-after-owner-delete", "shared-a", 9, input); !errors.Is(err, ErrNotFound) {
		t.Fatal("new attachment did not validate its missing owner", err)
	}
}

func TestMediaAttachSupportsRetainedOwnerWithoutCurrentProjection(t *testing.T) {
	fixture, library, input := mediaLibraryFixture(t, "item")
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.ownerID)
	if _, err := library.Attach(t.Context(), fixture.token, "attach-legacy-item", "shared-a", 7, input); err != nil {
		t.Fatal("real retained owner/counter was mistaken for missing cache authority", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='item' AND entity_id=? AND entity_revision=?", fixture.ownerID, fixture.revision+1) != 1 {
		t.Fatal("attachment did not publish the actual new owner projection")
	}
}

func TestMediaAttachRequiresRealSourcesAndCounters(t *testing.T) {
	for _, failure := range []string{"missing Media counter", "missing Media projection", "missing sibling counter", "missing sibling projection", "missing related Media projection", "missing owner counter", "Media overflow", "owner overflow"} {
		t.Run(failure, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, "item")
			s := fixture.store
			want := ErrDomainState
			switch failure {
			case "missing Media counter":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'; DELETE FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'`)
			case "missing Media projection":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'`)
			case "missing sibling counter":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'; DELETE FROM projection_revisions WHERE entity_type='media_link' AND entity_id='sibling'`)
			case "missing sibling projection":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'`)
			case "missing related Media projection":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-b'`)
			case "missing owner counter":
				execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?`, fixture.ownerID)
				execSQL(t, s.db, `DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?`, fixture.ownerID)
			case "Media overflow":
				setCatalogProjectionRevision(t, s, "media", "shared-a", maxSafeInteger)
				want = ErrRevisionOverflow
			case "owner overflow":
				setCatalogProjectionRevision(t, s, "item", fixture.ownerID, maxSafeInteger)
				input.OwnerRevision = maxSafeInteger
				want = ErrRevisionOverflow
			}
			mediaRevision := int64(7)
			if failure == "Media overflow" {
				mediaRevision = maxSafeInteger
			}
			before := domainState(t, s)
			if _, err := library.Attach(t.Context(), fixture.token, "attach-corrupt-source", "shared-a", mediaRevision, input); !errors.Is(err, want) {
				t.Fatalf("%s returned %v, want %v", failure, err, want)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestMediaAttachRevalidatesSessionAndReplaysAfterFreshLogin(t *testing.T) {
	for _, state := range []string{"invalid", "revoked", "mandatory", "epoch"} {
		t.Run(state, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, "item")
			token, want := fixture.token, ErrAuthentication
			switch state {
			case "invalid":
				token = "invalid"
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
			}
			before := domainState(t, fixture.store)
			if _, err := library.Attach(t.Context(), token, "attach-invalid-session", "shared-a", 7, input); !errors.Is(err, want) {
				t.Fatal("invalid session attached a link", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
	fixture, library, input := mediaLibraryFixture(t, "item")
	created, err := library.Attach(t.Context(), fixture.token, "attach-fresh-login", "shared-a", 7, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.auth.Logout(t.Context(), fixture.token, "logout-before-media-retry"); err != nil {
		t.Fatal(err)
	}
	_, freshToken := login(t, fixture.auth)
	if replay, err := library.Attach(t.Context(), freshToken, "attach-fresh-login", "shared-a", 7, input); err != nil || !reflect.DeepEqual(created, replay) {
		t.Fatal("fresh session could not reconcile original administrator/epoch key", err)
	}
}

func TestMediaAttachRollsBackLateTamperingAndRetriesOriginalKey(t *testing.T) {
	failures := []struct {
		name, trigger string
		want          error
	}{
		{"ignored source insert", `CREATE TRIGGER fail_media_attach BEFORE INSERT ON media_links BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"reused independent counter", `CREATE TRIGGER fail_media_attach BEFORE INSERT ON media_links BEGIN INSERT INTO projection_revisions VALUES('media_link',NEW.id,9); END`, ErrDomainState},
		{"rewritten new link", `CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE media_links SET description='tampered' WHERE id=NEW.id; END`, ErrDomainState},
		{"rewritten selected source", `CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-a'; END`, ErrDomainState},
		{"rewritten unrelated owner media", `CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-b'; END`, ErrDomainState},
		{"rewritten owner sibling", `CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE media_links SET description='tampered' WHERE id='owned-c'; END`, ErrDomainState},
		{"rewritten media sibling", `CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE media_links SET description='tampered' WHERE id='sibling'; END`, ErrDomainState},
		{"late other media projection", `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-b'; END`, ErrDomainState},
		{"late media projection", `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-a'; END`, ErrDomainState},
		{"late unreviewed owner link", `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN INSERT INTO media_links(id,media_id,item_id,item_unit_id,location_id) SELECT 'unexpected','shared-b',item_id,item_unit_id,location_id FROM media_links WHERE id='owned-c'; END`, ErrDomainState},
		{"late unreviewed media link", `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN INSERT INTO media_links(id,media_id,item_id,item_unit_id,location_id) SELECT 'unexpected','shared-a',item_id,item_unit_id,location_id FROM media_links WHERE id='sibling'; END`, ErrDomainState},
		{"late link projection", `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN DELETE FROM sync_changes WHERE entity_type='media_link' AND entity_revision=1; END`, ErrDomainState},
		{"ignored media projection", `CREATE TRIGGER fail_media_attach BEFORE UPDATE ON current_projections WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"ignored media outbox", `CREATE TRIGGER fail_media_attach BEFORE INSERT ON sync_changes WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"ignored event", `CREATE TRIGGER fail_media_attach BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"late authorization", `CREATE TRIGGER fail_media_attach AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication},
		{"deferred commit", `CREATE TABLE media_attach_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT; CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN INSERT INTO media_attach_guard VALUES('missing'); END`, nil},
	}
	for _, ownerType := range []string{"item", "unit", "location"} {
		for _, failure := range failures {
			t.Run(ownerType+"/"+failure.name, func(t *testing.T) {
				fixture, library, input := mediaLibraryFixture(t, ownerType)
				execSQL(t, fixture.store.db, failure.trigger)
				before := domainState(t, fixture.store)
				link, err := library.Attach(t.Context(), fixture.token, "failed-attach-media", "shared-a", 7, input)
				if err == nil || link.ID != "" || failure.want != nil && !errors.Is(err, failure.want) {
					t.Fatal("failed attachment released a result", err)
				}
				requireDomainState(t, fixture.store, before)
				if failure.name == "deferred commit" && scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_attach_guard") != 0 {
					t.Fatal("deferred failure committed a guard row")
				}
				execSQL(t, fixture.store.db, "DROP TRIGGER fail_media_attach")
				if link, err = library.Attach(t.Context(), fixture.token, "failed-attach-media", "shared-a", 7, input); err != nil || link.Revision != 1 {
					t.Fatal("rolled-back attachment did not accept its original key", err)
				}
			})
		}
	}
}

func TestMediaAttachRejectsIgnoredOwnerWritesAndLateOwnerTampering(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		for _, failure := range []string{"ignored timestamp", "late owner source", "late owner projection"} {
			t.Run(ownerType+"/"+failure, func(t *testing.T) {
				fixture, library, input := mediaLibraryFixture(t, ownerType)
				var table, idField, kind string
				switch ownerType {
				case "item":
					table, idField, kind = "items", "item_id", "item"
				case "unit":
					table, idField, kind = "item_units", "item_unit_id", "unit"
				case "location":
					table, idField, kind = "locations", "location_id", "location"
				}
				var trigger string
				switch failure {
				case "ignored timestamp":
					trigger = "CREATE TRIGGER fail_media_attach BEFORE UPDATE OF updated_at ON " + table + " BEGIN SELECT RAISE(IGNORE); END"
				case "late owner source":
					field := "notes"
					if ownerType == "location" {
						field = "name"
					}
					trigger = "CREATE TRIGGER fail_media_attach AFTER INSERT ON media_links BEGIN UPDATE " + table + " SET " + field + "='tampered' WHERE id=NEW." + idField + "; END"
				case "late owner projection":
					trigger = "CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='" + kind + "'; END"
				}
				execSQL(t, fixture.store.db, trigger)
				before := domainState(t, fixture.store)
				if _, err := library.Attach(t.Context(), fixture.token, "failed-owner-attach", "shared-a", 7, input); !errors.Is(err, ErrDomainState) {
					t.Fatal("owner change escaped final validation", err)
				}
				requireDomainState(t, fixture.store, before)
				execSQL(t, fixture.store.db, "DROP TRIGGER fail_media_attach")
				if _, err := library.Attach(t.Context(), fixture.token, "failed-owner-attach", "shared-a", 7, input); err != nil {
					t.Fatal("failed owner write retained the original key", err)
				}
			})
		}
	}
}

func TestMediaAttachValidatesMembershipBeyondOnePage(t *testing.T) {
	for _, scope := range []string{"media", "owner"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, input := mediaLibraryFixture(t, "location")
			for i := range 205 {
				id := fmt.Sprintf("media-attach-sibling-%03d", i)
				if scope == "media" {
					execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id,description) VALUES(?,'shared-a',?,'retained')`, id, fixture.siblingID)
				} else {
					execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,location_id,description) VALUES(?,'shared-b',?,'retained')`, id, fixture.ownerID)
				}
				seedMediaLinkProjection(t, fixture.store, id)
			}
			execSQL(t, fixture.store.db, `CREATE TRIGGER fail_media_attach AFTER INSERT ON mutation_events BEGIN
 UPDATE media_links SET description='late change' WHERE id='media-attach-sibling-204'; END`)
			before := domainState(t, fixture.store)
			if _, err := library.Attach(t.Context(), fixture.token, "large-attach", "shared-a", 7, input); !errors.Is(err, ErrDomainState) {
				t.Fatal("late mutation beyond one page escaped validation", err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_media_attach")
			if _, err := library.Attach(t.Context(), fixture.token, "large-attach", "shared-a", 7, input); err != nil {
				t.Fatal("complete membership attachment failed", err)
			}
		})
	}
}
