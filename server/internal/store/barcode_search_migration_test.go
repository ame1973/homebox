package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func barcodeRootTwentyFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := retainedRowsRootNineteenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[19]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestBarcodeLookupMigrationPreservesSourceProjectionsFrozenBytesAndEpoch(t *testing.T) {
	path, old := barcodeRootTwentyFixture(t)
	value := "\x00retained 001 é\r\n"
	for _, row := range []struct{ id, owner, symbology string }{{"legacy-a", "item-1", "EAN_13"}, {"legacy-b", "asset-item", "EAN_13"}, {"legacy-c", "asset-item", "OTHER"}} {
		execSQL(t, old.db, "INSERT INTO item_barcodes VALUES(?,?,?,'old raw',?,'original-created')", row.id, row.owner, row.symbology, value)
		seedCatalogProjection(t, old, "barcode", row.id, BarcodeData{ID: row.id, ItemID: row.owner, Symbology: row.symbology, RawValue: "old raw", NormalizedValue: value})
	}
	execSQL(t, old.db, `UPDATE current_projections SET payload_json=' { "retained_barcode_bytes" : true } ',updated_at='exact-original' WHERE entity_type='barcode'`)
	execSQL(t, old.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type='barcode'`)
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 20 || s.Report().To < 21 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("barcode index migration omitted recovery or changed the dataset epoch")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) {
		t.Fatal("barcode index migration rewrote current projection bytes")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-migration recovery snapshot failed verification", err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 20 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='idx_barcode_value_id'") != 0 {
		t.Fatal("recovery snapshot did not preserve the actual old schema")
	}
	for _, plan := range []struct {
		query, index string
		args         []any
	}{
		{barcodeSelect + " WHERE b.normalized_value=? AND b.id>? ORDER BY b.id LIMIT ?", "idx_barcode_value_id", []any{value, "", 51}},
		{barcodeSelect + " WHERE b.normalized_value=? AND b.symbology=? AND b.id>? ORDER BY b.id LIMIT ?", "idx_barcode_lookup", []any{value, "EAN_13", "", 51}},
	} {
		rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+plan.query, plan.args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(details, " ")
		if !strings.Contains(joined, plan.index) || strings.Contains(joined, "USE TEMP B-TREE") {
			t.Fatal("actual bounded exact lookup did not use its ordered index", joined)
		}
	}
}

func TestBarcodeLookupMigrationRollsBackIndexReplacementAndLedgerOnLateFailure(t *testing.T) {
	path, old := barcodeRootTwentyFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER fail_barcode_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=21 BEGIN SELECT RAISE(ABORT,'barcode index ledger failure'); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed ledger insertion committed barcode indexes")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed index upgrade lost verified recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 20 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 20 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='idx_barcode_value_id'") != 0 {
		t.Fatal("failed index replacement leaked DDL, source or migration history", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed index replacement did not retain the verified original snapshot", err)
	}
}
