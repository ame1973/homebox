package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func assetRootFourteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := purchaseRootThirteenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[13]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

const assetOriginalBytesTest = `SELECT json_group_array(json_array(item_unit_id,in_service_at,retired_at,cost_basis,manual_acquisition_minor,currency,estimated_residual_minor,usage_unit,created_at,updated_at)) FROM (SELECT * FROM asset_profiles ORDER BY item_unit_id)`
const usageOriginalBytesTest = `SELECT json_group_array(json_array(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,idempotency_key,created_at)) FROM (SELECT * FROM asset_usage_events ORDER BY id)`
const costOriginalBytesTest = `SELECT json_group_array(json_array(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,idempotency_key,created_at)) FROM (SELECT * FROM asset_cost_events ORDER BY id)`
const disposalOriginalBytesTest = `SELECT json_group_array(json_array(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at)) FROM (SELECT * FROM asset_disposals ORDER BY item_unit_id)`

func seedAssetUpgradeSource(t *testing.T, db *sql.DB) {
	t.Helper()
	execSQL(t, db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('asset-item','Retained Asset','individual','2026','2026')")
	execSQL(t, db, "INSERT INTO item_units(id,item_id,asset_code,status,created_at,updated_at) VALUES('asset-unit','asset-item','AST-RETAINED','disposed','2026','2026')")
	execSQL(t, db, `INSERT INTO asset_profiles(item_unit_id,in_service_at,retired_at,cost_basis,manual_acquisition_minor,currency,estimated_residual_minor,usage_unit,created_at,updated_at)
 VALUES('asset-unit','2024-11-07T12:00:00.123456789+08:00','2026-11-07T12:00:00.123456789+08:00','manual',0,'ZZZ',NULL,'times','unknown-legacy-created','unknown-legacy-updated')`)
	execSQL(t, db, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,idempotency_key,created_at)
 VALUES('asset-use','asset-unit','2026-11-07T12:00:00.123456789+08:00',250,'times','correction',?,'legacy-use-key','unknown-use-created')`, "\x00Exact retained usage é\n")
	execSQL(t, db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,idempotency_key,created_at)
 VALUES('asset-cost','asset-unit','2026-11-07T12:00:00.123456789+08:00','adjustment',5000,'credit','ZZZ',?,'legacy-cost-key','unknown-cost-created')`, "\x00Exact retained cost é\n")
	execSQL(t, db, `INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at)
 VALUES('asset-unit','2026-11-07T12:00:00.123456789+08:00','donated',17,'ZZZ',?,'unknown-disposal-created')`, "\x00Exact retained disposal é\n")
}

func TestAssetBackfillPreservesExactLegacySourceIndependentCountersUnknownProvenanceAndZeros(t *testing.T) {
	path, old := legacyFixture(t, 2)
	seedAssetUpgradeSource(t, old)
	id, eventID := strings.Repeat("界", 124)+"<&\" ", strings.Repeat("\x00", 128)
	execSQL(t, old, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,'asset-item','MAX-ASSET','2026','2026')", id)
	execSQL(t, old, "INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,estimated_residual_minor,created_at,updated_at) VALUES(?,'manual',?,'JPY',?,'legacy','legacy')", id, maxSafeInteger, maxSafeInteger)
	execSQL(t, old, "INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at) VALUES(?,?,'2026-11-07T00:00:00Z',?,'times','correction',?,'legacy')", eventID, id, -maxSafeInteger, "\x00"+strings.Repeat("😀", 9999))
	execSQL(t, old, "INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,created_at) VALUES(?,?,'2026-11-07T00:00:00Z','repair',?,'expense','JPY',?,'legacy')", id, id, maxSafeInteger, "\x00"+strings.Repeat("界", 9999))
	for _, row := range []struct {
		kind, id string
		revision int64
	}{{"asset", id, maxSafeInteger}, {"usage", eventID, 7}, {"cost", id, 11}, {"disposal", "asset-unit", 13},
		{"asset", "gone-asset", 3}, {"usage", "gone-usage", 5}, {"cost", "gone-cost", 7}, {"disposal", "gone-disposal", 9}} {
		execSQL(t, old, "INSERT INTO projection_revisions VALUES(?,?,?)", row.kind, row.id, row.revision)
	}
	execSQL(t, old, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('asset-item','UnverifiedAssetCache','NeverPublishAssetOCR',999,'2026')")
	queries := []string{assetOriginalBytesTest, usageOriginalBytesTest, costOriginalBytesTest, disposalOriginalBytesTest}
	var before []string
	for _, query := range queries {
		before = append(before, scalar[string](t, old, query))
	}
	epoch := scalar[string](t, old, "SELECT dataset_epoch FROM sync_state")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, query := range queries {
		if scalar[string](t, s.db, query) != before[i] {
			t.Fatal("Asset upgrade changed exact original fields/raw keys/creation spellings", i)
		}
	}
	if !s.Report().LegacyAdopted || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") == epoch {
		t.Fatal("explicit reference adoption lost its separate epoch policy")
	}
	asset, err := loadAsset(t.Context(), s.db, id)
	if err != nil || asset.Revision != maxSafeInteger || asset.ManualAcquisitionMinor == nil || *asset.ManualAcquisitionMinor != maxSafeInteger || asset.EstimatedResidualMinor == nil {
		t.Fatal("independent Asset/null/amount interpretation was lost", err)
	}
	usage, err := loadUsage(t.Context(), s.db, eventID)
	if err != nil || usage.Revision != 7 || usage.Delta != -maxSafeInteger || usage.CorrectionOf != nil {
		t.Fatal("retained usage was relabeled to new-input policy or given fictional provenance", err)
	}
	cost, err := loadCost(t.Context(), s.db, id)
	if err != nil || cost.Revision != 11 || cost.AmountMinor != maxSafeInteger {
		t.Fatal("safe retained cost was repriced or reseeded", err)
	}
	disposal, err := loadDisposal(t.Context(), s.db, "asset-unit")
	if err != nil || disposal.Revision != 13 || disposal.Kind != "donated" || disposal.RealizedProceedsMinor != 17 || disposal.Currency != "ZZZ" {
		t.Fatal("historical disposal was silently rewritten to today's eligibility", err)
	}
	for _, row := range []struct {
		kind, id string
		payload  any
	}{{"asset", id, asset.AssetData}, {"usage", eventID, usage.UsageData}, {"cost", id, cost.CostData}, {"disposal", "asset-unit", disposal.DisposalData}} {
		encoded, err := json.Marshal(row.payload)
		var expected, actual any
		if err != nil || json.Unmarshal(encoded, &expected) != nil || json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id)), &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("financial backfill did not materialize exact source-only offline data", row.kind, err)
		}
		if !validPurchaseInstant(scalar[string](t, s.db, "SELECT updated_at FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id)) {
			t.Fatal("materialization time was guessed from unknown historical creation")
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, "gone-"+row.kind) != 0 {
			t.Fatal("counter-only financial identity was resurrected")
		}
	}
	for _, table := range []string{"asset_usage_events", "asset_cost_events", "asset_disposals"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table+" WHERE mutation_event_id IS NOT NULL") != 0 {
			t.Fatal("migration invented old financial audit associations", table)
		}
	}
	for _, table := range []string{"sync_changes", "mutation_events", "api_idempotency", "inventory_events"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 0 {
			t.Fatal("backfill invented live events/receipts/movements", table)
		}
	}
	if scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision") != 0 || scalar[string](t, s.db, "SELECT reviewed_ai FROM search_documents") != "NeverPublishAssetOCR" || scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 {
		t.Fatal("backfill changed unrelated authority or published unreviewed cache")
	}
	for _, plan := range []struct{ query, index string }{
		{"SELECT id FROM asset_usage_events WHERE item_unit_id='asset-unit' AND id>'a' ORDER BY id LIMIT 51", "idx_usage_unit_id"},
		{"SELECT id FROM asset_cost_events WHERE item_unit_id='asset-unit' AND id>'a' ORDER BY id LIMIT 51", "idx_costs_unit_id"},
	} {
		var a, b, c int
		var detail string
		if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+plan.query).Scan(&a, &b, &c, &detail); err != nil || !strings.Contains(detail, plan.index) {
			t.Fatal("owned financial paging lacks its actual stable-ID index", detail, err)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE '%_v15'") != 0 {
		t.Fatal("temporary financial rebuild tables leaked into the completed schema")
	}
}

func TestAssetCertifiedUpgradeRetainsEveryOriginalCurrentFrozenSourceByteAndEpoch(t *testing.T) {
	path, old := assetRootFourteenFixture(t)
	seedAssetUpgradeSource(t, old.db)
	execSQL(t, old.db, "INSERT INTO projection_revisions VALUES('item','asset-item',3),('unit','asset-unit',5)")
	for _, row := range []projectionKey{{"asset", "asset-unit"}, {"usage", "asset-use"}, {"cost", "asset-cost"}, {"disposal", "asset-unit"}} {
		seedCatalogProjection(t, old, row.kind, row.id, map[string]string{"RetainedFinancialBytes": row.kind})
		setCatalogProjectionRevision(t, old, row.kind, row.id, 7)
	}
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"RetainedExactAssetBytes\":true} ',updated_at='retained-asset-time' WHERE entity_type IN('asset','usage','cost','disposal')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('asset','usage','cost','disposal')")
	before, current := domainState(t, old), currentProjectionState(t, old)
	var source []string
	for _, query := range []string{assetOriginalBytesTest, usageOriginalBytesTest, costOriginalBytesTest, disposalOriginalBytesTest} {
		source = append(source, scalar[string](t, old.db, query))
	}
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 14 || s.Report().To < 15 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("certified Asset upgrade changed epoch or skipped verified recovery")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-upgrade financial snapshot failed independent verification", err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 14 || scalar[int](t, backup, "SELECT COUNT(*) FROM pragma_table_info('asset_usage_events') WHERE name='correction_of'") != 0 {
		t.Fatal("recovery snapshot was not the original schema/source")
	}
	for i, query := range []string{assetOriginalBytesTest, usageOriginalBytesTest, costOriginalBytesTest, disposalOriginalBytesTest} {
		if scalar[string](t, s.db, query) != source[i] {
			t.Fatal("certified upgrade changed original financial values", i)
		}
	}
	after := domainState(t, s)
	for table, value := range before {
		if table != "asset_usage_events" && table != "asset_cost_events" && table != "asset_disposals" && table != "current_projections" && table != "projection_revisions" && after[table] != value {
			t.Fatal("Asset rebuild changed unrelated/original source or frozen history", table)
		}
	}
	materialized := currentProjectionState(t, s)
	for key, value := range current {
		if materialized[key] != value {
			t.Fatal("Asset rebuild rewrote prior current bytes/counters/timestamps", key)
		}
	}
	if value, err := loadAsset(t.Context(), s.db, "asset-unit"); err != nil || value.Revision != 7 || value.ManualAcquisitionMinor == nil || *value.ManualAcquisitionMinor != 0 {
		t.Fatal("loader used opaque retained cache instead of real zero-valued source", err)
	}
	for _, statement := range []string{"UPDATE asset_usage_events SET delta=delta", "DELETE FROM asset_usage_events", "UPDATE asset_cost_events SET note=note", "DELETE FROM asset_cost_events", "UPDATE asset_disposals SET note=note", "DELETE FROM asset_disposals", "DELETE FROM asset_profiles", "UPDATE asset_profiles SET item_unit_id='missing'"} {
		if _, err = s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("retained financial ownership/history remained destructively mutable", statement)
		}
	}
	for _, table := range []string{"asset_usage_events", "asset_cost_events", "asset_disposals"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM pragma_foreign_key_list('"+table+"') WHERE \"table\"='asset_profiles'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM pragma_foreign_key_list('"+table+"') WHERE \"table\"='mutation_events'") != 1 {
			t.Fatal("rebuild dropped or redirected financial ownership/audit FKs", table)
		}
	}
	execSQL(t, s.db, "UPDATE asset_profiles SET in_service_at='2026-11-07T12:00:00+08:00',retired_at='2026-11-07T05:00:00Z' WHERE item_unit_id='asset-unit'")
	if _, err = loadAsset(t.Context(), s.db, "asset-unit"); err != nil {
		t.Fatal("rebuilt profile retained the invalid lexical-offset date CHECK", err)
	}
}

func TestAssetInvalidLegacySourceOrRetainedCounterRollsBackAllPendingDDLAndChecksums(t *testing.T) {
	for _, invalid := range []struct{ name, statement string }{
		{"profile currency", "UPDATE asset_profiles SET currency='jpy'"},
		{"profile amount", "UPDATE asset_profiles SET manual_acquisition_minor=9007199254740992"},
		{"profile residual", "UPDATE asset_profiles SET estimated_residual_minor=9007199254740992"},
		{"invalid instant", "UPDATE asset_profiles SET in_service_at='2024'"},
		{"fraction truncation", "UPDATE asset_profiles SET in_service_at='2024-11-07T00:00:00.1234567890Z'"},
		{"UTC underflow", "UPDATE asset_profiles SET in_service_at='0001-01-01T00:00:00+00:01'"},
		{"chronological interval", "UPDATE asset_profiles SET in_service_at='2026-11-07T04:00:00Z',retired_at='2026-11-07T12:00:00+09:00'"},
		{"usage identity", "UPDATE asset_usage_events SET id=char(0)||printf('%0128d',0)"},
		{"usage UTF", "UPDATE asset_usage_events SET note=CAST(x'ff' AS TEXT)"},
		{"usage NUL bound", "UPDATE asset_usage_events SET note=char(0)||printf('%010000d',0)"},
		{"usage quantity", "UPDATE asset_usage_events SET delta=-9007199254740992"},
		{"usage date", "UPDATE asset_usage_events SET occurred_at='2026-02-30T00:00:00Z'"},
		{"cost identity UTF", "UPDATE asset_cost_events SET id=CAST(x'ff' AS TEXT)"},
		{"cost amount", "UPDATE asset_cost_events SET amount_minor=9007199254740992"},
		{"cost note bound", "UPDATE asset_cost_events SET note=char(0)||printf('%010000d',0)"},
		{"cost currency", "UPDATE asset_cost_events SET currency='日本円'"},
		{"disposal amount", "UPDATE asset_disposals SET realized_proceeds_minor=9007199254740992"},
		{"disposal note UTF", "UPDATE asset_disposals SET note=CAST(x'ff' AS TEXT)"},
		{"disposal note bound", "UPDATE asset_disposals SET note=char(0)||printf('%010000d',0)"},
		{"disposal date", "UPDATE asset_disposals SET disposed_at='9999-12-31T23:59:59-00:01'"},
		{"asset counter", "INSERT INTO projection_revisions VALUES('asset','gone',9007199254740992)"},
		{"usage counter", "INSERT INTO projection_revisions VALUES('usage','gone',9007199254740992)"},
		{"cost counter", "INSERT INTO projection_revisions VALUES('cost','gone',9007199254740992)"},
		{"disposal identity", "INSERT INTO projection_revisions VALUES('disposal',char(0)||printf('%0128d',0),1)"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			path, old := legacyFixture(t, 2)
			seedAssetUpgradeSource(t, old)
			execSQL(t, old, invalid.statement)
			queries := []string{assetOriginalBytesTest, usageOriginalBytesTest, costOriginalBytesTest, disposalOriginalBytesTest,
				"SELECT json_group_array(json_array(entity_type,entity_id,revision)) FROM (SELECT * FROM projection_revisions ORDER BY entity_type,entity_id)"}
			var before []string
			for _, query := range queries {
				before = append(before, scalar[string](t, old, query))
			}
			_ = old.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
				_ = s.Close()
				t.Fatal("invalid financial source was certified")
			}
			unchanged := rawDatabase(t, path)
			for i, query := range queries {
				if scalar[string](t, unchanged, query) != before[i] {
					t.Fatal("failed migration repaired original financial bytes/counters", i)
				}
			}
			if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 2 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('schema_migration_checksums','current_projections','idx_usage_unit_id','asset_profiles_v15','asset_usage_events_no_update')") != 0 {
				t.Fatal("failed financial upgrade leaked DDL/ledger/projections")
			}
		})
	}
}

func TestAssetCertifiedValidationAndLateLedgerFailuresPreserveEveryOriginalTableAndRecoverySnapshot(t *testing.T) {
	for _, failure := range []string{"missing Unit counter", "missing Item counter", "source validation", "ledger"} {
		t.Run(failure, func(t *testing.T) {
			path, old := assetRootFourteenFixture(t)
			seedAssetUpgradeSource(t, old.db)
			if failure != "missing Unit counter" {
				execSQL(t, old.db, "INSERT INTO projection_revisions VALUES('unit','asset-unit',1)")
			}
			if failure != "missing Item counter" {
				execSQL(t, old.db, "INSERT INTO projection_revisions VALUES('item','asset-item',1)")
			}
			if failure == "source validation" {
				execSQL(t, old.db, "UPDATE asset_usage_events SET note=char(0)||printf('%010000d',0)")
			}
			if failure == "ledger" {
				execSQL(t, old.db, "CREATE TRIGGER fail_asset_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=15 BEGIN SELECT RAISE(ABORT,'Asset ledger failure'); END")
			}
			before := domainState(t, old)
			ddl, err := schemaDescription(t.Context(), old.db)
			if err != nil {
				t.Fatal(err)
			}
			_ = old.db.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = s.Close()
				t.Fatal("failed Asset validation/ledger committed")
			} else if failure != "ledger" && !errors.Is(err, ErrDomainState) || !strings.Contains(err.Error(), "verified recovery snapshot retained") {
				t.Fatal("failure lost source error or independently verified recovery evidence", err)
			}
			unchanged := rawDatabase(t, path)
			requireDomainState(t, &Store{db: unchanged}, before)
			actualDDL, err := schemaDescription(t.Context(), unchanged)
			if err != nil || !reflect.DeepEqual(actualDDL, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 14 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 14 {
				t.Fatal("failed group rebuild changed original DDL/indices/triggers/FKs/checksums", err)
			}
			backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
			if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
				t.Fatal("failed Asset upgrade lost its one verified original recovery snapshot", err)
			}
		})
	}
}

func TestAssetUpgradeRejectsOldInvalidReferencesBeforeRebuildingAnyFinancialTable(t *testing.T) {
	path, old := assetRootFourteenFixture(t)
	seedAssetUpgradeSource(t, old.db)
	execSQL(t, old.db, "PRAGMA foreign_keys=OFF")
	execSQL(t, old.db, "UPDATE asset_usage_events SET item_unit_id='missing-profile'")
	execSQL(t, old.db, "PRAGMA foreign_keys=ON")
	before := scalar[string](t, old.db, usageOriginalBytesTest)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("group rebuild laundered a preexisting invalid financial reference")
	}
	unchanged := rawDatabase(t, path)
	actualDDL, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(ddl, actualDDL) || scalar[string](t, unchanged, usageOriginalBytesTest) != before || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 14 {
		t.Fatal("failed integrity preflight repaired/deleted old invalid source or changed DDL", err)
	}
}

func TestAssetFinancialForeignKeysRawKeyUniquenessAndNonuniqueAuditAssociationsAreEnforced(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	before := domainState(t, s)
	for _, statement := range []string{
		"INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at,idempotency_key) VALUES('bad','unit',1,'times','use','2026-11-07T00:00:00Z','legacy','retained-raw-use-key')",
		"INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at,idempotency_key) VALUES('bad','unit',1,'JPY','credit','refund','2026-11-07T00:00:00Z','legacy','retained-raw-cost-key')",
		"INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at,correction_of) VALUES('bad','unit',1,'times','correction','2026-11-07T00:00:00Z','legacy','bad')",
		"INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at,correction_of) VALUES('bad','unit',1,'JPY','credit','adjustment','2026-11-07T00:00:00Z','legacy','bad')",
		"INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at,correction_of) VALUES('bad','unit',1,'times','correction','2026-11-07T00:00:00Z','legacy','missing-use')",
		"INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at,correction_of) VALUES('bad','unit',1,'JPY','credit','adjustment','2026-11-07T00:00:00Z','legacy','missing-cost')",
		"INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at,mutation_event_id) VALUES('bad','unit',1,'times','use','2026-11-07T00:00:00Z','2026-11-07T00:00:00Z','missing-audit')",
		"INSERT INTO asset_cost_events(id,item_unit_id,amount_minor,currency,direction,kind,occurred_at,created_at,mutation_event_id) VALUES('bad','unit',1,'JPY','expense','repair','2026-11-07T00:00:00Z','2026-11-07T00:00:00Z','missing-audit')",
		"INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,currency,created_at,mutation_event_id) VALUES('unit','2026-11-07T00:00:00Z','sold','JPY','2026-11-07T00:00:00Z','missing-audit')",
		"INSERT INTO asset_usage_events(id,item_unit_id,delta,usage_unit,event_type,occurred_at,created_at,mutation_event_id) VALUES('bad','unit',1,'times','use','2026-11-07T00:00:00Z','2026','missing-audit')",
	} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("financial source bypassed stable/raw-key/reference/audit constraints", statement)
		}
		requireDomainState(t, s, before)
	}
	for _, check := range []struct{ table, index string }{{"asset_usage_events", "idx_usage_mutation"}, {"asset_cost_events", "idx_costs_mutation"}, {"asset_disposals", "idx_disposals_mutation"}} {
		if scalar[int](t, s.db, "SELECT \"unique\" FROM pragma_index_list('"+check.table+"') WHERE name=?", check.index) != 0 {
			t.Fatal("one real audit cannot associate multiple financial rows", check.table)
		}
	}
	for _, table := range []string{"asset_usage_events", "asset_cost_events"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM pragma_foreign_key_list('"+table+"') WHERE \"from\"='correction_of' AND \"table\"=?", table) != 1 {
			t.Fatal("self-reference was left pointing at a temporary rebuild table", table)
		}
	}
}

func TestAssetBackfillQueryFailuresAndCancellationNeverCertifyPartialAuthority(t *testing.T) {
	for _, stage := range []string{"projection_revisions", "asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			s := openTestStore(t)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			ctx := t.Context()
			if stage == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if _, err = tx.ExecContext(ctx, "ALTER TABLE "+stage+" RENAME TO unavailable_asset_validation"); err != nil {
				t.Fatal(err)
			}
			if err = validateAssetsBackfill(ctx, tx); err == nil || stage == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("failed/canceled backfill was accepted", err)
			}
		})
	}
}
