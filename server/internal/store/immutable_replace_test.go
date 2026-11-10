package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// immutableFixture seeds retained rows under real Item, Unit, Purchase and audit
// authority. These SQL-level tests do not claim financial command eligibility.
func immutableFixture(t *testing.T) assetFixture {
	t.Helper()
	fixture := assetsFixture(t, true)
	db := fixture.store.db
	execSQL(t, db, `INSERT INTO location_events(id,location_id,occurred_at) VALUES('retained-location-event','source','legacy-time')`)
	execSQL(t, db, `INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at,idempotency_key) VALUES('retained-inventory-event',?,'receive',1,'legacy-time','retained-inventory-key')`, fixture.quantity.ID)
	rows, err := db.QueryContext(t.Context(), `SELECT id FROM mutation_events ORDER BY id LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		events = append(events, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("fixture needs two real mutation audit events")
	}
	execSQL(t, db, `INSERT INTO purchase_header_corrections VALUES(?,?,'{}')`, events[0], fixture.purchase.ID)
	execSQL(t, db, `INSERT INTO asset_profile_changes VALUES(?,?,'{}')`, events[0], fixture.unit.ID)
	execSQL(t, db, `INSERT INTO purchase_line_corrections VALUES(?,1,?,?,?,'replace','reason','2026-11-07T00:00:00Z','{}')`, events[0], fixture.purchase.ID, fixture.unitLine.ID, fixture.stockLine.ID)
	execSQL(t, db, `INSERT INTO asset_disposal_corrections VALUES(?,1,?,NULL,2,'void','reason','2026-11-07T00:00:00Z','{}')`, events[0], fixture.unit.ID)
	execSQL(t, db, `INSERT INTO asset_disposal_corrections VALUES(?,1,?,?,3,'void','reason','2026-11-07T00:00:00Z','{}')`, events[1], fixture.unit.ID, events[0])
	return fixture
}

// immutableTableSnapshot compares all actual source columns and hidden rowids.
// Table identifiers come only from the fixed test cases below.
func immutableTableSnapshot(t *testing.T, tx *sql.Tx, table string) string {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{"guard_rowid"}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	query := "SELECT json_group_array(json_array(" + strings.Join(columns, ",") + ")) FROM (SELECT rowid AS guard_rowid,* FROM " + table + " ORDER BY rowid)"
	var result string
	if err := tx.QueryRowContext(t.Context(), query).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRetainedHistoryAndFrozenChangesRejectReplacement(t *testing.T) {
	fixture := immutableFixture(t)
	db := fixture.store.db
	// Check the database triggers independently of the connection setting.
	execSQL(t, db, `PRAGMA recursive_triggers=OFF`)
	defer execSQL(t, db, `PRAGMA recursive_triggers=ON`)
	cases := []struct {
		name, table, statement string
	}{
		{"audit primary", "mutation_events", `INSERT OR REPLACE INTO mutation_events SELECT * FROM mutation_events LIMIT 1`},
		{"audit key", "mutation_events", `INSERT OR REPLACE INTO mutation_events SELECT 'different-audit',admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json,note FROM mutation_events LIMIT 1`},
		{"audit hidden rowid", "mutation_events", `INSERT OR REPLACE INTO mutation_events(rowid,id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json,note) SELECT rowid,'different-audit',admin_id,dataset_epoch,operation,'different-key',occurred_at,reason,result_json,note FROM mutation_events LIMIT 1`},
		{"location event", "location_events", `INSERT OR REPLACE INTO location_events SELECT * FROM location_events LIMIT 1`},
		{"inventory event", "inventory_events", `INSERT OR REPLACE INTO inventory_events SELECT * FROM inventory_events LIMIT 1`},
		{"inventory legacy key", "inventory_events", `INSERT OR REPLACE INTO inventory_events SELECT 'different-inventory',item_id,item_unit_id,from_location_id,to_location_id,action,quantity_base,occurred_at,idempotency_key,note,reason,mutation_event_id FROM inventory_events LIMIT 1`},
		{"purchase header", "purchases", `INSERT OR REPLACE INTO purchases SELECT * FROM purchases LIMIT 1`},
		{"purchase line", "purchase_lines", `INSERT OR REPLACE INTO purchase_lines SELECT * FROM purchase_lines LIMIT 1`},
		{"purchase header correction", "purchase_header_corrections", `INSERT OR REPLACE INTO purchase_header_corrections SELECT * FROM purchase_header_corrections LIMIT 1`},
		{"purchase line correction", "purchase_line_corrections", `INSERT OR REPLACE INTO purchase_line_corrections SELECT * FROM purchase_line_corrections LIMIT 1`},
		{"purchase original line", "purchase_line_corrections", `INSERT OR REPLACE INTO purchase_line_corrections SELECT 'different-audit',revision,purchase_id,line_id,NULL,'void',reason,created_at,context_json FROM purchase_line_corrections LIMIT 1`},
		{"purchase replacement line", "purchase_line_corrections", `INSERT OR REPLACE INTO purchase_line_corrections SELECT 'different-audit',revision,purchase_id,'different-line',replacement_line_id,action,reason,created_at,context_json FROM purchase_line_corrections LIMIT 1`},
		{"asset profile", "asset_profiles", `INSERT OR REPLACE INTO asset_profiles SELECT * FROM asset_profiles LIMIT 1`},
		{"asset profile change", "asset_profile_changes", `INSERT OR REPLACE INTO asset_profile_changes SELECT * FROM asset_profile_changes LIMIT 1`},
		{"usage", "asset_usage_events", `INSERT OR REPLACE INTO asset_usage_events SELECT * FROM asset_usage_events LIMIT 1`},
		{"usage legacy key", "asset_usage_events", `INSERT OR REPLACE INTO asset_usage_events SELECT 'different-usage',item_unit_id,occurred_at,delta,usage_unit,event_type,note,idempotency_key,created_at,correction_of,mutation_event_id FROM asset_usage_events LIMIT 1`},
		{"cost", "asset_cost_events", `INSERT OR REPLACE INTO asset_cost_events SELECT * FROM asset_cost_events LIMIT 1`},
		{"cost legacy key", "asset_cost_events", `INSERT OR REPLACE INTO asset_cost_events SELECT 'different-cost',item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,idempotency_key,created_at,correction_of,mutation_event_id FROM asset_cost_events LIMIT 1`},
		{"disposal", "asset_disposals", `INSERT OR REPLACE INTO asset_disposals SELECT * FROM asset_disposals LIMIT 1`},
		{"disposal correction", "asset_disposal_corrections", `INSERT OR REPLACE INTO asset_disposal_corrections SELECT * FROM asset_disposal_corrections LIMIT 1`},
		{"disposal root", "asset_disposal_corrections", `INSERT OR REPLACE INTO asset_disposal_corrections SELECT 'different-audit',revision,item_unit_id,NULL,4,action,reason,created_at,context_json FROM asset_disposal_corrections WHERE previous_correction_id IS NULL`},
		{"disposal predecessor", "asset_disposal_corrections", `INSERT OR REPLACE INTO asset_disposal_corrections SELECT 'different-audit',revision,item_unit_id,previous_correction_id,4,action,reason,created_at,context_json FROM asset_disposal_corrections WHERE previous_correction_id IS NOT NULL`},
		{"disposal subject revision", "asset_disposal_corrections", `INSERT OR REPLACE INTO asset_disposal_corrections SELECT 'different-audit',revision,item_unit_id,mutation_event_id,subject_revision,action,reason,created_at,context_json FROM asset_disposal_corrections WHERE previous_correction_id IS NOT NULL`},
		{"frozen sequence", "sync_changes", `INSERT OR REPLACE INTO sync_changes SELECT seq,entity_type,entity_id,operation,entity_revision,CASE WHEN projection_json IS NULL THEN NULL ELSE '{"poison":true}' END,created_at FROM sync_changes ORDER BY seq LIMIT 1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			before := immutableTableSnapshot(t, tx, tc.table)
			_, err = tx.ExecContext(t.Context(), tc.statement)
			if err == nil || !(strings.Contains(err.Error(), "cannot be replaced") || strings.Contains(err.Error(), "sync payloads are frozen")) {
				t.Errorf("replacement did not encounter its retained-data guard: %v", err)
			}
			if after := immutableTableSnapshot(t, tx, tc.table); before != after {
				t.Errorf("replacement changed retained %s rows or their rowids", tc.table)
			}
		})
	}
}

func TestLateReplacementRollsBackDomainMutationAndOriginalKeyReplay(t *testing.T) {
	for _, tc := range []struct{ name, statement, message string }{
		{"audit", `INSERT OR REPLACE INTO mutation_events SELECT * FROM mutation_events WHERE operation='post_locations'`, "cannot be replaced"},
		{"outbox", `INSERT OR REPLACE INTO sync_changes SELECT seq,entity_type,entity_id,operation,entity_revision,'{"poison":true}',created_at FROM sync_changes WHERE operation='upsert' ORDER BY seq LIMIT 1`, "sync payloads are frozen"},
	} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay=%t", tc.name, replay), func(t *testing.T) {
				s, locations, _, token := locationFixture(t)
				input := LocationCreate{Code: "BOX", Name: "Original intent"}
				const key = "retained-history-original-key"
				var original Location
				var err error
				if replay {
					original, err = locations.Create(t.Context(), token, key, 0, input)
					if err != nil {
						t.Fatal(err)
					}
				}
				execSQL(t, s.db, "CREATE TRIGGER tamper_retained_after_session AFTER UPDATE OF last_seen_at ON sessions BEGIN "+tc.statement+"; END")
				before := domainState(t, s)
				value, err := locations.Create(t.Context(), token, key, 0, input)
				if err == nil || !strings.Contains(err.Error(), tc.message) || value != (Location{}) {
					t.Fatal("late history replacement returned a successful acknowledgement", value, err)
				}
				requireDomainState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER tamper_retained_after_session")
				value, err = locations.Create(t.Context(), token, key, 0, input)
				if err != nil || (replay && value != original) {
					t.Fatal("failed replacement damaged explicit original-key reconciliation", value, err)
				}
			})
		}
	}
}

func TestServingConnectionProtectsExplicitNegativeSentinelRowid(t *testing.T) {
	fixture := immutableFixture(t)
	db := fixture.store.db
	if scalar[int](t, db, `PRAGMA recursive_triggers`) != 1 {
		t.Fatal("serving connection did not enable recursive triggers")
	}
	execSQL(t, db, `INSERT INTO location_events(rowid,id,location_id,occurred_at) VALUES(-1,'negative-rowid','source','original')`)
	_, err := db.ExecContext(t.Context(), `INSERT OR REPLACE INTO location_events(rowid,id,location_id,occurred_at) VALUES(-1,'different-event','source','tampered')`)
	if err == nil || !strings.Contains(err.Error(), "location events are immutable") {
		t.Fatal("explicit sentinel rowid bypassed the serving connection's delete guard", err)
	}
	if scalar[string](t, db, `SELECT occurred_at FROM location_events WHERE rowid=-1`) != "original" {
		t.Fatal("failed sentinel replacement changed retained source")
	}
	execSQL(t, db, `INSERT INTO location_events(id,location_id,occurred_at) VALUES('automatic-after-sentinel','source','new')`)
}

func TestFrozenNegativeSequenceFailsClosedUntilExplicitRetention(t *testing.T) {
	fixture := immutableFixture(t)
	db := fixture.store.db
	execSQL(t, db, `PRAGMA recursive_triggers=OFF`)
	defer execSQL(t, db, `PRAGMA recursive_triggers=ON`)
	execSQL(t, db, `INSERT INTO sync_changes(seq,entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES(-1,'item','retained-invalid','upsert',1,'{"exact":true}','original')`)
	for _, statement := range []string{
		`INSERT OR REPLACE INTO sync_changes(seq,entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES(-1,'item','tampered','upsert',1,'{}','tampered')`,
		`INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('item','automatic','upsert',1,'{}','new')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err == nil || !strings.Contains(err.Error(), "sync payloads are frozen") {
			t.Fatal("ambiguous negative sequence did not fail closed", err)
		}
	}
	if scalar[string](t, db, `SELECT projection_json FROM sync_changes WHERE seq=-1`) != `{"exact":true}` {
		t.Fatal("negative retained sequence lost its exact frozen bytes")
	}
	execSQL(t, db, `DELETE FROM sync_changes WHERE seq=-1`)
	execSQL(t, db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('item','automatic','upsert',1,'{}','new')`)
}

func TestFrozenChangesPermitAutomaticAppendAndExplicitRetention(t *testing.T) {
	fixture := immutableFixture(t)
	db := fixture.store.db
	execSQL(t, db, `PRAGMA recursive_triggers=OFF`)
	defer execSQL(t, db, `PRAGMA recursive_triggers=ON`)
	before := scalar[int64](t, db, `SELECT MAX(seq) FROM sync_changes`)
	execSQL(t, db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('item','append','upsert',1,'{"id":"append"}','test')`)
	after := scalar[int64](t, db, `SELECT MAX(seq) FROM sync_changes`)
	if after <= before {
		t.Fatal("normal append did not allocate a new sequence", before, after)
	}
	execSQL(t, db, `DELETE FROM sync_changes WHERE seq=?`, after)
	if scalar[int](t, db, `SELECT COUNT(*) FROM sync_changes WHERE seq=?`, after) != 0 {
		t.Fatal("explicit retention could not remove a frozen row")
	}
	execSQL(t, db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('item','after-retention','upsert',1,'{"id":"after-retention"}','test')`)
	if next := scalar[int64](t, db, `SELECT MAX(seq) FROM sync_changes`); next <= after {
		t.Fatal(fmt.Sprintf("retention reused frozen sequence: %d after %d", next, after))
	}
}
