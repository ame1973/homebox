package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These fixtures model real source/counters; they do not claim image-byte
// verification or provide a media mutation/HTTP service.
func seedMediaProjection(t *testing.T, s *Store, id string) {
	t.Helper()
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('media',?,1)", id)
	value, err := loadMedia(t.Context(), s.db, id)
	if err != nil {
		t.Fatal("invalid media fixture source", err)
	}
	payload, err := json.Marshal(value.mediaData)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('media',?,1,?,'2026')`, id, string(payload))
}

func seedMediaLinkProjection(t *testing.T, s *Store, id string) {
	t.Helper()
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('media_link',?,1)", id)
	value, err := loadMediaLink(t.Context(), s.db, id)
	if err != nil {
		t.Fatal("invalid media-link fixture source", err)
	}
	payload, err := json.Marshal(value.mediaLinkData)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('media_link',?,1,?,'2026')`, id, string(payload))
}

type mediaCascadeFixture struct {
	store     *Store
	auth      *Auth
	token     string
	ownerID   string
	revision  int64
	delete    func(context.Context, string, string, int64) (ActionAck, error)
	siblingID string
}

func newMediaCascadeFixture(t *testing.T, ownerType string) mediaCascadeFixture {
	t.Helper()
	holding, inventory := inventoryFixture(t)
	fixture := mediaCascadeFixture{store: holding.store, auth: holding.auth, token: holding.token, siblingID: holding.quantity.ID}
	var column string
	switch ownerType {
	case "item":
		item := createCatalogItem(t, holding.catalog, holding.token, ItemCreate{Name: "Shared media owner"})
		fixture.ownerID, fixture.revision, column = item.ID, item.Revision, "item_id"
		fixture.delete = func(ctx context.Context, token, key string, revision int64) (ActionAck, error) {
			return holding.catalog.Delete(ctx, token, key, item.ID, revision)
		}
	case "unit":
		fixture.ownerID, fixture.revision, column = holding.unit.ID, holding.unit.Revision, "item_unit_id"
		fixture.delete = func(ctx context.Context, token, key string, revision int64) (ActionAck, error) {
			return inventory.DeleteUnit(ctx, token, key, holding.unit.ID, revision)
		}
	case "location":
		locations, err := holding.store.NewLocations(holding.auth)
		if err != nil {
			t.Fatal(err)
		}
		location := createLocation(t, locations, holding.token, "MEDIA-LEAF", nil)
		fixture.ownerID, fixture.revision, column = location.ID, location.Revision, "location_id"
		fixture.delete = func(ctx context.Context, token, key string, revision int64) (ActionAck, error) {
			return locations.Delete(ctx, token, key, location.ID, revision)
		}
	default:
		t.Fatal("unknown media owner fixture")
	}
	for _, id := range []string{"shared-a", "shared-b"} {
		execSQL(t, fixture.store.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,'image/png',10,20,30,'2026')`, id, "opaque-"+id, strings.Repeat("a", 64))
		seedMediaProjection(t, fixture.store, id)
	}
	for _, link := range []struct{ id, media string }{{"owned-a", "shared-a"}, {"owned-b", "shared-a"}, {"owned-c", "shared-b"}} {
		execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,"+column+",is_cover,sort_order,description) VALUES(?,?,?,1,-3,?)", link.id, link.media, fixture.ownerID, "\x00Exact é\n")
		seedMediaLinkProjection(t, fixture.store, link.id)
	}
	execSQL(t, fixture.store.db, `INSERT INTO media_links(id,media_id,item_id,is_cover,sort_order,description) VALUES('sibling','shared-a',?,0,7,'Other owner')`, fixture.siblingID)
	seedMediaLinkProjection(t, fixture.store, "sibling")
	execSQL(t, fixture.store.db, `INSERT INTO ocr_results(id,media_id,raw_text,engine,engine_version,created_at) VALUES('raw-evidence','shared-a','NeverPublishUnreviewedMediaOCR','fixture','1','2026')`)
	setCatalogProjectionRevision(t, fixture.store, "media", "shared-a", 7)
	setCatalogProjectionRevision(t, fixture.store, "media", "shared-b", 13)
	setCatalogProjectionRevision(t, fixture.store, "media_link", "sibling", 5)
	return fixture
}

func TestMediaProjectionReadsActualSourceAndRejectsUnsupportedVerifiedClaims(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), `UPDATE media SET mime_type='image/heic',perceptual_hash=? WHERE id='shared-a'`, "\x00"+strings.Repeat("界", 15)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE media_links SET description=? WHERE id='sibling'`, "\x00Exact source é\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE current_projections SET payload_json='{"not_source":"poison"}' WHERE entity_type IN('media','media_link')`); err != nil {
		t.Fatal(err)
	}
	mutation := mutationTx{ctx: t.Context(), tx: tx}
	mediaPayload, err := mutation.projectionPayload(projectionKey{"media", "shared-a"})
	if err != nil {
		t.Fatal(err)
	}
	var image mediaData
	var fields map[string]any
	if json.Unmarshal(mediaPayload, &image) != nil || json.Unmarshal(mediaPayload, &fields) != nil || image.MIMEType != "image/heic" ||
		image.PerceptualHash == nil || *image.PerceptualHash != "\x00"+strings.Repeat("界", 15) || image.ThumbURL != nil || image.ThumbSHA256 != nil ||
		len(fields) != 9 || fields["verification"] != nil || fields["storage_key"] != nil || fields["not_source"] != nil {
		t.Fatal("media projection copied a cache, exposed a private field or invented a thumbnail")
	}
	linkPayload, err := mutation.projectionPayload(projectionKey{"media_link", "sibling"})
	var link mediaLinkData
	if err != nil || json.Unmarshal(linkPayload, &link) != nil || link.MediaID != "shared-a" || link.OwnerType != "item" ||
		link.OwnerID != fixture.siblingID || link.Description != "\x00Exact source é\n" || link.SortOrder != 7 {
		t.Fatal("link projection copied a cache or changed exact source/owner fields", err)
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE media SET verification='verified' WHERE id='shared-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = mutation.projectionPayload(projectionKey{"media", "shared-a"}); !errors.Is(err, ErrDomainState) {
		t.Fatal("unsupported historical MIME was presented as a verified image", err)
	}
	if _, err = tx.ExecContext(t.Context(), `UPDATE media SET mime_type='image/png' WHERE id='shared-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = mutation.projectionPayload(projectionKey{"media", "shared-a"}); err != nil {
		t.Fatal("supported verified representation was rejected; this does not verify actual files", err)
	}
	if _, err = tx.ExecContext(t.Context(), `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'; DELETE FROM projection_revisions WHERE entity_type='media_link' AND entity_id='sibling'`); err != nil {
		t.Fatal(err)
	}
	if _, err = mutation.projectionPayload(projectionKey{"media_link", "sibling"}); !errors.Is(err, ErrDomainState) {
		t.Fatal("a missing real link counter was synthesized from source/cache", err)
	}
}

func TestMediaOwnerDeletionRollsBackEveryMembershipFailure(t *testing.T) {
	failures := []struct {
		name, trigger string
		want          error
		retry         bool
	}{
		{"ignored link delete", `CREATE TRIGGER fail_media_owner BEFORE DELETE ON media_links WHEN OLD.id='owned-a' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"reinserted owned link", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN INSERT INTO media_links(id,media_id,item_id,item_unit_id,location_id,is_cover,sort_order,description) VALUES(OLD.id,OLD.media_id,OLD.item_id,OLD.item_unit_id,OLD.location_id,OLD.is_cover,OLD.sort_order,OLD.description); END`, ErrDomainState, true},
		{"rewritten sibling", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN UPDATE media_links SET description='changed' WHERE id='sibling'; END`, ErrDomainState, true},
		{"removed sibling", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN DELETE FROM media_links WHERE id='sibling'; END`, ErrDomainState, true},
		{"unreviewed sibling", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN INSERT INTO media_links(id,media_id,item_id) SELECT 'unexpected','shared-a',item_id FROM media_links WHERE id='sibling'; END`, ErrDomainState, true},
		{"rewritten media", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN UPDATE media SET sha256=printf('%064d',1) WHERE id='shared-a'; END`, ErrDomainState, true},
		{"rewritten media counter", `CREATE TRIGGER fail_media_owner AFTER DELETE ON media_links WHEN OLD.id='owned-a' BEGIN UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='media' AND entity_id='shared-a'; END`, ErrDomainState, true},
		{"late sibling after counter", `CREATE TRIGGER fail_media_owner AFTER UPDATE ON projection_revisions WHEN NEW.entity_type='media' AND NEW.entity_id='shared-a' BEGIN UPDATE media_links SET description='late change' WHERE id='sibling'; END`, ErrDomainState, true},
		{"late sibling after audit", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN UPDATE media_links SET description='late change' WHERE id='sibling'; END`, ErrDomainState, true},
		{"late private key after session", `CREATE TRIGGER fail_media_owner AFTER UPDATE ON sessions BEGIN UPDATE media SET storage_key='changed-private-key' WHERE id='shared-a'; END`, ErrDomainState, true},
		{"late current rewrite", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media' AND entity_id='shared-a'; END`, ErrDomainState, true},
		{"late sibling current rewrite", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN UPDATE current_projections SET payload_json='{}' WHERE entity_type='media_link' AND entity_id='sibling'; END`, ErrDomainState, true},
		{"late sibling current removal", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'; END`, ErrDomainState, true},
		{"late outbox removal", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN DELETE FROM sync_changes WHERE entity_type='media' AND entity_id='shared-a' AND entity_revision=8; END`, ErrDomainState, true},
		{"ignored media current", `CREATE TRIGGER fail_media_owner BEFORE UPDATE ON current_projections WHEN NEW.entity_type='media' AND NEW.entity_id='shared-a' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"ignored media outbox", `CREATE TRIGGER fail_media_owner BEFORE INSERT ON sync_changes WHEN NEW.entity_type='media' AND NEW.entity_id='shared-a' BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"ignored link current deletion", `CREATE TRIGGER fail_media_owner BEFORE DELETE ON current_projections WHEN OLD.entity_type='media_link' AND OLD.entity_id='owned-a' BEGIN SELECT RAISE(IGNORE); END`, nil, true},
		{"ignored audit", `CREATE TRIGGER fail_media_owner BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END`, ErrDomainState, true},
		{"late session revocation", `CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z'; END`, ErrAuthentication, true},
		{"revocation during session touch", `CREATE TRIGGER fail_media_owner AFTER UPDATE OF last_seen_at ON sessions BEGIN UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, ErrAuthentication, true},
		{"deferred commit", `CREATE TABLE media_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT; CREATE TRIGGER fail_media_owner AFTER INSERT ON mutation_events BEGIN INSERT INTO media_commit_guard VALUES('missing'); END`, nil, true},
		{"media counter overflow", "", ErrRevisionOverflow, false},
		{"missing media counter", "", ErrDomainState, false},
		{"missing media current", "", ErrDomainState, false},
		{"missing sibling counter", "", ErrDomainState, false},
		{"missing sibling current", "", ErrDomainState, false},
		{"missing media source", "", ErrDomainState, false},
		{"invalid sibling description", "", ErrDomainState, false},
	}
	for _, ownerType := range []string{"item", "unit", "location"} {
		for _, failure := range failures {
			t.Run(ownerType+"/"+failure.name, func(t *testing.T) {
				fixture := newMediaCascadeFixture(t, ownerType)
				s := fixture.store
				switch failure.name {
				case "media counter overflow":
					setCatalogProjectionRevision(t, s, "media", "shared-a", maxSafeInteger)
				case "missing media counter":
					execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'; DELETE FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'`)
				case "missing media current":
					execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'`)
				case "missing sibling counter":
					execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'; DELETE FROM projection_revisions WHERE entity_type='media_link' AND entity_id='sibling'`)
				case "missing sibling current":
					execSQL(t, s.db, `DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='sibling'`)
				case "missing media source":
					execSQL(t, s.db, `PRAGMA foreign_keys=OFF; DELETE FROM media WHERE id='shared-a'; PRAGMA foreign_keys=ON`)
				case "invalid sibling description":
					execSQL(t, s.db, `UPDATE media_links SET description=char(0)||printf('%010000d',0) WHERE id='sibling'`)
				default:
					execSQL(t, s.db, failure.trigger)
				}
				before := domainState(t, s)
				ack, err := fixture.delete(t.Context(), fixture.token, "failed-media-owner", fixture.revision)
				if err == nil || ack.OK || failure.want != nil && !errors.Is(err, failure.want) {
					t.Fatal("invalid media membership released a partial deletion", failure.name, err)
				}
				requireDomainState(t, s, before)
				if failure.name == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM media_commit_guard") != 0 {
					t.Fatal("failed deferred commit retained a guard row")
				}
				if failure.retry {
					execSQL(t, s.db, "DROP TRIGGER fail_media_owner")
					ack, err = fixture.delete(t.Context(), fixture.token, "failed-media-owner", fixture.revision)
					if err != nil || !ack.OK {
						t.Fatal("rolled-back deletion retained its key or poisoned the connection", err)
					}
				}
			})
		}
	}
}

func TestMediaOwnerDeletionRejectsExpiryAfterFinalMembershipValidation(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
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
	if ack, err := fixture.delete(t.Context(), fixture.token, "expire-final-media-check", fixture.revision); !errors.Is(err, ErrAuthentication) || ack.OK {
		t.Fatal("session expired after media membership validation but deletion succeeded", err)
	}
	requireDomainState(t, fixture.store, before)
	fixture.auth.now = func() time.Time { return base }
	if ack, err := fixture.delete(t.Context(), fixture.token, "expire-final-media-check", fixture.revision); err != nil || !ack.OK {
		t.Fatal("rolled-back expiry retained the original claim", err)
	}
}

func TestMediaEmptyLocationDeletionRejectsLinksIntroducedAfterItsInitialRead(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	owner := createLocation(t, locations, token, "EMPTY-MEDIA-LEAF", nil)
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','opaque',?,'image/png',1,1,1,'2026')`, strings.Repeat("a", 64))
	seedMediaProjection(t, s, "photo")
	execSQL(t, s.db, `CREATE TRIGGER fail_media_owner AFTER UPDATE ON locations WHEN NEW.deleted_at IS NOT NULL BEGIN
 INSERT INTO media_links(id,media_id,location_id) VALUES('unexpected','photo',NEW.id); END`)
	before := domainState(t, s)
	if ack, err := locations.Delete(t.Context(), token, "delete-empty-media-leaf", owner.ID, owner.Revision); !errors.Is(err, ErrDomainState) || ack.OK {
		t.Fatal("a link added after the initial empty membership read escaped deletion review", err)
	}
	requireDomainState(t, s, before)
	execSQL(t, s.db, `DROP TRIGGER fail_media_owner`)
	if ack, err := locations.Delete(t.Context(), token, "delete-empty-media-leaf", owner.ID, owner.Revision); err != nil || !ack.OK {
		t.Fatal("rolled-back empty-owner deletion could not retry its original key", err)
	}
}

func TestMediaOwnerDeletionChecksAllSiblingLinksBeyondOnePage(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "location")
	s := fixture.store
	for index := range 205 {
		id := fmt.Sprintf("retained-sibling-%03d", index)
		execSQL(t, s.db, `INSERT INTO media_links(id,media_id,item_id,sort_order,description) VALUES(?,'shared-a',?,?,'Retained sibling')`, id, fixture.siblingID, index-102)
		seedMediaLinkProjection(t, s, id)
	}
	changed := requireProjectionReplay(t, s, func() {
		if ack, err := fixture.delete(t.Context(), fixture.token, "delete-large-media-owner", fixture.revision); err != nil || !ack.OK {
			t.Fatal("complete shared membership rejected a large retained owner collection", err)
		}
	})
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE media_id='shared-a' AND item_id=?", fixture.siblingID) != 206 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id='retained-sibling-204'") != 1 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 8 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media' AND entity_id='shared-a' AND entity_revision=8") != 1 {
		t.Fatal("deletion truncated a shared collection or advanced its membership more than once")
	}
	for key := range changed {
		if key.kind == "media_link" && strings.HasPrefix(key.id, "retained-sibling-") {
			t.Fatal("deletion republished another owner's unchanged link", key.id)
		}
	}
}

func TestMediaOwnerDeletionCoalescesMembershipPreservesSiblingsAndReplays(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture := newMediaCascadeFixture(t, ownerType)
			s := fixture.store
			beforeMedia := domainState(t, s)["media"]
			beforeOCR := domainState(t, s)["ocr_results"]
			beforeSibling := currentProjectionState(t, s)[projectionKey{"media_link", "sibling"}]
			beforePayload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'")
			execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'2026' FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'`)
			var ack ActionAck
			changed := requireProjectionReplay(t, s, func() {
				var err error
				ack, err = fixture.delete(t.Context(), fixture.token, "delete-media-owner", fixture.revision)
				if err != nil || !ack.OK || ack.Revision != fixture.revision+1 {
					t.Fatal("reviewed owner deletion failed", err)
				}
			})
			for _, id := range []string{"owned-a", "owned-b", "owned-c"} {
				if !changed[projectionKey{"media_link", id}] || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id=?", id) != 0 ||
					scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media_link' AND entity_id=? AND operation='delete' AND entity_revision=2 AND projection_json IS NULL", id) != 1 {
					t.Fatal("deletion lost an independent owned link tombstone", id)
				}
			}
			for _, media := range []struct {
				id       string
				revision int64
			}{{"shared-a", 8}, {"shared-b", 14}} {
				if !changed[projectionKey{"media", media.id}] || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id=?", media.id) != media.revision ||
					scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media' AND entity_id=? AND operation='upsert' AND entity_revision=?", media.id, media.revision) != 1 {
					t.Fatal("shared Media membership did not advance exactly once", media.id)
				}
			}
			if domainState(t, s)["media"] != beforeMedia || domainState(t, s)["ocr_results"] != beforeOCR ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE '%NeverPublishUnreviewedMediaOCR%'") != 0 ||
				currentProjectionState(t, s)[projectionKey{"media_link", "sibling"}] != beforeSibling ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id='sibling' AND item_id=?", fixture.siblingID) != 1 ||
				scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='media' AND entity_id='shared-a' AND entity_revision=7") != beforePayload ||
				scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id='shared-a'") != beforePayload {
				t.Fatal("deletion modified shared bytes, a sibling link or a previously frozen projection")
			}
			_, fresh := login(t, fixture.auth)
			beforeReplay := domainState(t, s)
			replayed, err := fixture.delete(t.Context(), fresh, "delete-media-owner", fixture.revision)
			if err != nil || !reflect.DeepEqual(replayed, ack) {
				t.Fatal("fresh login could not reconcile the original deletion after source removal", err)
			}
			if conflict, err := fixture.delete(t.Context(), fresh, "delete-media-owner", fixture.revision+1); !errors.Is(err, ErrIdempotencyConflict) || conflict.OK {
				t.Fatal("the original key accepted a changed owner proof", err)
			}
			requireDomainState(t, s, beforeReplay)
		})
	}
}
