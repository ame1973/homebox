package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func rootTwelveFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := rootElevenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[11]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

const purchaseOriginalBytesTest = `SELECT json_group_array(json_array(id,merchant_name,purchased_at,currency,receipt_note,created_at)) FROM (SELECT * FROM purchases ORDER BY id)`
const purchaseLineOriginalBytesTest = `SELECT json_group_array(json_array(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note)) FROM (SELECT * FROM purchase_lines ORDER BY id)`

func seedPurchaseUpgradeSource(t *testing.T, db *sql.DB) {
	t.Helper()
	execSQL(t, db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES('retained-purchase','Merchant','2026-11-06T12:00:00.123456789+08:00','JPY',?,'unknown-legacy-created')", "\x00Exact receipt é\n")
	execSQL(t, db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,note) VALUES('retained-line','retained-purchase','item-1',1500,0,?)", "\x00Exact allocation é\n")
}

func TestPurchaseBackfillPreservesExactLegacyValuesZerosCountersAndSourceOnlyPublication(t *testing.T) {
	path, old := legacyFixture(t, 2)
	seedPurchaseUpgradeSource(t, old)
	id := strings.Repeat("界", 124) + "<&\" "
	lineID := strings.Repeat("\x00", 128)
	execSQL(t, old, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,'TWD',?,'legacy-created')", id, strings.Repeat("😀", 300), "2026-11-06T01:02:03.123456789Z", "\x00"+strings.Repeat("😀", 9999))
	execSQL(t, old, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,note) VALUES(?,?,'item-1',?,?,?)", lineID, id, maxSafeInteger, maxSafeInteger, "\x00"+strings.Repeat("界", 9999))
	execSQL(t, old, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Physical','individual','2026','2026')")
	execSQL(t, old, "INSERT INTO item_units(id,item_id,asset_code,status,created_at,updated_at) VALUES('old-unit','individual','AST','retired','2026','2026')")
	execSQL(t, old, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('legacy-bulk-unit','retained-purchase','individual','old-unit',2,0)")
	for _, counter := range []struct {
		kind, id string
		revision int64
	}{
		{"purchase", id, maxSafeInteger}, {"purchase_line", lineID, 7}, {"purchase", "gone-header", 9}, {"purchase_line", "gone-line", 11},
	} {
		execSQL(t, old, "INSERT INTO projection_revisions VALUES(?,?,?)", counter.kind, counter.id, counter.revision)
	}
	execSQL(t, old, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('item-1','UnverifiedCache','NeverPublishPurchaseOCR',991,'2026')")
	beforePurchase, beforeLine := scalar[string](t, old, purchaseOriginalBytesTest), scalar[string](t, old, purchaseLineOriginalBytesTest)
	epoch := scalar[string](t, old, "SELECT dataset_epoch FROM sync_state")
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if scalar[string](t, s.db, purchaseOriginalBytesTest) != beforePurchase || scalar[string](t, s.db, purchaseLineOriginalBytesTest) != beforeLine {
		t.Fatal("backfill repaired exact source or rescaled financial/physical counts")
	}
	if scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") == epoch || !s.Report().LegacyAdopted {
		t.Fatal("explicit reference adoption did not retain its existing epoch policy")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines WHERE created_at IS NULL AND mutation_event_id IS NULL") != 3 {
		t.Fatal("migration guessed old allocation time/audit association")
	}
	purchase, err := loadPurchase(t.Context(), s.db, id)
	if err != nil || purchase.Revision != maxSafeInteger || purchase.MerchantName != strings.Repeat("😀", 300) {
		t.Fatal("exact header source/counter lost", err)
	}
	line, err := loadPurchaseLine(t.Context(), s.db, lineID)
	if err != nil || line.Revision != 7 || line.QuantityBase != maxSafeInteger || line.PaidMinor != maxSafeInteger || line.ItemUnitID != nil {
		t.Fatal("safe retained allocation lost", err)
	}
	legacy, err := loadPurchaseLine(t.Context(), s.db, "legacy-bulk-unit")
	if err != nil || legacy.QuantityBase != 2 || legacy.PaidMinor != 0 || legacy.ItemUnitID == nil {
		t.Fatal("retained representation was rewritten to new-input policy", err)
	}
	for _, row := range []struct {
		kind, id string
		payload  any
	}{
		{"purchase", id, purchase.PurchaseData}, {"purchase_line", lineID, line.PurchaseLineData}, {"purchase_line", legacy.ID, legacy.PurchaseLineData},
	} {
		encoded, err := json.Marshal(row.payload)
		var expected, actual any
		if err != nil || json.Unmarshal(encoded, &expected) != nil || json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id)), &actual) != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("backfill payload is not exact version-one source", row.kind, err)
		}
		if !validPurchaseInstant(scalar[string](t, s.db, "SELECT updated_at FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id)) {
			t.Fatal("materialization time was guessed from a legacy creation string")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('purchase','purchase_line')") != 5 || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_id IN('gone-header','gone-line')") != 0 || scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id='gone-line'") != 11 {
		t.Fatal("counter-only financial identities resurrected or reset")
	}
	for _, table := range []string{"sync_changes", "mutation_events", "api_idempotency", "inventory_events"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 0 {
			t.Fatal("backfill invented a new event/receipt/movement", table)
		}
	}
	if scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('item','search_doc')") != 0 || scalar[string](t, s.db, "SELECT reviewed_ai FROM search_documents") != "NeverPublishPurchaseOCR" || scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances WHERE item_id='item-1'") != 1500 {
		t.Fatal("backfill published unreviewed cache, inferred spend/receipt or changed unrelated counters")
	}
	for _, plan := range []struct{ query, index string }{
		{"SELECT id FROM purchase_lines WHERE purchase_id='retained-purchase' AND id>'a' ORDER BY id LIMIT 51", "idx_purchase_lines_purchase_id"},
		{"SELECT DISTINCT purchase_id FROM purchase_lines WHERE item_id='item-1' AND purchase_id>'a' ORDER BY purchase_id LIMIT 51", "idx_purchase_lines_item_purchase"},
	} {
		var a, b, c int
		var detail string
		if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+plan.query).Scan(&a, &b, &c, &detail); err != nil || !strings.Contains(detail, plan.index) {
			t.Fatal("financial owner lookup lacks its real index", detail, err)
		}
	}
	if scalar[int](t, s.db, `SELECT "unique" FROM pragma_index_list('purchase_lines') WHERE name='idx_purchase_lines_mutation'`) != 0 {
		t.Fatal("one audit event cannot associate multiple allocations")
	}
}

func TestPurchaseCertifiedUpgradeKeepsFrozenBytesEpochAndExistingSourceWithoutInventedHistory(t *testing.T) {
	path, old := rootTwelveFixture(t)
	seedPurchaseUpgradeSource(t, old.db)
	seedCatalogProjection(t, old, "purchase", "retained-purchase", PurchaseData{ID: "retained-purchase", MerchantName: "Merchant", PurchasedAt: "2026-11-06T12:00:00.123456789+08:00", Currency: "JPY", ReceiptNote: "\x00Exact receipt é\n"})
	seedCatalogProjection(t, old, "purchase_line", "retained-line", PurchaseLineData{ID: "retained-line", PurchaseID: "retained-purchase", ItemID: "item-1", QuantityBase: 1500, PaidMinor: 0, Note: "\x00Exact allocation é\n"})
	setCatalogProjectionRevision(t, old, "purchase", "retained-purchase", 7)
	setCatalogProjectionRevision(t, old, "purchase_line", "retained-line", 11)
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"RetainedExactBytes\":true} ',updated_at='retained-time' WHERE entity_type IN('purchase','purchase_line')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'retained-time' FROM current_projections WHERE entity_type IN('purchase','purchase_line')")
	before, projections := domainState(t, old), currentProjectionState(t, old)
	purchases, lines := scalar[string](t, old.db, purchaseOriginalBytesTest), scalar[string](t, old.db, purchaseLineOriginalBytesTest)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 12 || s.Report().To < 13 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch || scalar[string](t, s.db, purchaseOriginalBytesTest) != purchases || scalar[string](t, s.db, purchaseLineOriginalBytesTest) != lines {
		t.Fatal("certified upgrade changed source/epoch or skipped private recovery snapshot")
	}
	// Later additive backfills may add unrelated kinds. Preserve every original
	// identity/counter/byte rather than forbidding future materialized domains.
	current := currentProjectionState(t, s)
	for key, retained := range projections {
		if current[key] != retained {
			t.Fatal("existing current payload bytes/counters/timestamps were rewritten", key)
		}
	}
	if value, err := loadPurchase(t.Context(), s.db, "retained-purchase"); err != nil || value.Revision != 7 {
		t.Fatal("retained Purchase counter was reset or needlessly advanced", err)
	}
	after := domainState(t, s)
	for table, value := range before {
		if table != "purchase_lines" && table != "current_projections" && table != "projection_revisions" && after[table] != value {
			t.Fatal("backfill changed existing unrelated source/frozen state", table)
		}
	}
	if value, err := loadPurchaseLine(t.Context(), s.db, "retained-line"); err != nil || value.PaidMinor != 0 || value.Revision != 11 || value.Note != "\x00Exact allocation é\n" {
		t.Fatal("loader copied retained cache instead of real source", err)
	}
	for _, statement := range []string{"UPDATE purchase_lines SET paid_minor=1", "UPDATE purchase_lines SET note='changed'", "DELETE FROM purchase_lines", "DELETE FROM purchases"} {
		if _, err = s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("posted financial history remained destructively mutable", statement)
		}
	}
}

func TestPurchaseInvalidLegacySourceOrCounterRollsBackEveryPendingMigrationAndChecksum(t *testing.T) {
	for _, invalid := range []struct{ name, query string }{
		{"empty header identity", "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES('','M','2026-11-06T00:00:00Z','JPY','2026')"},
		{"native NUL identity bound", "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES(char(0)||printf('%0128d',0),'M','2026-11-06T00:00:00Z','JPY','2026')"},
		{"header UTF", "UPDATE purchases SET merchant_name=CAST(x'ff' AS TEXT)"},
		{"empty merchant", "UPDATE purchases SET merchant_name=''"},
		{"wide merchant", "UPDATE purchases SET merchant_name=printf('%0301d',0)"},
		{"native NUL merchant bound", "UPDATE purchases SET merchant_name=char(0)||printf('%0300d',0)"},
		{"receipt UTF", "UPDATE purchases SET receipt_note=CAST(x'ff' AS TEXT)"},
		{"native NUL receipt bound", "UPDATE purchases SET receipt_note=char(0)||printf('%010000d',0)"},
		{"guessed date", "UPDATE purchases SET purchased_at='2026-11-06'"},
		{"fraction truncation", "UPDATE purchases SET purchased_at='2026-11-06T00:00:00.1234567890Z'"},
		{"UTC underflow", "UPDATE purchases SET purchased_at='0001-01-01T00:00:00+00:01'"},
		{"currency spelling", "UPDATE purchases SET currency='jpy'"},
		{"currency Unicode", "UPDATE purchases SET currency='日本円'"},
		{"line identity UTF", "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES(CAST(x'ff' AS TEXT),'retained-purchase','item-1',1,0)"},
		{"native NUL line identity bound", "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES(char(0)||printf('%0128d',0),'retained-purchase','item-1',1,0)"},
		{"unsafe base quantity", "UPDATE purchase_lines SET quantity_base=9007199254740992"},
		{"unsafe minor amount", "UPDATE purchase_lines SET paid_minor=9007199254740992"},
		{"allocation note UTF", "UPDATE purchase_lines SET note=CAST(x'ff' AS TEXT)"},
		{"native NUL allocation note bound", "UPDATE purchase_lines SET note=char(0)||printf('%010000d',0)"},
		{"unsafe header counter", "INSERT INTO projection_revisions VALUES('purchase','gone',9007199254740992)"},
		{"unsafe line counter", "INSERT INTO projection_revisions VALUES('purchase_line','gone',9007199254740992)"},
		{"counter identity UTF", "INSERT INTO projection_revisions VALUES('purchase_line',CAST(x'ff' AS TEXT),1)"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			path, old := legacyFixture(t, 2)
			seedPurchaseUpgradeSource(t, old)
			execSQL(t, old, invalid.query)
			beforePurchase, beforeLine := scalar[string](t, old, purchaseOriginalBytesTest), scalar[string](t, old, purchaseLineOriginalBytesTest)
			_ = old.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
				_ = s.Close()
				t.Fatal("invalid financial source/counter certified")
			}
			unchanged := rawDatabase(t, path)
			if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 2 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('current_projections','schema_migration_checksums','idx_purchase_lines_purchase_id','purchase_lines_no_update')") != 0 || scalar[string](t, unchanged, purchaseOriginalBytesTest) != beforePurchase || scalar[string](t, unchanged, purchaseLineOriginalBytesTest) != beforeLine {
				t.Fatal("failed migration changed source or leaked any pending DDL/checksum/materialization")
			}
		})
	}
}

func TestPurchaseCertifiedValidationAndLateLedgerFailuresPreserveAllOriginalState(t *testing.T) {
	for _, failure := range []string{"source bound", "missing Item counter", "ledger"} {
		t.Run(failure, func(t *testing.T) {
			path, old := rootTwelveFixture(t)
			seedPurchaseUpgradeSource(t, old.db)
			if failure == "source bound" {
				execSQL(t, old.db, "UPDATE purchases SET receipt_note=char(0)||printf('%010000d',0)")
			}
			if failure == "missing Item counter" {
				execSQL(t, old.db, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id='item-1'")
			}
			if failure == "ledger" {
				execSQL(t, old.db, "CREATE TRIGGER fail_purchase_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=13 BEGIN SELECT RAISE(ABORT,'purchase ledger failure'); END")
			}
			before := domainState(t, old)
			_ = old.db.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = s.Close()
				t.Fatal("failed purchase upgrade committed")
			} else if failure != "ledger" && !errors.Is(err, ErrDomainState) || !strings.Contains(err.Error(), "verified recovery snapshot retained") {
				t.Fatal("failed upgrade lost domain error/recovery evidence", err)
			}
			unchanged := rawDatabase(t, path)
			requireDomainState(t, &Store{db: unchanged}, before)
			if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 12 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 12 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM pragma_table_info('purchase_lines') WHERE name IN('created_at','mutation_event_id')") != 0 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('idx_purchase_lines_purchase_id','purchase_lines_no_update')") != 0 {
				t.Fatal("late/Go validation failure leaked financial columns/indexes/immutability/ledger")
			}
		})
	}
}

func TestPurchaseBackfillQueryFailuresAndCancellationNeverCertifyPartialAuthority(t *testing.T) {
	for _, stage := range []string{"counters", "headers", "lines", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			s := openTestStore(t)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if stage != "canceled" {
				table := map[string]string{"counters": "projection_revisions", "headers": "purchases", "lines": "purchase_lines"}[stage]
				if _, err = tx.ExecContext(t.Context(), "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if stage == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if err = validatePurchasesBackfill(ctx, tx); err == nil {
				t.Fatal("failed/canceled financial authority scan certified")
			}
		})
	}
}
