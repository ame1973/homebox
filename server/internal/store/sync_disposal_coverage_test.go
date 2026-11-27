package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncDisposalCoverageClassifiesActualFinancialVoidAndReinstatement(t *testing.T) {
	fixture, assets, asset, original := disposalCommands(t)
	assertCoverage := func(active, voided int64) {
		t.Helper()
		before := disposalCorrectionState(t, fixture.store)
		withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
			coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
			if err != nil || coverage.Active != active || coverage.Voided != voided {
				t.Fatal("actual disposal decision was not covered", coverage, err)
			}
		})
		requireDisposalCorrectionState(t, fixture.store, before)
	}
	assertCoverage(1, 0)
	input := disposalCorrectionInput(asset.Revision, 0)
	input.Action, input.Replacement = "void", nil
	voided, err := assets.CorrectDisposal(t.Context(), fixture.token, "coverage-disposal-void", fixture.unit.ID, original.Revision, input)
	if err != nil || voided.After.Current != nil {
		t.Fatal("fixture did not void the financial decision", err)
	}
	assertCoverage(0, 1)
	input = disposalCorrectionInput(asset.Revision+1, 0)
	input.Replacement.Kind = "donated"
	reinstated, err := assets.CorrectDisposal(t.Context(), fixture.token, "coverage-disposal-reinstate", fixture.unit.ID, voided.After.Revision, input)
	if err != nil || reinstated.After.Current == nil || reinstated.After.Current.RealizedProceedsMinor != 0 {
		t.Fatal("fixture did not reinstate explicit zero proceeds", err)
	}
	assertCoverage(1, 0)
	status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("Disposal coverage enabled public synchronization", err)
	}
}

func TestSyncDisposalCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture, assets, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	input.Action, input.Replacement = "void", nil
	if _, err := assets.CorrectDisposal(t.Context(), fixture.token, "coverage-disposal-damage-void", fixture.unit.ID, record.Revision, input); err != nil {
		t.Fatal(err)
	}
	before := disposalCorrectionState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"voided current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'disposal',d.item_unit_id,r.revision,json_object('item_unit_id',d.item_unit_id,'kind',d.kind,'disposed_at',d.disposed_at,'realized_proceeds_minor',d.realized_proceeds_minor,'currency',d.currency,'note',d.note),'2026'
 FROM asset_disposals d JOIN projection_revisions r ON r.entity_type='disposal' AND r.entity_id=d.item_unit_id WHERE d.item_unit_id=?`, fixture.unit.ID)
		}, ErrDomainState},
		{"missing counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"corrupt correction", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposal_corrections_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE asset_disposal_corrections SET context_json='{}' WHERE item_unit_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"broken correction predecessor", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposal_corrections_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE asset_disposal_corrections SET previous_correction_id='missing-predecessor' WHERE item_unit_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"missing Asset counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"missing Unit counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"oversized NUL-prefixed source note", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposals_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE asset_disposals SET note=? WHERE item_unit_id=?", "\x00"+strings.Repeat("😀", 10000), fixture.unit.ID)
		}, ErrDomainState},
		{"missing retained original", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposals_no_delete")
			execSyncCoverageSQL(t, tx, "DELETE FROM asset_disposals WHERE item_unit_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('disposal','orphan-disposal',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('disposal','orphan-disposal',1,'{}','2026')`)
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncDisposalCoverage{}) {
				t.Fatal("damaged Disposal source returned coverage", coverage, err)
			}
		})
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestSyncDisposalCoverageRejectsDamagedActiveCurrent(t *testing.T) {
	fixture, _, _, _ := disposalCommands(t)
	before := disposalCorrectionState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID)
		}, errSyncDisposalUncovered},
		{"changed proceeds", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.realized_proceeds_minor',999) WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncDisposalCoverage{}) {
				t.Fatal("damaged active Disposal returned coverage", coverage, err)
			}
		})
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestSyncDisposalCoveragePagesExactIDsAndWithholdsLateMissingCurrent(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Disposal coverage item"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("entry-%03d", index))
	}
	ids = append(ids, "entry-098\x00-家庭", "entry-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncAssetCoverage(t, s, item.ID, ids)
	seedSyncDisposalCoverage(t, s, ids)
	before := disposalCorrectionState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "disposal", "", false)
		if err != nil || len(first) != 100 || first[99] != "entry-098\x00-家庭" {
			t.Fatal("first Disposal page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "disposal", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "entry-197\x00-第二" {
			t.Fatal("second Disposal page changed its embedded-NUL boundary", second, err)
		}
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if err != nil || coverage.Active != int64(len(ids)) || coverage.Voided != 0 {
			t.Fatal("multi-page Disposal coverage changed identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='disposal' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncDisposalUncovered) || coverage != (syncDisposalCoverage{}) {
			t.Fatal("late missing Disposal returned partial coverage", coverage, err)
		}
	}()
	requireDisposalCorrectionState(t, s, before)
}

func seedSyncDisposalCoverage(t *testing.T, s *Store, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO asset_disposals(item_unit_id,kind,disposed_at,realized_proceeds_minor,currency,note,created_at) VALUES(?,'donated','2026-01-01T00:00:00Z',0,'ZZZ','','legacy')`, id)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('disposal',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'disposal',item_unit_id,1,json_object('item_unit_id',item_unit_id,'kind',kind,'disposed_at',disposed_at,'realized_proceeds_minor',realized_proceeds_minor,'currency',currency,'note',note),'2026' FROM asset_disposals WHERE item_unit_id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDisposalCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if coverage, err := verifySyncDisposalCoverage(t.Context(), tx); err != nil || coverage != (syncDisposalCoverage{}) {
			t.Fatal("empty Disposal coverage failed", coverage, err)
		}
	})
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Disposal exact page owner"})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncAssetCoverage(t, s, item.ID, ids)
	seedSyncDisposalCoverage(t, s, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if err != nil || coverage.Active != 100 || coverage.Voided != 0 {
			t.Fatal("exact-boundary Disposal coverage failed", coverage, err)
		}
	})
}

func TestSyncDisposalCoverageAcceptsMaximumWidthNote(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Maximum Disposal owner"})
	id := strings.Repeat("😀", 128)
	seedSyncAssetCoverage(t, s, item.ID, []string{id})
	seedSyncDisposalCoverage(t, s, []string{id})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	note := strings.Repeat("😀", 10000)
	execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposals_no_update")
	execSyncCoverageSQL(t, tx, "UPDATE asset_disposals SET note=? WHERE item_unit_id=?", note, id)
	execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.note',?) WHERE entity_type='disposal' AND entity_id=?", note, id)
	if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	if coverage, err := verifySyncDisposalCoverage(t.Context(), tx); err != nil || coverage.Active != 1 || coverage.Voided != 0 {
		t.Fatal("maximum-width retained Disposal note was rejected", coverage, err)
	}
}

func TestSyncDisposalCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid Disposal owner"})
			seedSyncAssetCoverage(t, s, item.ID, []string{id})
			seedSyncDisposalCoverage(t, s, []string{id})
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncDisposalCoverage{}) {
					t.Fatal("invalid Disposal source identity was skipped", coverage, err)
				}
			})
		})
	}
}

func TestSyncDisposalCoverageRejectsOversizedIDAfterFirstPage(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Late invalid Disposal owner"})
	ids := make([]string, 0, 101)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("valid-%03d", index))
	}
	ids = append(ids, strings.Repeat("z", 513))
	seedSyncAssetCoverage(t, s, item.ID, ids)
	seedSyncDisposalCoverage(t, s, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "disposal", "", false)
		if err != nil || len(first) != 100 {
			t.Fatal("fixture did not reach the first Disposal page", err)
		}
		if next, err := readSyncCoverageIDs(t.Context(), tx, "disposal", first[99], true); !errors.Is(err, ErrDomainState) || next != nil {
			t.Fatal("oversized Disposal source was skipped after the first page", next, err)
		}
		if coverage, err := verifySyncDisposalCoverage(t.Context(), tx); !errors.Is(err, ErrDomainState) || coverage != (syncDisposalCoverage{}) {
			t.Fatal("late invalid Disposal identity returned partial coverage", coverage, err)
		}
	})
}

func TestSyncDisposalCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture, _, _, _ := disposalCommands(t)
	before := disposalCorrectionState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='disposal' AND entity_id=?", oversized, fixture.unit.ID)
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncDisposalCoverage{}) {
			t.Fatal("oversized Disposal projection returned coverage", coverage, err)
		}
	}()
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestSyncDisposalCoverageWithholdsOversizedCorrectionContext(t *testing.T) {
	fixture, assets, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	input.Action, input.Replacement = "void", nil
	if _, err := assets.CorrectDisposal(t.Context(), fixture.token, "coverage-large-disposal-correction", fixture.unit.ID, record.Revision, input); err != nil {
		t.Fatal(err)
	}
	before := disposalCorrectionState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposal_corrections_no_update")
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE asset_disposal_corrections SET context_json=? WHERE item_unit_id=?", oversized, fixture.unit.ID)
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if !errors.Is(err, ErrDomainState) || coverage != (syncDisposalCoverage{}) {
			t.Fatal("oversized Disposal correction returned coverage", coverage, err)
		}
	}()
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestSyncDisposalCoverageWithholdsOversizedLegacyCreationText(t *testing.T) {
	fixture := assetsFixture(t, true)
	before := disposalCorrectionState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DROP TRIGGER asset_disposals_no_update")
		execSyncCoverageSQL(t, tx, "UPDATE asset_disposals SET created_at=? WHERE item_unit_id=?", strings.Repeat("x", maxSyncPageBytes+1), fixture.unit.ID)
		coverage, err := verifySyncDisposalCoverage(t.Context(), tx)
		if !errors.Is(err, ErrDomainState) || coverage != (syncDisposalCoverage{}) {
			t.Fatal("oversized legacy Disposal creation text returned coverage", coverage, err)
		}
	}()
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestSyncDisposalCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncDisposalCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncDisposalCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncDisposalCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncDisposalCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncDisposalCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncDisposalCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncDisposalCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncDisposalCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
