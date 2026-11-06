package store

import (
	"errors"
	"testing"
	"time"
)

func TestPurchasePublicationRollsBackEverySourceProjectionSearchReceiptAndSessionStatement(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,revision,updated_at) VALUES(?,'OriginalPurchaseUnitCache',55,'2026')", fixture.unit.ID)
	now := fixture.auth.now().Add(time.Minute)
	fixture.auth.now = func() time.Time { return now }
	for _, failure := range []struct{ table, event string }{
		{"purchases", "UPDATE"}, {"purchase_lines", "INSERT"}, {"items", "UPDATE"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"},
		{"unit_search_documents", "INSERT"}, {"unit_search_documents", "UPDATE"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_purchase_statement BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'purchase publication failure'); END")
			if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-failed-statement", "Final"); err == nil || value != (Purchase{}) {
				t.Fatal("failed financial statement exposed partial success", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'FinalMerchant'") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'OriginalPurchaseUnitCache'") != 1 {
				t.Fatal("rolled-back purchase left partial approved FTS")
			}
			execSQL(t, s.db, "DROP TRIGGER fail_purchase_statement")
		})
	}
}

func TestPurchasePublicationLateAuthorizationAndDeferredCommitWithholdAllFinancialResults(t *testing.T) {
	for _, failure := range []string{"late revocation", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			fixture := purchasesFixture(t)
			s := fixture.store
			if failure == "late revocation" {
				execSQL(t, s.db, "CREATE TRIGGER fail_purchase_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			} else {
				execSQL(t, s.db, "CREATE TABLE purchase_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_purchase_late AFTER INSERT ON mutation_events BEGIN INSERT INTO purchase_commit_guard VALUES('missing'); END")
			}
			before := domainState(t, s)
			if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-failed-late", "Final"); err == nil || value != (Purchase{}) || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late authorization/COMMIT failure released a financial result", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts") != 0 {
				t.Fatal("failed final transaction left purchase evidence")
			}
		})
	}
}

func TestPurchasePublicationNeverReseedsMissingOriginalOrRelatedAuthority(t *testing.T) {
	for _, scope := range []string{"purchase", "unit line", "stock line", "individual Item", "quantity Item", "Unit"} {
		t.Run(scope, func(t *testing.T) {
			fixture := purchasesFixture(t)
			s := fixture.store
			key := projectionKey{"purchase", fixture.purchase.ID}
			switch scope {
			case "unit line":
				key = projectionKey{"purchase_line", fixture.unitLine.ID}
			case "stock line":
				key = projectionKey{"purchase_line", fixture.stockLine.ID}
			case "individual Item":
				key = projectionKey{"item", fixture.individual.ID}
			case "quantity Item":
				key = projectionKey{"item", fixture.quantity.ID}
			case "Unit":
				key = projectionKey{"unit", fixture.unit.ID}
			}
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			before := domainState(t, s)
			if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-missing-authority", "Final"); !errors.Is(err, ErrDomainState) || value != (Purchase{}) {
				t.Fatal("missing source authority became fictional revision one", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestPurchasePublicationExhaustionRollsBackAllocationsSearchHistoryAndReplies(t *testing.T) {
	for _, scope := range []string{"purchase", "individual Item", "quantity Item", "unit search", "item search", "catalog revision", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture := purchasesFixture(t)
			s := fixture.store
			key := projectionKey{"purchase", fixture.purchase.ID}
			switch scope {
			case "individual Item":
				key = projectionKey{"item", fixture.individual.ID}
			case "quantity Item":
				key = projectionKey{"item", fixture.quantity.ID}
			case "unit search":
				key = projectionKey{"search_doc", projectionPair("unit", fixture.unit.ID)}
				seedCatalogProjection(t, s, key.kind, key.id, searchData{EntityType: "unit", EntityID: fixture.unit.ID, ApprovedText: "Original"})
			case "item search":
				key = projectionKey{"search_doc", projectionPair("item", fixture.individual.ID)}
			}
			if scope == "catalog revision" {
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			} else if scope == "sequence" {
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			} else {
				setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
			}
			before := domainState(t, s)
			if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-exhausted-authority", "Final"); !errors.Is(err, ErrRevisionOverflow) || value != (Purchase{}) {
				t.Fatal("exhausted dependent counter released financial success", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestPurchaseAppendDoesNotAdvanceUnchangedUnitOrExistingImmutableLineCounters(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	for _, key := range []projectionKey{{"unit", fixture.unit.ID}, {"purchase_line", fixture.unitLine.ID}, {"purchase_line", fixture.stockLine.ID}} {
		setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
	}
	if value, err := mutatePurchaseProjectionFixture(t.Context(), fixture, "purchase-independent-counters", "Final"); err != nil || value.Revision != 8 {
		t.Fatal("unchanged independent maximum counter blocked a new allocation", err)
	}
	for _, key := range []projectionKey{{"unit", fixture.unit.ID}, {"purchase_line", fixture.unitLine.ID}, {"purchase_line", fixture.stockLine.ID}} {
		if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id) != maxSafeInteger {
			t.Fatal("purchase publication bumped unrelated immutable/Unit proof", key)
		}
	}
}

func TestPurchaseLineAuthorityWithholdsMissingRelatedSourceAndSQLFailures(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	for _, missing := range []string{"purchase", "item", "unit"} {
		t.Run(missing, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Deferral lets the private loader inspect an incomplete transaction;
			// no invalid reference is ever committed or production FK disabled.
			if _, err = tx.ExecContext(t.Context(), "PRAGMA defer_foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			purchase, item, unit := fixture.purchase.ID, fixture.individual.ID, fixture.unit.ID
			switch missing {
			case "purchase":
				purchase = "absent"
			case "item":
				item = "absent"
			case "unit":
				unit = "absent"
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('incomplete',?,?,?,1,0)", purchase, item, unit); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('purchase_line','incomplete',1)"); err != nil {
				t.Fatal(err)
			}
			if value, err := loadPurchaseLine(t.Context(), tx, "incomplete"); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
				t.Fatal("missing related source exposed a partial/404 Line", err)
			}
		})
	}
	for _, table := range []string{"purchases", "items", "item_units"} {
		t.Run(table+" query", func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO temporarily_unavailable_purchase_source"); err != nil {
				t.Fatal(err)
			}
			if value, err := loadPurchaseLine(t.Context(), tx, fixture.unitLine.ID); err == nil || value != (PurchaseLine{}) {
				t.Fatal("failed related source query exposed a financial DTO", err)
			}
		})
	}
	other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Other individual owner"})
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('wrong-individual',?,?,?,1,0)", fixture.purchase.ID, other.ID, fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase_line','wrong-individual',1)")
	if value, err := loadPurchaseLine(t.Context(), s.db, "wrong-individual"); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
		t.Fatal("same tracking mode hid a foreign Unit owner", err)
	}
}

func TestPurchaseProducerMetadataPairAndDeferredAuditReferenceCannotLeakUncommittedLines(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	before := domainState(t, s)
	for _, metadata := range []string{"NULL,'missing-audit'", "'2026-11-06T00:00:00Z',NULL", "'2026-11-06T00:00:00Z','missing-audit'"} {
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,created_at,mutation_event_id) VALUES('invalid-metadata',?,?,1,0,"+metadata+")", fixture.purchase.ID, fixture.quantity.ID); err == nil {
			t.Fatal("unpaired/noncommitted audit association accepted", metadata)
		}
		requireDomainState(t, s, before)
	}
}
