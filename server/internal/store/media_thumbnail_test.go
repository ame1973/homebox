package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func seedVerifiedThumbnail(t *testing.T, s *Store, id string) {
	t.Helper()
	execSQL(t, s.db, `UPDATE media SET verification='verified',mime_type='image/png' WHERE id=?`, id)
	execSQL(t, s.db, `INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES(?,?,2,3,321)`, id, strings.Repeat("b", 64))
}

func TestMediaThumbnailSourceKeepsClaimsPrivateAndGuardsLinkMutations(t *testing.T) {
	fixture, library, input := mediaLibraryFixture(t, "item")
	s := fixture.store
	seedVerifiedThumbnail(t, s, "shared-a")
	before, err := loadMedia(t.Context(), s.db, "shared-a")
	if err != nil || before.thumbnail == nil || before.thumbnail.SHA256 != strings.Repeat("b", 64) ||
		before.thumbnail.Width != 2 || before.thumbnail.Height != 3 || before.thumbnail.Bytes != 321 || before.ThumbURL != nil || before.ThumbSHA256 != nil {
		t.Fatal("thumbnail source was not retained exactly and privately", before, err)
	}
	// A manifest is not proof that any file exists or that a stream is available.
	public, err := library.Get(t.Context(), fixture.token, "shared-a")
	if err != nil || public.Verification != "verified" || public.ThumbURL != nil || public.ThumbSHA256 != nil {
		t.Fatal("a source claim invented a public thumbnail", public, err)
	}
	execSQL(t, s.db, `CREATE TRIGGER late_thumb_change AFTER INSERT ON mutation_events WHEN NEW.operation='post_media_id_links' BEGIN UPDATE media_thumbnails SET sha256='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' WHERE media_id='shared-a'; END`)
	state := domainState(t, s)
	if result, err := library.Attach(t.Context(), fixture.token, "attach-thumb-race", "shared-a", 7, input); !errors.Is(err, ErrDomainState) || result != (MediaLink{}) {
		t.Fatal("late thumbnail source change did not roll back attachment", result, err)
	}
	requireDomainState(t, s, state)
	if actual, err := loadMedia(t.Context(), s.db, "shared-a"); err != nil || !reflect.DeepEqual(actual, before) {
		t.Fatal("failed attachment retained changed thumbnail source", actual, err)
	}
	execSQL(t, s.db, "DROP TRIGGER late_thumb_change")
	link, err := library.Attach(t.Context(), fixture.token, "attach-thumb-race", "shared-a", 7, input)
	if err != nil || link.MediaID != "shared-a" || link.Revision != 1 {
		t.Fatal("the original reviewed attachment could not succeed after rollback", link, err)
	}
	current, err := loadMedia(t.Context(), s.db, "shared-a")
	before.Revision++
	if err != nil || !reflect.DeepEqual(current, before) {
		t.Fatal("link membership changed independent thumbnail source", current, err)
	}
}

func TestMediaThumbnailSourceGuardsOwnerDeletionAndSharedRecords(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	s := fixture.store
	seedVerifiedThumbnail(t, s, "shared-a")
	seedVerifiedThumbnail(t, s, "shared-b")
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails WHERE sha256=?", strings.Repeat("b", 64)) != 2 {
		t.Fatal("two Media records could not retain the same thumbnail digest")
	}
	before := domainState(t, s)
	execSQL(t, s.db, `CREATE TRIGGER late_owner_thumb_change AFTER INSERT ON mutation_events WHEN NEW.operation='delete_items_id' BEGIN UPDATE media_thumbnails SET width=3 WHERE media_id='shared-b'; END`)
	if result, err := fixture.delete(t.Context(), fixture.token, "delete-thumb-race", fixture.revision); !errors.Is(err, ErrDomainState) || result != (ActionAck{}) {
		t.Fatal("late thumbnail change escaped owner-deletion checks", result, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT width FROM media_thumbnails WHERE media_id='shared-b'") != 2 {
		t.Fatal("failed owner deletion left changed thumbnail source")
	}
	execSQL(t, s.db, "DROP TRIGGER late_owner_thumb_change")
	if result, err := fixture.delete(t.Context(), fixture.token, "delete-thumb-race", fixture.revision); err != nil || !result.OK {
		t.Fatal("reviewed owner deletion failed after rollback", result, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails WHERE sha256=?", strings.Repeat("b", 64)) != 2 {
		t.Fatal("owner deletion removed a shared Media thumbnail claim")
	}
}

func TestMediaThumbnailSourceRequiresVerifiedOwnerAndSafeBounds(t *testing.T) {
	fixture := newMediaCascadeFixture(t, "item")
	s := fixture.store
	valid := strings.Repeat("b", 64)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO media_thumbnails VALUES('shared-a',?,2,3,321)`, valid); err == nil {
		t.Fatal("unverified media acquired a thumbnail")
	}
	execSQL(t, s.db, `UPDATE media SET verification='verified' WHERE id='shared-a'`)
	for _, row := range []struct {
		hash                 string
		width, height, bytes int64
	}{
		{strings.Repeat("A", 64), 2, 3, 321},
		{strings.Repeat("b", 63), 2, 3, 321},
		{valid, 0, 3, 321}, {valid, 2, 513, 321}, {valid, 2, 3, 0}, {valid, 2, 3, 528777217},
	} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO media_thumbnails VALUES('shared-a',?,?,?,?)`, row.hash, row.width, row.height, row.bytes); err == nil {
			t.Fatal("invalid thumbnail row passed the SQL boundary", row)
		}
	}
	execSQL(t, s.db, `INSERT INTO media_thumbnails VALUES('shared-a',?,2,3,321)`, valid)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE media SET verification='unverified' WHERE id='shared-a'`); err == nil {
		t.Fatal("a thumbnail stayed attached to an unverified parent")
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE media_thumbnails SET media_id='shared-b' WHERE media_id='shared-a'`); err == nil {
		t.Fatal("a thumbnail moved to an unverified parent")
	}
	execSQL(t, s.db, `UPDATE media SET verification='verified' WHERE id='shared-b'`)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE media_thumbnails SET media_id='shared-b' WHERE media_id='shared-a'`); err == nil {
		t.Fatal("a thumbnail moved to a different verified Media")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	if _, err := s.db.ExecContext(t.Context(), `INSERT OR REPLACE INTO media_thumbnails VALUES('shared-a',?,2,3,321)`, strings.Repeat("c", 64)); err == nil {
		t.Fatal("SQLite REPLACE silently replaced the thumbnail source")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	// The SQL dimension bound alone cannot compare a child to its actual parent.
	execSQL(t, s.db, `UPDATE media_thumbnails SET width=11 WHERE media_id='shared-a'`)
	if _, err := loadMedia(t.Context(), s.db, "shared-a"); !errors.Is(err, ErrDomainState) {
		t.Fatal("thumbnail larger than the actual source was accepted", err)
	}
	execSQL(t, s.db, `UPDATE media_thumbnails SET width=2 WHERE media_id='shared-a'`)
	// Native SQLite text length stops at NUL; the Go authority must not.
	corrupt := valid + "\x00extra"
	execSQL(t, s.db, `UPDATE media_thumbnails SET sha256=? WHERE media_id='shared-a'`, corrupt)
	if _, err := loadMedia(t.Context(), s.db, "shared-a"); !errors.Is(err, ErrDomainState) {
		t.Fatal("NUL-hidden thumbnail digest bytes were accepted", err)
	}
}

func TestMediaThumbnailMissingTableFailsClosed(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	execSQL(t, fixture.store.db, "DROP TABLE media_thumbnails")
	if media, err := library.Get(t.Context(), fixture.token, "shared-a"); err == nil || media != (Media{}) {
		t.Fatal("a missing current-schema thumbnail table was treated as no thumbnail", media, err)
	}
	if page, err := library.Page(t.Context(), fixture.token, "", 5); err == nil || page.Data != nil || page.NextCursor != nil {
		t.Fatal("a missing thumbnail table returned a partial global page", page, err)
	}
}

func TestMediaThumbnailCorruptLookaheadWithholdsTheWholePage(t *testing.T) {
	fixture, library, _ := mediaLibraryFixture(t, "item")
	seedVerifiedThumbnail(t, fixture.store, "shared-b")
	execSQL(t, fixture.store.db, `UPDATE media SET width=1 WHERE id='shared-b'`)
	page, err := library.PageOwnerLinks(t.Context(), fixture.token, "item", fixture.ownerID, "", 2)
	if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("corrupt thumbnail lookahead returned a partial Media page", page, err)
	}
}

func mediaThumbnailRootTwentyTwoFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := keyedReceiptRootTwentyOneFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[21]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func TestMediaThumbnailMigrationDoesNotInventClaimsOrRewriteFrozenBytes(t *testing.T) {
	path, old := mediaThumbnailRootTwentyTwoFixture(t)
	execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('legacy-photo','opaque-old',?,'image/png',10,20,30,'old')`, strings.Repeat("a", 64))
	execSQL(t, old.db, `INSERT INTO projection_revisions VALUES('media','legacy-photo',3)`)
	legacyPayload := ` { "retained_thumbnail_claim" : true } `
	execSQL(t, old.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('media','legacy-photo',3,?,'old')`, legacyPayload)
	execSQL(t, old.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('media','legacy-photo','upsert',3,?,'old')`, legacyPayload)
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 22 || s.Report().To < 23 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("thumbnail migration omitted recovery or changed the epoch")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_thumbnails") != 0 {
		t.Fatal("migration fabricated a thumbnail or rewrote published bytes")
	}
	media, err := loadMedia(t.Context(), s.db, "legacy-photo")
	if err != nil || media.Verification != "unverified" || media.thumbnail != nil || media.ThumbURL != nil || media.ThumbSHA256 != nil {
		t.Fatal("migration changed retained media claims", media, err)
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-migration snapshot was not verified", err)
	}
	var node, parent, unused int
	var plan string
	if err = s.db.QueryRowContext(t.Context(), `EXPLAIN QUERY PLAN SELECT media_id FROM media_thumbnails WHERE sha256=? ORDER BY media_id`, strings.Repeat("b", 64)).Scan(&node, &parent, &unused, &plan); err != nil || !strings.Contains(plan, "idx_media_thumbnails_sha256") || strings.Contains(plan, "TEMP") {
		t.Fatal("thumbnail reference lookup lacks its ordered digest index", plan, err)
	}
}

func TestMediaThumbnailMigrationRejectsLateLedgerInjectionAndRollsBack(t *testing.T) {
	path, old := mediaThumbnailRootTwentyTwoFixture(t)
	execSQL(t, old.db, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification) VALUES('prior-verified','old',?,'image/png',10,20,30,'old','verified')`, strings.Repeat("a", 64))
	execSQL(t, old.db, `INSERT INTO projection_revisions VALUES('media','prior-verified',1)`)
	execSQL(t, old.db, `CREATE TRIGGER inject_thumbnail AFTER INSERT ON schema_migration_checksums WHEN NEW.version=23 BEGIN INSERT INTO media_thumbnails VALUES('prior-verified','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',2,3,321); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("late ledger injection committed an invented thumbnail")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration lost its recovery snapshot", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 22 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='media_thumbnails'") != 0 {
		t.Fatal("late failure leaked thumbnail DDL, source or migration history", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed migration did not retain its verified original snapshot", err)
	}
}
