package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncOwnedCatalogCoveragePreservesSharedAndLegacyBarcodes(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	first := createCatalogItem(t, catalog, token, ItemCreate{Name: "Barcode first owner"})
	second := createCatalogItem(t, catalog, token, ItemCreate{Name: "Barcode second owner"})
	firstBarcode, err := catalog.CreateBarcode(t.Context(), token, "owned-coverage-barcode-first", first.ID, first.Revision, BarcodeWrite{Symbology: "OTHER", RawValue: "Shared exact code"})
	if err != nil {
		t.Fatal(err)
	}
	secondBarcode, err := catalog.CreateBarcode(t.Context(), token, "owned-coverage-barcode-second", second.ID, second.Revision, BarcodeWrite{Symbology: "OTHER", RawValue: "Shared exact code"})
	if err != nil || firstBarcode.ID == secondBarcode.ID || firstBarcode.NormalizedValue != secondBarcode.NormalizedValue {
		t.Fatal("fixture did not preserve independently owned shared barcode IDs", err)
	}
	first = getCatalogItem(t, catalog, token, first.ID)
	if _, err = catalog.CreateAlias(t.Context(), token, "owned-coverage-alias", first.ID, first.Revision, "Exact alias"); err != nil {
		t.Fatal(err)
	}
	first = getCatalogItem(t, catalog, token, first.ID)
	if _, err = catalog.CreateAttribute(t.Context(), token, "owned-coverage-attribute", first.ID, first.Revision, AttributeWrite{AttributeKey: "Exact key", AttributeValue: ""}); err != nil {
		t.Fatal(err)
	}
	first = getCatalogItem(t, catalog, token, first.ID)
	removed, err := catalog.CreateAlias(t.Context(), token, "owned-coverage-removed", first.ID, first.Revision, "Removed alias")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.DeleteAlias(t.Context(), token, "owned-coverage-delete", removed.ID, removed.Revision); err != nil {
		t.Fatal(err)
	}
	// Retained barcode values need not satisfy today's new-write checksum policy.
	execSQL(t, s.db, "INSERT INTO item_barcodes VALUES('legacy-barcode',?,'EAN_13','legacy raw','legacy normalized','2026')", first.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('barcode','legacy-barcode',1)")
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('barcode','legacy-barcode',1,json_object('id','legacy-barcode','item_id',?,'symbology','EAN_13','raw_value','legacy raw','normalized_value','legacy normalized'),'2026')`, first.ID)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if err != nil || coverage.Barcodes != 3 || coverage.Aliases != 1 || coverage.Attributes != 1 {
			t.Fatal("independent and retained catalog links were not covered", coverage, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("owned catalog coverage enabled public synchronization", err)
	}
}

func TestSyncOwnedCatalogCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owned damage source"})
	barcode, err := catalog.CreateBarcode(t.Context(), token, "owned-damage-barcode", item.ID, item.Revision, BarcodeWrite{Symbology: "OTHER", RawValue: "ExactCode"})
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	alias, err := catalog.CreateAlias(t.Context(), token, "owned-damage-alias", item.ID, item.Revision, "Exact alias")
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	attribute, err := catalog.CreateAttribute(t.Context(), token, "owned-damage-attribute", item.ID, item.Revision, AttributeWrite{AttributeKey: "Key", AttributeValue: "Value"})
	if err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	for _, target := range []struct{ kind, id, field, invalidField, table string }{
		{"barcode", barcode.ID, "raw_value", "raw_value", "item_barcodes"},
		{"alias", alias.ID, "alias", "alias", "item_aliases"},
		{"attribute", attribute.ID, "attribute_value", "attribute_key", "item_attributes"},
	} {
		for _, test := range []struct {
			name   string
			damage func(*testing.T, *sql.Tx)
			want   error
		}{
			{"missing current", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, errSyncOwnedCatalogUncovered},
			{"stale current", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,?, 'changed') WHERE entity_type=? AND entity_id=?", "$."+target.field, target.kind, target.id)
			}, ErrDomainState},
			{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, ErrDomainState},
			{"counter missing", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", target.kind, target.id)
				execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, ErrDomainState},
			{"invalid source field", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET "+target.invalidField+"='' WHERE id=?", target.id)
			}, ErrDomainState},
			{"missing owner", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
				execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET item_id='missing-owner' WHERE id=?", target.id)
				execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.item_id','missing-owner') WHERE entity_type=? AND entity_id=?", target.kind, target.id)
			}, ErrDomainState},
			{"orphan current", func(t *testing.T, tx *sql.Tx) {
				execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?, 'orphan-link',1)", target.kind)
				execSyncCoverageSQL(t, tx, "INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?, 'orphan-link',1,'{}','2026')", target.kind)
			}, ErrDomainState},
		} {
			t.Run(target.kind+"/"+test.name, func(t *testing.T) {
				tx, err := s.db.BeginTx(t.Context(), nil)
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
				coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
				if !errors.Is(err, test.want) || coverage != (syncOwnedCatalogCoverage{}) {
					t.Fatal("damaged catalog link returned coverage", coverage, err)
				}
			})
		}
	}
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
		execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID)
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if !errors.Is(err, ErrDomainState) || coverage != (syncOwnedCatalogCoverage{}) {
			t.Fatal("missing Item owner counter returned coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncOwnedCatalogCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owned boundary owner"})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if err != nil || coverage != (syncOwnedCatalogCoverage{}) {
			t.Fatal("empty owned catalog coverage failed", coverage, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncOwnedCatalogCoverage(t, s, owner.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if err != nil || coverage.Barcodes != 100 || coverage.Aliases != 100 || coverage.Attributes != 100 {
			t.Fatal("exact-boundary owned catalog coverage failed", coverage, err)
		}
	})
}

func TestSyncOwnedCatalogCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Large owned source"})
	seedSyncOwnedCatalogCoverage(t, s, owner.ID, []string{"large-row"})
	before := domainState(t, s)
	oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
	for _, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id='large-row'", oversized, kind)
			coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncOwnedCatalogCoverage{}) {
				t.Fatal("oversized owned projection returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncOwnedCatalogCoveragePagesExactIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owned link paging"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("link-%03d", index))
	}
	ids = append(ids, "link-098\x00-家庭", "link-197\x00-第二", strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncOwnedCatalogCoverage(t, s, owner.ID, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		for _, kind := range []string{"barcode", "alias", "attribute"} {
			first, err := readSyncCoverageIDs(t.Context(), tx, kind, "", false)
			if err != nil || len(first) != 100 || first[99] != "link-098\x00-家庭" {
				t.Fatal("the first owned-link page changed its embedded-NUL boundary", kind, first, err)
			}
			second, err := readSyncCoverageIDs(t.Context(), tx, kind, first[99], true)
			if err != nil || len(second) != 100 || second[99] != "link-197\x00-第二" {
				t.Fatal("the second owned-link page changed its embedded-NUL boundary", kind, second, err)
			}
		}
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if err != nil || coverage.Barcodes != int64(len(ids)) || coverage.Aliases != int64(len(ids)) || coverage.Attributes != int64(len(ids)) {
			t.Fatal("multi-page owned links changed identities", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='attribute' AND entity_id='zz-uncovered'")
		coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncOwnedCatalogUncovered) || coverage != (syncOwnedCatalogCoverage{}) {
			t.Fatal("late missing attribute returned partial coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncOwnedCatalogCoverage(t *testing.T, s *Store, owner string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		value := fmt.Sprintf("legacy-%03d", index)
		execSyncCoverageSQL(t, tx, "INSERT INTO item_barcodes VALUES(?,?,'EAN_13',?,?,'2026')", id, owner, value, value)
		execSyncCoverageSQL(t, tx, "INSERT INTO item_aliases VALUES(?,?,?,'2026')", id, owner, value)
		execSyncCoverageSQL(t, tx, "INSERT INTO item_attributes VALUES(?,?,?,'','2026')", id, owner, value)
		for _, kind := range []string{"barcode", "alias", "attribute"} {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id)
		}
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('barcode',?,1,json_object('id',?,'item_id',?,'symbology','EAN_13','raw_value',?,'normalized_value',?),'2026')`, id, id, owner, value, value)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('alias',?,1,json_object('id',?,'item_id',?,'alias',?),'2026')`, id, id, owner, value)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('attribute',?,1,json_object('id',?,'item_id',?,'attribute_key',?,'attribute_value',''),'2026')`, id, id, owner, value)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncOwnedCatalogCoverageRejectsInvalidSourceIDs(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid owned source owner"})
	seedSyncOwnedCatalogCoverage(t, s, owner.ID, []string{"existing"})
	before := domainState(t, s)
	for _, target := range []struct{ kind, table string }{{"barcode", "item_barcodes"}, {"alias", "item_aliases"}, {"attribute", "item_attributes"}} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", target.kind, len(id)), func(t *testing.T) {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				execSyncCoverageSQL(t, tx, "UPDATE "+target.table+" SET id=? WHERE id='existing'", id)
				coverage, err := verifySyncOwnedCatalogCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncOwnedCatalogCoverage{}) {
					t.Fatal("invalid owned identity was skipped", coverage, err)
				}
			})
		}
	}
	requireDomainState(t, s, before)
}

func TestSyncOwnedCatalogCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySyncOwnedCatalogCoverageEntry(t.Context(), tx, "not-a-kind", "id"); !errors.Is(err, ErrDomainState) {
		t.Fatal("unsupported owned catalog kind was accepted", err)
	}
	if value, err := verifySyncOwnedCatalogCoverage(nil, tx); !errors.Is(err, ErrDomainState) || value != (syncOwnedCatalogCoverage{}) {
		t.Fatal("nil context returned coverage", value, err)
	}
	if value, err := verifySyncOwnedCatalogCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || value != (syncOwnedCatalogCoverage{}) {
		t.Fatal("nil transaction returned coverage", value, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := verifySyncOwnedCatalogCoverage(ctx, tx); !errors.Is(err, context.Canceled) || value != (syncOwnedCatalogCoverage{}) {
		t.Fatal("canceled context returned coverage", value, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if value, err := verifySyncOwnedCatalogCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || value != (syncOwnedCatalogCoverage{}) {
		t.Fatal("closed transaction returned coverage", value, err)
	}
}
