package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPurchaseReadAdmissionAndMissingOwnersNeverExposeEmptySuccessForInvalidAuthority(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	seedPurchaseReadContinuation(t, fixture)
	before := purchaseCommandState(t, s)
	for _, kind := range []string{"global", "item", "lines"} {
		for _, size := range []int{-1, 0, 101} {
			if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", size); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("unbounded purchase page size was admitted", kind, err)
			}
		}
		if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, strings.Repeat("x", 2049), 1); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("purchase cursor lost its 2048-byte bound", err)
		}
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if page, err := p.PageForItem(t.Context(), fixture.token, id, "", 1); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid Item owner was admitted", err)
		}
		if page, err := p.PageLines(t.Context(), fixture.token, id, "", 1); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid Purchase owner was admitted", err)
		}
	}
	if page, err := p.PageForItem(t.Context(), fixture.token, "absent", "", 1); !errors.Is(err, ErrNotFound) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("missing Item became an empty associated collection", err)
	}
	if page, err := p.PageLines(t.Context(), fixture.token, "absent", "", 1); !errors.Is(err, ErrNotFound) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("missing Purchase became an empty allocation collection", err)
	}
	for _, filters := range []PurchaseFilters{
		{From: "2026-2-03"}, {From: "0000-01-01"}, {To: "10000-01-01"}, {From: "2026-11-07", To: "2026-11-06"}, {From: "2026-11-06T00:00:00Z"},
		{Currency: "jpy"}, {Currency: "JPY\n"}, {Currency: "日本円"}, {CategoryID: strings.Repeat("界", 129)}, {LocationID: string([]byte{0xff})},
		{Status: "stored"}, {Status: "all"}, {Scope: "ALL"}, {Merchant: strings.Repeat("😀", 301)}, {Merchant: string([]byte{0xff})},
	} {
		if page, err := p.Page(t.Context(), fixture.token, "", 1, filters); !errors.Is(err, ErrDomainInput) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid date/filter was guessed or widened", err)
		}
	}
	requirePurchaseCommandState(t, s, before)
	for _, owner := range []string{"Item", "Purchase"} {
		key := projectionKey{"item", fixture.quantity.ID}
		if owner == "Purchase" {
			key = projectionKey{"purchase", fixture.purchase.ID}
		}
		execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		before = purchaseCommandState(t, s)
		var err error
		if owner == "Item" {
			_, err = p.PageForItem(t.Context(), fixture.token, fixture.quantity.ID, "", 1)
		} else {
			_, err = p.PageLines(t.Context(), fixture.token, fixture.purchase.ID, "", 1)
		}
		if !errors.Is(err, ErrDomainState) {
			t.Fatal("missing original owner counter was seeded as revision one", err)
		}
		requirePurchaseCommandState(t, s, before)
	}
}

func TestPurchaseReadCorruptLookaheadWithholdsWholeHeaderAndLinePagesWithoutRepair(t *testing.T) {
	for _, kind := range []string{"global", "item", "lines"} {
		for _, defect := range []string{"missing counter", "unsafe counter", "invalid source"} {
			t.Run(kind+"/"+defect, func(t *testing.T) {
				fixture, p := purchaseCommands(t)
				s := fixture.store
				seedPurchaseReadContinuation(t, fixture)
				key := projectionKey{"purchase", "zz-continuation"}
				if kind == "lines" {
					key = projectionKey{"purchase_line", fixture.unitLine.ID}
				}
				switch defect {
				case "missing counter":
					execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
				case "unsafe counter":
					execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
					execSQL(t, s.db, "UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", maxSafeInteger+1, key.kind, key.id)
				case "invalid source":
					if kind == "lines" {
						execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,note) VALUES('t-invalid-line',?,?,1,0,CAST(x'ff' AS TEXT))", fixture.purchase.ID, fixture.quantity.ID)
						execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase_line','t-invalid-line',1)")
					} else {
						execSQL(t, s.db, "UPDATE purchases SET purchased_at='2026-11-06' WHERE id='zz-continuation'")
					}
				}
				before := purchaseCommandState(t, s)
				if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", 1); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("invalid lookahead exposed a valid first result/cursor", kind, defect, err)
				}
				requirePurchaseCommandState(t, s, before)
			})
		}
	}
}

func TestPurchaseReadAssociatedAndFilteredHeadersValidateEveryMatchingLineAndRealRelatedCounter(t *testing.T) {
	for _, defect := range []string{"late matching Line", "Item counter", "Unit counter", "foreign Unit"} {
		t.Run(defect, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			switch defect {
			case "late matching Line":
				seedReadPurchaseLine(t, s, PurchaseLineData{ID: "zz-matching-line", PurchaseID: fixture.purchase.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: 0})
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id='zz-matching-line'")
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id='zz-matching-line'")
			case "Item counter":
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
			case "Unit counter":
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
			case "foreign Unit":
				seedReadPurchaseLine(t, s, PurchaseLineData{ID: "zz-matching-line", PurchaseID: fixture.purchase.ID, ItemID: fixture.quantity.ID, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0})
			}
			before := purchaseCommandState(t, s)
			if page, err := p.Page(t.Context(), fixture.token, "", 50, PurchaseFilters{Status: "active"}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("filtered header accepted a corrupt matching authority set", defect, err)
			}
			owner := fixture.quantity.ID
			if defect == "Unit counter" {
				owner = fixture.individual.ID
			}
			if page, err := p.PageForItem(t.Context(), fixture.token, owner, "", 50); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("associated header trusted an invalid matching Line/Item/Unit", defect, err)
			}
			requirePurchaseCommandState(t, s, before)
		})
	}
}

func TestPurchaseReadSQLAndTimezoneFailuresAreClosedAndUnrelatedInvalidSourceStaysOutsideScope(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	seedPurchaseReadContinuation(t, fixture)
	for _, table := range []string{"purchases", "purchase_lines", "items", "app_settings"} {
		before := purchaseCommandState(t, s)
		execSQL(t, s.db, "ALTER TABLE "+table+" RENAME TO unreadable_purchase_source")
		for _, kind := range []string{"global", "item", "lines"} {
			// Global headers need no Line/Item join without Item criteria; owned
			// collections have no home-civil filters/settings dependency.
			if kind == "global" && (table == "items" || table == "purchase_lines") || kind != "global" && table == "app_settings" {
				continue
			}
			if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", 1); err == nil || page.Data != nil || page.NextCursor != nil {
				t.Fatal("failed source/settings query returned a successful prefix", table, kind, err)
			}
		}
		if table == "items" || table == "purchase_lines" {
			if page, err := p.Page(t.Context(), fixture.token, "", 1, PurchaseFilters{Status: "active"}); err == nil || page.Data != nil || page.NextCursor != nil {
				t.Fatal("failed matching-Line query was ignored", err)
			}
		}
		execSQL(t, s.db, "ALTER TABLE unreadable_purchase_source RENAME TO "+table)
		requirePurchaseCommandState(t, s, before)
	}
	for _, encoded := range []string{`null`, `123`, `[]`, `"Local"`, `"Unknown/Zone"`} {
		execSQL(t, s.db, "INSERT INTO app_settings(key,value_json,updated_at) VALUES('preferences.timezone',?,'2026') ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json", encoded)
		before := purchaseCommandState(t, s)
		if page, err := p.Page(t.Context(), fixture.token, "", 1, PurchaseFilters{}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("invalid home timezone was replaced with a default", err)
		}
		for _, kind := range []string{"item", "lines"} {
			if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", 1); err != nil || len(page.Data) != 1 {
				t.Fatal("unfiltered owned collection acquired an uncontracted timezone prerequisite", err)
			}
		}
		requirePurchaseCommandState(t, s, before)
	}
	setCatalogTimezone(t, s, "UTC")
	execSQL(t, s.db, "UPDATE purchases SET purchased_at='unknown' WHERE id='zz-continuation'")
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor) VALUES('unrelated-invalid-counter','zz-continuation',?,1,0)", fixture.individual.ID)
	before := purchaseCommandState(t, s)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: fixture.purchase.MerchantName}, 1), fixture.purchase.ID)
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseReadFinalAuthorizationSigningSessionWriteAndCommitFailuresWithholdAllCollections(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "session write", "deferred commit", "late expiry", "signing key"} {
		t.Run(failure, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			seedPurchaseReadContinuation(t, fixture)
			ctx := t.Context()
			want := ErrAuthentication
			switch failure {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='other-purchase-read-epoch'")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_purchase_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'purchase read failure'); END")
				want = nil
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE purchase_read_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_purchase_read AFTER UPDATE ON sessions BEGIN INSERT INTO purchase_read_commit_guard VALUES('missing'); END")
				want = nil
			case "signing key":
				fixture.auth.serverSecret = nil
				want = ErrDomainState
			}
			base := fixture.auth.now()
			before := purchaseCommandState(t, s)
			for _, kind := range []string{"global", "item", "lines"} {
				if failure == "late expiry" {
					calls := 0
					fixture.auth.now = func() time.Time {
						calls++
						if calls > 2 {
							return base.Add(31 * 24 * time.Hour)
						}
						return base
					}
				}
				if page, err := readPurchaseCollection(ctx, fixture, p, kind, "", 1); err == nil || want != nil && !errors.Is(err, want) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("failed final read returned a successful source/cursor prefix", failure, kind, err)
				}
				requirePurchaseCommandState(t, s, before)
			}
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_read_commit_guard") != 0 {
				t.Fatal("failed read COMMIT leaked a deferred row")
			}
		})
	}
}

func TestPurchaseReadOwnerAndMatchingLookupsUseActualIndexesWithoutInventingSource(t *testing.T) {
	fixture, _ := purchaseCommands(t)
	criteria, args := purchaseLineCriteria(ItemFilters{Scope: "subtree"}, fixture.quantity.ID)
	for _, query := range []struct {
		sql, index string
		args       []any
	}{
		{purchaseLineSelect + " WHERE l.purchase_id=? AND l.id>? ORDER BY l.id LIMIT ?", "idx_purchase_lines_purchase_id", []any{fixture.purchase.ID, "", 51}},
		{itemFilterCTEs + purchaseSelect + " WHERE p.id>? AND EXISTS(SELECT 1 FROM purchase_lines l JOIN items i ON i.id=l.item_id WHERE l.purchase_id=p.id AND " + criteria + ") ORDER BY p.id", "idx_purchase_lines_item_purchase", append([]any{"", "", ""}, args...)},
	} {
		rows, err := fixture.store.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query.sql, query.args...)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			found = found || strings.Contains(detail, query.index)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil || !found {
			t.Fatal("actual owner/criteria query missed its bounded source index", query.index, err)
		}
	}
	before := purchaseCommandState(t, fixture.store)
	_, err := readDomain(t.Context(), fixture.auth, fixture.token, func(tx *sql.Tx) (bool, error) {
		return false, validatePurchaseCriteria(t.Context(), tx, fixture.purchase.ID, ItemFilters{}, "l.item_id='absent'", nil)
	})
	if !errors.Is(err, ErrDomainState) {
		t.Fatal("an internally inconsistent matching set was treated as evidence", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}
