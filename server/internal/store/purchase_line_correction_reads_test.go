package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPurchaseLineCorrectionHistoryPagesAreCompleteAcrossLargeVoidGapsAndKeepIndependentCursors(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	const count = 205
	lines := make([]PurchaseLine, 0, count)
	for index := range count {
		lines = append(lines, seedReadPurchaseLine(t, fixture.store, PurchaseLineData{ID: fmt.Sprintf("gap-%03d", index), PurchaseID: fixture.purchase.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: 0}))
	}
	for index, line := range lines {
		input := PurchaseLineCorrectionCreate{LineID: line.ID, LineRevision: line.Revision, ItemRevision: fixture.quantity.Revision + int64(index), Action: "void", Reason: "Explicit reviewed historical void"}
		if _, err := purchases.CorrectLine(t.Context(), fixture.token, fmt.Sprintf("actual-large-void-%03d", index), fixture.purchase.ID, fixture.purchase.Revision+int64(index), input); err != nil {
			t.Fatal("complete large history failed at an actual producer", index, err)
		}
	}
	before := purchaseCommandState(t, fixture.store)
	first, err := purchases.PageLines(t.Context(), fixture.token, fixture.purchase.ID, "", 1)
	if err != nil || len(first.Data) != 1 || first.Data[0].ID != fixture.stockLine.ID || first.NextCursor == nil {
		t.Fatal("raw void gaps fabricated an empty/terminal first current page", first, err)
	}
	last, err := purchases.PageLines(t.Context(), fixture.token, fixture.purchase.ID, *first.NextCursor, 1)
	if err != nil || len(last.Data) != 1 || last.Data[0].ID != fixture.unitLine.ID || last.NextCursor != nil {
		t.Fatal("visible continuation skipped or resurrected a raw Line", last, err)
	}
	var entries []PurchaseLineCorrection
	seen, cursor := make(map[string]bool), ""
	for {
		page, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, cursor, 50)
		if err != nil || page.Data == nil || len(page.Data) > 50 {
			t.Fatal("correction history lost its complete live collection", err)
		}
		for _, value := range page.Data {
			if value.Revision != 1 || value.After != nil || len(entries) > 0 && value.ID <= entries[len(entries)-1].ID {
				t.Fatal("history synthesized proofs/replacements or repeated/reordered identities")
			}
			entries = append(entries, value)
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Data) != 50 || len(*page.NextCursor) > 2048 || seen[*page.NextCursor] {
			t.Fatal("history truncated a live page or repeated/exceeded a signed continuation")
		}
		seen[*page.NextCursor], cursor = true, *page.NextCursor
	}
	if len(entries) != count || len(seen) != 4 {
		t.Fatal("history stopped at an implicit first hundred", len(entries), len(seen))
	}
	page, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 1)
	if err != nil || page.NextCursor == nil {
		t.Fatal(err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
	other := seedReadPurchase(t, fixture.store, PurchaseData{ID: "another-real-owner", MerchantName: "Another", PurchasedAt: fixture.purchase.PurchasedAt, Currency: "JPY"})
	before = purchaseCommandState(t, fixture.store)
	for _, token := range []string{*first.NextCursor, "x" + *page.NextCursor, "bad.cursor", *page.NextCursor + ".extra"} {
		if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, token, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("foreign/modified/ambiguous cursor entered correction history", err)
		}
	}
	if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, other.ID, *page.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("correction continuation crossed its actual owner", err)
	}
	if value, err := purchases.PageLines(t.Context(), fixture.token, fixture.purchase.ID, *page.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("history token was reused as current Line continuation", err)
	}
	if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, *page.NextCursor, 2); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed page size reused a correction continuation", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestPurchaseLineCorrectionCorruptActualContextNeverResurrectsCurrentFinancialOrSearchEvidence(t *testing.T) {
	for _, defect := range []string{"missing after", "missing replacement Item", "missing replacement decision", "missing replacement nullable Unit", "missing after nullable Unit", "unknown field", "wrong original interpretation", "wrong replacement proof", "native reason", "native time", "original counter", "missing replacement", "missing replacement counter", "unadvanced Purchase", "unadvanced original Item", "unadvanced replacement Item"} {
		t.Run(defect, func(t *testing.T) {
			fixture, purchases, assets, _ := purchaseAssetFixture(t)
			s := fixture.store
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			input.Action = "replace"
			input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 13}
			entry, err := purchases.CorrectLine(t.Context(), fixture.token, "actual-context-before-corruption", fixture.purchase.ID, fixture.purchase.Revision, input)
			if err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "DROP TRIGGER purchase_line_corrections_no_update")
			switch defect {
			case "missing after", "missing replacement Item", "missing replacement decision", "missing replacement nullable Unit", "missing after nullable Unit":
				path := map[string]string{"missing after": "$.after", "missing replacement Item": "$.replacement_item", "missing replacement decision": "$.input.replacement", "missing replacement nullable Unit": "$.input.replacement.item_unit_id", "missing after nullable Unit": "$.after.item_unit_id"}[defect]
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET context_json=json_remove(context_json,?)", path)
			case "unknown field":
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET context_json=json_set(context_json,'$.unexpected',1)")
			case "wrong original interpretation":
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET context_json=json_set(context_json,'$.item.scale_factor',2)")
			case "wrong replacement proof":
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET context_json=json_set(context_json,'$.input.replacement.item_revision',999)")
			case "native reason":
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET reason='Different exact reason'")
			case "native time":
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET created_at='2026-11-07T00:00:00Z'")
			case "original counter":
				setCatalogProjectionRevision(t, s, "purchase_line", fixture.unitLine.ID, fixture.unitLine.Revision)
			case "missing replacement":
				execSQL(t, s.db, "DROP TRIGGER purchase_lines_no_delete")
				execSQL(t, s.db, "PRAGMA foreign_keys=OFF")
				execSQL(t, s.db, "DELETE FROM purchase_lines WHERE id=?", entry.After.ID)
				execSQL(t, s.db, "PRAGMA foreign_keys=ON")
			case "missing replacement counter":
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", entry.After.ID)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id=?", entry.After.ID)
			case "unadvanced Purchase":
				setCatalogProjectionRevision(t, s, "purchase", fixture.purchase.ID, fixture.purchase.Revision)
			case "unadvanced original Item":
				setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, input.ItemRevision)
			case "unadvanced replacement Item":
				setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, input.Replacement.ItemRevision)
			}
			before := assetProfileState(t, s)
			for _, read := range []struct {
				name string
				run  func() (any, error)
			}{
				{"history", func() (any, error) {
					return purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 50)
				}},
				{"current Lines", func() (any, error) {
					return purchases.PageLines(t.Context(), fixture.token, fixture.purchase.ID, "", 50)
				}},
				{"original Item association", func() (any, error) {
					return purchases.PageForItem(t.Context(), fixture.token, fixture.individual.ID, "", 50)
				}},
				{"actual acquisition", func() (any, error) {
					return assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
				}},
				{"catalog evidence", func() (any, error) {
					return fixture.catalog.Page(t.Context(), fixture.token, "", 50, ItemFilters{Merchant: fixture.purchase.MerchantName})
				}},
				{"exact Unit evidence", func() (any, error) {
					return assets.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName})
				}},
			} {
				value, err := read.run()
				if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid/missing referenced context resurrected or exposed a successful prefix", defect, read.name, err)
				}
				requireAssetProfileState(t, s, before)
			}
		})
	}
}

func TestPurchaseLineCorrectionHistoryAdmissionLookaheadAndFinalReadFailuresReturnNoPrefix(t *testing.T) {
	for _, failure := range []string{"lookahead", "missing owner", "table query", "signing", "session write", "ignored session write", "deferred commit", "late expiry"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			s := fixture.store
			for index, line := range []PurchaseLine{fixture.unitLine, fixture.stockLine} {
				input := reviewedLineVoid(t, fixture, line)
				if _, err := purchases.CorrectLine(t.Context(), fixture.token, fmt.Sprintf("two-actual-corrections-%d", index), fixture.purchase.ID, fixture.purchase.Revision+int64(index), input); err != nil {
					t.Fatal(err)
				}
			}
			switch failure {
			case "lookahead":
				execSQL(t, s.db, "DROP TRIGGER purchase_line_corrections_no_update")
				execSQL(t, s.db, "UPDATE purchase_line_corrections SET context_json='{}' WHERE mutation_event_id=(SELECT MAX(mutation_event_id) FROM purchase_line_corrections)")
			case "missing owner":
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='purchase'")
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='purchase'")
			case "table query":
				execSQL(t, s.db, "ALTER TABLE purchase_line_corrections RENAME TO unavailable_correction_source")
				defer execSQL(t, s.db, "ALTER TABLE unavailable_correction_source RENAME TO purchase_line_corrections")
			case "signing":
				fixture.auth.serverSecret = nil
			case "session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_correction_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT,'read activity failed'); END")
			case "ignored session write":
				execSQL(t, s.db, "CREATE TRIGGER fail_correction_read BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END")
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE correction_read_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_correction_read AFTER UPDATE ON sessions BEGIN INSERT INTO correction_read_guard VALUES('missing'); END")
			case "late expiry":
				base, calls := fixture.auth.now(), 0
				fixture.auth.now = func() time.Time {
					calls++
					if calls > 2 {
						return base.Add(31 * 24 * time.Hour)
					}
					return base
				}
			}
			if failure == "table query" {
				if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 1); err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("failed source query exposed a history prefix", err)
				}
				return
			}
			before := purchaseCommandState(t, s)
			if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 1); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed lookahead/final read released a valid history prefix", failure, err)
			}
			requirePurchaseCommandState(t, s, before)
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM correction_read_guard") != 0 {
				t.Fatal("failed read retained its deferred guard row")
			}
		})
	}
	fixture, purchases := purchaseCommands(t)
	before := purchaseCommandState(t, fixture.store)
	for _, size := range []int{-1, 0, 101} {
		if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", size); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid history page size was admitted", err)
		}
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid correction owner was admitted", err)
		}
	}
	if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, "absent", "", 50); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("missing owner became an empty successful history", err)
	}
	if value, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, strings.Repeat("x", 2049), 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("history lost the original 2048-byte cursor bound", err)
	}
	page, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 50)
	encoded, _ := json.Marshal(page)
	if err != nil || page.Data == nil || page.NextCursor != nil || string(encoded) != `{"data":[],"next_cursor":null}` {
		t.Fatal("empty actual history lost required explicit null/empty collection", string(encoded), err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}
