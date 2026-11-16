package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"homebox/migrations"
)

func pendingTestStages(t *testing.T, staging, objects string) ([]*mediaStage, *os.File) {
	t.Helper()
	stages := []*mediaStage{
		sealedMediaStage(t, staging, []byte("sanitized original")),
		sealedMediaStage(t, staging, []byte("sanitized thumbnail")),
	}
	dir, err := openPrivateMediaDirectory(objects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
	})
	return stages, dir
}

func TestMediaPendingJournalRequiresCompletedSessionAndPreservesBothObjects(t *testing.T) {
	s, _, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	epoch := scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state")
	before := domainState(t, s)
	pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if err != nil || len(pending) != 2 || pending[0].ID == pending[1].ID || pending[0].Epoch != epoch || pending[1].Epoch != epoch ||
		pending[0].SHA256 != mediaObjectKey(sha256.Sum256([]byte("sanitized original")))[7:] || pending[1].SHA256 != mediaObjectKey(sha256.Sum256([]byte("sanitized thumbnail")))[7:] ||
		pending[0].Bytes != 18 || pending[1].Bytes != 19 || pending[0].StageName != stages[0].name || pending[1].StageName != stages[1].name ||
		pending[0].StageInode == pending[1].StageInode || pending[0].ObjectInode != pending[1].ObjectInode {
		t.Fatal("sanitized objects were not independently journaled with actual inode identity", pending, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("pending journal did not commit both objects")
	}
	if duplicate, err := library.journalMediaObjects(t.Context(), token, stages, objects); err == nil || duplicate != nil {
		t.Fatal("the same stage inode was journaled twice", duplicate, err)
	}
	requireDomainState(t, s, before)
	for _, row := range pending {
		if strings.Contains(row.SHA256, "sanitized") || strings.Contains(row.ID, token) {
			t.Fatal("pending journal retained raw bytes or a credential")
		}
	}
	if value, err := library.journalMediaObjects(t.Context(), "invalid-token", stages, objects); !errors.Is(err, ErrAuthentication) || value != nil {
		t.Fatal("an unauthenticated caller journaled objects", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := library.journalMediaObjects(t.Context(), token, stages, objects); !errors.Is(err, ErrPasswordChangeRequired) || value != nil {
		t.Fatal("an incomplete session journaled objects", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := library.journalMediaObjects(ctx, token, stages, objects); !errors.Is(err, context.Canceled) || value != nil {
		t.Fatal("canceled journal returned a prefix", value, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("a rejected request changed an earlier pending journal")
	}
}

func TestMediaPendingJournalRejectsInvalidInputsAndSharesDigestsSafely(t *testing.T) {
	s, _, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	third := sealedMediaStage(t, staging, []byte("sanitized original"))
	otherDir := privateTempDir(t)
	other := sealedMediaStage(t, otherDir, []byte("another stage"))
	unsealed, err := newMediaStage(t.Context(), staging, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unsealed.discard(); err != nil {
			t.Error(err)
		}
	})
	for _, values := range [][]*mediaStage{nil, {}, stages[:1], {stages[0], stages[1], third}, {stages[0], stages[0]}, {stages[0], nil}, {stages[0], other}} {
		if rows, err := library.journalMediaObjects(t.Context(), token, values, objects); !errors.Is(err, ErrDomainInput) || rows != nil {
			t.Fatal("invalid or mixed-directory stages were accepted", rows, err)
		}
	}
	if rows, err := library.journalMediaObjects(t.Context(), token, []*mediaStage{stages[0], unsealed}, objects); !errors.Is(err, errMediaStageUnsealed) || rows != nil {
		t.Fatal("unsealed stage was journaled", rows, err)
	}
	if rows, err := library.journalMediaObjects(t.Context(), token, stages, nil); !errors.Is(err, ErrUnsafePath) || rows != nil {
		t.Fatal("missing object directory was accepted", rows, err)
	}
	sameDirectory, err := openPrivateMediaDirectory(staging)
	if err != nil {
		t.Fatal(err)
	}
	defer sameDirectory.Close()
	if rows, err := library.journalMediaObjects(t.Context(), token, stages, sameDirectory); !errors.Is(err, errMediaObjectDirectory) || rows != nil {
		t.Fatal("staging and object directories were allowed to coincide", rows, err)
	}
	stagePath := filepath.Join(staging, stages[0].name)
	if err = os.Chmod(stagePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if rows, err := library.journalMediaObjects(t.Context(), token, stages, objects); !errors.Is(err, ErrUnsafePath) || rows != nil {
		t.Fatal("permissive stage permissions were journaled", rows, err)
	}
	if err = os.Chmod(stagePath, 0o600); err != nil {
		t.Fatal(err)
	}
	extra := stagePath + ".hardlink"
	if err = os.Link(stagePath, extra); err != nil {
		t.Fatal(err)
	}
	if rows, err := library.journalMediaObjects(t.Context(), token, stages, objects); !errors.Is(err, ErrUnsafePath) || rows != nil {
		t.Fatal("a writable stage with another hard link was journaled", rows, err)
	}
	if err = os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	rows, err := library.journalMediaObjects(t.Context(), token, []*mediaStage{stages[0], third}, objects)
	if err != nil || len(rows) != 2 || rows[0].ID == rows[1].ID || rows[0].SHA256 != rows[1].SHA256 || rows[0].StageInode == rows[1].StageInode {
		t.Fatal("same-content stages lost their independent pending identities", rows, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects WHERE sha256=?", rows[0].SHA256) != 2 {
		t.Fatal("a shared digest collapsed two pending operations")
	}
}

func TestMediaPendingJournalRollsBackLateAuthorizationAndIgnoredWrites(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"first insert ignored", `CREATE TRIGGER pending_failure BEFORE INSERT ON media_pending_objects BEGIN SELECT RAISE(IGNORE); END`},
		{"second insert ignored", `CREATE TRIGGER pending_failure BEFORE INSERT ON media_pending_objects WHEN (SELECT COUNT(*) FROM media_pending_objects)>0 BEGIN SELECT RAISE(IGNORE); END`},
		{"session activity ignored", `CREATE TRIGGER pending_failure BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END`},
		{"session revoked", `CREATE TRIGGER pending_failure AFTER INSERT ON media_pending_objects BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END`},
		{"epoch changed", `CREATE TRIGGER pending_failure AFTER INSERT ON media_pending_objects BEGIN UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1; END`},
		{"row removed after activity", `CREATE TRIGGER pending_failure AFTER UPDATE ON sessions BEGIN DELETE FROM media_pending_objects; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objectPath := privateUploadFixture(t)
			stages, objects := pendingTestStages(t, staging, objectPath)
			before := domainState(t, s)
			execSQL(t, s.db, test.trigger)
			if rows, err := library.journalMediaObjects(t.Context(), token, stages, objects); err == nil || rows != nil {
				t.Fatal("partial or unauthorized pending journal was returned", rows, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
				t.Fatal("failed journal left a partial pending set")
			}
		})
	}
}

func TestMediaPendingJournalRejectsStageSubstitutionAfterSessionActivity(t *testing.T) {
	s, auth, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	path := filepath.Join(staging, stages[0].name)
	clock := auth.now
	calls := 0
	var swapErr error
	auth.now = func() time.Time {
		calls++
		if calls == 3 {
			swapErr = os.Rename(path, path+".original")
			if swapErr == nil {
				swapErr = os.WriteFile(path, []byte("unrelated"), 0o600)
			}
		}
		return clock()
	}
	before := domainState(t, s)
	pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if calls < 3 || swapErr != nil || !errors.Is(err, errMediaStageChanged) || pending != nil {
		t.Fatal("a substituted stage was journaled", calls, swapErr, pending, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("failed stage identity check left pending records")
	}
	// Restore this test-owned stage before its fixture cleanup runs.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".original", path); err != nil {
		t.Fatal(err)
	}
}

func TestMediaPendingJournalRechecksExpiryAfterFileIdentityValidation(t *testing.T) {
	s, auth, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	clock := auth.now
	calls := 0
	auth.now = func() time.Time {
		calls++
		now := clock()
		if calls == 4 {
			return now.Add(31 * time.Minute)
		}
		return now
	}
	before := domainState(t, s)
	if pending, err := library.journalMediaObjects(t.Context(), token, stages, objects); !errors.Is(err, ErrAuthentication) || pending != nil || calls < 4 {
		t.Fatal("expired session committed a pending journal", pending, calls, err)
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("expired session left pending objects")
	}
}

func consumeTestPending(t *testing.T, auth *Auth, token, key string, pending []pendingMediaObject) (ActionAck, error) {
	t.Helper()
	return runDomainMutation(t.Context(), auth, token, key, "test_consume_pending", struct{}{}, func(m *mutationTx) (ActionAck, error) {
		if err := m.consumeMediaPending(pending); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
}

func TestMediaPendingConsumeRequiresExactEpochAndOriginalRows(t *testing.T) {
	s, auth, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]pendingMediaObject){
		func(rows []pendingMediaObject) { rows[1].Epoch = "different-epoch" },
		func(rows []pendingMediaObject) { rows[1].SHA256 = strings.Repeat("a", 64) },
		func(rows []pendingMediaObject) { rows[1].Bytes++ },
		func(rows []pendingMediaObject) { rows[1].CreatedAt += "x" },
		func(rows []pendingMediaObject) { rows[1].StageName = "../outside" },
		func(rows []pendingMediaObject) { rows[1].StageInode++ },
		func(rows []pendingMediaObject) { rows[1].StagingInode++ },
		func(rows []pendingMediaObject) { rows[1].ObjectInode++ },
		func(rows []pendingMediaObject) { rows[1].ID = rows[0].ID },
	} {
		altered := append([]pendingMediaObject(nil), pending...)
		change(altered)
		before := domainState(t, s)
		if ack, err := consumeTestPending(t, auth, token, "bad-pending-consume", altered); !errors.Is(err, ErrDomainState) || ack != (ActionAck{}) {
			t.Fatal("changed pending state was consumed", ack, err)
		}
		requireDomainState(t, s, before)
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
			t.Fatal("invalid consume removed a pending prefix")
		}
	}
	first, err := consumeTestPending(t, auth, token, "good-pending-consume", pending)
	if err != nil || !first.OK || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("actual pending set did not commit with the domain mutation", first, err)
	}
	replay, err := consumeTestPending(t, auth, token, "good-pending-consume", pending)
	if err != nil || replay != first || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("original-key replay incorrectly required the consumed pending rows", replay, err)
	}
}

func TestMediaPendingConsumeRollsBackIgnoredDeleteAndLateReinsertion(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"ignored delete", `CREATE TRIGGER pending_failure BEFORE DELETE ON media_pending_objects BEGIN SELECT RAISE(IGNORE); END`},
		{"late reinsert", `CREATE TRIGGER pending_failure AFTER INSERT ON mutation_events WHEN NEW.operation='test_consume_pending' BEGIN INSERT INTO media_pending_objects SELECT * FROM pending_shadow; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, auth, library, token, staging, objectPath := privateUploadFixture(t)
			stages, objects := pendingTestStages(t, staging, objectPath)
			pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "late reinsert" {
				execSQL(t, s.db, "CREATE TABLE pending_shadow AS SELECT * FROM media_pending_objects")
			}
			execSQL(t, s.db, test.trigger)
			before := domainState(t, s)
			if ack, err := consumeTestPending(t, auth, token, "pending-failure-key", pending); err == nil || ack != (ActionAck{}) {
				t.Fatal("a failed pending consume returned an acknowledgement", ack, err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
				t.Fatal("pending consume failure lost an original row")
			}
		})
	}
}

func TestMediaPendingJournalRowsAreImmutableAndNeverReplaceable(t *testing.T) {
	s, _, library, token, staging, objectPath := privateUploadFixture(t)
	stages, objects := pendingTestStages(t, staging, objectPath)
	pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, statement := range []string{
		`UPDATE media_pending_objects SET bytes=bytes+1`,
		`INSERT OR REPLACE INTO media_pending_objects SELECT id,dataset_epoch,sha256,bytes+1,created_at,stage_name,stage_device,stage_inode,staging_device,staging_inode,object_device,object_inode FROM media_pending_objects LIMIT 1`,
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("pending row accepted update or REPLACE", statement)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != len(pending) {
		t.Fatal("rejected pending writes changed the journal")
	}
}

func mediaPendingRootTwentyThreeFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := mediaThumbnailRootTwentyTwoFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[22]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func TestMediaPendingMigrationPreservesSourceAndStartsEmpty(t *testing.T) {
	path, old := mediaPendingRootTwentyThreeFixture(t)
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 23 || s.Report().To < 24 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("pending journal migration changed epoch or omitted recovery")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("migration invented pending objects or rewrote current bytes")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-migration snapshot failed verification", err)
	}
	for _, check := range []struct {
		query, index string
		args         []any
	}{
		{`SELECT id FROM media_pending_objects WHERE dataset_epoch=? AND object_device=? AND object_inode=? ORDER BY id`, "idx_media_pending_epoch", []any{epoch, 1, 2}},
		{`SELECT id FROM media_pending_objects WHERE sha256=? ORDER BY id`, "idx_media_pending_digest", []any{strings.Repeat("a", 64)}},
	} {
		var node, parent, unused int
		var plan string
		if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+check.query, check.args...).Scan(&node, &parent, &unused, &plan); err != nil || !strings.Contains(plan, check.index) || strings.Contains(plan, "TEMP") {
			t.Fatal("pending lookup lacks its ordered index", plan, err)
		}
	}
}

func TestMediaPendingMigrationRejectsLateLedgerInjectionAndRollsBack(t *testing.T) {
	path, old := mediaPendingRootTwentyThreeFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER inject_pending AFTER INSERT ON schema_migration_checksums WHEN NEW.version=24 BEGIN INSERT INTO media_pending_objects VALUES('fake','fake','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'2026','media-00000000000000000000000000000000.partial',1,1,1,2,1,3); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("late ledger injection committed a fabricated pending object")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration lost recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 23 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='media_pending_objects'") != 0 {
		t.Fatal("failed pending migration leaked DDL, source or ledger state", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed pending migration did not retain a verified snapshot", err)
	}
}
