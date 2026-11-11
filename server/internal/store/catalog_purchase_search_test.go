package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPurchaseSearchReadsEveryCurrentParentAndCorrection(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	s := fixture.store
	first := seedReadPurchase(t, s, PurchaseData{ID: "search-first", MerchantName: "FirstMerchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY", ReceiptNote: "FirstReceipt"})
	last := seedReadPurchase(t, s, PurchaseData{ID: "search-last", MerchantName: "LastMerchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY", ReceiptNote: "LastReceipt"})
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "a-search-active", PurchaseID: first.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, Note: "FirstLine"})
	voided := seedReadPurchaseLine(t, s, PurchaseLineData{ID: "z-search-void", PurchaseID: last.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, Note: "LastLine"})
	entry, err := purchases.CorrectLine(t.Context(), fixture.token, "void-last-search-line", last.ID, last.Revision, reviewedLineVoid(t, fixture, voided))
	if err != nil {
		t.Fatal(err)
	}

	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	text, err := purchaseSearchText(t.Context(), tx, fixture.quantity.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"FirstMerchant", "FirstReceipt", "FirstLine", "OriginalMerchant", "OriginalStockLine"} {
		if !strings.Contains(text, value) {
			t.Fatal("active purchase evidence was omitted", value)
		}
	}
	for _, value := range []string{"LastMerchant", "LastReceipt", "LastLine"} {
		if strings.Contains(text, value) {
			t.Fatal("voided purchase evidence was published", value)
		}
	}
	if _, err = tx.ExecContext(t.Context(), "UPDATE purchases SET merchant_name='ChangedMerchant' WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	text, err = purchaseSearchText(t.Context(), tx, fixture.quantity.ID, "")
	if err != nil || !strings.Contains(text, "ChangedMerchant") || strings.Contains(text, "FirstMerchant") {
		t.Fatal("a later search reused stale source values", err)
	}
	if _, err = tx.ExecContext(t.Context(), "DROP TRIGGER purchase_line_corrections_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "UPDATE purchase_line_corrections SET context_json=json_remove(context_json,'$.before.item_unit_id') WHERE mutation_event_id=?", entry.ID); err != nil {
		t.Fatal(err)
	}
	if text, err = purchaseSearchText(t.Context(), tx, fixture.quantity.ID, ""); !errors.Is(err, ErrDomainState) || text != "" {
		t.Fatal("a corrupt later correction exposed an earlier successful prefix", err)
	}
}

func TestPurchaseSearchOnlyQueriesRelatedSourcesForActualLines(t *testing.T) {
	for _, table := range []string{"purchases", "items", "purchase_line_corrections"} {
		t.Run(table, func(t *testing.T) {
			fixture := purchasesFixture(t)
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			unavailable := "unavailable_search_" + table
			if _, err = tx.ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO "+unavailable); err != nil {
				t.Fatal(err)
			}
			if text, err := purchaseSearchText(t.Context(), tx, "no-matching-item", ""); err != nil || text != "" {
				t.Fatal("an empty source unexpectedly required related lookups", err)
			}
			if text, err := purchaseSearchText(t.Context(), tx, fixture.quantity.ID, ""); err == nil || text != "" {
				t.Fatal("an unavailable related source exposed purchase evidence", err)
			}
			if _, err = tx.ExecContext(t.Context(), "ALTER TABLE "+unavailable+" RENAME TO "+table); err != nil {
				t.Fatal("failed search left its prepared statements open", err)
			}
			if text, err := purchaseSearchText(t.Context(), tx, fixture.quantity.ID, ""); err != nil || !strings.Contains(text, "OriginalStockLine") {
				t.Fatal("the same transaction could not read restored authority", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if text, err := purchaseSearchText(ctx, tx, fixture.quantity.ID, ""); !errors.Is(err, context.Canceled) || text != "" {
				t.Fatal("a canceled purchase search returned evidence", err)
			}
		})
	}
}
