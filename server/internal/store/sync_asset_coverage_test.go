package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncAssetCoverageIncludesActualUsageAndCostCorrections(t *testing.T) {
	fixture := assetsFixture(t, false)
	assets := assetReader(t, fixture.holdingFixture)
	usage, err := assets.RecordUsage(t.Context(), fixture.token, "coverage-usage", fixture.unit.ID, fixture.asset.Revision, UsageWrite{
		Delta: 2, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-08T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assets.RecordUsage(t.Context(), fixture.token, "coverage-usage-correction", fixture.unit.ID, fixture.asset.Revision+1, UsageWrite{
		Delta: -1, UsageUnit: "times", EventType: "correction", OccurredAt: "2026-11-08T01:00:00Z", CorrectionOf: &usage.ID,
	}); err != nil {
		t.Fatal(err)
	}
	cost, err := assets.RecordCost(t.Context(), fixture.token, "coverage-cost", fixture.unit.ID, fixture.asset.Revision+2, CostWrite{
		AmountMinor: 2, Currency: "JPY", Kind: "fee", Direction: "expense", OccurredAt: "2026-11-08T02:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assets.RecordCost(t.Context(), fixture.token, "coverage-cost-correction", fixture.unit.ID, fixture.asset.Revision+3, CostWrite{
		AmountMinor: 1, Currency: "JPY", Kind: "adjustment", Direction: "credit", OccurredAt: "2026-11-08T03:00:00Z", CorrectionOf: &cost.ID,
	}); err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, fixture.store)
	withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
		coverage, err := verifySyncAssetCoverage(t.Context(), tx)
		if err != nil || coverage.Profiles != 1 || coverage.Usage != 3 || coverage.Costs != 3 {
			t.Fatal("actual Asset sources and corrections were not covered", coverage, err)
		}
	})
	requireAssetProfileState(t, fixture.store, before)
	status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("Asset coverage enabled public synchronization", err)
	}
}

func TestSyncAssetCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture := assetsFixture(t, false)
	before := assetProfileState(t, fixture.store)
	for _, target := range []struct{ kind, id, field string }{
		{"asset", fixture.asset.ItemUnitID, "currency"}, {"usage", fixture.usage.ID, "note"}, {"cost", fixture.cost.ID, "note"},
	} {
		for _, test := range []struct {
			name   string
			damage func(*testing.T, *sql.Tx)
			want   error
		}{
			{"missing current", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, errSyncAssetUncovered},
			{"changed current", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,?,'changed') WHERE entity_type=? AND entity_id=?", "$."+target.field, target.kind, target.id)
			}, ErrDomainState},
			{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, ErrDomainState},
			{"counter missing", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", target.kind, target.id)
				execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, ErrDomainState},
			{"orphan current", func(t *testing.T, tx *sql.Tx) {
				seedOrphanAssetCoverageCurrent(t, tx, target.kind)
			}, ErrDomainState},
			{"missing Unit counter", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
				execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
			}, ErrDomainState},
			{"missing Item counter", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.individual.ID)
				execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.individual.ID)
			}, ErrDomainState},
		} {
			t.Run(target.kind+"/"+test.name, func(t *testing.T) {
				tx, err := fixture.store.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				test.damage(t, tx)
				if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
						t.Error("restore query-only mode:", err)
					}
				}()
				coverage, err := verifySyncAssetCoverage(t.Context(), tx)
				if !errors.Is(err, test.want) || coverage != (syncAssetCoverage{}) {
					t.Fatal("damaged Asset source returned coverage", coverage, err)
				}
			})
		}
	}
	requireAssetProfileState(t, fixture.store, before)
}

func seedOrphanAssetCoverageCurrent(t *testing.T, tx *sql.Tx, kind string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,'orphan-asset-coverage',1)", kind)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,'orphan-asset-coverage',1,'{}','2026')`, kind)
}

func TestSyncAssetCoveragePagesExactIDsAndWithholdsLateMissingCost(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Asset coverage item"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("entry-%03d", index))
	}
	ids = append(ids, "entry-098\x00-家庭", "entry-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncAssetCoverage(t, s, item.ID, ids)
	before := assetProfileState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, kind := range []string{"asset", "usage", "cost"} {
			first, err := readSyncCoverageIDs(t.Context(), tx, kind, "", false)
			if err != nil || len(first) != 100 || first[99] != "entry-098\x00-家庭" {
				t.Fatal("first Asset source page changed its embedded-NUL boundary", kind, first, err)
			}
			second, err := readSyncCoverageIDs(t.Context(), tx, kind, first[99], true)
			if err != nil || len(second) != 100 || second[99] != "entry-197\x00-第二" {
				t.Fatal("second Asset source page changed its embedded-NUL boundary", kind, second, err)
			}
		}
		coverage, err := verifySyncAssetCoverage(t.Context(), tx)
		if err != nil || coverage.Profiles != int64(len(ids)) || coverage.Usage != int64(len(ids)) || coverage.Costs != int64(len(ids)) {
			t.Fatal("multi-page Asset coverage changed identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='cost' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncAssetCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncAssetUncovered) || coverage != (syncAssetCoverage{}) {
			t.Fatal("late missing cost returned partial coverage", coverage, err)
		}
	}()
	requireAssetProfileState(t, s, before)
}

func seedSyncAssetCoverage(t *testing.T, s *Store, itemID string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO item_units(id,item_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,'stored','legacy','legacy')`, id, itemID, fmt.Sprintf("asset-code-%03d", index))
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('unit',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,usage_unit,created_at,updated_at) VALUES(?,'manual',0,'ZZZ','times','legacy','legacy')`, id)
		execSyncCoverageSQL(t, tx, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at) VALUES(?,?,'2026-01-01T00:00:00Z',1,'times','use','','legacy')`, id, id)
		execSyncCoverageSQL(t, tx, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,created_at) VALUES(?,?,'2026-01-01T00:00:00Z','fee',1,'expense','ZZZ','','legacy')`, id, id)
		for _, kind := range []string{"asset", "usage", "cost"} {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id)
		}
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'asset',item_unit_id,1,json_object('item_unit_id',item_unit_id,'cost_basis',cost_basis,'currency',currency,'manual_acquisition_minor',manual_acquisition_minor,'in_service_at',in_service_at,'retired_at',retired_at,'estimated_residual_minor',estimated_residual_minor,'usage_unit',usage_unit),'2026' FROM asset_profiles WHERE item_unit_id=?`, id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'usage',id,1,json_object('id',id,'item_unit_id',item_unit_id,'delta',delta,'usage_unit',usage_unit,'event_type',event_type,'occurred_at',occurred_at,'note',note,'correction_of',correction_of),'2026' FROM asset_usage_events WHERE id=?`, id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'cost',id,1,json_object('id',id,'item_unit_id',item_unit_id,'amount_minor',amount_minor,'direction',direction,'currency',currency,'kind',kind,'occurred_at',occurred_at,'note',note,'correction_of',correction_of),'2026' FROM asset_cost_events WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncAssetCoverageRejectsBrokenCorrectionChains(t *testing.T) {
	fixture := assetsFixture(t, false)
	assets := assetReader(t, fixture.holdingFixture)
	usage, err := assets.RecordUsage(t.Context(), fixture.token, "coverage-broken-use", fixture.unit.ID, fixture.asset.Revision, UsageWrite{
		Delta: -1, UsageUnit: "times", EventType: "correction", OccurredAt: "2026-11-08T00:00:00Z", CorrectionOf: &fixture.usage.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	cost, err := assets.RecordCost(t.Context(), fixture.token, "coverage-broken-cost", fixture.unit.ID, fixture.asset.Revision+1, CostWrite{
		AmountMinor: 1, Currency: "JPY", Kind: "adjustment", Direction: "credit", OccurredAt: "2026-11-08T01:00:00Z", CorrectionOf: &fixture.cost.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, fixture.store)
	for _, target := range []struct{ kind, table, original, correction string }{
		{"usage", "asset_usage_events", fixture.usage.ID, usage.ID},
		{"cost", "asset_cost_events", fixture.cost.ID, cost.ID},
	} {
		for _, damage := range []string{"missing target", "cycle"} {
			t.Run(target.kind+"/"+damage, func(t *testing.T) {
				tx, err := fixture.store.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
				execSyncCoverageSQL(t, tx, "DROP TRIGGER "+target.table+"_no_update")
				if damage == "missing target" {
					execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET correction_of='missing-target' WHERE id=?", target.correction)
					execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.correction_of','missing-target') WHERE entity_type=? AND entity_id=?", target.kind, target.correction)
				} else if target.kind == "usage" {
					execSyncCoverageSQL(t, tx, "UPDATE asset_usage_events SET event_type='correction',correction_of=? WHERE id=?", target.correction, target.original)
					execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.event_type','correction','$.correction_of',?) WHERE entity_type='usage' AND entity_id=?", target.correction, target.original)
				} else {
					execSyncCoverageSQL(t, tx, "UPDATE asset_cost_events SET kind='adjustment',correction_of=? WHERE id=?", target.correction, target.original)
					execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.kind','adjustment','$.correction_of',?) WHERE entity_type='cost' AND entity_id=?", target.correction, target.original)
				}
				coverage, err := verifySyncAssetCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncAssetCoverage{}) {
					t.Fatal("broken correction chain returned coverage", coverage, err)
				}
			})
		}
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestSyncAssetCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if coverage, err := verifySyncAssetCoverage(t.Context(), tx); err != nil || coverage != (syncAssetCoverage{}) {
			t.Fatal("empty Asset coverage failed", coverage, err)
		}
	})
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Asset exact page owner"})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncAssetCoverage(t, s, item.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncAssetCoverage(t.Context(), tx)
		if err != nil || coverage.Profiles != 100 || coverage.Usage != 100 || coverage.Costs != 100 {
			t.Fatal("exact-boundary Asset coverage failed", coverage, err)
		}
	})
}

func TestSyncAssetCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, kind := range []string{"asset", "usage", "cost"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", kind, len(id)), func(t *testing.T) {
				s, catalog, _, token := catalogFixture(t)
				item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid Asset identity owner"})
				if kind == "asset" {
					seedSyncAssetCoverage(t, s, item.ID, []string{id})
				} else {
					seedSyncAssetCoverage(t, s, item.ID, []string{"valid"})
					table := "asset_usage_events"
					if kind == "cost" {
						table = "asset_cost_events"
					}
					execSQL(t, s.db, "DROP TRIGGER "+table+"_no_update")
					execSQL(t, s.db, "UPDATE "+table+" SET id=? WHERE id='valid'", id)
				}
				withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
					coverage, err := verifySyncAssetCoverage(t.Context(), tx)
					if !errors.Is(err, ErrDomainState) || coverage != (syncAssetCoverage{}) {
						t.Fatal("invalid Asset source identity was skipped", coverage, err)
					}
				})
			})
		}
	}
}

func TestSyncAssetCoverageRejectsOversizedIDAfterFirstPage(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Late invalid Asset owner"})
	ids := make([]string, 0, 101)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("valid-%03d", index))
	}
	ids = append(ids, strings.Repeat("z", 513))
	seedSyncAssetCoverage(t, s, item.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "asset", "", false)
		if err != nil || len(first) != 100 {
			t.Fatal("fixture did not reach the first Asset page", err)
		}
		if next, err := readSyncCoverageIDs(t.Context(), tx, "asset", first[99], true); !errors.Is(err, ErrDomainState) || next != nil {
			t.Fatal("oversized Asset source was skipped after the first page", next, err)
		}
		if coverage, err := verifySyncAssetCoverage(t.Context(), tx); !errors.Is(err, ErrDomainState) || coverage != (syncAssetCoverage{}) {
			t.Fatal("late invalid Asset identity returned partial coverage", coverage, err)
		}
	})
}

func TestSyncAssetCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture := assetsFixture(t, false)
	before := assetProfileState(t, fixture.store)
	for _, target := range []struct{ kind, id string }{{"asset", fixture.asset.ItemUnitID}, {"usage", fixture.usage.ID}, {"cost", fixture.cost.ID}} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", oversized, target.kind, target.id)
			coverage, err := verifySyncAssetCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncAssetCoverage{}) {
				t.Fatal("oversized Asset projection returned coverage", coverage, err)
			}
		})
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestSyncAssetCoverageRejectsInvalidEventFields(t *testing.T) {
	fixture := assetsFixture(t, false)
	before := assetProfileState(t, fixture.store)
	for _, target := range []struct{ kind, table, id string }{
		{"usage", "asset_usage_events", fixture.usage.ID}, {"cost", "asset_cost_events", fixture.cost.ID},
	} {
		for _, damage := range []string{"oversized note", "invalid timestamp", "missing owner"} {
			t.Run(target.kind+"/"+damage, func(t *testing.T) {
				tx, err := fixture.store.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
				execSyncCoverageSQL(t, tx, "DROP TRIGGER "+target.table+"_no_update")
				switch damage {
				case "oversized note":
					execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET note=? WHERE id=?", "\x00"+strings.Repeat("😀", 10000), target.id)
				case "invalid timestamp":
					execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET occurred_at='not-an-instant' WHERE id=?", target.id)
				case "missing owner":
					execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET item_unit_id='missing-unit' WHERE id=?", target.id)
				}
				coverage, err := verifySyncAssetCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncAssetCoverage{}) {
					t.Fatal("invalid Asset event source returned coverage", coverage, err)
				}
			})
		}
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestSyncAssetCoverageAcceptsMaximumWidthEventNotes(t *testing.T) {
	fixture := assetsFixture(t, false)
	tx, err := fixture.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	note := strings.Repeat("😀", 10000)
	for _, target := range []struct{ kind, table, id string }{
		{"usage", "asset_usage_events", fixture.usage.ID}, {"cost", "asset_cost_events", fixture.cost.ID},
	} {
		execSyncCoverageSQL(t, tx, "DROP TRIGGER "+target.table+"_no_update")
		execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET note=? WHERE id=?", note, target.id)
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.note',?) WHERE entity_type=? AND entity_id=?", note, target.kind, target.id)
	}
	if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	coverage, err := verifySyncAssetCoverage(t.Context(), tx)
	if err != nil || coverage.Profiles != 1 || coverage.Usage != 1 || coverage.Costs != 1 {
		t.Fatal("maximum-width retained event notes were rejected", coverage, err)
	}
}

func TestSyncAssetCoverageWithholdsOversizedLegacyCreationText(t *testing.T) {
	fixture := assetsFixture(t, false)
	before := assetProfileState(t, fixture.store)
	for _, target := range []struct{ kind, table, id string }{
		{"usage", "asset_usage_events", fixture.usage.ID}, {"cost", "asset_cost_events", fixture.cost.ID},
	} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "DROP TRIGGER "+target.table+"_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET created_at=? WHERE id=?", strings.Repeat("x", maxSyncPageBytes+1), target.id)
			coverage, err := verifySyncAssetCoverage(t.Context(), tx)
			if !errors.Is(err, ErrDomainState) || coverage != (syncAssetCoverage{}) {
				t.Fatal("oversized legacy creation text returned coverage", coverage, err)
			}
		})
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestSyncAssetCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncAssetCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncAssetCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncAssetCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncAssetCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncAssetCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncAssetCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncAssetCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncAssetCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
