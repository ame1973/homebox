package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncPurchaseCoverageClassifiesActualVoidAndReplacement(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "coverage-void-unit", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal(err)
	}
	input = reviewedLineVoid(t, fixture, fixture.stockLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: input.ItemRevision, QuantityBase: 1500, PaidMinor: 0}
	replacement, err := purchases.CorrectLine(t.Context(), fixture.token, "coverage-replace-stock", fixture.purchase.ID, fixture.purchase.Revision+1, input)
	if err != nil || replacement.After == nil || replacement.After.ID == fixture.stockLine.ID {
		t.Fatal("fixture did not post an independent replacement", err)
	}
	before := purchaseCommandState(t, fixture.store)
	withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if err != nil || coverage.Headers != 1 || coverage.ActiveLines != 1 || coverage.VoidedLines != 2 {
			t.Fatal("actual Purchase and correction sources were not covered", coverage, err)
		}
	})
	requirePurchaseCommandState(t, fixture.store, before)
	status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("Purchase coverage enabled public synchronization", err)
	}
}

func TestSyncPurchaseCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "coverage-damage-void", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal(err)
	}
	before := purchaseCommandState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing Purchase current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
		}, errSyncPurchaseUncovered},
		{"changed Purchase current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.merchant_name','wrong') WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
		}, ErrDomainState},
		{"missing active Line current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
		}, errSyncPurchaseUncovered},
		{"changed active Line money", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.paid_minor',1) WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
		}, ErrDomainState},
		{"voided Line has a current projection", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'purchase_line',l.id,r.revision,json_object('id',l.id,'purchase_id',l.purchase_id,'item_id',l.item_id,'item_unit_id',l.item_unit_id,'quantity_base',l.quantity_base,'paid_minor',l.paid_minor,'note',l.note),'2026'
 FROM purchase_lines l JOIN projection_revisions r ON r.entity_type='purchase_line' AND r.entity_id=l.id WHERE l.id=?`, fixture.unitLine.ID)
		}, ErrDomainState},
		{"corrupt correction context", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_line_corrections_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE purchase_line_corrections SET context_json='{}' WHERE line_id=?", fixture.unitLine.ID)
		}, ErrDomainState},
		{"Line counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
		}, ErrDomainState},
		{"missing Line counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id=?", fixture.stockLine.ID)
		}, ErrDomainState},
		{"missing voided Line counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id=?", fixture.unitLine.ID)
		}, ErrDomainState},
		{"missing Purchase counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
		}, ErrDomainState},
		{"missing Unit counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"oversized NUL-prefixed merchant", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE purchases SET merchant_name=? WHERE id=?", "\x00"+strings.Repeat("😀", 300), fixture.purchase.ID)
		}, ErrDomainState},
		{"oversized NUL-prefixed receipt", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE purchases SET receipt_note=? WHERE id=?", "\x00"+strings.Repeat("😀", 10000), fixture.purchase.ID)
		}, ErrDomainState},
		{"oversized NUL-prefixed Line note", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_lines_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE purchase_lines SET note=? WHERE id=?", "\x00"+strings.Repeat("😀", 10000), fixture.stockLine.ID)
		}, ErrDomainState},
		{"missing Line owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_lines_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE purchase_lines SET item_id='missing-owner' WHERE id=?", fixture.stockLine.ID)
		}, ErrDomainState},
		{"missing Item counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
		}, ErrDomainState},
		{"orphan Purchase current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanPurchaseCoverageCurrent(t, tx, "purchase")
		}, ErrDomainState},
		{"orphan Line current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanPurchaseCoverageCurrent(t, tx, "purchase_line")
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
			coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncPurchaseCoverage{}) {
				t.Fatal("damaged Purchase returned coverage", coverage, err)
			}
		})
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestSyncPurchaseCoverageRejectsMissingCorrectionSources(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.stockLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: input.ItemRevision, QuantityBase: 1, PaidMinor: 0}
	replacement, err := purchases.CorrectLine(t.Context(), fixture.token, "coverage-missing-correction-source", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil || replacement.After == nil {
		t.Fatal("fixture did not post a replacement", err)
	}
	before := purchaseCommandState(t, fixture.store)
	for _, target := range []struct{ name, id string }{{"original", fixture.stockLine.ID}, {"replacement", replacement.After.ID}} {
		t.Run(target.name, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_lines_no_delete")
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", target.id)
			execSyncCoverageSQL(t, tx, "DELETE FROM purchase_lines WHERE id=?", target.id)
			coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
			if !errors.Is(err, ErrDomainState) || coverage != (syncPurchaseCoverage{}) {
				t.Fatal("missing correction source returned coverage", coverage, err)
			}
		})
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func seedOrphanPurchaseCoverageCurrent(t *testing.T, tx *sql.Tx, kind string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,'orphan-purchase-coverage',1)", kind)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,'orphan-purchase-coverage',1,'{}','2026')`, kind)
}

func TestSyncPurchaseCoveragePagesExactIDsAndWithholdsLateMissingLine(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	purchases, err := NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	purchase := createPurchaseCommand(t, purchases, token, "coverage-page-purchase", "JPY")
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Financial coverage item"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("line-%03d", index))
	}
	ids = append(ids, "line-098\x00-家庭", "line-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncPurchaseCoverageLines(t, s, purchase.ID, item.ID, ids)
	before := purchaseCommandState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "purchase_line", "", false)
		if err != nil || len(first) != 100 || first[99] != "line-098\x00-家庭" {
			t.Fatal("first Line page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "purchase_line", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "line-197\x00-第二" {
			t.Fatal("second Line page changed its embedded-NUL boundary", second, err)
		}
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if err != nil || coverage.Headers != 1 || coverage.ActiveLines != int64(len(ids)) || coverage.VoidedLines != 0 {
			t.Fatal("multi-page Purchase coverage changed identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPurchaseUncovered) || coverage != (syncPurchaseCoverage{}) {
			t.Fatal("late missing Line returned partial coverage", coverage, err)
		}
	}()
	requirePurchaseCommandState(t, s, before)
}

func seedSyncPurchaseCoverageLines(t *testing.T, s *Store, purchaseID, itemID string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,note) VALUES(?,?,?,1,0,'')", id, purchaseID, itemID)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('purchase_line',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'purchase_line',id,1,json_object('id',id,'purchase_id',purchase_id,'item_id',item_id,'item_unit_id',item_unit_id,'quantity_base',quantity_base,'paid_minor',paid_minor,'note',note),'2026'
 FROM purchase_lines WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedSyncPurchaseCoverageHeaders(t *testing.T, s *Store, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,'Retained merchant','2026-01-01T00:00:00Z','ZZZ','','legacy')", id)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('purchase',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'purchase',id,1,json_object('id',id,'merchant_name',merchant_name,'purchased_at',purchased_at,'currency',currency,'receipt_note',receipt_note),'2026'
 FROM purchases WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncPurchaseCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if coverage, err := verifySyncPurchaseCoverage(t.Context(), tx); err != nil || coverage != (syncPurchaseCoverage{}) {
			t.Fatal("empty Purchase coverage failed", coverage, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("header-%03d", index))
	}
	seedSyncPurchaseCoverageHeaders(t, s, ids)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Exact page financial owner"})
	seedSyncPurchaseCoverageLines(t, s, ids[0], item.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if err != nil || coverage.Headers != 100 || coverage.ActiveLines != 100 || coverage.VoidedLines != 0 {
			t.Fatal("exact-boundary Purchase coverage failed", coverage, err)
		}
	})
}

func TestSyncPurchaseCoveragePagesExactHeaderIDs(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("header-%03d", index))
	}
	ids = append(ids, "header-098\x00-家庭", "header-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncPurchaseCoverageHeaders(t, s, ids)
	before := purchaseCommandState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "purchase", "", false)
		if err != nil || len(first) != 100 || first[99] != "header-098\x00-家庭" {
			t.Fatal("first Purchase page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "purchase", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "header-197\x00-第二" {
			t.Fatal("second Purchase page changed its embedded-NUL boundary", second, err)
		}
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if err != nil || coverage.Headers != int64(len(ids)) || coverage.ActiveLines != 0 || coverage.VoidedLines != 0 {
			t.Fatal("multi-page Purchase headers changed exact identities", coverage, err)
		}
	})
	requirePurchaseCommandState(t, s, before)
}

func TestSyncPurchaseCoverageAcceptsMaximumWidthRetainedFields(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	id := strings.Repeat("😀", 128)
	seedSyncPurchaseCoverageHeaders(t, s, []string{id})
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Maximum financial source"})
	seedSyncPurchaseCoverageLines(t, s, id, item.ID, []string{id})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	merchant, note := strings.Repeat("😀", 300), strings.Repeat("😀", 10000)
	execSyncCoverageSQL(t, tx, "UPDATE purchases SET merchant_name=?,receipt_note=? WHERE id=?", merchant, note, id)
	execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_lines_no_update")
	execSyncCoverageSQL(t, tx, "UPDATE purchase_lines SET note=? WHERE id=?", note, id)
	execSyncCoverageSQL(t, tx, `UPDATE current_projections SET payload_json=(SELECT json_object('id',id,'merchant_name',merchant_name,'purchased_at',purchased_at,'currency',currency,'receipt_note',receipt_note) FROM purchases WHERE id=?) WHERE entity_type='purchase' AND entity_id=?`, id, id)
	execSyncCoverageSQL(t, tx, `UPDATE current_projections SET payload_json=(SELECT json_object('id',id,'purchase_id',purchase_id,'item_id',item_id,'item_unit_id',item_unit_id,'quantity_base',quantity_base,'paid_minor',paid_minor,'note',note) FROM purchase_lines WHERE id=?) WHERE entity_type='purchase_line' AND entity_id=?`, id, id)
	if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
	if err != nil || coverage.Headers != 1 || coverage.ActiveLines != 1 || coverage.VoidedLines != 0 {
		t.Fatal("maximum-width retained Purchase source was rejected", coverage, err)
	}
}

func TestSyncPurchaseCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, kind := range []string{"purchase", "purchase_line"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", kind, len(id)), func(t *testing.T) {
				s, catalog, _, token := catalogFixture(t)
				if kind == "purchase" {
					seedSyncPurchaseCoverageHeaders(t, s, []string{id})
				} else {
					seedSyncPurchaseCoverageHeaders(t, s, []string{"parent"})
					item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid financial identity"})
					seedSyncPurchaseCoverageLines(t, s, "parent", item.ID, []string{id})
				}
				withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
					coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
					if !errors.Is(err, ErrDomainState) || coverage != (syncPurchaseCoverage{}) {
						t.Fatal("invalid financial source identity was skipped", coverage, err)
					}
				})
			})
		}
	}
}

func TestSyncPurchaseCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture, _ := purchaseCommands(t)
	before := purchaseCommandState(t, fixture.store)
	for _, target := range []struct{ kind, id string }{{"purchase", fixture.purchase.ID}, {"purchase_line", fixture.stockLine.ID}} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", oversized, target.kind, target.id)
			coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncPurchaseCoverage{}) {
				t.Fatal("oversized Purchase projection returned coverage", coverage, err)
			}
		})
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestSyncPurchaseCoverageWithholdsOversizedCorrectionContext(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "coverage-large-correction", fixture.purchase.ID, fixture.purchase.Revision, reviewedLineVoid(t, fixture, fixture.unitLine)); err != nil {
		t.Fatal(err)
	}
	before := purchaseCommandState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DROP TRIGGER purchase_line_corrections_no_update")
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE purchase_line_corrections SET context_json=? WHERE line_id=?", oversized, fixture.unitLine.ID)
		coverage, err := verifySyncPurchaseCoverage(t.Context(), tx)
		if !errors.Is(err, ErrDomainState) || coverage != (syncPurchaseCoverage{}) {
			t.Fatal("oversized correction context returned coverage", coverage, err)
		}
	}()
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestSyncPurchaseCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncPurchaseCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncPurchaseCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncPurchaseCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncPurchaseCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncPurchaseCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncPurchaseCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncPurchaseCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncPurchaseCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
