package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"homebox/internal/barcode"
	"homebox/migrations"
)

func TestCatalogLinkBackfillPreservesIndependentSourceValuesCountersAndLookupPaths(t *testing.T) {
	path, old := legacyFixture(t, 2)
	id := strings.Repeat("界", 124) + "<&\" "
	execSQL(t, old, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('other','Other','individual','2026','2026')")
	for _, owner := range []string{"item-1", "other"} {
		execSQL(t, old, "INSERT INTO item_barcodes VALUES(?,?,'EAN_13',' 001234 ','retained-normalization','2026')", owner+"-barcode", owner)
		execSQL(t, old, "INSERT INTO item_aliases VALUES(?,?,'  Exact é  ','2026')", owner+"-alias", owner)
		execSQL(t, old, "INSERT INTO item_attributes VALUES(?,?,'Repeated','First','2026'),(?,?,'Repeated','','2026')", owner+"-attribute", owner, owner+"-second", owner)
	}
	execSQL(t, old, "INSERT INTO item_barcodes VALUES(?,'item-1','CODE_128',?,?,'2026')", id, "\x00GS1\x1d00123", "\x00GS1\x1d00123")
	for _, row := range []struct {
		kind, id string
		revision int64
	}{{"barcode", "item-1-barcode", 7}, {"alias", "item-1-alias", maxSafeInteger}, {"attribute", "item-1-attribute", 11}, {"barcode", "gone-barcode", 9}} {
		execSQL(t, old, "INSERT INTO projection_revisions VALUES(?,?,?)", row.kind, row.id, row.revision)
	}
	execSQL(t, old, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('item-1','PrivateCache','NeverPublishUnreviewedCache',8,'2026')")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	retained, err := loadBarcode(t.Context(), s.db, "item-1-barcode")
	if err != nil || retained.Revision != 7 || retained.RawValue != " 001234 " || retained.NormalizedValue != "retained-normalization" {
		t.Fatal("backfill reinterpreted historical barcode metadata or reset its counter", err)
	}
	if _, err := barcode.Normalize(retained.Symbology, retained.RawValue); !errors.Is(err, barcode.ErrInvalid) {
		t.Fatal("historical read fixture accidentally represents a valid new command")
	}
	alias, err := loadAlias(t.Context(), s.db, "item-1-alias")
	if err != nil || alias.Revision != maxSafeInteger || alias.Alias != "  Exact é  " {
		t.Fatal("alias source/counter was not retained", err)
	}
	attribute, err := loadAttribute(t.Context(), s.db, "item-1-second")
	if err != nil || attribute.AttributeValue != "" || attribute.Revision != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM item_attributes WHERE item_id='item-1' AND attribute_key='Repeated'") != 2 {
		t.Fatal("attribute identities or supplied-empty values were merged", err)
	}
	code, err := loadBarcode(t.Context(), s.db, id)
	if err != nil || code.RawValue != "\x00GS1\x1d00123" || code.ID != id {
		t.Fatal("backfill lost canonical Unicode identity or native Code128 controls", err)
	}
	for _, row := range []struct{ kind, id string }{{"barcode", "item-1-barcode"}, {"barcode", id}, {"alias", "item-1-alias"}, {"attribute", "item-1-second"}} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id)), &payload); err != nil || payload["id"] != row.id || payload["item_id"] != "item-1" {
			t.Fatal("backfill did not preserve independent owner/row payload identity", err)
		}
		if _, exists := payload["revision"]; exists {
			t.Fatal("offline source payload gained an inner revision")
		}
	}
	var controlPayload BarcodeData
	if err := json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='barcode' AND entity_id=?", id)), &controlPayload); err != nil || controlPayload != code.BarcodeData {
		t.Fatal("SQLite materialization truncated native controls or repaired Unicode source", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='barcode'") != 3 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_id='gone-barcode'") != 0 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_id='gone-barcode'") != 9 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('item','search_doc')") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 0 ||
		scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision") != 0 || scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 {
		t.Fatal("partial backfill changed unrelated source/history, fabricated publication, or resurrected an absent identity")
	}
	for _, lookup := range []struct{ table, index string }{{"item_barcodes", "idx_barcodes_item_id"}, {"item_aliases", "idx_aliases_item_id"}, {"item_attributes", "idx_attributes_item_id"}} {
		var number, parent, unused int
		var detail string
		err := s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM "+lookup.table+" WHERE item_id='item-1' AND id>'a' ORDER BY id LIMIT 51").Scan(&number, &parent, &unused, &detail)
		if err != nil || !strings.Contains(detail, lookup.index) {
			t.Fatal("owned stable-ID paging did not use its bounded lookup path", detail, err)
		}
	}
}

func rootEightFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, db := legacyFixture(t, 2)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Apply the historical root migrations to existing source before certifying
	// all eight checksums; versions above two are never legacy-adoption inputs.
	for _, migration := range all[2:8] {
		execSQL(t, db, migration.SQL)
	}
	execSQL(t, db, checksumDDL)
	for _, migration := range all[:8] {
		execSQL(t, db, "INSERT INTO schema_migrations(version,name,applied_at) SELECT ?,?,'2026' WHERE NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=?)", migration.Version, migration.Filename, migration.Version)
		execSQL(t, db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	}
	return path, &Store{db: db}
}

func TestCatalogLinkBackfillPreservesAlreadyMaterializedBytesAndRootEpoch(t *testing.T) {
	path, fixture := rootEightFixture(t)
	old := fixture.db
	execSQL(t, old, "INSERT INTO item_aliases VALUES('kept','item-1','Kept','2026')")
	seedCatalogProjection(t, fixture, "alias", "kept", AliasData{ID: "kept", ItemID: "item-1", Alias: "Kept"})
	execSQL(t, old, "UPDATE current_projections SET payload_json=?,updated_at='retained-time' WHERE entity_type='alias'", `{ "alias": "Kept", "item_id": "item-1", "id": "kept" }`)
	execSQL(t, old, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'2026' FROM current_projections WHERE entity_type='alias'")
	before := currentProjectionState(t, fixture)
	epoch := scalar[string](t, old, "SELECT dataset_epoch FROM sync_state")
	outbox := scalar[string](t, old, "SELECT projection_json FROM sync_changes")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Later additive backfills may add other domains; every previously frozen
	// identity must still preserve its exact payload bytes and retained revision.
	current := currentProjectionState(t, s)
	for key, retained := range before {
		if current[key] != retained {
			t.Fatal("root upgrade rewrote an already materialized identity", key)
		}
	}
	if scalar[string](t, s.db, "SELECT updated_at FROM current_projections WHERE entity_type='alias'") != "retained-time" ||
		scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch || scalar[string](t, s.db, "SELECT projection_json FROM sync_changes") != outbox ||
		s.Report().From != 8 || s.Report().To < 9 || scalar[int](t, s.db, "SELECT MAX(version) FROM schema_migrations") != s.Report().To {
		t.Fatal("root upgrade rewrote certified projection bytes, counter, frozen history or dataset epoch")
	}
}

func TestCatalogLinkRootUpgradeFailurePreservesSourceFrozenBytesAndCertifiedHistory(t *testing.T) {
	path, fixture := rootEightFixture(t)
	execSQL(t, fixture.db, "INSERT INTO item_aliases VALUES('kept','item-1','Kept','2026')")
	seedCatalogProjection(t, fixture, "alias", "kept", AliasData{ID: "kept", ItemID: "item-1", Alias: "Kept"})
	execSQL(t, fixture.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'2026' FROM current_projections WHERE entity_type='alias'")
	execSQL(t, fixture.db, "INSERT INTO item_barcodes VALUES('pending','item-1','CODE_128','00123','00123','2026')")
	execSQL(t, fixture.db, "INSERT INTO item_attributes VALUES('invalid','item-1','Key',char(0)||printf('%010000d',0),'2026')")
	before := domainState(t, fixture)
	_ = fixture.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); !errors.Is(err, ErrDomainState) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("an invalid root upgrade did not fail inside transactional source validation", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 8 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 8 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('idx_barcodes_item_id','idx_aliases_item_id','idx_attributes_item_id')") != 0 {
		t.Fatal("failed root upgrade published a new schema/checksum/index prefix")
	}
}

func TestCatalogLinkUnsafeBackfillRollsBackSourceLedgerAndAllPendingDDL(t *testing.T) {
	for _, invalid := range []struct{ name, insert string }{
		{"counter overflow", "INSERT INTO item_aliases VALUES('bad','item-1','Kept','2026'); INSERT INTO projection_revisions VALUES('alias','bad',9007199254740992)"},
		{"empty alias", "INSERT INTO item_aliases VALUES('bad','item-1','','2026')"},
		{"overlong alias", "INSERT INTO item_aliases VALUES('bad','item-1',printf('%0301d',0),'2026')"},
		{"invalid UTF-8", "INSERT INTO item_aliases VALUES('bad','item-1',CAST(x'ff' AS TEXT),'2026')"},
		{"unknown symbology", "INSERT INTO item_barcodes VALUES('bad','item-1','UNKNOWN','00123','00123','2026')"},
		{"empty normalized", "INSERT INTO item_barcodes VALUES('bad','item-1','OTHER','00123','','2026')"},
		{"empty row ID", "INSERT INTO item_aliases VALUES('','item-1','Kept','2026')"},
		{"overlong row ID", "INSERT INTO item_aliases VALUES(printf('%0129d',0),'item-1','Kept','2026')"},
		{"overlong owner ID", "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(printf('%0129d',0),'Owner','individual','2026','2026'); INSERT INTO item_barcodes VALUES('bad',printf('%0129d',0),'OTHER','00123','00123','2026')"},
		{"empty raw", "INSERT INTO item_barcodes VALUES('bad','item-1','OTHER','','00123','2026')"},
		{"overlong native controls", "INSERT INTO item_barcodes VALUES('bad','item-1','CODE_128',char(0)||printf('%0512d',0),'00123','2026')"},
		{"overlong normalized", "INSERT INTO item_barcodes VALUES('bad','item-1','OTHER','00123',printf('%0513d',0),'2026')"},
		{"invalid raw UTF-8", "INSERT INTO item_barcodes VALUES('bad','item-1','OTHER',CAST(x'ff' AS TEXT),'00123','2026')"},
		{"invalid normalized UTF-8", "INSERT INTO item_barcodes VALUES('bad','item-1','OTHER','00123',CAST(x'ff' AS TEXT),'2026')"},
		{"empty key", "INSERT INTO item_attributes VALUES('bad','item-1','','Value','2026')"},
		{"invalid value UTF-8", "INSERT INTO item_attributes VALUES('bad','item-1','Key',CAST(x'ff' AS TEXT),'2026')"},
		{"overlong key", "INSERT INTO item_attributes VALUES('bad','item-1',printf('%0101d',0),'','2026')"},
		{"overlong value", "INSERT INTO item_attributes VALUES('bad','item-1','Key',printf('%010001d',0),'2026')"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			path, old := legacyFixture(t, 2)
			execSQL(t, old, invalid.insert)
			_ = old.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
				_ = s.Close()
				t.Fatal("invalid owned data was certified by the migration")
			}
			unchanged := rawDatabase(t, path)
			if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 2 ||
				scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('current_projections','schema_migration_checksums','idx_barcodes_item_id','idx_aliases_item_id','idx_attributes_item_id')") != 0 ||
				scalar[int](t, unchanged, "SELECT quantity_base FROM stock_balances") != 1500 {
				t.Fatal("failed backfill committed a prefix, ledger, index or quantity change")
			}
			if scalar[int](t, unchanged, "SELECT (SELECT COUNT(*) FROM item_aliases)+(SELECT COUNT(*) FROM item_barcodes)+(SELECT COUNT(*) FROM item_attributes)") != 1 {
				t.Fatal("failed backfill lost the invalid source that requires explicit review")
			}
		})
	}
}
