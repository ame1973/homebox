package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Retained fixtures carry actual source/counters, never fictional command receipts.
func seedReadPurchase(t *testing.T, s *Store, data PurchaseData) Purchase {
	t.Helper()
	execSQL(t, s.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,?,?,'unknown-legacy-created')", data.ID, data.MerchantName, data.PurchasedAt, data.Currency, data.ReceiptNote)
	seedCatalogProjection(t, s, "purchase", data.ID, data)
	return Purchase{data, 1}
}
func seedReadPurchaseLine(t *testing.T, s *Store, data PurchaseLineData) PurchaseLine {
	t.Helper()
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note) VALUES(?,?,?,?,?,?,?)", data.ID, data.PurchaseID, data.ItemID, data.ItemUnitID, data.QuantityBase, data.PaidMinor, data.Note)
	seedCatalogProjection(t, s, "purchase_line", data.ID, data)
	return PurchaseLine{data, 1}
}
func allReadPurchases(t *testing.T, p *Purchases, token, itemID string, filters PurchaseFilters, size int) []Purchase {
	t.Helper()
	var result []Purchase
	cursor := ""
	seen := make(map[string]bool)
	for {
		var page PurchaseList
		var err error
		if itemID == "" {
			page, err = p.Page(t.Context(), token, cursor, size, filters)
		} else {
			page, err = p.PageForItem(t.Context(), token, itemID, cursor, size)
		}
		if err != nil || page.Data == nil || len(page.Data) > size {
			t.Fatal("incomplete purchase page", err)
		}
		for _, value := range page.Data {
			if len(result) > 0 && value.ID <= result[len(result)-1].ID {
				t.Fatal("purchase pages duplicated/reordered a header")
			}
			result = append(result, value)
		}
		if page.NextCursor == nil {
			return result
		}
		if seen[*page.NextCursor] || len(page.Data) != size || len(*page.NextCursor) > 2048 {
			t.Fatal("purchase continuation repeated/truncated/exceeded its original budget")
		}
		seen[*page.NextCursor] = true
		cursor = *page.NextCursor
	}
}
func allReadPurchaseLines(t *testing.T, p *Purchases, token, owner string, size int) []PurchaseLine {
	t.Helper()
	var result []PurchaseLine
	cursor := ""
	seen := make(map[string]bool)
	for {
		page, err := p.PageLines(t.Context(), token, owner, cursor, size)
		if err != nil || page.Data == nil || len(page.Data) > size {
			t.Fatal("incomplete allocation page", err)
		}
		for _, value := range page.Data {
			if len(result) > 0 && value.ID <= result[len(result)-1].ID {
				t.Fatal("allocation pages duplicated/reordered an identity")
			}
			result = append(result, value)
		}
		if page.NextCursor == nil {
			return result
		}
		if seen[*page.NextCursor] || len(page.Data) != size || len(*page.NextCursor) > 2048 {
			t.Fatal("allocation continuation repeated/truncated/exceeded its original budget")
		}
		seen[*page.NextCursor] = true
		cursor = *page.NextCursor
	}
}
func requirePurchaseIDs(t *testing.T, values []Purchase, want ...string) {
	t.Helper()
	got := make([]string, 0, len(values))
	for _, value := range values {
		got = append(got, value.ID)
	}
	if want == nil {
		want = []string{}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("purchase scope differs: got %q, want %q", got, want)
	}
}

func TestPurchaseReadsUseActualIndependentRetainedSourceWithoutRepricingOrCacheAuthority(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	execSQL(t, s.db, "UPDATE purchases SET currency='ZZZ' WHERE id=?", fixture.purchase.ID)
	execSQL(t, s.db, "INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,created_at,updated_at) VALUES(?,'manual','JPY',0,'legacy','legacy')", fixture.unit.ID)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"UntrustedFinancialCache\":true}' WHERE entity_type IN('purchase','purchase_line')")
	before := purchaseCommandState(t, s)
	want := fixture.purchase
	want.Currency = "ZZZ"
	for _, owner := range []string{"", fixture.individual.ID, fixture.quantity.ID} {
		values := allReadPurchases(t, p, fixture.token, owner, PurchaseFilters{}, 1)
		if len(values) != 1 || values[0] != want {
			t.Fatal("read repriced historical code, copied cache or lost source/counter")
		}
	}
	lines := allReadPurchaseLines(t, p, fixture.token, fixture.purchase.ID, 1)
	if !reflect.DeepEqual(lines, []PurchaseLine{fixture.stockLine, fixture.unitLine}) {
		t.Fatal("retained zero-paid/null/old bulk-Unit allocation was repaired or hidden")
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{LocationID: "source", Scope: "direct"}, 1), fixture.purchase.ID)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{LocationID: "root"}, 1), fixture.purchase.ID)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{LocationID: "target", Scope: "direct"}, 1))
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{LocationID: "root", Scope: "direct"}, 1))
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Currency: "ZZZ"}, 1), fixture.purchase.ID)
	for _, value := range []any{PurchaseList{Data: []Purchase{want}}, PurchaseLineList{Data: lines}} {
		encoded, err := json.Marshal(value)
		var envelope map[string]json.RawMessage
		if err != nil || json.Unmarshal(encoded, &envelope) != nil || len(envelope) != 2 || string(envelope["next_cursor"]) != "null" || strings.Contains(string(encoded), "created_at") || strings.Contains(string(encoded), "mutation_event_id") || strings.Contains(string(encoded), "item_revision") || strings.Contains(string(encoded), "UntrustedFinancialCache") {
			t.Fatal("page exposed private metadata or omitted its terminal cursor", err)
		}
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseReadsIncludeActualCommandResultsAndDistinctItemHeadersWithoutImplicitTotals(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	parent := createPurchaseCommand(t, p, fixture.token, "read-real-purchase-parent", "TWD")
	line, err := p.CreateLine(t.Context(), fixture.token, "read-real-purchase-allocation", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 17, PaidMinor: 0})
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	current, err := p.Get(t.Context(), fixture.token, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.CreateLine(t.Context(), fixture.token, "read-second-real-allocation", parent.ID, current.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 1234}); err != nil {
		t.Fatal(err)
	}
	before := purchaseCommandState(t, s)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{}, 1), parent.ID, fixture.purchase.ID)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, fixture.quantity.ID, PurchaseFilters{}, 1), parent.ID, fixture.purchase.ID)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, fixture.individual.ID, PurchaseFilters{}, 1), fixture.purchase.ID)
	lines := allReadPurchaseLines(t, p, fixture.token, parent.ID, 1)
	if len(lines) != 2 {
		t.Fatal("actual independent allocation disappeared")
	}
	found := false
	for _, value := range lines {
		if value.ID == line.ID {
			found = reflect.DeepEqual(value, line)
		}
	}
	if !found {
		t.Fatal("read changed a real command's independent Line/source")
	}
	for _, value := range allReadPurchases(t, p, fixture.token, fixture.quantity.ID, PurchaseFilters{}, 1) {
		if value.ID == parent.ID && value.Revision != 3 {
			t.Fatal("read invented header/proof-plus-one instead of the real aggregate")
		}
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseReadsFinishLargeRetainedCollectionsAndPreserveIndependentSafeCounters(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	for index := range 205 {
		id := fmt.Sprintf("read-purchase-%03d", index)
		seedReadPurchase(t, s, PurchaseData{ID: id, MerchantName: "Retained", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: id, PurchaseID: id, ItemID: fixture.quantity.ID, QuantityBase: 1500, PaidMinor: 0})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: fmt.Sprintf("read-owned-line-%03d", index), PurchaseID: fixture.purchase.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: maxSafeInteger})
	}
	setCatalogProjectionRevision(t, s, "purchase", "read-purchase-000", maxSafeInteger)
	setCatalogProjectionRevision(t, s, "purchase_line", "read-owned-line-000", maxSafeInteger)
	before := purchaseCommandState(t, s)
	for _, size := range []int{1, 50, 100} {
		global := allReadPurchases(t, p, fixture.token, "", PurchaseFilters{}, size)
		associated := allReadPurchases(t, p, fixture.token, fixture.quantity.ID, PurchaseFilters{}, size)
		lines := allReadPurchaseLines(t, p, fixture.token, fixture.purchase.ID, size)
		if len(global) != 206 || !reflect.DeepEqual(global, associated) || len(lines) != 207 {
			t.Fatal("large collection was truncated, duplicated or confused with another owner's lines")
		}
		found := false
		for _, value := range global {
			if value.ID == "read-purchase-000" {
				found = value.Revision == maxSafeInteger
			}
		}
		if !found || lines[0].Revision != maxSafeInteger {
			t.Fatal("read synthesized/reset an independent retained counter")
		}
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseReadsEmptyExistingOwnersAreNotMissingOrImplicitPlacement(t *testing.T) {
	fixture, p := purchaseCommands(t)
	item := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "No purchases"})
	parent := createPurchaseCommand(t, p, fixture.token, "empty-real-purchase", "JPY")
	before := purchaseCommandState(t, fixture.store)
	if page, err := p.PageForItem(t.Context(), fixture.token, item.ID, "", 50); err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("existing Item with no allocations became missing/invented history", err)
	}
	if page, err := p.PageLines(t.Context(), fixture.token, parent.ID, "", 50); err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("unallocated header became missing/invented allocation", err)
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: parent.MerchantName}, 1), parent.ID)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: parent.MerchantName, Status: "active"}, 1))
	requirePurchaseCommandState(t, fixture.store, before)
}
