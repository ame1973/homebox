package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func mediaRootEighteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := disposalRootSeventeenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[17]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

// Matrix cases share only a verified, empty schema-18 snapshot. Each case
// creates its own identities, owner source and counters after cloning it.
func emptyMediaRootEighteenSnapshot(t *testing.T) string {
	t.Helper()
	directory := privateTempDir(t)
	db := rawDatabase(t, filepath.Join(directory, "empty.sqlite"))
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range all[:18] {
		execSQL(t, db, migration.SQL)
	}
	execSQL(t, db, checksumDDL)
	for _, migration := range all[:18] {
		execSQL(t, db, "INSERT INTO schema_migrations(version,name,applied_at) SELECT ?,?,'2026' WHERE NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=?)", migration.Version, migration.Filename, migration.Version)
		execSQL(t, db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	}
	version, certified, err := history(t.Context(), db, all)
	if err != nil || version != 18 || !certified {
		t.Fatal("empty media matrix schema is not certified version 18", err)
	}
	for _, table := range []string{"admin_accounts", "sessions", "auth_bootstrap", "auth_reauth_grants", "auth_server_secret", "security_audit", "api_idempotency", "mutation_events", "items", "locations", "media", "media_links", "asset_profiles", "projection_revisions", "current_projections", "sync_changes"} {
		if scalar[int](t, db, "SELECT COUNT(*) FROM "+table) != 0 {
			t.Fatal("media matrix snapshot contains private or domain state", table)
		}
	}
	snapshot, _, err := (&Store{db: db}).snapshot(t.Context(), directory)
	if err != nil {
		t.Fatal("empty media matrix snapshot failed", err)
	}
	if err = VerifySnapshot(t.Context(), snapshot); err != nil {
		t.Fatal("empty media matrix snapshot was not verified", err)
	}
	t.Cleanup(func() {
		if err := VerifySnapshot(context.Background(), snapshot); err != nil {
			t.Error("media matrix fixture mutated its template", err)
		}
	})
	return snapshot
}

func mediaMatrixEighteenFixture(t *testing.T, snapshot string) (string, *Store) {
	t.Helper()
	path := cloneFixtureSnapshot(t, snapshot)
	db := rawDatabase(t, path)
	execSQL(t, db, `UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))),dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1`)
	execSQL(t, db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item-1','Owner','quantity','2026','2026')`)
	execSQL(t, db, `INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('loc-1','OWNER','Owner','2026','2026')`)
	execSQL(t, db, `INSERT INTO projection_revisions VALUES('item','item-1',1),('location','loc-1',1)`)
	return path, &Store{db: db}
}

func TestMediaMatrixFixturesKeepIndependentIdentitiesAndSource(t *testing.T) {
	snapshot := emptyMediaRootEighteenSnapshot(t)
	firstPath, first := mediaMatrixEighteenFixture(t, snapshot)
	secondPath, second := mediaMatrixEighteenFixture(t, snapshot)
	if firstPath == secondPath || scalar[string](t, first.db, "SELECT server_instance_id FROM sync_state") == scalar[string](t, second.db, "SELECT server_instance_id FROM sync_state") ||
		scalar[string](t, first.db, "SELECT dataset_epoch FROM sync_state") == scalar[string](t, second.db, "SELECT dataset_epoch FROM sync_state") {
		t.Fatal("media migration fixtures share database or sync identities")
	}
	for _, fixture := range []*Store{first, second} {
		if scalar[int](t, fixture.db, "SELECT COUNT(*) FROM auth_server_secret") != 0 || scalar[int](t, fixture.db, "SELECT COUNT(*) FROM admin_accounts") != 0 ||
			scalar[int](t, fixture.db, "SELECT COUNT(*) FROM mutation_events") != 0 || scalar[int](t, fixture.db, "SELECT COUNT(*) FROM media") != 0 {
			t.Fatal("media migration fixture copied private state or media source")
		}
	}
	execSQL(t, first.db, "UPDATE items SET name='First owner' WHERE id='item-1'")
	if scalar[string](t, second.db, "SELECT name FROM items WHERE id='item-1'") != "Owner" {
		t.Fatal("media migration fixtures share mutable owner source")
	}
}

func TestMediaUpgradePreservesLegacyClaimsSharedLinksCountersAndFrozenBytes(t *testing.T) {
	path, old := mediaRootEighteenFixture(t)
	mediaID := strings.Repeat("界", 124) + "<&\" "
	controlID := "\x00" + strings.Repeat("😀", 127)
	phash := "\x00" + strings.Repeat("界", 15)
	execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,perceptual_hash,created_at)
 VALUES(?,'opaque/legacy',?,'image/heic',?,?,17,?,'unknown-created')`, mediaID, strings.Repeat("a", 64), maxSafeInteger, 12, phash)
	execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at)
 VALUES(?,'opaque/controls',?,?,1,1,1,'unknown-created')`, controlID, strings.Repeat("b", 64), "\x00legacy-mime")
	execSQL(t, old.db, `INSERT INTO media_links(id,media_id,item_id,is_cover,sort_order) VALUES('item-link',?,'item-1',1,-9),('second-cover',?,'item-1',1,3),('negative-link',?,'item-1',0,?)`, mediaID, mediaID, mediaID, -maxSafeInteger)
	execSQL(t, old.db, `INSERT INTO media_links(id,media_id,location_id,is_cover,sort_order) VALUES('location-link',?,'loc-1',0,0)`, mediaID)
	execSQL(t, old.db, `INSERT INTO media_links(id,media_id,item_unit_id,is_cover,sort_order) VALUES('unit-link',?,'asset-unit',0,2)`, mediaID)
	for _, counter := range []struct {
		kind, id string
		revision int64
	}{{"media", mediaID, 7}, {"media_link", "item-link", 11}, {"media", "gone-media", 13}, {"media_link", "gone-link", 17}} {
		execSQL(t, old.db, "INSERT INTO projection_revisions VALUES(?,?,?)", counter.kind, counter.id, counter.revision)
	}
	retained := ` { "legacy_frozen_payload" : true } `
	execSQL(t, old.db, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('media_link','item-link',11,?,'retained-time')", retained)
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('media_link','item-link','upsert',11,?,'retained-time')", retained)
	before := domainState(t, old)
	projections := currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()

	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 18 || s.Report().To < 19 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("media upgrade skipped verified recovery, changed epoch or did not apply migration 19")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-upgrade database snapshot is not verified", err)
	}
	for key, value := range projections {
		if currentProjectionState(t, s)[key] != value {
			t.Fatal("media upgrade rewrote previously materialized bytes", key)
		}
	}
	after := domainState(t, s)
	for _, table := range []string{"sync_changes", "mutation_events", "data_revision", "ocr_results", "search_documents", "unit_search_documents"} {
		if after[table] != before[table] {
			t.Fatal("media upgrade invented a receipt, event, outbox row or search publication", table)
		}
	}
	if scalar[string](t, s.db, "SELECT verification FROM media WHERE id=?", mediaID) != "unverified" ||
		scalar[string](t, s.db, "SELECT storage_key FROM media WHERE id=?", mediaID) != "opaque/legacy" ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE item_id='item-1' AND is_cover=1") != 2 {
		t.Fatal("legacy source was marked verified, rewritten or silently deconflicted")
	}
	var image struct {
		ID             string  `json:"id"`
		SHA256         string  `json:"sha256"`
		MIME           string  `json:"mime_type"`
		Width          int64   `json:"width"`
		Height         int64   `json:"height"`
		Bytes          int64   `json:"bytes"`
		PerceptualHash *string `json:"perceptual_hash"`
		ThumbURL       *string `json:"thumb_url"`
		ThumbSHA256    *string `json:"thumb_sha256"`
	}
	encoded := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id=?", mediaID)
	if err = json.Unmarshal([]byte(encoded), &image); err != nil || image.ID != mediaID || image.SHA256 != strings.Repeat("a", 64) ||
		image.MIME != "image/heic" || image.Width != maxSafeInteger || image.Height != 12 || image.Bytes != 17 ||
		image.PerceptualHash == nil || *image.PerceptualHash != phash || image.ThumbURL != nil || image.ThumbSHA256 != nil {
		t.Fatal("media backfill changed claims or invented a verified thumbnail", err)
	}
	var decoded map[string]any
	if err = json.Unmarshal([]byte(encoded), &decoded); err != nil || len(decoded) != 9 || decoded["verification"] != nil || decoded["storage_key"] != nil {
		t.Fatal("version-one offline Media gained private metadata or verification fields", err)
	}
	var controlPayload mediaData
	if err = json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id=?", controlID)), &controlPayload); err != nil ||
		controlPayload.ID != controlID || controlPayload.MIMEType != "\x00legacy-mime" || controlPayload.ThumbURL != nil || controlPayload.ThumbSHA256 != nil ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id=?", controlID) != 1 {
		t.Fatal("media backfill truncated valid native controls or reset an independent new counter", err)
	}
	for _, example := range []struct {
		id, kind, owner string
		cover           bool
		sort            int64
	}{{"second-cover", "item", "item-1", true, 3}, {"negative-link", "item", "item-1", false, -maxSafeInteger}, {"location-link", "location", "loc-1", false, 0}, {"unit-link", "unit", "asset-unit", false, 2}} {
		var value struct {
			ID          string `json:"id"`
			MediaID     string `json:"media_id"`
			OwnerType   string `json:"owner_type"`
			OwnerID     string `json:"owner_id"`
			IsCover     bool   `json:"is_cover"`
			SortOrder   int64  `json:"sort_order"`
			Description string `json:"description"`
		}
		payload := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='media_link' AND entity_id=?", example.id)
		if err = json.Unmarshal([]byte(payload), &value); err != nil || value.ID != example.id || value.MediaID != mediaID ||
			value.OwnerType != example.kind || value.OwnerID != example.owner || value.IsCover != example.cover || value.SortOrder != example.sort || value.Description != "" {
			t.Fatal("link backfill lost exact independent owner/source values", example.id, err)
		}
	}
	for _, key := range []projectionKey{{"media", mediaID}, {"media", controlID}, {"media_link", "negative-link"}} {
		if !validPurchaseInstant(scalar[string](t, s.db, "SELECT updated_at FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)) {
			t.Fatal("media materialization guessed an unknown legacy creation or modification instant", key)
		}
	}
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id=?", mediaID) != 7 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id='item-link'") != 11 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='gone-media'") != 13 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id='gone-link'") != 17 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_id IN('gone-media','gone-link')") != 0 ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE entity_type='media_link' AND entity_id='item-link'") != retained {
		t.Fatal("backfill reset counters, resurrected absent identities or rewrote frozen history")
	}
	if !reflect.DeepEqual(projections[projectionKey{"media_link", "item-link"}], currentProjectionState(t, s)[projectionKey{"media_link", "item-link"}]) {
		t.Fatal("already-materialized link bytes were changed")
	}
	for _, lookup := range []struct{ column, index string }{
		{"media_id", "idx_media_links_media_id"}, {"item_id", "idx_media_links_item_id"},
		{"item_unit_id", "idx_media_links_unit_id"}, {"location_id", "idx_media_links_location_id"},
	} {
		var a, b, c int
		var detail string
		err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM media_links WHERE "+lookup.column+"=? AND id>? ORDER BY id LIMIT 51", "owner", "").Scan(&a, &b, &c, &detail)
		if err != nil || !strings.Contains(detail, lookup.index) || strings.Contains(detail, "TEMP") {
			t.Fatal("media membership lacks its stable owner/ID lookup", lookup.column, detail, err)
		}
	}
}

func TestMediaFreshSchemaDefaultsLegacyClaimsToUnverified(t *testing.T) {
	s := openFreshTestStore(t)
	execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','opaque',?,'image/heic',1,1,1,'legacy')`, strings.Repeat("a", 64))
	execSQL(t, s.db, `INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('owner','OWNER','Owner','2026','2026')`)
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item-owner','Owner','quantity','2026','2026')`)
	execSQL(t, s.db, `INSERT INTO media_links(id,media_id,location_id) VALUES('link','photo','owner')`)
	if s.Report().From != 0 || s.Report().To < 19 || scalar[string](t, s.db, "SELECT verification FROM media WHERE id='photo'") != "unverified" ||
		scalar[string](t, s.db, "SELECT description FROM media_links WHERE id='link'") != "" {
		t.Fatal("fresh schema invented image verification or a legacy description")
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE media SET verification='unsafe' WHERE id='photo'`); err == nil {
		t.Fatal("native verification-state constraint accepted an unknown status")
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO media_links(id,media_id) VALUES('ownerless','photo')`); err == nil {
		t.Fatal("native link constraint accepted an ownerless row")
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO media_links(id,media_id,item_id,location_id) VALUES('two-owners','photo','item-owner','owner')`); err == nil {
		t.Fatal("native link constraint accepted multiple owners")
	}
}

func TestMediaBackfillValidationWithholdsFailedQueriesAndCancellation(t *testing.T) {
	for _, stage := range []string{"counters", "media", "links", "current", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			s := openTestStore(t)
			if stage == "current" {
				execSQL(t, s.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','opaque',?,'image/jpeg',1,1,1,'2026')`, strings.Repeat("a", 64))
				seedMediaProjection(t, s, "photo")
			}
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if statement := map[string]string{"counters": "DROP TABLE projection_revisions", "media": "DROP TABLE media", "links": "DROP TABLE media_links", "current": "DROP TABLE current_projections"}[stage]; statement != "" {
				if _, err = tx.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if stage == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			err = validateMediaBackfill(ctx, tx)
			if err == nil || stage == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("failed or canceled media validation was treated as successful", err)
			}
		})
	}
}

func TestMediaUpgradeRejectsIgnoredBackfillAndLedgerWrites(t *testing.T) {
	snapshot := emptyMediaRootEighteenSnapshot(t)
	for _, failure := range []struct{ name, trigger string }{
		{"media counter", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON projection_revisions WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`},
		{"link counter", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON projection_revisions WHEN NEW.entity_type='media_link' BEGIN SELECT RAISE(IGNORE); END`},
		{"media current", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON current_projections WHEN NEW.entity_type='media' BEGIN SELECT RAISE(IGNORE); END`},
		{"link current", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON current_projections WHEN NEW.entity_type='media_link' BEGIN SELECT RAISE(IGNORE); END`},
		{"version row", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON schema_migrations WHEN NEW.version=19 BEGIN SELECT RAISE(IGNORE); END`},
		{"checksum row", `CREATE TRIGGER ignore_media_backfill BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=19 BEGIN SELECT RAISE(IGNORE); END`},
		{"late materialization removal", `CREATE TRIGGER ignore_media_backfill AFTER INSERT ON schema_migration_checksums WHEN NEW.version=19 BEGIN DELETE FROM current_projections WHERE entity_type='media_link' AND entity_id='link'; END`},
		{"late invented verification", `CREATE TRIGGER ignore_media_backfill AFTER INSERT ON schema_migration_checksums WHEN NEW.version=19 BEGIN UPDATE media SET verification='verified' WHERE id='photo'; END`},
		{"late checksum removal", `CREATE TRIGGER ignore_media_backfill AFTER INSERT ON schema_migration_checksums WHEN NEW.version=19 BEGIN DELETE FROM schema_migration_checksums WHERE version=18; END`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			path, old := mediaMatrixEighteenFixture(t, snapshot)
			execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','opaque',?,'image/jpeg',1,1,1,'2026')`, strings.Repeat("a", 64))
			execSQL(t, old.db, `INSERT INTO media_links(id,media_id,item_id) VALUES('link','photo','item-1')`)
			execSQL(t, old.db, failure.trigger)
			before := domainState(t, old)
			ddl, err := schemaDescription(t.Context(), old.db)
			if err != nil {
				t.Fatal(err)
			}
			_ = old.db.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = s.Close()
				t.Fatal("ignored required backfill or ledger write was reported as a committed upgrade")
			} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
				t.Fatal("failed upgrade lost its verified recovery snapshot", err)
			}
			unchanged := rawDatabase(t, path)
			requireDomainState(t, &Store{db: unchanged}, before)
			actual, err := schemaDescription(t.Context(), unchanged)
			if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 18 ||
				scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 18 {
				t.Fatal("ignored write committed a partial source, DDL or history", err)
			}
		})
	}
}

func TestMediaUpgradeRejectsInvalidLegacyAuthorityWithoutCommittingAnyPrefix(t *testing.T) {
	snapshot := emptyMediaRootEighteenSnapshot(t)
	for _, invalid := range []struct{ name, query string }{
		{"empty media ID", `UPDATE media SET id='' WHERE id='photo'`},
		{"oversized media ID with NUL", `UPDATE media SET id=char(0)||printf('%0128d',0) WHERE id='photo'`},
		{"invalid media ID UTF-8", `UPDATE media SET id=CAST(x'ff' AS TEXT) WHERE id='photo'`},
		{"short digest", `UPDATE media SET sha256='digest' WHERE id='photo'`},
		{"uppercase digest", `UPDATE media SET sha256=upper(sha256) WHERE id='photo'`},
		{"digest with native NUL", `UPDATE media SET sha256=char(0)||printf('%063d',0) WHERE id='photo'`},
		{"empty MIME", `UPDATE media SET mime_type='' WHERE id='photo'`},
		{"MIME with invalid UTF-8", `UPDATE media SET mime_type=CAST(x'ff' AS TEXT) WHERE id='photo'`},
		{"oversized MIME with NUL", `UPDATE media SET mime_type=char(0)||printf('%0100d',0) WHERE id='photo'`},
		{"unsafe width", `UPDATE media SET width=9007199254740992 WHERE id='photo'`},
		{"unsafe height", `UPDATE media SET height=9007199254740992 WHERE id='photo'`},
		{"unsafe byte count", `UPDATE media SET bytes=9007199254740992 WHERE id='photo'`},
		{"short perceptual hash", `UPDATE media SET perceptual_hash='short' WHERE id='photo'`},
		{"perceptual hash invalid UTF-8", `UPDATE media SET perceptual_hash=CAST(x'ff' AS TEXT) WHERE id='photo'`},
		{"unsafe source media counter", `INSERT INTO projection_revisions VALUES('media','photo',9007199254740992)`},
		{"unsafe absent media counter", `INSERT INTO projection_revisions VALUES('media','gone',9007199254740992)`},
		{"invalid absent media identity", `INSERT INTO projection_revisions VALUES('media','',4)`},
		{"empty link ID", `INSERT INTO media_links(id,media_id,item_id) VALUES('','photo','item-1')`},
		{"oversized link ID with NUL", `INSERT INTO media_links(id,media_id,item_id) VALUES(char(0)||printf('%0128d',0),'photo','item-1')`},
		{"invalid link ID UTF-8", `INSERT INTO media_links(id,media_id,item_id) VALUES(CAST(x'ff' AS TEXT),'photo','item-1')`},
		{"unsafe link sort", `INSERT INTO media_links(id,media_id,item_id,sort_order) VALUES('bad','photo','item-1',9007199254740992)`},
		{"unsafe source link counter", `INSERT INTO media_links(id,media_id,item_id) VALUES('bad','photo','item-1'); INSERT INTO projection_revisions VALUES('media_link','bad',9007199254740992)`},
		{"unsafe absent link counter", `INSERT INTO projection_revisions VALUES('media_link','gone',9007199254740992)`},
		{"missing item authority", `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('unproven','Unproven','quantity','2026','2026'); INSERT INTO media_links(id,media_id,item_id) VALUES('bad','photo','unproven')`},
		{"missing Unit authority", `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('unproven','Unproven','individual','2026','2026'); INSERT INTO projection_revisions VALUES('item','unproven',1); INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unproven-unit','unproven','UNPROVEN','2026','2026'); INSERT INTO media_links(id,media_id,item_unit_id) VALUES('bad','photo','unproven-unit')`},
		{"deleted location owner", `UPDATE locations SET deleted_at='2026' WHERE id='loc-1'; INSERT INTO media_links(id,media_id,location_id) VALUES('bad','photo','loc-1')`},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			path, old := mediaMatrixEighteenFixture(t, snapshot)
			execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','opaque',?,'image/jpeg',1,1,1,'2026')`, strings.Repeat("a", 64))
			execSQL(t, old.db, invalid.query)
			before := domainState(t, old)
			ddl, err := schemaDescription(t.Context(), old.db)
			if err != nil {
				t.Fatal(err)
			}
			_ = old.db.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = s.Close()
				t.Fatal("invalid retained media source or authority was certified")
			} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
				t.Fatal("failed backfill did not retain its actual recovery snapshot", err)
			}
			unchanged := rawDatabase(t, path)
			requireDomainState(t, &Store{db: unchanged}, before)
			actual, err := schemaDescription(t.Context(), unchanged)
			if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 18 ||
				scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 18 {
				t.Fatal("failed media validation committed schema, source or checksum prefix", err)
			}
		})
	}
}
