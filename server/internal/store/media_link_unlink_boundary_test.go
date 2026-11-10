package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMediaLinkUnlinkUsesOnlyItsOriginalProofAndReplaysAfterOwnerDeletion(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	catalog, err := NewCatalog(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	name := "Changed owner before unlink"
	if _, err = catalog.Patch(t.Context(), fixture.token, "media-unlink-concurrent-owner", fixture.ownerID, fixture.revision, ItemPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	sibling, err := loadItem(t.Context(), fixture.store.db, fixture.siblingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = library.Attach(t.Context(), fixture.token, "media-unlink-concurrent-membership", "shared-a", 7, MediaLinkCreate{ItemID: &fixture.siblingID, OwnerRevision: sibling.Revision}); err != nil {
		t.Fatal(err)
	}
	ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-independent-proof", "owned-a", 1)
	if err != nil || !ack.OK || ack.Revision != 2 {
		t.Fatal("unlink invented a Media or owner proof", ack, err)
	}
	if scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 9 ||
		scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.ownerID) != fixture.revision+2 {
		t.Fatal("unlink did not advance actual current Media and owner scopes")
	}
	if _, err = fixture.delete(t.Context(), fixture.token, "media-owner-delete-after-unlink", fixture.revision+2); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.auth.Logout(t.Context(), fixture.token, "media-unlink-logout-after-delete"); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	if replay, err := library.Unlink(t.Context(), fresh, "media-unlink-independent-proof", "owned-a", 1); err != nil || !reflect.DeepEqual(replay, ack) {
		t.Fatal("fresh-login reconciliation required the deleted owner or link", err)
	}
	if _, err = library.Unlink(t.Context(), fresh, "media-unlink-independent-proof", "owned-b", 1); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("a different target reused the original unlink key", err)
	}
	if _, err = library.Unlink(t.Context(), fresh, "media-unlink-independent-proof", "owned-a", 2); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("a different proof reused the original unlink key", err)
	}
	if _, err = library.Unlink(t.Context(), fresh, "media-unlink-new-key-after-delete", "owned-a", 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("a new key invented a deleted link", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media WHERE id IN ('shared-a','shared-b')") != 2 ||
		scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links WHERE id='sibling'") != 1 {
		t.Fatal("unlink or owner deletion removed shared Media or another owner's link")
	}
}

func TestMediaLinkUnlinkRejectsInvalidProofsAndMissingAuthority(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	before := domainState(t, fixture.store)
	for _, tc := range []struct {
		id       string
		revision int64
		want     error
	}{
		{"", 1, ErrDomainInput}, {strings.Repeat("界", 129), 1, ErrDomainInput}, {string([]byte{0xff}), 1, ErrDomainInput},
		{"owned-a", 0, ErrPreconditionRequired}, {"owned-a", -1, ErrDomainInput}, {"owned-a", maxSafeInteger + 1, ErrDomainInput},
		{"owned-a", 2, ErrRevisionConflict}, {"missing", 1, ErrNotFound},
	} {
		if ack, err := library.Unlink(t.Context(), fixture.token, "media-invalid-unlink", tc.id, tc.revision); !errors.Is(err, tc.want) || ack != (ActionAck{}) {
			t.Fatal("invalid unlink identity or proof was accepted", tc.id, tc.revision, ack, err)
		}
	}
	if ack, err := library.Unlink(t.Context(), "invalid", "media-unauthenticated-unlink", "owned-a", 1); !errors.Is(err, ErrAuthentication) || ack != (ActionAck{}) {
		t.Fatal("unlink accepted invalid credentials", ack, err)
	}
	requireDomainState(t, fixture.store, before)
	for _, scope := range []string{"media", "media-current", "media_link", "link-current", "sibling", "sibling-current", "related", "related-current"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			kind, id := "media", "shared-a"
			switch scope {
			case "media_link", "link-current":
				kind, id = "media_link", "owned-a"
			case "sibling", "sibling-current":
				kind, id = "media_link", "sibling"
			case "related", "related-current":
				kind, id = "media", "shared-b"
			}
			execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
			if !strings.HasSuffix(scope, "-current") {
				execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
			}
			before := domainState(t, fixture.store)
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-missing-authority", "owned-a", 1); !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
				t.Fatal("unlink reconstructed missing Media/link authority", scope, ack, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaLinkUnlinkRollsBackRevisionOverflow(t *testing.T) {
	for _, scope := range []string{"media_link", "media", "item", "catalog", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			proof := int64(1)
			switch scope {
			case "media_link":
				setCatalogProjectionRevision(t, fixture.store, scope, "owned-a", maxSafeInteger)
				proof = maxSafeInteger
			case "media":
				setCatalogProjectionRevision(t, fixture.store, scope, "shared-a", maxSafeInteger)
			case "item":
				setCatalogProjectionRevision(t, fixture.store, scope, fixture.ownerID, maxSafeInteger)
			case "catalog":
				execSQL(t, fixture.store.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, fixture.store.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := domainState(t, fixture.store)
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-overflow", "owned-a", proof); !errors.Is(err, ErrRevisionOverflow) || ack != (ActionAck{}) {
				t.Fatal("overflow returned a partial unlink", scope, ack, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestMediaLinkUnlinkConcurrentRequestsRespectItsOriginalProof(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-key=%t", sameKey), func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			type result struct {
				value ActionAck
				err   error
			}
			results := make(chan result, 2)
			var group sync.WaitGroup
			for index := range 2 {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					key := fmt.Sprintf("media-unlink-concurrent-%d", index)
					if sameKey {
						key = "media-unlink-concurrent-same"
					}
					value, err := library.Unlink(t.Context(), fixture.token, key, "owned-a", 1)
					results <- result{value, err}
				}(index)
			}
			group.Wait()
			close(results)
			var successful []ActionAck
			missing := 0
			for result := range results {
				if result.err == nil {
					successful = append(successful, result.value)
				} else if errors.Is(result.err, ErrNotFound) && result.value == (ActionAck{}) {
					missing++
				} else {
					t.Fatal("unexpected concurrent unlink result", result.err)
				}
			}
			if sameKey && (len(successful) != 2 || missing != 0 || !reflect.DeepEqual(successful[0], successful[1])) || !sameKey && (len(successful) != 1 || missing != 1) {
				t.Fatal("concurrent original proof duplicated or failed to reconcile one unlink")
			}
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='delete_media_links_id'") != 1 ||
				scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media_link' AND entity_id='owned-a' AND operation='delete'") != 1 {
				t.Fatal("concurrent requests duplicated their immutable audit or tombstone")
			}
		})
	}
}

func TestMediaLinkUnlinkRollsBackLateTamperingAndRetriesOriginalKey(t *testing.T) {
	for _, failure := range []struct {
		name, setup string
		want        error
	}{
		{"ignored link delete", `CREATE TRIGGER fail_unlink BEFORE DELETE ON media_links WHEN OLD.id='owned-a' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"reinserted source", `CREATE TRIGGER fail_unlink AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN INSERT INTO media_links(id,media_id,item_id,item_unit_id,location_id,is_cover,sort_order,description) VALUES(OLD.id,OLD.media_id,OLD.item_id,OLD.item_unit_id,OLD.location_id,OLD.is_cover,OLD.sort_order,OLD.description); END`, ErrDomainState},
		{"ignored owner timestamp", `CREATE TRIGGER fail_unlink BEFORE UPDATE OF updated_at ON items BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"ignored link current deletion", `CREATE TRIGGER fail_unlink BEFORE DELETE ON current_projections WHEN OLD.entity_type='media_link' AND OLD.entity_id='owned-a' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"late link resurrection", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN INSERT INTO media_links(id,media_id,item_id,is_cover,sort_order,description) SELECT 'owned-a',media_id,item_id,is_cover,sort_order,description FROM media_links WHERE id='owned-b'; END`, ErrDomainState},
		{"late link current resurrection", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('media_link','owned-a',2,'{}','2026'); END`, ErrDomainState},
		{"late link counter", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='media_link' AND entity_id='owned-a'; END`, ErrDomainState},
		{"late media sibling", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE media_links SET description='tampered' WHERE id='sibling'; END`, ErrDomainState},
		{"late owner sibling", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE media_links SET description='tampered' WHERE id='owned-c'; END`, ErrDomainState},
		{"late sibling current", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media_link' AND entity_id='sibling'; END`, ErrDomainState},
		{"late selected Media source", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-a'; END`, ErrDomainState},
		{"late selected Media current", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-a'; END`, ErrDomainState},
		{"late related Media source", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE media SET storage_key='tampered' WHERE id='shared-b'; END`, ErrDomainState},
		{"late related Media current", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-b'; END`, ErrDomainState},
		{"late owner source", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE items SET name='tampered' WHERE id=(SELECT item_id FROM media_links WHERE id='owned-b'); END`, ErrDomainState},
		{"late owner current", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='item' AND entity_id=(SELECT item_id FROM media_links WHERE id='owned-b'); END`, ErrDomainState},
		{"late extra owner link", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN INSERT INTO media_links(id,media_id,item_id) SELECT 'late-extra','shared-b',item_id FROM media_links WHERE id='owned-b'; END`, ErrDomainState},
		{"late extra Media link", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN INSERT INTO media_links(id,media_id,item_id) SELECT 'late-extra','shared-a',item_id FROM media_links WHERE id='sibling'; END`, ErrDomainState},
		{"late tombstone", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN DELETE FROM sync_changes WHERE entity_type='media_link' AND entity_id='owned-a' AND entity_revision=2; END`, ErrDomainState},
		{"late Media outbox", `CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN DELETE FROM sync_changes WHERE entity_type='media' AND entity_id='shared-a' AND entity_revision=8; END`, ErrDomainState},
		{"ignored audit", `CREATE TRIGGER fail_unlink BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState},
		{"late authorization", `CREATE TRIGGER fail_unlink AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication},
		{"deferred commit", `CREATE TABLE media_unlink_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT; CREATE TRIGGER fail_unlink AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN INSERT INTO media_unlink_guard VALUES('missing'); END`, nil},
	} {
		t.Run(failure.name, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "item")
			execSQL(t, fixture.store.db, failure.setup)
			before := domainState(t, fixture.store)
			ack, err := library.Unlink(t.Context(), fixture.token, "media-failed-unlink", "owned-a", 1)
			if err == nil || ack != (ActionAck{}) || failure.want != nil && !errors.Is(err, failure.want) {
				t.Fatal("ignored or changed source escaped unlink transaction", failure.name, ack, err)
			}
			requireDomainState(t, fixture.store, before)
			if failure.name == "deferred commit" && scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_unlink_guard") != 0 {
				t.Fatal("failed deferred COMMIT retained a guard row")
			}
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_unlink")
			if ack, err = library.Unlink(t.Context(), fixture.token, "media-failed-unlink", "owned-a", 1); err != nil || !ack.OK || ack.Revision != 2 {
				t.Fatal("rolled-back failure retained an unusable original key", ack, err)
			}
		})
	}
}

func TestMediaLinkUnlinkValidatesMembershipBeyondOnePage(t *testing.T) {
	for _, scope := range []string{"media", "owner"} {
		t.Run(scope, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, "location")
			if scope == "media" {
				seedMediaLinkBatch(t, fixture.store, "item_id", "shared-a", fixture.siblingID, "media-unlink-sibling-", 205)
			} else {
				seedMediaLinkBatch(t, fixture.store, "location_id", "shared-b", fixture.ownerID, "media-unlink-sibling-", 205)
			}
			execSQL(t, fixture.store.db, `CREATE TRIGGER fail_unlink_large AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE media_links SET description='late change' WHERE id='media-unlink-sibling-204'; END`)
			before := domainState(t, fixture.store)
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-large-unlink", "owned-a", 1); !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
				t.Fatal("late membership change beyond one page escaped unlink validation", ack, err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_unlink_large")
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-large-unlink", "owned-a", 1); err != nil || !ack.OK || ack.Revision != 2 {
				t.Fatal("failed large membership check retained an unusable key", ack, err)
			}
		})
	}
}

func TestMediaLinkUnlinkRechecksAuthorizationAfterFinalMembershipValidation(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	base := fixture.auth.now()
	calls := 0
	fixture.auth.now = func() time.Time {
		calls++
		if calls > 3 {
			return base.Add(31 * 24 * time.Hour)
		}
		return base
	}
	before := domainState(t, fixture.store)
	if ack, err := library.Unlink(t.Context(), fixture.token, "media-expire-final-unlink", "owned-a", 1); !errors.Is(err, ErrAuthentication) || ack != (ActionAck{}) {
		t.Fatal("expired session succeeded after final unlink checks", ack, err)
	}
	requireDomainState(t, fixture.store, before)
	fixture.auth.now = func() time.Time { return base }
	if ack, err := library.Unlink(t.Context(), fixture.token, "media-expire-final-unlink", "owned-a", 1); err != nil || !ack.OK || ack.Revision != 2 {
		t.Fatal("rolled-back expiry retained the original claim", ack, err)
	}
}

func TestMediaLinkUnlinkKeepsActualEmptyOwnerAndMediaScopes(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	owner := createLocation(t, locations, token, "UNLINK-EMPTY", nil)
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('unlink-empty-image','private',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "unlink-empty-image")
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	link, err := library.Attach(t.Context(), token, "unlink-empty-setup", "unlink-empty-image", 1, MediaLinkCreate{LocationID: &owner.ID, OwnerRevision: owner.Revision})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := library.Unlink(t.Context(), token, "unlink-last-member", link.ID, link.Revision)
	if err != nil || !ack.OK || ack.Revision != 2 {
		t.Fatal("unlink could not remove the last actual member", ack, err)
	}
	byOwner, err := library.PageOwnerLinks(t.Context(), token, "location", owner.ID, "", 50)
	if err != nil || byOwner.Data == nil || len(byOwner.Data) != 0 || byOwner.NextCursor != nil {
		t.Fatal("actual empty owner scope was not preserved", byOwner, err)
	}
	byMedia, err := library.PageMediaLinks(t.Context(), token, "unlink-empty-image", "", 50)
	if err != nil || byMedia.Data == nil || len(byMedia.Data) != 0 || byMedia.NextCursor != nil {
		t.Fatal("actual empty Media scope was not preserved", byMedia, err)
	}
	if image, err := library.Get(t.Context(), token, "unlink-empty-image"); err != nil || image.Revision != 3 || image.Verification != "unverified" {
		t.Fatal("last unlink removed or verified the retained Media", image, err)
	}
}

func TestMediaLinkUnlinkExpiredClaimNeverReusesItsOriginalKey(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	if ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-expired-claim", "owned-a", 1); err != nil || !ack.OK {
		t.Fatal("original unlink failed", ack, err)
	}
	base := fixture.auth.now()
	fixture.auth.now = func() time.Time { return base.Add(31 * 24 * time.Hour) }
	_, fresh := login(t, fixture.auth)
	before := domainState(t, fixture.store)
	if ack, err := library.Unlink(t.Context(), fresh, "media-unlink-expired-claim", "owned-a", 1); !errors.Is(err, ErrIdempotencyExpired) || ack != (ActionAck{}) {
		t.Fatal("expired unlink claim was reused or returned a partial acknowledgement", ack, err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestMediaLinkUnlinkSupportsRetainedOwnerWithoutCurrentProjection(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.ownerID)
	ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-legacy-owner", "owned-a", 1)
	if err != nil || !ack.OK || ack.Revision != 2 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.ownerID) != 1 {
		t.Fatal("actual unlink could not publish its retained owner", ack, err)
	}
}

func TestMediaLinkUnlinkAdvancesLocationAncestorsWithoutTouchingDescendants(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	root := createLocation(t, locations, token, "UNLINK-ROOT", nil)
	child := createLocation(t, locations, token, "UNLINK-CHILD", &root.ID)
	grandchild := createLocation(t, locations, token, "UNLINK-GRANDCHILD", &child.ID)
	child = getLocation(t, locations, token, child.ID)
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('unlink-image','private',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "unlink-image")
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	link, err := library.Attach(t.Context(), token, "unlink-location-setup", "unlink-image", 1, MediaLinkCreate{LocationID: &child.ID, OwnerRevision: child.Revision})
	if err != nil {
		t.Fatal(err)
	}
	root = getLocation(t, locations, token, root.ID)
	child = getLocation(t, locations, token, child.ID)
	grandchild = getLocation(t, locations, token, grandchild.ID)
	changed := requireProjectionReplay(t, s, func() {
		if ack, err := library.Unlink(t.Context(), token, "unlink-location-tree", link.ID, link.Revision); err != nil || !ack.OK || ack.Revision != 2 {
			t.Fatal("reviewed location unlink failed", ack, err)
		}
	})
	if !changed[projectionKey{"location", root.ID}] || !changed[projectionKey{"location", child.ID}] || changed[projectionKey{"location", grandchild.ID}] ||
		getLocation(t, locations, token, root.ID).Revision != root.Revision+1 || getLocation(t, locations, token, child.ID).Revision != child.Revision+1 ||
		getLocation(t, locations, token, grandchild.ID).Revision != grandchild.Revision {
		t.Fatal("location unlink changed the wrong ancestor or descendant scope")
	}
}

func TestMediaLinkUnlinkRechecksUnitAndLocationProjectionAuthority(t *testing.T) {
	for _, tc := range []struct{ ownerType, kind, id string }{
		{"unit", "unit", "unit"}, {"unit", "location", "root"}, {"location", "location", ""},
	} {
		t.Run(tc.ownerType+"/"+tc.kind+"/"+tc.id, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, tc.ownerType)
			id := tc.id
			if id == "" {
				id = fixture.ownerID
			}
			execSQL(t, fixture.store.db, `CREATE TRIGGER fail_unlink_owner AFTER INSERT ON mutation_events WHEN NEW.operation='delete_media_links_id' BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='`+tc.kind+`' AND entity_id='`+id+`'; END`)
			before := domainState(t, fixture.store)
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-owner-projection", "owned-a", 1); !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
				t.Fatal("unlink exposed a changed Unit or ancestor projection", ack, err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_unlink_owner")
			if ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-owner-projection", "owned-a", 1); err != nil || !ack.OK {
				t.Fatal("failed owner projection check retained the original key", ack, err)
			}
		})
	}
}

func TestMediaLinkUnlinkRetainsBodyOnlyLegacyIdentity(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	id := "legacy/\x00" + strings.Repeat("界", 120)
	execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id,description) VALUES(?,'shared-a',?,'retained')`, id, fixture.ownerID)
	seedMediaLinkProjection(t, fixture.store, id)
	ack, err := library.Unlink(t.Context(), fixture.token, "media-unlink-legacy-identity", id, 1)
	if err != nil || !ack.OK || ack.Revision != 2 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links WHERE id=?", id) != 0 ||
		scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media_link' AND entity_id=? AND operation='delete' AND entity_revision=2 AND projection_json IS NULL", id) != 1 {
		t.Fatal("unlink changed or lost a retained slash/NUL/Unicode identity", ack, err)
	}
	if replay, err := library.Unlink(t.Context(), fixture.token, "media-unlink-legacy-identity", id, 1); err != nil || !reflect.DeepEqual(ack, replay) {
		t.Fatal("legacy identity did not preserve original-key unlink replay", err)
	}
}
