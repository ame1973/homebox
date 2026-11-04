package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homebox/migrations"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), Config{Path: filepath.Join(privateTempDir(t), "homebox.sqlite"), Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var value T
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func rawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openSQL(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func legacyFixture(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(privateTempDir(t), "legacy.sqlite")
	db := rawDatabase(t, path)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range all[:version] {
		execSQL(t, db, migration.SQL)
	}
	execSQL(t, db, `INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('loc-1','BOX-1','Storage','2026','2026')`)
	execSQL(t, db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item-1','Cable','quantity','2026','2026')`)
	execSQL(t, db, `INSERT INTO stock_balances VALUES('item-1','loc-1',1500,'2026')`)
	return path, db
}

func TestFreshMigrationCapabilitiesAndReopen(t *testing.T) {
	s := openTestStore(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	health, err := s.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if health.SchemaVersion != len(all) || health.JournalMode != "wal" || !health.ForeignKeys || health.BusyTimeoutMS != 5000 || health.Integrity != "ok" {
		t.Fatalf("unexpected diagnostics: %+v", health)
	}
	if s.Report().From != 0 || s.Report().To != len(all) || s.Report().BackupPath != "" {
		t.Fatalf("unexpected fresh-install report: %+v", s.Report())
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM schema_migration_checksums"); count != len(all) {
		t.Fatal("migration checksums not committed")
	}
	for _, suffix := range []string{"", ".lock", "-wal", "-shm"} {
		info, err := os.Stat(s.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("private file %s has permissions %v", suffix, info.Mode().Perm())
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal("close is not idempotent", err)
	}
	reopened, err := Open(t.Context(), Config{Path: s.path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Report().From != len(all) || reopened.Report().BackupPath != "" {
		t.Fatal("normal startup unexpectedly migrated")
	}
}

func TestConnectionReplacementPreservesRequiredPragmas(t *testing.T) {
	s := openTestStore(t)
	s.db.SetMaxIdleConns(0)
	for range 3 {
		connection, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var foreignKeys, timeout int
		var mode string
		if err = connection.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err = connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if err = connection.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if err = connection.Close(); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || timeout != 5000 || mode != "wal" {
			t.Fatal("replacement connection lost its safety pragmas")
		}
	}
}

func TestIndependentOpenCannotBecomeASecondWriter(t *testing.T) {
	s := openTestStore(t)
	if second, err := Open(t.Context(), Config{Path: s.path, Migrate: true}); !errors.Is(err, ErrLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second writer was not blocked: %v", err)
	}
}

func TestStartupDoesNotSilentlyInitializeAnEmptyDatabase(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "empty.sqlite")
	if _, err := Open(t.Context(), Config{Path: path}); !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("missing database bypassed explicit initialization: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup silently created the database")
	}
	db := rawDatabase(t, path)
	_ = db.Close()
	if _, err := Open(t.Context(), Config{Path: path}); !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("expected an explicit migration gate, got %v", err)
	}
}

func TestLegacyAdoptionRequiresApprovalAndPreservesVerifiedSnapshot(t *testing.T) {
	path, old := legacyFixture(t, 1)
	_ = old.Close()
	if _, err := Open(t.Context(), Config{Path: path, Migrate: true}); !errors.Is(err, ErrLegacyApproval) {
		t.Fatalf("unapproved legacy adoption: %v", err)
	}
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	report := s.Report()
	if report.From != 1 || report.To != 2 || !report.LegacyAdopted || len(report.BackupSHA256) != 64 {
		t.Fatalf("invalid upgrade evidence: %+v", report)
	}
	if err = VerifySnapshot(t.Context(), report.BackupPath); err != nil {
		t.Fatal(err)
	}
	if quantity := scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances"); quantity != 1500 {
		t.Fatal("migration changed existing stock")
	}
	backup, err := openSQL(report.BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if version := scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations"); version != 1 {
		t.Fatal("backup was taken after the upgrade")
	}
	if quantity := scalar[int](t, backup, "SELECT quantity_base FROM stock_balances"); quantity != 1500 {
		t.Fatal("backup omitted committed WAL data")
	}
}

func TestCanonicalLegacyAdoptionRotatesEpochButKeepsInstance(t *testing.T) {
	path, old := legacyFixture(t, 2)
	epoch := scalar[string](t, old, "SELECT dataset_epoch FROM sync_state")
	instance := scalar[string](t, old, "SELECT server_instance_id FROM sync_state")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") == epoch {
		t.Fatal("legacy adoption retained an uncertified dataset epoch")
	}
	if scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state") != instance {
		t.Fatal("legacy adoption changed the server identity")
	}
}

func TestLegacyPrecisionMappingIsExplicitAndDoesNotRescaleStock(t *testing.T) {
	path, old := legacyFixture(t, 1)
	execSQL(t, old, "UPDATE items SET unit_code='kg'")
	_ = old.Close()
	cfg := Config{Path: path, Migrate: true, AllowLegacy: true}
	for _, mappings := range []map[string]UnitMapping{
		nil,
		{"item-1": {BaseUnit: "g", ScaleFactor: 0}},
		{"item-1": {BaseUnit: "", ScaleFactor: 1000}},
		{"item-1": {BaseUnit: "g", ScaleFactor: 1000}, "typo": {BaseUnit: "g", ScaleFactor: 1}},
	} {
		cfg.LegacyUnits = mappings
		if _, err := Open(t.Context(), cfg); !errors.Is(err, ErrUnitMapping) {
			t.Fatalf("invalid unit mapping accepted: %v", err)
		}
	}
	cfg.LegacyUnits = map[string]UnitMapping{"item-1": {BaseUnit: "g", ScaleFactor: 1000}}
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 ||
		scalar[int](t, s.db, "SELECT scale_factor FROM items") != 1000 ||
		scalar[string](t, s.db, "SELECT base_unit FROM items") != "g" {
		t.Fatal("legacy precision transition changed the stored count or lost its mapping")
	}
}

func TestLegacyUnknownSchemaCycleAndWrongTrackingModeAreRejected(t *testing.T) {
	for name, query := range map[string]string{
		"extra table":       "CREATE TABLE unsupported(value TEXT) STRICT",
		"missing index":     "DROP INDEX idx_stock_location",
		"location cycle":    `INSERT INTO locations(id,code,name,parent_id,created_at,updated_at) VALUES('loc-2','BOX-2','Child','loc-1','2026','2026'); UPDATE locations SET parent_id='loc-2' WHERE id='loc-1'`,
		"category cycle":    `INSERT INTO categories(id,name) VALUES('cat-1','A'); INSERT INTO categories(id,name,parent_id) VALUES('cat-2','B','cat-1'); UPDATE categories SET parent_id='cat-2' WHERE id='cat-1'`,
		"tracking mismatch": "UPDATE items SET tracking_mode='individual'",
	} {
		t.Run(name, func(t *testing.T) {
			path, old := legacyFixture(t, 1)
			execSQL(t, old, query)
			_ = old.Close()
			if _, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); !errors.Is(err, ErrSchemaChanged) {
				t.Fatalf("unsafe legacy database accepted: %v", err)
			}
		})
	}
}

func TestChecksumChangesMissingEntriesAndNewerSchemasAreRejected(t *testing.T) {
	for name, query := range map[string]string{
		"checksum":      "UPDATE schema_migration_checksums SET sha256='" + strings.Repeat("a", 64) + "' WHERE version=1",
		"filename":      "UPDATE schema_migration_checksums SET filename='unexpected.sql' WHERE version=1",
		"missing entry": "DELETE FROM schema_migration_checksums WHERE version=1",
		"newer schema":  "INSERT INTO schema_migrations VALUES(999,'future','2026')",
	} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			execSQL(t, s.db, query)
			_ = s.Close()
			_, err := Open(t.Context(), Config{Path: s.path, Migrate: true})
			want := ErrSchemaChanged
			if name == "newer schema" {
				want = ErrNewerSchema
			}
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
}

func TestOutOfOrderAndUnversionedDatabasesAreRejected(t *testing.T) {
	for name, query := range map[string]string{
		"unversioned":   "CREATE TABLE private_data(value TEXT) STRICT",
		"gap":           "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT,applied_at TEXT) STRICT; INSERT INTO schema_migrations VALUES(2,'gap','2026')",
		"empty history": "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT,applied_at TEXT) STRICT",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "unknown.sqlite")
			db := rawDatabase(t, path)
			execSQL(t, db, query)
			_ = db.Close()
			if _, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); !errors.Is(err, ErrSchemaChanged) {
				t.Fatalf("unsafe history accepted: %v", err)
			}
		})
	}
}

func TestAllPendingMigrationsRollbackWithTheirChecksumRows(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "INSERT INTO tags(id,name) VALUES('kept','Preserved')")
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	for offset, query := range []string{"CREATE TABLE rollback_probe(value INTEGER) STRICT;", "INSERT INTO rollback_probe VALUES(1); INVALID SQL;"} {
		digest := sha256.Sum256([]byte(query))
		all = append(all, migrations.Migration{Version: 3 + offset, Filename: fmt.Sprintf("%04d_test.sql", 3+offset), Checksum: hex.EncodeToString(digest[:]), SQL: query})
	}
	report, err := s.migrate(t.Context(), Config{Migrate: true, ReserveBytes: defaultReserveBytes}, all)
	if err == nil || report.BackupPath == "" {
		t.Fatalf("expected rollback and retained snapshot, got %+v, %v", report, err)
	}
	if err = VerifySnapshot(t.Context(), report.BackupPath); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sqlite_schema WHERE name='rollback_probe'") != 0 ||
		scalar[int](t, s.db, "SELECT MAX(version) FROM schema_migrations") != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM schema_migration_checksums") != 2 ||
		scalar[string](t, s.db, "SELECT name FROM tags WHERE id='kept'") != "Preserved" {
		t.Fatal("failed migration leaked DDL, data, or checksum metadata")
	}
}

func TestInsufficientSpaceRefusesUpgradeBeforeMutation(t *testing.T) {
	path, old := legacyFixture(t, 1)
	_ = old.Close()
	if _, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true, ReserveBytes: math.MaxUint64}); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("expected free-space refusal, got %v", err)
	}
	db, err := openSQL(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if scalar[int](t, db, "SELECT MAX(version) FROM schema_migrations") != 1 {
		t.Fatal("insufficient-space refusal still upgraded the database")
	}
}

func TestSnapshotIncludesCommittedWALAndDetectsTampering(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "PRAGMA wal_autocheckpoint=0")
	execSQL(t, s.db, "INSERT INTO tags(id,name) VALUES('tag-1','Before backup')")
	directory := privateTempDir(t)
	path, digest, err := s.snapshot(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatal("missing backup digest")
	}
	execSQL(t, s.db, "UPDATE tags SET name='After backup'")
	if err = VerifySnapshot(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	db, err := openSQL(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, db, "SELECT name FROM tags WHERE id='tag-1'") != "Before backup" {
		t.Fatal("snapshot is not frozen at committed WAL state")
	}
	_ = db.Close()
	if _, err = os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("snapshot is not self-contained")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("tampered")
	_ = file.Close()
	if err = VerifySnapshot(t.Context(), path); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
}

func TestCancellationLeavesNoPublishedBackupOrMigration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	path := filepath.Join(privateTempDir(t), "cancelled.sqlite")
	if _, err := Open(ctx, Config{Path: path, Migrate: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled startup created a database")
	}
	s := openTestStore(t)
	directory := privateTempDir(t)
	if _, _, err := s.snapshot(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backup: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled backup left staging data: %v, %v", entries, err)
	}
}

func TestLiteralFileNamesCannotInjectSQLiteURIOptions(t *testing.T) {
	path := filepath.Join(privateTempDir(t), "private ?mode=memory#file.sqlite")
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("database path was interpreted as a URI: %v", err)
	}
}

func TestUnsafePermissionsAndSymlinksAreRejected(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := privateTempDir(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), Config{Path: filepath.Join(dir, "db.sqlite"), Migrate: true}); !errors.Is(err, ErrUnsafePath) {
			t.Fatal(err)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(privateTempDir(t), "db.sqlite")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), Config{Path: path, Migrate: true}); !errors.Is(err, ErrUnsafePath) {
			t.Fatal(err)
		}
	})
	for _, suffix := range []string{"", ".lock"} {
		t.Run("symlink"+suffix, func(t *testing.T) {
			dir := privateTempDir(t)
			target := filepath.Join(dir, "preserved")
			if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "db.sqlite")
			if err := os.Symlink(target, path+suffix); err != nil {
				t.Fatal(err)
			}
			if store, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = store.Close()
				t.Fatal("symlink was followed")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "untouched" {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestFTSExternalContentTriggersWorkWithTheProductionDriver(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('item-1','Camera','individual','2026','2026')")
	execSQL(t, s.db, "INSERT INTO search_documents(item_id,title,updated_at) VALUES('item-1','Camera','2026')")
	execSQL(t, s.db, "INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,created_at,updated_at) VALUES('review-1','item-1','tag','unapproved','needs_review','2026','2026')")
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'Camera'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'unapproved'") != 0 {
		t.Fatal("FTS source isolation failed")
	}
	execSQL(t, s.db, "UPDATE search_documents SET reviewed_ai='curated'")
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'curated'") != 1 {
		t.Fatal("FTS update trigger failed")
	}
	execSQL(t, s.db, "DELETE FROM search_documents")
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'Camera OR curated'") != 0 {
		t.Fatal("FTS delete trigger left stale terms")
	}
}

func TestWriteTransactionsAcquireTheirLockBeforeAnyMutation(t *testing.T) {
	s := openTestStore(t)
	other := rawDatabase(t, s.path)
	execSQL(t, other, "PRAGMA busy_timeout=0")
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	second, err := other.BeginTx(t.Context(), nil)
	if err == nil {
		_ = second.Rollback()
		t.Fatal("a deferred transaction allowed another writer to acquire the reservation")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("expected SQLite writer-lock contention, got %v", err)
	}
}

func TestDatabaseRejectsPermissiveOrSymlinkedSQLiteSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		for _, kind := range []string{"permissive", "symlink"} {
			t.Run(suffix+kind, func(t *testing.T) {
				s := openTestStore(t)
				_ = s.Close()
				if kind == "symlink" {
					target := filepath.Join(filepath.Dir(s.path), "untouched")
					if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, s.path+suffix); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(s.path+suffix, []byte("invalid"), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := Open(t.Context(), Config{Path: s.path}); !errors.Is(err, ErrUnsafePath) {
					t.Fatalf("unsafe sidecar accepted: %v", err)
				}
			})
		}
	}
}

func TestSnapshotsRejectAllUnhashedSidecarsAndInvalidMarkers(t *testing.T) {
	for _, damage := range []string{"-wal", "-shm", "-journal", "missing marker", "oversized marker", "symlinked marker", "malformed marker", "wrong filename"} {
		t.Run(damage, func(t *testing.T) {
			s := openTestStore(t)
			path, digest, err := s.snapshot(t.Context(), privateTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			marker := path + ".sha256"
			switch damage {
			case "-wal", "-shm", "-journal":
				err = os.WriteFile(path+damage, []byte("unhashed"), 0o600)
			case "missing marker":
				err = os.Remove(marker)
			case "oversized marker":
				err = os.WriteFile(marker, []byte(strings.Repeat("a", 513)), 0o600)
			case "symlinked marker":
				if err = os.Remove(marker); err == nil {
					err = os.Symlink(path, marker)
				}
			case "malformed marker":
				err = os.WriteFile(marker, []byte("not a checksum"), 0o600)
			case "wrong filename":
				err = os.WriteFile(marker, []byte(digest+"  another.sqlite\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = VerifySnapshot(t.Context(), path); err == nil {
				t.Fatal("unsafe snapshot passed verification")
			}
		})
	}
}

func TestForeignKeyViolationBlocksStartup(t *testing.T) {
	s := openTestStore(t)
	execSQL(t, s.db, "PRAGMA foreign_keys=OFF")
	execSQL(t, s.db, "INSERT INTO item_aliases VALUES('bad','missing','Alias','2026')")
	_ = s.Close()
	if _, err := Open(t.Context(), Config{Path: s.path}); err == nil || !strings.Contains(err.Error(), "foreign-key") {
		t.Fatalf("invalid foreign keys accepted: %v", err)
	}
}
