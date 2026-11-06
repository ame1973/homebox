package store

import (
	"fmt"
	"testing"
)

func TestPurchaseFilteredReadsRequireOneMatchingLineForCategoryStatusAndCurrentPlacement(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	taxonomy, err := NewTaxonomy(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, fixture.token, "Purchase root", nil)
	child := createCategory(t, taxonomy, fixture.token, "Purchase child", &category.ID)
	otherCategory := createCategory(t, taxonomy, fixture.token, "Purchase other", nil)
	fixture.individual, err = fixture.catalog.Patch(t.Context(), fixture.token, "classify-purchase-individual", fixture.individual.ID, fixture.individual.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &category.ID}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.quantity, err = fixture.catalog.Patch(t.Context(), fixture.token, "classify-purchase-quantity", fixture.quantity.ID, fixture.quantity.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &child.ID}})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.CreateUnit(t.Context(), fixture.token, "purchase-sibling-target", fixture.individual.ID, fixture.individual.Revision, UnitCreate{LocationID: testPointer("target")}); err != nil {
		t.Fatal(err)
	}
	other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Archived other", CategoryID: &otherCategory.ID, Status: testPointer("archived")})
	otherUnit := UnitData{ID: "purchase-other-unit", ItemID: other.ID, LocationID: testPointer("target"), AssetCode: "OTHER-PURCHASE-UNIT", Status: "retired"}
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,?,?,'legacy','legacy')", otherUnit.ID, otherUnit.ItemID, otherUnit.LocationID, otherUnit.AssetCode, otherUnit.Status)
	seedCatalogProjection(t, s, "unit", otherUnit.ID, otherUnit)
	for _, row := range []struct{ id, merchant, code, instant string }{
		{"named-source", "Case Sensitive", "JPY", "2026-11-05T00:00:00Z"},
		{"unnamed-individual", "Exact Merchant  ", "USD", "2026-11-06T00:00:00Z"},
		{"quantity-source", "Case Sensitive", "JPY", "2026-11-05T00:00:00Z"},
		{"cross-line", "Case Sensitive", "TWD", "2026-11-05T00:00:00Z"},
		{"other-target", "Other", "JPY", "2026-11-04T00:00:00Z"},
		{"unallocated-header", "Empty merchant", "JPY", "2026-11-05T00:00:00Z"},
		{"child-quantity", "Case Sensitive", "TWD", "2026-11-05T00:00:00Z"},
	} {
		seedReadPurchase(t, s, PurchaseData{ID: row.id, MerchantName: row.merchant, PurchasedAt: row.instant, Currency: row.code})
	}
	for _, row := range []struct {
		id, parent, item string
		unit             *string
	}{
		{"named-source-line", "named-source", fixture.individual.ID, &fixture.unit.ID},
		{"unnamed-individual-line", "unnamed-individual", fixture.individual.ID, nil},
		{"quantity-source-line", "quantity-source", fixture.quantity.ID, nil},
		{"cross-category-line", "cross-line", fixture.individual.ID, &fixture.unit.ID},
		{"cross-placement-line", "cross-line", other.ID, &otherUnit.ID},
		{"other-target-line", "other-target", other.ID, &otherUnit.ID},
		{"child-quantity-line", "child-quantity", fixture.quantity.ID, nil},
	} {
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: row.id, PurchaseID: row.parent, ItemID: row.item, ItemUnitID: row.unit, QuantityBase: 1, PaidMinor: 0})
	}
	before := purchaseCommandState(t, s)
	for _, test := range []struct {
		name    string
		filters PurchaseFilters
		want    []string
	}{
		{"all including unallocated", PurchaseFilters{}, []string{"named-source", "unnamed-individual", "quantity-source", "cross-line", "other-target", "unallocated-header", "child-quantity", fixture.purchase.ID}},
		{"root category direct", PurchaseFilters{CategoryID: category.ID, Scope: "direct"}, []string{"named-source", "unnamed-individual", "cross-line", fixture.purchase.ID}},
		{"root category subtree", PurchaseFilters{CategoryID: category.ID}, []string{"named-source", "unnamed-individual", "quantity-source", "cross-line", "child-quantity", fixture.purchase.ID}},
		{"child category", PurchaseFilters{CategoryID: child.ID}, []string{"quantity-source", "child-quantity", fixture.purchase.ID}},
		{"other category", PurchaseFilters{CategoryID: otherCategory.ID}, []string{"cross-line", "other-target"}},
		{"actual source placement", PurchaseFilters{LocationID: "source", Scope: "direct"}, []string{"named-source", "unnamed-individual", "quantity-source", "cross-line", "child-quantity", fixture.purchase.ID}},
		{"named Unit not sibling", PurchaseFilters{LocationID: "target", Scope: "direct"}, []string{"unnamed-individual", "cross-line", "other-target"}},
		{"location subtree", PurchaseFilters{LocationID: "root"}, []string{"named-source", "unnamed-individual", "quantity-source", "cross-line", "other-target", "child-quantity", fixture.purchase.ID}},
		{"no implicit root placement", PurchaseFilters{LocationID: "root", Scope: "direct"}, nil},
		{"same Line category location", PurchaseFilters{CategoryID: category.ID, LocationID: "target", Scope: "direct"}, []string{"unnamed-individual"}},
		{"same Line subtree category location", PurchaseFilters{CategoryID: category.ID, LocationID: "target"}, []string{"unnamed-individual"}},
		{"zero balance not placement", PurchaseFilters{CategoryID: child.ID, LocationID: "target", Scope: "direct"}, nil},
		{"Item metadata archived", PurchaseFilters{Status: "archived"}, []string{"cross-line", "other-target"}},
		{"same Line category status", PurchaseFilters{CategoryID: category.ID, Status: "archived"}, nil},
		{"same Line location status", PurchaseFilters{LocationID: "source", Status: "archived"}, nil},
		{"combined header and Line", PurchaseFilters{From: "2026-11-05", To: "2026-11-05", Currency: "TWD", Merchant: "Case Sensitive", CategoryID: child.ID, LocationID: "source", Status: "active"}, []string{"child-quantity"}},
		{"exact merchant whitespace", PurchaseFilters{Merchant: "Exact Merchant  "}, []string{"unnamed-individual"}},
		{"no merchant trim", PurchaseFilters{Merchant: "Exact Merchant"}, nil},
		{"no merchant case fold", PurchaseFilters{Merchant: "case sensitive"}, nil},
		{"missing category", PurchaseFilters{CategoryID: "absent"}, nil},
		{"missing location", PurchaseFilters{LocationID: "absent"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", test.filters, 1), test.want...)
		})
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseFilteredReadsFollowRealDisposalAndStockChangesWithoutReconstructingHistoricalLocation(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, item string
		unit     *string
	}{
		{"live-named", fixture.individual.ID, &fixture.unit.ID}, {"live-unnamed", fixture.individual.ID, nil}, {"live-stock", fixture.quantity.ID, nil},
	} {
		seedReadPurchase(t, s, PurchaseData{ID: row.id, MerchantName: row.id, PurchasedAt: "2026-11-05T00:00:00Z", Currency: "JPY"})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: row.id, PurchaseID: row.id, ItemID: row.item, ItemUnitID: row.unit, QuantityBase: 1, PaidMinor: 0})
	}
	if _, err = inventory.CreateUnit(t.Context(), fixture.token, "live-other-unit-location", fixture.individual.ID, fixture.individual.Revision, UnitCreate{LocationID: testPointer("target")}); err != nil {
		t.Fatal(err)
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-named", LocationID: "source"}, 1), "live-named")
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-named", LocationID: "target"}, 1))
	if _, err = inventory.SetUnitStatus(t.Context(), fixture.token, "live-dispose-named-unit", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "disposed", Note: "Explicit physical disposal", DisposalKind: testPointer("recycled")}); err != nil {
		t.Fatal(err)
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-named", LocationID: "source"}, 1))
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-named"}, 1), "live-named")
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-unnamed", LocationID: "source"}, 1))
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-unnamed", LocationID: "target"}, 1), "live-unnamed")
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-stock", LocationID: "target"}, 1))
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	if _, err = inventory.Receive(t.Context(), fixture.token, "live-receive-target-stock", item.Revision, StockReceive{ItemID: item.ID, TargetLocationID: "target", QuantityBase: 17}); err != nil {
		t.Fatal(err)
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-stock", LocationID: "target"}, 1), "live-stock")
	item = getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	if _, err = inventory.Consume(t.Context(), fixture.token, "live-consume-target-stock", item.Revision, StockConsume{ItemID: item.ID, SourceLocationID: "target", QuantityBase: 17}); err != nil {
		t.Fatal(err)
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "live-stock", LocationID: "target"}, 1))
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM stock_balances WHERE item_id=? AND location_id='target' AND quantity_base=0", item.ID) != 1 {
		t.Fatal("filtering erased retained zero placement")
	}
}

func TestPurchaseFilteredReadsClassifyInclusiveHomeCivilDatesAcrossDSTRepeatedAndSkippedDays(t *testing.T) {
	for _, test := range []struct {
		zone, from, to string
		instants       []string
		want           []int
	}{
		{"UTC", "2026-11-05", "2026-11-05", []string{"2026-11-04T23:59:59.999999999Z", "2026-11-05T00:00:00Z", "2026-11-05T23:59:59.999999999Z", "2026-11-06T00:00:00Z"}, []int{1, 2}},
		{"Asia/Taipei", "2026-11-05", "2026-11-05", []string{"2026-11-04T15:59:59.999999999Z", "2026-11-04T16:00:00Z", "2026-11-05T23:59:59.999999999+08:00", "2026-11-05T16:00:00Z"}, []int{1, 2}},
		{"America/New_York", "2026-03-08", "2026-03-08", []string{"2026-03-08T04:59:59.999999999Z", "2026-03-08T05:00:00Z", "2026-03-09T03:59:59.999999999Z", "2026-03-09T04:00:00Z"}, []int{1, 2}},
		{"America/New_York", "2026-11-01", "2026-11-01", []string{"2026-11-01T03:59:59.999999999Z", "2026-11-01T04:00:00Z", "2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00", "2026-11-02T04:59:59.999999999Z", "2026-11-02T05:00:00Z"}, []int{1, 2, 3, 4}},
		{"Pacific/Apia", "2011-12-30", "2011-12-30", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z", "2011-12-31T23:59:59.999999999+14:00"}, nil},
		{"Pacific/Apia", "2011-12-31", "2011-12-31", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z", "2011-12-31T23:59:59.999999999+14:00"}, []int{1, 2}},
		{"America/Sao_Paulo", "2018-11-04", "2018-11-04", []string{"2018-11-04T02:59:59.999999999Z", "2018-11-04T03:00:00Z", "2018-11-05T01:59:59.999999999Z", "2018-11-05T02:00:00Z"}, []int{1, 2}},
	} {
		t.Run(test.zone+test.from, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			setCatalogTimezone(t, fixture.store, test.zone)
			for index, instant := range test.instants {
				seedReadPurchase(t, fixture.store, PurchaseData{ID: fmt.Sprintf("civil-%02d", index), MerchantName: "Civil date source", PurchasedAt: instant, Currency: "JPY"})
			}
			before := purchaseCommandState(t, fixture.store)
			var want []string
			for _, index := range test.want {
				want = append(want, fmt.Sprintf("civil-%02d", index))
			}
			requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "Civil date source", From: test.from, To: test.to}, 1), want...)
			requirePurchaseCommandState(t, fixture.store, before)
		})
	}
}

func TestPurchaseFilteredReadsFinishAllDateCandidatesBeforeSelectingACompletePage(t *testing.T) {
	fixture, p := purchaseCommands(t)
	for index := range 120 {
		seedReadPurchase(t, fixture.store, PurchaseData{ID: fmt.Sprintf("old-%03d", index), MerchantName: "Stream", PurchasedAt: "2025-11-06T00:00:00Z", Currency: "JPY"})
	}
	for index := range 3 {
		seedReadPurchase(t, fixture.store, PurchaseData{ID: fmt.Sprintf("zz-match-%03d", index), MerchantName: "Stream", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	}
	before := purchaseCommandState(t, fixture.store)
	for _, filters := range []PurchaseFilters{{Merchant: "Stream", From: "2026-11-06"}, {Merchant: "Stream", From: "2026-11-06", To: "2026-11-06"}} {
		requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", filters, 1), "zz-match-000", "zz-match-001", "zz-match-002")
	}
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "Stream", To: "2024-12-31"}, 1))
	requirePurchaseCommandState(t, fixture.store, before)
}
